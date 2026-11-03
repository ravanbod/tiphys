package serverhttp

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"tiphys/internal/application"
	"tiphys/internal/domain"
)

// livePing keeps proxies from closing an idle live stream.
const livePing = 25 * time.Second

type liveNamespaceDTO struct {
	Cluster   string `json:"cluster"`
	Namespace string `json:"namespace"`
	Error     string `json:"error,omitempty"` // denied | invalid | not_found | cluster_error
}

type liveStatusDTO struct {
	Desired   int32  `json:"desired"`
	Updated   int32  `json:"updated"`
	Ready     int32  `json:"ready"`
	Available int32  `json:"available"`
	Complete  bool   `json:"complete"`
	Failed    bool   `json:"failed"`
	Message   string `json:"message"`
}

type liveChangeDTO struct {
	Cluster   string         `json:"cluster"`
	Namespace string         `json:"namespace"`
	Kind      string         `json:"kind"`
	Name      string         `json:"name"`
	Type      string         `json:"type"`
	Pod       string         `json:"pod,omitempty"`
	Phase     string         `json:"phase,omitempty"`
	Container string         `json:"container,omitempty"`
	Reason    string         `json:"reason,omitempty"`
	ExitCode  *int32         `json:"exitCode,omitempty"`
	Status    *liveStatusDTO `json:"status,omitempty"`
	Event     *eventDTO      `json:"event,omitempty"`
}

func toLiveChange(c application.LiveChange) liveChangeDTO {
	d := liveChangeDTO{
		Cluster: c.Cluster, Namespace: c.Namespace, Kind: string(c.Workload.Kind), Name: c.Workload.Name, Type: string(c.Type),
		Pod: c.Pod, Phase: c.Phase, Container: c.Container, Reason: c.Reason, ExitCode: c.ExitCode,
	}
	if s := c.Status; s != nil {
		d.Status = &liveStatusDTO{Desired: s.Desired, Updated: s.Updated, Ready: s.Ready, Available: s.Available,
			Complete: s.Complete, Failed: s.Failed, Message: s.Message}
	}
	if c.Event != nil {
		e := toEvent(*c.Event)
		d.Event = &e
	}
	return d
}

// liveErrorCode names why a namespace is not followed.
func liveErrorCode(err error) string {
	var denied *application.DeniedError
	var invalid *application.InvalidError
	switch {
	case errors.As(err, &denied), errors.Is(err, application.ErrProtectedNamespace):
		return "denied"
	case errors.As(err, &invalid), errors.Is(err, application.ErrInvalidName):
		return "invalid"
	case errors.Is(err, application.ErrNotFound):
		return "not_found"
	}
	return "cluster_error"
}

// Live streams changes in namespaces as Server-Sent Events:
// ?ns=<cluster>/<namespace> (repeated). The server ends the stream after
// LiveMaxDuration and the browser reconnects.
func (h *Handler) Live(c *gin.Context) {
	var targets []domain.ClusterNS
	for _, v := range c.QueryArray("ns") {
		cluster, ns, ok := strings.Cut(v, "/")
		if !ok {
			h.writeError(c, application.Invalid("ns", "expected <cluster>/<namespace>"))
			return
		}
		targets = append(targets, domain.ClusterNS{Cluster: cluster, Namespace: ns})
	}
	live, err := h.svc.Workloads.Live(c.Request.Context(), principal(c), targets)
	if errors.Is(err, application.ErrTooManyStreams) {
		abort(c, http.StatusTooManyRequests, "rate_limited", "Too many live pages open, close some tabs.", nil)
		return
	}
	if err != nil {
		h.writeError(c, err)
		return
	}
	defer live.Stream.Close(application.EndClientClosed)

	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("X-Accel-Buffering", "no")
	c.Status(http.StatusOK)
	flusher, _ := c.Writer.(http.Flusher)
	flush := func() {
		if flusher != nil {
			flusher.Flush()
		}
	}
	nss := make([]liveNamespaceDTO, len(live.Namespaces))
	for i, n := range live.Namespaces {
		nss[i] = liveNamespaceDTO{Cluster: n.Cluster, Namespace: n.Namespace}
		if n.Err != nil {
			nss[i].Error = liveErrorCode(n.Err)
		}
	}
	_, _ = fmt.Fprint(c.Writer, "retry: 3000\n\n")
	c.SSEvent("ready", gin.H{"namespaces": nss})
	flush()

	ping := time.NewTicker(livePing)
	defer ping.Stop()
	for {
		select {
		case <-c.Request.Context().Done():
			return
		case ch, ok := <-live.Changes:
			if !ok {
				c.SSEvent("end", gin.H{"reason": live.Stream.Reason()})
				flush()
				return
			}
			c.SSEvent("change", toLiveChange(ch))
			flush()
		case <-ping.C:
			if _, err := fmt.Fprint(c.Writer, ": ping\n\n"); err != nil {
				return
			}
			flush()
		}
	}
}
