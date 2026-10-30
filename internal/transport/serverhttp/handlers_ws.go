package serverhttp

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"go.uber.org/zap"

	"tiphys/internal/application"
	"tiphys/internal/domain"
	"tiphys/internal/requestid"
)

const (
	wsPingInterval = 30 * time.Second
	wsReadTimeout  = 90 * time.Second
	wsWriteTimeout = 15 * time.Second
	wsMaxMessage   = 1 << 20
)

// wsGuard checks the Origin header and the CSRF query parameter. Browsers
// cannot set custom headers on WebSockets, so the token travels as ?csrf=.
func (h *Handler) wsGuard() gin.HandlerFunc {
	allowed := originOf(h.opts.PublicURL)
	return func(c *gin.Context) {
		if origin := c.GetHeader("Origin"); origin == "" || originOf(origin) != allowed {
			abort(c, http.StatusForbidden, "bad_origin", "Origin not allowed.", nil)
			return
		}
		p := principal(c)
		if got := c.Query("csrf"); got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(p.CSRFToken)) != 1 {
			abort(c, http.StatusForbidden, "csrf", "Missing or invalid CSRF token.", nil)
			return
		}
		c.Next()
	}
}

func originOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// wsConn serializes writes to a WebSocket.
type wsConn struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

func (w *wsConn) write(typ int, data []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.conn.SetWriteDeadline(time.Now().Add(wsWriteTimeout))
	return w.conn.WriteMessage(typ, data)
}

func (w *wsConn) control(v any) error {
	b, _ := json.Marshal(v)
	return w.write(websocket.TextMessage, b)
}

func (w *wsConn) close(code int, text string) {
	w.mu.Lock()
	_ = w.conn.SetWriteDeadline(time.Now().Add(time.Second))
	_ = w.conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(code, text))
	w.mu.Unlock()
	_ = w.conn.Close()
}

// upgrade turns the request into a WebSocket with ping/pong keepalive.
func (h *Handler) upgrade(c *gin.Context) (*wsConn, bool) {
	up := websocket.Upgrader{
		ReadBufferSize: 16 << 10, WriteBufferSize: 32 << 10,
		CheckOrigin: func(r *http.Request) bool { return originOf(r.Header.Get("Origin")) == originOf(h.opts.PublicURL) },
	}
	conn, err := up.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		h.logger.Warn("websocket upgrade failed", requestid.Field(c.Request.Context()), zap.Error(err))
		return nil, false
	}
	conn.SetReadLimit(wsMaxMessage)
	_ = conn.SetReadDeadline(time.Now().Add(wsReadTimeout))
	conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(wsReadTimeout)) })
	return &wsConn{conn: conn}, true
}

// keepAlive pings until done is closed.
func (w *wsConn) keepAlive(done <-chan struct{}) {
	t := time.NewTicker(wsPingInterval)
	defer t.Stop()
	for {
		select {
		case <-done:
			return
		case <-t.C:
			if err := w.write(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

type endMessage struct {
	Type    string `json:"type"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

var endText = map[application.EndReason]string{
	application.EndClientClosed:      "The connection was closed.",
	application.EndProcessExited:     "The process exited.",
	application.EndIdleTimeout:       "The session was idle for too long.",
	application.EndMaxDuration:       "The maximum session length was reached.",
	application.EndOutsideWindow:     "The access window closed.",
	application.EndHoliday:           "Access is closed because of a holiday.",
	application.EndGrantRevoked:      "Your access was revoked.",
	application.EndGrantExpired:      "Your grant expired.",
	application.EndUserDisabled:      "Your account was disabled or your login ended.",
	application.EndRoleChanged:       "Your role changed.",
	application.EndTerminatedByAdmin: "An administrator terminated the session.",
	application.EndBreakGlassEnded:   "Break-glass ended.",
	application.EndRecordingFailed:   "Session recording failed, so the session was ended.",
	application.EndClusterError:      "The cluster connection failed.",
	application.EndPortalRestart:     "The portal is restarting.",
	application.EndDenied:            "Access was denied.",
}

func endMsg(r application.EndReason) endMessage {
	return endMessage{Type: "end", Reason: string(r), Message: endText[r]}
}

func controlMessage(sc application.StreamControl) any {
	switch sc.Type {
	case "warning":
		return map[string]any{"type": "warning", "code": "closing_soon", "closesAt": timeStr(sc.ClosesAt), "reason": sc.Reason, "message": sc.Message}
	default:
		return map[string]any{"type": "closes_at", "closesAt": timeStr(sc.ClosesAt)}
	}
}

// refuseWS completes the upgrade and tells the client why the stream was not
// opened. Browsers cannot read the status or body of a rejected WebSocket
// handshake, so refusals travel as an `end` control message (reason `denied`,
// `recording_failed` or `cluster_error`) followed by a policy-violation close.
// Nothing was opened on the cluster and the refusal was audited by the service.
func (h *Handler) refuseWS(c *gin.Context, err error) {
	ws, ok := h.upgrade(c)
	if !ok {
		return
	}
	msg := map[string]any{"type": "end", "reason": string(application.EndDenied), "message": "Access was denied."}
	var denied *application.DeniedError
	var invalid *application.InvalidError
	var debugErr *application.DebugError
	switch {
	case errors.As(err, &denied):
		msg["code"], msg["message"] = "denied", denialText[denied.Decision.Reason]
		msg["decision"] = toDecisionInfo(denied.Decision)
	case errors.Is(err, application.ErrRecordingUnavailable):
		msg["reason"], msg["code"] = string(application.EndRecordingFailed), "recording_unavailable"
		msg["message"] = "Session recording storage is unavailable, so exec is refused."
	case errors.As(err, &invalid):
		msg["code"], msg["message"] = "invalid", invalid.Message
	case errors.Is(err, application.ErrProtectedNamespace):
		msg["code"], msg["message"] = "denied", denialText[domain.ReasonProtectedNamespace]
		msg["decision"] = decisionInfo{Reason: string(domain.ReasonProtectedNamespace), GrantIDs: []int64{}}
	case errors.Is(err, application.ErrPodNotInWorkload):
		msg["code"], msg["message"] = "pod_not_in_workload", "The pod does not belong to this workload."
	case errors.Is(err, application.ErrDebugUnavailable):
		msg["code"], msg["message"] = "debug_unavailable", "Debug containers are not enabled on this cluster."
	case errors.As(err, &debugErr):
		msg["reason"], msg["code"] = string(application.EndClusterError), "debug_failed"
		msg["message"] = "The debug container could not be started: " + debugErr.Reason
	case errors.Is(err, application.ErrForbiddenRole):
		msg["code"], msg["message"] = "forbidden_role", "Your role does not allow this."
	case errors.Is(err, application.ErrNotFound):
		msg["reason"], msg["code"], msg["message"] = string(application.EndClusterError), "not_found", "Not found."
	default:
		h.logger.Warn("stream could not be opened", requestid.Field(c.Request.Context()), zap.String("path", c.FullPath()), zap.Error(err))
		msg["reason"], msg["code"], msg["message"] = string(application.EndClusterError), "cluster_error", "The cluster could not be reached or refused the request."
	}
	_ = ws.control(msg)
	ws.close(websocket.ClosePolicyViolation, "refused")
}

// LogsStream streams pod logs over a WebSocket: binary frames carry the log
// bytes unchanged, text frames carry JSON control messages.
func (h *Handler) LogsStream(c *gin.Context) {
	req, ok := h.logRequest(c, c.Query("follow") != "false")
	if !ok {
		return
	}
	ls, err := h.svc.Workloads.OpenLogs(c.Request.Context(), principal(c), req)
	if err != nil {
		h.refuseWS(c, err)
		return
	}
	ws, ok := h.upgrade(c)
	if !ok {
		ls.Body.Close()
		ls.Stream.Close(application.EndClientClosed)
		return
	}
	st := ls.Stream
	done := make(chan struct{})
	defer close(done)
	go ws.keepAlive(done)

	_ = ws.control(map[string]any{"type": "ready", "startedAt": tstr(st.Started), "closesAt": timeStr(st.ClosesAt()),
		"environment": ls.Decision.Environment, "breakGlassId": ls.Decision.BreakGlassID})

	// Reader: detects client close.
	go func() {
		for {
			if _, _, err := ws.conn.ReadMessage(); err != nil {
				st.Close(application.EndClientClosed)
				return
			}
		}
	}()
	// Copier: log bytes to binary frames.
	copyDone := make(chan struct{})
	go func() {
		defer close(copyDone)
		buf := make([]byte, 32<<10)
		for {
			n, err := ls.Body.Read(buf)
			if n > 0 {
				if werr := ws.write(websocket.BinaryMessage, buf[:n]); werr != nil {
					st.Close(application.EndClientClosed)
					return
				}
			}
			if err != nil {
				if err != io.EOF && st.Reason() == "" {
					h.logger.Debug("log stream read ended", zap.Error(err))
				}
				return
			}
		}
	}()

	for {
		select {
		case sc := <-st.Controls:
			_ = ws.control(controlMessage(sc))
		case <-copyDone:
			st.Close(application.EndProcessExited)
		case <-st.Context().Done():
			_ = ls.Body.Close()
			<-copyDone
			_ = ws.control(endMsg(st.Reason()))
			ws.close(websocket.CloseNormalClosure, string(st.Reason()))
			return
		}
	}
}

// ExecStream is the exec WebSocket: binary frames are terminal bytes (stdin
// from the client, output from the pod), text frames are JSON control.
func (h *Handler) ExecStream(c *gin.Context) { h.shellStream(c, false) }

// DebugStream is ExecStream in a new debug container that shares the chosen
// container's processes and the pod's network.
func (h *Handler) DebugStream(c *gin.Context) { h.shellStream(c, true) }

func (h *Handler) shellStream(c *gin.Context, debug bool) {
	ref, ok := h.workloadRef(c)
	if !ok {
		return
	}
	cols, _ := strconv.Atoi(c.Query("cols"))
	rows, _ := strconv.Atoi(c.Query("rows"))
	handle, err := h.svc.Exec.Open(c.Request.Context(), principal(c), application.ExecRequest{
		Cluster: c.Param("cluster"), Namespace: c.Param("ns"), Workload: ref, Pod: c.Param("pod"),
		Container: c.Query("container"), Shell: c.Query("shell"), Cols: uint16(cols), Rows: uint16(rows), Debug: debug,
	})
	if err != nil {
		h.refuseWS(c, err)
		return
	}
	ws, ok := h.upgrade(c)
	if !ok {
		handle.Close()
		<-handle.Done()
		return
	}
	done := make(chan struct{})
	defer close(done)
	go ws.keepAlive(done)

	st := handle.Stream
	_ = ws.control(map[string]any{
		"type": "ready", "sessionId": handle.Session.ID, "startedAt": tstr(handle.Session.StartedAt), "closesAt": timeStr(st.ClosesAt()),
		"idleTimeoutSeconds": handle.Settings.ExecIdleTimeoutMinutes * 60, "maxDurationSeconds": handle.Settings.ExecMaxSessionMinutes * 60,
		"environment": handle.Decision.Environment, "breakGlassId": handle.Decision.BreakGlassID,
	})

	go func() {
		for {
			typ, data, err := ws.conn.ReadMessage()
			if err != nil {
				handle.Close()
				return
			}
			_ = ws.conn.SetReadDeadline(time.Now().Add(wsReadTimeout))
			switch typ {
			case websocket.BinaryMessage:
				if err := handle.Input(data); err != nil {
					return
				}
			case websocket.TextMessage:
				var m struct {
					Type string `json:"type"`
					Cols uint16 `json:"cols"`
					Rows uint16 `json:"rows"`
				}
				if json.Unmarshal(data, &m) == nil && m.Type == "resize" {
					handle.Resize(m.Cols, m.Rows)
				}
			}
		}
	}()

	out := handle.Output()
	for out != nil {
		select {
		case chunk, ok := <-out:
			if !ok {
				out = nil
				break
			}
			if err := ws.write(websocket.BinaryMessage, chunk); err != nil {
				handle.Close()
				out = nil
			}
		case sc := <-st.Controls:
			_ = ws.control(controlMessage(sc))
		}
	}
	<-handle.Done()
	_ = ws.control(endMsg(handle.EndReason()))
	ws.close(websocket.CloseNormalClosure, string(handle.EndReason()))
}

// WatchSession lets an admin or auditor follow an open session read-only.
func (h *Handler) WatchSession(c *gin.Context) {
	w, err := h.svc.Exec.Watch(c.Request.Context(), principal(c), c.Param("id"))
	if err != nil {
		h.refuseWS(c, err)
		return
	}
	ws, ok := h.upgrade(c)
	if !ok {
		w.Close()
		return
	}
	done := make(chan struct{})
	defer close(done)
	go ws.keepAlive(done)
	_ = ws.control(map[string]any{"type": "ready", "sessionId": w.Session.ID, "startedAt": tstr(w.Session.StartedAt),
		"closesAt": nil, "environment": w.Session.Environment, "breakGlassId": w.Session.BreakGlassID,
		"cols": w.Cols, "rows": w.Rows})
	go func() {
		for {
			if _, _, err := ws.conn.ReadMessage(); err != nil {
				w.Close()
				return
			}
		}
	}()
	st := w.Stream
	for {
		select {
		case chunk, ok := <-w.Output:
			if !ok {
				w.Output = nil
				<-w.Done()
				_ = ws.control(endMsg(application.EndProcessExited))
				ws.close(websocket.CloseNormalClosure, "session ended")
				w.Close()
				return
			}
			if err := ws.write(websocket.BinaryMessage, chunk); err != nil {
				w.Close()
				return
			}
		case sc := <-st.Controls:
			_ = ws.control(controlMessage(sc))
		case <-st.Context().Done():
			r := st.Reason()
			_ = ws.control(endMsg(r))
			ws.close(websocket.CloseNormalClosure, string(r))
			return
		}
	}
}
