// Package tenant carries the tenant ID of a request through context.Context.
// The API auth middleware sets it; handlers in every subsystem read it and
// pass it to the tenant-scoped internal services.
package tenant

import (
	"context"

	"github.com/portwatcher/lydiabox/internal/config"
)

type ctxKey struct{}

// WithID returns a context that carries tenantID.
func WithID(ctx context.Context, tenantID string) context.Context {
	return context.WithValue(ctx, ctxKey{}, tenantID)
}

// FromContext returns the tenant ID of ctx, or config.DefaultTenantID when
// none is set (single-user self-hosted mode).
func FromContext(ctx context.Context) string {
	if v, ok := ctx.Value(ctxKey{}).(string); ok && v != "" {
		return v
	}
	return config.DefaultTenantID
}
