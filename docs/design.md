# LydiaBox (`lydiabox`) — System Design & Autonomous Implementation Specification

> **Instructions for Coding Agent**:
> You are tasked with implementing **LydiaBox (`lydiabox`)**, an open-source, self-hostable, serverless-style Personal Agent VM & Harness Orchestration platform.
> Read this entire specification carefully before writing code. You must deliver a **complete, end-to-end compilable, production-grade MVP**—not architectural stubs or `TODO` placeholders. Enforce `CGO_ENABLED=0` across all Go binaries, implement all subsystems vertically, and verify every milestone with automated tests before finishing.

---

## 1. Vision & Core Architectural Pillars

**LydiaBox** decouples **Agent Execution (VM)**, **Agent Harness (Control Loop)**, **LLM Provider**, and **Persistent State (S3-backed POSIX FS)** so users retain 100% ownership of their data while enjoying an always-available personal agent workstation:

1. **Single-Binary Self-Hosted Control Plane (`CGO_ENABLED=0`)**:
   - Users launch LydiaBox with environment variables (e.g., S3 endpoint/bucket/keys) and a single static Go binary (`./lydiabox up`) that embeds the React Web UI (`go:embed`).
   - External heavy data-plane binaries (`juicefs`, `cloud-hypervisor`, `virtiofsd`, `magpie`) are automatically bootstrapped on first run into `~/.lydiabox/runtime/` and supervised as child processes.
2. **S3-Backed Persistent POSIX Filesystem + Always-On Offline Access**:
   - Powered by **JuiceFS** (`sqlite3://` local metadata engine with automatic background snapshot sync to the user's S3 bucket).
   - Users can browse, edit, upload, and download workspace & home files via the **Web UI**, **WebDAV**, or **Virtual S3 Gateway** even when the Agent VM is completely powered off (`STOPPED`).
   - Full VM persistence: `/home/agent` and `/workspace` mount via `virtio-fs`; `/` root filesystem uses `OverlayFS` (`rootfs.squashfs` read-only base + a sparse ext4 `root-upper.img` stored on JuiceFS attached via `virtio-blk`), so `sudo apt install`, `/etc` edits, Chrome login profiles, and Blender plugins persist across VM lifecycles.
3. **Multi-Harness Session Management & LLM Decoupling**:
   - First-class support for 5 Harnesses: **`claude-code` (`claude`)**, **`codex`**, **`dsh`**, **`antigravity` (`agy`)**, and **`opencode`**.
   - Integrates **`yetone/magpie`** on the Host Control Plane (`127.0.0.1:3425`) bridged into the VM via `vsock`. Upstream LLM API keys and OAuth subscriptions stay strictly on the host; Harnesses inside the VM only receive a revocable local gateway token.
   - **Session Manager**: Discovers sessions across all 5 Harnesses (even while VM is off), tracks usage, and changes bound LLMs on the fly.
4. **Zero-Trust Connectors (OAuth2 & Dynamic Credential Broker)**:
   - Built-in Connector manager for **GitHub**, **Google Drive**, **Notion**, and **Slack**. Self-hosted users configure their own OAuth `CLIENT_ID`/`CLIENT_SECRET` (or Personal Access Tokens for instant setup; also supports an optional `LYDIABOX_OAUTH_RELAY_URL` for future cloud OAuth relay).
   - Refresh tokens are AES-256-GCM encrypted in host SQLite and **never written to the VM disk**. Inside the VM, `lydiabox-guest` provides a dynamic `git-credential-helper` and a bridged **Connector MCP Server** over `vsock`.
5. **Computer Use, Desktop Apps (Chrome / Blender) & Live WebRTC Takeover**:
   - Guest OS runs a lightweight X11 desktop (`Xvfb`/`Xorg` + `Xfce4`), pre-installed with **Google Chrome** (CDP enabled), **Blender**, and standard Linux toolchains.
   - Pure-Go static guest agent (`lydiabox-guest`) provides a built-in **Computer Use MCP Server** (X11 screenshot, mouse, keyboard via pure-Go `xgb/xtest`, bash, Chrome CDP, high-level browser use, Blender Python) and its shell form `lbx` and **WebRTC desktop streaming** (`pion/webrtc`).
   - Users watch the agent operate the desktop in real time via Web UI (`OBSERVER` mode with action HUD overlay) and can click **"Takeover Desktop"** (`TAKEOVER` mode) to pause agent input and control Chrome/Blender directly with mouse and keyboard.

---

## 2. Non-Negotiable Engineering Rules

1. **Strict Zero-CGO (`CGO_ENABLED=0`)**:
   - Both `cmd/lydiabox` (host server/CLI) and `cmd/lydiabox-guest` (VM guest agent) MUST compile with `CGO_ENABLED=0`.
   - Use `modernc.org/sqlite` for SQLite on the host, `github.com/pion/webrtc/v4` for WebRTC, `github.com/jezek/xgb` for X11 capture & synthetic input, and `github.com/creack/pty` for PTY management. Never link `libsqlite3`, `libX11`, `libXtst`, or `libgstreamer` via CGO.
2. **Clean Multi-Tenant SaaS Seam**:
   - Every internal service (`storage.Provider`, `vmm.Driver`, `harness.Manager`, `connector.Broker`) MUST accept a `tenantID string` parameter (defaulting to `"default"` in self-hosted single-user mode). This ensures zero-refactor compatibility with future multi-tenant cloud deployments.
3. **Dual VMM Backend (`chv` Primary + `docker` Fallback)**:
   - Implement `internal/vmm/chv` driving **Cloud Hypervisor** + **virtiofsd** on Linux hosts with `/dev/kvm`.
   - Implement `internal/vmm/docker` running the exact same guest desktop image + `lydiabox-guest` daemon over a mounted UNIX domain socket when `/dev/kvm` is absent (macOS, Windows WSL2 without nested KVM, or CI). Auto-detect via `LYDIABOX_VM_DRIVER=auto|chv|docker`.
4. **Security Boundaries**:
   - Run `juicefs mount` and `magpie` ONLY on the trusted Host Control Plane—never inside the VM.
   - Enforce strict path-traversal sandboxing (`filepath.Clean` + root prefix verification / Go `os.Root`) on all host File Manager APIs.

---

## 3. Repository Structure

Create the following Go + TypeScript monorepo layout:

~~~text
lydiabox/
├── Makefile                        # Build, rootfs image build, lint, unit & integration tests (enforces CGO_ENABLED=0)
├── go.mod                          # Module: github.com/lydiabox/lydiabox
├── .env.example                    # Complete documented self-hosted configuration template
├── cmd/
│   ├── lydiabox/                   # Host single binary entrypoint (CLI: up, status, runtime, version)
│   │   └── main.go
│   └── lydiabox-guest/             # Static guest daemon injected into VM (/usr/local/bin/lydiabox-guest)
│       └── main.go                 # Also acts as git-credential-helper when invoked via symlink/subcommand
├── internal/
│   ├── config/                     # Env & YAML/TOML config loader + validation + auto-generated master key
│   ├── bootstrap/                  # First-run downloader & SHA256 verifier for juicefs, cloud-hypervisor, virtiofsd, magpie
│   ├── store/                      # Host SQLite state DB (modernc.org/sqlite) + AES-256-GCM secret encryption
│   ├── storage/                    # JuiceFS supervisor, SQLite meta S3 backup/restore, sparse root-upper.img init, WebDAV/S3 proxy, FS REST API
│   ├── vmm/                        # VMM interface, auto-wake/idle-hibernate controller, vsock dialer
│   │   ├── driver.go
│   │   ├── chv/                    # Cloud Hypervisor UNIX socket REST client + virtiofsd supervisor + TAP/NAT networking + optional VFIO GPU
│   │   └── docker/                 # Container fallback driver sharing identical guest protocol via bind-mounted UNIX socket
│   ├── magpie/                     # Host-side yetone/magpie process supervisor, gateway key provisioning, atomic Harness config wirer
│   ├── harness/                    # Unified Session manager and PTY runner client
│   │   ├── types.go                # SessionMeta, HarnessAdapter interface
│   │   └── adapters/               # claude.go, codex.go, dsh.go, antigravity.go, opencode.go
│   ├── connector/                  # OAuth2 state machine (GitHub, Google Drive, Notion, Slack), PAT support, MCP & git-cred broker
│   ├── desktop/                    # Host-side WebRTC signaling relay, Takeover state coordinator, action HUD broadcaster
│   ├── api/                        # HTTP/WebSocket router (net/http or chi), auth middleware, static web UI handler
│   └── guest/                      # Guest-only packages used by cmd/lydiabox-guest
│       ├── rpc/                    # Multiplexed framing over AF_VSOCK (or UNIX socket fallback)
│       ├── x11/                    # Pure-Go xgb screenshot grabber + xtest mouse/keyboard injector + Takeover mutex
│       ├── webrtc/                 # ffmpeg x11grab RTP supervisor + pion/webrtc PeerConnection & DataChannel input receiver
│       ├── mcp/                    # Guest Computer Use MCP server (screenshot, mouse/key, bash, Chrome CDP, Blender Python)
│       ├── ptymanager/             # Ring-buffered persistent PTY sessions for Harnesses & Web Terminal
│       └── credproxy/              # Local HTTP/CLI bridge forwarding LLM traffic (:3425) & git/MCP credentials over vsock to Host
├── deploy/
│   ├── initramfs/
│   │   └── init                    # POSIX shell / Go init mounting OverlayFS (rootfs.squashfs + root-upper.img) & virtiofs
│   └── rootfs/
│       ├── Dockerfile              # Builds Ubuntu 24.04 desktop rootfs (Xfce4, Xvfb, Chrome, Blender, ffmpeg, Node/Python/Git, 5 Harnesses)
│       ├── supervisord.conf        # Or lightweight Go init script starting X11, Xfce4, Chrome CDP, and lydiabox-guest
│       └── build-artifacts.sh      # Exports vmlinux, initramfs.cpio.gz, and rootfs.squashfs into ~/.lydiabox/runtime/images/
└── web/                            # Embedded Vite + React + TypeScript + TailwindCSS + daisyUI frontend
    ├── package.json
    ├── vite.config.ts
    └── src/
        ├── App.tsx
        ├── api/                    # Typed REST & WebSocket clients
        ├── components/product/     # Agent-product components on daisyUI: health labels, marks, model card, stat cells, command snippet, agent cursor/status/handoff, command palette
        └── pages/
            ├── StudioPage.tsx      # Sessions sidebar + xterm.js terminal
            ├── DesktopPage.tsx     # WebRTC video canvas + named agent cursor with trail + agent status pill + handoff control (Observer/Takeover) + VM state and power controls (only here)
            ├── FilesPage.tsx       # Offline-capable file tree, code editor, drag-drop folder upload, WebDAV/S3 mount info
            ├── ModelsPage.tsx      # Gateway/provider status, subscriptions, API keys, model cards, session usage, routing groups
            └── ConnectorsPage.tsx  # Connector setup rows (GitHub, Google Drive, Notion, Slack), remote access setup, audit log
~~~

---

## 4. Detailed Subsystem Specifications

### 4.1 Configuration & Single-Binary Startup (`internal/config` & `internal/bootstrap`)

Running `./lydiabox up` reads environment variables (and optional `lydiabox.yaml`):

| Variable | Required | Default | Description |
| :--- | :--- | :--- | :--- |
| `LYDIABOX_LISTEN_ADDR` | No | `0.0.0.0:8080` (`127.0.0.1:8080` when a Cloudflare Tunnel is set up) | Web UI, REST API, WebSocket, `/webdav`, and `/s3` port |
| `LYDIABOX_AUTH_TOKEN` | No | Auto-generated | Bearer/Cookie admin password printed on first boot if unset |
| `LYDIABOX_MASTER_KEY` | No | Auto-generated | 32-byte hex key (`0600` in `$DATA_DIR/state/master.key`) for encrypting OAuth tokens |
| `LYDIABOX_DATA_DIR` | No | `~/.lydiabox` | Host runtime, caches, SQLite metadata, and mount directory |
| `LYDIABOX_S3_ENDPOINT` | **Yes** | — | S3/R2/MinIO endpoint URL |
| `LYDIABOX_S3_REGION` | No | `auto` | S3 region |
| `LYDIABOX_S3_BUCKET` | **Yes** | — | User's S3 bucket name |
| `LYDIABOX_S3_ACCESS_KEY` | **Yes** | — | S3 Access Key ID |
| `LYDIABOX_S3_SECRET_KEY` | **Yes** | — | S3 Secret Access Key |
| `LYDIABOX_VM_DRIVER` | No | `auto` | `auto` (uses `chv` if `/dev/kvm` is accessible, else `docker`), `chv`, or `docker` |
| `LYDIABOX_VM_CPUS` | No | `4` | Guest vCPU count |
| `LYDIABOX_VM_MEM_MB` | No | `8192` | Guest RAM in MiB |
| `LYDIABOX_GPU_PCI_ADDR` | No | `""` | Optional host VFIO PCI address (e.g. `0000:01:00.0`) passed to Cloud Hypervisor for Blender GPU rendering |
| `LYDIABOX_IDLE_TIMEOUT` | No | `20m` | Auto-pause/stop VM when no active Harness PTY or Desktop stream exists (`0` disables) |
| `LYDIABOX_OAUTH_RELAY_URL`| No | `""` | Optional cloud OAuth broker URL; when empty, uses local `LYDIABOX_<PROVIDER>_CLIENT_ID` |

**Bootstrapper (`internal/bootstrap`)**:
- Checks `$DATA_DIR/runtime/bin/` for `juicefs`, `cloud-hypervisor`, `virtiofsd`, and `magpie`.
- Downloads missing binaries for `runtime.GOARCH` (`amd64` / `arm64`) from pinned official upstream GitHub release URLs (overridable via `LYDIABOX_MIRROR_*` env vars), verifies SHA256 checksums, and marks `+x`.
- Provides `lydiabox runtime build-rootfs` wrapping `deploy/rootfs/build-artifacts.sh` so users or CI can build `vmlinux`, `initramfs.cpio.gz`, and `rootfs.squashfs` locally via Docker if `LYDIABOX_ROOTFS_URL` is not supplied.

---

### 4.2 Persistent POSIX Storage & Host Disaster Recovery (`internal/storage`)

1. **Metadata Recovery & Formatting**:
   - Using AWS SDK Go v2 (`service/s3`), check `s3://$BUCKET/.lydiabox-system/tenants/$TENANT/jfs-meta.json.gz` when local `$DATA_DIR/meta/jfs.db` does not exist.
   - If remote backup exists, run `juicefs load sqlite3://$DATA_DIR/meta/jfs.db < backup` to seamlessly restore the entire POSIX filesystem on a brand-new host!
   - Otherwise, initialize with:
     ~~~bash
     juicefs format sqlite3://$DATA_DIR/meta/jfs.db lydiabox-$TENANT \
       --storage=s3 \
       --bucket=$LYDIABOX_S3_BUCKET_URL \
       --access-key=$LYDIABOX_S3_ACCESS_KEY \
       --secret-key=$LYDIABOX_S3_SECRET_KEY \
       --compress=lz4
     ~~~
2. **Host Mount & Background Meta Sync**:
   - Supervise `juicefs mount sqlite3://$DATA_DIR/meta/jfs.db $DATA_DIR/mnt/jfs --cache-dir=$DATA_DIR/cache/jfs --cache-size=10240 --writeback`.
   - Run a background ticker (every 60s when dirty, and cleanly on `SIGTERM` shutdown) executing `juicefs dump` + SQLite `VACUUM INTO` of `lydiabox.db`, uploading compressed snapshots to `s3://$BUCKET/.lydiabox-system/tenants/$TENANT/`.
3. **Directory Provisioning**:
   - Ensure `$DATA_DIR/mnt/jfs/home/agent`, `$DATA_DIR/mnt/jfs/workspace`, and `$DATA_DIR/mnt/jfs/system` exist (`uid=1000, gid=1000`).
   - If `$DATA_DIR/mnt/jfs/system/root-upper.img` does not exist, create a 40 GiB sparse file (`os.Truncate(f, 40*1024*1024*1024)`) and format with `mkfs.ext4 -F -q` so system-wide `apt` installations inside the VM persist incrementally.
4. **Offline-Capable File Access**:
   - **REST File API (`/api/v1/fs/*`)**: Directly manipulates `$DATA_DIR/mnt/jfs/{workspace,home/agent}` on the host. Supports directory listing, text/code read & save, binary streaming download, chunked/multipart directory drag-and-drop upload, rename, and delete—working identically whether the VM is `STOPPED` or `RUNNING`.
   - **WebDAV & Virtual S3 Gateway**: Supervise `juicefs webdav` (bound to localhost ephemeral port) and reverse-proxy under `/webdav/` protected by Basic Auth (`lydiabox` / `LYDIABOX_AUTH_TOKEN`).
5. **Provider Storage Mode (addition)**:
   - `LYDIABOX_STORAGE_MODE=provider` keeps `/home/agent` and `/workspace` on the disk of the agent machine (for example a hosted machine with a persistent `/data` disk) instead of JuiceFS. Host consumers use the `internal/agentfs` seam; its guest-backed implementation calls the guest daemon file methods (`fs.*`), which sandbox every path inside configured roots and run as the agent user. Session discovery and purge run in the guest; the host caches the last discovery in the state database. The host also keeps a copy of the last known events of each session in the state database (table `session_events_cache`, gzip-compressed JSON, the newest events inside 4 MiB per session; older events are cut), so that Studio shows a conversation at once while the machine is paused or stopped. This puts conversation content (prompts, replies, tool input and output, screenshots) of the sessions into the host state database, on the trusted control plane; it adds no credentials. An open session writes its copy at most once per 15 s after changes and once more when the last client leaves. After each fresh discovery a background fill reads the sessions whose copy is older than their transcript, newest first, without a wake and without a lease (at most 20 sessions and 2 minutes per run, a copy younger than 1 minute is not read again), and stops when the machine does not serve. Before the host pauses or stops the machine, a fill of at most 10 sessions and 10 s runs ahead of the filesystem sync. A delete of a session (with or without its transcript) removes its copy. In the `juicefs` mode the host reads the files itself and keeps no copy. WebDAV, `/s3`, and the JuiceFS metadata backup are not used; the state database backup runs only when S3 settings are present.

---

### 4.3 Serverless Agent VM Engine (`internal/vmm` + `deploy/`)

1. **Cloud Hypervisor Driver (`internal/vmm/chv`)**:
   - Spawn two `virtiofsd` daemons:
     - `--socket-path=$RUN_DIR/vfs-home.sock --shared-dir=$JFS/home/agent --cache=auto`
     - `--socket-path=$RUN_DIR/vfs-workspace.sock --shared-dir=$JFS/workspace --cache=auto`
   - Spawn `cloud-hypervisor --api-socket path=$RUN_DIR/chv.sock`.
   - Issue HTTP `PUT http://localhost/api/v1/vm.create` over `$RUN_DIR/chv.sock` with payload:
     - `cpus`: `{"boot_vcpus": N, "max_vcpus": N}`
     - `memory`: `{"size": Bytes, "shared": true}` (`shared: true` is REQUIRED for `virtio-fs` vhost-user memory mapping!)
     - `payload`: `{"kernel": ".../vmlinux", "initramfs": ".../initramfs.cpio.gz", "cmdline": "console=ttyS0 root=/dev/vda ro quiet"}`
     - `disks`: `[{"path": ".../rootfs.squashfs", "readonly": true}, {"path": ".../lydiabox-guest.ext4", "readonly": true}, {"path": "$JFS/system/root-upper.img", "readonly": false}]` (injecting the compiled `lydiabox-guest` binary via a tiny auto-built read-only image or virtio-fs mount so upgrading `lydiabox` automatically upgrades `lydiabox-guest` without rebuilding `rootfs.squashfs`!)
     - `fs`: `[{"tag": "lydia_home", "socket": "$RUN_DIR/vfs-home.sock", "num_queues": 2, "queue_size": 1024}, {"tag": "lydia_workspace", "socket": "$RUN_DIR/vfs-workspace.sock", "num_queues": 2, "queue_size": 1024}]`
     - `vsock`: `{"cid": 3, "socket": "$RUN_DIR/vsock.sock"}`
     - `net`: Configure TAP interface (`172.30.0.2/24`, gateway `172.30.0.1`) with host NAT masquerading for outbound internet access, blocking guest access to host private networks.
     - `devices`: Include `[{"path": "/sys/bus/pci/devices/" + gpuAddr}]` if `LYDIABOX_GPU_PCI_ADDR` is configured.
   - Boot via `PUT /api/v1/vm.boot`. Support `PUT /api/v1/vm.pause`, `PUT /api/v1/vm.resume`, and `PUT /api/v1/vm.shutdown`.
   - **Cloud Hypervisor Vsock Dialing Protocol**: To connect from Host to Guest port `P` (e.g., `9000`), dial UNIX socket `$RUN_DIR/vsock.sock`, write `"CONNECT 9000\n"`, and expect `"OK <host_port>\n"` before starting multiplexed RPC/HTTP2/yamux framing. For Guest-to-Host connections (e.g., port `9001`), listen on UNIX socket `$RUN_DIR/vsock.sock_9001`! Implement both directions accurately.
   - **Remote guest link** (added for hosted machines without vsock): `lydiabox-guest serve -listen ws://<host>:<port>` accepts one host-dialed WebSocket, both ends prove `LYDIABOX_LINK_SECRET` with HMAC, and one yamux session carries host-to-guest port `9000` and guest-to-host ports `9001`/`9002` (each stream starts with a 4-byte port). `internal/vmm/link` is the reconnecting host end for a remote `vmm.Driver`; drivers report remote behavior with `vmm.Capabilities` and `vmm.StateWatcher`.

2. **Guest Initramfs (`deploy/initramfs/init`)**:
   - Mount `/dev/vda` (`rootfs.squashfs`) to `/mnt/ro` and `/dev/vdc` (`root-upper.img`) to `/mnt/rw`.
   - `mkdir -p /mnt/rw/upper /mnt/rw/work /mnt/merged`
   - `mount -t overlay overlay -o lowerdir=/mnt/ro,upperdir=/mnt/rw/upper,workdir=/mnt/rw/work /mnt/merged`
   - Mount `virtiofs` `lydia_home` -> `/mnt/merged/home/agent` and `lydia_workspace` -> `/mnt/merged/workspace`.
   - Bind-mount injected `lydiabox-guest` to `/mnt/merged/usr/local/bin/lydiabox-guest` and `switch_root`.

3. **Auto-Wake & Scale-to-Zero Controller**:
   - Expose `EnsureRunning(ctx, tenantID)`: thread-safe singleflight boot/resume whenever a user opens the Terminal, starts a Harness session, requests a Desktop stream, or invokes a Guest MCP tool.
   - Track active leases (open WebRTC streams + running PTY processes); when leases == 0 for `LYDIABOX_IDLE_TIMEOUT`, flush filesystems (`sync`) and gracefully pause/stop the VM.
   - **Wake rule (2026-11-17):** a paused VM is woken only by a user action (the start control, opening a terminal, a new or resumed session, a file operation) or by a real demand of an agent or connector. Nothing automatic wakes it: no poll, no reconnect of a stream that the pause ended, and no reconcile. A pause from any source (the Hibernate control, another tab or device, the API, the idle pause, or the provider) ends the open terminal and desktop streams with `paused`; the pages show the hibernating state and connect again only when the VM runs. A user stop holds until an explicit start (section 7 of `docs/api.md`).

     | Request | Wakes a paused VM | Note |
     | :--- | :--- | :--- |
     | `POST /api/v1/vm/wake` (Start, Resume) | yes | Explicit start; also ends a user stop |
     | `POST /api/v1/sessions` (new or resumed session) | yes | Explicit start |
     | Terminal socket, first open (`GET /api/v1/sessions/{id}/ws`) of a session whose terminal is not attached | yes | Opening a terminal is a user action |
     | Terminal socket, automatic reconnect (`?reconnect=1`) | no | `paused`; the page attaches again when the VM runs |
     | Terminal socket of a live terminal | no | The room exists; no wake is needed |
     | Desktop socket (`GET /api/v1/desktop/ws`) | no | `paused` (or `stopped` when the VM does not run); the page opens it only while the VM runs |
     | Desktop app launch (Chrome, Blender, Terminal) | no | Sent over the desktop socket, which exists only while the VM runs |
     | File API in `provider` mode (list, stat, read, download, upload, mkdir, rename, delete) | yes | A file operation that the user clicks (open, upload, save, rename, delete, download, new folder) |
     | `GET /api/v1/fs/{root}/list?auto=1` (a client that must not wake the VM) | no | `503` while the VM does not serve. The web UI no longer sends it: the Files page lists without the mark and so starts or resumes the VM |
     | `GET /api/v1/fs/roots` | no | Last answer of the guest, or the defaults |
     | `DELETE /api/v1/sessions/{id}?purge=true` in `provider` mode | yes | Deletes the transcript in the guest |
     | `GET /api/v1/sessions/{id}/transcript` in `provider` mode | yes | The user opened a session; the guest reads its transcript |
     | `GET /api/v1/sessions/{id}/events` in `provider` mode, first read | yes | The user opened a session; the guest reads its events. The stored copy of the events is sent first (`cached: true`), so the conversation shows while the VM starts or resumes |
     | `GET /api/v1/sessions/{id}/events`, later reads (1 s or 4 s poll) | no | Skipped while the VM does not serve |
     | `POST /api/v1/sessions/{id}/input` and `/interrupt` | yes | The user sends a prompt to a running session (a terminal that was attached before a host restart is attached again) |
     | `DELETE /api/v1/sessions/{id}` without purge | no | Ends the guest terminal without a wake; the next reconcile ends it when the guest is not reachable |
     | `GET /api/v1/sessions` (Studio list, 10 s poll) | no | Guest discovery only while the VM serves; otherwise the cached result |
     | `GET /api/v1/status`, `GET /api/v1/vm/status` (5 s poll) | no | Host only |
     | `GET` and `PUT /api/v1/desktop/mode` | no | `503` while the VM does not run |
     | Models, connectors, subscriptions pages and their polls | no | Host only |
     | WebDAV and S3 (`juicefs` mode) | no | Host files |
     | Guest traffic: LLM gateway (9002), connector broker (9001) | n/a | Comes from the running guest |
     | Reconcile of terminals (VM changes to `RUNNING`, host start) | no | No-wake list and attach |
     | `instabox` driver: link reconnect after a pause | no | The link is closed at the pause and opens again only at the next boot or resume |

   - **State matrix (2026-11-18):** what the workstation shows in each state. "Top bar" is the VM pill and its controls; Models, Connectors, and Subscriptions (on the Models page) need only the host, so they work in every VM state while the cell answers.

     | State | Top bar | Studio | Desktop | Files (`provider` mode) | Models, Connectors | Automatic requests |
     | :--- | :--- | :--- | :--- | :--- | :--- | :--- |
     | `RUNNING` | `RUNNING`; Hibernate, Stop | All actions | Stream | All actions | All actions | Status 5 s, sessions 10 s, connectors 30 s, gateway 10 s |
     | Pausing (`transition: pausing`) | `PAUSING`; no controls (the tab that asked shows its busy button) | Actions start the VM after the pause | "Hibernating the VM…", stream closed | Actions resume the VM after the pause | All actions | Status 1.5 s |
     | `PAUSED` | `PAUSED`; Resume, Stop | New and resume start the VM; live terminals show "The VM is hibernating." | "The VM is hibernating", Resume | "The VM is hibernating", Resume; no list request; an action resumes the VM | All actions | As `RUNNING`; none wakes the VM |
     | Stopping (`transition: stopping`) | `STOPPING`; no controls | As pausing | "Stopping the VM…" | As pausing | All actions | Status 1.5 s |
     | `STOPPED` (user stop, held) | `STOPPED`; Start | Cached list; New and resume start the VM | "The VM is stopped", Start | "The VM is stopped", Start; no list request; change actions disabled | All actions | Status 5 s, sessions 10 s (cached) |
     | `BOOTING` | `BOOTING`; no controls; after 15 s "The VM provider is slow." | Actions wait for the start | "Starting the VM…" | Actions wait for the start | All actions | Status 1.5 s |
     | Provider cooldown (`startBlockedUntil`) | State; Start or Resume disabled, the reason in the bar and tooltip | New and resume disabled with the reason | Start or Resume disabled | Start disabled | All actions | As the state |
     | Cell stopped (`cell_stopped`) | `STOPPED`, no controls, no figures | Banner "The VM is stopped", the server's text, Open the dashboard; page dimmed and inert | Same banner; stream closed | Same banner | Same banner | Status 5 s only |
     | Cell starting (`cell_starting`) | `BOOTING`, no controls | Banner "The VM is starting. LydiaBox connects again by itself."; page inert | Same | Same | Same | Status 1.5 s only |
     | No status (network error or 5xx for 10 s) | `NO STATUS`, no controls, no figures | Banner "The VM status is not available. LydiaBox tries again by itself."; page inert | Same | Same | Same | Status 5 s only |

     A cell outage keeps the pages mounted, so unsaved text stays; the next status answer ends it, and each page loads again once. A page loaded during an outage (start screen) shows the same title and tries again every 5 s.

---

### 4.4 Magpie Integration & Multi-Harness Sessions (`internal/magpie` & `internal/harness`)

1. **Host Magpie Supervisor & Config Wirer**:
   - Run `magpie` on the host listening on `127.0.0.1:3425` with data dir `$DATA_DIR/state/magpie`.
   - Provision a named Magpie Gateway Key for the VM. Inside the VM, `lydiabox-guest` listens on `127.0.0.1:3425`, injects the Gateway Key header on the host side of the `vsock` tunnel, and forwards requests to Host Magpie (`127.0.0.1:3425`). Thus OpenAI Chat (`/v1/chat/completions`), OpenAI Responses (`/v1/responses`), Anthropic Messages (`/v1/messages`), and Gemini endpoints all work transparently inside the VM with zero upstream secret exposure!
   - Implement **Atomic Config Wirers** operating directly on `$DATA_DIR/mnt/jfs/home/agent/` (preserving user comments/unrelated keys):
     - **`claude-code`**: `~/.claude/settings.json` -> `env.ANTHROPIC_BASE_URL = "http://127.0.0.1:3425"`, `env.ANTHROPIC_AUTH_TOKEN = "lydiabox-local"`, `model` field, `skipDangerousModePermissionPrompt`, plus MCP server entries in `~/.claude.json`.
     - **`codex`**: `~/.codex/config.toml` -> custom provider pointing `base_url = "http://127.0.0.1:3425/v1"`, `model` field, and `[mcp_servers.lydiabox]` with `default_tools_approval_mode = "approve"`.
     - **`dsh`**: `~/.dsh/lydiabox.env` (exported by the launcher; dsh refuses base URLs in its own `~/.dsh/.env`) -> `OPENAI_BASE_URL` / `ANTHROPIC_BASE_URL` / `DEEPSEEK_BASE_URL` pointing to `http://127.0.0.1:3425` and `DSH_MODEL`; `~/.dsh/lydiabox.patch.yml` (a per-launch overlay, `dsh <profile> --patch`) routes the profile model rows to the gateway with that model.
     - **`antigravity`**: `~/.antigravity/settings.json` -> gateway endpoint `http://127.0.0.1:3425`, selected model, and `~/.gemini/antigravity/mcp_config.json`.
     - **`opencode`**: `~/.config/opencode/opencode.json` -> custom provider pointing to `http://127.0.0.1:3425/v1`, selected `model`, and `mcp` section.
   - Launch every harness in its full-access mode (new, resumed, and model-change restarts): the VM is the agent's own computer and the security boundary, so no command or MCP tool call waits for an approval, and shell commands run outside the harness sandbox (they reach `127.0.0.1` services such as Chrome CDP `9222` and the internet). `claude --dangerously-skip-permissions`, `codex --dangerously-bypass-approvals-and-sandbox`, `agy --dangerously-skip-permissions`, `opencode --auto`, and `DSH_PERMISSION_MODE=danger-full-access` for `dsh` (`docs/harness-formats.md`).

2. **Cross-Harness Session Discovery (`internal/harness`)**:
   - Define `HarnessAdapter`:
     ~~~go
     type HarnessAdapter interface {
         Name() string // "claude-code" | "codex" | "dsh" | "antigravity" | "opencode"
         DiscoverSessions(homeDir string) ([]SessionMeta, error)
     }
     ~~~

---

### 4.5 Computer Use, Desktop Streaming & Takeover (`internal/guest/*`)

1. **Guest Desktop Stack (`deploy/rootfs/Dockerfile`)**:
   - Base: `ubuntu:24.04` (build argument `DESKTOP_IMAGE`; the InstaBox image uses `debian:trixie-slim`, the release of the InstaBox machine template, see [instabox.md](instabox.md#image)). Install `xvfb`, `xorg`, `xfce4`, `xfce4-terminal`, `dbus-x11`, `fonts-noto-cjk`, `ffmpeg`, `google-chrome-stable` (or `chromium` on `arm64`), `blender`, `git`, `gh`, `curl`, `jq`, `ripgrep`, `python3`, `nodejs`, `npm`, `bubblewrap` (Codex checks for `bwrap` on PATH at each start), plus the 5 Harness CLIs (`@anthropic-ai/claude-code`, `@openai/codex`, `opencode-ai`, `dsh`, `antigravity` wrapper/installer).
   - Start `Xvfb :0 -screen 0 1920x1080x24` + `startxfce4` under user `agent` (`uid=1000`, `$HOME=/home/agent`).
2. **Pure-Go X11 Controller & Takeover Lock (`internal/guest/x11`)**:
   - Connect via `xgb.NewConnDisplay(":0")` and initialize `xtest.Init(conn)`.
   - Implement `CaptureScreen()` (`xproto.GetImage` -> `image.RGBA` -> JPEG/PNG encoder), `MoveMouse(x, y)`, `MouseButton(btn, down)`, `KeyTap(keysym)`, and `TypeText(utf8String)`.
   - Maintain atomic mode: `ModeObserver` vs `ModeTakeover`.
     - In `ModeObserver`: Guest MCP calls succeed and emit `{type: "agent_action", action, x, y, text, timestamp}` events to Host -> Browser WebSocket so the UI draws animated click ripples and keystroke pills over the video! Browser input events on WebRTC DataChannel are ignored.
     - In `ModeTakeover`: Browser DataChannel mouse/keyboard events inject directly into `xtest`; if an Agent calls `computer_action` MCP tool while in `ModeTakeover`, block up to 30s waiting for handback (or return a structured tool message: `"Desktop is currently under manual user takeover; wait or re-screenshot after user hands control back"`).
3. **WebRTC Live Stream (`internal/guest/webrtc`)**:
   - On first viewer join, spawn `ffmpeg -hide_banner -nostats -f x11grab -framerate 24 -video_size 1920x1080 -i :0.0 -c:v libx264 -preset ultrafast -tune zerolatency -pix_fmt yuv420p -b:v 2500k -g 48 -f rtp rtp://127.0.0.1:5004?pkt_size=1100` (stop `ffmpeg` automatically when viewer count drops to 0 to save CPU!). `pkt_size=1100` keeps each relayed packet (1100 RTP + 16 SRTP + 40 TURN + 8 UDP + 40 IPv6 = 1204 bytes) below a 1280-byte path MTU, because paths drop IP fragments; `-g 48` gives a keyframe every 2 seconds, because the guest cannot make ffmpeg send one on a picture loss request.
   - Read RTP packets on `127.0.0.1:5004` and write to a shared `webrtc.TrackLocalStaticRTP` (`video/H264`) served to browser `RTCPeerConnection`s signaled via Host WebSocket `/api/v1/desktop/ws`.
4. **Guest Computer Use MCP Server (`internal/guest/mcp`)**:
   - Implement JSON-RPC 2.0 MCP protocol (`initialize`, `tools/list`, `tools/call`) on `http://127.0.0.1:8910/mcp` with tools:
     - `computer_screenshot`
     - `computer_mouse_click` (`x`, `y`, `button`, `double`)
     - `computer_mouse_move` / `computer_mouse_drag` / `computer_scroll`
     - `computer_keyboard` (`text`, `key_combo`)
     - `desktop_launch_app` (`chrome`, `blender`, `terminal`)
     - `browser_cdp_endpoint` (returns running Chrome `--remote-debugging-port=9222` WebSocket URL; auto-starts Chrome with `--user-data-dir=/home/agent/.config/google-chrome` if not running)
     - `blender_run_script` (executes Python code in Blender)
     - High-level browser tools (addition, `internal/guest/browser`): `browser_open` (`url`, `new_tab`), `browser_back`, `browser_forward`, `browser_tabs` (`list`, `select`, `close`), `browser_snapshot`, `browser_click` (`ref`), `browser_type` (`ref`, `text`, `clear`, `submit`), `browser_select` (`ref`, `values`), `browser_press` (`key`, `ref`), `browser_scroll` (`ref` or `dx`/`dy`), `browser_text`, `browser_eval`, `browser_screenshot` (`full_page`), `browser_wait` (`load`, `selector`, `text`, `url`).
5. **Browser Use (`internal/guest/browser`, addition)**:
   - One pure-Go implementation drives the visible desktop Chrome that `browser_cdp_endpoint` starts, so the user can watch every step in the stream and take over. A small own CDP client on `github.com/coder/websocket` (already a dependency) is used instead of `chromedp`, which adds the large generated `cdproto` packages and a second WebSocket library for the 15 CDP methods that the tools need. One browser-level WebSocket reaches every tab through flattened sessions (`Target.attachToTarget` with `flatten`).
   - `browser_snapshot` lists the headings and the interactive elements of the current tab in document order, one line each, for example `[e5] link "Learn more" -> https://...` or `[e7] textbox "Search" value="..."`. A ref stays the same for the same element in every snapshot of a document (a `WeakMap` in the page); a new document starts again at `e1`. Clicks, typing, and key presses are real input events (`Input.dispatchMouseEvent`, `Input.insertText`, `Input.dispatchKeyEvent`), not DOM calls.
   - The current tab is the tab the agent selected while it is on screen, else the tab the user sees (the agent follows a tab switch made during a takeover). A click that opens a tab makes the new tab current; a click or a key press that starts a navigation waits for the load (10 s).
   - Actions that change the page go through `x11.Controller.Act`: they wait up to 30 s while the user holds the desktop (then return the takeover message), never interleave with other agent input, and emit `agent_action` events (`click` at the screen point of the element, `type` with masked passwords, `key`, `scroll`, `launch` with the URL). Snapshots, text, screenshots, and the tab list are allowed during a takeover.
6. **Shell Form and Discovery (addition)**:
   - `lbx` (a link to `lydiabox-guest`, also `lydiabox-guest browser|computer ...`) calls the tools of the running guest MCP server, so harnesses without MCP support get the same browser session, refs, takeover rules, and events: `lbx browser open|snapshot|click|type|fill|select|press|scroll|text|eval|screenshot|wait|tabs|tab|close|new|back|forward`, `lbx computer screenshot|click|move|drag|type|key|scroll|launch`, `lbx tools`, and `lbx call <tool> <json>`. Screenshots are written to a PNG file and the path is printed. `LYDIABOX_MCP_URL` overrides the endpoint.
   - Every harness wirer (`internal/magpie/wire`) keeps a marked block (`<!-- lydiabox:begin ... -->` to `<!-- lydiabox:end -->`) in the global instruction file of its harness: `~/.claude/CLAUDE.md`, `~/.codex/AGENTS.md`, `~/.config/opencode/AGENTS.md`, `~/.dsh/AGENTS.md`, and `~/.gemini/GEMINI.md` (Antigravity). The block names the `lbx` commands and the MCP tools. It is replaced in place on each harness start; text outside the markers stays.

---

### 4.6 Zero-Trust Connectors (`internal/connector`)

- Implement OAuth2 Authorization Code + PKCE flows (`/api/v1/connectors/:provider/authorize` & `/api/v1/connectors/:provider/callback`) AND direct Token/PAT binding (`POST /api/v1/connectors/:provider/token`) for:
  - **GitHub** (`repo`, `read:org`, `workflow`)
  - **Google Drive** (`drive.file` / `drive.readonly`)
  - **Notion** & **Slack**
- Store encrypted credentials in host SQLite table `connector_accounts`. Automatically refresh expired OAuth2 `access_token`s using stored `refresh_token`.
- **In-VM Consumption (Zero Disk Persistence)**:
  1. `git` global config inside VM sets `credential.helper = /usr/local/bin/lydiabox-guest git-cred`. When `git push`/`pull` runs for `github.com`, `lydiabox-guest` requests a fresh token over `vsock` port `9001` from the Host Broker and prints `username=x-access-token\npassword=<token>\n` to stdout.
  2. Host Connector MCP Server (`/mcp/connectors` proxied into VM `127.0.0.1:8911/mcp`) exposes `github_*`, `gdrive_search`, `gdrive_download_to_workspace`, `gdrive_upload_from_workspace`, `notion_*`, and `slack_*` tools to all 5 Harnesses.

---

## 5. Unified Web UI Specification (`web/`)

Build a responsive, dark-themed workstation UI (TailwindCSS + daisyUI + Lucide icons + `xterm.js`) embedded into `cmd/lydiabox`:

1. **Top Global Status Bar**:
   - S3 sync indicator (`Synced Xs ago`), the reason when the VM cannot run, a slow-provider notice, and "Starting the VM…" (or "Hibernating" / "Stopping") while a transition runs. The VM state pill, CPU/RAM, and the Start (Resume when paused) / Hibernate / Stop buttons are in the Desktop toolbar only. The sync indicator shows only when the server syncs to S3 (always in the `juicefs` storage mode; in the `provider` mode only with S3 settings for the state database backup).
   - Widths (2026-11-18): every page tab stays in view from 320 px up. The bar keeps one row while everything fits; then the VM group (status lines, pill, power buttons, account) wraps as a whole to a second row. Below 640 px the five tabs fill the first row, each as wide as its label needs, with the icon above the label; from 640 px to 1024 px the tab icons are hidden. The tabs never scroll out of view.
   - Status outage (2026-11-18): the pages do not show the last state as a fact when the status poll fails. A `503` with `code: "cell_stopped"` (LydiaBox Cloud stopped the cell process; also the older text "the agent computer is not running") shows the VM as stopped at once, with the server's text and its dashboard link; `code: "cell_starting"` (or "the agent computer is starting") shows it as starting. A network error or another `5xx` that lasts 10 s shows "NO STATUS" and "The VM status is not available". During an outage no power control shows, and the Desktop page closes its stream and offers no start. The next status answer ends the outage.
   - On 2026-11-19 the owner moved the VM power controls (state pill, Start or Resume / Hibernate / Stop) from the status bar to the Desktop page (they were in the status bar from 2026-11-17): LydiaBox is a personal agent, and the user works with sessions, not with the VM. Pages start or resume the VM when they need it: a new or continued session, the history of a session (`provider` mode), and the Files page (its lists start or resume the VM). The Desktop page starts a stopped VM or resumes a paused one from its own "Start" or "Resume" action. When a page needs a VM that the user stopped, its error view offers Start. The verbs are the same on every workstation page (2026-11-18): "Start" for a stopped VM, "Resume" for a paused VM; "Start desktop" shows only when the VM runs and only the desktop stream is off. A stop by the user holds until such an explicit start: the desktop stream, a terminal reconnect, and file requests do not start the VM again, also after a restart of the host process (the hold is in the state database; `docs/api.md` section 7). Hibernate has no confirm step because it ends no process: the guest keeps its memory, and a live terminal attaches again to the same process after the wake.
2. **Studio Tab (`/studio`)**:
   - New session chooses the harness and the model. The dialog preselects the harness of the last start in this browser and the last model of that harness (its binding, which every start sets). A session keeps its harness; the model menu in the session header changes its model (a running harness resumes the same session with the new model). The Models page has no per-harness default model table (owner, 2026-11-19).
   - Left pane: Unified Sessions list filterable by Harness (`claude-code`, `codex`, `dsh`, `antigravity`, `opencode`) and Workspace directory; works even when VM is `STOPPED`.
   - Center pane (2026-11-19): a harness session opens in the chat view; a Chat / Terminal switch in the session header shows the live `xterm.js` WebSocket PTY terminal of the same session (shell sessions show only the terminal). New sessions from the web UI run with the ACP engine (owner, 2026-11-19: use the Agent Client Protocol, a common protocol for all five harnesses): the guest runs `lydiabox-guest acp`, an ACP client that starts the harness's ACP agent (`claude-agent-acp`, `codex-acp`, `opencode acp`, `dsh acp`; Antigravity stays on the terminal engine because `agy acp` cannot use the gateway model) and talks to the host in JSON lines over a guest terminal in raw mode, so the terminal lifecycle (lease, pause and attach, reconcile, stop) is the same as for a TUI. The bridge selects the full-access mode of each agent, passes the MCP servers through ACP, streams `session/update` as session events, and turns `session/request_permission` into an approval card in the chat. On a live session the switch changes the engine: it ends the bridge or the TUI and resumes the same native session in the other one (only one front end runs a session at a time). The terminal engine keeps the transcript-tail view below: the chat view reads the typed events of the session (`GET /api/v1/sessions/{id}/events`, built from the native transcript of each harness, `docs/harness-formats.md`), types prompts into the terminal (`POST …/input`, bracketed paste and a delayed Enter), and stops a turn (`POST …/interrupt`, Escape). It shows the turns (prompt, reasoning, tool calls with input, output, diff and screenshots, plan, files touched, totals per turn), the agent state (working, ready, needs input, needs setup, not running; the state comes from the terminal output), the context meter, starter prompts for an empty session, dictation (browser speech recognition, where present), and three handoffs: an approval prompt on the terminal screen offers the terminal; a first-run setup screen before the first message (the Antigravity color scheme and terms, folder trust, sign-in method; LydiaBox never answers it for the user) replaces the starter prompts with an "Open terminal" button, names the Antigravity steps, and holds a sent message until the harness is ready; and computer or browser use offers the Desktop takeover. A session without a live terminal shows the same view without the prompt field, with "Continue session". The components are in `web/src/components/agent/`; their design follows the uiarc agent components (Pro license: re-implemented from the published descriptions with daisyUI, no source copied).
3. **Desktop & Computer Use Tab (`/desktop`)**:
   - Full-viewport WebRTC `<video>` player with aspect-ratio scaling (`1920x1080` coordinate mapping).
   - Live SVG overlay rendering the agent cursor (a named tag and a fading trail of its recent path), click ripples, and keystroke pills; the recent tool call feed is a timeline in a collapsible right sidebar.
   - Agent status pill in the toolbar (2026-11-19): the current action, the run time, and the step count while the agent works; "Watching" when it is idle; "You have control" during a takeover. It opens and closes the activity sidebar.
   - Handoff control (2026-11-19) for **Observer** vs **Takeover** (captures pointer/keyboard events and sends over WebRTC DataChannel): a track shows who has control (agent or you), and one button says "Take over" or "Hand back to the agent". Quick-launch buttons for Chrome, Blender, and Terminal.
4. **Files Tab (`/files`)**:
   - Always-available file browser over `/workspace` and `/home/agent` (works with VM off!).
   - Supports directory tree navigation, inline file editor/viewer, folder drag-and-drop upload, file download, and a **"Mount Locally (WebDAV)"** modal showing endpoint & credentials.
5. **Models & Magpie Tab (`/models`)**:
   - Configure LLM Provider keys / Routing Groups via Magpie API. From top to bottom (2026-11-19): a status list (the gateway, every signed-in subscription, and every API-key provider, with its health and model count; an error opens in place); the subscription cards; the API keys (masked key state, health, routing switch, rotate the key in place, edit, delete); model cards grouped by provider (context window, the harnesses whose last session used the model, its routing groups, copy the model ID); the token use and cost that sessions report, by model (hidden when no session reports usage); the routing groups. The cards show the bindings and do not change them.
6. **Connectors Tab (`/connectors`)**:
   - One row each for GitHub, Google Drive, Notion, and Slack (2026-11-19): the account line, and the action that moves the connector forward (OAuth "Connect", "Paste token", or "Set up" for a missing client ID). A failed OAuth sign-in stays on its row with "Try again". An open row lists the accounts with their scopes, an in-place disconnect confirmation, and "Activity", which filters the real-time credential access audit log to that connector.
   - Remote access (`GET /api/v1/remote-access`): the hostname, allowed emails, tunnel state, and desktop relay when it is on; the `lydiabox cloudflare setup` steps with copy buttons when it is off. A workstation signed in through Cloudflare Access does not show the setup steps.
7. **Command palette** (2026-11-19): Cmd+K (macOS) or Ctrl+K, or the search button in the status bar, opens a palette of the pages, the recent sessions, and the VM action that fits its state (Start, Resume, or Hibernate). The key does nothing inside a terminal (Ctrl+K edits the shell line) or while the remote desktop holds the keyboard.

---

## 6. Phased Execution Plan & Verification Gates

Implement the repository sequentially in the following 6 phases. Do NOT skip verification steps:

- **Phase 1 — Scaffolding, Config, Encrypted Store & Storage Engine**:
  - Initialize `go.mod`, `Makefile` (with `CGO_ENABLED=0` hard flag), `internal/config`, `internal/store` (SQLite + AES-256-GCM), `internal/bootstrap`, and `internal/storage` (JuiceFS supervisor + S3 metadata dump/restore + offline `/api/v1/fs/*` REST handlers + WebDAV proxy).
  - *Verification*: Write unit & integration tests verifying path-traversal rejection, AES-256-GCM round-trips, and full CRUD/upload/download on the File API without any VM running.
- **Phase 2 — Guest Daemon (`cmd/lydiabox-guest`) & VMM Drivers (`chv` + `docker`)**:
  - Implement `internal/guest/rpc` (`vsock` + UNIX socket framing), `ptymanager`, `x11` (`xgb` capture & `xtest` input + Takeover mutex), `webrtc` (`pion/webrtc` + RTP), `mcp` (Computer Use tools), and `credproxy`.
  - Implement `internal/vmm/chv` (Cloud Hypervisor UNIX socket REST client + `virtiofsd` + `vsock` `CONNECT` protocol), `internal/vmm/docker` fallback, auto-wake/idle-hibernate controller, `deploy/initramfs/init`, and `deploy/rootfs/Dockerfile`.
  - *Verification*: Compile both `cmd/lydiabox` and `cmd/lydiabox-guest` with `CGO_ENABLED=0` for `linux/amd64` and `linux/arm64`. Test `chv` JSON payload generation & mock UNIX socket lifecycle (`vm.create`, `vm.boot`, `vm.pause`, `vsock` handshake) plus `guest/rpc` PTY + MCP round-trip over UNIX sockets.
- **Phase 3 — Magpie Gateway Integration & 5-Harness Sessions**:
  - Implement `internal/magpie` supervisor & non-destructive config wirers for `claude-code`, `codex`, `dsh`, `antigravity`, and `opencode`.
  - Implement all 5 `HarnessAdapter`s (`DiscoverSessions`, launch and resume commands, transcript purge).
  - *Verification*: Table-driven tests with realistic sample session logs for all 5 Harnesses proving lossless discovery and config wiring.
- **Phase 4 — Zero-Trust Connectors & Dynamic Credential Broker**:
  - Implement OAuth2 PKCE + PAT flows, token auto-refresh, `vsock` `git-cred` subcommand handler, and Host Connector MCP server (GitHub + Google Drive file sync).
  - *Verification*: End-to-end test invoking `lydiabox-guest git-cred get` over simulated `vsock` and verifying encrypted host token retrieval without writing secrets to guest disk.
- **Phase 5 — React Web UI & Single-Binary Embedding**:
  - Build `web/` (`StudioPage`, `DesktopPage`, `FilesPage`, `ModelsPage`, `ConnectorsPage`), run `npm run build`, and embed `web/dist` into `cmd/lydiabox` via `embed.FS`.
- **Phase 6 — Final End-to-End Integration & Polish**:
  - Add `.env.example` and `README.md` documenting 1-command self-hosted deployment (`./lydiabox up`), Cloud Hypervisor KVM requirements, GPU passthrough setup for Blender, and custom rootfs building (`./lydiabox runtime build-rootfs`).
  - Run `CGO_ENABLED=0 go test -race ./...` and `make build` ensuring zero warnings, zero race conditions, and clean static binaries in `dist/lydiabox` and `dist/lydiabox-guest`.
