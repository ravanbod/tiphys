package kubeaccess

// Opt-in tests against a real cluster. They need:
//
//	TIPHYS_TEST_KUBECONFIG        kubeconfig of the tiphys-connector ServiceAccount (bearer token)
//	TIPHYS_TEST_ADMIN_KUBECONFIG  an admin kubeconfig, for fixtures checks and the over-privilege test (optional)
//	TIPHYS_TEST_NAMESPACE         namespace with testdata/e2e-fixtures.yaml applied (default e2e)
//	TIPHYS_TEST_ADMISSION         admission mode the chart was installed with: vap (default), kyverno, none
//	TIPHYS_TEST_DEBUG_IMAGE       the chart's debug.image, when it was installed with debug.enabled (debug tests skip otherwise)
//
// The cluster must have deploy/chart/tiphys-connector installed. See docs/kubeaccess.md.

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	authenticationv1 "k8s.io/api/authentication/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/utils/ptr"

	"tiphys/internal/application"
	"tiphys/internal/domain"
)

type e2e struct {
	a         *Access
	ns        string
	connector []byte
	asUser    *kubernetes.Clientset // tiphys:user, impersonated directly, bypassing kubeaccess
	asUserCfg *rest.Config
	admin     *kubernetes.Clientset // may be nil
	adminCfg  *rest.Config
}

func newE2E(t *testing.T) *e2e {
	t.Helper()
	path := os.Getenv("TIPHYS_TEST_KUBECONFIG")
	if path == "" {
		t.Skip("TIPHYS_TEST_KUBECONFIG not set")
	}
	kc, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := parseKubeconfig(kc)
	if err != nil {
		t.Fatalf("connector kubeconfig: %v", err)
	}
	e := &e2e{ns: "e2e", connector: kc}
	if v := os.Getenv("TIPHYS_TEST_NAMESPACE"); v != "" {
		e.ns = v
	}
	reg := NewRegistry(&memStore{creds: map[string][]byte{"kind": kc}}, zap.NewNop())
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go reg.Run(ctx)
	e.a = New(reg, protectedSet{"kube-system": true, "tiphys": true}, zap.NewNop(), WithDebugImage(os.Getenv("TIPHYS_TEST_DEBUG_IMAGE")))
	eventually(t, "cluster usable (privilege check passed)", func() bool { _, err := reg.lookup("kind"); return err == nil })

	e.asUserCfg = rest.CopyConfig(cfg)
	e.asUserCfg.Impersonate = e.a.impersonation(application.KubeActor{Username: "e2e-direct"})
	if e.asUser, err = kubernetes.NewForConfig(e.asUserCfg); err != nil {
		t.Fatal(err)
	}
	if ap := os.Getenv("TIPHYS_TEST_ADMIN_KUBECONFIG"); ap != "" {
		if e.adminCfg, err = clientcmd.BuildConfigFromFlags("", ap); err != nil {
			t.Fatal(err)
		}
		if e.admin, err = kubernetes.NewForConfig(e.adminCfg); err != nil {
			t.Fatal(err)
		}
	}
	return e
}

func (e *e2e) needAdmin(t *testing.T) {
	t.Helper()
	if e.admin == nil {
		t.Skip("TIPHYS_TEST_ADMIN_KUBECONFIG not set")
	}
}

func (e *e2e) target(kind domain.WorkloadKind, name string) application.KubeWorkloadTarget {
	return application.KubeWorkloadTarget{Cluster: "kind", Namespace: e.ns, Workload: domain.WorkloadRef{Kind: kind, Name: name}}
}

var e2eActor = application.KubeActor{Username: "e2e-user", BreakGlassID: ptr.To(int64(7))}

func (e *e2e) runningPod(t *testing.T, kind domain.WorkloadKind, name string) application.PodInfo {
	t.Helper()
	var found application.PodInfo
	eventually2(t, 120*time.Second, "running pod of "+name, func() bool {
		pods, err := e.a.ListPods(context.Background(), e2eActor, e.target(kind, name))
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range pods {
			if p.Phase == "Running" && p.ReadyCount == p.TotalCount && p.TotalCount > 0 {
				found = p
				return true
			}
		}
		return false
	})
	return found
}

func eventually2(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestE2EListWorkloadsAndPods(t *testing.T) {
	e := newE2E(t)
	ws, err := e.a.ListWorkloads(context.Background(), e2eActor, "kind", e.ns)
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, w := range ws {
		have[w.Ref.String()] = true
	}
	for _, want := range []string{"Deployment/web", "Deployment/other", "StatefulSet/db", "DaemonSet/agent", "CronJob/report", "CronJob/quick"} {
		if !have[want] {
			t.Errorf("missing %s in %v", want, have)
		}
	}
	web := e.runningPod(t, domain.KindDeployment, "web")
	if web.Containers[0].Image != "busybox:1.36" {
		t.Fatalf("pod = %+v", web)
	}
	// The pod of another Deployment never shows up for this one.
	pods, _ := e.a.ListPods(context.Background(), e2eActor, e.target(domain.KindDeployment, "web"))
	for _, p := range pods {
		if strings.HasPrefix(p.Name, "other-") {
			t.Fatalf("foreign pod listed: %s", p.Name)
		}
	}
	if _, err := e.a.Events(context.Background(), e2eActor, e.target(domain.KindDeployment, "web")); err != nil {
		t.Fatal(err)
	}
}

func TestE2ELogs(t *testing.T) {
	e := newE2E(t)
	p := e.runningPod(t, domain.KindDeployment, "web")
	rc, err := e.a.Logs(context.Background(), e2eActor, e.target(domain.KindDeployment, "web"), p.Name,
		application.LogOptions{Container: "app", TailLines: 100})
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	b, _ := io.ReadAll(rc)
	if !bytes.Contains(b, []byte("hello-from-web")) {
		t.Fatalf("log = %q", b)
	}
	// follow: stops when the context is cancelled
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	rc2, err := e.a.Logs(ctx, e2eActor, e.target(domain.KindDeployment, "web"), p.Name,
		application.LogOptions{Container: "app", Follow: true, TailLines: 100, Timestamps: true})
	if err != nil {
		t.Fatal(err)
	}
	defer rc2.Close()
	buf := make([]byte, 4096)
	if n, err := rc2.Read(buf); n == 0 || err != nil {
		t.Fatalf("follow read: %d %v", n, err)
	}
	// A pod of another workload is refused.
	other := e.runningPod(t, domain.KindDeployment, "other")
	_, err = e.a.Logs(context.Background(), e2eActor, e.target(domain.KindDeployment, "web"), other.Name,
		application.LogOptions{Container: "app", TailLines: 100})
	wantErr(t, err, application.ErrPodNotInWorkload)
}

func TestE2EExec(t *testing.T) {
	e := newE2E(t)
	p := e.runningPod(t, domain.KindDeployment, "web")
	s, err := e.a.Exec(context.Background(), e2eActor, e.target(domain.KindDeployment, "web"), p.Name, "app", "/bin/sh", 80, 24)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var mu sync.Mutex
	var acc bytes.Buffer
	go func() { // drain until the session ends, as the portal does
		buf := make([]byte, 1024)
		for {
			n, err := s.Output().Read(buf)
			mu.Lock()
			acc.Write(buf[:n])
			mu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	output := func() string { mu.Lock(); defer mu.Unlock(); return acc.String() }
	time.Sleep(300 * time.Millisecond)
	s.Resize(100, 30)
	time.Sleep(300 * time.Millisecond)
	io.WriteString(s, "echo tiphys-$((6*7)); stty size\n")
	eventually2(t, 20*time.Second, "exec output", func() bool {
		return strings.Contains(output(), "tiphys-42") && strings.Contains(output(), "30 100")
	})
	io.WriteString(s, "exit\n")
	select {
	case <-s.Done():
		if err := s.Err(); err != nil {
			t.Fatalf("exec ended with %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("exec did not end after exit")
	}
	// Pod of another workload / disallowed shell are refused before connecting.
	other := e.runningPod(t, domain.KindDeployment, "other")
	_, err = e.a.Exec(context.Background(), e2eActor, e.target(domain.KindDeployment, "web"), other.Name, "app", "/bin/sh", 80, 24)
	wantErr(t, err, application.ErrPodNotInWorkload)
}

func TestE2ERestart(t *testing.T) {
	e := newE2E(t)
	kinds := []struct {
		kind domain.WorkloadKind
		name string
	}{{domain.KindDeployment, "web"}, {domain.KindStatefulSet, "db"}, {domain.KindDaemonSet, "agent"}, {domain.KindRollout, "canary"}}
	for _, k := range kinds {
		t.Run(string(k.kind), func(t *testing.T) {
			ctx := context.Background()
			if k.kind == domain.KindRollout {
				if _, err := e.a.GetWorkload(ctx, e2eActor, e.target(k.kind, k.name)); err != nil {
					t.Skipf("no Argo Rollouts in this cluster: %v", err)
				}
			}
			w := e.target(k.kind, k.name)
			if err := e.a.Restart(ctx, e2eActor, w); err != nil {
				t.Fatalf("the cluster rejected the impersonated patch: %v", err)
			}
			ch, err := e.a.WatchRollout(ctx, e2eActor, w, 150*time.Second)
			if err != nil {
				t.Fatal(err)
			}
			var last application.RolloutStatus
			n := 0
			for s := range ch {
				last, n = s, n+1
			}
			if !last.Complete || last.Failed {
				t.Fatalf("rollout did not complete after %d statuses: %+v", n, last)
			}
			if e.admin == nil || k.kind == domain.KindRollout {
				return
			}
			// The write really came through kubeaccess: the managed-fields entry carries its user agent.
			var mf []metav1.ManagedFieldsEntry
			switch k.kind {
			case domain.KindDeployment:
				d, _ := e.admin.AppsV1().Deployments(e.ns).Get(ctx, k.name, metav1.GetOptions{})
				mf = d.ManagedFields
			case domain.KindStatefulSet:
				d, _ := e.admin.AppsV1().StatefulSets(e.ns).Get(ctx, k.name, metav1.GetOptions{})
				mf = d.ManagedFields
			case domain.KindDaemonSet:
				d, _ := e.admin.AppsV1().DaemonSets(e.ns).Get(ctx, k.name, metav1.GetOptions{})
				mf = d.ManagedFields
			}
			var ok bool
			for _, f := range mf {
				ok = ok || f.Manager == userAgent
			}
			if !ok {
				t.Fatalf("no managed-fields entry from %s: %v", userAgent, mf)
			}
		})
	}
}

func TestE2EWatchNamespaceSeesARollout(t *testing.T) {
	e := newE2E(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	ch, err := e.a.WatchNamespace(ctx, e2eActor, "kind", e.ns)
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	web := domain.WorkloadRef{Kind: domain.KindDeployment, Name: "web"}
	if err := e.a.Restart(ctx, e2eActor, e.target(web.Kind, web.Name)); err != nil {
		t.Fatal(err)
	}
	seen := map[application.ChangeType]bool{}
	for c := range ch {
		if c.Workload != web {
			continue
		}
		seen[c.Type] = true
		if c.Type == application.ChangeRolloutComplete {
			break
		}
	}
	for _, want := range []application.ChangeType{application.ChangePodAdded, application.ChangePodRemoved, application.ChangeRolloutStarted, application.ChangeRolloutComplete} {
		if !seen[want] {
			t.Errorf("no %s for %s (saw %v)", want, web, seen)
		}
	}
}

func TestE2ERunCronJob(t *testing.T) {
	e := newE2E(t)
	ctx := context.Background()
	var created []string
	t.Cleanup(func() {
		for _, n := range created {
			if e.admin != nil {
				e.admin.BatchV1().Jobs(e.ns).Delete(ctx, n, metav1.DeleteOptions{PropagationPolicy: ptr.To(metav1.DeletePropagationBackground)})
			}
		}
	})

	name, err := e.a.RunCronJob(ctx, e2eActor, "kind", e.ns, "quick")
	if err != nil {
		t.Fatalf("the cluster rejected the Job: %v", err)
	}
	created = append(created, name)
	ch, err := e.a.WatchJob(ctx, e2eActor, "kind", e.ns, name, 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	var last application.JobInfo
	for j := range ch {
		last = j
	}
	if last.Status != "succeeded" || !last.Manual || last.CronJob != "quick" || last.TriggeredBy != "e2e-user" {
		t.Fatalf("job = %+v", last)
	}
	if e.admin != nil {
		j, err := e.admin.BatchV1().Jobs(e.ns).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			t.Fatal(err)
		}
		o := metav1.GetControllerOf(j)
		if o == nil || o.Kind != "CronJob" || o.Name != "quick" || j.Annotations[instantiateKey] != "manual" {
			t.Fatalf("job = %+v", j.ObjectMeta)
		}
	}

	// Forbid: the second run while the first is active is refused.
	first, err := e.a.RunCronJob(ctx, e2eActor, "kind", e.ns, "report")
	if err != nil {
		t.Fatal(err)
	}
	created = append(created, first)
	_, err = e.a.RunCronJob(ctx, e2eActor, "kind", e.ns, "report")
	wantErr(t, err, application.ErrAlreadyRunning)
	cjs, err := e.a.ListCronJobs(ctx, e2eActor, "kind", e.ns)
	if err != nil {
		t.Fatal(err)
	}
	var seen bool
	for _, c := range cjs {
		for _, j := range c.RecentJobs {
			seen = seen || (j.Name == first && j.Manual)
		}
	}
	if !seen {
		t.Fatalf("manual job %s missing from ListCronJobs", first)
	}
}

func TestE2EProtectedNamespaceRefusedByKubeaccess(t *testing.T) {
	e := newE2E(t)
	_, err := e.a.ListWorkloads(context.Background(), e2eActor, "kind", "kube-system")
	wantErr(t, err, application.ErrProtectedNamespace)
}

// ---- Defense in depth: call the API server directly as tiphys:user ----

func TestE2EAdmissionRejectsImageChange(t *testing.T) {
	e := newE2E(t)
	_, err := e.asUser.AppsV1().Deployments(e.ns).Patch(context.Background(), "web", types.StrategicMergePatchType,
		[]byte(`{"spec":{"template":{"spec":{"containers":[{"name":"app","image":"evil:latest"}]}}}}`), metav1.PatchOptions{})
	mustBeAdmissionDenied(t, err, "tiphys-restart-only")
	// ... and so does every other change to a workload.
	for _, body := range []string{
		`{"spec":{"replicas":9}}`,
		`{"spec":{"template":{"metadata":{"annotations":{"other":"x"}}}}}`,
		`{"metadata":{"labels":{"x":"y"}}}`,
	} {
		_, err := e.asUser.AppsV1().Deployments(e.ns).Patch(context.Background(), "web", types.StrategicMergePatchType, []byte(body), metav1.PatchOptions{})
		mustBeAdmissionDenied(t, err, "tiphys-restart-only")
	}
	// The restart annotation alone passes.
	_, err = e.asUser.AppsV1().Deployments(e.ns).Patch(context.Background(), "web", types.StrategicMergePatchType,
		restartedAtPatch("2026-10-20T12:00:00Z"), metav1.PatchOptions{})
	if err != nil {
		t.Fatalf("restart patch rejected: %v", err)
	}
}

func TestE2EAdmissionRejectsJobWithoutCronJobOwner(t *testing.T) {
	e := newE2E(t)
	j := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: "rogue", Namespace: e.ns},
		Spec: batchv1.JobSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers:    []corev1.Container{{Name: "x", Image: "busybox:1.36", Command: []string{"id"}}},
		}}},
	}
	_, err := e.asUser.BatchV1().Jobs(e.ns).Create(context.Background(), j, metav1.CreateOptions{})
	mustBeAdmissionDenied(t, err, "tiphys-jobs-from-cronjobs")
	// An owner without the manual annotation is not enough either.
	j.OwnerReferences = ctrl("batch/v1", "CronJob", "quick")
	j.OwnerReferences[0].UID = "11111111-2222-3333-4444-555555555555"
	_, err = e.asUser.BatchV1().Jobs(e.ns).Create(context.Background(), j, metav1.CreateOptions{})
	mustBeAdmissionDenied(t, err, "tiphys-jobs-from-cronjobs")
}

func TestE2ESecretsAndPodCreationForbidden(t *testing.T) {
	e := newE2E(t)
	ctx := context.Background()
	_, err := e.asUser.CoreV1().Secrets(e.ns).List(ctx, metav1.ListOptions{})
	if !apierrors.IsForbidden(err) {
		t.Fatalf("list secrets: %v", err)
	}
	_, err = e.asUser.CoreV1().Secrets("kube-system").Get(ctx, "anything", metav1.GetOptions{})
	if !apierrors.IsForbidden(err) {
		t.Fatalf("get secret: %v", err)
	}
	_, err = e.asUser.CoreV1().ConfigMaps(e.ns).List(ctx, metav1.ListOptions{})
	if !apierrors.IsForbidden(err) {
		t.Fatalf("list configmaps: %v", err)
	}
	_, err = e.asUser.CoreV1().Pods(e.ns).Create(ctx, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p"}}, metav1.CreateOptions{})
	if !apierrors.IsForbidden(err) {
		t.Fatalf("create pod: %v", err)
	}
	err = e.asUser.AppsV1().Deployments(e.ns).Delete(ctx, "web", metav1.DeleteOptions{})
	if !apierrors.IsForbidden(err) {
		t.Fatalf("delete deployment: %v", err)
	}
}

func TestE2EConnectorCannotImpersonateAnyoneElse(t *testing.T) {
	e := newE2E(t)
	base, _ := parseKubeconfig(e.connector)
	try := func(name string, mut func(*rest.Config)) {
		t.Run(name, func(t *testing.T) {
			cfg := rest.CopyConfig(base)
			cfg.Impersonate = e.a.impersonation(application.KubeActor{Username: "x"})
			mut(cfg)
			cs, _ := kubernetes.NewForConfig(cfg)
			_, err := cs.CoreV1().Pods(e.ns).List(context.Background(), metav1.ListOptions{})
			if !apierrors.IsForbidden(err) || !strings.Contains(err.Error(), "impersonate") {
				t.Fatalf("err = %v", err)
			}
		})
	}
	try("system:admin", func(c *rest.Config) { c.Impersonate.UserName = "system:admin" })
	try("another service account", func(c *rest.Config) { c.Impersonate.UserName = "system:serviceaccount:kube-system:default" })
	try("group system:masters", func(c *rest.Config) { c.Impersonate.Groups = []string{"tiphys:users", "system:masters"} })
	try("unknown extra", func(c *rest.Config) { c.Impersonate.Extra = map[string][]string{"portal.example.com/role": {"admin"}} })
	try("extra under another domain", func(c *rest.Config) {
		c.Impersonate.Extra = map[string][]string{"evil.example.com/username": {"x"}}
	})
	// The connector itself has no workload access without impersonation.
	cs, _ := kubernetes.NewForConfig(base)
	if _, err := cs.CoreV1().Pods(e.ns).List(context.Background(), metav1.ListOptions{}); !apierrors.IsForbidden(err) {
		t.Fatalf("connector without impersonation read pods: %v", err)
	}
}

func TestE2EProtectedNamespacesRejectWritesAndExec(t *testing.T) {
	e := newE2E(t)
	ctx := context.Background()
	// Reads are fine at the cluster level (kubeaccess refuses them one layer up).
	pods, err := e.asUser.CoreV1().Pods("kube-system").List(ctx, metav1.ListOptions{})
	if err != nil || len(pods.Items) == 0 {
		t.Fatalf("read of kube-system pods: %v", err)
	}
	_, err = e.asUser.AppsV1().Deployments("kube-system").Patch(ctx, "coredns", types.StrategicMergePatchType,
		restartedAtPatch("2026-10-20T12:00:00Z"), metav1.PatchOptions{})
	mustBeAdmissionDenied(t, err, "tiphys-protected-namespaces")

	req := e.asUser.CoreV1().RESTClient().Post().Resource("pods").Namespace("kube-system").Name(pods.Items[0].Name).SubResource("exec").
		VersionedParams(&corev1.PodExecOptions{Command: []string{"/bin/sh"}, Stdin: true, Stdout: true, TTY: true}, scheme.ParameterCodec)
	ex, err := remotecommand.NewSPDYExecutor(e.asUserCfg, "POST", req.URL())
	if err != nil {
		t.Fatal(err)
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	err = ex.StreamWithContext(cctx, remotecommand.StreamOptions{Stdin: strings.NewReader("exit\n"), Stdout: io.Discard, Tty: true})
	if err == nil || !strings.Contains(err.Error(), "tiphys-protected-namespaces") && !strings.Contains(err.Error(), "protected") {
		t.Fatalf("exec in kube-system: %v", err)
	}
	// Writes in an unprotected namespace still work (the policy is not a blanket deny).
	if _, err := e.asUser.AppsV1().Deployments(e.ns).Patch(ctx, "other", types.StrategicMergePatchType,
		restartedAtPatch("2026-10-20T12:00:00Z"), metav1.PatchOptions{}); err != nil {
		t.Fatal(err)
	}
}

func TestE2EProbeOfConnectorKubeconfig(t *testing.T) {
	e := newE2E(t)
	p, err := e.a.ProbeKubeconfig(context.Background(), e.connector)
	if err != nil {
		t.Fatal(err)
	}
	if !p.PrivilegeOK || len(p.Violations) != 0 || !strings.HasPrefix(p.Version, "v1.") || p.AdmissionMode != wantAdmissionMode() {
		t.Fatalf("probe = %+v, want admission mode %s", p, wantAdmissionMode())
	}
	if v, err := e.a.Version(context.Background(), "kind"); err != nil || v != p.Version {
		t.Fatalf("version %q %v", v, err)
	}
}

func TestE2EPrivilegeCheckFlagsClusterAdminKubeconfig(t *testing.T) {
	e := newE2E(t)
	e.needAdmin(t)
	ctx := context.Background()
	sa, err := e.admin.CoreV1().ServiceAccounts(e.ns).Create(ctx, &corev1.ServiceAccount{ObjectMeta: metav1.ObjectMeta{GenerateName: "e2e-argocd-like-"}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	crb, err := e.admin.RbacV1().ClusterRoleBindings().Create(ctx, &rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{GenerateName: "e2e-argocd-like-"},
		RoleRef:    rbacv1.RoleRef{APIGroup: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: "cluster-admin"},
		Subjects:   []rbacv1.Subject{{Kind: "ServiceAccount", Name: sa.Name, Namespace: e.ns}},
	}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		e.admin.RbacV1().ClusterRoleBindings().Delete(ctx, crb.Name, metav1.DeleteOptions{})
		e.admin.CoreV1().ServiceAccounts(e.ns).Delete(ctx, sa.Name, metav1.DeleteOptions{})
	})
	tok, err := e.admin.CoreV1().ServiceAccounts(e.ns).CreateToken(ctx, sa.Name,
		&authenticationv1.TokenRequest{Spec: authenticationv1.TokenRequestSpec{ExpirationSeconds: ptr.To(int64(600))}}, metav1.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	base, _ := parseKubeconfig(e.connector)
	adminKC := kubeconfigYAML("    server: "+base.Host+"\n    certificate-authority-data: "+base64Std(base.CAData), "    token: "+tok.Status.Token)

	var p application.ClusterProbe
	eventually2(t, 30*time.Second, "RBAC to propagate", func() bool {
		p, err = e.a.ProbeKubeconfig(ctx, adminKC)
		return err == nil && !p.PrivilegeOK
	})
	if len(p.Violations) < 4 {
		t.Fatalf("violations = %v", p.Violations)
	}
	t.Logf("violations: %v", p.Violations)

	// A registry given this credential refuses to use the cluster.
	reg := NewRegistry(&memStore{creds: map[string][]byte{"admin": []byte(adminKC)}}, zap.NewNop())
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go reg.Run(cctx)
	eventually(t, "registration", func() bool { return len(reg.Registered()) == 1 })
	time.Sleep(2 * time.Second)
	a := New(reg, protectedSet{}, zap.NewNop())
	_, err = a.ListWorkloads(ctx, e2eActor, "admin", e.ns)
	wantErr(t, err, application.ErrClusterUnusable)
}

// wantAdmissionMode is vap unless TIPHYS_TEST_ADMISSION says otherwise (kyverno, none).
func wantAdmissionMode() domain.AdmissionMode {
	if v := os.Getenv("TIPHYS_TEST_ADMISSION"); v != "" {
		return domain.AdmissionMode(v)
	}
	return domain.AdmissionVAP
}

func mustBeAdmissionDenied(t *testing.T, err error, policy string) {
	t.Helper()
	if err == nil {
		t.Fatalf("request was accepted, want denial by %s", policy)
	}
	if !apierrors.IsForbidden(err) && !apierrors.IsInvalid(err) || !strings.Contains(err.Error(), policy) {
		t.Fatalf("error = %v, want denial by ValidatingAdmissionPolicy %s", err, policy)
	}
}
