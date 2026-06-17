package app

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/JumpTechCode/portcullis/internal/domain"
	"github.com/JumpTechCode/portcullis/internal/edge"
	"github.com/JumpTechCode/portcullis/internal/policy"
	"github.com/JumpTechCode/portcullis/internal/registry"
	"github.com/JumpTechCode/portcullis/internal/resilience"
)

// gatewaySession must satisfy the edge seam.
var _ edge.Session = (*gatewaySession)(nil)

func fixedCatalog(t *testing.T, eng *policy.Engine, listings map[string][]registry.ToolInfo) *catalogCache {
	t.Helper()
	names := make([]string, 0, len(listings))
	for n := range listings {
		names = append(names, n)
	}
	return &catalogCache{
		downstreams: names,
		syncer:      eng,
		list: func(_ context.Context, ds string) ([]registry.ToolInfo, error) {
			return listings[ds], nil
		},
	}
}

func TestGatewaySessionListToolsFiltersAndPaginates(t *testing.T) {
	eng := policy.New(false, []policy.Rule{{Client: "c", Allow: []string{"github__a", "github__b"}}})
	cc := fixedCatalog(t, eng, map[string][]registry.ToolInfo{
		"github": {{Name: "a"}, {Name: "b"}},
		"search": {{Name: "c"}}, // denied for client c
	})
	s := &gatewaySession{
		identity: domain.Identity{ID: "c"},
		filter:   eng,
		catalog:  cc,
		known:    map[string]bool{"github": true, "search": true},
		pageSize: 1,
		closeFn:  func() error { return nil },
	}

	page1, next1, err := s.ListTools(context.Background(), "")
	if err != nil {
		t.Fatalf("page 1: %v", err)
	}
	if len(page1.Tools) != 1 || page1.Tools[0].Ref.Namespaced() != "github__a" {
		t.Fatalf("page 1 = %+v", page1.Tools)
	}
	if next1 == "" {
		t.Fatal("expected a cursor for the next page")
	}

	page2, next2, err := s.ListTools(context.Background(), next1)
	if err != nil {
		t.Fatalf("page 2: %v", err)
	}
	if len(page2.Tools) != 1 || page2.Tools[0].Ref.Namespaced() != "github__b" {
		t.Fatalf("page 2 = %+v", page2.Tools)
	}
	if next2 != "" {
		t.Errorf("expected the last page (search__c is filtered out), got cursor %q", next2)
	}
}

func TestGatewaySessionListToolsPropagatesCatalogError(t *testing.T) {
	eng := policy.New(false, nil)
	cc := &catalogCache{
		downstreams: []string{"github"},
		syncer:      eng,
		list: func(context.Context, string) ([]registry.ToolInfo, error) {
			return nil, errors.New("downstream unavailable")
		},
	}
	s := &gatewaySession{
		identity: domain.Identity{ID: "c"},
		filter:   eng,
		catalog:  cc,
		known:    map[string]bool{"github": true},
		pageSize: 10,
		closeFn:  func() error { return nil },
	}

	if _, _, err := s.ListTools(context.Background(), ""); err == nil {
		t.Fatal("expected the catalog build error to propagate to the client")
	}
}

func TestGatewaySessionCallResolvesAndRunsChain(t *testing.T) {
	var got *domain.Call
	s := &gatewaySession{
		identity: domain.Identity{ID: "claude-desktop"},
		known:    map[string]bool{"github": true},
		chain: func(_ context.Context, c *domain.Call) (*domain.Result, error) {
			got = c
			return &domain.Result{Content: []byte("{}")}, nil
		},
		closeFn: func() error { return nil },
	}

	res, err := s.Call(context.Background(), "github__create_issue", json.RawMessage(`{"x":1}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res == nil {
		t.Fatal("expected a result")
	}
	if got == nil || got.Tool.Downstream != "github" || got.Tool.Tool != "create_issue" {
		t.Fatalf("call routed wrong: %+v", got)
	}
	if got.Client.ID != "claude-desktop" || string(got.Args) != `{"x":1}` {
		t.Errorf("call carried wrong identity/args: %+v", got)
	}
}

func TestGatewaySessionCallRejectsUnknownTool(t *testing.T) {
	ran := false
	s := &gatewaySession{
		known:   map[string]bool{"github": true},
		chain:   func(context.Context, *domain.Call) (*domain.Result, error) { ran = true; return &domain.Result{}, nil },
		closeFn: func() error { return nil },
	}

	_, err := s.Call(context.Background(), "unknown__tool", nil)
	if err == nil {
		t.Fatal("expected an error for an unknown downstream")
	}
	if ran {
		t.Error("an unresolvable tool must not reach the chain")
	}
}

func TestGatewaySessionCloseDelegates(t *testing.T) {
	closed := false
	s := &gatewaySession{closeFn: func() error { closed = true; return nil }}
	if err := s.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if !closed {
		t.Error("Close did not delegate to the underlying session")
	}
}

// --- session factory wiring ---

// fakeDownstream is a minimal registry.DownstreamSession for exercising the
// per-connection dispatcher the factory wires.
type fakeDownstream struct{}

func (fakeDownstream) Close() error { return nil }
func (fakeDownstream) CallTool(_ context.Context, _ string, _ json.RawMessage) (*domain.Result, error) {
	return &domain.Result{Content: []byte("{}")}, nil
}
func (fakeDownstream) ListTools(context.Context, string) ([]registry.ToolInfo, string, error) {
	return nil, "", nil
}
func (fakeDownstream) Ping(context.Context) error { return nil }

type fakeFactory struct{}

func (fakeFactory) New(context.Context) (registry.Session, error) { return fakeDownstream{}, nil }

func TestNewSessionFactoryBuildsUsableSession(t *testing.T) {
	pool := registry.NewPool(fakeFactory{}, registry.PoolConfig{Max: 2, AcquireTimeout: time.Second, IdleTTL: time.Minute})
	mgr, err := registry.NewManager([]registry.ManagedDownstream{{Name: "github", Mode: registry.PerClient, Pool: pool}})
	if err != nil {
		t.Fatalf("manager: %v", err)
	}
	t.Cleanup(func() { _ = mgr.Close() })

	eng := policy.New(false, []policy.Rule{{Client: "c", Allow: []string{"github__create_issue"}}})
	fw, fm := &fakeWriter{}, &fakeMeter{}
	deps := sessionDeps{
		manager: mgr,
		stages: &stageDeps{
			decider: eng, redactor: mustRedactorNoErr(nil, nil), breakers: resilience.New(resilience.Config{}),
			writer: fw, meter: fm, maxBytes: 1 << 20, timeout: time.Second,
			now: func() time.Time { return time.Time{} },
		},
		filter:   eng,
		catalog:  fixedCatalog(t, eng, map[string][]registry.ToolInfo{"github": {{Name: "create_issue"}}}),
		known:    map[string]bool{"github": true},
		pageSize: 50,
	}
	factory := newSessionFactory(deps)

	sess, err := factory(domain.Identity{ID: "c"})
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	res, err := sess.Call(context.Background(), "github__create_issue", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("call through wired session: %v", err)
	}
	if res == nil {
		t.Fatal("expected a result from the wired chain")
	}
	if err := sess.Close(); err != nil {
		t.Errorf("close: %v", err)
	}
}
