package recording

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
)

func TestPrefix(t *testing.T) {
	at := time.Date(2026, 3, 9, 23, 30, 0, 0, time.FixedZone("x", -3600*2)) // 01:30 UTC on the 10th
	if got := Prefix("abc-123", at); got != "sessions/2026/03/abc-123" {
		t.Errorf("Prefix = %q", got)
	}
	if got := Prefix("id", time.Date(2026, 12, 31, 22, 0, 0, 0, time.FixedZone("x", 3600*5))); got != "sessions/2026/12/id" {
		t.Errorf("Prefix = %q", got)
	}
}

func TestSegmentKey(t *testing.T) {
	for in, want := range map[string]string{
		segmentKey("sessions/2026/10/x", 0):   "sessions/2026/10/x/000000.cast",
		segmentKey("sessions/2026/10/x/", 42): "sessions/2026/10/x/000042.cast",
		segmentKey("p", 1234567):              "p/1234567.cast",
	} {
		if in != want {
			t.Errorf("got %q, want %q", in, want)
		}
	}
	if !segmentRE.MatchString("000007.cast") || segmentRE.MatchString("manifest.json") || segmentRE.MatchString("7.cast") {
		t.Error("segmentRE mismatch")
	}
}

func TestConfigValidation(t *testing.T) {
	base := Config{Endpoint: "localhost:9000", AccessKey: "a", SecretKey: "b", Bucket: "rec"}
	if _, err := NewMinIOStore(base, zap.NewNop()); err != nil {
		t.Fatal(err)
	}
	bad := base
	bad.RetentionMode = "forever"
	if _, err := NewMinIOStore(bad, zap.NewNop()); err == nil {
		t.Error("invalid mode accepted")
	}
	bad = base
	bad.Bucket = ""
	if _, err := NewMinIOStore(bad, zap.NewNop()); err == nil {
		t.Error("empty bucket accepted")
	}
	gov := base
	gov.RetentionMode = "governance"
	if s, err := NewMinIOStore(gov, zap.NewNop()); err != nil || s.mode != "GOVERNANCE" {
		t.Errorf("governance: %v", err)
	}
}

// fakeS3 answers just the three calls Healthy makes.
type fakeS3 struct {
	requests   atomic.Int32
	lock       bool
	statCode   int
	bucketGone bool
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.requests.Add(1)
	switch {
	case r.Method == http.MethodHead && r.URL.Path == "/rec/":
		if f.bucketGone {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
	case r.Method == http.MethodGet && strings.TrimSuffix(r.URL.Path, "/") == "/rec" && r.URL.Query().Has("object-lock"):
		if !f.lock {
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`<Error><Code>ObjectLockConfigurationNotFoundError</Code><Message>x</Message></Error>`))
			return
		}
		w.Write([]byte(`<ObjectLockConfiguration xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><ObjectLockEnabled>Enabled</ObjectLockEnabled></ObjectLockConfiguration>`))
	case r.Method == http.MethodHead && r.URL.Path == "/rec/"+healthProbeKey:
		w.WriteHeader(f.statCode)
	default:
		w.WriteHeader(http.StatusTeapot)
	}
}

func newFakeStore(t *testing.T, f *fakeS3) (*MinIOStore, *time.Time) {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	s, err := NewMinIOStore(Config{Endpoint: strings.TrimPrefix(srv.URL, "http://"), AccessKey: "a", SecretKey: "b", Bucket: "rec", Region: "us-east-1"}, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	clock := time.Unix(1_700_000_000, 0)
	s.now = func() time.Time { return clock }
	return s, &clock
}

func TestHealthyCachesResult(t *testing.T) {
	f := &fakeS3{lock: true, statCode: http.StatusNotFound}
	s, clock := newFakeStore(t, f)
	ctx := context.Background()

	if err := s.Healthy(ctx); err != nil {
		t.Fatal(err)
	}
	n := f.requests.Load()
	if n != 3 {
		t.Errorf("first check made %d requests, want 3", n)
	}
	// Within 5s: cached, even though MinIO turned bad meanwhile.
	f.lock = false
	*clock = clock.Add(4 * time.Second)
	if err := s.Healthy(ctx); err != nil || f.requests.Load() != n {
		t.Errorf("cached call: err=%v requests=%d", err, f.requests.Load())
	}
	// After 5s: re-checked, and the failure is cached too.
	*clock = clock.Add(2 * time.Second)
	if err := s.Healthy(ctx); err == nil || !strings.Contains(err.Error(), "Object Lock") {
		t.Fatalf("err = %v, want object lock error", err)
	}
	n = f.requests.Load()
	f.lock = true
	if err := s.Healthy(ctx); err == nil || f.requests.Load() != n {
		t.Errorf("failure should be cached: err=%v", err)
	}
}

func TestHealthyFailures(t *testing.T) {
	cases := map[string]*fakeS3{
		"no bucket":      {lock: true, statCode: 404, bucketGone: true},
		"no lock":        {lock: false, statCode: 404},
		"creds rejected": {lock: true, statCode: http.StatusForbidden},
	}
	for name, f := range cases {
		s, _ := newFakeStore(t, f)
		if err := s.Healthy(context.Background()); err == nil {
			t.Errorf("%s: want error", name)
		}
	}
}

func TestPutRejectsPastRetention(t *testing.T) {
	s, clock := newFakeStore(t, &fakeS3{})
	if err := s.PutSegment(context.Background(), "p", 0, []byte("x"), clock.Add(-time.Hour)); err == nil {
		t.Error("past retain-until accepted")
	}
	if err := s.PutSegment(context.Background(), "p", -1, []byte("x"), clock.Add(time.Hour)); err == nil {
		t.Error("negative seq accepted")
	}
}
