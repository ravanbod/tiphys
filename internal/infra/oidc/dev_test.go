package oidc

import (
	"context"
	"errors"
	"testing"
)

func TestDevRoles(t *testing.T) {
	ctx := context.Background()
	_, admin, _ := NewDev("admin", "http://p/auth/callback").Exchange(ctx, "", "", "")
	if len(admin.Groups) != 1 || admin.Groups[0] != DevAdminGroup {
		t.Fatalf("admin groups: %v", admin.Groups)
	}
	dev := NewDev("developer", "http://p/auth/callback")
	tok, claims, _ := dev.Exchange(ctx, "", "", "")
	if len(claims.Groups) != 0 || claims.PreferredUsername != "developer" {
		t.Fatalf("developer claims: %+v", claims)
	}
	if _, err := dev.Refresh(ctx, tok.RefreshToken); err != nil {
		t.Fatalf("own session must refresh: %v", err)
	}
	if _, err := NewDev("admin", "http://p/auth/callback").Refresh(ctx, tok.RefreshToken); !errors.Is(err, ErrSessionEnded) {
		t.Fatalf("another user's session must end: %v", err)
	}
}
