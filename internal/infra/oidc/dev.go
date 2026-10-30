package oidc

import (
	"context"
	"net/url"

	"tiphys/internal/application"
)

// DevAdminGroup is the group the Dev provider puts an admin in; the caller
// adds it to the admin groups so the user is promoted at every login.
const DevAdminGroup = "tiphys-dev-admins"

// DevAdminUser is the DEV_LOGIN_USER that logs in as an admin; any other
// name logs in as a regular user.
const DevAdminUser = "admin"

// Dev implements application.OIDCProvider without Keycloak, for local
// development (DEV_LOGIN_USER): the authorization URL is the portal's own
// callback, and every login succeeds as the same user.
type Dev struct {
	username    string
	redirectURL string
}

var _ application.OIDCProvider = (*Dev)(nil)

func NewDev(username, redirectURL string) *Dev {
	return &Dev{username: username, redirectURL: redirectURL}
}

func (d *Dev) AuthCodeURL(state, _, _ string) string {
	return d.redirectURL + "?" + url.Values{"code": {"dev"}, "state": {state}}.Encode()
}

func (d *Dev) Exchange(context.Context, string, string, string) (application.OIDCTokens, application.OIDCClaims, error) {
	c := application.OIDCClaims{Subject: "dev:" + d.username, PreferredUsername: d.username, Name: d.username}
	if d.username == DevAdminUser {
		c.Groups = []string{DevAdminGroup}
	}
	return d.tokens(), c, nil
}

// Refresh fails for a session of another DEV_LOGIN_USER, so switching the
// user and restarting ends the old session at its next refresh.
func (d *Dev) Refresh(_ context.Context, refreshToken string) (application.OIDCTokens, error) {
	if refreshToken != d.tokens().RefreshToken {
		return application.OIDCTokens{}, ErrSessionEnded
	}
	return d.tokens(), nil
}

func (d *Dev) LogoutURL(string, string) string { return "" }

func (d *Dev) tokens() application.OIDCTokens {
	t := "dev:" + d.username
	return application.OIDCTokens{AccessToken: t, RefreshToken: t, IDToken: t}
}
