# LydiaBox Cloud: hosted deployment design

Status: design. The core seams of phase I0 are implemented (section 4 is
the contract). The control plane is not part of this repository: an
operator who runs a hosted service writes it against the contract in
section 4. Host names in this document are examples
(`cloud.example.com`).

This design has three documents:

- This document: the decision, the architecture, the contract between
  the control plane and a cell, secrets, the open decisions, and the
  phases.
- [cloud-instabox.md](cloud-instabox.md): the InstaBox facts with their
  sources, the `vmm.Driver` mapping, provider storage, the desktop, the
  24/7 behavior, the cost, and the supplier risks.
- [cloud-own-compute.md](cloud-own-compute.md): the design for the
  service's own KVM hosts. It is now the later phase.

LydiaBox Cloud is the name in this document for a hosted, multi-tenant
service on top of the open-source LydiaBox core.
A user signs in, configures a harness, and gets a personal agent that is
always reachable. The user can observe and take over the agent desktop,
and the agent files persist.

Terms:

- The **core** is the code in this repository, as [design.md](design.md)
  specifies it.
- A **cell** is one core process (`lydiabox up`) for one tenant, with its
  children (magpie) and its agent VM.
- A **cell host** is one process that starts and supervises many cells.
- The **control plane** is the central service for accounts, tenants,
  routing, and the dashboard.
- A **machine** is one InstaBox Firecracker VM. On the InstaBox path, the
  machine is the agent VM of one tenant.
- The **guest link** is a WebSocket that the cell dials to the guest. It
  replaces vsock on backends that have no vsock.
- **Provider storage mode** keeps the agent home and workspace on the
  disk of the VM provider, not on JuiceFS.

## 1. Decision record

| Item | Value |
| :--- | :--- |
| Compute | InstaBox machines first. Own KVM hosts are a later option. |
| Alternative | Operate KVM hosts with the `chv` driver, a host agent, JuiceFS on a service bucket, and a shared PostgreSQL metadata engine: [cloud-own-compute.md](cloud-own-compute.md). |
| First login | Cloudflare Access email verification on the service domain |
| Control plane code | A separate repository that runs the core as a pinned binary |
| Deployment | A Kubernetes cluster, with the manifests in a GitOps repository of the operator |

Reasons:

1. The service does not buy, operate, or patch KVM hosts.
2. Each tenant gets a Firecracker VM, so the hypervisor is the tenant
   boundary ([cloud-instabox.md §1](cloud-instabox.md#1-instabox-facts-used-in-this-design)).
3. InstaBox gives a persistent disk, pause that keeps memory, custom image
   import, private HTTPS ports with WebSocket, and an exec and file API.

Rule for all core changes: own compute later must be only a driver swap.
Thus each core change is generic. The core does not know the name
InstaBox outside the `instabox` driver.

## 2. Goals, non-goals, and user journey

### 2.1 Present system

One `lydiabox up` process is the trusted control plane for one tenant:

- `serve` builds one store, one storage supervisor, one authenticator, and
  one API server for `cfg.TenantID` ([internal/cli/up.go](../internal/cli/up.go)).
- `cfg.TenantID` is always `"default"`. No setting changes it
  ([internal/config/config.go](../internal/config/config.go)).
- The VM drivers are `chv` and `docker` ([internal/vmm/driver.go](../internal/vmm/driver.go)).
  The host reaches the guest over vsock ports 9000 (guest RPC), 9001
  (host broker), and 9002 (LLM gateway) ([internal/guest/rpc/addr.go](../internal/guest/rpc/addr.go)).
- The home and the workspace are on a JuiceFS mount in the user's S3
  bucket. The host shares them into the VM with `virtiofsd`.
- Cloudflare Access login, the TURN relay for the desktop stream, and
  harness subscription login are on `main`
  ([remote-access-manual-test.md](remote-access-manual-test.md),
  [turn-manual-test.md](turn-manual-test.md),
  [subscriptions-manual-test.md](subscriptions-manual-test.md)).

### 2.2 Goals

1. A user gets a working agent without a server, a bucket, or a KVM host.
2. The agent is always reachable. Work can start without an open browser.
3. The user observes and takes over the desktop, as in the core.
4. One tenant cannot read, change, or slow down another tenant.
5. LLM keys, connector tokens, and the InstaBox API key never go into an
   InstaBox machine.
6. The core stays a complete self-hosted product. Each cloud change in the
   core is opt-in, and the defaults keep self-hosted behavior.

### 2.3 Non-goals for the InstaBox path

- Teams, shared agents, and roles. One account has one agent.
- GPU passthrough, the `docker` driver, and `arm64` guests. InstaBox
  imports `linux/amd64` images only.
- A choice of region. InstaBox does not let a client select one.
- Public sign-up and billing. The first users are people whose email the
  operator adds to the Cloudflare Access policy.

### 2.4 User journey (first version)

1. The operator adds the email of the user to the Access policy.
2. The user opens `https://cloud.example.com/`. Cloudflare Access sends a code
   to the email and admits the user.
3. The dashboard makes the tenant. The control plane starts a cell, and
   the cell makes the machine from the guest image snapshot.
4. The user opens `https://<tenant>.cloud.example.com/`, the core web UI of
   the cell, and sets the LLM endpoint.
5. The user runs harness sessions and takes over the desktop through TURN.
6. The machine pauses when idle and wakes on the next use.

## 3. Architecture

```text
Browser ──HTTPS──> Cloudflare (Access; cloud.example.com, *.cloud.example.com)
                       │ Cloudflare Tunnel
                       ▼
K8s:   lbx-router ──HTTP──> lbx-cellhost pod ──Unix socket──> cell
       lbx-control                                (lydiabox up in cell mode,
       (accounts, tenants)                         magpie with the LLM keys)
                                                    │                │
                                  InstaBox API (HTTPS)   guest link (WSS to
                                                    │    a private port)
                                                    ▼                ▼
InstaBox:                         machine of the tenant: lydiabox-guest,
                                  harnesses, desktop
                                                    │ desktop media,
                                                    ▼ TURN on TCP/TLS 443
Browser <──────────────────────── Cloudflare Realtime TURN
```

### 3.1 Core changes in this repository

Parallel tasks make these changes. Each change is opt-in.

| Change | Function | Default |
| :--- | :--- | :--- |
| Guest link | A WebSocket that the host dials to a TCP port of the guest. One stream multiplexer carries host-to-guest streams (`DialGuest`) and guest-to-host reverse channels (`ListenGuestPort`, ports 9001 and 9002). A link key from the cell authenticates the host to the guest. | vsock |
| Provider storage mode | The home and the workspace stay on the machine disk. The host reaches the files through guest RPC file methods. No JuiceFS mount and no S3 settings. WebDAV and `/s3` are off. | JuiceFS |
| Cell mode | Many core processes on one host: the tenant ID from a setting, a per-tenant data directory, a Unix socket or loopback listener, no host-wide resources (UDP port, tunnel), and no bootstrap token output | off |
| `instabox` VM driver | `vmm.Driver` on the InstaBox API and the guest link ([mapping](cloud-instabox.md#3-vmmdriver-mapping)) | not selected |
| Guest image for InstaBox | The guest root filesystem as a `linux/amd64` OCI image, which InstaBox imports as a snapshot | none |

The tasks that implement these changes set the final names of settings
and routes. [api.md](api.md) and [README.md](../README.md) document them
when they land. Section 4 gives the names as proposals.

### 3.2 Cloud repository components

A separate repository holds the control plane. It runs the core as a
pinned binary and imports no core `internal/` package. The component
names below are examples.

| Component | Function |
| :--- | :--- |
| `lbx-control` | Accounts (identity: the Access email), tenant registry, tenant lifecycle, assignment of tenants to cell hosts, custody of the InstaBox API key and the tenant master keys |
| `lbx-cellhost` | Starts, supervises, and stops the cells of its tenants. Keeps each cell data directory on a persistent volume. Forwards requests from `lbx-router` to the Unix socket of each cell. Reports cell health to `lbx-control`. |
| `lbx-router` | Routes `<tenant>.cloud.example.com` to the cell of the tenant. Checks the Access identity. Keeps WebSocket routes open without buffering. |
| Dashboard | Tenant creation, tenant state, link to the cell web UI |
| Registry database | PostgreSQL: accounts, tenants, cell host assignment, machine IDs, wrapped keys |

Later components: a trigger service, an OAuth relay for connectors, and
metering and billing ([cloud-own-compute.md §3.1](cloud-own-compute.md#31-components)).

TURN is the Cloudflare Realtime TURN service. The core already mints
short-lived credentials for it (`LYDIABOX_CLOUDFLARE_TURN_KEY_ID`,
[README.md](../README.md#desktop-stream-through-a-turn-relay)).

### 3.3 Deployment

- A GitOps repository holds the Kubernetes manifests. A GitOps tool (for
  example Argo CD) syncs them to the cluster. The CI of the control plane
  repository builds the images.
- `lbx-cellhost` is a StatefulSet. Each replica has its own persistent
  volume for the cell data directories. A NetworkPolicy lets only
  `lbx-router` and `lbx-control` reach it.
- `cloudflared` runs in the cluster. One Cloudflare Tunnel serves
  `cloud.example.com` and `*.cloud.example.com`.
- One Cloudflare Access application covers `cloud.example.com` and
  `*.cloud.example.com` with an email policy.
- The InstaBox webhook endpoint of `lbx-control` needs an Access bypass
  for its path. `lbx-control` verifies the HMAC signature of each call
  ([source](https://instabox.sh/docs/integrations/webhooks.md)).
- Kubernetes Secrets hold the InstaBox API key, the TURN key, the
  database credentials, and the key that wraps the tenant master keys.
  Keep them out of the GitOps repository in plain text (for example, use
  sealed or external secrets).
- Cell data directories hold the store, the master key, and the magpie
  state. Back them up to object storage (decision 19).

## 4. Contract between the control plane and a cell

### 4.1 Start

1. `lbx-control` assigns the tenant to a cell host and sends a start
   command with the tenant ID.
2. `lbx-cellhost` makes the data directory `/cells/<tenant>` (mode
   `0700`) if it does not exist. It writes the secret files of
   section 4.2 with mode `0600`.
3. `lbx-cellhost` starts `lydiabox up` as a child process with the
   settings of section 4.2. It never puts a secret on the command line.
4. On its first start, the cell makes the machine (section 3 of
   [cloud-instabox.md](cloud-instabox.md#3-vmmdriver-mapping)). On a
   later start, the cell finds the existing machine and attaches to it.
5. The cell is ready when `GET /api/v1/ready` (public) answers `200` with
   the tenant ID of the cell. The VM can stay paused or stopped: the cell
   makes the machine and wakes it on the first use.

### 4.2 Configuration

All names below are implemented (2026-11-15). The control plane starts a
cell with this environment; values that are secrets go in `0600` files.

| Setting | Value | Source |
| :--- | :--- | :--- |
| `LYDIABOX_TENANT_ID` | The tenant ID | `lbx-control` |
| `LYDIABOX_CELL_MODE` | `true` | Fixed |
| `LYDIABOX_DATA_DIR` | `/cells/<tenant>` | `lbx-cellhost` |
| `LYDIABOX_LISTEN_ADDR` | `unix:/run/lbx/<tenant>.sock` (mode `0660`) | `lbx-cellhost` |
| `LYDIABOX_VM_DRIVER` | `instabox` (or `auto`: provider mode with InstaBox settings selects it) | Fixed |
| `LYDIABOX_STORAGE_MODE` | `provider` | Fixed |
| `LYDIABOX_VM_CPUS`, `LYDIABOX_VM_MEM_MB` | An InstaBox size, for example `2` and `4096` | `lbx-control` |
| `LYDIABOX_IDLE_TIMEOUT` | For example `10m` | `lbx-control` |
| `LYDIABOX_INSTABOX_API_URL` | `https://api.instabox.sh`, or a broker URL later (decision 17) | Fixed |
| `LYDIABOX_INSTABOX_API_KEY_FILE` | A file with the InstaBox API key | `lbx-control` |
| `LYDIABOX_INSTABOX_SNAPSHOT` | The snapshot ID of the guest image version | `lbx-control` |
| `LYDIABOX_INSTABOX_IDLE_PAUSE` | `30m` (the safety net), or `0` for "always running" | `lbx-control` |
| `LYDIABOX_MASTER_KEY_FILE` | A file with the tenant master key, unwrapped | `lbx-control` |
| `LYDIABOX_AUTH_TOKEN_FILE` | A file with the service token of `lbx-cellhost` and `lbx-control` | `lbx-control` |
| `LYDIABOX_ACCESS_TEAM_DOMAIN`, `_AUD`, `_EMAILS` | The Access team domain and audience; the email of the tenant owner | `lbx-control` |
| `LYDIABOX_MAGPIE_ADDR`, `LYDIABOX_MAGPIE_WEB_ADDR` | Two loopback ports that are free on the cell host | `lbx-cellhost` |
| `LYDIABOX_S3_GATEWAY_ADDR` | `off` | Fixed |
| `LYDIABOX_CLOUDFLARE_TURN_KEY_ID`, `_API_TOKEN` | The TURN key of the service | `lbx-control` |
| `LYDIABOX_WEBRTC_RELAY_ONLY` | `true` | Fixed |

`LYDIABOX_CELL_MODE=true` refuses to start without the tenant ID, the
admin token, and the master key; it never generates or prints a secret,
turns `LYDIABOX_CLOUDFLARE_TUNNEL` off, and forwards no UDP port. The
`instabox` driver refuses `LYDIABOX_WEBRTC_UDP_PORT` other than `0`.

The cell does not get S3 settings in provider storage mode.

### 4.3 Health

| Check | Route | Use |
| :--- | :--- | :--- |
| Liveness | `GET /api/v1/health` ([api.md](api.md#get-apiv1health--public-implemented)) | The process answers |
| Readiness | `GET /api/v1/ready` (public) | Every subsystem started; the body has the tenant ID |
| Readiness and state | `GET /api/v1/status` ([api.md](api.md#get-apiv1status--implemented)) | Store, storage, and VM state |
| VM detail | `GET /api/v1/vm/status` ([api.md](api.md#7-vm-power--implemented-phase-2)) | Power state of the machine |

`lbx-cellhost` calls `/api/v1/health` every 10 seconds through the
socket. After 3 failures, it stops the cell and starts it again with an
increasing delay. It sends the state of each cell to `lbx-control` every
30 seconds. A cell that fails at start 5 times goes to the state
`failed`, and an operator examines it.

### 4.4 How the proxy authenticates to the cell

First version (no new code in the core):

1. Cloudflare Access admits only allowed emails and adds the
   `Cf-Access-Jwt-Assertion` header.
2. `lbx-router` verifies the Access JWT. It finds the tenant from the
   host name and checks in the registry that the email owns the tenant.
   Else it answers `403`.
3. `lbx-router` forwards the request with the Access JWT unchanged to
   the cell host of the tenant, which forwards it to the Unix socket of
   the cell. `lbx-router` removes other identity headers.
4. The cell verifies the same JWT in its existing Access login mode
   ([api.md](api.md#21-cloudflare-access--implemented)). `LYDIABOX_ACCESS_EMAILS`
   contains only the owner email, so a JWT of another user fails.

The cell listens only on a Unix socket in its cell host pod. Thus only
the cell host, and through it `lbx-router` and `lbx-control`, reach it.
`lbx-control` uses the service token of section 4.2 for service calls.

Later: when login moves from Access to a hosted identity provider
(decision 9), `lbx-router` signs a short-lived JWT, and the cell verifies
it with the same verifier ([internal/cloudflare/accessjwt](../internal/cloudflare/accessjwt))
and a configured issuer, audience, and key set.

### 4.5 Stop

| Reason | Steps |
| :--- | :--- |
| Upgrade or move of a cell | `lbx-cellhost` sends `SIGTERM`. The cell closes the guest link, releases its InstaBox activity lease, and exits. The machine continues; its idle pause applies. The new cell attaches to the same machine (implemented and tested against the fake API) and installs its guest binary and its system bundle there ([instabox.md](instabox.md#guest-binary-upgrade), [system upgrade](instabox.md#system-upgrade)). |
| Suspend a tenant | `lbx-control` calls `POST /api/v1/vm/stop` on the cell, which stops the machine with a clean shutdown. Then `SIGTERM`. The data stays. |
| Delete a tenant | Suspend. Revoke connector tokens. `lbx-control` deletes the machine and its snapshots through the InstaBox API (or runs `lydiabox instabox destroy -confirm <machine ID>` with the cell environment), then the backups, the cell data directory, and the registry entry. It destroys the wrapped master key last. |

If a cell does not exit 30 seconds after `SIGTERM`, `lbx-cellhost`
sends `SIGKILL`.

### 4.6 Gaps in the core

The core implements the contract above. The driver also upgrades the
guest binary of each machine to the cell's own at attach, and restarts
the guest daemon only when no terminal or harness session runs
([instabox.md](instabox.md#guest-binary-upgrade)). It also brings the
system files of each machine to the cell's own bundle, with no new
snapshot ([instabox.md](instabox.md#system-upgrade)). These parts are not in
the core yet; the control plane or a later task must add them:

1. State changes from webhooks. The cell reads the machine state itself
   (every 30 seconds, and at once when the guest link drops). It has no
   route for InstaBox webhooks; `lbx-control` must take them.
2. Task leases (`POST /api/v1/tasks`) and the InstaBox activity lease of a
   task (§6) are not implemented. The cell holds one activity lease while
   it is attached to a running machine.
3. A key broker (decision 17). `LYDIABOX_INSTABOX_API_URL` can point at
   it, but the cell sends the workspace key in `X-API-Key`; the broker
   must accept a per-cell credential in its place.
4. A cell host protocol. Health, readiness, and stop are the routes and
   signals above; there is no route that reports the cell's own version
   or configuration to `lbx-cellhost` other than `GET /api/v1/status`.
5. Backups outside InstaBox (decision 19) and metering are not in the
   core.
6. The cell makes the machine on the first use, not at start, so that a
   cell of an inactive tenant costs nothing. A control plane that wants
   the machine at once calls `POST /api/v1/vm/wake`.

## 5. Secrets on the InstaBox path

| Secret | Where it is | Never |
| :--- | :--- | :--- |
| LLM keys, subscription tokens | magpie in the cell data directory | In the machine. The guest gets only the revocable gateway key, which the host side of channel 9002 adds. |
| Connector tokens | Cell store, encrypted with the tenant master key | In the machine. The guest asks for a short-lived token over channel 9001. |
| Tenant master key | Registry, wrapped with a control-plane key; a `0600` file for the cell | In the machine |
| InstaBox API key | `lbx-control`; a `0600` file for each cell in the first version | In the machine |
| Private port token | Cell store, encrypted | In a log |
| Link key | Cell store, encrypted; the machine environment (it protects only the guest) | In a log |

Do not use InstaBox Org Secrets or machine environment variables for
these secrets. Every process in a machine can read its environment
variables ([source](https://instabox.sh/docs/machines/environment.md)).

The supplier can read what is in the machine: the files on its disk and
the memory of the VM. This is true of every VM host. The privacy policy
must say it. The secrets above are not in the machine.

## 6. Harness authorization, triggers, and metering

- **Harness authorization.** The default is an LLM endpoint with an API
  key, stored in magpie. Do not offer subscription login in the cloud
  until a legal review of each provider's terms permits it (decision 7,
  [subscriptions-manual-test.md](subscriptions-manual-test.md)).
- **Triggers.** A schedule, a webhook, an email, or a chat message starts
  a headless task through a new `POST /api/v1/tasks` route. The task
  holds a `task` lease. On InstaBox, the driver also holds an InstaBox
  activity lease for the task
  ([cloud-instabox.md §5](cloud-instabox.md#5-online-247)).
- **Metering.** InstaBox reports compute use for each machine
  (`GET /v1/machines/{id}/usage`, `GET /v1/usage/machines`,
  [OpenAPI](https://api.instabox.sh/v1/openapi.json)). The control plane
  reads it for each tenant.

The trigger design is in [cloud-own-compute.md §6.2](cloud-own-compute.md#62-trigger-layer).
It applies to both paths.

## 7. Security model on the InstaBox path

The guest is not trusted: it runs code from LLMs and the internet.

| Threat | Control |
| :--- | :--- |
| Guest escape to another tenant | Firecracker VM per tenant (supplier control) |
| Guest reads host secrets | No secret in the machine (section 5) |
| Someone else reaches the guest link | Private port: a request without the token gets `401`. The guest also checks the link key. |
| Wrong-tenant routing or forged identity | Access JWT, owner check in `lbx-router`, email allow-list in the cell, Unix socket listener, NetworkPolicy on the cell hosts |
| Cell compromise | Exposes one tenant, and the InstaBox API key in the first version (decision 17) |
| Control-plane compromise | Exposes all tenants. No standing operator shell access; audit the deploy credentials. |
| Supplier compromise or failure | Backups outside InstaBox (decision 19); see [cloud-instabox.md §7](cloud-instabox.md#7-supplier-risks) |
| Prompt injection through triggers | Treat trigger content as untrusted data; show the trigger source in the session |

## 8. Open decisions

**R** is the recommendation. Other documents refer to these decisions by
number.

Decided in this design:

- **16. Compute.** InstaBox first; own compute later.
- **2. Cloud code location.** A separate repository for the control
  plane. The core seams stay here.
- **3. Core license.** The core is Apache-2.0 ([LICENSE](../LICENSE)).
- **9. Login.** First login is Cloudflare Access email verification on
  the service domain. A hosted identity provider is for public sign-up
  later.

Open:

1. **Tenancy model.** (a) Process per tenant; (b) one multi-tenant
   process. **R**: (a). The InstaBox path implements (a) as cells.
4. **Bucket ownership** (own compute). **R**: (a) service bucket with a
   prefix per tenant, [cloud-own-compute.md](cloud-own-compute.md#42-bucket-and-prefix).
5. **Metadata engine and writeback** (own compute). **R**: PostgreSQL
   with writeback off.
6. **"Online 24/7".** (a) Always running; (b) always reachable, wake on
   use; (c) (b) by default, (a) as a paid option. **R**: (c). On InstaBox
   a paused machine costs only its disk.
7. **Subscription credentials in the cloud.** **R**: never at launch;
   then only after a legal review of each provider's terms.
8. **LLM supply.** **R**: the user brings a key or endpoint; later the
   service sells LLM access through magpie.
10. **Tenant address.** **R**: a subdomain per tenant
    (`<tenant>.cloud.example.com`), so the core cookie and `Origin` rules work
    unchanged.
11. **Sleep ladder** (own compute). **R**: pause, stop VM, stop cell.
12. **First trigger channels.** **R**: schedule and webhook, then email,
    then chat.
13. **Guest egress.** InstaBox controls the machine network. **R**: accept
    the InstaBox defaults for the first version.
14. **Operator access** (own compute). **R**: no standing root access.
15. **Default VM size.** (a) 4 vCPU, 8 GiB; (b) 2 vCPU, 4 GiB with an
    upgrade. **R**: (b), if phase I1 shows that Chrome and a harness run
    well in it.
17. **InstaBox API key custody.** (a) Each cell holds the workspace key;
    (b) a broker in `lbx-control` holds it and lets each cell act only on
    its own machine. **R**: (a) in phases I1 and I2 (few trusted users),
    (b) before users that the operator does not know. The driver takes the
    API URL as a setting, so (b) needs no core change.
18. **InstaBox plan.** **R**: choose the plan from the number of
    machines that must run at once
    ([cloud-instabox.md §7](cloud-instabox.md#7-supplier-risks)).
19. **Backups outside InstaBox.** (a) None; (b) a host-driven backup of
    the home and the workspace to a service bucket; (c) (b) to a bucket
    of the user. **R**: (b) daily, (c) later as an option.
20. **Desktop transport.** (a) LydiaBox WebRTC through TURN on TCP; (b)
    InstaBox VNC. **R**: (a), see
    [cloud-instabox.md §4](cloud-instabox.md#4-desktop-options).
21. **Registry database.** (a) Managed PostgreSQL; (b) PostgreSQL in the
    cluster. **R**: (a), so that backups and failover are the provider's.

## 9. Phased plan and verification gates (InstaBox path)

Do the phases in sequence. Start a phase after the previous gate passes.

- **Phase I0: core seams.** Guest link, provider storage mode, cell mode,
  the `instabox` driver, and the guest image.
  - *Verification*: `CGO_ENABLED=0 go test -race ./...` and `go vet ./...`
    pass. The [KVM checklist](kvm-test.md) passes with default settings.
    Driver tests against a fake InstaBox API cover each `vmm.Driver`
    method and the errors `409 operation_conflict` and `no_capacity`.
    Guest link tests carry RPC, 9001, and 9002 traffic over one
    WebSocket, reconnect after a cut, and refuse a wrong link key. In
    provider storage mode, the file API works through the guest, and
    `/webdav` and `/s3` answer `404`.
- **Phase I1: one tenant on real InstaBox.** One cell on a workstation
  with a real InstaBox key and the imported guest image.
  - *Verification*: Create, boot, a harness session through the magpie
    gateway, pause, wake on a request, stop, and start all work. Files in
    the home and the workspace stay after stop and start. The desktop
    works through Cloudflare TURN over TCP from a network without UDP. No
    LLM key is in the machine environment, on its disk, or in
    `/run/instabox/env`. A request to the private port without the token
    gets `401`. Record the wake time from paused and the start time from
    stopped.
- **Phase I2: cloud repository and deployment.** `lbx-control`,
  `lbx-cellhost`, `lbx-router`, and the dashboard on a Kubernetes
  cluster, behind Access.
  - *Verification*: An allowed email makes a tenant and uses
    `https://<tenant>.cloud.example.com/`. Access stops an email that is not
    allowed. A second allowed user gets `403` on the first tenant. A
    request with a forged identity header fails. After a restart of a
    cell host, its cells attach to their machines with no data loss.
- **Phase I3: durability.** Host-driven backup to S3, InstaBox
  snapshots, backup of the cell data directories, and a restore drill.
  - *Verification*: Delete the machine of a test tenant. Restore the
    backup into a new machine; the checksums of all files match. Restore
    a cell data directory on a new volume; the LLM configuration and the
    connector tokens work.
- **Phase I4: 24/7 and triggers.** Task API with activity leases,
  schedules, and webhooks.
  - *Verification*: A scheduled task runs with no browser. Its transcript
    shows in the Studio, and the machine pauses after it. A webhook
    without a valid secret starts no task. Record the time from trigger
    to first output from a paused and from a stopped machine.
- **Phase I5: private beta.** Metering from InstaBox usage, the API key
  broker (decision 17), the connector relay, and an external security
  review.
  - *Verification*: Metered cost matches the InstaBox bill within 2
    percent. A cell cannot act on another tenant's machine. Deletion
    removes the machine, snapshots, backups, cell data, and the wrapped
    key of a test tenant. No high or critical finding stays open.
- **Later phase O: own compute.** Do it when the cost, a supplier risk,
  or a need (UDP, `arm64`, a region) makes it necessary. The phases C0 to
  C6 in [cloud-own-compute.md §11](cloud-own-compute.md#11-phased-plan-and-verification-gates)
  apply. The cell contract of section 4 stays the same.
  - *Verification*: The phase I1 checks pass with the `chv` driver. One
    tenant moves from InstaBox to an own host through backup and restore.
