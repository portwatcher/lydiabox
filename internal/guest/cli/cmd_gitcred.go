package cli

import (
	"context"
	"os"

	"github.com/portwatcher/lydiabox/internal/guest/gitcred"
)

// The git credential helper. Git calls it as
// `lydiabox-guest git-cred <get|store|erase>` (credential.helper) or through
// the git-credential-lydiabox symlink; `git-cred install` sets
// credential.helper in the guest's global git config.
func init() {
	Register(Command{
		Name:    "git-cred",
		Summary: "Git credential helper backed by the host connector broker",
		Aliases: []string{"git-credential-lydiabox"},
		Run: func(ctx context.Context, env Env, args []string) error {
			return gitcred.Run(ctx, args, gitcred.Options{
				HostAddr: os.Getenv(gitcred.HostAddrEnv),
				Stdin:    env.Stdin, Stdout: env.Stdout, Stderr: env.Stderr,
			})
		},
	})
}
