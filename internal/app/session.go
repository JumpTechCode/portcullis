package app

import (
	"context"
	"encoding/json"

	"github.com/JumpTechCode/portcullis/internal/aggregate"
	"github.com/JumpTechCode/portcullis/internal/domain"
	"github.com/JumpTechCode/portcullis/internal/edge"
	"github.com/JumpTechCode/portcullis/internal/pipeline"
	"github.com/JumpTechCode/portcullis/internal/registry"
)

// gatewaySession is one client connection's view of the gateway: the
// policy-filtered catalog it may list and the pipeline-guarded invocation of
// those tools. It implements the edge seam (edge.Session), translating the MCP
// surface the edge speaks into the gateway's internals — the shared catalog, the
// per-identity policy filter, and this connection's own dispatcher-backed stage
// chain (ADR-0013/0014). One is built per client connection by the session
// factory.
type gatewaySession struct {
	identity domain.Identity
	// filter reduces the gateway-wide catalog to the tools this identity may see;
	// catalog filtering is security-load-bearing (design §8).
	filter domain.CatalogFilter
	// catalog is the shared, lazily built gateway-wide catalog.
	catalog *catalogCache
	// chain is this connection's security pipeline terminating in its own
	// per-connection dispatcher, so a call can only reach this connection's
	// downstream sessions (mis-routing is not expressible).
	chain domain.HandlerFunc
	// known is the set of configured downstream names, used to resolve a
	// namespaced call to a downstream before dispatch.
	known map[string]bool
	// pageSize bounds one client-facing tools/list page.
	pageSize int
	// closeFn releases this connection's downstream sessions.
	closeFn func() error
}

// Compile-time assertion that gatewaySession satisfies the edge seam.
var _ edge.Session = (*gatewaySession)(nil)

// ListTools returns one page of this client's namespaced, policy-filtered
// catalog. The full catalog is built once and shared; the per-client view is
// derived by filtering, then paginated with the opaque, name-stable cursor.
func (s *gatewaySession) ListTools(ctx context.Context, cursor string) (domain.Catalog, string, error) {
	full, err := s.catalog.get(ctx)
	if err != nil {
		return domain.Catalog{}, "", err
	}
	filtered := s.filter.Filter(s.identity, full)
	return aggregate.Page(filtered, cursor, s.pageSize)
}

// Call resolves a namespaced tool name to its downstream and runs the invocation
// through this connection's security pipeline. Resolution rejects a malformed or
// unknown name before any downstream work; policy enforcement (deny-by-default)
// happens inside the chain, so an unauthorized but well-formed name is denied
// there rather than here.
func (s *gatewaySession) Call(ctx context.Context, name string, args json.RawMessage) (*domain.Result, error) {
	ref, err := aggregate.Resolve(name, s.known)
	if err != nil {
		return nil, err
	}
	call := &domain.Call{Client: s.identity, Tool: ref, Args: args}
	return s.chain(ctx, call)
}

// Close releases this connection's downstream sessions. It is called once, when
// the client's MCP session ends.
func (s *gatewaySession) Close() error { return s.closeFn() }

// clientSessionFactory is the subset of registry.Manager the session factory
// needs: it hands out a fresh per-connection dispatcher. *registry.Manager
// satisfies it.
type clientSessionFactory interface {
	NewClientSession() *registry.ClientSession
}

// sessionDeps gathers what the session factory composes per connection: the
// downstream session manager, the per-call stage dependencies, the shared
// catalog and policy filter, the known downstream set, and the client-facing
// page size.
type sessionDeps struct {
	manager  clientSessionFactory
	stages   *stageDeps
	filter   domain.CatalogFilter
	catalog  *catalogCache
	known    map[string]bool
	pageSize int
}

// newSessionFactory returns the edge.SessionFactory the edge calls once per
// authenticated client connection. Each call opens a fresh per-connection
// dispatcher and wraps it in the security stage chain, so every connection gets
// its own isolated downstream session set behind an identical pipeline.
func newSessionFactory(d sessionDeps) edge.SessionFactory {
	return func(id domain.Identity) (edge.Session, error) {
		cs := d.manager.NewClientSession()
		chain := pipeline.Chain(buildStages(d.stages), pipeline.FromDispatcher(cs))
		return &gatewaySession{
			identity: id,
			filter:   d.filter,
			catalog:  d.catalog,
			chain:    chain,
			known:    d.known,
			pageSize: d.pageSize,
			closeFn:  cs.Close,
		}, nil
	}
}
