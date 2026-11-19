// Package cli implements the lydiabox-guest command dispatch. Each command
// lives in its own cmd_<name>.go file and registers itself in init, so the
// phases that implement guest features edit only their own file:
//
//	cmd_serve.go    Phase 2: guest daemon (rpc, ptymanager, x11, webrtc, mcp, credproxy)
//	cmd_gitcred.go  Phase 4: git credential helper over vsock
//	cmd_lbx.go      browser and computer use from the shell (lbx)
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"github.com/lydiabox/lydiabox/internal/version"
)

// ErrNotImplemented is returned by commands that this build does not provide.
var ErrNotImplemented = errors.New("not implemented in this build")

// Env carries the process streams.
type Env struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

// Command is one lydiabox-guest subcommand.
type Command struct {
	Name    string
	Summary string
	// Aliases are executable base names that select this command when the
	// binary is invoked through a symlink (for example git-credential-lydiabox).
	Aliases []string
	Run     func(ctx context.Context, env Env, args []string) error
}

var registry = map[string]Command{}

// Register adds a command; it panics on duplicate names or aliases.
func Register(c Command) {
	for _, n := range append([]string{c.Name}, c.Aliases...) {
		if _, dup := registry[n]; dup {
			panic("guest cli: duplicate command " + n)
		}
		registry[n] = c
	}
}

func init() {
	Register(Command{Name: "version", Summary: "Print version information", Run: func(_ context.Context, env Env, _ []string) error {
		_, err := fmt.Fprintf(env.Stdout, "lydiabox-guest %s\n", version.Get())
		return err
	}})
}

// Main dispatches on the executable name (symlink aliases) or the first
// argument and returns the process exit code.
func Main(ctx context.Context, argv0 string, args []string, env Env) int {
	if c, ok := registry[filepath.Base(argv0)]; ok && filepath.Base(argv0) != "lydiabox-guest" {
		return run(ctx, c, args, env)
	}
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		usage(env.Stdout)
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	if args[0] == "--version" || args[0] == "-v" {
		args = []string{"version"}
	}
	c, ok := registry[args[0]]
	if !ok {
		fmt.Fprintf(env.Stderr, "lydiabox-guest: unknown command %q\n\n", args[0])
		usage(env.Stderr)
		return 2
	}
	return run(ctx, c, args[1:], env)
}

func run(ctx context.Context, c Command, args []string, env Env) int {
	if err := c.Run(ctx, env, args); err != nil {
		if !errors.Is(err, errReported) {
			fmt.Fprintf(env.Stderr, "lydiabox-guest %s: %v\n", c.Name, err)
		}
		return 1
	}
	return 0
}

func usage(w io.Writer) {
	fmt.Fprintln(w, "Usage: lydiabox-guest <command> [args]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Commands:")
	seen := map[string]bool{}
	var names []string
	for _, c := range registry {
		if !seen[c.Name] {
			seen[c.Name] = true
			names = append(names, c.Name)
		}
	}
	sort.Strings(names)
	for _, n := range names {
		c := registry[n]
		line := fmt.Sprintf("  %-9s %s", c.Name, c.Summary)
		if len(c.Aliases) > 0 {
			line += " (also as " + strings.Join(c.Aliases, ", ") + ")"
		}
		fmt.Fprintln(w, line)
	}
}
