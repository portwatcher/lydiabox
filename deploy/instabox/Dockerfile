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
# deploy/rootfs/Dockerfile, built for linux/amd64. Build both with
# deploy/instabox/build.sh (or `make instabox-image`).
#
# Run-time layout (the core uses the guest paths /home/agent and
# /workspace, so both stay as links onto the persistent disk):
#   /home/agent -> /data/home         agent home (InstaBox's HOME is /data/home)
#   /workspace  -> /data/workspace    agent workspace
#   /data/.lydiabox/guest.env
#                      lydiabox-guest settings the driver writes before
#                      the guest daemon starts (guest.env.example)

ARG BASE_IMAGE=lydiabox/guest:instabox-base
FROM ${BASE_IMAGE}

# The home content of the base image becomes the seed of /data/home, and
# /home/agent and /workspace become links onto the persistent disk (the
# import keeps both links; the entry point makes them when they are
# missing).
RUN set -eux; \
	mkdir -p /usr/local/share/lydiabox; \
	mv /home/agent /usr/local/share/lydiabox/home-seed; \
	ln -s /data/home /home/agent; \
	rm -rf /workspace; \
	ln -s /data/workspace /workspace; \
	mkdir -p /etc/lydiabox

COPY deploy/instabox/supervisord.conf /etc/lydiabox/instabox-supervisord.conf
COPY deploy/instabox/guest.env.example /etc/lydiabox/guest.env.example
COPY --chmod=0755 deploy/instabox/lydiabox-instabox-entry /usr/local/sbin/lydiabox-instabox-entry
COPY --chmod=0755 deploy/instabox/lydiabox-instabox-guest /usr/local/bin/lydiabox-instabox-guest
COPY --chmod=0755 deploy/instabox/lydiabox-instabox-xfce /usr/local/bin/lydiabox-instabox-xfce

# InstaBox exports the image ENV into sessions (exec, SSH) and into
# instabox-image.service, except HOME and PATH: there HOME is the machine
# user's /data/home (the same directory as /home/agent). DISPLAY matches
# the desktop that the entry point starts.
ENV HOME=/home/agent DISPLAY=:0 LYDIABOX_DATA_ROOT=/data

LABEL org.opencontainers.image.title="lydiabox-instabox" \
	org.opencontainers.image.description="LydiaBox guest desktop and lydiabox-guest for InstaBox snapshot import" \
	org.opencontainers.image.source="https://github.com/portwatcher/lydiabox"

# systemd starts the unit in WORKDIR before the entry point creates
# /data/home, so the work directory is /.
WORKDIR /
ENTRYPOINT ["/usr/local/sbin/lydiabox-instabox-entry"]
CMD []
