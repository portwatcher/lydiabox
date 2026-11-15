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
[docs/cloud-design.md](docs/cloud-design.md) is a proposal for a hosted
service (LydiaBox Cloud) on top of this core. Its first version runs each
agent VM on InstaBox ([docs/cloud-instabox.md](docs/cloud-instabox.md));
[docs/cloud-own-compute.md](docs/cloud-own-compute.md) is the later design
for KVM hosts that the service operates.

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

### Provider storage mode

With `LYDIABOX_STORAGE_MODE=provider`, `/home/agent` and `/workspace` stay
on the disk of the agent machine (for example the persistent `/data` disk
of a hosted machine) instead of a JuiceFS volume. LydiaBox then:

- does not need the S3 settings, FUSE, or `juicefs`, and does not install
  or start it;
- reaches the files through the guest daemon: the Files page, the File API,
  the harness config wirers, and the Google Drive tools wake the machine
  when it is paused or stopped (`503` after 90 s without an answer);
- shows the Studio session list from the last scan while the machine is
  paused or stopped, and marks it as cached;
- answers `/webdav` and `/s3` with `501`;
- backs up the host state database to S3 only when the S3 settings are
  present (it logs one warning at start when they are absent).

The `docker` driver keeps the trees in the named volumes
`lydiabox-<tenant>-home` and `lydiabox-<tenant>-workspace`. The `chv` driver
needs the `juicefs` mode (`auto` selects `docker`). In the guest,
`LYDIABOX_GUEST_HOME_ROOT` and `LYDIABOX_GUEST_WORKSPACE_ROOT` set the root
directories (default `/home/agent` and `/workspace`); file operations run
as the agent user (`LYDIABOX_GUEST_FS_USER`, default `agent`) when the
guest daemon runs as root.

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
| `LYDIABOX_STORAGE_MODE` | `juicefs` | `juicefs` (S3-backed host volume) or `provider` (machine disk; see above) |
| `LYDIABOX_S3_ENDPOINT` | required (`juicefs`) | S3, R2, or MinIO endpoint URL |
| `LYDIABOX_S3_BUCKET` | required (`juicefs`) | Bucket name |
| `LYDIABOX_S3_ACCESS_KEY` | required (`juicefs`) | Access key ID |
| `LYDIABOX_S3_SECRET_KEY` | required (`juicefs`) | Secret access key |
| `LYDIABOX_S3_REGION` | `auto` | Region (AWS needs the bucket region) |
| `LYDIABOX_S3_BUCKET_URL` | derived | JuiceFS `--bucket` URL override |
| `LYDIABOX_LISTEN_ADDR` | `0.0.0.0:8080` (`127.0.0.1:8080` with a tunnel) | Web UI, API, WebSocket, `/webdav`, and the S3 gateway (`/s3`) |
| `LYDIABOX_S3_GATEWAY_ADDR` | `127.0.0.1:8090` | Backend of the S3 gateway (empty or `off` disables it) |
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
| `LYDIABOX_TENANT_ID` | `default` | Tenant of this process (cell mode) |
| `LYDIABOX_MAGPIE_ADDR`, `_WEB_ADDR` | `127.0.0.1:3425`, `127.0.0.1:3426` | Host magpie gateway and its admin web API |
| `LYDIABOX_VM_SUBNET_INDEX` | `auto` | Guest subnet `172.30.<n>.0/24` (`chv`); `auto` reads it from the state database |
| `LYDIABOX_VM_TAP`, `LYDIABOX_VM_NFT_TABLE` | from the tenant ID | TAP device and nftables table (`chv`) |
| `LYDIABOX_STUN_URLS` | Google STUN | STUN URLs of the desktop stream (`none` disables) |
| `LYDIABOX_TURN_URLS` | empty | TURN URLs (`turn:`, `turn:...?transport=tcp`, `turns:`) |
| `LYDIABOX_TURN_USERNAME`, `_PASSWORD` | empty | Static TURN credentials |
| `LYDIABOX_TURN_SECRET` | empty | TURN REST shared secret (coturn); mints short-lived credentials |
| `LYDIABOX_CLOUDFLARE_TURN_KEY_ID`, `_API_TOKEN` | empty | Cloudflare Realtime TURN key; mints credentials for each viewer |
| `LYDIABOX_TURN_TTL` | `1h` | Lifetime of minted TURN credentials (1m to 48h) |
| `LYDIABOX_WEBRTC_RELAY_ONLY` | `false` | Desktop stream through TURN only |
| `LYDIABOX_CLOUDFLARE_API_TOKEN`, `_ACCOUNT_ID` | empty | Cloudflare API access of `lydiabox cloudflare` (environment only) |
| `LYDIABOX_CLOUDFLARE_TUNNEL` | `auto` | `auto` runs cloudflared when a tunnel is set up; `off` never runs it |
| `LYDIABOX_ACCESS_TEAM_DOMAIN`, `_AUD`, `_EMAILS` | stored by setup | Cloudflare Access settings; replace the stored values |
| `LYDIABOX_DOCKER_IMAGE` | `lydiabox/guest:latest` | Guest image of the `docker` driver |
| `LYDIABOX_GUEST_BINARY` | found next to `lydiabox` | Linux `lydiabox-guest` injected into the VM |
| `LYDIABOX_ROOTFS_URL` | empty | Base URL of prebuilt guest images (`chv`) |
| `LYDIABOX_GATEWAY_UPSTREAM` | `http://` + `LYDIABOX_MAGPIE_ADDR` | LLM gateway that guest traffic goes to |
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

`lydiabox up` downloads `cloud-hypervisor` and, on amd64, `virtiofsd`.
Upstream has no arm64 `virtiofsd`, so on arm64 `runtime build-rootfs` also
builds it from source with Docker (static, about one minute) and installs
it. To build it alone: `make virtiofsd`, then
`./dist/lydiabox runtime install-local virtiofsd dist/virtiofsd-linux-arm64`. The guest
boots `rootfs.squashfs` read-only with a persistent overlay
(`system/root-upper.img` on JuiceFS), so `apt install`, `/etc` changes,
and browser profiles survive a restart. `/home/agent` and `/workspace` are
shared through virtio-fs. The guest has the address `172.30.0.2/24`
behind NAT and cannot reach private networks of the host. Other tenants
get their own subnet, `172.30.<n>.0/24` (n = 1 to 254, kept in the state
database).

### Several processes on one host (cell mode)

One `lydiabox up` process serves one tenant. To run several on one host
(one "cell" for each tenant), give each process its own
`LYDIABOX_TENANT_ID`, `LYDIABOX_DATA_DIR`, and host addresses:
`LYDIABOX_LISTEN_ADDR`, `LYDIABOX_S3_GATEWAY_ADDR`, `LYDIABOX_MAGPIE_ADDR`,
`LYDIABOX_MAGPIE_WEB_ADDR`, and `LYDIABOX_WEBRTC_UDP_PORT` (or `0`). With
`chv`, also set `LYDIABOX_VM_SUBNET_INDEX`: each process has its own state
database, so `auto` gives every non-default tenant index 1. The TAP name
and the nftables table come from the tenant ID; `LYDIABOX_VM_TAP` and
`LYDIABOX_VM_NFT_TABLE` override them.

### Remote guest link

A remote VM driver reaches a guest that has no vsock and no shared socket
(a hosted machine behind an HTTPS proxy) through one WebSocket that the
host dials. Start the guest daemon with
`lydiabox-guest serve -listen ws://0.0.0.0:<port>[/path]` and the link
secret in `LYDIABOX_LINK_SECRET` (or a file in
`LYDIABOX_LINK_SECRET_FILE`, 16 bytes or more). Both ends prove the secret
before the link carries the RPC (port 9000) and, in the other direction,
the connector broker (9001) and the LLM gateway tunnel (9002). Inside the
guest, local UNIX sockets (`/run/lydiabox/host-9001.sock` and
`host-9002.sock`) bridge to the host ports. The host end is the Go package
`internal/vmm/link`; no driver in this repository uses it yet.

To boot and check the `chv` driver on a KVM host (also nested in Lima on a Mac), see [docs/kvm-test.md](docs/kvm-test.md).
For an amd64 host, see [step 9](docs/kvm-test.md#9-amd64-kvm-host); to check GPU passthrough, use the checklist in [docs/kvm-test.md](docs/kvm-test.md#10-gpu-passthrough-vfio).

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

## Desktop stream through a TURN relay

The desktop stream is WebRTC media on UDP. When browsers cannot reach
`LYDIABOX_WEBRTC_UDP_PORT` (a Cloudflare Tunnel, a host with no inbound
port), configure a TURN server. The host sends the same STUN and TURN
servers to the browser and to the guest peer, so the stream can go through
the relay from both sides. `lydiabox cloudflare setup` (see "Remote access with
Cloudflare Zero Trust") does this for you: it creates a Cloudflare Realtime TURN key, and `lydiabox up`
then relays the stream through it with no UDP port. To use another TURN
server or a key that you made yourself, set the variables below; any STUN or
TURN setting replaces the stored key. For a tunnel-only host:

```sh
LYDIABOX_CLOUDFLARE_TURN_KEY_ID=...        # or LYDIABOX_TURN_URLS with credentials
LYDIABOX_CLOUDFLARE_TURN_API_TOKEN=...
LYDIABOX_WEBRTC_RELAY_ONLY=true
LYDIABOX_WEBRTC_UDP_PORT=0
```

`turn:...?transport=tcp` and `turns:` URLs work on networks that block UDP.
Minted credentials (Cloudflare, `LYDIABOX_TURN_SECRET`) are renewed before
`LYDIABOX_TURN_TTL` ends. With `chv`, the guest cannot reach private
networks or the host, so the TURN server needs a public address.
[docs/turn-manual-test.md](docs/turn-manual-test.md) is the check with a
real TURN service.

## Files, WebDAV, and S3

In the `juicefs` storage mode, files are available while the VM is off: in
the Files page, through the REST API (`/api/v1/fs`), over S3 (below), and
over WebDAV at `http://<host>:8080/webdav/` (user `lydiabox`, password =
admin token). In the `provider` mode only the Files page and the REST API
are available, and they wake the machine. `/webdav/workspace/` and
`/webdav/home/agent/` are the two user trees.

```sh
# macOS Finder: Go > Connect to Server > http://<host>:8080/webdav/
rclone mount :webdav: ~/lydiabox --webdav-url=http://<host>:8080/webdav/ \
  --webdav-user=lydiabox --webdav-pass="$(rclone obscure "$LYDIABOX_AUTH_TOKEN")"
```

The virtual S3 gateway serves the same files on the main port. Use the
endpoint `http://<host>:8080/s3`, the bucket `lydiabox`, and path-style
addressing. The access key is `lydiabox` and the secret key is the admin
token. Object keys are `workspace/...` and `home/agent/...`.

```sh
export AWS_ACCESS_KEY_ID=lydiabox AWS_SECRET_ACCESS_KEY="$LYDIABOX_AUTH_TOKEN"
aws --endpoint-url http://<host>:8080/s3 s3 ls
aws --endpoint-url http://<host>:8080/s3 s3 ls s3://lydiabox/workspace/
aws --endpoint-url http://<host>:8080/s3 s3 cp report.pdf s3://lydiabox/workspace/
rclone lsd :s3:lydiabox/workspace --s3-provider=Minio --s3-force-path-style \
  --s3-endpoint=http://<host>:8080/s3 \
  --s3-access-key-id=lydiabox --s3-secret-access-key="$LYDIABOX_AUTH_TOKEN"
```

In an rclone configuration file, set `endpoint = http://<host>:8080/s3` and
`force_path_style = true`. The gateway process (`juicefs gateway`) listens
on `LYDIABOX_S3_GATEWAY_ADDR` (default `127.0.0.1:8090`). LydiaBox verifies
the SigV4 signature of each request at `/s3`, removes the prefix, and signs
the request again for the gateway.

Put LydiaBox behind TLS before you expose it to a network you do not
trust. The simplest way is Cloudflare Zero Trust (next section).

## Remote access with Cloudflare Zero Trust

One command connects LydiaBox to a hostname on your Cloudflare account.
Only the emails you give can sign in (Cloudflare sends a one-time PIN), and
the host needs no open port. Create an API token with the permissions in
[docs/remote-access-manual-test.md](docs/remote-access-manual-test.md#1-before-you-start),
enable Zero Trust once in the Cloudflare dashboard (pick a team name), then:

```sh
export LYDIABOX_CLOUDFLARE_API_TOKEN=... LYDIABOX_CLOUDFLARE_ACCOUNT_ID=...
lydiabox cloudflare setup -hostname agent.example.com -email me@example.com
lydiabox up                    # runs cloudflared; open https://agent.example.com
```

`setup` creates (or reuses) a Cloudflare Tunnel, a proxied DNS record, an
Access application with a policy for the emails, a service token, and a
Cloudflare Realtime TURN key for the desktop stream (`-turn=false` skips
it). It stores the tokens encrypted in the state database. Run it again to
change the emails; `lydiabox cloudflare status` shows the setup and
`lydiabox cloudflare teardown` deletes it. `lydiabox up` downloads the
pinned `cloudflared` on first use.

- With a tunnel, `lydiabox up` listens on `127.0.0.1:8080`, so that every
  request goes through Cloudflare Access. To also serve the LAN, set
  `LYDIABOX_LISTEN_ADDR=0.0.0.0:8080`; there, only the admin token
  protects LydiaBox.
- The web UI skips the token prompt for an Access login and shows the
  email. The admin token keeps working everywhere.
- The desktop stream relays through the stored TURN key (relay only, no
  UDP port), so video works through the tunnel with no extra setting.
  Cloudflare bills TURN traffic above its free monthly amount. Any
  `LYDIABOX_STUN_*` or `LYDIABOX_*TURN*` setting replaces the stored key.
- Scripts, WebDAV, and S3 clients send the service token headers that
  `setup` prints, in addition to the admin token:

  ```sh
  curl -H "CF-Access-Client-Id: $CF_ID" -H "CF-Access-Client-Secret: $CF_SECRET" \
    -H "Authorization: Bearer $LYDIABOX_AUTH_TOKEN" https://agent.example.com/api/v1/status
  rclone lsd :webdav: --webdav-url=https://agent.example.com/webdav/ --webdav-user=lydiabox \
    --webdav-pass="$(rclone obscure "$LYDIABOX_AUTH_TOKEN")" \
    --header "CF-Access-Client-Id: $CF_ID" --header "CF-Access-Client-Secret: $CF_SECRET"
  ```

- Clients that cannot send extra headers (macOS Finder, the AWS CLI) need
  `setup -bypass /webdav,/s3`. These paths then skip the Access login, and
  only the admin token protects them.
- To run cloudflared yourself, or to use an Access application that you made
  by hand, set `LYDIABOX_CLOUDFLARE_TUNNEL=off` and the
  `LYDIABOX_ACCESS_*` variables (see [.env.example](.env.example)).

## Models and sessions

`lydiabox up` runs magpie on `127.0.0.1:3425` (`LYDIABOX_MAGPIE_ADDR`). Add providers, routing
groups, and the default model of each harness in the Models page. Upstream
keys stay in `$LYDIABOX_DATA_DIR/state/magpie`. Inside the VM, harnesses
use the gateway at `http://127.0.0.1:3425` with a placeholder token; the
host side of the tunnel replaces it with a revocable gateway key.

Instead of an API key, you can sign in to a Claude subscription (for Claude
Code) or a ChatGPT subscription (for Codex) in the Subscriptions panel of
the Models page. magpie does the sign-in on the host and keeps the tokens in
the same directory; the harnesses in the VM keep the placeholder token. Read
the provider terms first: see
[docs/subscriptions-manual-test.md](docs/subscriptions-manual-test.md).

The Studio lists the sessions of all five harnesses from `/home/agent`,
also while the VM is off. See
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

To test the connectors against the real providers, use [docs/connectors-manual-test.md](docs/connectors-manual-test.md).

Tokens are AES-256-GCM encrypted in the host database and refresh
automatically. In the VM, `git` gets a fresh GitHub token from
`lydiabox-guest git-cred` for each push or pull, and the harnesses use the
connector MCP server at `http://127.0.0.1:8911/mcp`. The same server is on
the host at `http://<host>:8080/mcp/connectors` (admin token as a Bearer
token). Every token issue and tool call is in the audit log.

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
| Provider storage mode (files on the machine disk) | Done; tested against an in-process guest daemon; not yet run on a hosted machine |
| Docker VM driver, guest daemon, sessions | Done; tested end to end |
| Cloud Hypervisor driver | Done; verified on real KVM (arm64, nested KVM in Lima; amd64, nested KVM on Google Cloud; see [docs/kvm-test.md](docs/kvm-test.md)); GPU passthrough untested |
| magpie gateway, harness sessions | Done; harness formats partly inferred |
| Harness subscriptions (Claude, ChatGPT) | Done; tested against a fake magpie; real-account checklist in [docs/subscriptions-manual-test.md](docs/subscriptions-manual-test.md) |
| Connectors | Done; tested against fake provider APIs; full real-provider checklist passed on GitHub ([results](docs/conformance.md#connector-run-github-real-provider)); real-provider checklist in [docs/connectors-manual-test.md](docs/connectors-manual-test.md) |
| Virtual S3 gateway | Done, on the main port (`/s3`, SigV4 verified and re-signed); tested end to end |
| Remote access (Cloudflare Zero Trust) | Done; tested against a fake Cloudflare API, a fake JWKS, and a fake cloudflared; real-account checklist in [docs/remote-access-manual-test.md](docs/remote-access-manual-test.md) |

Details and open items: [docs/conformance.md](docs/conformance.md).

## Layout

```text
cmd/lydiabox            host CLI and control plane (internal/cli)
cmd/lydiabox-guest      guest daemon (internal/guest/cli)
internal/config         configuration, validation, secrets, data directory layout
internal/store          SQLite state database, AES-256-GCM secrets
internal/bootstrap      runtime binary downloader, guest image fetcher
internal/storage        JuiceFS supervisor, S3 metadata backup, WebDAV and S3 proxies, File API
internal/agentfs        filesystem seam of the guest trees: host mount (os.Root) or guest RPC (remotefs)
internal/vmm            VM driver interface, auto-wake and idle controller (chv, docker)
internal/magpie         magpie supervisor, admin client, harness config wirers
internal/harness        session manager, terminal rooms, adapters
internal/connector      OAuth, tokens, refresh, audit, guest broker
internal/desktop        desktop signaling relay and takeover mode
internal/api            HTTP server, authentication, routes
internal/cloudflare     Cloudflare API client, tunnel and Access setup, Access JWT verifier, cloudflared supervisor
internal/guest          guest daemon: rpc, guestfs, ptymanager, x11, webrtc, mcp, credproxy, gitcred
internal/webui          embedded web UI
web                     React + TypeScript web workstation
deploy                  guest initramfs and root filesystem builds
```
