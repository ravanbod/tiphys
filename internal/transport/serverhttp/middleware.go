package serverhttp

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"tiphys/internal/application"
	"tiphys/internal/domain"
	"tiphys/internal/monitoring"
	"tiphys/internal/requestid"
)

const (
	sessionCookie = "tiphys_session"
	loginCookie   = "tiphys_login"
	principalKey  = "principal"
)

// withRequestID puts the request's ID into its context, reusing the incoming
// X-Request-ID when present, and echoes it back.
func withRequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader(requestid.Header)
		if id == "" {
			b := make([]byte, 16)
			_, _ = rand.Read(b)
			id = hex.EncodeToString(b)
		}
		c.Header(requestid.Header, id)
		c.Request = c.Request.WithContext(requestid.NewContext(c.Request.Context(), id))
		c.Next()
	}
}

// accessLog logs each request once and counts it.
func accessLog(logger *zap.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		route := c.FullPath()
		if route == "" {
			route = "unmatched"
		}
		status := c.Writer.Status()
		monitoring.HTTPRequests.WithLabelValues(c.Request.Method, route, statusClass(status)).Inc()
		if route == "/healthz" || route == "/readyz" {
			return
		}
		logger.Info("request", requestid.Field(c.Request.Context()),
			zap.String("method", c.Request.Method), zap.String("route", route), zap.Int("status", status),
			zap.Duration("duration", time.Since(start)), zap.String("remote_ip", c.ClientIP()))
	}
}

func statusClass(s int) string {
	switch {
	case s >= 500:
		return "5xx"
	case s >= 400:
		return "4xx"
	case s >= 300:
		return "3xx"
	}
	return "2xx"
}

// securityHeaders sets a strict Content-Security-Policy and friends.
func securityHeaders(publicURL string, secure bool) gin.HandlerFunc {
	ws := "'self'"
	if u, err := url.Parse(publicURL); err == nil {
		scheme := "ws"
		if u.Scheme == "https" {
			scheme = "wss"
		}
		ws = "'self' " + scheme + "://" + u.Host
	}
	csp := "default-src 'self'; script-src 'self' 'wasm-unsafe-eval'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'; " +
		"connect-src " + ws + "; frame-ancestors 'none'; base-uri 'none'; form-action 'self'; object-src 'none'"
	return func(c *gin.Context) {
		h := c.Writer.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("Cache-Control", "no-store")
		if secure {
			h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		}
		c.Next()
	}
}

func (h *Handler) setSessionCookie(c *gin.Context, token string, maxAge int) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/", MaxAge: maxAge,
		HttpOnly: true, Secure: h.opts.CookieSecure, SameSite: http.SameSiteLaxMode,
	})
}

// authRequired resolves the session cookie to a Principal.
func (h *Handler) authRequired() gin.HandlerFunc {
	return func(c *gin.Context) {
		token, _ := c.Cookie(sessionCookie)
		p, err := h.svc.Auth.Authenticate(c.Request.Context(), token, c.ClientIP(), c.Request.UserAgent())
		if err != nil {
			if token != "" {
				h.setSessionCookie(c, "", -1)
			}
			h.writeError(c, err)
			return
		}
		c.Set(principalKey, p)
		c.Next()
	}
}

func principal(c *gin.Context) application.Principal {
	return c.MustGet(principalKey).(application.Principal)
}

// csrfRequired checks the CSRF header on state-changing requests.
func csrfRequired() gin.HandlerFunc {
	return func(c *gin.Context) {
		switch c.Request.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			c.Next()
			return
		}
		p := principal(c)
		got := c.GetHeader("X-CSRF-Token")
		if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(p.CSRFToken)) != 1 {
			abort(c, http.StatusForbidden, "csrf", "Missing or invalid CSRF token.", nil)
			return
		}
		c.Next()
	}
}

// rateLimited limits state-changing requests per user (streams excluded).
func (h *Handler) rateLimited() gin.HandlerFunc {
	return func(c *gin.Context) {
		switch c.Request.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			c.Next()
			return
		}
		if !h.limits.Allow(principal(c).User.ID, time.Now()) {
			c.Header("Retry-After", "10")
			abort(c, http.StatusTooManyRequests, "rate_limited", "Too many actions, slow down.", nil)
			return
		}
		c.Next()
	}
}

// requireRoles allows only the given roles.
func (h *Handler) requireRoles(roles ...domain.Role) gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := application.RequireRole(principal(c), roles...); err != nil {
			h.writeError(c, err)
			return
		}
		c.Next()
	}
}

// readOnlyForAuditors lets auditors through on GET only; admins on anything.
func (h *Handler) adminOrReadOnlyAuditor() gin.HandlerFunc {
	return func(c *gin.Context) {
		p := principal(c)
		switch {
		case p.User.Role == domain.RoleAdmin:
		case p.User.Role == domain.RoleAuditor && c.Request.Method == http.MethodGet:
		default:
			h.writeError(c, application.ErrForbiddenRole)
			return
		}
		c.Next()
	}
}

// rateLimiter is a per-key (user ID, or login IP or username) sliding window
// of one minute.
type rateLimiter[K comparable] struct {
	limit     int
	mu        sync.Mutex
	hits      map[K][]time.Time
	lastSweep time.Time
}

func newRateLimiter[K comparable](limit int) *rateLimiter[K] {
	if limit <= 0 {
		limit = 30
	}
	return &rateLimiter[K]{limit: limit, hits: map[K][]time.Time{}}
}

func (r *rateLimiter[K]) Allow(key K, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	cutoff := now.Add(-time.Minute)
	if now.Sub(r.lastSweep) > time.Minute {
		// Drop idle keys, so made-up usernames do not pile up.
		for k, ts := range r.hits {
			if !ts[len(ts)-1].After(cutoff) {
				delete(r.hits, k)
			}
		}
		r.lastSweep = now
	}
	kept := r.hits[key][:0]
	for _, t := range r.hits[key] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= r.limit {
		r.hits[key] = kept
		return false
	}
	r.hits[key] = append(kept, now)
	return true
}
