#!/bin/sh
# Shared guest image setup for the "desktop" and "desktop-slim" stages of
# deploy/rootfs/Dockerfile. The agent user owns uid 1000 (the default user
# "ubuntu" of the Ubuntu image is removed; the Debian image has none),
# /workspace exists, git asks lydiabox-guest for credentials, and lbx
# (browser and computer use from the shell) is on PATH.
set -eux
if id ubuntu >/dev/null 2>&1; then userdel -r ubuntu; fi
useradd --create-home --uid 1000 --user-group --shell /bin/bash agent
echo 'agent ALL=(ALL) NOPASSWD:ALL' >/etc/sudoers.d/agent
chmod 0440 /etc/sudoers.d/agent
mkdir -p /workspace
chown agent:agent /workspace
git config --system credential.helper "/usr/local/bin/lydiabox-guest git-cred"
git config --system init.defaultBranch main
ln -sf /usr/local/bin/lydiabox-guest /usr/local/bin/git-credential-lydiabox
ln -sf /usr/local/bin/lydiabox-guest /usr/local/bin/lbx
