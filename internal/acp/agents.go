package acp

import (
	"fmt"

	"github.com/lydiabox/lydiabox/internal/harness"
	"github.com/lydiabox/lydiabox/internal/harness/adapters"
)

// Agent describes the ACP agent of one harness.
type Agent struct {
	// Command is the argv of the agent process.
	Command []string
	// Env is added to the environment of the agent.
	Env map[string]string
	// FullAccess lists the session modes that run without approvals, in
	// order of preference; the bridge selects the first one the agent has.
	FullAccess []string
}

// Versions of the ACP adapters that deploy/rootfs/install-harnesses.sh
// installs; the other three harnesses have ACP in their own CLI.
const (
	ClaudeAgentACPVersion = "0.108.1"
	CodexACPVersion       = "2.7.0"
)

// AgentFor returns the ACP agent of a harness (docs/harness-formats.md,
// "ACP agents"). The model is used where the agent reads it from the
// environment; otherwise the bridge sets it as a session config option.
func AgentFor(h, model string) (Agent, error) {
	switch h {
	case harness.ClaudeCode:
		// @agentclientprotocol/claude-agent-acp; bypassPermissions is offered
		// to a user other than root.
		a := Agent{Command: []string{"claude-agent-acp"}, FullAccess: []string{"bypassPermissions"}}
		if model != "" {
			a.Env = map[string]string{"ANTHROPIC_MODEL": model}
		}
		return a, nil
	case harness.Codex:
		// @agentclientprotocol/codex-acp drives the installed `codex
		// app-server`, so its sessions are the same rollouts as the TUI's.
		return Agent{Command: []string{"codex-acp"}, FullAccess: []string{"agent-full-access", "full-access"}}, nil
	case harness.OpenCode:
		// opencode acp has no permission flag; the inline config allows
		// every tool, as `opencode --auto` does in the terminal.
		return Agent{Command: []string{"opencode", "acp"},
			Env: map[string]string{"OPENCODE_CONFIG_CONTENT": `{"permission":{"edit":"allow","bash":"allow","webfetch":"allow","external_directory":"allow","doom_loop":"allow"}}`},
		}, nil
	case harness.Antigravity:
		return Agent{Command: []string{"agy", "acp", "--dangerously-skip-permissions"}}, nil
	case harness.DSH:
		// dsh acp (the @deepseek-ai/dsh-acp-app profile) with the same
		// environment as the terminal launch (DSH_PERMISSION_MODE, DSH_MODEL,
		// ~/.dsh/lydiabox.env).
		return Agent{Command: adapters.DSHCommand(model, "acp")}, nil
	}
	return Agent{}, fmt.Errorf("%w: %s has no ACP agent", harness.ErrBadRequest, h)
}
