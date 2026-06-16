package edge

import (
	"context"

	"github.com/JumpTechCode/portcullis/internal/domain"
)

// identityKey is the unexported context key under which the request guard stores
// the authenticated identity. An unexported type prevents collisions with keys
// from other packages.
type identityKey struct{}

// withIdentity returns a copy of ctx carrying the authenticated identity.
func withIdentity(ctx context.Context, id domain.Identity) context.Context {
	return context.WithValue(ctx, identityKey{}, id)
}

// IdentityFromContext returns the authenticated identity the request guard
// placed in ctx, and whether one was present. Handlers downstream of
// [Guard.Wrap] use it to attribute a request to its client.
func IdentityFromContext(ctx context.Context) (domain.Identity, bool) {
	id, ok := ctx.Value(identityKey{}).(domain.Identity)
	return id, ok
}
