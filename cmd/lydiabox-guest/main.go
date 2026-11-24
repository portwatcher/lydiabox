// Command lydiabox-guest is the static guest daemon injected into the VM at
// /usr/local/bin/lydiabox-guest. Command logic lives in internal/guest/cli.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/portwatcher/lydiabox/internal/guest/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cli.Main(ctx, os.Args[0], os.Args[1:], cli.Env{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr})
	stop()
	os.Exit(code)
}
