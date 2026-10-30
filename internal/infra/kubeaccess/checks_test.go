package kubeaccess

import (
	"context"
	"strings"
	"testing"
	"time"

	"tiphys/internal/application"
	"tiphys/internal/domain"
)

// Every verb refuses before it touches a client, so the fake must see no call.
func TestVerbsRefuseBadInputWithoutCallingKubernetes(t *testing.T) {
	r := newRig(t)
	r.debugImage = testDebugImage
	ctx := context.Background()
	deploy := func(mut func(*application.KubeWorkloadTarget)) application.KubeWorkloadTarget {
		w := target(domain.KindDeployment, "web")
		mut(&w)
		return w
	}
	type verb struct {
		name string
		call func(w application.KubeWorkloadTarget, a application.KubeActor) error
	}
	verbs := []verb{
		{"GetWorkload", func(w application.KubeWorkloadTarget, a application.KubeActor) error {
			_, err := r.GetWorkload(ctx, a, w)
			return err
		}},
		{"ListPods", func(w application.KubeWorkloadTarget, a application.KubeActor) error {
			_, err := r.ListPods(ctx, a, w)
			return err
		}},
		{"Events", func(w application.KubeWorkloadTarget, a application.KubeActor) error {
			_, err := r.Events(ctx, a, w)
			return err
		}},
		{"Logs", func(w application.KubeWorkloadTarget, a application.KubeActor) error {
			_, err := r.Logs(ctx, a, w, "web-1", application.LogOptions{Container: "app", TailLines: 100})
			return err
		}},
		{"Exec", func(w application.KubeWorkloadTarget, a application.KubeActor) error {
			_, err := r.Exec(ctx, a, w, "web-1", "app", "/bin/sh", 80, 24)
			return err
		}},
		{"Debug", func(w application.KubeWorkloadTarget, a application.KubeActor) error {
			_, err := r.Debug(ctx, a, w, "web-1", debugSpec, "/bin/sh", 80, 24)
			return err
		}},
		{"Restart", func(w application.KubeWorkloadTarget, a application.KubeActor) error { return r.Restart(ctx, a, w) }},
		{"WatchRollout", func(w application.KubeWorkloadTarget, a application.KubeActor) error {
			_, err := r.WatchRollout(ctx, a, w, time.Second)
			return err
		}},
		{"ListWorkloads", func(w application.KubeWorkloadTarget, a application.KubeActor) error {
			_, err := r.ListWorkloads(ctx, a, w.Cluster, w.Namespace)
			return err
		}},
		{"NamespaceEvents", func(w application.KubeWorkloadTarget, a application.KubeActor) error {
			_, err := r.NamespaceEvents(ctx, a, w.Cluster, w.Namespace)
			return err
		}},
		{"NamespaceTraffic", func(w application.KubeWorkloadTarget, a application.KubeActor) error {
			_, err := r.NamespaceTraffic(ctx, a, w.Cluster, w.Namespace)
			return err
		}},
		{"ListCronJobs", func(w application.KubeWorkloadTarget, a application.KubeActor) error {
			_, err := r.ListCronJobs(ctx, a, w.Cluster, w.Namespace)
			return err
		}},
		{"RunCronJob", func(w application.KubeWorkloadTarget, a application.KubeActor) error {
			_, err := r.RunCronJob(ctx, a, w.Cluster, w.Namespace, "report")
			return err
		}},
		{"WatchJob", func(w application.KubeWorkloadTarget, a application.KubeActor) error {
			_, err := r.WatchJob(ctx, a, w.Cluster, w.Namespace, "report-1", time.Second)
			return err
		}},
	}
	cases := []struct {
		name string
		w    application.KubeWorkloadTarget
		a    application.KubeActor
		want error
	}{
		{"protected namespace", deploy(func(w *application.KubeWorkloadTarget) { w.Namespace = "kube-system" }), actor, application.ErrProtectedNamespace},
		{"namespace with uppercase", deploy(func(w *application.KubeWorkloadTarget) { w.Namespace = "Shop" }), actor, application.ErrInvalidName},
		{"namespace with selector chars", deploy(func(w *application.KubeWorkloadTarget) { w.Namespace = "shop,x=y" }), actor, application.ErrInvalidName},
		{"empty namespace", deploy(func(w *application.KubeWorkloadTarget) { w.Namespace = "" }), actor, application.ErrInvalidName},
		{"cluster name with slash", deploy(func(w *application.KubeWorkloadTarget) { w.Cluster = "c1/../x" }), actor, application.ErrInvalidName},
		{"unknown cluster", deploy(func(w *application.KubeWorkloadTarget) { w.Cluster = "nope" }), actor, application.ErrClusterUnknown},
		{"workload name with path", deploy(func(w *application.KubeWorkloadTarget) { w.Workload.Name = "web/../../secrets" }), actor, application.ErrInvalidName},
		{"empty workload name", deploy(func(w *application.KubeWorkloadTarget) { w.Workload.Name = "" }), actor, application.ErrInvalidName},
		{"unlisted kind", deploy(func(w *application.KubeWorkloadTarget) { w.Workload.Kind = "Secret" }), actor, application.ErrInvalidName},
		{"actor without username", deploy(func(*application.KubeWorkloadTarget) {}), application.KubeActor{}, application.ErrInvalidName},
		{"actor with newline", deploy(func(*application.KubeWorkloadTarget) {}), application.KubeActor{Username: "a\nb"}, application.ErrInvalidName},
	}
	for _, v := range verbs {
		for _, c := range cases {
			t.Run(v.name+"/"+c.name, func(t *testing.T) {
				// Verbs that take no workload ignore its fields; skip the cases that cannot apply.
				if strings.HasPrefix(c.name, "workload") || c.name == "unlisted kind" || c.name == "empty workload name" {
					switch v.name {
					case "ListWorkloads", "NamespaceEvents", "NamespaceTraffic", "ListCronJobs", "RunCronJob", "WatchJob":
						t.Skip("verb does not take a workload")
					}
				}
				r.kube.ClearActions()
				wantErr(t, v.call(c.w, c.a), c.want)
				if n := len(r.kube.Actions()); n != 0 {
					t.Fatalf("kubernetes was called %d times: %v", n, r.kube.Actions())
				}
			})
		}
	}
}

func TestUnusableClusterRefusesEveryVerb(t *testing.T) {
	r := newRig(t, deployment("web"))
	r.SetUsable("c1", false)
	_, err := r.ListWorkloads(context.Background(), actor, "c1", testNS)
	wantErr(t, err, application.ErrClusterUnusable)
	r.SetUsable("c1", true)
	if _, err := r.ListWorkloads(context.Background(), actor, "c1", testNS); err != nil {
		t.Fatal(err)
	}
}

func TestSetUsableCannotOverridePrivilegeFailure(t *testing.T) {
	r := newRig(t)
	r.clusters["c1"].privilegeOK.Store(false)
	r.SetUsable("c1", true)
	_, err := r.ListWorkloads(context.Background(), actor, "c1", testNS)
	wantErr(t, err, application.ErrClusterUnusable)
}

func TestRestartRefusesNonRestartableKinds(t *testing.T) {
	r := newRig(t)
	wantErr(t, r.Restart(context.Background(), actor, target(domain.KindCronJob, "report")), application.ErrInvalidName)
}

func TestExecRefusesShellOutsideAllowList(t *testing.T) {
	r := newRig(t)
	for _, sh := range []string{"", "sh", "/bin/zsh", "/bin/sh -c id", "/bin/sh;id"} {
		_, err := r.Exec(context.Background(), actor, target(domain.KindDeployment, "web"), "web-1", "app", sh, 80, 24)
		wantErr(t, err, application.ErrInvalidName)
	}
}

func TestLogsRefusesUnlistedOptions(t *testing.T) {
	r := newRig(t)
	w := target(domain.KindDeployment, "web")
	for _, o := range []application.LogOptions{
		{Container: "app", TailLines: 5},
		{Container: "app", TailLines: 100, SinceSeconds: 7},
		{Container: "app", TailLines: 100, LimitBytes: -1},
		{Container: "App", TailLines: 100},
		{Container: "", TailLines: 100},
	} {
		_, err := r.Logs(context.Background(), actor, w, "web-1", o)
		wantErr(t, err, application.ErrInvalidName)
	}
}
