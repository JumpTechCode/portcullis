package app

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"reflect"
	"sort"
	"sync"
	"time"

	"github.com/JumpTechCode/portcullis/internal/audit"
	"github.com/JumpTechCode/portcullis/internal/config"
	"github.com/JumpTechCode/portcullis/internal/edge"
	"github.com/JumpTechCode/portcullis/internal/metrics"
	"github.com/JumpTechCode/portcullis/internal/policy"
	"github.com/JumpTechCode/portcullis/internal/redact"
	"github.com/JumpTechCode/portcullis/internal/registry"
	"github.com/JumpTechCode/portcullis/internal/resilience"
)

// Composition-root defaults not exposed on the V1 config surface. They are the
// policy here rather than tunables; the design pins the behavior, not a knob.
const (
	// defaultCallTimeout bounds a single downstream tool call (design §5).
	defaultCallTimeout = 30 * time.Second
	// defaultSessionTimeout closes an idle client session, which reclaims its
	// downstream sessions since the SDK exposes no client-disconnect hook
	// (ADR-0013/0014).
	defaultSessionTimeout = 5 * time.Minute
	// defaultPageSize bounds one client-facing tools/list page.
	defaultPageSize = 100
	// defaultShutdownGrace bounds draining in-flight requests on shutdown.
	defaultShutdownGrace = 15 * time.Second
	// serverName identifies the gateway to clients during the MCP handshake.
	serverName = "portcullis"
)

// Gateway is a fully wired, ready-to-run gateway. Build constructs it from a
// validated configuration; Run serves it until its context is cancelled and then
// shuts down gracefully.
type Gateway struct {
	server  *http.Server
	handler http.Handler
	manager *registry.Manager
	audit   *audit.Writer
	sink    *os.File // the audit file when the sink is a file; nil for stdout
	grace   time.Duration

	// Hot-reloadable state and the snapshot of the running topology used to detect
	// restart-required changes on reload (design §5).
	engine          *policy.Engine
	guard           *edge.Guard
	catalog         *catalogCache
	origDownstreams []config.Downstream
	origRedaction   config.Redaction
	origAudit       config.Audit

	shutdownOnce sync.Once
	shutdownErr  error
}

// Build wires the gateway from a validated configuration. It resolves
// connection-level secrets from the environment, builds a supervised, bounded
// pool per downstream and the session manager over them, constructs the security
// stages and the per-connection session factory, and assembles the client-facing
// HTTP server (the guarded MCP endpoint plus /metrics). It performs no network or
// subprocess I/O: downstream sessions open lazily on first use.
//
// The configuration is assumed already validated by the config loader; Build
// still fails fast on a connection-level secret whose environment variable is
// unset, so an injected credential is never silently empty.
func Build(cfg *config.Config, version string) (*Gateway, error) {
	met := metrics.New()

	pools, managed, secretValues, err := buildDownstreams(cfg)
	if err != nil {
		return nil, err
	}

	manager, err := registry.NewManager(managed)
	if err != nil {
		return nil, fmt.Errorf("app: building session manager: %w", err)
	}

	redactor, err := buildRedactor(cfg, secretValues)
	if err != nil {
		_ = manager.Close()
		return nil, err
	}

	auditWriter, sinkFile, err := buildAudit(&cfg.Audit)
	if err != nil {
		_ = manager.Close()
		return nil, err
	}

	engine := policy.New(cfg.Policy.Default == config.DefaultAllow, policyRules(cfg.Policy.Rules))

	stages := &stageDeps{
		decider:  engine,
		redactor: redactor,
		breakers: resilience.New(resilience.Config{}),
		writer:   auditWriter,
		meter:    met,
		maxBytes: cfg.Redaction.MaxResultBytes,
		timeout:  defaultCallTimeout,
		now:      time.Now,
	}

	catalog := &catalogCache{
		downstreams: downstreamNames(cfg),
		list:        poolLister(pools),
		syncer:      engine,
	}

	sessionFactory := newSessionFactory(sessionDeps{
		manager:  manager,
		stages:   stages,
		filter:   engine,
		catalog:  catalog,
		known:    knownDownstreams(cfg),
		pageSize: defaultPageSize,
	})

	guard, err := buildGuard(cfg)
	if err != nil {
		_ = manager.Close()
		_ = auditWriter.Close()
		closeFile(sinkFile)
		return nil, err
	}

	mcpHandler := edge.NewHandler(edge.Config{
		ServerName:     serverName,
		ServerVersion:  version,
		Guard:          guard,
		NewSession:     sessionFactory,
		SessionTimeout: defaultSessionTimeout,
	})

	mux := http.NewServeMux()
	mux.Handle("/metrics", met.Handler())
	mux.Handle("/", mcpHandler)

	return &Gateway{
		server:          &http.Server{Addr: cfg.Listen, Handler: mux, ReadHeaderTimeout: 10 * time.Second},
		handler:         mux,
		manager:         manager,
		audit:           auditWriter,
		sink:            sinkFile,
		grace:           defaultShutdownGrace,
		engine:          engine,
		guard:           guard,
		catalog:         catalog,
		origDownstreams: cfg.Downstreams,
		origRedaction:   cfg.Redaction,
		origAudit:       cfg.Audit,
	}, nil
}

// Reload applies a configuration reload (SIGHUP / file-watch). Only the client
// auth map and the policy rules are hot-reloaded — atomically, behind each
// engine's lock, so no live session or request is dropped (design §5). The
// reload is rejected as a whole if the new configuration is invalid, so a bad
// edit never takes down the running gateway. Downstream topology, redaction, and
// audit changes are restart-required; if any changed, the reload still applies
// the client + policy parts and logs that a restart is needed for the rest.
func (g *Gateway) Reload(newCfg *config.Config) error {
	if err := newCfg.Validate(); err != nil {
		return fmt.Errorf("app: rejected configuration reload: %w", err)
	}

	clients, err := resolveClientKeys(newCfg)
	if err != nil {
		return err
	}
	g.guard.Reload(newCfg.AllowedOrigins, clients)

	g.engine.Reload(newCfg.Policy.Default == config.DefaultAllow, policyRules(newCfg.Policy.Rules))
	// Re-pin wildcards against the live catalog so a reloaded wildcard rule admits
	// the tools currently observed; if the catalog has not been built yet, the
	// first build will sync it.
	if cat, ok := g.catalog.current(); ok {
		g.engine.Sync(cat)
	}

	if g.restartRequired(newCfg) {
		log.Printf("portcullis: reload applied client + policy; downstream/redaction/audit changes require a restart")
	}
	return nil
}

// restartRequired reports whether newCfg changes anything outside the hot-reload
// scope (downstream topology, redaction, or audit) relative to the running
// configuration, which a reload cannot apply without rebuilding sessions.
func (g *Gateway) restartRequired(newCfg *config.Config) bool {
	return !reflect.DeepEqual(g.origDownstreams, newCfg.Downstreams) ||
		!reflect.DeepEqual(g.origRedaction, newCfg.Redaction) ||
		!reflect.DeepEqual(g.origAudit, newCfg.Audit)
}

// Handler returns the gateway's root HTTP handler (the guarded MCP endpoint plus
// /metrics). It is exposed so the gateway can be exercised over httptest without
// binding a real listener.
func (g *Gateway) Handler() http.Handler { return g.handler }

// Run serves the gateway until ctx is cancelled, then shuts down gracefully. It
// returns a non-nil error only if the server fails to start; a clean shutdown
// returns nil.
func (g *Gateway) Run(ctx context.Context) error {
	serveErr := make(chan error, 1)
	go func() {
		err := g.server.ListenAndServe()
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serveErr <- err
	}()

	select {
	case err := <-serveErr:
		// The listener failed before any shutdown was requested; still release the
		// other resources so we do not leak the pools or the audit writer.
		_ = g.Shutdown()
		return err
	case <-ctx.Done():
		return g.Shutdown()
	}
}

// Shutdown stops accepting requests, drains in-flight calls within the grace
// window, then tears down in the order the lifecycle requires (design §5): stop
// the HTTP server first so no new call enters, then close the downstream pools,
// then drain and fsync the audit writer last, after every producer has stopped.
// It returns the first error encountered and is safe to call more than once.
func (g *Gateway) Shutdown() error {
	g.shutdownOnce.Do(func() {
		sctx, cancel := context.WithTimeout(context.Background(), g.grace)
		defer cancel()

		record := func(err error) {
			if err != nil && g.shutdownErr == nil {
				g.shutdownErr = err
			}
		}

		record(g.server.Shutdown(sctx))
		record(g.manager.Close())
		record(g.audit.Close())
		if g.sink != nil {
			record(g.sink.Close())
		}
	})
	return g.shutdownErr
}

// buildDownstreams resolves each downstream's secrets and builds its supervised,
// bounded pool, returning the pools by name, the manager registrations, and every
// resolved secret value (for the outbound redactor).
func buildDownstreams(cfg *config.Config) (pools map[string]*registry.Pool, managed []registry.ManagedDownstream, secretValues []string, err error) {
	pools = make(map[string]*registry.Pool, len(cfg.Downstreams))
	managed = make([]registry.ManagedDownstream, 0, len(cfg.Downstreams))

	for i := range cfg.Downstreams {
		d := &cfg.Downstreams[i]
		resolved, rerr := resolveSecrets(d)
		if rerr != nil {
			return nil, nil, nil, rerr
		}
		for _, v := range resolved {
			secretValues = append(secretValues, v)
		}

		factory, ferr := transportFactory(d, resolved)
		if ferr != nil {
			return nil, nil, nil, ferr
		}
		supervised := registry.NewSupervisedFactory(factory, registry.SupervisorConfig{})
		pool := registry.NewPool(supervised, poolConfig(d))
		pools[d.Name] = pool
		managed = append(managed, registry.ManagedDownstream{
			Name: d.Name,
			Mode: sessionMode(d),
			Pool: pool,
		})
	}
	return pools, managed, secretValues, nil
}

// resolveSecrets resolves a downstream's connection-level secrets from the
// environment, keyed by their destination (an env-var name for stdio, a header
// name for http). It fails fast on an unset variable so an injected credential is
// never silently empty.
func resolveSecrets(d *config.Downstream) (map[string]string, error) {
	if len(d.Secrets) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(d.Secrets))
	for key, ref := range d.Secrets {
		value := os.Getenv(ref.Env)
		if value == "" {
			return nil, fmt.Errorf("app: downstream %q secret %q: env var %q is not set", d.Name, key, ref.Env)
		}
		out[key] = value
	}
	return out, nil
}

// transportFactory builds the session factory for a downstream's transport,
// injecting the resolved secrets where the transport carries them: stdio places
// them in the child environment, http in the request headers.
func transportFactory(d *config.Downstream, secrets map[string]string) (registry.Factory, error) {
	switch d.Transport {
	case config.TransportStdio:
		return registry.NewStdioFactory(registry.StdioConfig{
			Command: d.Command,
			Env:     secrets,
		}), nil
	case config.TransportHTTP:
		return registry.NewHTTPFactory(registry.HTTPConfig{
			URL:     d.URL,
			Headers: secrets,
		}), nil
	default:
		return nil, fmt.Errorf("app: downstream %q: unsupported transport %q", d.Name, d.Transport)
	}
}

// poolConfig derives a pool's bounds. stdio downstreams carry explicit pool
// config; http downstreams have none, so the pool falls back to its defaults.
func poolConfig(d *config.Downstream) registry.PoolConfig {
	if d.Transport == config.TransportStdio {
		return registry.PoolConfig{
			Max:            d.Pool.Max,
			AcquireTimeout: d.Pool.AcquireTimeout.Duration(),
			IdleTTL:        d.Pool.IdleTTL.Duration(),
		}
	}
	return registry.PoolConfig{}
}

// sessionMode maps the configured session sharing to the registry's mode. http
// downstreams have no session_mode and are treated as per_client (one session per
// client connection), matching the design's remote-session model.
func sessionMode(d *config.Downstream) registry.SessionMode {
	if d.Transport == config.TransportStdio && d.SessionMode == config.SessionShared {
		return registry.Shared
	}
	return registry.PerClient
}

// buildRedactor builds the outbound redactor from every resolved connection-level
// secret value (so a downstream that echoes an injected credential is scrubbed)
// and the configured PII patterns.
func buildRedactor(cfg *config.Config, secretValues []string) (*redact.Redactor, error) {
	patterns := make([]redact.Pattern, len(cfg.Redaction.Patterns))
	for i, p := range cfg.Redaction.Patterns {
		patterns[i] = redact.Pattern{Name: p.Name, Regex: p.Regex, Anchor: p.Anchor}
	}
	redactor, err := redact.New(secretValues, patterns)
	if err != nil {
		return nil, fmt.Errorf("app: building redactor: %w", err)
	}
	return redactor, nil
}

// buildAudit opens the configured sink and starts the audit writer. A "stdout"
// sink is wrapped fsync-free; a "file:<path>" sink is opened append-only and
// returned so the caller can close it after the writer drains.
func buildAudit(cfg *config.Audit) (*audit.Writer, *os.File, error) {
	var sink audit.SyncWriter
	var file *os.File
	if path, ok := filePath(cfg.Sink); ok {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // G304: operator-supplied audit path, not attacker-controlled.
		if err != nil {
			return nil, nil, fmt.Errorf("app: opening audit sink %q: %w", path, err)
		}
		sink, file = f, f
	} else {
		sink = audit.NopSync(os.Stdout)
	}

	writer := audit.New(sink, audit.Config{
		Buffer:               cfg.Buffer,
		HighWaterMark:        cfg.HighWaterMark,
		FlushMaxRecords:      cfg.Flush.MaxRecords,
		FlushMaxInterval:     cfg.Flush.MaxInterval.Duration(),
		FsyncInterval:        cfg.FsyncInterval.Duration(),
		Overflow:             audit.OverflowPolicy(cfg.Overflow),
		SecurityBlockTimeout: cfg.SecurityBlockTimeout.Duration(),
	})
	return writer, file, nil
}

// buildGuard resolves each client's API key from the environment and builds the
// edge request guard from the resolved keys and the configured allowed origins.
func buildGuard(cfg *config.Config) (*edge.Guard, error) {
	clients, err := resolveClientKeys(cfg)
	if err != nil {
		return nil, err
	}
	return edge.NewGuard(cfg.AllowedOrigins, clients), nil
}

// resolveClientKeys resolves each configured client's API key from the
// environment, failing fast on an unset variable. It backs both the initial
// guard build and a reload.
func resolveClientKeys(cfg *config.Config) ([]edge.ClientKey, error) {
	clients := make([]edge.ClientKey, 0, len(cfg.Clients))
	for _, c := range cfg.Clients {
		key := os.Getenv(c.APIKeyEnv)
		if key == "" {
			return nil, fmt.Errorf("app: client %q: api key env var %q is not set", c.ID, c.APIKeyEnv)
		}
		clients = append(clients, edge.ClientKey{ID: c.ID, Key: key})
	}
	return clients, nil
}

// poolLister returns the gateway-wide tool lister the catalog cache uses: it
// acquires a transient session to a downstream from its pool, pages the full tool
// list, and returns the session to the pool. A session that errors mid-listing is
// discarded rather than reused.
func poolLister(pools map[string]*registry.Pool) toolLister {
	return func(ctx context.Context, downstream string) ([]registry.ToolInfo, error) {
		pool, ok := pools[downstream]
		if !ok {
			return nil, fmt.Errorf("app: no pool for downstream %q", downstream)
		}
		sess, err := pool.Acquire(ctx)
		if err != nil {
			return nil, err
		}
		ds, ok := sess.(registry.DownstreamSession)
		if !ok {
			pool.Discard(sess)
			return nil, fmt.Errorf("app: downstream %q session is not listable", downstream)
		}

		var all []registry.ToolInfo
		cursor := ""
		for {
			tools, next, err := ds.ListTools(ctx, cursor)
			if err != nil {
				pool.Discard(sess)
				return nil, err
			}
			all = append(all, tools...)
			if next == "" {
				break
			}
			cursor = next
		}
		pool.Release(sess)
		return all, nil
	}
}

// policyRules converts configured policy rules into the policy engine's form.
func policyRules(rules []config.Rule) []policy.Rule {
	out := make([]policy.Rule, len(rules))
	for i, r := range rules {
		out[i] = policy.Rule{Client: r.Client, Allow: r.Allow}
	}
	return out
}

// downstreamNames returns the configured downstream names in a stable order, so
// the gateway-wide catalog builds deterministically.
func downstreamNames(cfg *config.Config) []string {
	names := make([]string, len(cfg.Downstreams))
	for i := range cfg.Downstreams {
		names[i] = cfg.Downstreams[i].Name
	}
	sort.Strings(names)
	return names
}

// knownDownstreams is the set of configured downstream names, used to resolve a
// namespaced call to a known downstream before dispatch.
func knownDownstreams(cfg *config.Config) map[string]bool {
	known := make(map[string]bool, len(cfg.Downstreams))
	for i := range cfg.Downstreams {
		known[cfg.Downstreams[i].Name] = true
	}
	return known
}

// filePath returns the path of a "file:<path>" sink, or false for any other
// sink (such as "stdout").
func filePath(sink string) (string, bool) {
	const prefix = "file:"
	if len(sink) > len(prefix) && sink[:len(prefix)] == prefix {
		return sink[len(prefix):], true
	}
	return "", false
}

// closeFile closes f when it is non-nil, used on Build's error paths.
func closeFile(f *os.File) {
	if f != nil {
		_ = f.Close()
	}
}

// Compile-time assertion that the metrics surface satisfies the stages' meter.
var _ fullMeter = (*metrics.Metrics)(nil)

// Compile-time assertion that the audit writer satisfies the stages' writer.
var _ auditWriter = (*audit.Writer)(nil)

// Compile-time assertion that the manager satisfies the session factory's needs.
var _ clientSessionFactory = (*registry.Manager)(nil)
