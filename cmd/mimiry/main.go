// Command mimiry is the Mimiry command line, and — as `mimiry rpc` — the
// machine interface the Python package calls. Both are thin over the library.
package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/mimiry-com/mimiry-sdk/internal/cli"
	"github.com/mimiry-com/mimiry-sdk/internal/rpc"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if len(os.Args) > 1 && os.Args[1] == "rpc" {
		os.Exit(rpc.Serve(ctx, os.Stdin, os.Stdout, rpc.DefaultClientFactory))
	}
	os.Exit(cli.Run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
