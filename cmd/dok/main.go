// Command dok reads and writes Confluence Cloud pages from the terminal or
// as an MCP server, limited to allowed spaces, with confirmed writes.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/nyactl/dok-for-confluence/internal/cli"
)

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := cli.Default(version).Execute(ctx, os.Args[1:]); err != nil {
		os.Exit(1)
	}
}
