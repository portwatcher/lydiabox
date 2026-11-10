// Command lydiabox is the self-hosted LydiaBox control plane and CLI.
// Command logic lives in internal/cli.
package main

import (
	"context"
	"os"

	"github.com/lydiabox/lydiabox/internal/cli"
)

func main() {
	os.Exit(cli.Main(context.Background(), os.Args[1:], cli.DefaultEnv()))
}
