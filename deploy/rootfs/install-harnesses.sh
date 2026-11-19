#!/bin/sh
# Install the five harness CLIs into the guest image (docs/design.md 4.5.1).
#
# npm packages (override with build arguments of the same name):
#   CLAUDE_CODE_PACKAGE  @anthropic-ai/claude-code
#   CODEX_PACKAGE        @openai/codex
#   OPENCODE_PACKAGE     opencode-ai
# ACP agents for the chat view (internal/acp; pinned, override likewise):
#   CLAUDE_ACP_PACKAGE   @agentclientprotocol/claude-agent-acp@0.108.1 (claude-agent-acp)
#   CODEX_ACP_PACKAGE    @agentclientprotocol/codex-acp@2.7.0 (codex-acp; drives the
#                        installed `codex app-server`)
# opencode (`opencode acp`), dsh (`dsh acp`), and agy (`agy acp`) have ACP in
# their own CLI.
# dsh and antigravity (agy) use shell installers; override them with build
# arguments of the same name (an empty value skips that harness with a
# warning):
#   DSH_INSTALL          npm install -g @deepseek-ai/dsh (official npm package of
#                        github.com/deepseek-ai/deepseek-harness; Node.js >= 24.9)
#   ANTIGRAVITY_INSTALL  the official installer https://antigravity.google/cli/install.sh
#                        (downloads the native agy binary and checks its SHA512)
set -eu

: "${CLAUDE_CODE_PACKAGE:=@anthropic-ai/claude-code}"
: "${CODEX_PACKAGE:=@openai/codex}"
: "${OPENCODE_PACKAGE:=opencode-ai}"
: "${CLAUDE_ACP_PACKAGE:=@agentclientprotocol/claude-agent-acp@0.108.1}"
: "${CODEX_ACP_PACKAGE:=@agentclientprotocol/codex-acp@2.7.0}"
: "${DSH_INSTALL=npm install -g --no-fund --no-audit @deepseek-ai/dsh}"
: "${ANTIGRAVITY_INSTALL=curl -fsSL https://antigravity.google/cli/install.sh | bash -s -- --dir /usr/local/bin}"

npm install -g --no-fund --no-audit "$CLAUDE_CODE_PACKAGE" "$CODEX_PACKAGE" "$OPENCODE_PACKAGE" \
	"$CLAUDE_ACP_PACKAGE" "$CODEX_ACP_PACKAGE"
command -v claude >/dev/null
command -v codex >/dev/null
command -v opencode >/dev/null
command -v claude-agent-acp >/dev/null
command -v codex-acp >/dev/null

run_installer() {
	name=$1
	cmd=$2
	if [ -z "$cmd" ]; then
		echo "install-harnesses: WARNING: no installer for $name; set the build argument to include it" >&2
		return 0
	fi
	echo "install-harnesses: installing $name"
	sh -eu -c "$cmd"
}

run_installer dsh "$DSH_INSTALL"
run_installer antigravity "$ANTIGRAVITY_INSTALL"
if [ -n "$DSH_INSTALL" ]; then
	command -v dsh >/dev/null || { echo "install-harnesses: dsh is not on PATH after DSH_INSTALL" >&2; exit 1; }
fi
if [ -n "$ANTIGRAVITY_INSTALL" ]; then
	command -v agy >/dev/null || { echo "install-harnesses: agy is not on PATH after ANTIGRAVITY_INSTALL" >&2; exit 1; }
fi
npm cache clean --force >/dev/null 2>&1 || true
