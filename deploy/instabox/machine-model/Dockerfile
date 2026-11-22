# Test image for deploy/instabox/test.sh (scenario "machine"): the
# LydiaBox InstaBox image as an InstaBox import makes it, verified on a
# real machine on 2026-11-16 (docs/instabox.md):
#
# - The import keeps the machine's own user database: no user "agent";
#   UID 1000 has the account's name (here "machineuser") and the home /data/home.
# - The machine keeps its own shared libraries and its dpkg database (the
#   template is Debian 13): the stage "template" installs a part of the
#   template's packages (apt, libdbus, the Xfce panel and its plugins) on
#   TEMPLATE_IMAGE, and the model puts their shared libraries and the
#   template's /var/lib/dpkg over the image's files. So an image of another
#   release fails here as on the machine (Ubuntu 24.04: apt-get update
#   stops with a segmentation fault, seen on 2026-11-19).
# - The image of an Ubuntu-based snapshot could not run dbus-run-session
#   (LIBDBUS_PRIVATE_1.14.10 not found); the stub below keeps that fact, so
#   the test checks that the image does not need dbus-run-session. The
#   lingering user manager runs the session bus at /run/user/1000/bus.
# - instabox-image.service sends standard output to a journal socket and
#   passes no HOME.
# - The template's /etc/codex/config.toml (with InstaBox's MCP servers)
#   and its /usr/local/bin/claude, codex, and opencode links to /opt/npm
#   can stay over the image's files. So does the template's own Node.js
#   in /usr/local/bin (a stub here that prints v22.0.0-template).
# - /data/home is InstaBox's own home with the template's files
#   (.claude.json with InstaBox's MCP servers, skills, AGENTS.md); with
#   INSTABOX_MODEL_OLD_LAYOUT=1, /home/agent links to it as in an image of
#   core 802e85c and earlier (model-init).
#
# IMAGE is the image under test; TEMPLATE_IMAGE is the release of the
# InstaBox template agent-browser.
ARG IMAGE=lydiabox/instabox:latest
ARG TEMPLATE_IMAGE=debian:trixie

FROM ${TEMPLATE_IMAGE} AS template
# /keep gets the shared libraries of all installed packages, with the
# directory part resolved (/lib is a link to /usr/lib), and the dpkg
# database.
RUN set -eu; \
	apt-get update; \
	DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends dbus-bin libdbus-1-3 xfce4-panel; \
	rm -rf /var/lib/apt/lists/*; \
	dpkg-query -W -f '${Package}:${Architecture}\n' | xargs dpkg -L 2>/dev/null | grep -E '/[^/]+\.so(\.[0-9]+)*$' | sort -u | \
	while read -r f; do \
		if [ -f "$f" ] || [ -L "$f" ]; then \
			d=/keep$(readlink -f "$(dirname "$f")"); \
			mkdir -p "$d"; \
			cp -a "$f" "$d/"; \
		fi; \
	done; \
	mkdir -p /keep/var/lib; \
	cp -a /var/lib/dpkg /keep/var/lib/dpkg

FROM ${IMAGE}

RUN set -eux; \
	userdel agent; \
	if getent group agent >/dev/null; then groupdel agent; fi; \
	groupadd --gid 1000 machineuser; \
	useradd --uid 1000 --gid 1000 --groups sudo --home-dir /data/home --no-create-home --shell /bin/bash machineuser; \
	printf '#!/bin/sh\necho "/usr/bin/dbus-run-session: /lib/x86_64-linux-gnu/libdbus-1.so.3: version LIBDBUS_PRIVATE_1.14.10 not found (machine-model stub)" >&2\nexit 1\n' \
	>/usr/bin/dbus-run-session; \
	chmod 0755 /usr/bin/dbus-run-session; \
	mkdir -p /etc/codex /etc/claude-code; \
	printf '[mcp_servers.instabox]\ncommand = "/usr/local/bin/instabox-mcp"\nargs = ["mcp"]\n' >/etc/codex/config.toml; \
	printf '{"env":{"DISABLE_AUTOUPDATER":"1"}}\n' >/etc/claude-code/managed-settings.json; \
	for h in claude codex opencode; do ln -sfn "/opt/npm/bin/$h" "/usr/local/bin/$h"; done; \
	printf '#!/bin/sh\necho v22.0.0-template\n' >/usr/local/bin/node; \
	chmod 0755 /usr/local/bin/node

# The template's shared libraries and dpkg database win over the image's.
RUN rm -rf /var/lib/dpkg
COPY --from=template /keep/ /

COPY --chmod=0755 model-init /usr/local/sbin/instabox-model-init
COPY --chmod=0755 stdout-socket /usr/local/sbin/instabox-model-stdout-socket

ENTRYPOINT ["/usr/local/sbin/instabox-model-init"]
CMD []
