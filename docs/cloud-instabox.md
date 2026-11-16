# LydiaBox Cloud on InstaBox: supplier facts and mapping

Status: proposal (2026-11-15). The `instabox` driver of section 3
(`internal/vmm/instabox`) is implemented and tested against a fake
InstaBox API, not against the real service. Read it with
[cloud-design.md](cloud-design.md), which gives the decision, the
architecture, and the phases.

## 0. Sources and rules

We read these InstaBox pages on 2026-11-15. Each InstaBox fact in this
document has a source. **Not stated** marks a fact that the pages do not
give. **Inferred** marks a conclusion that the pages suggest but do not
state. InstaBox changes often: read the pages again before each phase.

| Label | Page |
| :--- | :--- |
| limits | https://instabox.sh/docs/account/limits.md |
| pricing | https://instabox.sh/docs/account/pricing.md |
| auto-pause | https://instabox.sh/docs/machines/auto-pause.md |
| lifecycle | https://instabox.sh/docs/machines/lifecycle.md |
| storage | https://instabox.sh/docs/machines/storage.md |
| files | https://instabox.sh/docs/machines/files.md |
| images | https://instabox.sh/docs/machines/custom-images.md |
| snapshots | https://instabox.sh/docs/machines/snapshots.md |
| ports | https://instabox.sh/docs/access/ports.md |
| desktop | https://instabox.sh/docs/access/desktop.md |
| api | https://instabox.sh/docs/api-reference/introduction.md |
| webhooks | https://instabox.sh/docs/integrations/webhooks.md |
| environment | https://instabox.sh/docs/machines/environment.md |
| secrets | https://instabox.sh/docs/security/secrets.md |
| sandbox | https://instabox.sh/docs/reference/typescript/sandbox.md |
| concepts | https://instabox.sh/docs/concepts.md |
| full | https://instabox.sh/llms-full.txt (all pages in one file) |
| openapi | https://api.instabox.sh/v1/openapi.json |

## 1. InstaBox facts used in this design

| Fact | Source |
| :--- | :--- |
| A machine is a Firecracker microVM with its own kernel (6.18.60), Debian 13 `amd64`, systemd | limits, concepts |
| Default size 4 vCPU and 8 GiB. Pro allows 2/4, 4/8, 8/16; Builder up to 16/32; Team up to 32/64 | limits, openapi (`POST /v1/machines`) |
| One persistent ext4 disk at `/data`, 20 GiB by default; home is `/data/home`. Paid plans resize it (Pro: up to 200 GiB) | limits |
| Files under `/data` and the installed software stay across pause and stop | concepts, lifecycle |
| No `/dev/kvm` in the guest; no nested VMs | sandbox |
| States: `creating`, `starting`, `running`, `stopping`, `stopped`, `suspending`, `suspended`, `resuming`, `deleting`, `deleted`, `error` | openapi (`MachineState`) |
| Lifecycle calls return `202` with an operation; a machine runs one operation at a time; `Idempotency-Key` is required and kept for 7 days | api, lifecycle |
| `no_capacity` is retryable; `409 operation_conflict` names the running operation | api |
| Pause keeps memory and processes. A paused machine cannot be stopped. If a resume cannot bring memory back, the machine starts fresh with its files | lifecycle |
| A stop asks the guest to power off; `timeoutSeconds` sets the wait before a forced stop | lifecycle, openapi (`StopMachineRequest`) |
| Create takes `name` (lowercase, unique in the org), `snapshotId`, `vcpus`, `memMiB`, `metadata`, `autoSuspendAfterSeconds`, and `desiredState` (`running` or `stopped`) | openapi (`CreateMachineRequest`) |
| Delete needs `X-Confirm-Delete` with the ID or name; snapshots of the machine stay | lifecycle, openapi |
| Custom image import makes a snapshot from a `linux/amd64` OCI image; `ENTRYPOINT` and `CMD` run as `instabox-image.service`; systemd, sshd, the UID 1000 user, and `/data` stay. Verified on 2026-11-16: the machine's user database stays, so UID 1000 has the account's name (not `agent`) and the home `/data/home` ([instabox.md](instabox.md#verified-on-a-real-machine-2026-11-16)) | images |
| A snapshot holds the installed software and `/data`; a running machine can be saved live; a paused one is refused with `409`; restore in place replaces root and `/data` | snapshots |
| Snapshots are free | pricing |
| A private port answers only requests with its token: `Authorization: Bearer ibp_...` from a script, or a cookie that the link sets in a browser. The cookie also admits WebSockets. The token is shown once | ports |
| A request without the token gets `401` and does not wake the machine | ports, auto-pause |
| Ports 1024 to 65535 can be published, 16 for each machine; 5901 is reserved for the desktop | ports, limits |
| Raw TCP ports are a preview for admins only | ports |
| Default idle pause after 30 minutes; paid plans set 5 to 30 minutes or off | auto-pause, limits |
| A request that wakes a self-paused machine waits up to 45 seconds for the resume | auto-pause |
| A machine that the client paused or stopped does not wake on use | auto-pause, lifecycle |
| Idle means: no coding agent works, no terminal or SSH traffic, no CPU load. An exec, a file read, a port request, or a lease in progress keeps it busy | auto-pause |
| Activity leases: `PUT /v1/machines/{id}/activity-leases/{taskID}`, TTL 30 to 3600 seconds, at most 128; an explicit lifecycle operation cancels them | auto-pause, openapi |
| File routes wake a self-paused machine; a stopped machine, or one that the client paused, answers `409 machine_not_running` | files |
| `exec`: at most 32 at once for each workspace, shared with file reads, the desktop, and port forwards; 10 minutes for each command | limits |
| VNC over a WebSocket at `GET /v1/machines/{id}/vnc` with a `machines:write` key; it does not wake the machine | desktop, auto-pause |
| Each user has at most 8 open terminals and desktops; a workspace key counts as the owner | desktop |
| Desktop stream links need the personal key of the machine creator | desktop |
| Outbound traffic of a machine: about 50 Mbit/s; inbound is not limited | limits |
| Port 25 is closed; 587 and 465 are open on paid plans | full (Limits section) |
| Every process in a machine can read its environment variables | environment |
| Webhooks: `sandbox.created`, `.started`, `.stopped`, `.suspended`, `.error`, `.deleted`, snapshot events; HMAC-signed; retried | webhooks |
| After a stop or pause, the disk uploads to object storage in about 10 seconds (no bound); `storage.synced` shows it | storage |
| Data stays until deleted. A disabled workspace keeps it 30 days; a deleted workspace loses it at once | storage |
| No region field; the platform places machines | storage |
| The InstaBox export API works only for a paused or disabled workspace | openapi (`POST /v1/orgs/{id}/exports`) |

## 2. Guest image

1. Build the guest root filesystem ([deploy/rootfs/Dockerfile](../deploy/rootfs/Dockerfile))
   as a `linux/amd64` OCI image. Push it to a private registry.
2. Import it with `instabox snapshot import`. The registry password is
   not kept in the snapshot (images).
3. The `ENTRYPOINT` starts `lydiabox-guest` as `instabox-image.service`.
   systemd restarts it when it fails (images).
4. Keep the agent home and the workspace on `/data`: the image
   ([deploy/instabox](../deploy/instabox), [instabox.md](instabox.md)) keeps
   the guest paths `/home/agent` and `/workspace` as links to
   `/data/lydiabox/home` (LydiaBox's own home) and `/data/workspace`.
   InstaBox's own HOME, `/data/home`, has the template's harness files
   (MCP servers, skills, `AGENTS.md`) and LydiaBox does not use it. Only
   `/data` is the user disk. The image runs its programs as UID 1000 by
   number, never by a user name, and gives them a defined environment
   ([instabox.md](instabox.md#supplier-files-and-environment)).
5. The guest needs no `/dev/kvm`: the InstaBox machine is the agent VM.

Guest binary upgrades: the driver compares the version of the running
guest with `LYDIABOX_GUEST_BINARY`. If they differ, it uploads the binary
to `/data/.lydiabox/bin/lydiabox-guest` (`PUT /v1/machines/{id}/files`),
which the image runs instead of its own binary, and restarts the program
through `exec` ([instabox.md](instabox.md#run-time-layout)). The machine user has
passwordless `sudo` (limits).

Root filesystem upgrades: **not stated** how to replace the root disk of
an existing machine and keep `/data`. A restore replaces `/data` too
(snapshots). Until InstaBox gives a way, change the root through `exec`
(for example `apt-get`), or make a new machine from the new snapshot and
move the data with the backup of
[§6.2](#62-mitigations).

## 3. `vmm.Driver` mapping

The interface is in [internal/vmm/driver.go](../internal/vmm/driver.go);
the driver is [internal/vmm/instabox](../internal/vmm/instabox).
`<id>` is the InstaBox machine ID. Each lifecycle call sends an
`Idempotency-Key` and follows the operation with `GET /v1/operations/{id}`
until it is `succeeded` or `failed`. The cell keeps a record of the machine
in the state database (`vm_machines`): the machine ID, the name, a pending
create key, the link URL, and the hash of the guest settings; the link
secret and the port token are encrypted with the master key.

| Method | InstaBox calls | Notes |
| :--- | :--- | :--- |
| `Name` | none | Returns `"instabox"`. |
| `Create` | `GET /v1/machines/{id}` for a recorded machine; else `GET /v1/machines?metadata=lydiabox_tenant:<tenant>` (adopt it); else `POST /v1/machines` with `name`, `snapshotId`, `vcpus`, `memMiB`, `metadata`, `autoSuspendAfterSeconds`, and `desiredState: running` | The name is `lbx-` and 16 hex characters of a hash of the tenant ID. The `Idempotency-Key` is saved in the record before the call, so that a restart after a crash replays the same create; the machine ID is saved from the answer. A recorded machine that is gone is an error (`ErrMachineGone`); the driver never makes a new machine in its place. `Spec.Kernel`, `Initramfs`, `Rootfs`, `RootUpper`, `HomeDir`, `WorkspaceDir`, and `GPUPCIAddr` are not used. No machine `env`: the settings go in a file (below). |
| `Boot` | `GET /v1/machines/{id}`; `POST .../start` (stopped or error), `.../resume` (paused by a client), or `.../wake` (self-paused); then the attach steps below | Waits for a transition that runs; retries `409 operation_conflict`. |
| attach | `POST /v1/machines/{id}/ports` with `{"port": 9000, "access": "private"}` when no token is recorded; `GET /v1/machines/{id}/files/content?path=/data/.lydiabox/guest.env`, `PUT /v1/machines/{id}/files` when it differs, and `POST .../exec` with `sudo supervisorctl ... restart lydiabox-guest` when an old file was replaced; `PUT /v1/machines/{id}/activity-leases/lydiabox-cell` (TTL 15 minutes, renewed every 5 minutes) | Then the guest link dials the port URL (`wss://`, path `/`) with `Authorization: Bearer <port token>`. A `401` from the port clears the token; the next attach publishes the port again (a new token). |
| `Pause` | Close the link, end the lease renewal; `POST /v1/machines/{id}/suspend` | An explicit pause: the machine does not wake on use, and its activity leases end. |
| `Resume` | As `Boot` | `wake` for a self-paused machine (`autoSuspendedAt` set), else `resume`. If memory is lost, the guest starts fresh; the controller probes it as after a boot. |
| `Shutdown` | Close the link; `POST /v1/machines/{id}/resume` first for a paused machine; `POST /v1/machines/{id}/stop` (with `force`: `timeoutSeconds` 5) | `Shutdown` keeps the machine and its disk. Only `lydiabox instabox destroy -confirm <id>` deletes a machine. |
| `State` | `GET /v1/machines/{id}` | `running` to running; `creating`, `starting`, `resuming` to booting; `suspending`, `suspended` to paused; `stopping`, `stopped`, `error` to stopped; `deleting`, `deleted`, or `404` to an error that names the deleted machine. No recorded machine is stopped. |
| `DialGuest` | none (guest link); one `POST .../exec` with a status command when the port answers `502` for 45 seconds | The first dial after a start of the cell attaches to a running machine. While this cell paused or stopped the machine, a dial fails at once with `ErrUnavailable`. A `502` from the port (the machine runs, but nothing listens on port 9000) becomes the error "the InstaBox machine runs, but its guest daemon does not answer on port 9000"; after 45 seconds of `502` the driver logs one line with `systemctl is-active instabox-image.service`, `supervisorctl status`, and the last 5 journal lines of the unit (no secrets), once for each connection of the link. |
| `ListenGuestPort` | none (guest link) | Registers a reverse channel for port 9001 or 9002. It works before boot; streams arrive when the link connects. |
| `Capabilities` | none | `Remote`, `SelfPause`, and a probe of 60 seconds (a wake can wait 45 seconds). |
| `WatchState` | `GET /v1/machines/{id}` every 30 seconds, and at once when the link drops or a lease renewal fails | Reports a pause, resume, or stop that the cell did not make; on a self-pause it closes the link (a reconnect would wake the machine) and ends the lease. |

Errors: `401` (key), `402` or `credit_exhausted` (payment), `403`
(scope or plan), `429` (quota or rate), and `no_capacity` become a clear
message in `lastError` of `GET /api/v1/vm/status` and in the web status
bar, and the API answers `503`. After such an error the driver makes no
call for the tenant for 30 seconds (longer when `Retry-After` says so),
so that requests that wake the machine do not repeat it.

Not in the interface, but used by the controller and the cell:

- At `SIGTERM` the controller leaves a remote machine as it is; the driver
  closes the link and deletes its activity lease, so that the idle pause
  applies.
- Webhooks are not used (see [cloud-design.md §4.6](cloud-design.md#46-gaps-in-the-core)).

## 4. Desktop options

### 4.1 Option A: LydiaBox WebRTC stream through TURN on TCP

The core streams the X display of the guest with WebRTC. With
`LYDIABOX_WEBRTC_RELAY_ONLY=true` and a Cloudflare Realtime TURN key,
both peers use the TURN relay, and the guest needs no inbound port
([README.md](../README.md#desktop-stream-through-a-turn-relay)).

- Advantages: the same web UI, the takeover lease, and the computer-use
  MCP tools on the same display. Media goes from the guest to TURN to the
  browser, not through the cell. No InstaBox viewer limit.
- Disadvantages: inbound UDP is **not stated**, and outbound UDP is only
  implied (secrets: HTTP/3 on UDP 443 passes the proxy). Use TURN over
  TCP or TLS on port 443 (`turns:` or `?transport=tcp`). Not verified on
  InstaBox. The outbound cap of about 50 Mbit/s (limits) is more than one
  desktop stream needs (**inferred**).

### 4.2 Option B: InstaBox VNC

The cell opens `GET /v1/machines/{id}/vnc` (a WebSocket with VNC) and
relays it to a noVNC viewer in the web UI (desktop).

- Advantages: no TURN service.
- Disadvantages:
  - A workspace key counts as the owner, and each user has at most 8
    open terminals and desktops. Thus at most 8 viewers for all tenants
    together (desktop).
  - VNC sessions share the limit of 32 `exec` calls at once (limits).
  - `/vnc` does not wake a machine (auto-pause).
  - Stream links need the personal key of the machine creator (desktop).
  - **Not stated**: whether a machine from a custom image has the
    InstaBox VNC server, and which X display it shows.
  - All media goes through the cell. The web UI needs a new viewer, and
    the takeover lease and the MCP tools do not see VNC input.

### 4.3 Recommendation

Use option A. Keep option B only as an operator tool to examine a
machine. Phase I1 must show option A from a network without UDP.

## 5. Online 24/7

### 5.1 Behavior

- Paused and stopped machines cost only their disk (pricing). A paused
  machine keeps memory (lifecycle).
- A self-paused machine wakes on use, and the request waits up to 45
  seconds (auto-pause). The real wake time is **not stated**.
- A machine that the client paused does not wake on use (auto-pause).
- **Not stated**: whether a request with a valid private-port token
  wakes a self-paused machine, and whether an open but quiet WebSocket
  keeps a machine busy.

### 5.2 Policy

1. The cell controller owns the power state. After
   `LYDIABOX_IDLE_TIMEOUT` (for example 10 minutes) with no lease, it
   closes the guest link and calls `suspend`.
2. While the cell is attached to a running machine, the driver holds the
   activity lease `lydiabox-cell` (TTL 15 minutes, renewed every 5
   minutes). Thus InstaBox does not pause a machine that the cell uses,
   also if a quiet WebSocket does not count as activity (fact 4 of §8).
3. Keep the InstaBox idle pause (`LYDIABOX_INSTABOX_IDLE_PAUSE`, default
   30 minutes) as a safety net, for a cell that stopped without a pause:
   the lease ends at most 15 minutes later, then the idle pause applies.
4. Before each use, the cell reads the state and calls `resume`, `wake`,
   or `start` as necessary. This works for both kinds of pause. When
   InstaBox pauses the machine by itself, the state watcher reports it,
   and the cell does not reconnect until the next use.
5. For a `task` lease, hold an InstaBox activity lease with a TTL of 15
   minutes and renew it every 5 minutes (not implemented: no task API).
6. Do not stop machines to save cost: a paused machine costs the same
   as a stopped one. Stop only to suspend a tenant or to repair a
   machine.

"Always running" is a paid option: `LYDIABOX_INSTABOX_IDLE_PAUSE=0` and
`LYDIABOX_IDLE_TIMEOUT=0`.

### 5.3 Cost for one tenant

Unit prices (pricing): $0.004 per vCPU-hour, $0.00225 per GiB-hour of
memory, $0.00011 per GiB-hour of disk ($0.08 per GiB-month), egress
$0.02 per GiB. Runtime is billed per second, only while running. A month
is 730 hours.

| Size | Running, per hour | Always running, per month | 4 hours a day (121.7 h), per month |
| :--- | ---: | ---: | ---: |
| 2 vCPU, 4 GiB | $0.017 | $12.41 | $2.07 |
| 4 vCPU, 8 GiB | $0.034 | $24.82 | $4.14 |
| 8 vCPU, 16 GiB | $0.068 | $49.64 | $8.28 |

Add for each tenant:

- Disk: 20 GiB is $1.60 a month. Pro includes 200 GiB of disk across all
  machines, so the first 10 tenants with 20 GiB pay no disk (pricing).
- Egress: 1 TiB a month is included on Pro, then $0.02 per GiB
  (pricing). **Inferred**: backups to S3 count as egress.
- The plan: Pro is $20 a month and includes $20 of usage credit
  (pricing).

Not in these numbers: the Kubernetes cluster, Cloudflare TURN, and LLM use.

Plan limits that affect scale (limits):

| Plan | Machines | Running at once | Starts a minute |
| :--- | ---: | ---: | ---: |
| Pro | 500 | 100 | 30 |
| Builder | 1,000 | 200 | 50 |
| Team | unlimited | 500 | 100 |

Create, start, resume, and wake count as starts. **Not stated**: whether
a paused machine counts as running.

## 6. Provider storage mode

### 6.1 What changes for the user

| Item | Self-hosted core | InstaBox, provider storage mode |
| :--- | :--- | :--- |
| Where the files are | JuiceFS in the user's S3 bucket | The `/data` disk of the InstaBox machine (storage) |
| Who controls the files | The user | The supplier keeps them for the service |
| Files page | Works from the host mount, also with the VM stopped | Works only through the guest: the cell must resume or start the machine first |
| WebDAV (`/webdav`) | On | Off |
| S3 gateway (`/s3`) | On | Off |
| Root overlay | `root-upper.img` on JuiceFS | The root disk of the machine, which stays |
| Size | The bucket quota | `/data`: 20 GiB, up to 200 GiB on Pro (limits) |
| Backup | Metadata dump to S3 every 60 seconds | None built in |
| Speed | JuiceFS with a local cache | Local ext4 (**inferred**: faster for `git` and `npm`) |

The Files page reads files through the guest RPC, not through the
InstaBox file API. This avoids the limit of 32 `exec` and file calls at
once for the workspace (limits).

### 6.2 Mitigations

1. **Host-driven backup to S3.** The cell asks the guest, over the guest
   link, to stream a compressed archive of the home and the workspace.
   The cell uploads it to a service bucket under `tenants/<tenant>/`. No
   S3 credential goes into the machine. A guest upload to presigned URLs
   is an option that keeps the data out of the cell. At about 50 Mbit/s
   out (limits), 10 GiB takes about 28 minutes. Do it daily, and keep 7
   daily and 4 weekly copies (decision 19).
2. **InstaBox snapshots.** Free (pricing) and taken live (snapshots).
   Take one each day and before each guest upgrade. They protect against
   a broken machine, not against loss of the supplier.
3. **Export.** Give the user the latest backup archive as a time-limited
   download. The InstaBox export API is not for an active workspace
   (openapi).
4. **Files while stopped.** Later, show the latest backup read-only when
   the machine cannot start.
5. **Account health.** Watch billing; a disabled workspace loses all
   machines after 30 days (storage).

## 7. Supplier risks

| Risk | Evidence | Mitigation |
| :--- | :--- | :--- |
| The company is unknown | The pages read name no company, address, or jurisdiction. The SDK package is `@insforge/instabox-sdk` (**inferred**: the company is InsForge). | Get the company name, jurisdiction, and data locations in writing before users that the owner does not know. |
| Resale terms are not checked | The API records agreement to "rules of the road" (`PUT /v1/me/terms`, openapi). We did not read the terms. | Read the terms. Ask InstaBox in writing if a hosted service for third parties is permitted. |
| `linux/amd64` only | images | Build the guest image for `amd64` only on this path. Own compute later can use `arm64`. |
| No stated UDP | ports and limits state HTTPS ports and a raw TCP preview only | Desktop through TURN on TCP or TLS (§4). |
| No region choice | storage | Accept for the first version. State it in the privacy policy. |
| The supplier can read user data | The supplier runs the hosts (**inferred**). Encryption at rest is **not stated**. | No secrets in the machine (cloud-design §5). Say it in the privacy policy. |
| Data loss when the account fails | A disabled workspace keeps data 30 days; a deleted one loses it at once (storage) | A spend cap, billing alerts, and backups outside InstaBox (§6.2) |
| Limits at scale | 32 `exec` calls at once, starts a minute, running machines (limits) | Guest link instead of `exec` for traffic. Choose the plan from the count of active tenants (decision 18). |
| No SLA on Pro | A 99.9% SLA is for Enterprise; Team has "SLAs" without numbers (pricing) | Measure availability in phases I1 to I4. Examine Enterprise before a paid launch. |
| The API changes often | Pages name template and control releases from October and November 2026 | Driver tests against a fake API; read the changelog (https://instabox.sh/changelog.md) before each upgrade. |
| Lock-in | The export API is only for a disabled workspace (openapi) | Own backups (§6.2) and the driver boundary (cloud-design §1) |

## 8. Facts that the pages do not confirm

Phase I1 must measure or ask InstaBox about each of these:

1. The real wake time of a paused machine. The pages give only the
   45-second wait bound.
2. The start time of a stopped machine.
3. Whether a request with a valid private-port token wakes a self-paused
   machine.
4. Whether an open, quiet WebSocket on a private port keeps a machine
   busy.
5. Whether inbound UDP reaches a machine, and whether all outbound UDP
   works (only UDP 443 is implied).
6. Whether a paused machine counts toward "running at once".
7. Whether a machine from a custom image has the InstaBox VNC server.
8. How to replace the root disk with a new image version and keep
   `/data`.
9. Whether traffic to S3 for backups counts as billed egress.
10. The company identity, its jurisdiction, the data locations,
    encryption at rest, and the resale terms.
11. Whether a WebSocket upgrade with the `Authorization: Bearer` port
    token (not the browser cookie) passes the port proxy. The guest link
    needs it.
