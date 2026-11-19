# syntax=docker/dockerfile:1.7
#
# LydiaBox guest image for InstaBox (docs/instabox.md). InstaBox imports a
# linux/amd64 OCI image from a registry as a snapshot
# (`instabox snapshot import IMAGE --name N`): it lays the image's files
# over an agent-browser machine and runs the image ENTRYPOINT and CMD as
# the systemd unit instabox-image.service, as root (no USER). systemd,
# sshd, sudo, the user database (UID 1000 has the account's name, not
# "agent", and the home /data/home), the shared libraries of the
# machine's own programs (glibc, libdbus), and /data stay the machine's.
# So the run-time scripts use UID 1000 by number, never a user name.
#
# BASE_IMAGE is the "desktop" (or "desktop-slim") stage of
# deploy/rootfs/Dockerfile, built for linux/amd64 on Debian 13 (trixie),
# the release of the template agent-browser. Build both with
# deploy/instabox/build.sh (or `make instabox-image`).
#
# The import keeps the machine's shared libraries (*.so) and dpkg
# database over the image's, but the image's other files (programs, /etc,
# links) replace the machine's: verified on a real machine
# (docs/instabox.md). With a Debian 13 base, the image's programs (apt,
# xfce4-panel, the dbus tools) and the machine's libraries are of the same
# release; the image of an Ubuntu 24.04 base mixed the two releases.
#
# Run-time layout (the core uses the guest paths /home/agent and
# /workspace, so both stay as links onto the persistent disk):
#   /home/agent -> /data/lydiabox/home   LydiaBox's agent home
#   /workspace  -> /data/workspace       agent workspace
#   /data/home                           InstaBox's own home (HOME of exec
#                                        and SSH sessions); not used
#   /data/.lydiabox/guest.env
#                      lydiabox-guest settings the driver writes before
#                      the guest daemon starts (guest.env.example)

ARG BASE_IMAGE=lydiabox/guest:instabox-base
FROM ${BASE_IMAGE}

# The home content of the base image becomes the seed of the agent home,
# and /home/agent and /workspace become links onto the persistent disk
# (the import keeps both links; the entry point makes them when they are
# missing and moves the link of the old layout).
# /etc/apt of the image replaces the machine's: it must have the sources of
# the base's release only, and no package lists. dpkg's unsafe I/O setting
# of the Debian container image does not go to a machine.
RUN set -eux; \
	rm -f /etc/dpkg/dpkg.cfg.d/docker-apt-speedup; \
	if [ "$(. /etc/os-release && echo "$ID")" = debian ] && grep -rIil -e ubuntu -e launchpad /etc/apt; then \
	echo "/etc/apt names Ubuntu" >&2; exit 1; \
	fi; \
	rm -rf /var/lib/apt/lists/*; \
	mkdir -p /usr/local/share/lydiabox/etc; \
	mv /home/agent /usr/local/share/lydiabox/home-seed; \
	ln -s /data/lydiabox/home /home/agent; \
	rm -rf /workspace; \
	ln -s /data/workspace /workspace; \
	mkdir -p /etc/lydiabox

# Harness and desktop settings that replace the machine template's; the
# entry point installs them at each start and links claude, codex,
# opencode, dsh, and Node.js (node, npm, npx, corepack) in /usr/local/bin
# to the image's programs
# (docs/instabox.md, "Supplier files and environment"):
# - /etc/codex/config.toml: the template's adds InstaBox's MCP servers.
# - /etc/claude-code/managed-settings.json: the template's sets
#   DISABLE_AUTOUPDATER=1; the image keeps that one setting as its own.
# - /etc/xdg/xfce4/panel/default.xml: a panel with only the plugins that
#   load in the panel's process (xfce4-panel.xml has the reason).
# The system upgrade installs the same files on existing machines: keep
# the table Files in deploy/instabox/system.go equal to these COPY lines
# (a test compares them; docs/instabox.md, "System upgrade").
COPY deploy/instabox/codex-config.toml /usr/local/share/lydiabox/etc/codex-config.toml
COPY deploy/instabox/claude-managed-settings.json /usr/local/share/lydiabox/etc/claude-managed-settings.json
COPY deploy/instabox/xfce4-panel.xml /usr/local/share/lydiabox/etc/xfce4-panel.xml
COPY deploy/instabox/supervisord.conf /etc/lydiabox/instabox-supervisord.conf
COPY deploy/instabox/guest.env.example /etc/lydiabox/guest.env.example
COPY --chmod=0755 deploy/instabox/lydiabox-instabox-entry /usr/local/sbin/lydiabox-instabox-entry
COPY --chmod=0755 deploy/instabox/lydiabox-instabox-guest /usr/local/bin/lydiabox-instabox-guest
COPY --chmod=0755 deploy/instabox/lydiabox-instabox-xfce /usr/local/bin/lydiabox-instabox-xfce

# InstaBox exports the image ENV into sessions (exec, SSH) and into
# instabox-image.service, except HOME and PATH: there HOME is the machine
# user's /data/home (InstaBox's home, not /home/agent). DISPLAY matches
# the desktop that the entry point starts.
ENV HOME=/home/agent DISPLAY=:0 LYDIABOX_DATA_ROOT=/data

LABEL org.opencontainers.image.title="lydiabox-instabox" \
	org.opencontainers.image.description="LydiaBox guest desktop and lydiabox-guest for InstaBox snapshot import" \
	org.opencontainers.image.source="https://github.com/portwatcher/lydiabox"

# systemd starts the unit in WORKDIR before the entry point creates
# /data/lydiabox/home, so the work directory is /.
WORKDIR /
ENTRYPOINT ["/usr/local/sbin/lydiabox-instabox-entry"]
CMD []
