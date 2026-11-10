package cli

import (
	"context"
	"fmt"
)

// The git credential helper is implemented in Phase 4. Git calls it as
// `lydiabox-guest git-cred <get|store|erase>` (credential.helper) or through
// the git-credential-lydiabox symlink.
func init() {
	Register(Command{
		Name:    "git-cred",
		Summary: "Git credential helper backed by the host connector broker",
		Aliases: []string{"git-credential-lydiabox"},
		Run: func(context.Context, Env, []string) error {
			return fmt.Errorf("git-cred: %w", ErrNotImplemented)
		},
	})
}
