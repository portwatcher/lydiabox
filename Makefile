# LydiaBox build. Every Go build and test runs with CGO_ENABLED=0.
#
#   make build                          web UI (when npm is available) + dist/lydiabox (host
#                                       platform) + dist/lydiabox-guest (linux)
#   make build GOOS=linux GOARCH=arm64  cross build for a linux/arm64 host
#   make release                        dist/linux-<arch>/ for linux/amd64 and linux/arm64

GO         ?= go
GOOS       ?= $(shell $(GO) env GOOS)
GOARCH     ?= $(shell $(GO) env GOARCH)
DIST       ?= dist
VERSION    ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
COMMIT     ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
BUILD_DATE ?= $(shell date -u +%Y-%m-%dT%H:%M:%SZ)
VERSION_PKG := github.com/lydiabox/lydiabox/internal/version
LDFLAGS    := -s -w -X $(VERSION_PKG).Version=$(VERSION) -X $(VERSION_PKG).Commit=$(COMMIT) -X $(VERSION_PKG).BuildDate=$(BUILD_DATE)
GOBUILD     = CGO_ENABLED=0 $(GO) build -trimpath -ldflags "$(LDFLAGS)"
WEBUI_DIST := internal/webui/dist

export CGO_ENABLED := 0

.PHONY: all build build-host build-guest webui release test vet lint fmt web-build rootfs guest-image guest-image-slim virtiofsd clean
.PHONY: instabox-image instabox-image-test

all: build

build: webui
	@$(MAKE) --no-print-directory build-host build-guest

# Rebuilds the embedded web UI when npm and web/package.json are available;
# otherwise the binary embeds the existing bundle (or the placeholder page).
webui:
	@if [ ! -f web/package.json ]; then echo "notice: web/package.json not found; embedding the existing web UI bundle"; \
	elif ! command -v npm >/dev/null 2>&1; then echo "notice: npm not found; embedding the existing web UI bundle (install Node.js 22+ to rebuild it)"; \
	else $(MAKE) --no-print-directory web-build; fi

build-host:
	@mkdir -p $(DIST)
	GOOS=$(GOOS) GOARCH=$(GOARCH) $(GOBUILD) -o $(DIST)/lydiabox ./cmd/lydiabox

# The guest daemon always targets Linux (it runs inside the VM or container).
build-guest:
	@mkdir -p $(DIST)
	GOOS=linux GOARCH=$(GOARCH) $(GOBUILD) -o $(DIST)/lydiabox-guest ./cmd/lydiabox-guest

release: webui
	@for arch in amd64 arm64; do \
		echo "building linux/$$arch"; \
		mkdir -p $(DIST)/linux-$$arch; \
		GOOS=linux GOARCH=$$arch $(GOBUILD) -o $(DIST)/linux-$$arch/lydiabox ./cmd/lydiabox || exit 1; \
		GOOS=linux GOARCH=$$arch $(GOBUILD) -o $(DIST)/linux-$$arch/lydiabox-guest ./cmd/lydiabox-guest || exit 1; \
	done

# The race detector needs cgo on Linux (not on macOS). The race pass only
# instruments test binaries; shipped binaries are always CGO_ENABLED=0.
RACE_CGO := $(if $(filter linux,$(shell $(GO) env GOHOSTOS)),1,0)

test:
	CGO_ENABLED=0 $(GO) test -count=1 ./...
	CGO_ENABLED=$(RACE_CGO) $(GO) test -race -count=1 ./...

vet:
	CGO_ENABLED=0 $(GO) vet ./...

fmt:
	gofmt -w cmd internal

lint: vet
	@unformatted=$$(gofmt -l cmd internal); \
	if [ -n "$$unformatted" ]; then echo "gofmt needed:"; echo "$$unformatted"; exit 1; fi
	@if command -v staticcheck >/dev/null 2>&1; then CGO_ENABLED=0 staticcheck ./...; \
	else echo "staticcheck not installed; skipped (go install honnef.co/go/tools/cmd/staticcheck@latest)"; fi
	@if command -v shellcheck >/dev/null 2>&1; then shellcheck deploy/rootfs/build-artifacts.sh deploy/rootfs/setup-guest.sh deploy/rootfs/install-harnesses.sh deploy/rootfs/lydiabox-init deploy/initramfs/init deploy/virtiofsd/build.sh; fi
	@if command -v shellcheck >/dev/null 2>&1; then shellcheck -x deploy/instabox/build.sh deploy/instabox/test.sh deploy/instabox/test-checks.sh deploy/instabox/lydiabox-instabox-entry deploy/instabox/lydiabox-instabox-guest deploy/instabox/lydiabox-instabox-xfce deploy/instabox/machine-model/model-init; fi

# Builds web/ (Phase 5) and copies the bundle into internal/webui/dist, which
# go:embed picks up on the next `make build`.
web-build:
	@test -f web/package.json || { echo "web/package.json not found: the web UI is not in this checkout"; exit 1; }
	@if [ ! -d web/node_modules ] || [ web/package-lock.json -nt web/node_modules/.package-lock.json ]; then cd web && npm ci; fi
	cd web && npm run build
	find $(WEBUI_DIST) -mindepth 1 ! -name .gitkeep -delete
	cp -R web/dist/. $(WEBUI_DIST)/

# Guest images (deploy/rootfs/Dockerfile, Docker with BuildKit):
#   make rootfs              vmlinux, initramfs.cpio.gz, rootfs.squashfs for chv
#   make guest-image         desktop image for the docker driver ($(GUEST_IMAGE))
#   make guest-image-slim    small desktop image ($(GUEST_IMAGE_SLIM))
GUEST_IMAGE      ?= lydiabox/guest:latest
GUEST_IMAGE_SLIM ?= lydiabox/guest:slim

rootfs:
	deploy/rootfs/build-artifacts.sh

guest-image:
	deploy/rootfs/build-artifacts.sh --docker-image $(GUEST_IMAGE) --no-vm-images

guest-image-slim:
	deploy/rootfs/build-artifacts.sh --slim --docker-image $(GUEST_IMAGE_SLIM) --no-vm-images

# LydiaBox image for InstaBox (deploy/instabox, docs/instabox.md), linux/amd64:
#   make instabox-image          build $(INSTABOX_IMAGE) and print the import command
#   make instabox-image-test     container test of $(INSTABOX_IMAGE) (skips without Docker)
# INSTABOX_IMAGE_FLAGS passes build.sh flags, for example --slim or --push.
INSTABOX_IMAGE       ?= lydiabox/instabox:latest
INSTABOX_IMAGE_FLAGS ?=

instabox-image:
	deploy/instabox/build.sh --tag $(INSTABOX_IMAGE) $(INSTABOX_IMAGE_FLAGS)

instabox-image-test:
	deploy/instabox/test.sh --image $(INSTABOX_IMAGE)

# virtiofsd from source as a static musl binary, dist/virtiofsd-linux-<arch>
# (upstream has no linux/arm64 build). Install it into runtime/bin with
# `lydiabox runtime install-local virtiofsd dist/virtiofsd-linux-<arch>`;
# `lydiabox runtime build-rootfs` builds and installs it on arm64.
VIRTIOFSD_ARCH ?= $(GOARCH)

virtiofsd:
	deploy/virtiofsd/build.sh --arch $(VIRTIOFSD_ARCH)

clean:
	@test -n "$(DIST)" && test "$(DIST)" != "/" && rm -rf -- "$(DIST)"
	find $(WEBUI_DIST) -mindepth 1 ! -name .gitkeep -delete
