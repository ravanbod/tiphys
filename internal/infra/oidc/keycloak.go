// Package oidc implements application.OIDCProvider for Keycloak: the
// authorization code flow with PKCE as a confidential client.
package oidc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	gooidc "github.com/coreos/go-oidc/v3/oidc"
	"go.uber.org/zap"
	"golang.org/x/oauth2"

	"tiphys/internal/application"
)

// ErrSessionEnded is returned by Refresh when Keycloak rejects the refresh
// token (invalid_grant): the Keycloak session is over and the user must log
// in again. Any other Refresh error is transient (Keycloak unreachable or
// failing) and the session may still be valid.
var ErrSessionEnded = errors.New("keycloak session ended")

// DefaultScopes: offline_access makes Keycloak issue the refresh token the
// portal needs to re-check the session.
var DefaultScopes = []string{gooidc.ScopeOpenID, "profile", "email", gooidc.ScopeOfflineAccess}

const callTimeout = 15 * time.Second

// Config holds the Keycloak client settings. The caller reads the client
// secret from its mounted file.
type Config struct {
	Issuer       string // e.g. https://keycloak.example.com/realms/main
	ClientID     string
	ClientSecret string
	RedirectURL  string
	Scopes       []string // DefaultScopes when empty
	// GroupsClaim is the ID token claim holding the user's groups; "groups"
	// when empty (Keycloak's group membership mapper).
	GroupsClaim string
}

// Keycloak is the OIDCProvider for one Keycloak realm.
type Keycloak struct {
	oauth       oauth2.Config
	verifier    *gooidc.IDTokenVerifier
	endSession  string
	httpClient  *http.Client
	logger      *zap.Logger
	groupsClaim string
}

// NewKeycloak performs issuer discovery, so Keycloak must be reachable at
// startup. TLS settings (custom CA) belong to the injected *http.Client.
func NewKeycloak(ctx context.Context, cfg Config, httpClient *http.Client, logger *zap.Logger) (*Keycloak, error) {
	ctx = gooidc.ClientContext(ctx, httpClient)
	groupsClaim := cfg.GroupsClaim
	if groupsClaim == "" {
		groupsClaim = "groups"
	}
	provider, err := gooidc.NewProvider(ctx, cfg.Issuer)
	if err != nil {
		logger.Error("oidc issuer discovery failed", zap.String("issuer", cfg.Issuer), zap.Error(err))
		return nil, fmt.Errorf("oidc issuer discovery failed: %w", err)
	}
	var meta struct {
		EndSessionEndpoint string `json:"end_session_endpoint"`
	}
	if err := provider.Claims(&meta); err != nil {
		logger.Error("failed to read oidc discovery document", zap.Error(err))
		return nil, fmt.Errorf("failed to read oidc discovery document: %w", err)
	}

	scopes := cfg.Scopes
	if len(scopes) == 0 {
		scopes = DefaultScopes
	}
	endpoint := provider.Endpoint()
	// Fixed, so a failed call is never retried with the other style (which
	// would blur invalid_grant with a client authentication error).
	endpoint.AuthStyle = oauth2.AuthStyleInHeader

	return &Keycloak{
		oauth: oauth2.Config{
			ClientID:     cfg.ClientID,
			ClientSecret: cfg.ClientSecret,
			Endpoint:     endpoint,
			RedirectURL:  cfg.RedirectURL,
			Scopes:       scopes,
		},
		verifier:    provider.Verifier(&gooidc.Config{ClientID: cfg.ClientID}),
		groupsClaim: groupsClaim,
		endSession:  meta.EndSessionEndpoint,
		httpClient:  httpClient,
		logger:      logger,
	}, nil
}

// AuthCodeURL is the Keycloak login URL with state, nonce and a PKCE S256
// challenge (the caller derives it from the verifier it keeps).
func (k *Keycloak) AuthCodeURL(state, nonce, codeChallenge string) string {
	return k.oauth.AuthCodeURL(state,
		gooidc.Nonce(nonce),
		oauth2.SetAuthURLParam("code_challenge", codeChallenge),
		oauth2.SetAuthURLParam("code_challenge_method", "S256"),
	)
}

// Exchange trades the authorization code for tokens and verifies the ID token:
// signature, issuer, audience, expiry and nonce.
func (k *Keycloak) Exchange(ctx context.Context, code, codeVerifier, nonce string) (application.OIDCTokens, application.OIDCClaims, error) {
	ctx, cancel := k.callContext(ctx)
	defer cancel()

	tok, err := k.oauth.Exchange(ctx, code, oauth2.VerifierOption(codeVerifier))
	if err != nil {
		if isInvalidGrant(err) { // wrong or replayed code: the user's problem, not an outage
			k.logger.Debug("oidc code exchange rejected", zap.Error(err))
		} else {
			k.logger.Error("oidc code exchange failed", zap.Error(err))
		}
		return application.OIDCTokens{}, application.OIDCClaims{}, fmt.Errorf("oidc code exchange failed: %w", err)
	}
	rawID, _ := tok.Extra("id_token").(string)
	if rawID == "" {
		k.logger.Error("oidc token response has no id_token")
		return application.OIDCTokens{}, application.OIDCClaims{}, errors.New("oidc token response has no id_token")
	}
	idToken, err := k.verifier.Verify(ctx, rawID)
	if err != nil {
		k.logger.Warn("oidc id token verification failed", zap.Error(err))
		return application.OIDCTokens{}, application.OIDCClaims{}, fmt.Errorf("oidc id token verification failed: %w", err)
	}
	if nonce == "" || idToken.Nonce != nonce {
		k.logger.Warn("oidc id token nonce mismatch")
		return application.OIDCTokens{}, application.OIDCClaims{}, errors.New("oidc id token nonce mismatch")
	}
	var c struct {
		PreferredUsername string `json:"preferred_username"`
		Name              string `json:"name"`
		Email             string `json:"email"`
	}
	if err := idToken.Claims(&c); err != nil {
		k.logger.Error("failed to decode oidc id token claims", zap.Error(err))
		return application.OIDCTokens{}, application.OIDCClaims{}, fmt.Errorf("failed to decode oidc id token claims: %w", err)
	}
	var all map[string]json.RawMessage
	if err := idToken.Claims(&all); err != nil {
		k.logger.Error("failed to decode oidc id token claims", zap.Error(err))
		return application.OIDCTokens{}, application.OIDCClaims{}, fmt.Errorf("failed to decode oidc id token claims: %w", err)
	}
	if idToken.Subject == "" || c.PreferredUsername == "" {
		// Keycloak puts preferred_username in the ID token via the profile scope mapper.
		k.logger.Error("oidc id token lacks sub or preferred_username; check the client's scope mappers", zap.Bool("has_sub", idToken.Subject != ""))
		return application.OIDCTokens{}, application.OIDCClaims{}, errors.New("oidc id token lacks sub or preferred_username")
	}

	return tokensFrom(tok, rawID), application.OIDCClaims{
		Subject:           idToken.Subject,
		PreferredUsername: c.PreferredUsername,
		Name:              c.Name,
		Email:             c.Email,
		Groups:            groupsFrom(all[k.groupsClaim]),
	}, nil
}

// groupsFrom reads a groups claim given as a JSON array of strings (what
// Keycloak's group membership mapper emits) or as a single string.
func groupsFrom(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var list []string
	if err := json.Unmarshal(raw, &list); err == nil {
		return list
	}
	var one string
	if err := json.Unmarshal(raw, &one); err == nil && one != "" {
		return []string{one}
	}
	return nil
}

// Refresh exchanges the refresh token for new tokens. It returns
// ErrSessionEnded when Keycloak says the grant is no longer valid; any other
// error is transient.
func (k *Keycloak) Refresh(ctx context.Context, refreshToken string) (application.OIDCTokens, error) {
	ctx, cancel := k.callContext(ctx)
	defer cancel()

	// An already-expired token makes the token source go to Keycloak.
	stale := &oauth2.Token{RefreshToken: refreshToken, Expiry: time.Unix(1, 0)}
	tok, err := k.oauth.TokenSource(ctx, stale).Token()
	if err != nil {
		if isInvalidGrant(err) {
			k.logger.Debug("oidc refresh rejected, keycloak session ended", zap.Error(err))
			return application.OIDCTokens{}, fmt.Errorf("%w: %v", ErrSessionEnded, err)
		}
		k.logger.Error("oidc refresh failed", zap.Error(err))
		return application.OIDCTokens{}, fmt.Errorf("oidc refresh failed: %w", err)
	}
	rawID, _ := tok.Extra("id_token").(string)
	return tokensFrom(tok, rawID), nil
}

// LogoutURL is the RP-initiated logout URL, or "" if Keycloak has no
// end_session_endpoint. client_id is always sent: Keycloak requires it (or the
// hint) to accept post_logout_redirect_uri.
func (k *Keycloak) LogoutURL(idToken, postLogoutRedirect string) string {
	if k.endSession == "" {
		return ""
	}
	u, err := url.Parse(k.endSession)
	if err != nil {
		return ""
	}
	q := u.Query()
	q.Set("client_id", k.oauth.ClientID)
	if idToken != "" {
		q.Set("id_token_hint", idToken)
	}
	if postLogoutRedirect != "" {
		q.Set("post_logout_redirect_uri", postLogoutRedirect)
	}
	u.RawQuery = q.Encode()
	return u.String()
}

func (k *Keycloak) callContext(ctx context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	return gooidc.ClientContext(ctx, k.httpClient), cancel
}

func isInvalidGrant(err error) bool {
	var re *oauth2.RetrieveError
	return errors.As(err, &re) && re.ErrorCode == "invalid_grant"
}

func tokensFrom(tok *oauth2.Token, idToken string) application.OIDCTokens {
	t := application.OIDCTokens{
		AccessToken:  tok.AccessToken,
		RefreshToken: tok.RefreshToken,
		IDToken:      idToken,
		Expiry:       tok.Expiry,
	}
	t.SessionExpiry = refreshTokenExpiry(tok)
	return t
}

// refreshTokenExpiry follows the Keycloak session: the refresh token's exp
// claim, or refresh_expires_in from the response. Zero when neither says (an
// offline token has no expiry).
func refreshTokenExpiry(tok *oauth2.Token) time.Time {
	if parts := strings.Split(tok.RefreshToken, "."); len(parts) == 3 {
		if payload, err := base64.RawURLEncoding.DecodeString(parts[1]); err == nil {
			var c struct {
				Exp float64 `json:"exp"`
			}
			if json.Unmarshal(payload, &c) == nil && c.Exp > 0 {
				return time.Unix(int64(c.Exp), 0)
			}
		}
	}
	if secs, ok := numeric(tok.Extra("refresh_expires_in")); ok && secs > 0 {
		return time.Now().Add(time.Duration(secs) * time.Second)
	}
	return time.Time{}
}

func numeric(v any) (int64, bool) {
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case string:
		i, err := strconv.ParseInt(n, 10, 64)
		return i, err == nil
	}
	return 0, false
}
