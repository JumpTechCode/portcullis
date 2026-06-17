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

	"github.com/JumpTechCode/portcullis/internal/app"
	"github.com/JumpTechCode/portcullis/internal/config"
)

// version is the gateway version, stamped at release time via
// -ldflags "-X main.version=<tag>". It defaults to "dev" for local builds.
var version = "dev"

func main() {
	showVersion := flag.Bool("version", false, "print the gateway version and exit")
	configPath := flag.String("config", "portcullis.yaml", "path to the gateway configuration file")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	if err := run(*configPath); err != nil {
		fmt.Fprintf(os.Stderr, "portcullis: %v\n", err)
		os.Exit(1)
	}
}

// run loads the configuration, builds the gateway, and serves it until an
// interrupt or termination signal arrives, then shuts down gracefully. It is
// separated from main so the failure path returns an error rather than exiting,
// keeping main a thin shell.
func run(configPath string) error {
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

	// SIGHUP hot-reloads the client + policy maps from the same config file,
	// atomically and without dropping connections; topology changes are
	// restart-required (design §5). A failed or invalid reload is logged and the
	// running configuration stands.
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)
	go func() {
		for range hup {
			reload(gateway, configPath)
		}
	}()

	return gateway.Run(ctx)
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
