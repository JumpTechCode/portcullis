// Command portcullis runs the Portcullis gateway: a single MCP endpoint that
// authenticates clients, applies policy, and proxies allowed tool calls to the
// downstream MCP servers each client is permitted to use.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/JumpTechCode/portcullis/internal/app"
	"github.com/JumpTechCode/portcullis/internal/config"
	"github.com/JumpTechCode/portcullis/internal/watch"
)

// version is the gateway version, stamped at release time via
// -ldflags "-X main.version=<tag>". It defaults to "dev" for local builds.
var version = "dev"

func main() {
	showVersion := flag.Bool("version", false, "print the gateway version and exit")
	configPath := flag.String("config", "portcullis.yaml", "path to the gateway configuration file")
	watchFiles := flag.Bool("watch", false, "reload automatically when the config file changes")
	watchInterval := flag.Duration("watch-interval", time.Second, "how often to poll the config file when --watch is set")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	if err := run(*configPath, *watchFiles, *watchInterval); err != nil {
		fmt.Fprintf(os.Stderr, "portcullis: %v\n", err)
		os.Exit(1)
	}
}

// run loads the configuration, builds the gateway, and serves it until an
// interrupt or termination signal arrives, then shuts down gracefully. SIGHUP
// and, when enabled, a config file-watcher both feed a single reload consumer
// so reloads are serialized and never overlap. It is separated from main so the
// failure path returns an error rather than exiting, keeping main a thin shell.
func run(configPath string, watchFiles bool, watchInterval time.Duration) error {
	if watchFiles && watchInterval <= 0 {
		return fmt.Errorf("--watch-interval must be positive, got %s", watchInterval)
	}

	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}

	gateway, err := app.Build(cfg, version)
	if err != nil {
		return err
	}

	// SIGINT/SIGTERM cancel the context, which Run treats as a graceful-shutdown
	// signal (stop accepting, drain in-flight, close sessions, flush audit).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// A single consumer applies reloads one at a time. Both SIGHUP and the
	// optional file-watcher enqueue through trigger; a buffered(1) channel plus a
	// non-blocking send coalesces a burst into at most one pending reload (the
	// reload re-reads the latest file, so a dropped duplicate is harmless).
	reloads := make(chan struct{}, 1)
	trigger := newReloadTrigger(reloads)
	go serveReloads(ctx, reloads, func() { reload(gateway, configPath) })

	// SIGHUP hot-reloads the client + policy maps from the same config file;
	// topology/redaction/audit changes are restart-required (design §5).
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)
	go func() {
		for range hup {
			trigger()
		}
	}()

	if watchFiles {
		log.Printf("portcullis: watching %s for changes (interval %s)", configPath, watchInterval)
		go watch.New(configPath, watchInterval, trigger).Run(ctx)
	}

	return gateway.Run(ctx)
}

// newReloadTrigger returns a function that enqueues a reload without blocking.
// If a reload is already pending in the buffered(1) channel, the extra request
// is dropped — coalescing a burst of triggers into a single reload.
func newReloadTrigger(reloads chan<- struct{}) func() {
	return func() {
		select {
		case reloads <- struct{}{}:
		default:
		}
	}
}

// serveReloads applies queued reloads one at a time by calling do, until ctx is
// cancelled. Running every reload on this single goroutine serializes them, so
// two reloads can never interleave their config swaps.
func serveReloads(ctx context.Context, reloads <-chan struct{}, do func()) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-reloads:
			do()
		}
	}
}

// reload re-reads the configuration file and applies a hot reload, logging the
// outcome. A read or validation failure leaves the running configuration in
// place rather than taking the gateway down.
func reload(gateway *app.Gateway, configPath string) {
	newCfg, err := config.Load(configPath)
	if err != nil {
		log.Printf("portcullis: reload failed: %v", err)
		return
	}
	if err := gateway.Reload(newCfg); err != nil {
		log.Printf("portcullis: reload rejected: %v", err)
		return
	}
	log.Print("portcullis: configuration reloaded")
}
