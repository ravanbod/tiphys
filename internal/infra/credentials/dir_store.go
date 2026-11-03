package credentials

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"go.uber.org/zap"

	"tiphys/internal/application"
)

const fileSuffix = ".kubeconfig"

// DirStore keeps kubeconfigs as <name>.kubeconfig files in a directory. It is
// for local development: Watch polls the directory.
type DirStore struct {
	dir    string
	poll   time.Duration
	logger *zap.Logger
}

var _ application.CredentialStore = (*DirStore)(nil)

// NewDirStore creates the directory if needed. poll defaults to 2 seconds.
func NewDirStore(dir string, poll time.Duration, logger *zap.Logger) (*DirStore, error) {
	if poll <= 0 {
		poll = 2 * time.Second
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("credential directory: %w", err)
	}
	return &DirStore{dir: dir, poll: poll, logger: logger}, nil
}

func (d *DirStore) path(cluster string) string { return filepath.Join(d.dir, cluster+fileSuffix) }

// Environment: files carry no metadata, so the environment is always set on
// the Clusters page.
func (d *DirStore) Environment(_ context.Context, cluster string) (string, bool, error) {
	if err := validName(cluster); err != nil {
		return "", false, err
	}
	if _, err := os.Stat(d.path(cluster)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", false, application.ErrNotFound
		}
		return "", false, fmt.Errorf("read credential: %w", err)
	}
	return "", false, nil
}

// Protected: files carry no metadata, so no cluster is protected.
func (d *DirStore) Protected(ctx context.Context, cluster string) (bool, error) {
	_, _, err := d.Environment(ctx, cluster)
	return false, err
}

// Put writes the file atomically with mode 0600.
func (d *DirStore) Put(_ context.Context, cluster string, kubeconfig []byte) error {
	if err := validName(cluster); err != nil {
		return err
	}
	if err := emptyKubeconfig(kubeconfig); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(d.dir, ".tmp-*")
	if err != nil {
		d.logger.Error("write cluster credential failed", zap.String("cluster", cluster), zap.Error(err))
		return fmt.Errorf("store credential: %w", err)
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(kubeconfig); err == nil {
		err = tmp.Chmod(0o600)
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp.Name(), d.path(cluster))
	}
	if err != nil {
		d.logger.Error("write cluster credential failed", zap.String("cluster", cluster), zap.Error(err))
		return fmt.Errorf("store credential: %w", err)
	}
	return nil
}

func (d *DirStore) Get(_ context.Context, cluster string) ([]byte, error) {
	if err := validName(cluster); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(d.path(cluster))
	if errors.Is(err, os.ErrNotExist) {
		return nil, application.ErrNotFound
	}
	if err != nil {
		d.logger.Error("read cluster credential failed", zap.String("cluster", cluster), zap.Error(err))
		return nil, fmt.Errorf("read credential: %w", err)
	}
	return b, nil
}

func (d *DirStore) Delete(_ context.Context, cluster string) error {
	if err := validName(cluster); err != nil {
		return err
	}
	err := os.Remove(d.path(cluster))
	if errors.Is(err, os.ErrNotExist) {
		return application.ErrNotFound
	}
	if err != nil {
		d.logger.Error("delete cluster credential failed", zap.String("cluster", cluster), zap.Error(err))
		return fmt.Errorf("delete credential: %w", err)
	}
	return nil
}

func (d *DirStore) List(_ context.Context) ([]string, error) {
	snap, err := d.snapshot()
	if err != nil {
		d.logger.Error("list cluster credentials failed", zap.Error(err))
		return nil, fmt.Errorf("list credentials: %w", err)
	}
	out := make([]string, 0, len(snap))
	for n := range snap {
		out = append(out, n)
	}
	sort.Strings(out)
	return out, nil
}

type fileState struct {
	modTime time.Time
	size    int64
}

// snapshot reads the directory; files with invalid cluster names are ignored.
func (d *DirStore) snapshot() (map[string]fileState, error) {
	entries, err := os.ReadDir(d.dir)
	if err != nil {
		return nil, err
	}
	out := map[string]fileState{}
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), fileSuffix)
		if !ok || e.IsDir() || validName(name) != nil {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out[name] = fileState{info.ModTime(), info.Size()}
	}
	return out, nil
}

// Watch polls the directory. Files present at start are reported as adds.
func (d *DirStore) Watch(ctx context.Context, onChange func(cluster string)) error {
	known := map[string]fileState{}
	tick := time.NewTicker(d.poll)
	defer tick.Stop()
	for {
		cur, err := d.snapshot()
		if err != nil {
			d.logger.Error("poll cluster credentials failed", zap.Error(err))
		} else {
			for n, st := range cur {
				if old, ok := known[n]; !ok || old != st {
					onChange(n)
				}
			}
			for n := range known {
				if _, ok := cur[n]; !ok {
					onChange(n)
				}
			}
			known = cur
		}
		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}
