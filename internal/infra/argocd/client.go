// Package argocd implements application.ArgoClient over the Argo CD REST API
// with a bearer token: it reads Applications, AppProjects and Clusters, and
// syncs single applications.
package argocd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"go.uber.org/zap"

	"tiphys/internal/application"
	"tiphys/internal/domain"
)

const (
	// requestTimeout bounds each call, including reading the body: the
	// application list with status.resources can be large.
	requestTimeout = 30 * time.Second
	maxBodyBytes   = 256 << 20
	logBodyLimit   = 1024

	inClusterServer = "https://kubernetes.default.svc"
	inClusterName   = "in-cluster"
)

// HTTPClient reads Applications, AppProjects and Clusters and syncs
// applications. TLS settings (a custom CA, or insecure) belong to the
// injected *http.Client.
type HTTPClient struct {
	baseURL    string
	token      string
	httpClient *http.Client
	logger     *zap.Logger
}

func NewHTTPClient(baseURL, token string, httpClient *http.Client, logger *zap.Logger) *HTTPClient {
	return &HTTPClient{
		baseURL:    strings.TrimRight(baseURL, "/"),
		token:      token,
		httpClient: httpClient,
		logger:     logger,
	}
}

type objectMeta struct {
	Name      string `json:"name"`
	Namespace string `json:"namespace"`
}

type applicationList struct {
	Items []struct {
		Metadata objectMeta `json:"metadata"`
		Spec     struct {
			Project     string `json:"project"`
			Destination struct {
				Server    string `json:"server"`
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"destination"`
		} `json:"spec"`
		Status struct {
			Resources []struct {
				Kind      string `json:"kind"`
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"resources"`
			Health struct {
				Status string `json:"status"`
			} `json:"health"`
			Sync struct {
				Status string `json:"status"`
			} `json:"sync"`
			OperationState *struct {
				Phase      string     `json:"phase"`
				FinishedAt *time.Time `json:"finishedAt"`
			} `json:"operationState"`
		} `json:"status"`
	} `json:"items"`
}

type projectList struct {
	Items []struct {
		Metadata objectMeta `json:"metadata"`
	} `json:"items"`
}

type clusterList struct {
	Items []struct {
		Server string `json:"server"`
		Name   string `json:"name"`
	} `json:"items"`
}

// appFields trims the response to what the portal reads. Argo CD supports only
// a fixed set of paths here (appFields in its
// pkg/apiclient/application/forwarder_overwrite.go) and silently drops any
// other, e.g. items.spec.destination or items.status.resources.namespace, so
// spec, status.health and status.resources are requested whole.
const appFields = "items.metadata.name,items.metadata.namespace,items.spec," +
	"items.status.resources,items.status.health,items.status.sync.status," +
	"items.status.operationState.phase,items.status.operationState.finishedAt"

// ListApplications returns every application with its destination resolved to
// the Argo cluster name. Any failure returns no apps at all, so a partial list
// can never be mistaken for the full one.
func (c *HTTPClient) ListApplications(ctx context.Context) ([]domain.ArgoApp, error) {
	var clusters clusterList
	if err := c.get(ctx, "/api/v1/clusters", nil, &clusters); err != nil {
		return nil, err
	}
	names := map[string]string{inClusterServer: inClusterName}
	for _, cl := range clusters.Items {
		if cl.Server != "" && cl.Name != "" {
			names[normalizeServer(cl.Server)] = cl.Name
		}
	}

	var list applicationList
	if err := c.get(ctx, "/api/v1/applications", url.Values{"fields": {appFields}}, &list); err != nil {
		return nil, err
	}

	apps := make([]domain.ArgoApp, 0, len(list.Items))
	var unresolved []string
	unmatched := map[string]int{} // destination server (or "none") -> apps
	for _, it := range list.Items {
		app := domain.ArgoApp{
			Name:         it.Metadata.Name,
			Project:      it.Spec.Project,
			Namespace:    it.Spec.Destination.Namespace,
			Health:       it.Status.Health.Status,
			Sync:         it.Status.Sync.Status,
			AppNamespace: it.Metadata.Namespace,
		}
		// A running sync has no finishedAt yet; its phase still shows.
		if op := it.Status.OperationState; op != nil {
			app.LastSyncPhase = op.Phase
			if op.FinishedAt != nil {
				app.LastSyncAt = op.FinishedAt.UTC()
			}
		}
		if app.Project == "" {
			app.Project = "default" // Argo's implicit project
		}
		switch d := it.Spec.Destination; {
		case d.Name != "":
			app.Cluster = d.Name
		case d.Server != "":
			app.Cluster = names[normalizeServer(d.Server)]
		}
		// An app whose cluster cannot be named stays in the list with an empty
		// Cluster; ArgoApp.Pairs() then covers nothing, which fails closed.
		if app.Cluster == "" {
			unresolved = append(unresolved, app.Name)
			dest := normalizeServer(it.Spec.Destination.Server)
			if dest == "" {
				dest = "none"
			}
			unmatched[dest]++
		}

		seen := map[string]struct{}{}
		for _, r := range it.Status.Resources {
			if r.Namespace == "" { // cluster-scoped
				app.ClusterResources = append(app.ClusterResources, r.Kind+"/"+r.Name)
				continue
			}
			if _, ok := seen[r.Namespace]; ok {
				continue
			}
			seen[r.Namespace] = struct{}{}
			app.ResourceNamespaces = append(app.ResourceNamespaces, r.Namespace)
		}
		sort.Strings(app.ResourceNamespaces)
		apps = append(apps, app)
	}
	if len(unresolved) > 0 {
		known := make([]string, 0, len(names))
		for server, name := range names {
			known = append(known, name+"="+server)
		}
		sort.Strings(known)
		dests := make([]string, 0, len(unmatched))
		for server, n := range unmatched {
			dests = append(dests, fmt.Sprintf("%s (%d apps)", server, n))
		}
		sort.Strings(dests)
		hint := "the destination servers differ from the clusters' server addresses"
		if len(clusters.Items) == 0 {
			hint = "Argo returned no clusters: the token probably lacks 'clusters, get' in argocd-rbac-cm"
		}
		c.logger.Warn("argo applications with a destination that matches no Argo cluster",
			zap.Int("count", len(unresolved)), zap.Strings("apps", firstN(unresolved, 10)),
			zap.Int("argo_clusters", len(clusters.Items)), zap.Strings("known_clusters", known),
			zap.Strings("unmatched_destinations", firstN(dests, 10)), zap.String("hint", hint))
	}
	return apps, nil
}

// operationState is an application's status.operationState.
type operationState struct {
	Phase      string    `json:"phase"`
	Message    string    `json:"message"`
	StartedAt  time.Time `json:"startedAt"`
	SyncResult *struct {
		Resources []struct {
			Status string `json:"status"`
		} `json:"resources"`
	} `json:"syncResult"`
}

// appState is the part of an Application a sync is followed by.
type appState struct {
	Operation *json.RawMessage `json:"operation"`
	Status    struct {
		OperationState *operationState `json:"operationState"`
	} `json:"status"`
}

func (a appState) operation() application.ArgoOperation {
	op := application.ArgoOperation{Pending: a.Operation != nil && string(*a.Operation) != "null"}
	st := a.Status.OperationState
	if st == nil {
		return op
	}
	op.Phase, op.Message, op.StartedAt = st.Phase, st.Message, st.StartedAt.UTC()
	if st.SyncResult != nil {
		for _, r := range st.SyncResult.Resources {
			op.Total++
			switch r.Status {
			case "Synced", "Pruned":
				op.Synced++
			case "SyncFailed":
				op.Failed++
			}
		}
	}
	return op
}

// details is the part of an Application the details panel shows.
type details struct {
	Spec struct {
		Source  *source  `json:"source"`
		Sources []source `json:"sources"`
	} `json:"spec"`
	Status struct {
		Health struct {
			Status  string `json:"status"`
			Message string `json:"message"`
		} `json:"health"`
		Sync struct {
			Status   string `json:"status"`
			Revision string `json:"revision"`
		} `json:"sync"`
		Conditions []struct {
			Type               string     `json:"type"`
			Message            string     `json:"message"`
			LastTransitionTime *time.Time `json:"lastTransitionTime"`
		} `json:"conditions"`
		OperationState *struct {
			Phase      string     `json:"phase"`
			Message    string     `json:"message"`
			StartedAt  time.Time  `json:"startedAt"`
			FinishedAt *time.Time `json:"finishedAt"`
			Operation  struct {
				InitiatedBy initiatedBy `json:"initiatedBy"`
			} `json:"operation"`
			SyncResult *struct {
				Revision  string `json:"revision"`
				Resources []struct {
					Group     string `json:"group"`
					Kind      string `json:"kind"`
					Namespace string `json:"namespace"`
					Name      string `json:"name"`
					Status    string `json:"status"`
					Message   string `json:"message"`
					HookType  string `json:"hookType"`
					HookPhase string `json:"hookPhase"`
					SyncPhase string `json:"syncPhase"`
				} `json:"resources"`
			} `json:"syncResult"`
		} `json:"operationState"`
		History []struct {
			ID          int64       `json:"id"`
			Revision    string      `json:"revision"`
			Revisions   []string    `json:"revisions"`
			DeployedAt  time.Time   `json:"deployedAt"`
			InitiatedBy initiatedBy `json:"initiatedBy"`
			Source      *source     `json:"source"`
			Sources     []source    `json:"sources"`
		} `json:"history"`
	} `json:"status"`
}

type initiatedBy struct {
	Username  string `json:"username"`
	Automated bool   `json:"automated"`
}

type source struct {
	RepoURL string `json:"repoURL"`
	Chart   string `json:"chart"`
}

func (s source) String() string {
	if s.Chart != "" {
		return s.RepoURL + " (" + s.Chart + ")"
	}
	return s.RepoURL
}

func sourcesString(one *source, many []source) string {
	if one != nil && one.RepoURL != "" {
		return one.String()
	}
	out := make([]string, 0, len(many))
	for _, s := range many {
		out = append(out, s.String())
	}
	return strings.Join(out, ", ")
}

// GetAppDetails reads the application's conditions, last operation and history.
func (c *HTTPClient) GetAppDetails(ctx context.Context, appNamespace, name string) (application.ArgoAppDetails, error) {
	var d details
	q := url.Values{"appNamespace": {appNamespace}}
	if err := c.do(ctx, http.MethodGet, "/api/v1/applications/"+url.PathEscape(name), q, nil, &d); err != nil {
		return application.ArgoAppDetails{}, err
	}
	st := d.Status
	out := application.ArgoAppDetails{Health: st.Health.Status, HealthMessage: st.Health.Message,
		SyncStatus: st.Sync.Status, SyncRevision: st.Sync.Revision}
	for _, cnd := range st.Conditions {
		out.Conditions = append(out.Conditions, application.ArgoCondition{Type: cnd.Type, Message: cnd.Message, At: cnd.LastTransitionTime})
	}
	if op := st.OperationState; op != nil {
		r := &application.ArgoOperationResult{Phase: op.Phase, Message: op.Message, StartedAt: op.StartedAt.UTC(), FinishedAt: op.FinishedAt,
			InitiatedBy: op.Operation.InitiatedBy.Username, Automated: op.Operation.InitiatedBy.Automated}
		if sr := op.SyncResult; sr != nil {
			r.Revision = sr.Revision
			for _, x := range sr.Resources {
				r.Resources = append(r.Resources, application.ArgoResourceResult{Group: x.Group, Kind: x.Kind, Namespace: x.Namespace, Name: x.Name,
					Status: x.Status, Message: x.Message, HookType: x.HookType, HookPhase: x.HookPhase, SyncPhase: x.SyncPhase})
			}
		}
		out.LastOperation = r
	}
	for _, h := range st.History {
		out.History = append(out.History, application.ArgoDeploy{ID: h.ID, Revision: h.Revision, Revisions: h.Revisions, DeployedAt: h.DeployedAt.UTC(),
			InitiatedBy: h.InitiatedBy.Username, Automated: h.InitiatedBy.Automated, Source: sourcesString(h.Source, h.Sources)})
	}
	return out, nil
}

// ManagedResources reads the application's resources with their states.
// Like Argo CD's diff view it prefers the normalized live state and the
// predicted live state (the live object with the desired changes applied),
// so fields Argo is told to ignore do not show as differences.
func (c *HTTPClient) ManagedResources(ctx context.Context, appNamespace, name string) ([]application.ArgoManagedResource, error) {
	var list struct {
		Items []struct {
			Group               string `json:"group"`
			Kind                string `json:"kind"`
			Namespace           string `json:"namespace"`
			Name                string `json:"name"`
			TargetState         string `json:"targetState"`
			LiveState           string `json:"liveState"`
			NormalizedLiveState string `json:"normalizedLiveState"`
			PredictedLiveState  string `json:"predictedLiveState"`
			Modified            bool   `json:"modified"`
			Hook                bool   `json:"hook"`
		} `json:"items"`
	}
	q := url.Values{"appNamespace": {appNamespace}}
	if err := c.do(ctx, http.MethodGet, "/api/v1/applications/"+url.PathEscape(name)+"/managed-resources", q, nil, &list); err != nil {
		return nil, err
	}
	out := make([]application.ArgoManagedResource, 0, len(list.Items))
	for _, it := range list.Items {
		out = append(out, application.ArgoManagedResource{Group: it.Group, Kind: it.Kind, Namespace: it.Namespace, Name: it.Name,
			Live: state(it.NormalizedLiveState, it.LiveState), Desired: state(it.PredictedLiveState, it.TargetState),
			Modified: it.Modified, Hook: it.Hook})
	}
	return out, nil
}

// state returns the first present JSON state; Argo sends "null" or "" for none.
func state(preferred, fallback string) []byte {
	for _, s := range []string{preferred, fallback} {
		if s != "" && s != "null" {
			return []byte(s)
		}
	}
	return nil
}

// RevisionMetadata reads the commit behind a Git revision. It fails for
// revisions that are not commits (e.g. Helm chart versions); that is not
// logged, since the caller just shows the revision.
func (c *HTTPClient) RevisionMetadata(ctx context.Context, appNamespace, name, revision string) (application.ArgoRevision, error) {
	var m struct {
		Author  string     `json:"author"`
		Date    *time.Time `json:"date"`
		Message string     `json:"message"`
		Tags    []string   `json:"tags"`
	}
	q := url.Values{"appNamespace": {appNamespace}}
	path := "/api/v1/applications/" + url.PathEscape(name) + "/revisions/" + url.PathEscape(revision) + "/metadata"
	if err := c.request(ctx, http.MethodGet, path, q, nil, &m, false); err != nil {
		return application.ArgoRevision{}, err
	}
	return application.ArgoRevision{Author: m.Author, Date: m.Date, Message: m.Message, Tags: m.Tags}, nil
}

// errInProgress is Argo's FailedPrecondition message for a second operation.
const errInProgress = "another operation is already in progress"

// SyncApplication starts a sync of the application to its target revision:
// never prune, dry run or a chosen revision. The user is named in the
// operation's info, since every call is made as the token's account.
func (c *HTTPClient) SyncApplication(ctx context.Context, appNamespace, name, triggeredBy string) (application.ArgoOperation, error) {
	body := map[string]any{
		"name":         name,
		"appNamespace": appNamespace,
		"prune":        false,
		"dryRun":       false,
		"infos":        []map[string]string{{"name": "Triggered by", "value": triggeredBy + " (tiphys)"}},
	}
	var app appState
	err := c.do(ctx, http.MethodPost, "/api/v1/applications/"+url.PathEscape(name)+"/sync", nil, body, &app)
	if err != nil {
		return application.ArgoOperation{}, err
	}
	return app.operation(), nil
}

// GetOperation reads the application's operation state.
func (c *HTTPClient) GetOperation(ctx context.Context, appNamespace, name string) (application.ArgoOperation, error) {
	var app appState
	q := url.Values{"appNamespace": {appNamespace}}
	if err := c.do(ctx, http.MethodGet, "/api/v1/applications/"+url.PathEscape(name), q, nil, &app); err != nil {
		return application.ArgoOperation{}, err
	}
	return app.operation(), nil
}

// ListProjects returns the names of all AppProjects.
func (c *HTTPClient) ListProjects(ctx context.Context) ([]string, error) {
	var list projectList
	if err := c.get(ctx, "/api/v1/projects", nil, &list); err != nil {
		return nil, err
	}
	names := make([]string, 0, len(list.Items))
	for _, it := range list.Items {
		names = append(names, it.Metadata.Name)
	}
	return names, nil
}

func (c *HTTPClient) get(ctx context.Context, path string, query url.Values, out any) error {
	return c.do(ctx, http.MethodGet, path, query, nil, out)
}

// do sends a request with an optional JSON body and decodes the response.
// A sync refused because another operation runs is ErrSyncInProgress and
// is not logged: it is an outcome, not a failure.
func (c *HTTPClient) do(ctx context.Context, method, path string, query url.Values, body, out any) error {
	return c.request(ctx, method, path, query, body, out, true)
}

// request is do with failure logging switchable (logFailures false for
// calls whose failure is an expected outcome).
func (c *HTTPClient) request(ctx context.Context, method, path string, query url.Values, body, out any, logFailures bool) error {
	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()

	requestURL := c.baseURL + path
	if len(query) > 0 {
		requestURL += "?" + query.Encode()
	}
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("failed to encode argo request: %w", err)
		}
		reqBody = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, requestURL, reqBody)
	if err != nil {
		c.logger.Error("failed to build argo request", zap.String("url", requestURL), zap.Error(err))
		return fmt.Errorf("failed to build argo request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if logFailures {
			c.logger.Error("argo upstream call failed", zap.String("url", requestURL), zap.Error(err))
		}
		return fmt.Errorf("argo upstream call failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, logBodyLimit))
		if method == http.MethodPost && strings.Contains(string(body), errInProgress) {
			return application.ErrSyncInProgress
		}
		if logFailures {
			c.logger.Error("argo upstream returned non-200", zap.String("method", method), zap.String("url", requestURL), zap.Int("status", resp.StatusCode), zap.String("body", string(body)))
		}
		return fmt.Errorf("argo upstream returned status %d for %s: %s", resp.StatusCode, path, firstLine(string(body)))
	}
	// Streaming decode: the application list can be tens of megabytes.
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBodyBytes)).Decode(out); err != nil {
		c.logger.Error("failed to decode argo response", zap.String("url", requestURL), zap.Error(err))
		return fmt.Errorf("failed to decode argo response for %s: %w", path, err)
	}
	return nil
}

func normalizeServer(s string) string { return strings.TrimRight(s, "/") }

// firstLine is Argo's error message from a JSON error body, else the body's
// first line, for the error shown to the user.
func firstLine(body string) string {
	var e struct {
		Message string `json:"message"`
	}
	if json.Unmarshal([]byte(body), &e) == nil && e.Message != "" {
		return e.Message
	}
	line, _, _ := strings.Cut(body, "\n")
	return line
}

func firstN(s []string, n int) []string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
