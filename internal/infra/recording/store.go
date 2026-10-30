// Package recording implements application.RecordingStore on MinIO (S3 API)
// with Object Lock. The portal's credentials can put and read but not delete;
// retention is enforced by the bucket, not by this code.
package recording

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/minio/minio-go/v7/pkg/lifecycle"
	"go.uber.org/zap"

	"tiphys/internal/application"
)

const (
	healthTTL = 5 * time.Second
	// healthProbeKey is never written (the credentials cannot delete it); we
	// only stat it, and a clean "not found" proves the credentials work.
	healthProbeKey = "healthcheck/absent"
)

// Retention modes. COMPLIANCE cannot be shortened or removed by anyone,
// including the root user; GOVERNANCE can be bypassed by privileged users.
const (
	ModeCompliance = "COMPLIANCE"
	ModeGovernance = "GOVERNANCE"
)

// Config describes the MinIO connection.
type Config struct {
	Endpoint  string // host:port, no scheme
	AccessKey string
	SecretKey string
	Secure    bool // https
	Bucket    string
	Region    string // optional; set it to skip the bucket-location lookup
	// RetentionMode is ModeCompliance (default when empty) or ModeGovernance.
	RetentionMode string
	// Transport carries a custom CA or insecure TLS; nil uses the default.
	Transport http.RoundTripper
}

// MinIOStore stores recordings as sessions/<yyyy>/<mm>/<id>/<seq>.cast.
type MinIOStore struct {
	client *minio.Client
	bucket string
	mode   minio.RetentionMode
	logger *zap.Logger

	now      func() time.Time
	mu       sync.Mutex
	checked  time.Time
	checkErr error
}

func NewMinIOStore(cfg Config, logger *zap.Logger) (*MinIOStore, error) {
	var mode minio.RetentionMode
	switch strings.ToUpper(cfg.RetentionMode) {
	case "", ModeCompliance:
		mode = minio.Compliance
	case ModeGovernance:
		mode = minio.Governance
	default:
		return nil, fmt.Errorf("recording: invalid retention mode %q (want COMPLIANCE or GOVERNANCE)", cfg.RetentionMode)
	}
	if cfg.Bucket == "" {
		return nil, errors.New("recording: bucket is required")
	}
	client, err := minio.New(cfg.Endpoint, &minio.Options{
		Creds:     credentials.NewStaticV4(cfg.AccessKey, cfg.SecretKey, ""),
		Secure:    cfg.Secure,
		Region:    cfg.Region,
		Transport: cfg.Transport,
	})
	if err != nil {
		return nil, fmt.Errorf("recording: %w", err)
	}
	return &MinIOStore{client: client, bucket: cfg.Bucket, mode: mode, logger: logger, now: time.Now}, nil
}

// Prefix is the object prefix of a session: sessions/<yyyy>/<mm>/<id>. at is
// the session start, so the prefix can be recomputed from the session record.
func Prefix(sessionID string, at time.Time) string {
	at = at.UTC()
	return fmt.Sprintf("sessions/%04d/%02d/%s", at.Year(), int(at.Month()), sessionID)
}

// Healthy checks that a recording can be started: the bucket exists, Object
// Lock is enabled and the credentials can talk to it. The result (success or
// failure) is cached for 5 seconds, so exec starts do not hammer MinIO.
func (s *MinIOStore) Healthy(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.checked.IsZero() && s.now().Sub(s.checked) < healthTTL {
		return s.checkErr
	}
	err := s.check(ctx)
	if ctx.Err() == nil { // a cancelled caller says nothing about MinIO
		s.checked, s.checkErr = s.now(), err
	}
	return err
}

func (s *MinIOStore) check(ctx context.Context) error {
	exists, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		s.logger.Error("recording store health check failed", zap.String("check", "bucket exists"), zap.String("bucket", s.bucket), zap.Error(err))
		return fmt.Errorf("recording store unavailable: %w", err)
	}
	if !exists {
		s.logger.Error("recording bucket does not exist", zap.String("bucket", s.bucket))
		return fmt.Errorf("recording bucket %q does not exist", s.bucket)
	}
	lock, _, _, _, err := s.client.GetObjectLockConfig(ctx, s.bucket)
	if err != nil || lock != "Enabled" {
		s.logger.Error("recording bucket has no object lock", zap.String("bucket", s.bucket), zap.String("lock", lock), zap.Error(err))
		return fmt.Errorf("recording bucket %q does not have Object Lock enabled", s.bucket)
	}
	_, err = s.client.StatObject(ctx, s.bucket, healthProbeKey, minio.StatObjectOptions{})
	if err != nil && minio.ToErrorResponse(err).Code != minio.NoSuchKey {
		s.logger.Error("recording store credentials check failed", zap.String("bucket", s.bucket), zap.Error(err))
		return fmt.Errorf("recording store credentials rejected: %w", err)
	}
	return nil
}

// PutSegment writes <prefix>/<seq>.cast (seq zero-padded to 6 digits), locked
// until retainUntil.
func (s *MinIOStore) PutSegment(ctx context.Context, prefix string, seq int, data []byte, retainUntil time.Time) error {
	if seq < 0 {
		return fmt.Errorf("invalid segment sequence %d", seq)
	}
	return s.put(ctx, segmentKey(prefix, seq), "application/x-asciicast", data, retainUntil)
}

// PutManifest writes <prefix>/manifest.json, locked until retainUntil.
func (s *MinIOStore) PutManifest(ctx context.Context, prefix string, m application.RecordingMeta, retainUntil time.Time) error {
	data, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("failed to encode recording manifest: %w", err)
	}
	return s.put(ctx, strings.TrimRight(prefix, "/")+"/manifest.json", "application/json", data, retainUntil)
}

func (s *MinIOStore) put(ctx context.Context, key, contentType string, data []byte, retainUntil time.Time) error {
	if !retainUntil.After(s.now()) {
		return fmt.Errorf("retain-until %s is not in the future", retainUntil.Format(time.RFC3339))
	}
	_, err := s.client.PutObject(ctx, s.bucket, key, bytes.NewReader(data), int64(len(data)), minio.PutObjectOptions{
		ContentType:     contentType,
		Mode:            s.mode,
		RetainUntilDate: retainUntil.UTC(),
		SendContentMd5:  true, // S3 requires an integrity header on locked puts
	})
	if err != nil {
		s.logger.Error("failed to write recording object", zap.String("bucket", s.bucket), zap.String("key", key), zap.Error(err))
		return fmt.Errorf("failed to write recording object %s: %w", key, err)
	}
	return nil
}

func segmentKey(prefix string, seq int) string {
	return fmt.Sprintf("%s/%06d.cast", strings.TrimRight(prefix, "/"), seq)
}

var segmentRE = regexp.MustCompile(`^(\d{6,})\.cast$`)

// Open lists the prefix and returns the concatenation of its .cast segments in
// sequence order. Segment 0 carries the asciicast header; the caller wrote the
// later ones as events only. The stream reads lazily and needs ctx to stay
// alive until it is closed. It returns application.ErrNotFound if there are no
// segments.
func (s *MinIOStore) Open(ctx context.Context, prefix string) (io.ReadCloser, error) {
	dir := strings.TrimRight(prefix, "/") + "/"
	type segment struct {
		seq int
		key string
	}
	var segs []segment
	for obj := range s.client.ListObjects(ctx, s.bucket, minio.ListObjectsOptions{Prefix: dir}) {
		if obj.Err != nil {
			s.logger.Error("failed to list recording segments", zap.String("bucket", s.bucket), zap.String("prefix", dir), zap.Error(obj.Err))
			return nil, fmt.Errorf("failed to list recording segments: %w", obj.Err)
		}
		m := segmentRE.FindStringSubmatch(strings.TrimPrefix(obj.Key, dir))
		if m == nil {
			continue // manifest.json or anything else
		}
		seq, _ := strconv.Atoi(m[1])
		segs = append(segs, segment{seq, obj.Key})
	}
	if len(segs) == 0 {
		return nil, fmt.Errorf("recording %s: %w", dir, application.ErrNotFound)
	}
	sort.Slice(segs, func(i, j int) bool { return segs[i].seq < segs[j].seq })

	// A gap would replay as if complete, so make it visible in the logs.
	for i, sg := range segs {
		if sg.seq != i {
			s.logger.Warn("recording has missing segments", zap.String("prefix", dir), zap.Int("expected", i), zap.Int("found", sg.seq))
			break
		}
	}
	keys := make([]string, len(segs))
	for i, sg := range segs {
		keys[i] = sg.key
	}
	return &concatReader{ctx: ctx, store: s, keys: keys}, nil
}

// concatReader streams objects one after another, opening each lazily.
type concatReader struct {
	ctx   context.Context
	store *MinIOStore
	keys  []string
	cur   *minio.Object
}

func (r *concatReader) Read(p []byte) (int, error) {
	for {
		if r.cur == nil {
			if len(r.keys) == 0 {
				return 0, io.EOF
			}
			obj, err := r.store.client.GetObject(r.ctx, r.store.bucket, r.keys[0], minio.GetObjectOptions{})
			if err != nil {
				r.store.logger.Error("failed to open recording segment", zap.String("key", r.keys[0]), zap.Error(err))
				return 0, fmt.Errorf("failed to open recording segment %s: %w", r.keys[0], err)
			}
			r.cur = obj
		}
		n, err := r.cur.Read(p)
		if err == io.EOF {
			r.cur.Close()
			r.cur, r.keys = nil, r.keys[1:]
			if n > 0 {
				return n, nil
			}
			continue
		}
		if err != nil {
			r.store.logger.Error("failed to read recording segment", zap.String("key", r.keys[0]), zap.Error(err))
			return n, fmt.Errorf("failed to read recording segment %s: %w", r.keys[0], err)
		}
		return n, nil
	}
}

func (r *concatReader) Close() error {
	r.keys = nil
	if r.cur != nil {
		err := r.cur.Close()
		r.cur = nil
		return err
	}
	return nil
}

// EnsureBucket creates the bucket with Object Lock and a lifecycle rule that
// expires objects after expireDays (locked versions survive until their lock
// ends). It is for development only: production buckets are provisioned with
// credentials that the portal does not have. An existing bucket is left as is.
func (s *MinIOStore) EnsureBucket(ctx context.Context, expireDays int) error {
	exists, err := s.client.BucketExists(ctx, s.bucket)
	if err != nil {
		return fmt.Errorf("failed to check bucket: %w", err)
	}
	if exists {
		return nil
	}
	if err := s.client.MakeBucket(ctx, s.bucket, minio.MakeBucketOptions{ObjectLocking: true}); err != nil {
		return fmt.Errorf("failed to create bucket: %w", err)
	}
	cfg := lifecycle.NewConfiguration()
	cfg.Rules = []lifecycle.Rule{{
		ID:         "expire-recordings",
		Status:     "Enabled",
		Expiration: lifecycle.Expiration{Days: lifecycle.ExpirationDays(expireDays)},
		// Object Lock buckets are versioned: expiry adds a delete marker and
		// the data goes when the noncurrent version expires.
		NoncurrentVersionExpiration: lifecycle.NoncurrentVersionExpiration{NoncurrentDays: 1},
	}}
	if err := s.client.SetBucketLifecycle(ctx, s.bucket, cfg); err != nil {
		return fmt.Errorf("failed to set bucket lifecycle: %w", err)
	}
	return nil
}
