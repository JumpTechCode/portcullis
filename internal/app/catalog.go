package app

import (
	"context"
	"fmt"
	"sync"

	"github.com/JumpTechCode/portcullis/internal/aggregate"
	"github.com/JumpTechCode/portcullis/internal/domain"
	"github.com/JumpTechCode/portcullis/internal/registry"
)

// toolLister pages one downstream's full tool list. The composition root backs
// it with a transient pooled session to the named downstream; tests pass a fake.
// Tool listing is session-independent in V1 (a fixed catalog), so it need not run
// on a client's own per_client session — any session to the downstream reports
// the same tools.
type toolLister func(ctx context.Context, downstream string) ([]registry.ToolInfo, error)

// catalogSyncer pins wildcard policy rules to the observed catalog (ADR-0009).
// *policy.Engine satisfies it.
type catalogSyncer interface {
	Sync(catalog domain.Catalog)
}

// catalogCache builds the gateway-wide namespaced catalog once, lazily, on first
// use and serves it thereafter. The catalog spans every configured downstream
// (not just one client's allowed subset) because wildcard pinning is a
// gateway-wide operation: policy.Sync must observe the full tool set so a
// client's "github__*" pins to exactly the tools present at sync time. Per-client
// views are derived by filtering this catalog, never by re-syncing a partial one.
//
// V1 serves a fixed catalog, so the build runs once and is cached; live
// re-aggregation on a downstream tools/list_changed is a documented follow-up
// (ADR-0014). A build that fails (a downstream could not be listed) is not
// cached, so the next call retries and the catalog self-heals once the downstream
// recovers.
type catalogCache struct {
	downstreams []string
	list        toolLister
	syncer      catalogSyncer

	mu      sync.RWMutex
	built   bool
	catalog domain.Catalog
}

// get returns the gateway-wide catalog, building and caching it on first use. The
// build holds the write lock across the per-downstream listing so concurrent
// first callers build exactly once rather than racing to list every downstream.
func (c *catalogCache) get(ctx context.Context) (domain.Catalog, error) {
	c.mu.RLock()
	if c.built {
		cat := c.catalog
		c.mu.RUnlock()
		return cat, nil
	}
	c.mu.RUnlock()

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.built {
		return c.catalog, nil
	}

	listings := make([]aggregate.Listing, 0, len(c.downstreams))
	for _, name := range c.downstreams {
		tools, err := c.list(ctx, name)
		if err != nil {
			return domain.Catalog{}, fmt.Errorf("app: listing downstream %q: %w", name, err)
		}
		listings = append(listings, toListing(name, tools))
	}

	cat, err := aggregate.Build(listings)
	if err != nil {
		return domain.Catalog{}, fmt.Errorf("app: building catalog: %w", err)
	}
	c.syncer.Sync(cat)
	c.catalog = cat
	c.built = true
	return cat, nil
}

// toListing converts a downstream's reported tools into an aggregate.Listing,
// carrying each tool's description and raw input schema through unmodified.
func toListing(downstream string, tools []registry.ToolInfo) aggregate.Listing {
	out := aggregate.Listing{Downstream: downstream, Tools: make([]aggregate.Tool, len(tools))}
	for i, t := range tools {
		out.Tools[i] = aggregate.Tool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.InputSchema,
		}
	}
	return out
}
