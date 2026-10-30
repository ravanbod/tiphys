package kubeaccess

import (
	"context"
	"sort"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"tiphys/internal/application"
)

const maxEvents = 200

// ListPods lists the pods whose owner chain leads to the workload.
func (a *Access) ListPods(ctx context.Context, actor application.KubeActor, w application.KubeWorkloadTarget) ([]application.PodInfo, error) {
	c, err := a.openWorkload(actor, w)
	if err != nil {
		return nil, err
	}
	ctx, cancel := callCtx(ctx)
	defer cancel()
	if _, err := a.getWorkload(ctx, c, w); err != nil {
		return nil, err
	}
	pods, err := a.ownedPods(ctx, c, w)
	if err != nil {
		return nil, err
	}
	out := make([]application.PodInfo, 0, len(pods))
	for i := range pods {
		out = append(out, podInfo(&pods[i]))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// Events returns the events of the workload, its ReplicaSets or Jobs, and
// its pods, newest first.
func (a *Access) Events(ctx context.Context, actor application.KubeActor, w application.KubeWorkloadTarget) ([]application.EventInfo, error) {
	c, err := a.openWorkload(actor, w)
	if err != nil {
		return nil, err
	}
	ctx, cancel := callCtx(ctx)
	defer cancel()
	if _, err := a.getWorkload(ctx, c, w); err != nil {
		return nil, err
	}
	pods, err := a.ownedPods(ctx, c, w)
	if err != nil {
		return nil, err
	}
	involved := map[string]bool{string(w.Workload.Kind) + "/" + w.Workload.Name: true}
	for i := range pods {
		involved["Pod/"+pods[i].Name] = true
		if o := metav1.GetControllerOf(&pods[i]); o != nil {
			involved[o.Kind+"/"+o.Name] = true
		}
	}
	return a.listEvents(ctx, c, w.Cluster, w.Namespace, func(obj string) bool { return involved[obj] })
}

// NamespaceEvents returns the newest events of every object in the namespace.
func (a *Access) NamespaceEvents(ctx context.Context, actor application.KubeActor, cluster, namespace string) ([]application.EventInfo, error) {
	c, err := a.open(actor, cluster, namespace)
	if err != nil {
		return nil, err
	}
	ctx, cancel := callCtx(ctx)
	defer cancel()
	return a.listEvents(ctx, c, cluster, namespace, func(string) bool { return true })
}

// listEvents lists the namespace's events whose object ("Kind/name") keep
// accepts, newest first, at most maxEvents.
func (a *Access) listEvents(ctx context.Context, c *clients, cluster, namespace string, keep func(object string) bool) ([]application.EventInfo, error) {
	list, err := c.kube.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, a.fail("list events", cluster, err)
	}
	var out []application.EventInfo
	for i := range list.Items {
		e := &list.Items[i]
		obj := e.InvolvedObject.Kind + "/" + e.InvolvedObject.Name
		if keep(obj) {
			out = append(out, eventInfo(e, obj))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastSeen.After(out[j].LastSeen) })
	if len(out) > maxEvents {
		out = out[:maxEvents]
	}
	return out, nil
}

func eventInfo(e *corev1.Event, object string) application.EventInfo {
	first, last := e.FirstTimestamp.Time, e.LastTimestamp.Time
	if last.IsZero() {
		last = e.EventTime.Time
	}
	if last.IsZero() {
		last = e.CreationTimestamp.Time
	}
	if first.IsZero() {
		first = last
	}
	count := e.Count
	if count == 0 {
		count = 1
	}
	return application.EventInfo{Type: e.Type, Reason: e.Reason, Message: e.Message, Object: object, Count: count, FirstSeen: first, LastSeen: last}
}

func podInfo(p *corev1.Pod) application.PodInfo {
	info := application.PodInfo{
		Name:      p.Name,
		Phase:     string(p.Status.Phase),
		Node:      p.Spec.NodeName,
		CreatedAt: p.CreationTimestamp.Time,
	}
	if p.DeletionTimestamp != nil {
		info.Phase = "Terminating"
	}
	limits, images := map[string]string{}, map[string]string{}
	for _, c := range append(append([]corev1.Container{}, p.Spec.InitContainers...), p.Spec.Containers...) {
		images[c.Name] = c.Image
		if m, ok := c.Resources.Limits[corev1.ResourceMemory]; ok {
			limits[c.Name] = m.String()
		}
	}
	add := func(statuses []corev1.ContainerStatus, specs []corev1.Container, init bool) {
		seen := map[string]bool{}
		for _, s := range statuses {
			seen[s.Name] = true
			ci := containerInfo(s, init)
			if img := images[s.Name]; img != "" {
				ci.Image = img // as configured, not the resolved digest reference
			}
			ci.MemoryLimit = limits[s.Name]
			info.Containers = append(info.Containers, ci)
			if !init {
				info.TotalCount++
				if s.Ready {
					info.ReadyCount++
				}
				info.Restarts += s.RestartCount
				if t := s.LastTerminationState.Terminated; t != nil && (info.LastTerminatedAt == nil || t.FinishedAt.After(*info.LastTerminatedAt)) {
					info.LastTermination = t.Reason
					info.LastTerminatedAt = timePtr(t.FinishedAt)
				}
			}
		}
		for _, c := range specs { // containers that have no status yet
			if !seen[c.Name] {
				info.Containers = append(info.Containers, application.ContainerInfo{
					Name: c.Name, Image: c.Image, State: "waiting", StateReason: "Pending", Init: init, MemoryLimit: limits[c.Name],
				})
				if !init {
					info.TotalCount++
				}
			}
		}
	}
	add(p.Status.InitContainerStatuses, p.Spec.InitContainers, true)
	add(p.Status.ContainerStatuses, p.Spec.Containers, false)
	return info
}

func containerInfo(s corev1.ContainerStatus, init bool) application.ContainerInfo {
	ci := application.ContainerInfo{Name: s.Name, Image: s.Image, Ready: s.Ready, RestartCount: s.RestartCount, Init: init}
	switch {
	case s.State.Running != nil:
		ci.State = "running"
	case s.State.Terminated != nil:
		ci.State, ci.StateReason = "terminated", s.State.Terminated.Reason
	case s.State.Waiting != nil:
		ci.State, ci.StateReason = "waiting", s.State.Waiting.Reason
	}
	if t := s.LastTerminationState.Terminated; t != nil {
		ci.LastState, ci.LastReason = "terminated", t.Reason
		code := t.ExitCode
		ci.LastExitCode = &code
		ci.LastFinished = timePtr(t.FinishedAt)
		ci.LastStarted = timePtr(t.StartedAt)
	}
	return ci
}

// containerNames lists the regular and init containers of a pod.
func containerNames(p *corev1.Pod, includeInit bool) map[string]bool {
	out := map[string]bool{}
	for _, c := range p.Spec.Containers {
		out[c.Name] = true
	}
	if includeInit {
		for _, c := range p.Spec.InitContainers {
			out[c.Name] = true
		}
	}
	return out
}
