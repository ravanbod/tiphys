package application

import (
	"context"
	"errors"
	"sync"
	"time"

	"tiphys/internal/domain"
)

const (
	// MaxLiveNamespaces is how many namespaces one live stream may follow.
	MaxLiveNamespaces = 10
	// MaxLiveStreams is how many live streams a user may have open at once
	// (per portal pod); each browser tab holds one.
	MaxLiveStreams = 8
	// LiveMaxDuration ends a live stream; the browser reconnects, which
	// checks the login session again.
	LiveMaxDuration = 10 * time.Minute
)

// ErrTooManyStreams refuses a live stream over MaxLiveStreams.
var ErrTooManyStreams = errors.New("too many live streams")

// LiveNamespace is a namespace a live stream was asked to follow; Err says
// why it is not followed.
type LiveNamespace struct {
	domain.ClusterNS
	Err error
}

// LiveChange is a change in a followed namespace, cut to what the principal
// may see of it.
type LiveChange struct {
	domain.ClusterNS
	NamespaceChange
}

// Live is an open live stream. Changes is closed when Stream ends.
type Live struct {
	Namespaces []LiveNamespace
	Changes    <-chan LiveChange
	Stream     *Stream
}

// Live follows namespaces for the pages that show them. A namespace needs a
// grant there (like Workloads); every change is then decided again against
// the current policy (liveView), so grant changes apply to the next change
// without ending the stream. The stream is registered with the
// StreamManager (logout, disabling the user, LiveMaxDuration end it).
func (s *WorkloadService) Live(ctx context.Context, p Principal, targets []domain.ClusterNS) (*Live, error) {
	if len(targets) == 0 || len(targets) > MaxLiveNamespaces {
		return nil, Invalid("ns", "follow 1 to 10 namespaces")
	}
	seen := map[domain.ClusterNS]bool{}
	var uniq []domain.ClusterNS
	for _, t := range targets {
		if !domain.ValidDNS1123Subdomain(t.Cluster) {
			return nil, Invalid("ns", "invalid cluster name")
		}
		if err := validateNames(t.Namespace, domain.WorkloadRef{}); err != nil {
			return nil, err
		}
		if !seen[t] {
			seen[t] = true
			uniq = append(uniq, t)
		}
	}
	if !s.acquireLive(p.User.ID) {
		return nil, ErrTooManyStreams
	}
	st := s.streams.Open(s.rootCtx, StreamSpec{
		Kind: StreamLive, UserID: p.User.ID, Username: p.User.Username, WebSessionID: p.SessionID, MaxDuration: LiveMaxDuration,
	}, domain.Decision{})

	live := &Live{Namespaces: make([]LiveNamespace, len(uniq)), Stream: st}
	feeds := make([]<-chan NamespaceChange, len(uniq))
	var wg sync.WaitGroup
	for i, t := range uniq {
		wg.Add(1)
		go func() {
			defer wg.Done()
			live.Namespaces[i].ClusterNS = t
			if err := s.requireAnyGrant(ctx, p, t.Cluster, t.Namespace); err != nil {
				live.Namespaces[i].Err = err
				return
			}
			ch, err := s.kube.WatchNamespace(st.Context(), p.KubeActor(nil), t.Cluster, t.Namespace)
			live.Namespaces[i].Err, feeds[i] = s.kubeErr(t.Cluster, err), ch
		}()
	}
	wg.Wait()

	out := make(chan LiveChange, 64)
	live.Changes = out
	var fan sync.WaitGroup
	for i, feed := range feeds {
		if feed == nil {
			continue
		}
		fan.Add(1)
		go func() {
			defer fan.Done()
			t := uniq[i]
			for c := range feed {
				v, ok := s.liveView(p, t, c)
				if !ok {
					continue
				}
				select {
				case out <- LiveChange{ClusterNS: t, NamespaceChange: v}:
				case <-st.Context().Done():
					return
				}
			}
		}()
	}
	go func() {
		<-st.Context().Done()
		fan.Wait()
		close(out)
		s.releaseLive(p.User.ID)
	}()
	return live, nil
}

// liveView is what the principal may see of a change: everything with logs
// on the workload (like Pods and Events); with only other actions, the
// replica counts the workload list and page show and otherwise only that
// the workload changed; nothing without a grant on it.
func (s *WorkloadService) liveView(p Principal, t domain.ClusterNS, c NamespaceChange) (NamespaceChange, bool) {
	wt := wlTarget(t.Cluster, t.Namespace, c.Workload)
	if s.access.Decide(p, wt, domain.ActionLogs).Allowed {
		return c, true
	}
	if !holdsAny(s.access.AccessMap(p, wt)) {
		return NamespaceChange{}, false
	}
	switch c.Type {
	case ChangeWorkload, ChangeRolloutStarted, ChangeRolloutComplete, ChangeRolloutFailed:
		return c, true
	}
	return NamespaceChange{Workload: c.Workload, Type: ChangeWorkload}, true
}

func (s *WorkloadService) acquireLive(userID int64) bool {
	s.liveMu.Lock()
	defer s.liveMu.Unlock()
	if s.liveOpen[userID] >= MaxLiveStreams {
		return false
	}
	s.liveOpen[userID]++
	return true
}

func (s *WorkloadService) releaseLive(userID int64) {
	s.liveMu.Lock()
	defer s.liveMu.Unlock()
	if s.liveOpen[userID]--; s.liveOpen[userID] <= 0 {
		delete(s.liveOpen, userID)
	}
}
