# LydiaBox

LydiaBox (`lydiabox`) is a self-hostable personal agent workstation. One
static Go binary runs the control plane on your server:

- an S3-backed POSIX filesystem (JuiceFS) for `/workspace` and
  `/home/agent`, readable and writable while the agent VM is off;
- an agent VM (Cloud Hypervisor on Linux with KVM, or a Docker container)
  with an Xfce desktop, Chrome, Blender, and five agent harnesses
  (`claude-code`, `codex`, `dsh`, `antigravity`, `opencode`);
- the magpie LLM gateway, which keeps upstream API keys on the host;
- GitHub, Google Drive, Notion, and Slack connectors whose tokens never
  reach the VM disk;
- a web workstation: terminal sessions, live desktop with takeover, files,
  models, and connectors.

See [docs/design.md](docs/design.md) for the architecture,
[docs/api.md](docs/api.md) for the HTTP and WebSocket API, and
[docs/conformance.md](docs/conformance.md) for the implementation status.

## Quick start

You need a Linux host with FUSE (`/dev/fuse`), Docker or KVM, and an S3,
Cloudflare R2, or MinIO bucket.

```sh
make build                       # dist/lydiabox and dist/lydiabox-guest
cp .env.example .env             # fill the LYDIABOX_S3_* values
set -a; . ./.env; set +a
./dist/lydiabox up
```

`lydiabox up` does everything on first start:

1. It downloads `juicefs` and `magpie` (and `cloud-hypervisor` and
   `virtiofsd` for the `chv` driver) into `$LYDIABOX_DATA_DIR/runtime/bin`
   and verifies their pinned SHA256 checksums.
2. It restores the filesystem from the metadata backup in the bucket, or
   formats a new JuiceFS volume, and mounts it.
3. It prints a generated admin token (saved in
   `$LYDIABOX_DATA_DIR/state/auth.token`) unless you set
   `LYDIABOX_AUTH_TOKEN`.

Open `http://<host>:8080/` and sign in with the admin token. The VM starts
when you open a terminal session or the desktop, and pauses after
`LYDIABOX_IDLE_TIMEOUT` without activity.

```sh
./dist/lydiabox status           # VM, mount, and S3 sync state
./dist/lydiabox runtime status   # runtime binaries and guest images
./dist/lydiabox version
```

Back up `$LYDIABOX_DATA_DIR/state/master.key`. It decrypts the stored
connector tokens. To recover on a new host, copy the master key to
`$LYDIABOX_DATA_DIR/state/master.key` and start `lydiabox up` with the
same S3 settings and an otherwise empty data directory. LydiaBox restores
the filesystem metadata (`jfs-meta.json.gz`) and the host state database
(connectors, session metadata, model bindings, from
`.lydiabox-system/tenants/default/lydiabox.db.gz`) before it opens them,
and logs each restore. The master key must be the same: with a new key the
state database is restored, but the stored connector tokens cannot be
decrypted (the log tells you), and you must reconnect the connectors. An
existing `lydiabox.db` is never replaced.

## Configuration

Set environment variables, or put the same settings in `lydiabox.yaml`.
[.env.example](.env.example) documents every variable with its YAML key.

| Variable | Default | Description |
| :--- | :--- | :--- |
| `LYDIABOX_S3_ENDPOINT` | required | S3, R2, or MinIO endpoint URL |
| `LYDIABOX_S3_BUCKET` | required | Bucket name |
| `LYDIABOX_S3_ACCESS_KEY` | required | Access key ID |
| `LYDIABOX_S3_SECRET_KEY` | required | Secret access key |
| `LYDIABOX_S3_REGION` | `auto` | Region (AWS needs the bucket region) |
| `LYDIABOX_S3_BUCKET_URL` | derived | JuiceFS `--bucket` URL override |
| `LYDIABOX_LISTEN_ADDR` | `0.0.0.0:8080` | Web UI, API, WebSocket, `/webdav`, and `/s3` |
| `LYDIABOX_S3_GATEWAY_ADDR` | `127.0.0.1:8090` | Backend of the `/s3` gateway (empty or `off` disables it) |
| `LYDIABOX_AUTH_TOKEN` | generated | Admin password (16 characters or more) |
| `LYDIABOX_MASTER_KEY` | generated | 64 hex characters; encrypts connector tokens |
| `LYDIABOX_DATA_DIR` | `~/.lydiabox` | Runtime, caches, metadata, and the mount |
| `LYDIABOX_CONFIG` | `./lydiabox.yaml` | YAML configuration file |
| `LYDIABOX_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, or `error` |
| `LYDIABOX_META_SYNC_INTERVAL` | `60s` | Metadata backup interval (minimum 5s) |
| `LYDIABOX_VM_DRIVER` | `auto` | `chv` when `/dev/kvm` is accessible, else `docker` |
| `LYDIABOX_VM_CPUS` | `4` | Guest vCPUs |
| `LYDIABOX_VM_MEM_MB` | `8192` | Guest RAM in MiB |
| `LYDIABOX_GPU_PCI_ADDR` | empty | VFIO PCI address for GPU passthrough (`chv`) |
| `LYDIABOX_IDLE_TIMEOUT` | `20m` | Pause the VM after this long without sessions or viewers (`0` disables) |
| `LYDIABOX_WEBRTC_UDP_PORT` | `50000` | UDP port of the desktop stream (`0` disables forwarding) |
| `LYDIABOX_DOCKER_IMAGE` | `lydiabox/guest:latest` | Guest image of the `docker` driver |
| `LYDIABOX_GUEST_BINARY` | found next to `lydiabox` | Linux `lydiabox-guest` injected into the VM |
| `LYDIABOX_ROOTFS_URL` | empty | Base URL of prebuilt guest images (`chv`) |
| `LYDIABOX_GATEWAY_UPSTREAM` | `http://127.0.0.1:3425` | LLM gateway that guest traffic goes to |
| `LYDIABOX_OAUTH_RELAY_URL` | empty | Optional OAuth relay for connectors |
| `LYDIABOX_<PROVIDER>_CLIENT_ID`, `_CLIENT_SECRET` | empty | OAuth apps: `GITHUB`, `GDRIVE`, `NOTION`, `SLACK` |
| `LYDIABOX_MIRROR_<BINARY>`, `_SHA256` | pinned | Download mirror for a runtime binary |

## Agent VM drivers

### Cloud Hypervisor (`chv`, Linux with KVM)

Requirements:

- `/dev/kvm` readable and writable by the user that runs `lydiabox`;
- root (or `CAP_NET_ADMIN`) for the TAP device and NAT rules;
- `nft` (or `iptables`), `ip`, and `sysctl` for networking;
- `mkfs.ext4` (e2fsprogs) for the persistent root image and the guest
  binary image;
- guest images: `vmlinux`, `initramfs.cpio.gz`, and `rootfs.squashfs` in
  `$LYDIABOX_DATA_DIR/runtime/images`.

Build the guest images locally with Docker (BuildKit), or download them
from a server that publishes the same files with a `SHA256SUMS` list:

```sh
./dist/lydiabox runtime build-rootfs        # or: make rootfs
LYDIABOX_ROOTFS_URL=https://example.com/lydiabox/images ./dist/lydiabox runtime fetch-images
```

`lydiabox up` downloads `cloud-hypervisor` and `virtiofsd`. The guest
boots `rootfs.squashfs` read-only with a persistent overlay
(`system/root-upper.img` on JuiceFS), so `apt install`, `/etc` changes,
and browser profiles survive a restart. `/home/agent` and `/workspace` are
shared through virtio-fs. The guest has the address `172.30.0.2/24`
behind NAT and cannot reach private networks of the host.

### Docker fallback (`docker`)

Without `/dev/kvm` (for example on a VPS without nested virtualization),
the same desktop runs as a container. Build the image once:

```sh
make guest-image          # lydiabox/guest:latest: Chrome, Blender, harnesses
make guest-image-slim     # lydiabox/guest:slim: Xvfb, Xfce, terminal, ffmpeg
```

Set `LYDIABOX_DOCKER_IMAGE` to use the slim image. The container shares
`/home/agent` and `/workspace` through bind mounts of the JuiceFS mount.
Stop keeps the container, so changes to its root filesystem (`apt install`,
`/etc`) persist across stop and wake, as with `chv`. `/tmp` is empty
on each boot. LydiaBox creates a new container (and loses those changes)
when the image, the VM settings, or the guest binary changes, or when you
stop with `{"resetRoot": true}` (`POST /api/v1/vm/stop`).

### macOS and hosts without FUSE

The JuiceFS mount needs FUSE. On macOS, install macFUSE, or run LydiaBox in
a privileged Linux container that has FUSE and its own Docker engine:

```sh
make release
docker run -d --privileged --name lydiabox -p 8080:8080 \
  -v "$PWD/dist:/opt/lydiabox:ro" --env-file .env docker:28-dind
docker exec lydiabox apk add --no-cache fuse fuse3
docker exec -d lydiabox /opt/lydiabox/linux-arm64/lydiabox up
```

Use `linux-amd64` on an Intel Mac. Load the guest image into that engine
with
`docker save lydiabox/guest:slim | docker exec -i lydiabox docker load`.
[docs/conformance.md](docs/conformance.md) records a full run of this
setup.

### GPU passthrough for Blender (`chv`)

1. Enable the IOMMU in the firmware and on the kernel command line
   (`intel_iommu=on` or `amd_iommu=on`, plus `iommu=pt`).
2. Find the GPU and every device in its IOMMU group:
   `lspci -nn | grep -i vga` and `ls /sys/bus/pci/devices/0000:01:00.0/iommu_group/devices`.
3. Bind each device of the group to `vfio-pci`:

   ```sh
   modprobe vfio-pci
   echo vfio-pci > /sys/bus/pci/devices/0000:01:00.0/driver_override
   echo 0000:01:00.0 > /sys/bus/pci/devices/0000:01:00.0/driver/unbind
   echo 0000:01:00.0 > /sys/bus/pci/drivers_probe
   ```

4. Set `LYDIABOX_GPU_PCI_ADDR=0000:01:00.0` and restart `lydiabox up`.
   Cloud Hypervisor attaches the device; install the guest GPU driver in
   the VM (it persists in the overlay).

## Files, WebDAV, and S3

Files are available while the VM is off: in the Files page, through the
REST API (`/api/v1/fs`), over S3 (below), and over WebDAV at `http://<host>:8080/webdav/`
(user `lydiabox`, password = admin token). `/webdav/workspace/` and
`/webdav/home/agent/` are the two user trees.

```sh
# macOS Finder: Go > Connect to Server > http://<host>:8080/webdav/
rclone mount :webdav: ~/lydiabox --webdav-url=http://<host>:8080/webdav/ \
  --webdav-user=lydiabox --webdav-pass="$(rclone obscure "$LYDIABOX_AUTH_TOKEN")"
```

The virtual S3 gateway serves the same files under `/s3` on the main port.
Use the endpoint `http://<host>:8080`, the bucket `s3`, and path-style
addressing. The access key is `lydiabox` and the secret key is the admin
token. Object keys are `workspace/...` and `home/agent/...`. Name the
bucket in each command; `aws s3 ls` without a bucket does not work,
because `/` is the web UI.

```sh
export AWS_ACCESS_KEY_ID=lydiabox AWS_SECRET_ACCESS_KEY="$LYDIABOX_AUTH_TOKEN"
aws --endpoint-url http://<host>:8080 s3 ls s3://s3/workspace/
aws --endpoint-url http://<host>:8080 s3 cp report.pdf s3://s3/workspace/
rclone lsd :s3:s3/workspace --s3-provider=Minio --s3-force-path-style \
  --s3-endpoint=http://<host>:8080 \
  --s3-access-key-id=lydiabox --s3-secret-access-key="$LYDIABOX_AUTH_TOKEN"
```

In an rclone configuration file, set `force_path_style = true`. The gateway
process (`juicefs gateway`) listens on `LYDIABOX_S3_GATEWAY_ADDR` (default
`127.0.0.1:8090`); LydiaBox forwards `/s3` to it without changes, so the
SigV4 signature stays valid.

Put LydiaBox behind TLS before you expose it to a network you do not
trust.

## Models and sessions

`lydiabox up` runs magpie on `127.0.0.1:3425`. Add providers, routing
groups, and the default model of each harness in the Models page. Upstream
keys stay in `$LYDIABOX_DATA_DIR/state/magpie`. Inside the VM, harnesses
use the gateway at `http://127.0.0.1:3425` with a placeholder token; the
host side of the tunnel replaces it with a revocable gateway key.

The Studio lists the sessions of all five harnesses from `/home/agent`,
also while the VM is off. "Switch Harness / Model" stops the running
harness, writes `.agent/HANDOFF.md`, and continues the task in another
harness in the same terminal. See
[docs/harness-formats.md](docs/harness-formats.md).

## Connectors

Connect GitHub, Google Drive, Notion, or Slack in the Connectors page:

- paste a personal access token or integration token; or
- configure an OAuth app (Authorization Code with PKCE): set
  `LYDIABOX_<PROVIDER>_CLIENT_ID` and `_CLIENT_SECRET`, or save the client
  in the page, and register the callback URL
  `http(s)://<host>/api/v1/connectors/<provider>/callback`.

```sh
curl -H "Authorization: Bearer $TOKEN" -d '{"token":"ghp_..."}' \
  http://localhost:8080/api/v1/connectors/github/token
```

Tokens are AES-256-GCM encrypted in the host database and refresh
automatically. In the VM, `git` gets a fresh GitHub token from
`lydiabox-guest git-cred` for each push or pull, and the harnesses use the
connector MCP server at `http://127.0.0.1:8911/mcp`. Every token issue and
tool call is in the audit log.

## Development

```sh
make build          # web UI (when npm exists), dist/lydiabox, dist/lydiabox-guest
make build GOOS=linux GOARCH=arm64
make release        # dist/linux-amd64 and dist/linux-arm64
make test           # CGO_ENABLED=0 tests, then the race detector
make lint           # go vet, gofmt, staticcheck, shellcheck
make web-build      # rebuild only the embedded web UI
make rootfs         # chv guest images
make guest-image    # docker driver image
```

All binaries build with `CGO_ENABLED=0`. Runtime binary versions and
checksums are pinned in
[internal/bootstrap/manifest.json](internal/bootstrap/manifest.json).

The web UI (Node.js 22 or later) runs with a proxy to a local
`lydiabox up` on port 8080:

```sh
cd web && npm ci && npm run dev    # http://localhost:5173
npm run typecheck && npm run lint && npm run test
```

## Status

| Area | State |
| :--- | :--- |
| Storage, File API, WebDAV | Done; tested end to end |
| Docker VM driver, guest daemon, sessions | Done; tested end to end |
| Cloud Hypervisor driver | Done; tested against a mock API, not on real KVM |
| magpie gateway, harness bridge | Done; harness formats partly inferred |
| Connectors | Done; tested against fake provider APIs |
| Virtual S3 gateway | Done, under `/s3`; tested end to end |

Details and open items: [docs/conformance.md](docs/conformance.md).

## Layout

```text
cmd/lydiabox            host CLI and control plane (internal/cli)
cmd/lydiabox-guest      guest daemon (internal/guest/cli)
internal/config         configuration, validation, secrets, data directory layout
internal/store          SQLite state database, AES-256-GCM secrets
internal/bootstrap      runtime binary downloader, guest image fetcher
internal/storage        JuiceFS supervisor, S3 metadata backup, WebDAV proxy, File API
internal/vmm            VM driver interface, auto-wake and idle controller (chv, docker)
internal/magpie         magpie supervisor, admin client, harness config wirers
internal/harness        session manager, terminal rooms, hot-switch bridge, adapters
internal/connector      OAuth, tokens, refresh, audit, guest broker
internal/desktop        desktop signaling relay and takeover mode
internal/api            HTTP server, authentication, routes
internal/guest          guest daemon: rpc, ptymanager, x11, webrtc, mcp, credproxy, gitcred
internal/webui          embedded web UI
web                     React + TypeScript web workstation
deploy                  guest initramfs and root filesystem builds
```
