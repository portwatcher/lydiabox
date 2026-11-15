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
   - Pure-Go static guest agent (`lydiabox-guest`) provides a built-in **Computer Use MCP Server** (X11 screenshot, mouse, keyboard via pure-Go `xgb/xtest`, bash, Chrome CDP, Blender Python) and **WebRTC desktop streaming** (`pion/webrtc`).
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
└── web/                            # Embedded Vite + React + TypeScript + TailwindCSS frontend
    ├── package.json
    ├── vite.config.ts
    └── src/
        ├── App.tsx
        ├── api/                    # Typed REST & WebSocket clients
        └── pages/
            ├── StudioPage.tsx      # Sessions sidebar + xterm.js terminal
            ├── DesktopPage.tsx     # WebRTC video canvas + Agent action overlay + Observer/Takeover toggle + VM power bar
            ├── FilesPage.tsx       # Offline-capable file tree, code editor, drag-drop folder upload, WebDAV/S3 mount info
            ├── ModelsPage.tsx      # Magpie provider/routing management & per-Harness model selector
            └── ConnectorsPage.tsx  # OAuth2 / PAT setup cards (GitHub, Google Drive, Notion, Slack) + audit log
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
   - `LYDIABOX_STORAGE_MODE=provider` keeps `/home/agent` and `/workspace` on the disk of the agent machine (for example a hosted machine with a persistent `/data` disk) instead of JuiceFS. Host consumers use the `internal/agentfs` seam; its guest-backed implementation calls the guest daemon file methods (`fs.*`), which sandbox every path inside configured roots and run as the agent user. Session discovery and purge run in the guest; the host caches the last discovery in the state database. WebDAV, `/s3`, and the JuiceFS metadata backup are not used; the state database backup runs only when S3 settings are present.

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

---

### 4.4 Magpie Integration & Multi-Harness Sessions (`internal/magpie` & `internal/harness`)

1. **Host Magpie Supervisor & Config Wirer**:
   - Run `magpie` on the host listening on `127.0.0.1:3425` with data dir `$DATA_DIR/state/magpie`.
   - Provision a named Magpie Gateway Key for the VM. Inside the VM, `lydiabox-guest` listens on `127.0.0.1:3425`, injects the Gateway Key header on the host side of the `vsock` tunnel, and forwards requests to Host Magpie (`127.0.0.1:3425`). Thus OpenAI Chat (`/v1/chat/completions`), OpenAI Responses (`/v1/responses`), Anthropic Messages (`/v1/messages`), and Gemini endpoints all work transparently inside the VM with zero upstream secret exposure!
   - Implement **Atomic Config Wirers** operating directly on `$DATA_DIR/mnt/jfs/home/agent/` (preserving user comments/unrelated keys):
     - **`claude-code`**: `~/.claude/settings.json` -> `env.ANTHROPIC_BASE_URL = "http://127.0.0.1:3425"`, `env.ANTHROPIC_AUTH_TOKEN = "lydiabox-local"`, `model` field, plus MCP server entries in `~/.claude.json`.
     - **`codex`**: `~/.codex/config.toml` -> custom provider pointing `base_url = "http://127.0.0.1:3425/v1"`, `model` field, and `[mcp_servers.lydiabox]`.
     - **`dsh`**: `~/.dsh/.env` / config -> `OPENAI_BASE_URL` / `ANTHROPIC_BASE_URL` / `DEEPSEEK_BASE_URL` pointing to `http://127.0.0.1:3425` and selected model mapping.
     - **`antigravity`**: `~/.antigravity/settings.json` -> gateway endpoint `http://127.0.0.1:3425`, selected model, and `~/.gemini/antigravity/mcp_config.json`.
     - **`opencode`**: `~/.config/opencode/opencode.json` -> custom provider pointing to `http://127.0.0.1:3425/v1`, selected `model`, and `mcp` section.

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
   - Base: `ubuntu:24.04`. Install `xvfb`, `xorg`, `xfce4`, `xfce4-terminal`, `dbus-x11`, `fonts-noto-cjk`, `ffmpeg`, `google-chrome-stable` (or `chromium` on `arm64`), `blender`, `git`, `gh`, `curl`, `jq`, `ripgrep`, `python3`, `nodejs`, `npm`, plus the 5 Harness CLIs (`@anthropic-ai/claude-code`, `@openai/codex`, `opencode-ai`, `dsh`, `antigravity` wrapper/installer).
   - Start `Xvfb :0 -screen 0 1920x1080x24` + `startxfce4` under user `agent` (`uid=1000`, `$HOME=/home/agent`).
2. **Pure-Go X11 Controller & Takeover Lock (`internal/guest/x11`)**:
   - Connect via `xgb.NewConnDisplay(":0")` and initialize `xtest.Init(conn)`.
   - Implement `CaptureScreen()` (`xproto.GetImage` -> `image.RGBA` -> JPEG/PNG encoder), `MoveMouse(x, y)`, `MouseButton(btn, down)`, `KeyTap(keysym)`, and `TypeText(utf8String)`.
   - Maintain atomic mode: `ModeObserver` vs `ModeTakeover`.
     - In `ModeObserver`: Guest MCP calls succeed and emit `{type: "agent_action", action, x, y, text, timestamp}` events to Host -> Browser WebSocket so the UI draws animated click ripples and keystroke pills over the video! Browser input events on WebRTC DataChannel are ignored.
     - In `ModeTakeover`: Browser DataChannel mouse/keyboard events inject directly into `xtest`; if an Agent calls `computer_action` MCP tool while in `ModeTakeover`, block up to 30s waiting for handback (or return a structured tool message: `"Desktop is currently under manual user takeover; wait or re-screenshot after user hands control back"`).
3. **WebRTC Live Stream (`internal/guest/webrtc`)**:
   - On first viewer join, spawn `ffmpeg -f x11grab -framerate 24 -video_size 1920x1080 -i :0.0 -c:v libx264 -preset ultrafast -tune zerolatency -pix_fmt yuv420p -b:v 2500k -f rtp rtp://127.0.0.1:5004` (stop `ffmpeg` automatically when viewer count drops to 0 to save CPU!).
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

Build a responsive, dark-themed workstation UI (TailwindCSS + Lucide icons + `xterm.js`) embedded into `cmd/lydiabox`:

1. **Top Global Status Bar**:
   - VM Status pill (`STOPPED`, `BOOTING`, `RUNNING`, `PAUSED`) with 1-click Wake / Hibernate / Stop buttons, CPU/RAM & S3 sync indicator (`Synced Xs ago`).
2. **Studio Tab (`/studio`)**:
   - Left pane: Unified Sessions list filterable by Harness (`claude-code`, `codex`, `dsh`, `antigravity`, `opencode`) and Workspace directory; works even when VM is `STOPPED`.
   - Center pane: Live `xterm.js` WebSocket PTY terminal attached to the active Harness session.
3. **Desktop & Computer Use Tab (`/desktop`)**:
   - Full-viewport WebRTC `<video>` player with aspect-ratio scaling (`1920x1080` coordinate mapping).
   - Live SVG overlay rendering Agent cursor position, click ripples, and recent MCP tool call feed on a collapsible right sidebar.
   - Prominent Mode Switcher: **`👁 Observer Mode`** vs **`🖱 Takeover Control`** (captures pointer/keyboard events and sends over WebRTC DataChannel) + quick-launch buttons for Chrome & Blender.
4. **Files Tab (`/files`)**:
   - Always-available file browser over `/workspace` and `/home/agent` (works with VM off!).
   - Supports directory tree navigation, inline file editor/viewer, folder drag-and-drop upload, file download, and a **"Mount Locally (WebDAV)"** modal showing endpoint & credentials.
5. **Models & Magpie Tab (`/models`)**:
   - Configure LLM Provider keys / Routing Groups via Magpie API and bind default models per Harness (`claude-code`, `codex`, `dsh`, `antigravity`, `opencode`).
6. **Connectors Tab (`/connectors`)**:
   - Cards for GitHub, Google Drive, Notion, and Slack showing connection status, OAuth "Connect" button (or "Configure Client ID / Paste Token" drawer for self-hosted setup), and real-time credential access audit logs.

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
