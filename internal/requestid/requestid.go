// Package requestid carries a request's ID through ctx so every log line of
// that request, and every upstream call it makes, can be tied together.
package requestid

import (
	"context"

	"go.uber.org/zap"
)

// Header is the request ID header. The Istio sidecar sets it on incoming
// requests; we forward it on outgoing ones so upstream logs carry the same ID.
const Header = "X-Request-ID"

type ctxKey struct{}

func NewContext(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, ctxKey{}, id)
}

// FromContext returns ctx's request ID, or "" if it has none.
func FromContext(ctx context.Context) string {
	id, _ := ctx.Value(ctxKey{}).(string)
	return id
}

// Field is ctx's request ID as a log field.
func Field(ctx context.Context) zap.Field {
	return zap.String("request_id", FromContext(ctx))
}
