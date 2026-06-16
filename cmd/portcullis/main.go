// Command portcullis runs the Portcullis gateway: a single MCP endpoint that
// authenticates clients, applies policy, and proxies allowed tool calls to the
// downstream MCP servers each client is permitted to use.
package main

import (
	"flag"
	"fmt"
)

// version is the gateway version, stamped at release time via
// -ldflags "-X main.version=<tag>". It defaults to "dev" for local builds.
var version = "dev"

func main() {
	showVersion := flag.Bool("version", false, "print the gateway version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	// The server bootstrap is assembled in internal/app and wired here in a
	// later change; for now the binary reports its usage.
	flag.Usage()
}
