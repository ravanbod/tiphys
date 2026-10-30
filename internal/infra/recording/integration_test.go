package recording

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/minio/minio-go/v7"
	"go.uber.org/zap"

	"tiphys/internal/application"
)

// Opt-in integration test against a real MinIO:
//
//	eval "$(deploy/minio/test-minio.sh start)"
//	go test ./internal/infra/recording -run Integration -v
//	deploy/minio/test-minio.sh stop
//
// Skipped unless TIPHYS_TEST_MINIO_ENDPOINT is set.
func integrationStore(t *testing.T, mode string) (*MinIOStore, context.Context) {
	t.Helper()
	endpoint := os.Getenv("TIPHYS_TEST_MINIO_ENDPOINT")
	if endpoint == "" {
		t.Skip("TIPHYS_TEST_MINIO_ENDPOINT not set")
	}
	cfg := Config{
		Endpoint:      endpoint,
		AccessKey:     os.Getenv("TIPHYS_TEST_MINIO_ACCESS_KEY"),
		SecretKey:     os.Getenv("TIPHYS_TEST_MINIO_SECRET_KEY"),
		Secure:        os.Getenv("TIPHYS_TEST_MINIO_SECURE") == "true",
		Bucket:        fmt.Sprintf("rec-%d", time.Now().UnixNano()),
		RetentionMode: mode,
	}
	s, err := NewMinIOStore(cfg, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)

	if err := s.Healthy(ctx); err == nil {
		t.Fatal("Healthy passed for a bucket that does not exist")
	}
	s.checked = time.Time{} // drop the cached failure
	if err := s.EnsureBucket(ctx, 180); err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureBucket(ctx, 180); err != nil {
		t.Fatalf("second EnsureBucket: %v", err)
	}
	return s, ctx
}

func TestIntegrationRoundTripAndLock(t *testing.T) {
	s, ctx := integrationStore(t, "")
	if err := s.Healthy(ctx); err != nil {
		t.Fatalf("Healthy: %v", err)
	}

	prefix := Prefix("11111111-2222-3333-4444-555555555555", time.Now())
	retain := time.Now().Add(24 * time.Hour)
	header := `{"version":2,"width":80,"height":24}` + "\n"
	segs := []string{header + `[0.1,"o","a"]` + "\n", `[31.0,"o","b"]` + "\n", `[61.5,"o","c"]` + "\n"}
	// Write out of order and with a seq beyond 9 to check numeric ordering.
	order := []int{2, 0, 1}
	for _, i := range order {
		if err := s.PutSegment(ctx, prefix, i, []byte(segs[i]), retain); err != nil {
			t.Fatalf("PutSegment %d: %v", i, err)
		}
	}
	if err := s.PutSegment(ctx, prefix, 10, []byte(`[90.0,"o","d"]`+"\n"), retain); err != nil {
		t.Fatal(err)
	}
	meta := application.RecordingMeta{SessionID: "s", Segments: 4, Bytes: 10, StartedAt: time.Now(), EndedAt: time.Now(), EndReason: "closed"}
	if err := s.PutManifest(ctx, prefix, meta, retain); err != nil {
		t.Fatalf("PutManifest: %v", err)
	}

	rc, err := s.Open(ctx, prefix)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	rc.Close()
	want := strings.Join(segs, "") + `[90.0,"o","d"]` + "\n"
	if string(got) != want {
		t.Errorf("Open returned\n%q\nwant\n%q", got, want)
	}

	// Another session's prefix with the same leading characters is not mixed in.
	if _, err := s.Open(ctx, prefix[:len(prefix)-3]); !errors.Is(err, application.ErrNotFound) {
		t.Errorf("Open(partial prefix) err = %v, want ErrNotFound", err)
	}

	// Large segment (multi-MB) round trip.
	big := bytes.Repeat([]byte("0123456789abcdef"), 1<<18) // 4 MiB
	bigPrefix := Prefix("big", time.Now())
	if err := s.PutSegment(ctx, bigPrefix, 0, big, retain); err != nil {
		t.Fatal(err)
	}
	rc, err = s.Open(ctx, bigPrefix)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := io.ReadAll(rc); !bytes.Equal(b, big) {
		t.Error("large segment mismatch")
	}
	rc.Close()

	// The object carries COMPLIANCE retention until retain.
	key := segmentKey(prefix, 0)
	st, err := s.client.StatObject(ctx, s.bucket, key, minio.StatObjectOptions{})
	if err != nil {
		t.Fatal(err)
	}
	mode, until, err := s.client.GetObjectRetention(ctx, s.bucket, key, "")
	if err != nil {
		t.Fatal(err)
	}
	if mode == nil || *mode != minio.Compliance || until == nil || until.Sub(retain).Abs() > 2*time.Second {
		t.Fatalf("retention = %v until %v", mode, until)
	}

	// Locked versions cannot be deleted, not even by the root credentials
	// with the governance bypass.
	for _, bypass := range []bool{false, true} {
		err = s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{VersionID: st.VersionID, GovernanceBypass: bypass})
		if err == nil {
			t.Fatalf("locked version deleted (bypass=%v)", bypass)
		}
		t.Logf("delete locked version (bypass=%v) refused: %v", bypass, err)
	}
	// A plain delete only adds a delete marker; the data stays readable by version.
	_ = s.client.RemoveObject(ctx, s.bucket, key, minio.RemoveObjectOptions{})
	o, err := s.client.GetObject(ctx, s.bucket, key, minio.GetObjectOptions{VersionID: st.VersionID})
	if err != nil {
		t.Fatal(err)
	}
	if b, err := io.ReadAll(o); err != nil || string(b) != segs[0] {
		t.Errorf("locked version unreadable after delete: %v", err)
	}
	// The lock cannot be shortened either.
	short, compliance := time.Now().Add(time.Minute), minio.Compliance
	if err := s.client.PutObjectRetention(ctx, s.bucket, key, minio.PutObjectRetentionOptions{Mode: &compliance, RetainUntilDate: &short, VersionID: st.VersionID}); err == nil {
		t.Error("compliance retention was shortened")
	}

	// Health check fails without a lock (mid-test: a bucket made without Object Lock).
	plain := &MinIOStore{client: s.client, bucket: s.bucket + "-nolock", mode: s.mode, logger: s.logger, now: time.Now}
	if err := plain.client.MakeBucket(ctx, plain.bucket, minio.MakeBucketOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := plain.Healthy(ctx); err == nil || !strings.Contains(err.Error(), "Object Lock") {
		t.Errorf("Healthy on bucket without lock: %v", err)
	}
	_ = plain.client.RemoveBucket(ctx, plain.bucket)
}

func TestIntegrationGovernanceMode(t *testing.T) {
	s, ctx := integrationStore(t, "GOVERNANCE")
	retain := time.Now().Add(time.Hour)
	if err := s.PutSegment(ctx, "gov/x", 0, []byte("data"), retain); err != nil {
		t.Fatal(err)
	}
	mode, _, err := s.client.GetObjectRetention(ctx, s.bucket, "gov/x/000000.cast", "")
	if err != nil || mode == nil || *mode != minio.Governance {
		t.Fatalf("mode = %v, err = %v", mode, err)
	}
	st, _ := s.client.StatObject(ctx, s.bucket, "gov/x/000000.cast", minio.StatObjectOptions{})
	if err := s.client.RemoveObject(ctx, s.bucket, "gov/x/000000.cast", minio.RemoveObjectOptions{VersionID: st.VersionID}); err == nil {
		t.Error("governance-locked version deleted without bypass")
	}
	// Clean up the test data: governance can be bypassed by the admin.
	if err := s.client.RemoveObject(ctx, s.bucket, "gov/x/000000.cast", minio.RemoveObjectOptions{VersionID: st.VersionID, GovernanceBypass: true}); err != nil {
		t.Errorf("bypass delete: %v", err)
	}
}
