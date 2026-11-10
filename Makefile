# LydiaBox build. Every Go build and test runs with CGO_ENABLED=0.
#
#   make build                          dist/lydiabox (host platform) + dist/lydiabox-guest (linux)
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

.PHONY: all build build-host build-guest release test vet lint fmt web-build clean

all: build

build: build-host build-guest

build-host:
	@mkdir -p $(DIST)
	GOOS=$(GOOS) GOARCH=$(GOARCH) $(GOBUILD) -o $(DIST)/lydiabox ./cmd/lydiabox

# The guest daemon always targets Linux (it runs inside the VM or container).
build-guest:
	@mkdir -p $(DIST)
	GOOS=linux GOARCH=$(GOARCH) $(GOBUILD) -o $(DIST)/lydiabox-guest ./cmd/lydiabox-guest

release:
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
	@if command -v shellcheck >/dev/null 2>&1; then shellcheck deploy/rootfs/build-artifacts.sh; fi

# Builds web/ (Phase 5) and copies the bundle into internal/webui/dist, which
# go:embed picks up on the next `make build`.
web-build:
	@test -f web/package.json || { echo "web/package.json not found: the web UI is not in this checkout"; exit 1; }
	cd web && npm ci && npm run build
	find $(WEBUI_DIST) -mindepth 1 ! -name .gitkeep -delete
	cp -R web/dist/. $(WEBUI_DIST)/

clean:
	@test -n "$(DIST)" && test "$(DIST)" != "/" && rm -rf -- "$(DIST)"
	find $(WEBUI_DIST) -mindepth 1 ! -name .gitkeep -delete
