package kubeaccess

import (
	"context"
	"errors"
	"strconv"
	"time"

	"go.uber.org/zap"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/utils/ptr"

	"tiphys/internal/application"
	"tiphys/internal/domain"
)

const (
	watchBuffer      = 64
	watchTimeoutSecs = 300              // the API server ends each watch call after this; it is reopened
	watchRetryMax    = 30 * time.Second // longest wait between attempts to reopen a lost watch
	eventRepeatEvery = 30 * time.Second // a repeating event (BackOff, Unhealthy) is passed on at most this often
	rolloutFollowMax = 15 * time.Minute // a rollout's status is polled at most this long
	podFollowFor     = 10 * time.Second // a workload's status is polled this long after a pod change, as its controller updates it later
	maxOwnerCache    = 4096
	maxSeenEvents    = 1024
)

// notableNormal are the Normal events a namespace watch passes on besides
// warnings, with the kind of object they must be about: a CronJob creating a
// Job and a Job finishing.
var notableNormal = map[string]string{"SuccessfulCreate": "CronJob", "SawCompletedJob": "CronJob", "Completed": "Job"}

// nsWatch follows one namespace for WatchNamespace. Only its goroutine
// touches its state after the start.
type nsWatch struct {
	a       *Access
	c       *clients
	cluster string
	ns      string
	out     chan application.NamespaceChange

	pods      map[string]podState
	owners    map[string]domain.WorkloadRef // "ReplicaSet/x" or "Job/x"; zero = owned by no workload
	workloads map[domain.WorkloadRef]*workloadState
	seen      map[types.UID]time.Time // when an event was last passed on
	podsRV    string
	eventsRV  string
}

type podState struct {
	workload domain.WorkloadRef
	phase    string
	ready    int
	restarts map[string]int32 // per regular container
}

type workloadState struct {
	known    bool // revision and status were read
	revision string
	status   application.RolloutStatus
	due      bool      // read again at the next tick
	until    time.Time // read at every tick until then (after a pod change)
	rolling  time.Time // when the running rollout was seen to start; zero when none
}

// WatchNamespace lists the namespace's pods, then watches pods and events
// and turns them into changes of the workloads that own them (resolved
// through the owner chain, like podBelongs). Pod changes make the owning
// workload's status be read again at the next tick, which detects rollouts
// (a new revision) and polls them until complete. The status is read at
// every tick for podFollowFor after a pod change, since the controller
// counts a ready or updated pod only after the pod changed. Repeated events are passed
// on at most every eventRepeatEvery. Lost watches are reopened, after a new
// list when needed, until ctx is done.
func (a *Access) WatchNamespace(ctx context.Context, actor application.KubeActor, cluster, namespace string) (<-chan application.NamespaceChange, error) {
	c, err := a.open(actor, cluster, namespace)
	if err != nil {
		return nil, err
	}
	w := &nsWatch{
		a: a, c: c, cluster: cluster, ns: namespace, out: make(chan application.NamespaceChange, watchBuffer),
		pods: map[string]podState{}, owners: map[string]domain.WorkloadRef{},
		workloads: map[domain.WorkloadRef]*workloadState{}, seen: map[types.UID]time.Time{},
	}
	if err := w.listPods(ctx, false); err != nil {
		return nil, a.fail("list pods", cluster, err)
	}
	if err := w.listEvents(ctx); err != nil {
		return nil, a.fail("list events", cluster, err)
	}
	w.baseline(ctx)
	pods, err := w.watchPods(ctx)
	if err != nil {
		return nil, a.fail("watch pods", cluster, err)
	}
	events, err := w.watchEvents(ctx)
	if err != nil {
		pods.Stop()
		return nil, a.fail("watch events", cluster, err)
	}
	go w.run(ctx, pods, events)
	return w.out, nil
}

func (w *nsWatch) run(ctx context.Context, pods, events watch.Interface) {
	defer close(w.out)
	defer func() {
		if pods != nil {
			pods.Stop()
		}
		if events != nil {
			events.Stop()
		}
	}()
	tick := time.NewTicker(w.a.pollEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case e, ok := <-pods.ResultChan():
			if !ok || e.Type == watch.Error {
				pods.Stop()
				if pods = w.reopen(ctx, "pods", ok, w.listPodsChanged, w.watchPods); pods == nil {
					return
				}
				continue
			}
			w.onPod(ctx, e)
		case e, ok := <-events.ResultChan():
			if !ok || e.Type == watch.Error {
				events.Stop()
				if events = w.reopen(ctx, "events", ok, w.listEvents, w.watchEvents); events == nil {
					return
				}
				continue
			}
			w.onEvent(ctx, e)
		case <-tick.C:
			now := w.a.clock.Now()
			for ref, st := range w.workloads {
				if st.due || now.Before(st.until) || !st.rolling.IsZero() {
					w.read(ctx, ref, st, true)
				}
			}
		}
	}
}

// reopen opens a lost watch again: from the last resource version when the
// call merely ended, after a new list when the watch failed (e.g. the
// version expired). It retries with backoff and returns nil once ctx is done.
// An outage is logged once.
func (w *nsWatch) reopen(ctx context.Context, what string, failed bool, list func(context.Context) error, open func(context.Context) (watch.Interface, error)) watch.Interface {
	relist, logged := failed, false
	delay := w.a.pollEvery
	for {
		var err error
		if relist {
			if err = list(ctx); err == nil {
				relist = false
			}
		}
		if !relist {
			var wi watch.Interface
			if wi, err = open(ctx); err == nil {
				if logged {
					w.a.logger.Info("namespace watch resumed", zap.String("cluster", w.cluster), zap.String("namespace", w.ns), zap.String("watch", what))
				}
				return wi
			}
			relist = true
		}
		if ctx.Err() != nil {
			return nil
		}
		if !logged {
			w.a.logger.Warn("namespace watch interrupted", zap.String("cluster", w.cluster), zap.String("namespace", w.ns), zap.String("watch", what), zap.Error(err))
			logged = true
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(delay):
		}
		delay = min(delay*2, watchRetryMax)
	}
}

func watchOpts(rv string) metav1.ListOptions {
	return metav1.ListOptions{ResourceVersion: rv, AllowWatchBookmarks: true, TimeoutSeconds: ptr.To(int64(watchTimeoutSecs))}
}

func (w *nsWatch) watchPods(ctx context.Context) (watch.Interface, error) {
	return w.c.kube.CoreV1().Pods(w.ns).Watch(ctx, watchOpts(w.podsRV))
}

func (w *nsWatch) watchEvents(ctx context.Context) (watch.Interface, error) {
	return w.c.kube.CoreV1().Events(w.ns).Watch(ctx, watchOpts(w.eventsRV))
}

// listPods reads every pod; with emit, differences to what was known are
// sent as changes (after a lost watch).
func (w *nsWatch) listPods(ctx context.Context, emit bool) error {
	lctx, cancel := callCtx(ctx)
	defer cancel()
	list, err := w.c.kube.CoreV1().Pods(w.ns).List(lctx, metav1.ListOptions{})
	if err != nil {
		return err
	}
	w.podsRV = list.ResourceVersion
	present := map[string]bool{}
	for i := range list.Items {
		present[list.Items[i].Name] = true
		w.podChanged(ctx, &list.Items[i], emit)
	}
	for name := range w.pods {
		if !present[name] {
			w.podGone(ctx, name, emit)
		}
	}
	return nil
}

func (w *nsWatch) listPodsChanged(ctx context.Context) error { return w.listPods(ctx, true) }

// listEvents only takes the resource version: events from before the watch
// are not passed on.
func (w *nsWatch) listEvents(ctx context.Context) error {
	lctx, cancel := callCtx(ctx)
	defer cancel()
	list, err := w.c.kube.CoreV1().Events(w.ns).List(lctx, metav1.ListOptions{Limit: 1})
	if err != nil {
		return err
	}
	w.eventsRV = list.ResourceVersion
	return nil
}

func (w *nsWatch) onPod(ctx context.Context, e watch.Event) {
	p, ok := e.Object.(*corev1.Pod)
	if !ok {
		return
	}
	w.podsRV = p.ResourceVersion
	switch e.Type {
	case watch.Added, watch.Modified:
		w.podChanged(ctx, p, true)
	case watch.Deleted:
		w.podGone(ctx, p.Name, true)
	}
}

func (w *nsWatch) onEvent(ctx context.Context, e watch.Event) {
	ev, ok := e.Object.(*corev1.Event)
	if !ok {
		return
	}
	w.eventsRV = ev.ResourceVersion
	if e.Type == watch.Added || e.Type == watch.Modified {
		w.eventChanged(ctx, ev)
	}
}

func (w *nsWatch) podChanged(ctx context.Context, p *corev1.Pod, emit bool) {
	old, had := w.pods[p.Name]
	ref := old.workload
	if !had {
		var ok bool
		if ref, ok = w.podOwner(ctx, p); !ok {
			return
		}
	}
	info := podInfo(p)
	cur := podState{workload: ref, phase: info.Phase, ready: info.ReadyCount, restarts: map[string]int32{}}
	for _, ci := range info.Containers {
		if !ci.Init {
			cur.restarts[ci.Name] = ci.RestartCount
		}
	}
	w.pods[p.Name] = cur
	w.touch(ref)
	if !emit {
		return
	}
	base := application.NamespaceChange{Workload: ref, Pod: p.Name, Phase: cur.phase}
	if !had {
		base.Type = application.ChangePodAdded
		w.send(ctx, base)
		return
	}
	restarted := false
	for _, ci := range info.Containers {
		if ci.Init || ci.RestartCount <= old.restarts[ci.Name] {
			continue
		}
		ch := base
		ch.Type, ch.Container, ch.Reason, ch.ExitCode = application.ChangePodRestarted, ci.Name, ci.LastReason, ci.LastExitCode
		w.send(ctx, ch)
		restarted = true
	}
	if !restarted && (cur.phase != old.phase || cur.ready != old.ready) {
		base.Type = application.ChangePodUpdated
		w.send(ctx, base)
	}
}

func (w *nsWatch) podGone(ctx context.Context, name string, emit bool) {
	old, ok := w.pods[name]
	if !ok {
		return
	}
	delete(w.pods, name)
	w.touch(old.workload)
	if emit {
		w.send(ctx, application.NamespaceChange{Workload: old.workload, Type: application.ChangePodRemoved, Pod: name})
	}
}

func (w *nsWatch) eventChanged(ctx context.Context, e *corev1.Event) {
	obj := e.InvolvedObject
	if e.Type != corev1.EventTypeWarning && notableNormal[e.Reason] != obj.Kind {
		return
	}
	now := w.a.clock.Now()
	if last, ok := w.seen[e.UID]; ok && now.Sub(last) < eventRepeatEvery {
		return
	}
	ref, ok := w.eventOwner(ctx, e)
	if !ok {
		return
	}
	if len(w.seen) >= maxSeenEvents {
		for uid, at := range w.seen {
			if now.Sub(at) >= eventRepeatEvery {
				delete(w.seen, uid)
			}
		}
	}
	w.seen[e.UID] = now
	info := eventInfo(e, obj.Kind+"/"+obj.Name)
	ch := application.NamespaceChange{Workload: ref, Type: application.ChangeEvent, Event: &info}
	if obj.Kind == "Pod" {
		ch.Pod = obj.Name
	}
	w.send(ctx, ch)
}

// touch makes a workload's status be read at the next ticks, for
// podFollowFor. CronJobs have no rollout.
func (w *nsWatch) touch(ref domain.WorkloadRef) {
	if !ref.Kind.Restartable() {
		return
	}
	st := w.workloads[ref]
	if st == nil {
		st = &workloadState{}
		w.workloads[ref] = st
	}
	st.due, st.until = true, w.a.clock.Now().Add(podFollowFor)
}

// read reads a workload's revision and status and, with emit, sends what
// changed: a new revision starts a rollout, which is followed until it
// completes or fails; other status changes are sent as ChangeWorkload. A
// workload that is gone is forgotten. Other errors leave it for the next
// pod change (or tick, while rolling).
func (w *nsWatch) read(ctx context.Context, ref domain.WorkloadRef, st *workloadState, emit bool) {
	st.due = false
	status, rev, err := w.a.rolloutState(ctx, w.c, application.KubeWorkloadTarget{Cluster: w.cluster, Namespace: w.ns, Workload: ref})
	if err != nil {
		if errors.Is(err, application.ErrNotFound) {
			delete(w.workloads, ref)
			if emit {
				w.send(ctx, application.NamespaceChange{Workload: ref, Type: application.ChangeWorkload})
			}
		}
		return
	}
	prev := *st
	st.known, st.revision, st.status = true, rev, status
	if !emit {
		return
	}
	now := w.a.clock.Now()
	change := func(t application.ChangeType) {
		s := status
		w.send(ctx, application.NamespaceChange{Workload: ref, Type: t, Status: &s})
	}
	switch {
	case !prev.known:
		change(application.ChangeWorkload)
	case rev != prev.revision && prev.revision != "":
		change(application.ChangeRolloutStarted)
		st.rolling = now
		w.settle(st, change)
	case !prev.rolling.IsZero():
		w.settle(st, change)
		if st.status != prev.status && !st.rolling.IsZero() {
			change(application.ChangeWorkload)
		}
	case status != prev.status:
		change(application.ChangeWorkload)
	}
	if !st.rolling.IsZero() && now.Sub(st.rolling) > rolloutFollowMax {
		st.rolling = time.Time{}
	}
}

// baseline reads the revisions and statuses rollouts are told apart from,
// with one list per kind rather than one read per workload. A workload
// missing here is read on its first pod change (without rollout detection).
func (w *nsWatch) baseline(ctx context.Context) {
	lctx, cancel := callCtx(ctx)
	defer cancel()
	set := func(kind domain.WorkloadKind, name string, status application.RolloutStatus, rev string) {
		if st := w.workloads[domain.WorkloadRef{Kind: kind, Name: name}]; st != nil {
			st.known, st.status, st.revision, st.due, st.until = true, status, rev, false, time.Time{}
		}
	}
	if l, err := w.c.kube.AppsV1().Deployments(w.ns).List(lctx, metav1.ListOptions{}); err == nil {
		for i := range l.Items {
			o := &l.Items[i]
			set(domain.KindDeployment, o.Name, deploymentStatus(o), o.Annotations[deploymentRevision])
		}
	}
	if l, err := w.c.kube.AppsV1().StatefulSets(w.ns).List(lctx, metav1.ListOptions{}); err == nil {
		for i := range l.Items {
			o := &l.Items[i]
			set(domain.KindStatefulSet, o.Name, statefulSetStatus(o), o.Status.UpdateRevision)
		}
	}
	if l, err := w.c.kube.AppsV1().DaemonSets(w.ns).List(lctx, metav1.ListOptions{}); err == nil {
		for i := range l.Items {
			o := &l.Items[i]
			set(domain.KindDaemonSet, o.Name, daemonSetStatus(o), strconv.FormatInt(o.Generation, 10))
		}
	}
	if l, err := w.c.dyn.Resource(rolloutGVR).Namespace(w.ns).List(lctx, metav1.ListOptions{}); err == nil {
		for i := range l.Items {
			o := &l.Items[i]
			set(domain.KindRollout, o.GetName(), rolloutStatusOf(o), rolloutRevision(o))
		}
	}
}

// settle ends a running rollout that completed or failed.
func (w *nsWatch) settle(st *workloadState, change func(application.ChangeType)) {
	switch {
	case st.status.Complete:
		st.rolling = time.Time{}
		change(application.ChangeRolloutComplete)
	case st.status.Failed:
		st.rolling = time.Time{}
		change(application.ChangeRolloutFailed)
	}
}

func (w *nsWatch) send(ctx context.Context, ch application.NamespaceChange) {
	select {
	case w.out <- ch:
	case <-ctx.Done():
	}
}

// podOwner resolves a pod's workload like podBelongs: Pod -> StatefulSet or
// DaemonSet, Pod -> ReplicaSet -> Deployment or Rollout, Pod -> Job -> CronJob.
func (w *nsWatch) podOwner(ctx context.Context, p *corev1.Pod) (domain.WorkloadRef, bool) {
	o := metav1.GetControllerOf(p)
	if o == nil {
		return domain.WorkloadRef{}, false
	}
	switch o.Kind {
	case replicaSetGK.Kind:
		return w.via(ctx, replicaSetGK, o.APIVersion, o.Name, domain.KindDeployment, domain.KindRollout)
	case jobGK.Kind:
		return w.via(ctx, jobGK, o.APIVersion, o.Name, domain.KindCronJob)
	}
	return directRef(o.APIVersion, o.Kind, o.Name, domain.KindStatefulSet, domain.KindDaemonSet)
}

// eventOwner resolves the workload an event is about: a known pod's, a
// ReplicaSet's or Job's owner, or the workload itself.
func (w *nsWatch) eventOwner(ctx context.Context, e *corev1.Event) (domain.WorkloadRef, bool) {
	o := e.InvolvedObject
	switch o.Kind {
	case "Pod":
		p, ok := w.pods[o.Name]
		return p.workload, ok
	case replicaSetGK.Kind:
		return w.via(ctx, replicaSetGK, o.APIVersion, o.Name, domain.KindDeployment, domain.KindRollout)
	case jobGK.Kind:
		return w.via(ctx, jobGK, o.APIVersion, o.Name, domain.KindCronJob)
	}
	return directRef(o.APIVersion, o.Kind, o.Name, domain.AllWorkloadKinds...)
}

// via resolves a ReplicaSet or Job to the workload controlling it (one of
// kinds), reading it once and remembering the answer.
func (w *nsWatch) via(ctx context.Context, gk schema.GroupKind, apiVersion, name string, kinds ...domain.WorkloadKind) (domain.WorkloadRef, bool) {
	gv, err := schema.ParseGroupVersion(apiVersion)
	if err != nil || gv.Group != gk.Group || validObjectName(gk.Kind, name) != nil {
		return domain.WorkloadRef{}, false
	}
	key := gk.Kind + "/" + name
	if ref, ok := w.owners[key]; ok {
		return ref, ref.Name != ""
	}
	gctx, cancel := callCtx(ctx)
	defer cancel()
	var owner *metav1.OwnerReference
	if gk == replicaSetGK {
		rs, gerr := w.c.kube.AppsV1().ReplicaSets(w.ns).Get(gctx, name, metav1.GetOptions{})
		if err = gerr; err == nil {
			owner = metav1.GetControllerOf(rs)
		}
	} else {
		job, gerr := w.c.kube.BatchV1().Jobs(w.ns).Get(gctx, name, metav1.GetOptions{})
		if err = gerr; err == nil {
			owner = metav1.GetControllerOf(job)
		}
	}
	if err = w.a.fail("get "+gk.Kind, w.cluster, err); err != nil && !errors.Is(err, application.ErrNotFound) {
		return domain.WorkloadRef{}, false // not remembered: read again next time
	}
	var ref domain.WorkloadRef
	if owner != nil {
		ref, _ = directRef(owner.APIVersion, owner.Kind, owner.Name, kinds...)
	}
	if len(w.owners) >= maxOwnerCache {
		clear(w.owners)
	}
	w.owners[key] = ref
	return ref, ref.Name != ""
}

// directRef is the workload an owner reference or involved object names,
// when it is one of kinds (group and kind both checked).
func directRef(apiVersion, kind, name string, kinds ...domain.WorkloadKind) (domain.WorkloadRef, bool) {
	gv, err := schema.ParseGroupVersion(apiVersion)
	if err != nil || validObjectName(kind, name) != nil {
		return domain.WorkloadRef{}, false
	}
	for _, k := range kinds {
		if gk := workloadGK(k); gk.Kind == kind && gk.Group == gv.Group {
			return domain.WorkloadRef{Kind: k, Name: name}, true
		}
	}
	return domain.WorkloadRef{}, false
}
