package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/JumpTechCode/portcullis/internal/config"
	"github.com/JumpTechCode/portcullis/internal/domain"
	"github.com/JumpTechCode/portcullis/internal/registry"
)

func minimalConfig() *config.Config {
	return &config.Config{
		Listen:         "127.0.0.1:0",
		AllowedOrigins: []string{"http://localhost"},
		Clients:        []config.Client{{ID: "c", APIKeyEnv: "PORTCULLIS_TEST_KEY"}},
		Downstreams: []config.Downstream{{
			Name:        "github",
			Transport:   config.TransportStdio,
			Command:     []string{"true"},
			SessionMode: config.SessionPerClient,
			Pool: config.Pool{
				Max:            4,
				AcquireTimeout: config.Duration(2 * time.Second),
				IdleTTL:        config.Duration(90 * time.Second),
			},
			Health: config.Health{Interval: config.Duration(30 * time.Second)},
		}},
		Policy:    config.Policy{Default: config.DefaultDeny},
		Redaction: config.Redaction{MaxResultBytes: 1 << 20},
		Audit: config.Audit{
			Sink:                 "stdout",
			Buffer:               100,
			HighWaterMark:        0.85,
			Overflow:             config.OverflowShed,
			Flush:                config.Flush{MaxRecords: 100, MaxInterval: config.Duration(50 * time.Millisecond)},
			FsyncInterval:        config.Duration(time.Second),
			SecurityBlockTimeout: config.Duration(250 * time.Millisecond),
		},
	}
}

func TestBuildServesMetricsAndGuardsMCP(t *testing.T) {
	t.Setenv("PORTCULLIS_TEST_KEY", "supersecret")

	g, err := Build(minimalConfig(), "test")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(func() { _ = g.Shutdown() })

	srv := httptest.NewServer(g.Handler())
	defer srv.Close()

	t.Run("metrics is served unguarded", func(t *testing.T) {
		resp, err := http.Get(srv.URL + "/metrics")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("/metrics status = %d, want 200", resp.StatusCode)
		}
	})

	t.Run("MCP endpoint rejects an unauthenticated request", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/", strings.NewReader(`{}`))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("unauthenticated status = %d, want 401", resp.StatusCode)
		}
	})

	t.Run("MCP endpoint rejects a forbidden origin", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/", strings.NewReader(`{}`))
		req.Header.Set("Origin", "http://evil.example")
		req.Header.Set("Authorization", "Bearer supersecret")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Fatalf("forbidden-origin status = %d, want 403", resp.StatusCode)
		}
	})
}

func TestBuildFailsOnMissingSecret(t *testing.T) {
	t.Setenv("PORTCULLIS_TEST_KEY", "supersecret")
	cfg := minimalConfig()
	cfg.Downstreams[0].Secrets = map[string]config.SecretRef{"GITHUB_TOKEN": {Env: "PORTCULLIS_UNSET_SECRET"}}

	if _, err := Build(cfg, "test"); err == nil {
		t.Fatal("expected Build to fail when an injected secret env var is unset")
	}
}

func TestRunShutsDownOnContextCancel(t *testing.T) {
	t.Setenv("PORTCULLIS_TEST_KEY", "supersecret")
	g, err := Build(minimalConfig(), "test")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- g.Run(ctx) }()

	// Give the listener a moment to bind, then ask for shutdown.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v, want nil on graceful shutdown", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after context cancellation")
	}
}

func TestBuildHTTPDownstreamWithFileSink(t *testing.T) {
	t.Setenv("PORTCULLIS_TEST_KEY", "supersecret")
	t.Setenv("PORTCULLIS_TEST_BEARER", "tok")
	cfg := minimalConfig()
	cfg.Downstreams = []config.Downstream{{
		Name:      "search",
		Transport: config.TransportHTTP,
		URL:       "https://mcp.example.com/mcp",
		Secrets:   map[string]config.SecretRef{"Authorization": {Env: "PORTCULLIS_TEST_BEARER"}},
	}}
	cfg.Audit.Sink = "file:" + t.TempDir() + "/audit.jsonl"

	g, err := Build(cfg, "test")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if err := g.Shutdown(); err != nil {
		t.Errorf("Shutdown: %v", err)
	}
}

func TestBuildSharedStdioDownstream(t *testing.T) {
	t.Setenv("PORTCULLIS_TEST_KEY", "supersecret")
	cfg := minimalConfig()
	cfg.Downstreams[0].SessionMode = config.SessionShared

	g, err := Build(cfg, "test")
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	t.Cleanup(func() { _ = g.Shutdown() })
}

// pagingDownstream returns its tools across two pages to exercise the lister's
// cursor loop.
type pagingDownstream struct{}

func (pagingDownstream) Close() error { return nil }
func (pagingDownstream) CallTool(context.Context, string, json.RawMessage) (*domain.Result, error) {
	return &domain.Result{Content: []byte("{}")}, nil
}
func (pagingDownstream) ListTools(_ context.Context, cursor string) ([]registry.ToolInfo, string, error) {
	if cursor == "" {
		return []registry.ToolInfo{{Name: "a"}}, "p2", nil
	}
	return []registry.ToolInfo{{Name: "b"}}, "", nil
}
func (pagingDownstream) Ping(context.Context) error { return nil }

type pagingFactory struct{}

func (pagingFactory) New(context.Context) (registry.Session, error) { return pagingDownstream{}, nil }

func TestPoolListerPagesFully(t *testing.T) {
	pool := registry.NewPool(pagingFactory{}, registry.PoolConfig{Max: 1, AcquireTimeout: time.Second, IdleTTL: time.Minute})
	t.Cleanup(func() { _ = pool.Close() })
	lister := poolLister(map[string]*registry.Pool{"github": pool})

	tools, err := lister(context.Background(), "github")
	if err != nil {
		t.Fatalf("lister: %v", err)
	}
	if len(tools) != 2 || tools[0].Name != "a" || tools[1].Name != "b" {
		t.Fatalf("listed tools = %+v, want [a b]", tools)
	}
}

func TestPoolListerUnknownDownstream(t *testing.T) {
	lister := poolLister(map[string]*registry.Pool{})
	if _, err := lister(context.Background(), "nope"); err == nil {
		t.Fatal("expected an error for an unknown downstream")
	}
}
