package cli

import (
	"context"
	"fmt"
)

// The guest daemon is implemented in Phase 2. This build reports that the
// command is unavailable instead of starting a partial daemon.
func init() {
	Register(Command{
		Name:    "serve",
		Summary: "Run the guest daemon (PTY, desktop, MCP, and credential bridges)",
		Run: func(context.Context, Env, []string) error {
			return fmt.Errorf("serve: %w", ErrNotImplemented)
		},
	})
}
