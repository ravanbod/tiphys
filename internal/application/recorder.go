package application

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"
	"unicode/utf8"
)

// recordingSegmentInterval is how often buffered events are written to
// storage; a portal crash loses at most this much of a session.
const recordingSegmentInterval = 30 * time.Second

// recordingPrefix is the object prefix of a session's recording:
// sessions/<yyyy>/<mm>/<session-id>.
func recordingPrefix(sessionID string, at time.Time) string {
	at = at.UTC()
	return fmt.Sprintf("sessions/%04d/%02d/%s", at.Year(), int(at.Month()), sessionID)
}

// recorder writes an exec session as asciicast v2: a header line, then
// `[seconds, "o"|"i"|"r", data]` events. Segment 0 holds only the header and
// is written at session start (proving storage works); later segments hold
// the events of each interval.
type recorder struct {
	store       RecordingStore
	prefix      string
	retainUntil func() time.Time
	started     time.Time

	mu       sync.Mutex
	buf      bytes.Buffer
	seq      int
	bytes    int64
	outCarry []byte
	inCarry  []byte
	lastErr  error
}

func newRecorder(store RecordingStore, prefix string, started time.Time, retainUntil func() time.Time) *recorder {
	return &recorder{store: store, prefix: prefix, started: started, retainUntil: retainUntil}
}

type castHeader struct {
	Version   int               `json:"version"`
	Width     int               `json:"width"`
	Height    int               `json:"height"`
	Timestamp int64             `json:"timestamp"`
	Title     string            `json:"title,omitempty"`
	Env       map[string]string `json:"env,omitempty"`
}

// writeHeader stores segment 0.
func (r *recorder) writeHeader(ctx context.Context, cols, rows uint16, title, shell string) error {
	h := castHeader{Version: 2, Width: int(cols), Height: int(rows), Timestamp: r.started.Unix(), Title: title,
		Env: map[string]string{"SHELL": shell, "TERM": "xterm-256color"}}
	line, _ := json.Marshal(h)
	line = append(line, '\n')
	if err := r.store.PutSegment(ctx, r.prefix, 0, line, r.retainUntil()); err != nil {
		return err
	}
	r.mu.Lock()
	r.seq = 1
	r.bytes += int64(len(line))
	r.mu.Unlock()
	return nil
}

func (r *recorder) offset(now time.Time) float64 {
	return float64(now.Sub(r.started).Microseconds()) / 1e6
}

// splitUTF8 returns the longest valid-UTF-8-boundary prefix of carry+data
// and keeps an incomplete trailing sequence for the next call.
func splitUTF8(carry *[]byte, data []byte) []byte {
	all := append(*carry, data...)
	*carry = nil
	cut := len(all)
	for i := 1; i <= utf8.UTFMax-1 && i <= len(all); i++ {
		b := all[len(all)-i]
		if utf8.RuneStart(b) {
			if !utf8.FullRune(all[len(all)-i:]) {
				cut = len(all) - i
			}
			break
		}
	}
	*carry = append([]byte(nil), all[cut:]...)
	return all[:cut]
}

func (r *recorder) event(now time.Time, kind string, data string) {
	line, _ := json.Marshal([]any{r.offset(now), kind, data})
	r.buf.Write(line)
	r.buf.WriteByte('\n')
}

// Output records terminal output.
func (r *recorder) Output(now time.Time, data []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if d := splitUTF8(&r.outCarry, data); len(d) > 0 {
		r.event(now, "o", string(d))
	}
}

// Input records terminal input.
func (r *recorder) Input(now time.Time, data []byte) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if d := splitUTF8(&r.inCarry, data); len(d) > 0 {
		r.event(now, "i", string(d))
	}
}

// Resize records a terminal resize.
func (r *recorder) Resize(now time.Time, cols, rows uint16) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.event(now, "r", fmt.Sprintf("%dx%d", cols, rows))
}

// Flush writes buffered events as the next segment, retrying briefly. The
// buffer is kept on failure so a later flush can still write it.
func (r *recorder) Flush(ctx context.Context) error {
	r.mu.Lock()
	if r.buf.Len() == 0 {
		r.mu.Unlock()
		return nil
	}
	data := append([]byte(nil), r.buf.Bytes()...)
	seq := r.seq
	r.mu.Unlock()

	var err error
	for attempt := 0; attempt < 3; attempt++ {
		if err = r.store.PutSegment(ctx, r.prefix, seq, data, r.retainUntil()); err == nil {
			r.mu.Lock()
			r.buf.Next(len(data))
			r.seq++
			r.bytes += int64(len(data))
			r.mu.Unlock()
			return nil
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(time.Second):
		}
	}
	r.mu.Lock()
	r.lastErr = err
	r.mu.Unlock()
	return err
}

// Run flushes every recordingSegmentInterval until ctx is done; onFail is
// called once if a flush fails permanently.
func (r *recorder) Run(ctx context.Context, onFail func(error)) {
	t := time.NewTicker(recordingSegmentInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := r.Flush(ctx); err != nil {
				onFail(err)
				return
			}
		}
	}
}

// Close writes the last segment and the manifest. It returns the recording
// size in bytes.
func (r *recorder) Close(ctx context.Context, sessionID string, ended time.Time, reason string) (int64, error) {
	flushErr := r.Flush(ctx)
	r.mu.Lock()
	meta := RecordingMeta{SessionID: sessionID, Segments: r.seq, Bytes: r.bytes, StartedAt: r.started, EndedAt: ended, EndReason: reason}
	r.mu.Unlock()
	if err := r.store.PutManifest(ctx, r.prefix, meta, r.retainUntil()); err != nil && flushErr == nil {
		flushErr = err
	}
	return meta.Bytes, flushErr
}

// Bytes is the number of bytes written so far.
func (r *recorder) Bytes() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.bytes
}
