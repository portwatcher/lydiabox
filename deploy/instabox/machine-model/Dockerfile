# Test image for deploy/instabox/test.sh (scenario "machine"): the
# LydiaBox InstaBox image as an InstaBox import makes it, verified on a
# real machine on 2026-11-16 (docs/instabox.md):
#
# - The import keeps the machine's own user database: no user "agent";
#   UID 1000 has the account's name (here "alice") and the home /data/home.
# - The machine keeps its own libdbus, so the image's dbus-run-session
#   cannot run (LIBDBUS_PRIVATE_1.14.10 not found); the lingering user
#   manager runs the session bus at /run/user/1000/bus instead.
# - instabox-image.service sends standard output to a journal socket and
#   passes no HOME.
# - The template's /etc/codex/config.toml (with InstaBox's MCP servers)
#   and its /usr/local/bin/claude, codex, and opencode links to /opt/npm
#   can stay over the image's files.
# - /data/home is InstaBox's own home with the template's files
#   (.claude.json with InstaBox's MCP servers, skills, AGENTS.md); with
#   INSTABOX_MODEL_OLD_LAYOUT=1, /home/agent links to it as in an image of
#   core 802e85c and earlier (model-init).
#
# IMAGE is the image under test.
ARG IMAGE=lydiabox/instabox:latest
FROM ${IMAGE}

RUN set -eux; \
	userdel agent; \
	if getent group agent >/dev/null; then groupdel agent; fi; \
	groupadd --gid 1000 alice; \
	useradd --uid 1000 --gid 1000 --groups sudo --home-dir /data/home --no-create-home --shell /bin/bash alice; \
	printf '#!/bin/sh\necho "/usr/bin/dbus-run-session: /lib/x86_64-linux-gnu/libdbus-1.so.3: version LIBDBUS_PRIVATE_1.14.10 not found (machine-model stub)" >&2\nexit 1\n' \
	>/usr/bin/dbus-run-session; \
	chmod 0755 /usr/bin/dbus-run-session; \
	mkdir -p /etc/codex /etc/claude-code; \
	printf '[mcp_servers.instabox]\ncommand = "/usr/local/bin/instabox-mcp"\nargs = ["mcp"]\n' >/etc/codex/config.toml; \
	printf '{"env":{"DISABLE_AUTOUPDATER":"1"}}\n' >/etc/claude-code/managed-settings.json; \
	for h in claude codex opencode; do ln -sfn "/opt/npm/bin/$h" "/usr/local/bin/$h"; done

COPY --chmod=0755 model-init /usr/local/sbin/instabox-model-init
COPY --chmod=0755 stdout-socket /usr/local/sbin/instabox-model-stdout-socket

ENTRYPOINT ["/usr/local/sbin/instabox-model-init"]
CMD []
