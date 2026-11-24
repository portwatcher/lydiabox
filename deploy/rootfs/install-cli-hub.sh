#!/bin/sh
# Install the CLI-Anything package manager cli-hub
# (https://github.com/HKUDS/CLI-Anything, Apache-2.0) and uv into the
# "desktop" guest image (docs/design.md section 4.4.3).
#
# - /opt/cli-hub is a Python venv outside the agent home (the home is
#   persistent storage at run time and hides image files). It holds
#   cli-anything-hub and uv at the pinned versions below.
# - The venv sees the user site (--system-site-packages), and its own
#   pip.conf sets "user = true". cli-hub installs a hub CLI with
#   `<venv python> -m pip install` (cli_hub/installer.py), so the package
#   goes to ~/.local (persistent) and its command to ~/.local/bin. The
#   setting is in the venv only: pip outside the venv is not changed.
# - /usr/local/bin/cli-hub is a wrapper (cli-hub-wrapper) that turns off
#   the upstream usage analytics (CLI_HUB_NO_ANALYTICS=1) for every
#   caller and puts ~/.local/bin on PATH.
# - /usr/local/bin/uv links to the venv's uv: the "uv" install strategy of
#   cli-hub and `uv tool install` need it.
#
# Override the versions with the build arguments of the same name:
#   CLI_HUB_VERSION   cli-anything-hub version (PyPI)
#   UV_VERSION        uv version (PyPI)
set -eu

: "${CLI_HUB_VERSION:=0.4.1}"
: "${UV_VERSION:=0.13.7}"
venv=/opt/cli-hub

python3 -m venv --system-site-packages "$venv"
"$venv/bin/python" -m pip install --no-cache-dir --disable-pip-version-check \
	"cli-anything-hub==$CLI_HUB_VERSION" "uv==$UV_VERSION"
cat >"$venv/pip.conf" <<'EOF'
# LydiaBox: packages that this venv's pip installs (cli-hub install) go to
# the user's ~/.local, which persists. Only this venv reads this file.
[install]
user = true
EOF
install -m 0755 /tmp/cli-hub-wrapper /usr/local/bin/cli-hub
ln -sf "$venv/bin/uv" /usr/local/bin/uv
ln -sf "$venv/bin/uvx" /usr/local/bin/uvx

# Checks with a throwaway home, so that no state stays in the image.
check_home=$(mktemp -d)
HOME=$check_home /usr/local/bin/cli-hub --version
HOME=$check_home /usr/local/bin/uv --version
user_site=$(HOME=$check_home "$venv/bin/python" -m pip config list)
echo "install-cli-hub: pip config of the venv: $user_site"
case "$user_site" in
*"install.user='true'"*) ;;
*)
	echo "install-cli-hub: the venv pip does not read $venv/pip.conf" >&2
	exit 1
	;;
esac
rm -rf "$check_home"
