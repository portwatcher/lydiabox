# syntax=docker/dockerfile:1.7
#
# LydiaBox guest images (docs/design.md sections 4.3 and 4.5).
#
# Build from the repository root with deploy/rootfs/build-artifacts.sh:
#   target "artifacts"  /vmlinux, /initramfs.cpio.gz, /rootfs.squashfs (chv driver)
#   target "desktop"    the guest desktop image run by the docker driver
#   target "desktop-slim"  a small desktop image (Xvfb, Xfce, terminal, no
#                       Chrome, Blender, ffmpeg, or harness CLIs) for the
#                       docker driver on small hosts and in tests
#
# Build arguments:
#   DESKTOP_IMAGE        distribution of the "desktop" and "desktop-slim"
#                        stages: ubuntu:24.04 (default; the chv and docker
#                        drivers) or debian:trixie-slim (the InstaBox image,
#                        deploy/instabox/build.sh). One package list serves
#                        both; the steps that differ check ID in
#                        /etc/os-release.
#   CH_LINUX_REF         branch of github.com/cloud-hypervisor/linux (kernel)
#   NODE_MAJOR           Node.js major version for the harness CLIs
#   CLAUDE_CODE_PACKAGE, CODEX_PACKAGE, OPENCODE_PACKAGE, DSH_INSTALL,
#   ANTIGRAVITY_INSTALL  harness installers (deploy/rootfs/install-harnesses.sh)

ARG UBUNTU_IMAGE=ubuntu:24.04
# The kernel, initramfs, and squash stages are build tools and stay on
# UBUNTU_IMAGE.
ARG DESKTOP_IMAGE=${UBUNTU_IMAGE}
ARG GO_IMAGE=golang:1.26-bookworm
# Stage copied into rootfs.squashfs: "desktop" or "desktop-slim"
# (build-artifacts.sh --slim).
ARG ROOTFS_STAGE=desktop

# --- Guest daemon (also bind-mounted at run time by the VMM drivers) ---------
FROM --platform=$BUILDPLATFORM ${GO_IMAGE} AS guestbin
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=linux GOARCH=$TARGETARCH go build -trimpath -ldflags "-s -w" \
	-o /out/lydiabox-guest ./cmd/lydiabox-guest

# --- Guest kernel (Cloud Hypervisor direct kernel boot) ----------------------
FROM ${UBUNTU_IMAGE} AS kernel
ARG TARGETARCH
ARG CH_LINUX_REF=ch-6.12.8
RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
	bc bison build-essential ca-certificates cpio flex git kmod libelf-dev libssl-dev python3 \
	&& rm -rf /var/lib/apt/lists/*
RUN git clone --depth 1 --branch "$CH_LINUX_REF" https://github.com/cloud-hypervisor/linux.git /src/linux
WORKDIR /src/linux
COPY deploy/rootfs/kernel.config /src/kernel.config
RUN set -eux; \
	case "$TARGETARCH" in \
	amd64) karch=x86_64 ;; \
	arm64) karch=arm64 ;; \
	*) echo "unsupported architecture $TARGETARCH" >&2; exit 1 ;; \
	esac; \
	make ARCH="$karch" ch_defconfig; \
	scripts/kconfig/merge_config.sh -m .config /src/kernel.config; \
	make ARCH="$karch" olddefconfig; \
	missing=$(grep -E '^CONFIG_[A-Z0-9_]+=' /src/kernel.config | while read -r opt; do grep -qx "$opt" .config || echo "$opt"; done); \
	if [ -n "$missing" ]; then echo "kernel.config options not in the final .config: $missing" >&2; exit 1; fi; \
	mkdir -p /out; \
	if [ "$karch" = x86_64 ]; then \
	KCFLAGS="-Wa,-mx86-used-note=no" make ARCH=x86_64 -j"$(nproc)" bzImage; \
	cp arch/x86/boot/compressed/vmlinux.bin /out/vmlinux; \
	else \
	make ARCH=arm64 -j"$(nproc)" Image; \
	cp arch/arm64/boot/Image /out/vmlinux; \
	fi

# --- Initramfs (busybox + deploy/initramfs/init) -----------------------------
FROM ${UBUNTU_IMAGE} AS initramfs
RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends \
	busybox-static cpio gzip && rm -rf /var/lib/apt/lists/*
COPY deploy/initramfs/init /initramfs/init
RUN set -eux; \
	cd /initramfs; \
	mkdir -p bin sbin usr/bin usr/sbin dev proc sys mnt run tmp; \
	cp /bin/busybox bin/busybox; \
	chmod 0755 init bin/busybox; \
	for app in $(bin/busybox --list); do [ -e "bin/$app" ] || ln -s busybox "bin/$app"; done; \
	mknod -m 600 dev/console c 5 1 || true; \
	mknod -m 666 dev/null c 1 3 || true; \
	mkdir -p /out; \
	find . -print0 | cpio --null --create --format=newc --quiet | gzip -9 >/out/initramfs.cpio.gz

# --- Desktop root filesystem (also the docker driver image) ------------------
FROM ${DESKTOP_IMAGE} AS desktop
ARG TARGETARCH
ARG NODE_MAJOR=24
ARG CLAUDE_CODE_PACKAGE=@anthropic-ai/claude-code
ARG CODEX_PACKAGE=@openai/codex
ARG OPENCODE_PACKAGE=opencode-ai
# Defaults are in install-harnesses.sh; pass an empty value to skip one.
ARG DSH_INSTALL="npm install -g --no-fund --no-audit @deepseek-ai/dsh"
ARG ANTIGRAVITY_INSTALL="curl -fsSL https://antigravity.google/cli/install.sh | bash -s -- --dir /usr/local/bin"
ENV DEBIAN_FRONTEND=noninteractive LANG=C.UTF-8 LC_ALL=C.UTF-8 TZ=Etc/UTC
COPY deploy/rootfs/dpkg-excludes /tmp/dpkg-excludes
# Ubuntu and Debian get the same package list. Distribution steps:
# - Debian: dpkg leaves out the manuals, translations, and documentation
#   (as /etc/dpkg/dpkg.cfg.d/excludes of the Ubuntu image does), so that
#   the InstaBox image fits the import disk (docs/instabox.md).
# - Ubuntu: software-properties-common (add-apt-repository, for the
#   Chromium PPA on arm64); Debian 13 does not have the package.
# bubblewrap: Codex's background app server checks for it on PATH at each
# start (also when the session runs with
# --dangerously-bypass-approvals-and-sandbox) and shows a warning without it.
RUN set -eux; \
	. /etc/os-release; \
	extra=""; \
	case "$ID" in \
	ubuntu) extra=software-properties-common ;; \
	debian) cp /tmp/dpkg-excludes /etc/dpkg/dpkg.cfg.d/lydiabox-excludes ;; \
	*) echo "unsupported distribution $ID" >&2; exit 1 ;; \
	esac; \
	rm /tmp/dpkg-excludes; \
	apt-get update; \
	apt-get install -y --no-install-recommends \
	xvfb xorg xfce4 xfce4-terminal dbus dbus-x11 fonts-noto-cjk ffmpeg blender \
	git gh curl jq ripgrep python3 python3-pip python3-venv bubblewrap \
	supervisor sudo ca-certificates gnupg wget xdg-utils iproute2 iputils-ping procps less \
	vim-tiny unzip zip openssh-client build-essential $extra; \
	rm -rf /var/lib/apt/lists/*
# Node.js for the harness CLIs (NodeSource: its "nodistro" source serves
# Ubuntu and Debian).
RUN set -eux; \
	curl -fsSL "https://deb.nodesource.com/setup_${NODE_MAJOR}.x" | bash -; \
	apt-get install -y --no-install-recommends nodejs; \
	rm -rf /var/lib/apt/lists/*
# Google Chrome on amd64. On arm64: Chromium from Debian main, or on Ubuntu
# the deb of the xtradeb PPA (Ubuntu's own chromium is a snap).
RUN set -eux; \
	. /etc/os-release; \
	if [ "$TARGETARCH" = amd64 ]; then \
	curl -fsSL https://dl.google.com/linux/linux_signing_key.pub | gpg --dearmor -o /usr/share/keyrings/google-chrome.gpg; \
	echo "deb [arch=amd64 signed-by=/usr/share/keyrings/google-chrome.gpg] https://dl.google.com/linux/chrome/deb/ stable main" \
	>/etc/apt/sources.list.d/google-chrome.list; \
	apt-get update; apt-get install -y --no-install-recommends google-chrome-stable; \
	elif [ "$ID" = ubuntu ]; then \
	add-apt-repository -y ppa:xtradeb/apps; \
	apt-get update; apt-get install -y --no-install-recommends chromium; \
	else \
	apt-get update; apt-get install -y --no-install-recommends chromium; \
	fi; \
	rm -rf /var/lib/apt/lists/*
COPY deploy/rootfs/install-harnesses.sh /tmp/install-harnesses.sh
RUN CLAUDE_CODE_PACKAGE="$CLAUDE_CODE_PACKAGE" CODEX_PACKAGE="$CODEX_PACKAGE" OPENCODE_PACKAGE="$OPENCODE_PACKAGE" \
	DSH_INSTALL="$DSH_INSTALL" ANTIGRAVITY_INSTALL="$ANTIGRAVITY_INSTALL" sh /tmp/install-harnesses.sh \
	&& rm /tmp/install-harnesses.sh
COPY deploy/rootfs/setup-guest.sh /tmp/setup-guest.sh
RUN sh /tmp/setup-guest.sh && rm /tmp/setup-guest.sh
COPY --from=guestbin /out/lydiabox-guest /usr/local/bin/lydiabox-guest
COPY deploy/rootfs/supervisord.conf /etc/supervisor/supervisord.conf
COPY deploy/rootfs/lydiabox-init /usr/local/sbin/lydiabox-init
RUN chmod 0755 /usr/local/sbin/lydiabox-init /usr/local/bin/lydiabox-guest
ENV HOME=/home/agent
WORKDIR /home/agent
CMD ["/usr/local/sbin/lydiabox-init"]

# --- Slim desktop image (docker driver on small hosts, CI, and tests) --------
# The same init, supervisor layout, agent user, and guest daemon as
# "desktop", with only Xvfb, a minimal Xfce session, xfce4-terminal, and
# ffmpeg for the WebRTC stream. It has no Chrome, Blender, Node.js, or
# harness CLIs:
#   deploy/rootfs/build-artifacts.sh --slim --docker-image lydiabox/guest:slim --no-vm-images
FROM ${DESKTOP_IMAGE} AS desktop-slim
ENV DEBIAN_FRONTEND=noninteractive LANG=C.UTF-8 LC_ALL=C.UTF-8 TZ=Etc/UTC
RUN apt-get update && apt-get install -y --no-install-recommends \
	xvfb xfce4-session xfwm4 xfce4-terminal dbus dbus-x11 x11-utils ffmpeg \
	git curl ca-certificates supervisor sudo iproute2 procps less \
	&& rm -rf /var/lib/apt/lists/*
COPY deploy/rootfs/setup-guest.sh /tmp/setup-guest.sh
RUN sh /tmp/setup-guest.sh && rm /tmp/setup-guest.sh
COPY --from=guestbin /out/lydiabox-guest /usr/local/bin/lydiabox-guest
COPY deploy/rootfs/supervisord.conf /etc/supervisor/supervisord.conf
COPY deploy/rootfs/lydiabox-init /usr/local/sbin/lydiabox-init
RUN chmod 0755 /usr/local/sbin/lydiabox-init /usr/local/bin/lydiabox-guest
ENV HOME=/home/agent
WORKDIR /home/agent
CMD ["/usr/local/sbin/lydiabox-init"]

# --- rootfs.squashfs ----------------------------------------------------------
FROM ${ROOTFS_STAGE} AS rootfs-src

FROM ${UBUNTU_IMAGE} AS squash
RUN apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends squashfs-tools \
	&& rm -rf /var/lib/apt/lists/*
COPY --from=rootfs-src / /rootfs/
RUN set -eux; \
	rm -rf /rootfs/proc/* /rootfs/sys/* /rootfs/dev/* /rootfs/tmp/* /rootfs/run/*; \
	mkdir -p /out; \
	mksquashfs /rootfs /out/rootfs.squashfs -comp zstd -noappend -quiet

# --- Exported files (build-artifacts.sh --target artifacts) -------------------
FROM scratch AS artifacts
COPY --from=kernel /out/vmlinux /vmlinux
COPY --from=initramfs /out/initramfs.cpio.gz /initramfs.cpio.gz
COPY --from=squash /out/rootfs.squashfs /rootfs.squashfs
