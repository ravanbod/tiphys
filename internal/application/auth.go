package application

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"

	"tiphys/internal/domain"
)

const (
	// SessionRefreshInterval is how often a login session is re-checked with
	// Keycloak; a failed refresh ends the session.
	SessionRefreshInterval = 5 * time.Minute
	// loginTransactionTTL is how long a started login may take.
	loginTransactionTTL = 10 * time.Minute
	// defaultSessionMaxAge bounds a session when Keycloak gives no expiry.
	defaultSessionMaxAge = 12 * time.Hour
	// localSessionMaxAge bounds a local user's session: no Keycloak session
	// ends it, so it is shorter.
	localSessionMaxAge = 8 * time.Hour
	// Local passwords: bcrypt's input is at most 72 bytes.
	minPasswordLength = 12
	maxPasswordBytes  = 72
	passwordCost      = 12
)

// AuthConfig configures AuthService.
type AuthConfig struct {
	// BootstrapAdmins are Keycloak usernames made admin at every login.
	BootstrapAdmins []string
	// AdminGroups are Keycloak groups whose members are made admin at every
	// login. It only promotes: leaving the group does not demote, so a role
	// can still be managed in the portal (and the last-admin rule holds).
	// PostLogoutRedirect is where Keycloak sends the browser after logout.
	PostLogoutRedirect string
	AdminGroups        []string
	// LocalUsers (LOCAL_USERS_ENABLED) allows users with a portal password
	// instead of Keycloak. Off, they can neither log in nor be created, and
	// their open sessions end at the next refresh.
	LocalUsers bool
}

type loginTransaction struct {
	State    string    `json:"s"`
	Nonce    string    `json:"n"`
	Verifier string    `json:"v"`
	Next     string    `json:"x"`
	Expires  time.Time `json:"e"`
}

// AuthService runs the OIDC login and keeps server-side sessions. Keycloak
// tokens never reach the browser: the cookie holds a random session token
// whose hash is the database key.
type AuthService struct {
	oidc      OIDCProvider
	sealer    Sealer
	store     Store
	policy    *PolicyService
	audit     *AuditService
	clock     Clock
	cfg       AuthConfig
	bootstrap map[string]struct{}
	logger    *zap.Logger

	onSessionEnded func(sessionID string)

	refreshMu  sync.Mutex
	refreshing map[string]*sync.Mutex

	// dummyHash is compared against when there is no local user, so an
	// unknown username takes as long as a wrong password.
	dummyOnce sync.Once
	dummyHash []byte
}

func NewAuthService(oidc OIDCProvider, sealer Sealer, store Store, policy *PolicyService, audit *AuditService, clock Clock, cfg AuthConfig, logger *zap.Logger) *AuthService {
	b := map[string]struct{}{}
	for _, u := range cfg.BootstrapAdmins {
		if u = strings.ToLower(strings.TrimSpace(u)); u != "" {
			b[u] = struct{}{}
		}
	}
	return &AuthService{
		oidc: oidc, sealer: sealer, store: store, policy: policy, audit: audit, clock: clock,
		cfg: cfg, bootstrap: b, logger: logger, refreshing: map[string]*sync.Mutex{},
	}
}

// SetOnSessionEnded registers a callback run when a session ends for any
// reason other than a plain request (refresh failure, logout, disable); the
// stream manager uses it to close that session's streams.
func (s *AuthService) SetOnSessionEnded(f func(sessionID string)) { s.onSessionEnded = f }

// SessionID returns the database key for a cookie token.
func SessionID(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func randomToken(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

func pkceChallenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// BeginLogin returns the Keycloak URL to redirect to and the sealed value of
// the short-lived login-transaction cookie. next must be a same-origin path.
func (s *AuthService) BeginLogin(next string) (authURL, sealed string, err error) {
	tx := loginTransaction{
		State:    randomToken(16),
		Nonce:    randomToken(16),
		Verifier: randomToken(32),
		Next:     SafeNext(next),
		Expires:  s.clock.Now().Add(loginTransactionTTL),
	}
	raw, _ := json.Marshal(tx)
	sealed, err = s.sealer.Seal(raw)
	if err != nil {
		return "", "", fmt.Errorf("seal login transaction: %w", err)
	}
	return s.oidc.AuthCodeURL(tx.State, tx.Nonce, pkceChallenge(tx.Verifier)), sealed, nil
}

// SafeNext reduces next to a same-origin absolute path.
func SafeNext(next string) string {
	if next == "" || !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") || strings.Contains(next, "\\") {
		return "/"
	}
	return next
}

// CompleteLogin finishes the code flow, creates or binds the user, and
// creates the session. It returns the cookie token and the path to go to.
func (s *AuthService) CompleteLogin(ctx context.Context, code, state, sealedTx, ip, userAgent string) (token, next string, err error) {
	raw, err := s.sealer.Open(sealedTx)
	if err != nil {
		return "", "", fmt.Errorf("%w: invalid login transaction", ErrUnauthenticated)
	}
	var tx loginTransaction
	if err := json.Unmarshal(raw, &tx); err != nil || tx.State == "" || tx.State != state || s.clock.Now().After(tx.Expires) {
		return "", "", fmt.Errorf("%w: login state mismatch or expired", ErrUnauthenticated)
	}

	tokens, claims, err := s.oidc.Exchange(ctx, code, tx.Verifier, tx.Nonce)
	if err != nil {
		s.logger.Warn("oidc code exchange failed", zap.Error(err))
		return "", "", fmt.Errorf("%w: code exchange failed", ErrUnauthenticated)
	}
	if claims.Subject == "" || claims.PreferredUsername == "" {
		return "", "", fmt.Errorf("%w: identity token lacks sub or preferred_username", ErrUnauthenticated)
	}

	user, created, err := s.resolveUser(ctx, claims)
	if err != nil {
		return "", "", err
	}
	if user.Disabled {
		e := s.audit.Event(Principal{User: user, SourceIP: ip, UserAgent: userAgent}, domain.EventLogin)
		e.Result, e.Reason = domain.ResultDenied, string(domain.ReasonUserDisabled)
		s.audit.RecordBestEffort(ctx, e)
		return "", "", ErrUserDisabled
	}

	now := s.clock.Now()
	token = randomToken(32)
	expires := tokens.SessionExpiry
	if expires.IsZero() {
		expires = now.Add(defaultSessionMaxAge)
	}
	ws, err := s.sealTokens(WebSession{
		ID: SessionID(token), UserID: user.ID, CSRFToken: randomToken(24),
		AccessToken: tokens.AccessToken, RefreshToken: tokens.RefreshToken, IDToken: tokens.IDToken,
		CreatedAt: now, LastRefreshAt: now, ExpiresAt: expires, SourceIP: ip, UserAgent: userAgent,
	})
	if err != nil {
		return "", "", err
	}
	if err := s.store.Repos().WebSessions.Create(ctx, ws); err != nil {
		return "", "", fmt.Errorf("create session: %w", err)
	}
	user, promoted, err := s.promoteAdmin(ctx, user, claims, ip, userAgent)
	if err != nil {
		return "", "", err
	}
	if created || promoted {
		s.policy.Changed(ctx)
	}

	p := Principal{User: user, SessionID: ws.ID, CSRFToken: ws.CSRFToken, SourceIP: ip, UserAgent: userAgent}
	e := s.audit.Event(p, domain.EventLogin)
	e.Result = domain.ResultSucceeded
	e.SessionID = ""
	s.audit.RecordBestEffort(ctx, e)
	return token, tx.Next, nil
}

// resolveUser finds the user for the claims: by Keycloak sub, else by a
// pre-created username (binding the sub), else a new user. Admin promotion
// happens afterwards in promoteAdmin; no one is ever demoted here.
func (s *AuthService) resolveUser(ctx context.Context, c OIDCClaims) (domain.User, bool, error) {
	repos := s.store.Repos()
	now := s.clock.Now()
	display := c.Name
	if display == "" {
		display = c.PreferredUsername
	}

	u, err := repos.Users.GetByKeycloakSub(ctx, c.Subject)
	switch {
	case err == nil:
		u.LastLoginAt = &now
		u.DisplayName = display
		if !strings.EqualFold(u.Username, c.PreferredUsername) {
			// The user was renamed in Keycloak; keep the portal's username
			// in step so audit shows the current one.
			u.Username = c.PreferredUsername
		}
		u.UpdatedBy = "login"
		if err := repos.Users.Update(ctx, u); err != nil {
			return u, false, fmt.Errorf("update user at login: %w", err)
		}
		return u, false, nil
	case !errors.Is(err, ErrNotFound):
		return u, false, err
	}

	u, err = repos.Users.GetByUsername(ctx, c.PreferredUsername)
	switch {
	case err == nil:
		if u.Local {
			return u, false, fmt.Errorf("%w: username %q is a local user", ErrUnauthenticated, c.PreferredUsername)
		}
		if u.KeycloakSub != "" && u.KeycloakSub != c.Subject {
			return u, false, fmt.Errorf("%w: username %q is bound to a different Keycloak identity", ErrUnauthenticated, c.PreferredUsername)
		}
		u.KeycloakSub = c.Subject
		u.LastLoginAt = &now
		u.DisplayName = display
		u.UpdatedBy = "login"
		if err := repos.Users.Update(ctx, u); err != nil {
			return u, false, fmt.Errorf("bind user at login: %w", err)
		}
		return u, true, nil
	case !errors.Is(err, ErrNotFound):
		return u, false, err
	}

	u, err = repos.Users.Create(ctx, domain.User{
		KeycloakSub: c.Subject, Username: c.PreferredUsername, DisplayName: display,
		Role: domain.RoleUser, LastLoginAt: &now, UpdatedBy: "login",
	})
	if err != nil {
		return u, false, fmt.Errorf("create user at login: %w", err)
	}
	return u, true, nil
}

// inAdminGroup returns the first of the user's groups that is an admin group.
// Groups compare case-insensitively with a leading "/" ignored, so "/admins"
// (full path) and "admins" are the same group.
func (s *AuthService) inAdminGroup(groups []string) (string, bool) {
	norm := func(g string) string { return strings.ToLower(strings.TrimPrefix(strings.TrimSpace(g), "/")) }
	for _, g := range groups {
		for _, want := range s.cfg.AdminGroups {
			if n := norm(want); n != "" && n == norm(g) {
				return g, true
			}
		}
	}
	return "", false
}

// promoteAdmin makes a user admin when they belong to an admin group or their
// username is listed in TIPHYS_BOOTSTRAP_ADMINS. The change and its audit event
// are written in one transaction, like any other role change. It never demotes.
func (s *AuthService) promoteAdmin(ctx context.Context, u domain.User, c OIDCClaims, ip, userAgent string) (domain.User, bool, error) {
	if u.Role == domain.RoleAdmin || u.Disabled {
		return u, false, nil
	}
	operation, updatedBy, why := "promote_by_group", "keycloak-group", map[string]any{}
	if group, ok := s.inAdminGroup(c.Groups); ok {
		why["group"] = group
	} else if _, ok := s.bootstrap[strings.ToLower(c.PreferredUsername)]; ok {
		operation, updatedBy = "promote_by_username", "bootstrap-admins"
	} else {
		return u, false, nil
	}
	var ev domain.AuditEvent
	before := u.Role
	err := s.store.InTx(ctx, func(r Repositories) error {
		cur, err := r.Users.Get(ctx, u.ID)
		if err != nil {
			return err
		}
		cur.Role, cur.UpdatedBy = domain.RoleAdmin, updatedBy
		if err := r.Users.Update(ctx, cur); err != nil {
			return err
		}
		u = cur
		e := s.audit.SystemEvent(domain.EventAdminChange)
		e.Result, e.SourceIP, e.UserAgent = domain.ResultSucceeded, ip, userAgent
		e.Details = map[string]any{"entity": "user", "operation": operation, "id": u.ID, "username": u.Username,
			"before": map[string]any{"role": before}, "after": map[string]any{"role": domain.RoleAdmin}}
		for k, v := range why {
			e.Details[k] = v
		}
		ev, err = s.audit.RecordIn(ctx, r, e)
		return err
	})
	if err != nil {
		return u, false, fmt.Errorf("promote admin at login: %w", err)
	}
	s.audit.Publish(ev)
	return u, true, nil
}

func (s *AuthService) sealTokens(ws WebSession) (WebSession, error) {
	var err error
	if ws.AccessToken, err = s.sealer.Seal([]byte(ws.AccessToken)); err != nil {
		return ws, err
	}
	if ws.RefreshToken, err = s.sealer.Seal([]byte(ws.RefreshToken)); err != nil {
		return ws, err
	}
	if ws.IDToken, err = s.sealer.Seal([]byte(ws.IDToken)); err != nil {
		return ws, err
	}
	return ws, nil
}

func (s *AuthService) openRefreshToken(ws WebSession) (string, error) {
	b, err := s.sealer.Open(ws.RefreshToken)
	return string(b), err
}

// Authenticate resolves the session cookie token to a Principal, refreshing
// with Keycloak when the last check is older than SessionRefreshInterval.
func (s *AuthService) Authenticate(ctx context.Context, token, ip, userAgent string) (Principal, error) {
	if token == "" {
		return Principal{}, ErrUnauthenticated
	}
	id := SessionID(token)
	ws, err := s.store.Repos().WebSessions.Get(ctx, id)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return Principal{}, ErrUnauthenticated
		}
		return Principal{}, err
	}
	now := s.clock.Now()
	if !now.Before(ws.ExpiresAt) {
		s.endSession(ctx, id)
		return Principal{}, ErrUnauthenticated
	}
	if now.Sub(ws.LastRefreshAt) > SessionRefreshInterval {
		if err := s.refresh(ctx, ws); err != nil {
			return Principal{}, ErrUnauthenticated
		}
	}
	user, ok := s.policy.Current().Users[ws.UserID]
	if !ok {
		var err error
		user, err = s.store.Repos().Users.Get(ctx, ws.UserID)
		if err != nil {
			return Principal{}, ErrUnauthenticated
		}
	}
	if user.Disabled {
		s.endSession(ctx, id)
		return Principal{}, ErrUnauthenticated
	}
	return Principal{User: user, SessionID: id, CSRFToken: ws.CSRFToken, SourceIP: ip, UserAgent: userAgent}, nil
}

func (s *AuthService) sessionLock(id string) *sync.Mutex {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	m, ok := s.refreshing[id]
	if !ok {
		m = &sync.Mutex{}
		s.refreshing[id] = m
	}
	return m
}

// refresh re-checks the session with Keycloak; on failure the session ends.
func (s *AuthService) refresh(ctx context.Context, ws WebSession) error {
	lock := s.sessionLock(ws.ID)
	lock.Lock()
	defer lock.Unlock()

	// Another request may have refreshed while this one waited.
	repos := s.store.Repos()
	if cur, err := repos.WebSessions.Get(ctx, ws.ID); err != nil {
		return ErrUnauthenticated
	} else if s.clock.Now().Sub(cur.LastRefreshAt) <= SessionRefreshInterval {
		return nil
	} else {
		ws = cur
	}
	if u, err := repos.Users.Get(ctx, ws.UserID); err == nil && u.Local {
		return s.refreshLocal(ctx, ws, u)
	}

	rt, err := s.openRefreshToken(ws)
	if err != nil {
		s.endSession(ctx, ws.ID)
		return ErrUnauthenticated
	}
	rctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	tokens, err := s.oidc.Refresh(rctx, rt)
	if err != nil {
		s.logger.Info("keycloak refresh failed, ending session", zap.Int64("user_id", ws.UserID), zap.Error(err))
		s.endSession(ctx, ws.ID)
		return ErrUnauthenticated
	}
	now := s.clock.Now()
	expires := tokens.SessionExpiry
	if expires.IsZero() {
		expires = ws.ExpiresAt
	}
	if tokens.RefreshToken == "" {
		tokens.RefreshToken = rt
	}
	idTok := tokens.IDToken
	if idTok == "" {
		if b, err := s.sealer.Open(ws.IDToken); err == nil {
			idTok = string(b)
		}
	}
	sealed, err := s.sealTokens(WebSession{AccessToken: tokens.AccessToken, RefreshToken: tokens.RefreshToken, IDToken: idTok})
	if err != nil {
		return err
	}
	if err := repos.WebSessions.UpdateTokens(ctx, ws.ID, sealed.AccessToken, sealed.RefreshToken, sealed.IDToken, now, expires); err != nil {
		return fmt.Errorf("store refreshed tokens: %w", err)
	}
	return nil
}

// refreshLocal re-checks a local user's session: there is no Keycloak to ask,
// so it ends only when the user is disabled, local users are turned off or it
// expires.
func (s *AuthService) refreshLocal(ctx context.Context, ws WebSession, u domain.User) error {
	if !s.cfg.LocalUsers || u.Disabled {
		s.endSession(ctx, ws.ID)
		return ErrUnauthenticated
	}
	if err := s.store.Repos().WebSessions.UpdateTokens(ctx, ws.ID, ws.AccessToken, ws.RefreshToken, ws.IDToken, s.clock.Now(), ws.ExpiresAt); err != nil {
		return fmt.Errorf("store session refresh: %w", err)
	}
	return nil
}

// LocalUsersEnabled reports whether LOCAL_USERS_ENABLED is on.
func (s *AuthService) LocalUsersEnabled() bool { return s.cfg.LocalUsers }

// LocalLogin checks a local user's password and creates a session. It returns
// the cookie token. Unknown users, Keycloak users and wrong passwords all fail
// with ErrInvalidCredentials; a disabled user is told only after the password
// matched.
func (s *AuthService) LocalLogin(ctx context.Context, username, password, ip, userAgent string) (string, error) {
	repos := s.store.Repos()
	var user domain.User
	var hash string
	if s.cfg.LocalUsers && username != "" {
		u, err := repos.Users.GetByUsername(ctx, username)
		switch {
		case err == nil && u.Local:
			if hash, err = repos.Users.PasswordHash(ctx, u.ID); err != nil {
				return "", err
			}
			user = u
		case err != nil && !errors.Is(err, ErrNotFound):
			return "", err
		}
	}
	if !s.checkPassword(hash, password) {
		if user.ID != 0 {
			e := s.audit.Event(Principal{User: user, SourceIP: ip, UserAgent: userAgent}, domain.EventLogin)
			e.Result, e.Reason = domain.ResultDenied, "invalid_credentials"
			e.Details = map[string]any{"method": "local"}
			s.audit.RecordBestEffort(ctx, e)
		}
		return "", ErrInvalidCredentials
	}
	if user.Disabled {
		e := s.audit.Event(Principal{User: user, SourceIP: ip, UserAgent: userAgent}, domain.EventLogin)
		e.Result, e.Reason = domain.ResultDenied, string(domain.ReasonUserDisabled)
		e.Details = map[string]any{"method": "local"}
		s.audit.RecordBestEffort(ctx, e)
		return "", ErrUserDisabled
	}

	now := s.clock.Now()
	user.LastLoginAt = &now
	user.UpdatedBy = "login"
	if err := repos.Users.Update(ctx, user); err != nil {
		return "", fmt.Errorf("update user at login: %w", err)
	}
	token := randomToken(32)
	ws, err := s.sealTokens(WebSession{
		ID: SessionID(token), UserID: user.ID, CSRFToken: randomToken(24),
		CreatedAt: now, LastRefreshAt: now, ExpiresAt: now.Add(localSessionMaxAge), SourceIP: ip, UserAgent: userAgent,
	})
	if err != nil {
		return "", err
	}
	if err := repos.WebSessions.Create(ctx, ws); err != nil {
		return "", fmt.Errorf("create session: %w", err)
	}

	e := s.audit.Event(Principal{User: user, SessionID: ws.ID, SourceIP: ip, UserAgent: userAgent}, domain.EventLogin)
	e.Result = domain.ResultSucceeded
	e.SessionID = ""
	e.Details = map[string]any{"method": "local"}
	s.audit.RecordBestEffort(ctx, e)
	return token, nil
}

// checkPassword compares in constant time; with no hash (or a password bcrypt
// would truncate) it still spends the same bcrypt work and fails.
func (s *AuthService) checkPassword(hash, password string) bool {
	if hash == "" || len(password) > maxPasswordBytes {
		s.dummyOnce.Do(func() {
			s.dummyHash, _ = bcrypt.GenerateFromPassword([]byte(randomToken(16)), passwordCost)
		})
		_ = bcrypt.CompareHashAndPassword(s.dummyHash, []byte(password))
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

// hashPassword checks a new local password and returns its bcrypt hash.
func hashPassword(password string) (string, error) {
	switch {
	case len([]rune(password)) < minPasswordLength:
		return "", Invalid("password", fmt.Sprintf("at least %d characters", minPasswordLength))
	case len(password) > maxPasswordBytes:
		return "", Invalid("password", fmt.Sprintf("at most %d bytes", maxPasswordBytes))
	}
	h, err := bcrypt.GenerateFromPassword([]byte(password), passwordCost)
	if err != nil {
		return "", fmt.Errorf("hash password: %w", err)
	}
	return string(h), nil
}

// endSession deletes a session and closes its streams.
func (s *AuthService) endSession(ctx context.Context, id string) {
	if err := s.store.Repos().WebSessions.Delete(ctx, id); err != nil {
		s.logger.Error("failed to delete session", zap.Error(err))
	}
	s.refreshMu.Lock()
	delete(s.refreshing, id)
	s.refreshMu.Unlock()
	if s.onSessionEnded != nil {
		s.onSessionEnded(id)
	}
}

// Logout ends the session and returns where the browser should go next.
func (s *AuthService) Logout(ctx context.Context, p Principal) string {
	var idToken string
	if ws, err := s.store.Repos().WebSessions.Get(ctx, p.SessionID); err == nil {
		if b, err := s.sealer.Open(ws.IDToken); err == nil {
			idToken = string(b)
		}
	}
	e := s.audit.Event(p, domain.EventLogout)
	e.Result = domain.ResultSucceeded
	s.audit.RecordBestEffort(ctx, e)
	s.endSession(ctx, p.SessionID)
	if p.User.Local {
		return "/login"
	}
	if u := s.oidc.LogoutURL(idToken, s.cfg.PostLogoutRedirect); u != "" {
		return u
	}
	return "/"
}

// EndUserSessions deletes every session of a user (used when disabling).
func (s *AuthService) EndUserSessions(ctx context.Context, userID int64) {
	if _, err := s.store.Repos().WebSessions.DeleteByUser(ctx, userID); err != nil {
		s.logger.Error("failed to delete user sessions", zap.Int64("user_id", userID), zap.Error(err))
	}
}

// RefreshLoop re-checks sessions with Keycloak in the background, so open
// streams (which make no HTTP requests) are still tied to the Keycloak
// session. Run it in its own goroutine.
func (s *AuthService) RefreshLoop(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			due, err := s.store.Repos().WebSessions.ListRefreshDue(ctx, s.clock.Now().Add(-SessionRefreshInterval+time.Minute))
			if err != nil {
				s.logger.Error("failed to list sessions due for refresh", zap.Error(err))
				continue
			}
			for _, ws := range due {
				_ = s.refresh(ctx, ws)
			}
			if _, err := s.store.Repos().WebSessions.DeleteExpired(ctx, s.clock.Now()); err != nil {
				s.logger.Error("failed to delete expired sessions", zap.Error(err))
			}
		}
	}
}
