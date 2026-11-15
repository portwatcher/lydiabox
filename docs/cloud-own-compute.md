# LydiaBox Cloud on own compute: later-phase design

Status: later phase. This is the design of 2026-11-14 for KVM hosts that
the service operates. On 2026-11-15 the owner decided to build LydiaBox
Cloud first on InstaBox ([cloud-design.md](cloud-design.md)). Own compute
must reuse the cell contract of [cloud-design.md §4](cloud-design.md#4-contract-between-the-control-plane-and-a-cell).
Only sections 1.1, 1.2, 10.3, and 12 changed since 2026-11-14.

Terms: the **core** is the code in this repository, as
[design.md](design.md) specifies it. A **cell** is one core process for one
tenant on one KVM host, with its children and its VM. The **host agent** is
a new privileged daemon on each KVM host that starts, stops, and moves
cells. The **control plane** is the new central service for accounts,
billing, placement, routing, and triggers.

## 1. Goals, non-goals, and user journey

### 1.1 Present system

One `lydiabox up` process is the trusted control plane for one tenant:

- `serve` builds one store, one storage supervisor, one authenticator, and
  one API server for `cfg.TenantID` ([internal/cli/up.go](../internal/cli/up.go)).
- `cfg.TenantID` is always `"default"`. No environment variable or YAML
  key sets it ([internal/config/config.go](../internal/config/config.go),
  [internal/config/sources.go](../internal/config/sources.go)).
- All internal services accept a `tenantID`. The `chv` driver gives each
  tenant its own subnet `172.30.<n>.0/24` and TAP device
  ([internal/store/vmnet.go](../internal/store/vmnet.go),
  [internal/vmm/chv/payload.go](../internal/vmm/chv/payload.go)).
- Authentication is one admin token per process
  ([internal/api/auth.go](../internal/api/auth.go), [api.md](api.md#2-authentication)).
- The VM pauses after `LYDIABOX_IDLE_TIMEOUT` (default 20 minutes) without
  a PTY or desktop lease. Pause keeps the guest RAM allocated
  ([internal/vmm/controller.go](../internal/vmm/controller.go)).
- Only a person in the web UI or the API can start work. There is no
  scheduler, webhook, or chat channel.

Cloudflare Tunnel with Access email login, a TURN relay for the desktop
stream, and subscription login on the host are now on `main`.

### 1.2 Goals

The goals are those of [cloud-design.md §2.2](cloud-design.md#22-goals).
On own compute, the service also guarantees that files, the root overlay,
and sessions persist across sleep and host failure.

### 1.3 Non-goals

- Teams, shared agents, and roles. One account has one agent.
- GPU passthrough. It is not verified in the core ([conformance.md](conformance.md#summary)).
- The `docker` VM driver. A container shares the host kernel, so it is not
  a sufficient tenant boundary. The cloud uses `chv` only.
- More than one region at launch, a new guest image, or a new harness.

### 1.4 User journey

1. **Sign up** with email, Google, or GitHub. The control plane makes an
   account and a tenant ID.
2. **Configure** a harness and an LLM endpoint (base URL, API key, model).
   Subscription login is possible only if decision 7 permits it.
3. **Provision.** The control plane makes the storage, keys, and DNS name,
   and places a cell. The cell formats the volume.
4. **Use** `https://<tenant>.<cloud domain>/`: the core web UI of the cell.
5. **Observe and take over** the desktop through the TURN relay.
6. **Work without a browser.** A schedule, webhook, or email starts a
   task. The cell wakes the VM, runs the task, and lets the VM sleep.
7. **Persist, export, delete.** Data persists until the user deletes it.

## 2. Tenancy model

### 2.1 Options

- **A. Process per tenant (cells).** Each tenant gets its own core
  process, store, master key, magpie, JuiceFS mount, and VM. A host agent
  starts the cells. A control plane routes to them.
- **B. One multi-tenant process per host.** One core process serves all
  tenants of a host. It gets the tenant from the identity of each request.

### 2.2 Comparison

| Criterion | A. Process per tenant | B. Multi-tenant process |
| :--- | :--- | :--- |
| Isolation | OS process boundary. A bug in one cell affects one tenant. Each cell can run as its own Unix user. | One address space. `tenant.FromContext` returns `"default"` when the context has no tenant ([internal/tenant/tenant.go](../internal/tenant/tenant.go)), so a missing tenant fails open. A crash stops all tenants of the host. |
| Secret exposure | A cell holds only its own master key, LLM keys, and OAuth tokens. | The process holds the keys of all tenants of the host. One memory disclosure exposes all of them. |
| Magpie | One magpie per tenant, as tested. | magpie has one gateway with named keys ([internal/magpie/gatewaykey.go](../internal/magpie/gatewaykey.go)) and no separation of provider keys by tenant. One magpie per tenant is option A for this part. |
| JuiceFS | One volume and mount per tenant. | Also one volume (`lydiabox-<tenant>`) and mount per tenant. No saving. |
| Memory per tenant | Go process, magpie, WebDAV and S3 gateway children: estimate 150 to 300 MiB, to measure in Phase C1. | Saves most of that memory. |
| Dominant cost | Guest RAM (default 8192 MiB) and vCPUs. Same for both. | Same. |
| Operations | Many small processes. Restart, upgrade, canary, and move one tenant at a time. | Fewer processes. An upgrade or a crash affects all tenants of a host. A move needs a per-tenant export from a shared process. |
| Change to the tested core | Small opt-in settings (section 10). The KVM runs stay valid. | Large: per-request identity, per-tenant S3 credentials, master keys, magpie, and connector clients, and a new test matrix. |

### 2.3 Recommendation

Use option A. The coordinator recommended it, and the analysis supports
it:

1. The density gain of B is small. The guest VM is more than 90 percent of
   the memory of a running tenant (estimate), and both options need one
   JuiceFS mount per tenant.
2. A keeps a bug or a secret leak to one tenant. B puts the secrets of
   all tenants of a host in one process.
3. A uses the core as a binary with settings. Go does not permit another
   module to import the core `internal/` packages, and A does not need it.

Option A needs core changes. Today two cells on one host collide, because
each process uses the tenant `"default"`:

- Both get subnet index 0 and the same TAP name `lbx<hash("default")>`.
- Both use the nftables table `lydiabox_<tap>` and DNAT the same host UDP
  port for WebRTC (`LYDIABOX_WEBRTC_UDP_PORT`, default 50000,
  [internal/vmm/chv/network.go](../internal/vmm/chv/network.go)).
- `chv` needs root or `CAP_NET_ADMIN` for TAP and NAT
  ([README.md](../README.md#cloud-hypervisor-chv-linux-with-kvm)). A cell
  that runs as root weakens the process boundary.

Section 10.3 removes these collisions. To lower the cost of dormant
tenants, the host agent stops the whole cell (not only the VM) after a
long idle time. A request or a trigger starts it again.

## 3. Control plane

### 3.1 Components

| Component | Function |
| :--- | :--- |
| Accounts | Sign-up, login, email verification, deletion. Uses a hosted identity provider. |
| Dashboard | Harness and LLM setup, triggers, usage, billing, export. Links to the cell web UI. |
| Tenant registry | Tenant ID, state, placement, storage location, wrapped keys, plan, quotas. PostgreSQL. |
| Scheduler | Selects a KVM host for each cell from free RAM, free vCPU, and host health. |
| Host agent | Starts and stops cells; owns TAP devices and nftables; reports capacity and usage. |
| Router | Public HTTPS and WebSocket entry. Authenticates the user and forwards to the cell. |
| TURN service | Relays desktop media with short-lived credentials for each tenant. |
| Trigger service | Schedules, webhooks, inbound email, chat (section 6.2). |
| OAuth relay | Service-owned OAuth clients for connectors (section 5.4). |
| Metering and billing | Usage records to the billing provider (section 8). |

### 3.2 Login and request routing

1. The user logs in at the control plane, which sets a session cookie for
   its own domain.
2. Each tenant has its own subdomain. The core cookie has `Path=/`, and the
   core accepts a cookie state change only when `Origin` equals `Host`
   ([api.md](api.md#2-authentication)). A subdomain for each tenant keeps
   these rules correct without a change.
3. The router verifies the session, finds the cell in the registry, and
   forwards the request with a signed short-lived JWT (tenant ID, user
   ID). It removes all identity headers from the client.
4. The cell verifies the JWT in a new trusted-proxy auth mode and checks
   that the tenant claim equals its own tenant ID. The Cloudflare Access
   task adds a check of `Cf-Access-Jwt-Assertion`. Use one verifier with a
   configured issuer, audience, and key set for both.
5. If the cell is stopped, the router asks the host agent to start it and
   holds the request for up to 30 seconds, then shows a wait page.

WebSocket routes (`/api/v1/sessions/{id}/ws`, `/api/v1/desktop/ws`) use the
same router, which must keep long connections and not buffer frames.

### 3.3 WebRTC media

Today the guest sends media through one host UDP port that the host DNATs
to the guest ([internal/guest/webrtc/streamer.go](../internal/guest/webrtc/streamer.go)).
The `hello` message gives the browser Google STUN only
([internal/desktop/relay.go](../internal/desktop/relay.go)). In the cloud:

- Send all desktop media through the planned TURN relay. The guest
  connects out to TURN, so a cell needs no public UDP port (port `0`).
- The control plane issues TURN credentials with a time limit (TURN REST
  style: username `<expiry>:<tenant>`, HMAC password). The cell sends them
  in `hello`.
- TURN denies relay to private ranges and counts bytes for each username.

### 3.4 Tenant lifecycle

| Operation | Steps |
| :--- | :--- |
| Provision | Make the tenant ID, the storage prefix and its scoped credentials, the metadata database, and the tenant data key. Place the cell; it formats the volume and makes `root-upper.img`. Push the LLM configuration. |
| Suspend | Stop triggers. Stop the VM with flush (`POST /api/v1/vm/stop`), stop the cell, keep all data. For unpaid accounts and abuse. |
| Resume | Place and start the cell. The volume mounts from the shared metadata engine. |
| Export | Start a cell without a VM if necessary. Give a time-limited archive of `/workspace` and `/home/agent`, and `root-upper.img` on request. No secrets. |
| Delete | Suspend. Revoke connector tokens at the providers. Delete the tenant objects, the metadata database, and the registry entry. Destroy the wrapped data key last, so copies in backups cannot be decrypted. Keep a deletion record without content. |

### 3.5 Placement, host failure, and migration

- Place a cell on the healthy host with the most free guest RAM that has
  the guest images of the current version.
- The host agent sends a heartbeat every 10 seconds. After 60 seconds
  without one, the control plane fences the host (the host agent stops its
  cells on self-fence; the metadata engine revokes the host's sessions)
  and places its cells on other hosts.
- Drain: stop the VM with flush, stop the cell, start it on the new host.
  Only one cell can mount a volume at a time, because `root-upper.img` is
  an ext4 image on JuiceFS
  ([design.md](design.md#43-serverless-agent-vm-engine-internalvmm--deploy)).
  Two writers corrupt it.
- No live migration. A drained tenant sees a VM restart. Running PTYs end;
  transcripts stay and can be resumed ([api.md](api.md#post-apiv1sessions)).

### 3.6 Control plane to cell API

The host agent calls the cell over a Unix socket with a service
credential, which never leaves the host. It uses existing routes where
possible:

| Use | Existing route |
| :--- | :--- |
| Health and state | `GET /api/v1/health`, `GET /api/v1/status` |
| VM power | `POST /api/v1/vm/wake`, `/hibernate`, `/stop` ([api.md](api.md#7-vm-power--implemented-phase-2)) |
| LLM configuration | `/api/v1/models/providers`, `/api/v1/models/bindings/{harness}` ([api.md](api.md#10-models-and-magpie--implemented-phase-3)) |
| Export | `/api/v1/fs/*`, `/webdav/` |

New routes (section 10.3): `POST /api/v1/tasks` (a headless harness run
with a prompt, which holds a new `task` lease), `GET /api/v1/metering`
(counters since a cursor), and `PUT /api/v1/desktop/ice-servers`.

## 4. Storage

### 4.1 Present system

- One bucket per process. JuiceFS objects go under the volume name
  `lydiabox-<tenant>`; backups go under `.lydiabox-system/tenants/<tenant>/`
  ([internal/storage/objectstore.go](../internal/storage/objectstore.go)).
- Metadata is local SQLite. A ticker uploads `jfs-meta.json.gz` and
  `lydiabox.db.gz` every 60 seconds when dirty
  ([internal/storage/metasync.go](../internal/storage/metasync.go)).
- The mount uses `--writeback`
  ([internal/storage/juicefs.go](../internal/storage/juicefs.go)): writes go
  to the local cache first and upload later.
- The backup does not include `state/magpie`
  ([internal/config/layout.go](../internal/config/layout.go)), so LLM
  provider keys are lost with the host disk.

On host loss, the core can lose up to 60 seconds of metadata and all
writeback data that did not upload. One self-hosted server can accept
this risk. A service that moves tenants cannot.

### 4.2 Bucket and prefix

| Option | Advantages | Disadvantages |
| :--- | :--- | :--- |
| Service bucket, prefix per tenant | Simple provisioning, quotas, deletion, one lifecycle policy | Each tenant credential needs an IAM policy for its prefix. Bucket limits are shared. |
| Service bucket per tenant | Strong separation, simple deletion | Account limits on the bucket count |
| User-owned bucket | Matches the core pillar "the user owns the data" | No quota control, no availability guarantee, the service holds user credentials |

Recommendation: a service bucket with a prefix and scoped credentials for
each tenant. Put the volume objects and the backups under one root,
`tenants/<tenant>/`, so one IAM statement covers both. Offer a user-owned
bucket later.

### 4.3 Metadata engine

| Option | Loss on host failure | Migration | Cost and risk |
| :--- | :--- | :--- | :--- |
| Local SQLite + 60 s backup (today) | Up to 60 s of metadata | Restore the dump; slow for large volumes | No new service; tested |
| PostgreSQL, one database per tenant | None for committed metadata (synchronous replica) | Mount on the new host; seconds | Network latency on each metadata operation |
| Redis | Depends on persistence settings | Mount on the new host | Weaker durability |
| TiKV | None | Mount on the new host | Highest operational cost; for very large scale |

Recommendation: managed PostgreSQL with high availability, one database
and one database user per tenant. Keep SQLite for self-hosted mode;
select the engine with a setting. Measure `git status` and `npm install`
in Phase C3, and tune the JuiceFS metadata cache if necessary. Also turn
off or bound `--writeback` in the cloud, so a host loss loses no
acknowledged write (decision 5).

### 4.4 Quotas and deletion

Set a JuiceFS capacity quota (`juicefs quota`) from the plan. Count the
allocated bytes of the 40 GiB sparse `root-upper.img`, not its size. The
cache (`--cache-size`, 10240 MiB today) is per cell: put it on local NVMe
and count it in placement. Delete a large prefix as a background job.

## 5. Secrets

### 5.1 Present system

- `LYDIABOX_MASTER_KEY` (32 bytes) encrypts connector tokens with
  AES-256-GCM ([internal/store/crypto.go](../internal/store/crypto.go)). It
  is a `0600` file in `state/` ([internal/config/secrets.go](../internal/config/secrets.go)).
- magpie keeps the upstream LLM keys in its data directory on the host.
  The guest gets only a revocable gateway key, which the host side of the
  vsock tunnel adds ([internal/magpie/gatewaykey.go](../internal/magpie/gatewaykey.go),
  [internal/guest/credproxy](../internal/guest/credproxy)).

### 5.2 Keys for each tenant

Use envelope encryption with a key management service (KMS):

1. The KMS keeps one key-encryption key for each region. It never leaves
   the KMS.
2. Each tenant has a 32-byte data key. The registry stores it only
   wrapped, with the tenant ID as encryption context.
3. Only host-agent identities can unwrap, and only for tenants placed on
   their host. The host agent gives the key to the cell as
   `LYDIABOX_MASTER_KEY` in a memory-only file, never on a command line.
4. The control plane keeps the LLM configuration, encrypted with the data
   key, as the source of truth. The host agent pushes it through
   `/api/v1/models/providers` at each cell start. This removes the magpie
   loss of section 4.1 without a magpie change.

### 5.3 LLM keys and subscription tokens

API keys follow section 5.2 and stay on the host. Subscription tokens
(if decision 7 permits them) are encrypted in the cell store.

### 5.4 Connector OAuth with service-owned clients

The core supports a relay. With `LYDIABOX_OAUTH_RELAY_URL` set and no
local client, authorize goes to `<relay>/v1/<provider>/authorize` and the
code exchange posts to `<relay>/v1/<provider>/token`
([internal/connector/manager.go](../internal/connector/manager.go),
[internal/connector/oauth.go](../internal/connector/oauth.go)). The client
secret stays in the relay. The cloud relay must also:

- **Own the redirect.** Providers accept only registered redirect URIs.
  The relay registers one callback per provider, keeps the tenant
  `redirect_uri` in a short-lived record keyed by `state`, and then
  redirects the browser to the tenant callback with the code and state.
- **Allow-list redirects.** Accept only `redirect_uri` values on the
  tenant domains. Else it is an open redirect.
- **Authenticate cells.** The core sends no credential to the relay
  today. Add a relay token for each cell.
- **Refresh** through the relay token endpoint (it needs the secret).

Tokens stay encrypted in the cell. The relay does not store them.

### 5.5 What operators can and cannot read

| Data | Control-plane operator | Root on a KVM host |
| :--- | :--- | :--- |
| Email, plan, usage | Can read | Cannot |
| Wrapped keys, LLM configuration | Cannot unwrap without a KMS permission that operators do not have | Can read the keys of cells on that host |
| Bucket objects | Can read the chunks; JuiceFS encryption blocks this | Can read the mounted files of cells on that host |
| Guest screen and memory | Cannot | Can |

Software cannot stop root on a host from reading the cells on it, except
with confidential VMs (AMD SEV-SNP, Intel TDX), which Cloud Hypervisor
supports only in part. Say this in the privacy policy. Lower the risk: no
standing root access, two-person break-glass with an audit record, and
hosts rebuilt from an image.

## 6. Online 24/7

### 6.1 Cost of an always-running VM

Most personal agents are idle most of the day. A running or paused VM
occupies its full guest RAM (`Pause` in
[internal/vmm/controller.go](../internal/vmm/controller.go)).

| Mode | Host RAM while idle | Time to first response |
| :--- | :--- | :--- |
| Always running | Full guest RAM | None |
| Paused (today) | Full guest RAM | Resume (not measured) |
| Snapshot to local disk (new: Cloud Hypervisor `vm.snapshot`, `vm.restore`) | None | Restore; depends on RAM size and disk speed |
| VM stopped (today) | None | Wake from `STOPPED` in 3.7 s on amd64 ([conformance.md](conformance.md#integration-run-chv-on-kvm-amd64)), plus desktop start |
| Cell stopped (new) | None, no cell processes | Cell start, mount, then VM wake |

Recommendation: "online 24/7" means "always reachable", not "always
running". Use a ladder: pause after 5 minutes idle, stop the VM after 1
hour, stop the cell after 24 hours. Each request or trigger wakes the
tenant. Sell "always running" as a paid option.

### 6.2 Trigger layer

Today no work starts without a person, and a session is an interactive
PTY. The trigger layer is new:

| Trigger | Entry | Action |
| :--- | :--- | :--- |
| Schedule | Trigger service (cron) | Calls `POST /api/v1/tasks` at the time |
| Webhook | `https://hooks.<cloud domain>/<secret path>` | Verifies the secret or signature, starts a task with the payload |
| Inbound email | One address per tenant | Accepts mail from verified user addresses only; starts a task |
| Chat (Slack, Telegram) | Trigger service | Maps the chat user to a tenant, starts a task, posts the result |

`POST /api/v1/tasks` runs the harness in its headless mode (for example
`claude -p` or `codex exec`) in a PTY. It holds a `task` lease until the
run ends and keeps the transcript, so the user can open it in the Studio.
The trigger service limits concurrent tasks for each tenant and reaches
the cell through the host agent (section 3.6).

## 7. Harness authorization

The dashboard offers two paths:

1. **LLM endpoint (default).** Provider kind, base URL, API key, and
   model. The control plane stores it (section 5.2) and pushes it to
   magpie, which serves all five harnesses
   ([design.md](design.md#44-magpie-integration--multi-harness-sessions-internalmagpie--internalharness)).
   API keys are for programmatic use by the key owner, so this is the safe
   default.
2. **Subscription login (optional).** The user signs in to a Claude or
   ChatGPT subscription. The planned host-login task adds this for
   self-hosted use, where the user runs the software.

Risk of path 2 in a hosted service: consumer subscriptions are for
personal use in the provider's clients. Their terms can forbid use through
a third-party service, credential sharing, or automated use. In the cloud,
the service holds the credentials and runs the client. A provider can
block the accounts or the service. Do not offer path 2 in the cloud until
a legal review of each provider's current terms permits it, or a provider
offers a supported partner path (decision 7).

## 8. Metering, abuse controls, and egress

### 8.1 Metering inputs

| Input | Source | State |
| :--- | :--- | :--- |
| VM time (vCPU and GiB seconds, by state) | Controller state changes; cgroup counters | State changes exist; export is new |
| Cell time | Host agent | New |
| Storage bytes | JuiceFS used space; object bytes of the prefix | New |
| Guest egress bytes | nftables counters on the tenant TAP | New rules in `lydiabox_<tap>` |
| TURN relay bytes | TURN accounting by username | New |
| LLM tokens | magpie usage; billed only if the service sells LLM access | Usage exists in sessions ([api.md](api.md#8-sessions-and-pty--implemented-phase-3)) |
| Tasks | Trigger service | New |

The host agent sends usage records with an idempotency key every minute.

### 8.2 Abuse controls

Require email verification and a payment method before the first VM
boot; a trial gets small limits. Set plan limits for vCPU, RAM, storage,
egress, concurrent tasks, and TURN bytes. Suspend on sustained full CPU
with no session (mining). Rate-limit sign-ups and webhooks. Suspension
keeps the data.

### 8.3 Guest egress policy

The `chv` rules drop guest traffic to the host and to `10.0.0.0/8`,
`172.16.0.0/12`, `192.168.0.0/16`, `169.254.0.0/16`, `100.64.0.0/10`, and
`127.0.0.0/8` ([internal/vmm/chv/network.go](../internal/vmm/chv/network.go)).
This already blocks `169.254.169.254` and the other tenants' subnets on the
host. Add in the cloud:

- Drop outbound TCP port 25. Drop traffic to the control-plane and
  management networks. Drop IPv6, or apply the same rules to it.
- Rate-limit new outbound connections for each tenant, and use a separate
  egress IP pool for guests.

## 9. Security model and threats

The guest is not trusted: it runs code from LLMs and the internet. Trust
boundaries are browser, router, control plane, host agent, cell, guest.

| Threat | Present controls | Cloud controls |
| :--- | :--- | :--- |
| Guest escape through KVM or Cloud Hypervisor | Hardware virtualization; no host secrets in the guest | Cloud Hypervisor and `virtiofsd` as the cell Unix user, with seccomp, own cgroup and mount namespace; patched host kernel |
| Guest escape through virtio-fs | `virtiofsd` shares only `home/agent` and `workspace` | `virtiofsd` in namespace sandbox mode as the cell user |
| Guest to host services | nftables drop on the TAP input | magpie and the cell API on loopback or Unix sockets only |
| Guest to other guests or the metadata service | Private-range drop covers `172.30.0.0/16` and `169.254.0.0/16` | Test in every release (Phase C0 gate) |
| Cell to cell on one host | Not applicable (one process per host) | One Unix user per cell; no root in cells; host agent owns TAP and nftables; data directories `0700` |
| Stolen guest gateway key | Revocable; valid only through the host tunnel | Rotate at each cell start |
| Wrong-tenant routing or forged identity header | One token per process | Router derives the cell from the verified session only and removes client identity headers; cell verifies signature, issuer, audience, expiry, and tenant claim |
| Control-plane compromise | Not applicable | Cannot unwrap tenant keys; separate deploy credentials; signed host-agent commands; audit log in a separate account |
| Host-agent compromise | Not applicable | Exposes one host's tenants; KMS unwrap limited to placed tenants; frequent host rebuilds |
| OAuth relay or TURN misuse | Not applicable | Redirect allow-list, cell tokens, short-lived state (section 5.4); TURN credentials with a time limit, no private peers, byte limits |
| Prompt injection through webhook or email | Not applicable | Treat trigger content as untrusted data; show the trigger source in the session; let the user limit connectors per trigger |

## 10. Code: what stays, what changes, what is new

### 10.1 Repository

Put the control plane, host agent, router, trigger service, OAuth relay,
and dashboard in a separate private repository (for example
`lydiabox-cloud`). The cloud runs the core as a pinned binary, so it does
not need the core `internal/` packages. The core stays complete without
cloud code. Billing and abuse rules change often and do not help
self-hosters.

Keep the changes of section 10.3 in this repository. Each one is an
opt-in setting whose default keeps self-hosted behavior. The existing
tests and the [KVM checklist](kvm-test.md) must pass unchanged. Document
the cell contract (settings and routes) in [api.md](api.md).

### 10.2 Core code that stays the same

`internal/storage/files`, `internal/magpie`, `internal/guest/*`,
`cmd/lydiabox-guest`, `deploy/`, `web/`, and most of `internal/storage`,
`internal/harness`, and `internal/desktop`.

### 10.3 Core code that changes (opt-in)

The InstaBox path adds cell mode and the guest link
([cloud-design.md §3.1](cloud-design.md#31-core-changes-in-this-repository)).

| Package | Change |
| :--- | :--- |
| `internal/config` | `LYDIABOX_TENANT_ID`; `LYDIABOX_META_URL` (JuiceFS metadata URL, default local SQLite); `LYDIABOX_JUICEFS_WRITEBACK`; `LYDIABOX_VM_NETWORK` (TAP, subnet, host IP from the host agent); `LYDIABOX_OAUTH_RELAY_TOKEN`; a `unix:` form of `LYDIABOX_LISTEN_ADDR` |
| `internal/vmm/chv` | With `LYDIABOX_VM_NETWORK`, use the host agent's TAP and skip nftables, so the cell needs no root. `vm.snapshot` and `vm.restore`. |
| `internal/vmm` | `task` lease kind; configurable pause, stop, and snapshot times |
| `internal/storage` | Use `LYDIABOX_META_URL`; skip the SQLite dump with a shared engine but keep the `lydiabox.db` backup; object prefix setting |
| `internal/api` | Trusted-proxy JWT auth (shared with the Cloudflare Access check); Unix-socket listener |
| `internal/connector` | Send the relay token to the relay |
| `internal/desktop` | Route to replace the ICE servers |
| `internal/harness` | Headless task runs, `POST /api/v1/tasks` |
| `internal/cli` | Wire the settings; `GET /api/v1/metering` |

### 10.4 New code (cloud repository)

| Package | Function |
| :--- | :--- |
| `cmd/lbx-control` | Control-plane API, registry, scheduler, KMS client |
| `cmd/lbx-hostagent` | Cells, TAP and nftables, usage export, fencing |
| `cmd/lbx-router` | HTTPS and WebSocket router, identity JWTs |
| `cmd/lbx-trigger` | Schedules, webhooks, email, chat |
| `cmd/lbx-oauthrelay` | OAuth relay (section 5.4) |
| `internal/billing` | Usage records, billing provider |
| `web-dashboard/` | Sign-up, harness setup, triggers, usage |

TURN uses an existing server (for example coturn), set up by the TURN task.

## 11. Phased plan and verification gates

Do the phases in sequence. Start a phase after the previous gate passes.

- **Phase C0 — Core seams in this repository**: the settings and routes
  of section 10.3.
  - *Verification*: `CGO_ENABLED=0 go test -race ./...`, `go vet ./...`,
    and the KVM checklist pass with default settings. On one KVM host, two
    non-root cells with different tenant IDs run at the same time. A guest
    cannot reach the other guest, its host IP, or `169.254.169.254`.
- **Phase C1 — Host agent and cells on one host**, from a static list.
  - *Verification*: 10 cells on one host. A killed cell does not stop the
    others and restarts with no data loss. The C0 isolation tests pass for
    each pair. Cell memory without a VM and with a paused VM is recorded.
- **Phase C2 — Control plane, router, and dashboard**: accounts, registry,
  scheduler, router, trusted-proxy auth, TURN credentials, LLM setup.
  - *Verification*: A new user signs up, enters an LLM endpoint, runs a
    harness session, and takes over the desktop through TURN from a
    network without inbound UDP. A forged identity header and a request
    for another tenant fail with `401` or `403`.
- **Phase C3 — Durable storage, keys, and migration**: shared metadata
  engine, scoped credentials, KMS keys, writeback policy, drain, recovery.
  - *Verification*: Write a file and cut the host power; the tenant runs
    on another host within 5 minutes with the file. Drain a host with 10
    tenants. Record `git status` and `npm install` times against SQLite. A
    tenant credential cannot list another tenant prefix.
- **Phase C4 — Triggers and 24/7**: task API, sleep ladder, snapshot,
  schedules, webhooks, email.
  - *Verification*: A scheduled task runs with no browser, its transcript
    shows in the Studio, and the VM sleeps again. A webhook without a
    valid secret starts no task. Trigger-to-harness time is recorded for
    each sleep level.
- **Phase C5 — Connector relay, metering, billing, abuse**.
  - *Verification*: GitHub connects through the relay for two tenants. A
    relay call without a cell token fails, and so does a `redirect_uri`
    outside the tenant domains. Usage records match a controlled workload
    within 2 percent. Port 25 is blocked.
- **Phase C6 — Security review and private beta**: external penetration
  test of guest escape, cross-tenant paths, router, and relay.
  - *Verification*: No open high or critical findings. Deletion removes
    all objects, the metadata database, and the wrapped key of a test
    tenant. An export opens on another machine.

## 12. Open decisions

**R** is the recommendation. The current status of each decision is in
[cloud-design.md §8](cloud-design.md#8-open-decisions). The owner decided
decisions 2 and 9 on 2026-11-15: a separate private repository
(`lydiabox-cloud`), and Cloudflare Access email verification
on `lydiabox.com` as the first login.

1. **Tenancy model.** (a) Process per tenant behind a control plane; (b)
   one multi-tenant process per host. **R**: (a), see section 2.3.
2. **Cloud code location.** (a) Separate private repository that runs a
   pinned core binary; (b) `cloud/` here. **R**: (a), core seams here.
3. **Core license.** There is no license file. (a) MIT or Apache-2.0; (b)
   AGPL-3.0; (c) source-available. **R**: decide before launch; (b) to
   stop other hosted copies, (a) for adoption.
4. **Bucket ownership.** (a) Service bucket, prefix per tenant; (b) bucket
   per tenant; (c) user-owned bucket. **R**: (a), later (c) as an option.
5. **Metadata engine and writeback.** (a) SQLite with backup; (b)
   PostgreSQL, one database per tenant; (c) TiKV; writeback on, off, or
   bounded. **R**: (b) with writeback off, if C3 latency is acceptable.
6. **"Online 24/7".** (a) VM always running; (b) always reachable, wake on
   request and triggers; (c) (b) by default, (a) as a paid plan. **R**: (c).
7. **Subscription credentials in the cloud.** (a) Never, self-hosted only;
   (b) after a legal review of each provider's terms; (c) at launch.
   **R**: (a) at launch, then (b). The LLM endpoint is the default.
8. **LLM supply.** (a) User brings a key or endpoint; (b) the service
   sells LLM access through its magpie. **R**: (a), later (b).
9. **Login.** (a) Hosted identity provider (email links, Google, GitHub);
   (b) own passwords; (c) Cloudflare Access for all. **R**: (a); keep
   Cloudflare Access for self-hosted users.
10. **Tenant address.** (a) Subdomain per tenant; (b) path prefix. **R**:
    (a), so the core cookie and `Origin` rules work unchanged.
11. **Sleep ladder.** (a) Pause only (today); (b) pause, stop VM, stop
    cell; (c) pause, snapshot, stop cell. **R**: (b) in C2, (c) after C4.
12. **First trigger channels.** Schedule, webhook, email, Slack, Telegram.
    **R**: schedule and webhook, then email, then chat.
13. **Guest egress default.** (a) Open internet with the block list of
    section 8.3; (b) domain allow-list. **R**: (a) with rate limits.
14. **Operator access.** (a) Standing root on hosts; (b) no standing
    access, two-person audited break-glass; (c) (b) plus confidential VMs.
    **R**: (b); examine (c) when Cloud Hypervisor SEV-SNP or TDX matures.
15. **Default VM size.** (a) 4 vCPU, 8 GiB (core default); (b) 2 vCPU,
    4 GiB with a paid upgrade. **R**: (b), if C1 shows that Chrome and a
    harness run well in it.
