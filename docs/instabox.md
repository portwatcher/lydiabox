# InstaBox: API client and guest image

LydiaBox Cloud v1 runs each guest on an [InstaBox](https://instabox.sh)
machine: a hosted Firecracker microVM (`linux/amd64`) with a persistent
disk at `/data`. The core reaches it through a remote `vmm.Driver` (a
later task; [cloud-instabox.md](cloud-instabox.md) maps the driver and
[cloud-design.md](cloud-design.md) the service). This page covers the two
parts that driver builds on:

- `internal/instabox`: a typed Go client for the InstaBox REST API, with a
  fake server (`internal/instabox/instaboxtest`) for tests.
- `deploy/instabox`: the LydiaBox desktop image that InstaBox imports as a
  snapshot.

Nothing here was checked against the real service: no account was used.
[What the documentation does not state](#what-the-documentation-does-not-state)
lists the open facts, and [First check with a real account](#first-check-with-a-real-account)
is the checklist for the first run.

## Sources

All facts come from the public documentation and the public OpenAPI
document, read on 2026-11-15:

- OpenAPI document: <https://api.instabox.sh/v1/openapi.json> (OpenAPI
  3.1, `info.version` 1.0.0). The client records its SHA-256 in
  `instabox.OpenAPISHA256` and a fingerprint of the operations and schemas
  it uses in `internal/instabox/testdata/openapi-fingerprint.json`.
- Agent index: <https://instabox.sh/llms.txt>; every page in one file:
  <https://instabox.sh/llms-full.txt>.

The document changes often (on 2026-11-15 its hash changed within an
hour, when the exec keepalive was added), so the drift check compares the fingerprint, not
the hash:

```bash
LYDIABOX_INSTABOX_DRIFT=1 go test ./internal/instabox -run TestOpenAPIDrift -v
```

A failure names each operation or schema whose method, path, header
parameters, response codes, fields, required fields, or enum values
changed. After a review of the change, `-update-openapi` rewrites the
fingerprint.

## API facts the client uses

| Fact | Source |
| :--- | :--- |
| Base URL `https://api.instabox.sh`, paths under `/v1`, JSON bodies in camelCase, RFC 3339 UTC times. | [API overview](https://instabox.sh/docs/api-reference/introduction.md) |
| Authentication: `X-API-Key: ibk_...` or `Authorization: Bearer ibk_...`; 401 codes `missing_key`, `malformed_key`, `invalid_key`, `expired_key`, `revoked_key`; a key without the scope gets `403 missing_scope`. | [API overview](https://instabox.sh/docs/api-reference/introduction.md#authentication-errors) |
| Scopes: `machines:read`, `machines:write` (create, PATCH, lifecycle, exec, files), `machines:delete`, `images:read`, `images:write`, `images:delete`. | OpenAPI `info.description`, "API keys" |
| `Idempotency-Key` (1-200 printable ASCII) is required on create, PATCH, start, stop, suspend, resume, machine delete, snapshot create, and snapshot delete. The same key and request replays the first answer; the same key with another request is `409 idempotency_conflict`; records last 7 days. | OpenAPI "Idempotency"; [API overview](https://instabox.sh/docs/api-reference/introduction.md#idempotency) |
| Exec, wake, activity leases, port publish, and file upload take no `Idempotency-Key`. A retried exec runs the command again. | OpenAPI operation descriptions |
| A machine delete and a snapshot delete need `X-Confirm-Delete` with the ID or exact name, else `409 delete_confirmation_required`. | [API overview](https://instabox.sh/docs/api-reference/introduction.md#confirming-deletes) |
| Errors: `{"error": {"code", "message", "requestId", "retryable", "operationId"}}`, plus `retryAfterSeconds` and the `Retry-After` header for `no_capacity`, `rate_limited`, and `machine_cooldown`. Branch on `code`. | [API overview](https://instabox.sh/docs/api-reference/introduction.md#errors), [Errors](https://instabox.sh/docs/reference/errors.md) |
| Lifecycle calls answer `202` with an operation (`queued`, `running`, `succeeded`, `failed`); `GET /v1/operations/{id}?wait=25s` long-polls (capped at 30 s). One operation at a time per machine: another change is `409 operation_conflict` with `operationId`; the same change returns the running operation; a change already in effect returns a `succeeded` operation. | OpenAPI "Sandbox lifecycle", `getOperation` |
| Machine states: `creating`, `starting`, `running`, `stopping`, `stopped`, `suspending`, `suspended` (paused), `resuming`, `deleting`, `deleted`, `error`. `runtime.ready` is true when exec, files, and SSH work. | OpenAPI schemas `MachineState`, `Runtime` |
| Pause (`suspend`) keeps memory and processes; `start` on a paused machine resumes it; `stop` on a paused machine is `409 operation_conflict`. If programs cannot be restored, the machine boots fresh and `runtime.processesPreserved` is false; files stay. | OpenAPI "Sandbox lifecycle" |
| `/data` (home `/data/home`), 20 GiB by default, is kept across stop, pause, and start. `/tmp` is a RAM tmpfs. | OpenAPI "Sandboxes"; [llms.txt](https://instabox.sh/llms.txt) "Machine", "/tmp" |
| Idle pause: `autoSuspendAfterSeconds`, default 1800; paid plans take 300-1800 in whole minutes or 0 (off); the free plan only 1800 (`403 spec_not_allowed` otherwise). Exec, files, terminal, SSH certificates, leases, and wake resume a machine that paused itself (`autoSuspendedAt` set); a machine paused or stopped by a call stays down (`409 machine_not_running`). | [Auto-pause](https://instabox.sh/docs/machines/auto-pause.md), OpenAPI `AutoSuspendAfterSeconds` |
| Maximum run: `maxRunMinutes`, -1 unlimited (paid default); the free plan starts each run at 120 min and grows to 480 min with use. | [Auto-pause](https://instabox.sh/docs/machines/auto-pause.md#set-a-maximum-runtime), OpenAPI `MaxRunMinutes` |
| Activity leases: `PUT /v1/machines/{id}/activity-leases/{leaseID}` with `ttlSeconds` 30-3600 keeps a machine from its idle pause (and wakes it); `DELETE` releases it. At most 128 per machine. | [Auto-pause](https://instabox.sh/docs/machines/auto-pause.md#keep-a-machine-awake-during-a-silent-task) |
| Exec: `cmd` (run with `bash -lc`) or `argv`, `cwd`, `envs`, `stdin` (UTF-8, at most 1 MiB), `timeoutSeconds` (default 60, at most 600). Each output stream is cut at 1 MiB. `Accept: text/event-stream` streams `stdout`/`stderr` events, then `exit` or `error`, with `: ping` every 15 s; closing the stream kills the command. `X-Instabox-Exec-Keepalive: 1` makes a long non-streamed exec answer 200 early, send newlines, then the result or `{"error", "status"}`. | OpenAPI `execCommand`, `ExecRequest`, `ExecResult` |
| At most 32 (free), 64 (Pro), or 256 requests at once per user for exec, files, and desktop; more is a retryable `429 quota_exceeded`. Starts: 3 (free) to 100 (Team) a minute, more is `429 rate_limited`. | OpenAPI `execCommand`; [Limits](https://instabox.sh/docs/account/limits.md) |
| Files: `PUT /v1/machines/{id}/files?path=&mode=&parents=` streams raw bytes (up to 5 GiB), renames into place only when complete, writes as the machine user (no sudo); `GET .../files/content?path=` downloads with `X-Instabox-File-Mode` and `ETag`; `GET .../files?path=` lists a directory or returns a text file up to 256 KiB. | OpenAPI `writeMachineFile`, `downloadMachineFile`, `getMachineFiles` |
| Ports: `POST /v1/machines/{id}/ports` with `access: private` gives `https://<port>-<id without m_>.<domain>` that answers only with the port token (`ibp_...`): `?_token=` once (sets a cookie), the cookie, or `Authorization: Bearer`. The token is returned only by the publish that created or rotated it. Ports 1024-65535 except 5901 and 6768; at most 16 per machine. A request without the token does not wake the machine. WebSockets work through the cookie. | OpenAPI `publishPort`, `PublicPort`; [Ports](https://instabox.sh/docs/access/ports.md) |
| Snapshots: `POST /v1/machines/{id}/snapshots` (`201` ready for a stopped machine, `202` pending for a running one), `GET /v1/snapshots`, `GET`/`DELETE /v1/snapshots/{id}`. Memory and programs are not saved. A machine created from a snapshot gets the plan's default size unless `vcpus`/`memMiB` are given. | OpenAPI "Templates and snapshots"; [Snapshots](https://instabox.sh/docs/machines/snapshots.md) |
| Snapshot import is a CLI flow, not an API call: `instabox snapshot import IMAGE --name N` starts a throwaway `agent-browser` machine, pulls the image with Docker, lays its files over the machine, restarts it to check it, saves a snapshot, and deletes the machine. The CLI reads `INSTABOX_API_KEY`, `INSTABOX_API_URL`, and `INSTABOX_REGISTRY_PASSWORD`; `--json` prints `{"error": {...}}` on failure. | [Custom images](https://instabox.sh/docs/machines/custom-images.md); [llms-full.txt](https://instabox.sh/llms-full.txt) "CLI reference" |
| Sizes (vCPU/GiB): free 4/8 only; Pro 2/4, 4/8, 8/16; Builder up to 16/32; Team up to 32/64. Outbound bandwidth is about 50 Mbit/s per VM. | [llms.txt](https://instabox.sh/llms.txt), [Limits](https://instabox.sh/docs/account/limits.md) |

## Client API

Package `github.com/lydiabox/lydiabox/internal/instabox` (no dependency on
`internal/vmm`).

- `NewClient(Options{BaseURL, APIKey, HTTPClient, MaxRetries, RetryBaseDelay, MaxRetryDelay, UserAgent, Logger, Sleep}) (*Client, error)`
- Machines: `CreateMachine`, `GetMachine`, `ListMachines`, `ListAllMachines`,
  `UpdateMachine`, `SetIdlePause`, `SetMaxRun`, `StartMachine`,
  `StopMachine`, `PauseMachine`, `ResumeMachine`, `WakeMachine`,
  `DeleteMachine`, `GetOperation`, `WaitOperation`, `PutActivityLease`,
  `DeleteActivityLease`.
- Commands and files: `Exec`, `ExecStream`, `PutFile`, `PutFileBytes`,
  `GetFile`, `ReadFile`, `StatFile`.
- Ports: `PublishPort`, `PublishPrivatePort`, `ListPorts`, `UnpublishPort`.
- Snapshots: `CreateSnapshot`, `ListSnapshots`, `GetSnapshot`,
  `FindSnapshot`, `DeleteSnapshot`, `WaitSnapshot`, `ImportSnapshot`
  (runs the `instabox` CLI), `ImportArgs`.
- Types: `Machine`, `State` (and `State*` constants), `Operation`,
  `OperationType`, `OperationState`, `MachineSpec`, `Storage`, `Runtime`,
  `CreateMachineRequest`, `UpdateMachineRequest`, `StartOptions`,
  `StopOptions`, `ExecRequest`, `ExecResult`, `FileWritten`,
  `FileContent`, `FileInfo`, `PublishPortRequest`, `PublicPort`,
  `Snapshot`, `CreateSnapshotRequest`, `ActivityLease`, `ImportOptions`.
- Errors: `*APIError` (status, `ErrorDetail`, `RetryAfter`, `Operation`),
  matched with `errors.Is` to `ErrUnauthorized`, `ErrForbidden`,
  `ErrPaymentRequired`, `ErrNotFound`, `ErrConflict`, `ErrNotRunning`,
  `ErrIdempotencyConflict`, `ErrQuota`, `ErrNoCapacity`, `ErrUnavailable`;
  `CodeOf`, `IsRetryable`.
- Idempotency: every call that needs a key gets a random one;
  `WithIdempotencyKey(ctx, key)` sets one the caller saved first, so a
  retry after a crash replays the request.

Retry rules:

- GET calls and calls with an `Idempotency-Key` retry transport errors
  and answers with `retryable: true` (or a 429/502/503/504 without an
  error body), with exponential backoff, at most `MaxRetries` (4) times.
  A `Retry-After` is honored; one longer than `MaxRetryDelay` (30 s, for
  example `no_capacity`'s 60 s) goes back to the caller.
- Exec retries only answers that prove the command did not start
  (`quota_exceeded`, `rate_limited`, `concurrency_limit`, and a retryable
  `operation_conflict` while the machine wakes), never a transport error
  or `host_unavailable`.
- A file upload retries only when the body is an `io.Seeker`.
- A create whose operation failed with `no_capacity` replays that failure
  for the same key; retry with a new key.

The API key goes only into the `X-API-Key` header. `Client` prints as
`instabox.Client{<base URL>}`, the debug log has method, path, status,
and attempt, and `*APIError` holds no headers. `ImportSnapshot` passes
the key to the CLI in its environment, never on its command line.

The fake (`instaboxtest.New(Options{APIKey, OpDelay, MaxMachines, MaxSnapshots, FreePlan, Exec})`)
keeps its own JSON types. Test hooks: `InjectError`, `InjectDisconnect`,
`FailNextCreate`, `FailNextExecAfterStart`, `IdlePause`, `State`,
`Requests`, `Replays`, `File`, `PortToken`, `Leases`.

## Image

`deploy/instabox/Dockerfile` builds `lydiabox/instabox:latest` from the
`desktop` stage of `deploy/rootfs/Dockerfile` (built first as
`lydiabox/guest:instabox-base`, `linux/amd64`); it does not copy that
Dockerfile.

```bash
make instabox-image                                  # or deploy/instabox/build.sh
make instabox-image INSTABOX_IMAGE_FLAGS=--slim      # small base, for tests
make instabox-image-test                             # container test
```

To import, push the image to a registry that InstaBox can pull from, then
run the command that `build.sh` prints:

```bash
deploy/instabox/build.sh --tag ghcr.io/OWNER/lydiabox-instabox:TAG --push
instabox snapshot import ghcr.io/OWNER/lydiabox-instabox@sha256:... --name lydiabox-desktop
```

### InstaBox image rules and how the image meets them

From [Custom images](https://instabox.sh/docs/machines/custom-images.md):

| Rule | Image |
| :--- | :--- |
| Only `linux/amd64` images can be imported. | `build.sh` builds `--platform linux/amd64`, checks the base architecture, and writes a single-platform image (`--provenance=false --sbom=false`). |
| The import pulls from a registry; a local image cannot be imported. | `build.sh --push`, or `docker tag` and `docker push`; the printed command uses the pushed digest. |
| `ENTRYPOINT` and `CMD` run as the systemd unit `instabox-image.service`, in `WORKDIR`, as `USER` (root when unset), restarted on failure; output goes to the journal. | `ENTRYPOINT ["/usr/local/sbin/lydiabox-instabox-entry"]`, no `USER`, `WORKDIR /`. The entry point runs supervisord in the foreground, so the unit stays active. |
| The image's own init (tini, s6) is not run. systemd, sshd, sudo, the UID 1000 user, `/data`, and the machine's shared libraries stay. | The entry point mounts nothing (the `lydiabox-init` of the VM and docker drivers mounts `/run` and `/tmp` and is not used). It starts the system D-Bus only when `/run/dbus/system_bus_socket` is missing. |
| The image's user with UID 1000 is the machine user. | `agent` is UID 1000 in the base image; its home stays `/home/agent`, a link to `/data/home`. |
| `ENV` reaches every session (exec, SSH, login shells). | `ENV HOME=/home/agent DISPLAY=:0 LYDIABOX_DATA_ROOT=/data`. |
| `EXPOSE` is not published. | The driver publishes the link port with `PublishPrivatePort`. |
| `/etc/instabox/image.json` records the image reference, digest, and config. | The driver can read it to check the snapshot's image. |

### Run-time layout

- The core uses the guest paths `/home/agent` (agent home, `HOME`) and
  `/workspace` (PTY and session directories), so both are links onto the
  persistent disk: `/home/agent` to `/data/home` (also InstaBox's own
  `HOME`) and `/workspace` to `/data/workspace`.
- `/usr/local/sbin/lydiabox-instabox-entry` (root): creates `/data/home`,
  `/data/workspace`, and `/data/.lydiabox` (mode 0700) owned by UID 1000
  when they are missing, copies the home content of the base image
  (`/usr/local/share/lydiabox/home-seed`) into a new home, prepares
  `/tmp/.X11-unix` and `/run/user/1000`, and runs supervisord with
  `/etc/lydiabox/instabox-supervisord.conf`.
- supervisord starts, as `agent`: Xvfb on `LYDIABOX_DISPLAY` (default
  `:0`), Xfce (`dbus-run-session startxfce4`), and
  `/usr/local/bin/lydiabox-instabox-guest`.
- `lydiabox-instabox-guest` waits for `/data/.lydiabox/guest.env`, exports
  its `LYDIABOX_*` lines (the file is read, never run), sets the file
  roots, and starts
  `lydiabox-guest serve -workdir $LYDIABOX_GUEST_WORKSPACE_ROOT -display $LYDIABOX_DISPLAY`.
  When `/data/.lydiabox/bin/lydiabox-guest` exists, it runs that binary
  instead of the image's: the driver upgrades the guest with a file upload
  and a restart, with no sudo and no new snapshot.
- `/run/lydiabox` belongs to UID 1000, so that the daemon can create the
  link-mode sockets `host-9001.sock` (broker) and `host-9002.sock` (LLM
  gateway) there.

### Settings the driver supplies

The driver writes `/data/.lydiabox/guest.env` with
`PutFileBytes(..., PutFileOptions{Mode: 0o600, Parents: true})` before it
needs the guest daemon. The file is on the persistent disk, so it stays
across stop and start. To change a value, the driver rewrites it and runs
`sudo supervisorctl -c /etc/lydiabox/instabox-supervisord.conf restart lydiabox-guest`
through exec. `deploy/instabox/guest.env.example` is the contract:

| Variable | Meaning |
| :--- | :--- |
| `LYDIABOX_GUEST_LISTEN` | `ws://0.0.0.0:9000/`: the host link listener (`rpc.EnvGuestListen`). The driver publishes port 9000 with `PublishPrivatePort` and dials `wss://9000-<id without m_>.<port domain>/` with `Authorization: Bearer <port token>` (`link.Target`) |
| `LYDIABOX_LINK_SECRET` or `LYDIABOX_LINK_SECRET_FILE` | Link secret, at least 16 bytes; the daemon removes both from its environment after reading |
| `LYDIABOX_HOST_ADDR`, `LYDIABOX_GATEWAY_ADDR` | Leave out: in link mode they default to `unix:///run/lydiabox/host-9001.sock` and `host-9002.sock` |
| `LYDIABOX_GUEST_HOME_ROOT`, `LYDIABOX_GUEST_WORKSPACE_ROOT` | The two file roots of the guest file API (provider storage mode); the wrapper defaults them to `/data/home` and `/data/workspace`; the workspace is also the PTY directory |
| `LYDIABOX_WEBRTC_UDP_PORT` | `0`: the desktop stream uses TURN only (no inbound UDP is documented) |
| `LYDIABOX_LOG_LEVEL`, `LYDIABOX_CHROME_FLAGS` | Optional |

The file holds the link secret. The daemon runs as UID 1000, so the agent
processes of that user can read the secret (also from the daemon's
`/proc/<pid>/environ`); the secret authenticates the two ends of the
link, and the private port token keeps the endpoint closed to the
network. Upstream LLM keys and refresh tokens stay on the host control
plane, as in the other drivers.

### Container test

`deploy/instabox/test.sh` runs the image with Docker as `linux/amd64`
(emulated on arm64) with a volume as `/data` and port 9000 on
`127.0.0.1`. It checks that:

- the Xvfb socket and display `:0` answer, and `xfwm4` and
  `xfce4-session` run as UID 1000;
- the guest wrapper waits for `guest.env`; the agent home is
  `/home/agent`, a link to `/data/home`, `/workspace` links to
  `/data/workspace`, and both directories belong to UID 1000;
- after the test writes `guest.env` as UID 1000 (link listener and
  secret), `lydiabox-guest serve` runs as UID 1000 with the file's
  variables (and without non-`LYDIABOX_*` lines), with `HOME=/home/agent`, and creates
  `/run/lydiabox/host-9001.sock`;
- the link probe (`internal/instabox/imagetest`, run with `go test`)
  connects over the real host link with the secret, calls `ping`, lists
  the MCP tools and takes a `computer_screenshot` (the daemon reaches X11),
  and starts a PTY in `/data/workspace` that calls the host broker through
  `/run/lydiabox/host-9001.sock` and the link;
- after `docker restart`, the desktop and the guest start again from the
  kept `guest.env`, and the probe passes again.

Without Docker it prints `SKIP` and exits 0; without Go it skips only the
probe. Docker is not systemd: the test does not cover
`instabox-image.service` or the overlay on an `agent-browser` machine.

## What the documentation does not state

Each item needs a check with a real account (see the checklist below).

1. **Inbound UDP.** Ports are HTTPS (and raw TCP behind TLS, paid, in
   preview); mosh's UDP is relayed by the SSH gateway only. Nothing says
   that a machine can receive UDP, so the WebRTC desktop stream must use
   the TURN relay ([turn-manual-test.md](turn-manual-test.md), the TURN key
   of `lydiabox cloudflare setup`). Outbound UDP to the TURN server is
   also not stated.
2. **Root disk across stop and start.** `/data` is kept across stop,
   pause, and start. The root disk (the image's files, `/etc`, installed
   packages) is in snapshots, but its persistence across a plain stop and
   start of one machine is not stated. The image keeps all state on
   `/data`, so either answer works; the check confirms it.
3. **Cold start, resume, and import times.** `readyAt - createdAt` is the
   create time, but no figures are given for create from a large
   snapshot, start from stopped, resume from paused, or the import of a
   multi-GB image ("a few minutes").
4. **Image size limits.** No limit on the image or the root disk it fills
   (16 GiB on `agent-browser`) is stated; the full desktop image is
   6.65 GB in Docker (the slim one 1.1 GB).
5. **Machine user of an imported image.** The image's UID 1000 user "is
   that user", but whether exec, SSH, and files log in as `agent` (and
   which `guestUser` the snapshot records), and whether the overlay keeps
   the image's `/home/agent` link while the machine's HOME is
   `/data/home`, are not stated.
6. **Display conflicts.** `agent-browser` has its own desktop (Xfce, VNC,
   Chromium) that `POST /v1/machines/{id}/desktop` starts. Whether it runs
   an X server on `:0` at boot after the overlay is not stated;
   `LYDIABOX_DISPLAY` moves the LydiaBox display if it does.
7. **Unit environment.** Whether `instabox-image.service` gets the image
   `ENV` and the create-time `env` (stated for sessions only), and its
   restart delay and limits.
8. **Private ports from a server.** `Authorization: Bearer <token>` is
   stated for scripts and the cookie for browser WebSockets; a WebSocket
   upgrade with the Bearer header from the core, its idle timeout, and its
   maximum duration are not stated.
9. **Wake and idleness through a private port.** A request without the
   token does not wake a paused machine, and public port requests do. That
   a request with the token wakes an idle-paused machine, and that a long
   WebSocket through the port counts as activity, are inferred, not
   stated.
10. **Pause and the link.** Connections close when a machine pauses or
    stops; how long the core's reconnect must wait after a resume is not
    stated.
11. **systemd and supervisord after resume.** Pause keeps processes; that
    Xvfb, Xfce, and lydiabox-guest continue after a resume (and that
    `processesPreserved: false` restarts the unit) is inferred.

## First check with a real account

Use a paid workspace key with `machines:*` and `images:*` scopes, a
registry InstaBox can pull from, and the `instabox` CLI. Record each
result in `docs/conformance.md`.

1. Drift: `LYDIABOX_INSTABOX_DRIFT=1 go test ./internal/instabox -run TestOpenAPIDrift -v`.
2. Build and push: `deploy/instabox/build.sh --tag <registry>/lydiabox-instabox:<tag> --push`.
3. Import with the printed command; record the time and the snapshot
   size (`instabox snapshot ls --json`). Check `ImportSnapshot` with the
   same image under another name.
4. Create a machine from the snapshot with `CreateMachine` (2/4 and 4/8),
   `WaitOperation`; record `readyAt - createdAt`.
5. In the machine: `journalctl -u instabox-image.service`,
   `systemctl show instabox-image.service -p Environment,Restart,RestartUSec`,
   `cat /etc/instabox/image.json`, `id`, `echo $HOME`, `readlink /home/agent /workspace`, `ls -la /data`,
   `pgrep -a Xvfb`, `pgrep -a Xorg`, `ls /tmp/.X11-unix`
   (facts 5, 6, 7).
6. Write `guest.env` with `PutFileBytes` (WebSocket listener, link
   secret), publish port 9000 with `PublishPrivatePort`, and run the
   probe through the port proxy (fact 8):
   `LYDIABOX_IMAGE_LINK_URL=wss://9000-<id>.<domain>/ LYDIABOX_IMAGE_LINK_SECRET=... LYDIABOX_IMAGE_LINK_TOKEN=ibp_... go test ./internal/instabox/imagetest -v`. Run a PTY session and a desktop stream through TURN (fact 1:
   also try a direct UDP candidate and record whether it connects).
7. Exec: `Exec` and `ExecStream` with a 5-minute command (keepalive),
   a non-zero exit, and 2 MiB of output.
8. Files: upload and download 100 MB; `StatFile` of `/data/home`.
9. Idle pause: `SetIdlePause(5m)`, leave the link idle, record when it
   pauses; then send a request with the port token and record whether it
   wakes and how long it takes (fact 9). Repeat with an open WebSocket
   and no traffic.
10. Pause and resume: `PauseMachine`, `ResumeMachine`; check that Xvfb,
    Xfce, and lydiabox-guest kept running, record the resume time and how
    long the link needs to reconnect (facts 3, 10, 11).
11. Stop and start: write a file on `/` and on `/data`, `StopMachine`,
    `StartMachine`; record which file is still there and the start time
    (facts 2, 3); check that the guest starts again without a new
    `guest.env`.
12. Errors: a wrong key (401), a stopped machine's exec (409
    `machine_not_running`), a delete without `X-Confirm-Delete` via curl
    (409), a second create with the same `Idempotency-Key` (replay).
13. Clean up: `DeleteMachine`, `DeleteSnapshot`, and revoke the key.
