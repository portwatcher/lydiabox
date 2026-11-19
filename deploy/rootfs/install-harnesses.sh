#!/bin/sh
# Install the five harness CLIs into the guest image (docs/design.md 4.5.1).
#
# npm packages (override with build arguments of the same name):
#   CLAUDE_CODE_PACKAGE  @anthropic-ai/claude-code
#   CODEX_PACKAGE        @openai/codex
#   OPENCODE_PACKAGE     opencode-ai
# ACP agents for the chat view (internal/acp; pinned, override likewise):
#   CLAUDE_ACP_PACKAGE   @agentclientprotocol/claude-agent-acp@0.108.1 (claude-agent-acp)
#   CODEX_ACP_PACKAGE    @agentclientprotocol/codex-acp@2.7.0 (codex-acp; drives
#                        `codex app-server`)
# Both ACP agents run the installed claude and codex (the bridge sets
# CLAUDE_CODE_EXECUTABLE and CODEX_PATH), so prune_node_modules removes the
# copies that their packages bundle, with other files that no program reads
# at run time.
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
# prune_node_modules removes files that no program of the image runs or
# reads (the image must fit the InstaBox import disk; docs/instabox.md):
# - the Claude and Codex binaries bundled with the ACP agents (they use the
#   installed claude and codex);
# - opencode's platform package, whose binary its postinstall copied to
#   opencode-ai/bin/opencode.exe;
# - native prebuilds for other platforms;
# - source maps, and type declarations outside claude-agent-acp (its SDK
#   names .d.ts files at run time).
prune_node_modules() {
	root=$(npm root -g)
	rm -rf "$root/@agentclientprotocol/claude-agent-acp/node_modules/@anthropic-ai/"claude-agent-sdk-*
	rm -rf "$root/@agentclientprotocol/codex-acp/node_modules/@openai"
	if [ -f "$root/opencode-ai/bin/opencode.exe" ] && [ ! -L "$root/opencode-ai/bin/opencode.exe" ]; then
		rm -rf "$root/opencode-ai/node_modules/"opencode-*
	fi
	find "$root" -type d -path '*/prebuilds/*' \( -name 'win32-*' -o -name 'darwin-*' -o -name 'linux-arm*' \
		-o -name 'android-*' -o -name 'freebsd-*' \) -prune -exec rm -rf {} +
	rm -rf "$root/@deepseek-ai/dsh/node_modules/node-pty/third_party/conpty"
	find "$root" -type f \( -name '*.js.map' -o -name '*.mjs.map' -o -name '*.cjs.map' -o -name '*.css.map' \
		-o -name '*.d.ts.map' -o -name '*.d.mts.map' -o -name '*.d.cts.map' \) -delete
	find "$root" -path "$root/@agentclientprotocol/claude-agent-acp" -prune -o -type f \
		\( -name '*.d.ts' -o -name '*.d.mts' -o -name '*.d.cts' \) -exec rm -f {} +
}

prune_node_modules
# The programs still start after the prune (with a throwaway home, so the
# checks leave no state in the image).
check_home=$(mktemp -d)
HOME=$check_home claude --version >/dev/null
HOME=$check_home codex --version >/dev/null
HOME=$check_home opencode --version >/dev/null
rm -rf "$check_home"
command -v claude-agent-acp >/dev/null
command -v codex-acp >/dev/null
npm cache clean --force >/dev/null 2>&1 || true
