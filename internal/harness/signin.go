package harness

import (
	"fmt"
	"strings"
)

// Guest-native mode (docs/design.md section 4.4.2): each harness signs in
// itself inside the VM, with its own sign-in flow, and keeps the tokens in
// the home directory of the VM. The host only starts a terminal that runs
// the sign-in command; it does not see or keep the tokens.

// SignInCommands are the native sign-in commands. Codex uses the device
// code flow, because the browser of the user is not in the VM. agy has no
// separate sign-in command: its first start asks for the sign-in method.
// dsh has no subscription: its API keys are in ~/.dsh/.env.
var SignInCommands = map[string]string{
	ClaudeCode:  "claude auth login",
	Codex:       "codex login --device-auth",
	OpenCode:    "opencode auth login",
	Antigravity: "agy",
	DSH:         `echo "Add your API key to ~/.dsh/.env, for example DEEPSEEK_API_KEY=..."; ${EDITOR:-vi} ~/.dsh/.env`,
}

// signInCommand returns the shell command line that runs the sign-in of
// harness and then stays in a login shell, so that the user sees the
// result.
func (m *Manager) signInCommand(harness string) ([]string, error) {
	c, ok := SignInCommands[harness]
	if !ok {
		return nil, fmt.Errorf("%w: harness %q has no sign-in", ErrBadRequest, harness)
	}
	if !m.opts.GuestNative {
		return nil, fmt.Errorf("%w: the harnesses sign in on the Models page (the host gateway); the VM sign-in is for guest-native mode", ErrConflict)
	}
	return []string{"bash", "-lc", strings.TrimSpace(c) + "; exec bash -l"}, nil
}
