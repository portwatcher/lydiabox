# LydiaBox

LydiaBox (`lydiabox`) is a self-hostable personal agent VM and harness
orchestration platform. One static Go binary runs the control plane: an
S3-backed POSIX filesystem (JuiceFS), the agent VM (Cloud Hypervisor or a
Docker fallback), the magpie LLM gateway, connectors, and an embedded web
workstation. See [docs/design.md](docs/design.md) for the architecture and
[docs/api.md](docs/api.md) for the HTTP and WebSocket API.

## Status

| Phase | Scope | State |
| :--- | :--- | :--- |
| 1 | Config, encrypted store, runtime bootstrap, JuiceFS storage, File API, WebDAV | Done |
| 2 | Guest daemon, VMM drivers (`chv`, `docker`), desktop streaming | Done |
| 3 | magpie gateway, harness session bridge | Done |
| 4 | Connectors and credential broker | Done |
| 5 | Web UI (Studio, Desktop, Files, Models, Connectors) | Done |

## Quick start

Requirements: Linux (or macOS with macFUSE for development), FUSE, an S3,
R2, or MinIO bucket. `mkfs.ext4` is needed for the `chv` driver.

```sh
make build
cp .env.example .env   # fill the LYDIABOX_S3_* values
set -a; . ./.env; set +a
./dist/lydiabox up
```

On first boot, `lydiabox up` downloads `juicefs` and `magpie` (plus
`cloud-hypervisor` and `virtiofsd` when `/dev/kvm` is available) into
`~/.lydiabox/runtime/bin`, verifies their SHA256 checksums, formats or
restores the JuiceFS volume, and prints the generated admin token.

```sh
./dist/lydiabox status           # VM, mount, and S3 sync state
./dist/lydiabox runtime status   # runtime binaries and guest images
./dist/lydiabox version
```

Files are available while the VM is off: in the web UI, through the REST
API (`/api/v1/fs`), and over WebDAV at `http://<host>:8080/webdav/` (user
`lydiabox`, password = admin token).

## Connectors

GitHub, Google Drive, Notion, and Slack connect through OAuth (Authorization
Code with PKCE) or a pasted token. Set `LYDIABOX_<PROVIDER>_CLIENT_ID` and
`_CLIENT_SECRET`, save a client through `PUT /api/v1/connectors/<provider>/client`,
or set `LYDIABOX_OAUTH_RELAY_URL`. Register the callback URL
`http(s)://<host>/api/v1/connectors/<provider>/callback` with the provider.

```sh
curl -H "Authorization: Bearer $TOKEN" -d '{"token":"ghp_..."}' \
  http://localhost:8080/api/v1/connectors/github/token
```

Tokens are AES-256-GCM encrypted in the host state database and refresh
automatically. The guest never stores them: `git` inside the VM uses
`lydiabox-guest git-cred` (set it up with `lydiabox-guest git-cred install`),
which asks the host broker for a fresh token on each `git push` or `pull`.
The same broker serves the connector MCP server (`github_*`, `gdrive_*`,
`notion_*`, `slack_*`). The guest reaches it over vsock port 9001, or the
UNIX socket `$LYDIABOX_DATA_DIR/run/host-9001.sock`. Every token issue and
tool call is in the audit log (`GET /api/v1/connectors/audit`).

## Agent VM (Phase 2)

The VM boots on demand: opening the desktop, starting a session, or
`POST /api/v1/vm/wake` calls the controller, and concurrent requests share
one boot. After `LYDIABOX_IDLE_TIMEOUT` without a running PTY or an open
desktop stream, the guest filesystems are synced and the VM is paused.

- `chv` (Linux with `/dev/kvm`, run as root for TAP and nftables): two
  `virtiofsd` daemons share `home/agent` and `workspace`, Cloud
  Hypervisor boots `vmlinux` + `initramfs.cpio.gz` with
  `rootfs.squashfs`, an auto-built read-only `lydiabox-guest.ext4`, and
  the persistent `system/root-upper.img`. The guest is `172.30.0.2/24`
  behind host NAT and cannot reach host private networks. Build the
  images with `lydiabox runtime build-rootfs` or set
  `LYDIABOX_ROOTFS_URL`.
- `docker` (no KVM, for example macOS): the same desktop image runs as a
  container; the guest daemon listens on a UNIX socket in a bind-mounted
  directory. Build the image with
  `deploy/rootfs/build-artifacts.sh --docker-image lydiabox/guest:latest --no-vm-images`.

Both drivers inject the linux `dist/lydiabox-guest` built by `make build`,
so upgrading `lydiabox` upgrades the guest daemon. Inside the guest,
`lydiabox-guest serve` runs the PTY manager, the X11 controller, WebRTC
streaming (`ffmpeg` x11grab to H.264 RTP to `pion/webrtc`), the Computer
Use MCP server on `http://127.0.0.1:8910/mcp`, the LLM gateway on
`127.0.0.1:3425` (tunneled to the host over vsock port 9002), and the
connector MCP bridge on `127.0.0.1:8911/mcp` (vsock port 9001). Desktop
media uses UDP port `LYDIABOX_WEBRTC_UDP_PORT` (default 50000), which the
host must allow.

## Models and harness sessions (Phase 3)

`lydiabox up` runs `magpie serve` on `127.0.0.1:3425` (data in
`$LYDIABOX_DATA_DIR/state/magpie`) and restarts it when it stops. Upstream
API keys stay in that directory on the host. Add providers, routing groups,
and the default model of each harness in the Models page or through
`/api/v1/models/*`.

Inside the VM, the five harnesses (`claude-code`, `codex`, `dsh`,
`antigravity`, `opencode`) talk to the guest gateway on
`127.0.0.1:3425` with the placeholder token `lydiabox-local`. The host side
of the tunnel replaces it with the VM gateway key (`lydiabox-vm`, encrypted
in the state database). LydiaBox keeps the harness configs in
`/home/agent` pointed at the gateway and registers the computer-use and
connector MCP servers; it changes only the keys it manages and keeps user
settings and comments.

Sessions of all five harnesses are discovered from `/home/agent`, so the
Studio lists them while the VM is off. Starting or resuming a session
wakes the VM. "Switch Harness / Model" interrupts the running harness,
writes `.agent/HANDOFF.md` and `.agent/state.json` into the workspace, adds a
managed block to `AGENTS.md`, `CLAUDE.md`, and `GEMINI.md`, and resumes the
task in the other harness in the same terminal. See
[docs/harness-formats.md](docs/harness-formats.md) for the session formats.

## Development

```sh
make build        # dist/lydiabox (host) + dist/lydiabox-guest (linux)
make build GOOS=linux GOARCH=arm64
make test         # CGO_ENABLED=0 tests, then the race detector
make lint         # go vet, gofmt, staticcheck, shellcheck
make web-build    # build web/ and embed it into the next `make build`
```

Web UI development (Node.js 22 or later) runs Vite with a proxy to a local
`lydiabox up` on port 8080:

```sh
cd web && npm ci && npm run dev    # http://localhost:5173
npm run typecheck && npm run lint && npm run test
```

See [web/README.md](web/README.md) for the layout and scripts.

All binaries build with `CGO_ENABLED=0`. Runtime binary versions and
checksums are pinned in
[internal/bootstrap/manifest.json](internal/bootstrap/manifest.json).

## Layout

```text
cmd/lydiabox            host CLI and control plane (internal/cli)
cmd/lydiabox-guest      guest daemon entrypoint (internal/guest/cli)
internal/config         env + lydiabox.yaml loader, validation, secrets, data dir layout
internal/store          SQLite state DB, migrations, AES-256-GCM secret encryption
internal/bootstrap      pinned runtime binary downloader and guest image fetcher
internal/storage        JuiceFS supervisor, S3 metadata backup and restore, WebDAV proxy
internal/storage/files  sandboxed File Manager service and HTTP handlers
internal/connector      OAuth2 PKCE and token binding, refresh, revoke, audit
internal/connector/broker  guest broker: git credentials and connector MCP server
internal/guest/gitcred  guest git credential helper (vsock or UNIX socket)
internal/vmm            VM driver interface and auto-wake / idle-hibernate controller
internal/vmm/chv        Cloud Hypervisor driver (REST API, virtiofsd, TAP NAT, vsock)
internal/vmm/docker     container fallback driver
internal/vmm/guestclient host client of the guest daemon RPC (PTY, MCP, desktop)
internal/desktop        desktop signaling relay, takeover mode, action broadcast
internal/magpie         magpie gateway supervisor, admin client, VM gateway key
internal/magpie/wire    non-destructive harness config wirers
internal/harness        session manager, terminal rooms, hot-switch bridge
internal/harness/adapters  claude-code, codex, dsh, antigravity, opencode session formats
internal/guest/*        guest daemon: rpc, ptymanager, x11, webrtc, mcp, credproxy
internal/api            HTTP server, auth, route registration contract
internal/webui          embedded web UI (built from web/, placeholder when not built)
web                     React + TypeScript web workstation (Vite)
internal/httpx          JSON response and error conventions
internal/tenant         request tenant context
deploy/initramfs        guest initramfs init (OverlayFS root, virtio-fs, switch_root)
deploy/rootfs           guest kernel, initramfs, and desktop image build (Dockerfile)
```
