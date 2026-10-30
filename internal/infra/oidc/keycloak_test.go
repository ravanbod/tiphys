package oidc

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"
	"golang.org/x/oauth2"
)

const (
	clientID     = "tiphys"
	clientSecret = "s3cret"
	redirectURL  = "https://portal.example.com/auth/callback"
)

var b64 = base64.RawURLEncoding

// fakeKeycloak is a minimal OIDC provider: discovery, JWKS, a token endpoint
// that checks client credentials and PKCE, and refresh with invalid_grant.
type fakeKeycloak struct {
	t   *testing.T
	srv *httptest.Server
	key *rsa.PrivateKey

	mu         sync.Mutex
	codes      map[string]authRequest // code -> what the login request asked for
	refreshOK  map[string]bool        // refresh tokens Keycloak still accepts
	refreshErr int                    // when set, the token endpoint answers this status to refresh
	// knobs for tampered tokens
	idIssuer, idAudience string
	idSigner             *rsa.PrivateKey
	idExpiresIn          time.Duration
	refreshExp           int64 // exp claim in issued refresh tokens (0 = none)
	noEndSession         bool
}

type authRequest struct{ nonce, challenge string }

func newFake(t *testing.T) *fakeKeycloak {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeKeycloak{t: t, key: key, codes: map[string]authRequest{}, refreshOK: map[string]bool{}, idExpiresIn: time.Hour, idSigner: key}
	mux := http.NewServeMux()
	mux.HandleFunc("/realms/main/.well-known/openid-configuration", f.discovery)
	mux.HandleFunc("/realms/main/certs", f.jwks)
	mux.HandleFunc("/realms/main/token", f.token)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	f.idIssuer, f.idAudience = f.issuer(), clientID
	return f
}

func (f *fakeKeycloak) issuer() string { return f.srv.URL + "/realms/main" }

func (f *fakeKeycloak) discovery(w http.ResponseWriter, r *http.Request) {
	doc := map[string]any{
		"issuer":                                f.issuer(),
		"authorization_endpoint":                f.issuer() + "/auth",
		"token_endpoint":                        f.issuer() + "/token",
		"jwks_uri":                              f.issuer() + "/certs",
		"id_token_signing_alg_values_supported": []string{"RS256"},
	}
	if !f.noEndSession {
		doc["end_session_endpoint"] = f.issuer() + "/logout"
	}
	json.NewEncoder(w).Encode(doc)
}

func (f *fakeKeycloak) jwks(w http.ResponseWriter, r *http.Request) {
	json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
		"kty": "RSA", "alg": "RS256", "use": "sig", "kid": "k1",
		"n": b64.EncodeToString(f.key.N.Bytes()),
		"e": b64.EncodeToString(big.NewInt(int64(f.key.E)).Bytes()),
	}}})
}

func (f *fakeKeycloak) sign(key *rsa.PrivateKey, claims map[string]any) string {
	hdr, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": "k1"})
	body, _ := json.Marshal(claims)
	signing := b64.EncodeToString(hdr) + "." + b64.EncodeToString(body)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		f.t.Fatal(err)
	}
	return signing + "." + b64.EncodeToString(sig)
}

func oauthError(w http.ResponseWriter, status int, code, desc string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": code, "error_description": desc})
}

func (f *fakeKeycloak) token(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if u, p, ok := r.BasicAuth(); !ok || u != clientID || p != clientSecret {
		oauthError(w, 401, "invalid_client", "bad client credentials")
		return
	}
	r.ParseForm()
	now := time.Now()
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		req, ok := f.codes[r.PostForm.Get("code")]
		if !ok {
			oauthError(w, 400, "invalid_grant", "Code not valid")
			return
		}
		delete(f.codes, r.PostForm.Get("code")) // single use
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		if b64.EncodeToString(sum[:]) != req.challenge {
			oauthError(w, 400, "invalid_grant", "PKCE verification failed")
			return
		}
		if r.PostForm.Get("redirect_uri") != redirectURL {
			oauthError(w, 400, "invalid_grant", "Incorrect redirect_uri")
			return
		}
		f.respond(w, now, req.nonce)
	case "refresh_token":
		rt := r.PostForm.Get("refresh_token")
		if f.refreshErr != 0 {
			w.WriteHeader(f.refreshErr)
			return
		}
		if !f.refreshOK[rt] {
			oauthError(w, 400, "invalid_grant", "Token is not active")
			return
		}
		delete(f.refreshOK, rt) // rotation
		f.respond(w, now, "")
	default:
		oauthError(w, 400, "unsupported_grant_type", "")
	}
}

func (f *fakeKeycloak) respond(w http.ResponseWriter, now time.Time, nonce string) {
	idClaims := map[string]any{
		"iss": f.idIssuer, "aud": f.idAudience, "sub": "9d1f-uuid", "azp": clientID,
		"iat": now.Unix(), "exp": now.Add(f.idExpiresIn).Unix(),
		"preferred_username": "alice", "name": "Alice Example", "email": "alice@example.com",
	}
	if nonce != "" {
		idClaims["nonce"] = nonce
	}
	rtClaims := map[string]any{"typ": "Refresh", "sub": "9d1f-uuid", "jti": randomString()}
	if f.refreshExp != 0 {
		rtClaims["exp"] = f.refreshExp
	}
	rt := f.sign(f.key, rtClaims)
	f.refreshOK[rt] = true
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"access_token": "access-" + randomString(), "token_type": "Bearer", "expires_in": 300,
		"refresh_token": rt, "id_token": f.sign(f.idSigner, idClaims),
		"refresh_expires_in": 1800,
	})
}

func randomString() string {
	b := make([]byte, 9)
	rand.Read(b)
	return b64.EncodeToString(b)
}

// login plays the browser: parses the provider's login URL, remembers the
// request under a new code and returns the code.
func (f *fakeKeycloak) login(t *testing.T, loginURL string) (code string, q url.Values) {
	t.Helper()
	u, err := url.Parse(loginURL)
	if err != nil {
		t.Fatal(err)
	}
	q = u.Query()
	code = randomString()
	f.mu.Lock()
	f.codes[code] = authRequest{nonce: q.Get("nonce"), challenge: q.Get("code_challenge")}
	f.mu.Unlock()
	return code, q
}

func challengeOf(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return b64.EncodeToString(sum[:])
}

func newProvider(t *testing.T, f *fakeKeycloak) *Keycloak {
	t.Helper()
	k, err := NewKeycloak(context.Background(), Config{
		Issuer: f.issuer(), ClientID: clientID, ClientSecret: clientSecret, RedirectURL: redirectURL,
	}, f.srv.Client(), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	return k
}

const verifier = "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk" // RFC 7636 appendix B

func TestAuthCodeURL(t *testing.T) {
	f := newFake(t)
	k := newProvider(t, f)
	u := k.AuthCodeURL("state-1", "nonce-1", challengeOf(verifier))
	if !strings.HasPrefix(u, f.issuer()+"/auth?") {
		t.Fatalf("url = %s", u)
	}
	pu, _ := url.Parse(u)
	q := pu.Query()
	for k, want := range map[string]string{
		"response_type": "code", "client_id": clientID, "redirect_uri": redirectURL, "state": "state-1",
		"nonce": "nonce-1", "code_challenge": challengeOf(verifier), "code_challenge_method": "S256",
		"scope": "openid profile email offline_access",
	} {
		if q.Get(k) != want {
			t.Errorf("%s = %q, want %q", k, q.Get(k), want)
		}
	}
}

func TestExchange(t *testing.T) {
	f := newFake(t)
	k := newProvider(t, f)
	exp := time.Now().Add(30 * time.Minute).Truncate(time.Second)
	f.refreshExp = exp.Unix()

	code, _ := f.login(t, k.AuthCodeURL("s", "n1", challengeOf(verifier)))
	tokens, claims, err := k.Exchange(context.Background(), code, verifier, "n1")
	if err != nil {
		t.Fatal(err)
	}
	if claims.Subject != "9d1f-uuid" || claims.PreferredUsername != "alice" || claims.Name != "Alice Example" || claims.Email != "alice@example.com" {
		t.Errorf("claims = %+v", claims)
	}
	if tokens.AccessToken == "" || tokens.RefreshToken == "" || tokens.IDToken == "" {
		t.Errorf("tokens = %+v", tokens)
	}
	if time.Until(tokens.Expiry) <= 0 || time.Until(tokens.Expiry) > 6*time.Minute {
		t.Errorf("access expiry = %v", tokens.Expiry)
	}
	if !tokens.SessionExpiry.Equal(exp) {
		t.Errorf("SessionExpiry = %v, want %v (refresh token exp)", tokens.SessionExpiry, exp)
	}
}

func TestExchangeFallsBackToRefreshExpiresIn(t *testing.T) {
	f := newFake(t) // refresh tokens without an exp claim; response says 1800s
	k := newProvider(t, f)
	code, _ := f.login(t, k.AuthCodeURL("s", "n", challengeOf(verifier)))
	tokens, _, err := k.Exchange(context.Background(), code, verifier, "n")
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Until(tokens.SessionExpiry); d < 29*time.Minute || d > 31*time.Minute {
		t.Errorf("SessionExpiry in %v", d)
	}
}

func TestExchangeRejects(t *testing.T) {
	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	cases := map[string]struct {
		tweak func(*fakeKeycloak)
		nonce string // nonce the portal expects
		ver   string
	}{
		"wrong code_verifier": {nil, "n", verifier + "x"},
		"wrong nonce":         {nil, "other-nonce", verifier},
		"empty nonce":         {nil, "", verifier},
		"wrong audience":      {func(f *fakeKeycloak) { f.idAudience = "someone-else" }, "n", verifier},
		"wrong issuer":        {func(f *fakeKeycloak) { f.idIssuer = "https://evil.example.com/realms/main" }, "n", verifier},
		"bad signature":       {func(f *fakeKeycloak) { f.idSigner = other }, "n", verifier},
		"expired id token":    {func(f *fakeKeycloak) { f.idExpiresIn = -time.Hour }, "n", verifier},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFake(t)
			if tc.tweak != nil {
				tc.tweak(f)
			}
			k := newProvider(t, f)
			code, _ := f.login(t, k.AuthCodeURL("s", "n", challengeOf(verifier)))
			if _, _, err := k.Exchange(context.Background(), code, tc.ver, tc.nonce); err == nil {
				t.Fatal("Exchange succeeded, want error")
			}
		})
	}
}

func TestExchangeCodeIsSingleUse(t *testing.T) {
	f := newFake(t)
	k := newProvider(t, f)
	code, _ := f.login(t, k.AuthCodeURL("s", "n", challengeOf(verifier)))
	if _, _, err := k.Exchange(context.Background(), code, verifier, "n"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := k.Exchange(context.Background(), code, verifier, "n"); err == nil {
		t.Error("replayed code accepted")
	}
}

func TestWrongClientSecret(t *testing.T) {
	f := newFake(t)
	k, err := NewKeycloak(context.Background(), Config{Issuer: f.issuer(), ClientID: clientID, ClientSecret: "wrong", RedirectURL: redirectURL}, f.srv.Client(), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	code, _ := f.login(t, k.AuthCodeURL("s", "n", challengeOf(verifier)))
	if _, _, err := k.Exchange(context.Background(), code, verifier, "n"); err == nil {
		t.Error("wrong client secret accepted")
	}
}

func TestRefresh(t *testing.T) {
	f := newFake(t)
	k := newProvider(t, f)
	exp := time.Now().Add(20 * time.Minute).Truncate(time.Second)
	f.refreshExp = exp.Unix()
	code, _ := f.login(t, k.AuthCodeURL("s", "n", challengeOf(verifier)))
	first, _, err := k.Exchange(context.Background(), code, verifier, "n")
	if err != nil {
		t.Fatal(err)
	}

	exp2 := exp.Add(10 * time.Minute)
	f.refreshExp = exp2.Unix()
	second, err := k.Refresh(context.Background(), first.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if second.AccessToken == "" || second.AccessToken == first.AccessToken || second.RefreshToken == first.RefreshToken || second.IDToken == "" {
		t.Errorf("tokens not renewed: %+v", second)
	}
	if !second.SessionExpiry.Equal(exp2) {
		t.Errorf("SessionExpiry = %v, want %v", second.SessionExpiry, exp2)
	}
	if time.Until(second.Expiry) <= 0 {
		t.Errorf("Expiry = %v", second.Expiry)
	}

	// Keycloak rotated the refresh token: the old one is dead, which is how
	// an ended session looks too.
	_, err = k.Refresh(context.Background(), first.RefreshToken)
	if !errors.Is(err, ErrSessionEnded) {
		t.Fatalf("old refresh token: err = %v, want ErrSessionEnded", err)
	}
}

func TestRefreshTransientError(t *testing.T) {
	f := newFake(t)
	k := newProvider(t, f)
	code, _ := f.login(t, k.AuthCodeURL("s", "n", challengeOf(verifier)))
	tokens, _, _ := k.Exchange(context.Background(), code, verifier, "n")

	for _, status := range []int{http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable} {
		f.refreshErr = status
		_, err := k.Refresh(context.Background(), tokens.RefreshToken)
		if err == nil || errors.Is(err, ErrSessionEnded) {
			t.Errorf("status %d: err = %v, want a transient error", status, err)
		}
	}
	// Keycloak down entirely.
	f.srv.Close()
	if _, err := k.Refresh(context.Background(), tokens.RefreshToken); err == nil || errors.Is(err, ErrSessionEnded) {
		t.Errorf("unreachable: err = %v, want a transient error", err)
	}
}

func TestRefreshTokenWithoutExp(t *testing.T) {
	// An offline token has no exp and the response has no refresh_expires_in
	// we can use: SessionExpiry stays zero.
	if got := refreshTokenExpiry(&oauth2.Token{RefreshToken: "opaque-token"}); !got.IsZero() {
		t.Errorf("opaque token expiry = %v", got)
	}
}

func TestLogoutURL(t *testing.T) {
	f := newFake(t)
	k := newProvider(t, f)
	u, err := url.Parse(k.LogoutURL("the.id.token", "https://portal.example.com/"))
	if err != nil {
		t.Fatal(err)
	}
	if got := u.Scheme + "://" + u.Host + u.Path; got != f.issuer()+"/logout" {
		t.Errorf("endpoint = %s", got)
	}
	q := u.Query()
	if q.Get("id_token_hint") != "the.id.token" || q.Get("post_logout_redirect_uri") != "https://portal.example.com/" || q.Get("client_id") != clientID {
		t.Errorf("query = %v", q)
	}
	if q := mustQuery(t, k.LogoutURL("", "")); q.Has("id_token_hint") || q.Has("post_logout_redirect_uri") {
		t.Errorf("empty args should be omitted: %v", q)
	}

	f2 := newFake(t)
	f2.noEndSession = true
	if got := newProvider(t, f2).LogoutURL("x", "y"); got != "" {
		t.Errorf("no end_session_endpoint: LogoutURL = %q, want empty", got)
	}
}

func TestDiscoveryFailure(t *testing.T) {
	f := newFake(t)
	f.srv.Close()
	if _, err := NewKeycloak(context.Background(), Config{Issuer: f.issuer(), ClientID: clientID}, http.DefaultClient, zap.NewNop()); err == nil {
		t.Error("discovery against a dead server succeeded")
	}
}

func mustQuery(t *testing.T, raw string) url.Values {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u.Query()
}
