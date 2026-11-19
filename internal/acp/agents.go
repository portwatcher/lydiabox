package acp

import (
	"fmt"
	"os/exec"

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

// lookPath finds a program on PATH (exec.LookPath; tests replace it).
var lookPath = exec.LookPath

// installed returns {env: path of program} when the program is on PATH.
func installed(env, program string) map[string]string {
	p, err := lookPath(program)
	if err != nil {
		return map[string]string{}
	}
	return map[string]string{env: p}
}

// AgentFor returns the ACP agent of a harness (docs/harness-formats.md,
// "ACP agents"). The model is used where the agent reads it from the
// environment; otherwise the bridge sets it as a session config option.
//
// The Claude and Codex agents run the installed claude and codex
// (CLAUDE_CODE_EXECUTABLE, CODEX_PATH): the image removes the copies that
// their npm packages bundle, and the sessions stay the ones of the TUIs.
func AgentFor(h, model string) (Agent, error) {
	switch h {
	case harness.ClaudeCode:
		// @agentclientprotocol/claude-agent-acp; bypassPermissions is offered
		// to a user other than root.
		a := Agent{Command: []string{"claude-agent-acp"}, FullAccess: []string{"bypassPermissions"},
			Env: installed("CLAUDE_CODE_EXECUTABLE", "claude")}
		if model != "" {
			a.Env["ANTHROPIC_MODEL"] = model
		}
		return a, nil
	case harness.Codex:
		// @agentclientprotocol/codex-acp drives `codex app-server`, so its
		// sessions are the same rollouts as the TUI's.
		return Agent{Command: []string{"codex-acp"}, FullAccess: []string{"agent-full-access", "full-access"},
			Env: installed("CODEX_PATH", "codex")}, nil
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
		// environment and overlay as the terminal launch: the overlay sets
		// the model of the acp row, which dsh uses for session/new.
		return Agent{Command: adapters.DSHCommand(model, "acp")}, nil
	}
	return Agent{}, fmt.Errorf("%w: %s has no ACP agent", harness.ErrBadRequest, h)
}
