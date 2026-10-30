package credentials

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"

	"tiphys/internal/application"
)

func errorsIs(err, target error) bool { return errors.Is(err, target) }

func TestDirStoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, err := NewDirStore(dir, 10*time.Millisecond, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := s.Put(ctx, "dev", []byte("kc")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, "dev.kubeconfig"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("file: %v %v", info, err)
	}
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("x"), 0o600)
	os.WriteFile(filepath.Join(dir, "Bad_Name.kubeconfig"), []byte("x"), 0o600)
	if names, _ := s.List(ctx); len(names) != 1 || names[0] != "dev" {
		t.Fatalf("list = %v", names)
	}
	if b, err := s.Get(ctx, "dev"); err != nil || string(b) != "kc" {
		t.Fatalf("get = %q %v", b, err)
	}
	if _, err := s.Get(ctx, "../etc/passwd"); !errors.Is(err, application.ErrInvalidName) {
		t.Fatalf("traversal: %v", err)
	}
	if err := s.Delete(ctx, "dev"); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(ctx, "dev"); !errors.Is(err, application.ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 2 {
		t.Fatalf("temp files left behind: %v", entries)
	}
}

func TestDirStoreWatchPolls(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewDirStore(dir, 10*time.Millisecond, zap.NewNop())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Put(ctx, "a", []byte("1"))
	var c changes
	go s.Watch(ctx, c.add)
	waitFor(t, "initial", func() bool { return c.has("a") })
	n := c.count()
	s.Put(ctx, "a", []byte("22")) // size changes
	waitFor(t, "change", func() bool { return c.count() > n })
	s.Put(ctx, "b", []byte("1"))
	waitFor(t, "add", func() bool { return c.has("b") })
	n = c.count()
	s.Delete(ctx, "b")
	waitFor(t, "delete", func() bool { return c.count() > n })
}
