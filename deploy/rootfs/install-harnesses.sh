#!/bin/sh
# Install the five harness CLIs into the guest image (docs/design.md 4.5.1).
#
# npm packages (override with build arguments of the same name):
#   CLAUDE_CODE_PACKAGE  @anthropic-ai/claude-code
#   CODEX_PACKAGE        @openai/codex
#   OPENCODE_PACKAGE     opencode-ai
# dsh and antigravity (agy) have no npm package named by the design; their
# installers are shell commands passed as build arguments:
#   DSH_INSTALL          for example: npm install -g <dsh package>
#   ANTIGRAVITY_INSTALL  for example: the vendor's .deb or install script
# An empty installer skips that harness with a warning.
set -eu

: "${CLAUDE_CODE_PACKAGE:=@anthropic-ai/claude-code}"
: "${CODEX_PACKAGE:=@openai/codex}"
: "${OPENCODE_PACKAGE:=opencode-ai}"
: "${DSH_INSTALL:=}"
: "${ANTIGRAVITY_INSTALL:=}"

npm install -g --no-fund --no-audit "$CLAUDE_CODE_PACKAGE" "$CODEX_PACKAGE" "$OPENCODE_PACKAGE"
command -v claude >/dev/null
command -v codex >/dev/null
command -v opencode >/dev/null

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
npm cache clean --force >/dev/null 2>&1 || true
