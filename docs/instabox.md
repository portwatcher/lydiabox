# InstaBox: API client, guest image, and VM driver

LydiaBox Cloud v1 runs each guest on an [InstaBox](https://instabox.sh)
machine: a hosted Firecracker microVM (`linux/amd64`) with a persistent
disk at `/data`. The core reaches it through the `instabox` VM driver
([cloud-instabox.md](cloud-instabox.md) maps the driver and
[cloud-design.md](cloud-design.md) the service). This page covers:

- `internal/instabox`: a typed Go client for the InstaBox REST API, with a
  fake server (`internal/instabox/instaboxtest`) for tests.
- `deploy/instabox`: the LydiaBox desktop image that InstaBox imports as a
  snapshot.
- `internal/vmm/instabox`: the `instabox` VM driver
  ([Driver](#driver)), and the operator command `lydiabox instabox`.

The first real tenant (2026-11-16) checked the image on a real machine:
[Verified on a real machine](#verified-on-a-real-machine-2026-11-16) has
the results.
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
| Maximum run: `maxRunMinutes`, -1 unlimited (paid default); the machine is paused at the limit even while it is busy, and activity leases and wake calls do not extend it. The free plan starts each run (each start or resume) at 120 min and grows it, up to 480 min, only with use in the last 30 min (commands, file access, port requests; not leases or wakes); a free machine cannot change the value. After a pause at the maximum run, a wake answers `409 machine_cooldown` until `cooldownUntil` (5 min plus 0-15 min; on the free plan the 2nd and later steps are longer). The driver does not set `maxRunMinutes`; it logs `reason=maximum run time` with `cooldownUntil` for such a pause, and a wake during the cooldown fails at once with that reason and time until the cooldown ends. | [Auto-pause](https://instabox.sh/docs/machines/auto-pause.md#set-a-maximum-runtime), OpenAPI `MaxRunMinutes` |
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

The fake (`instaboxtest.New(Options{APIKey, OpDelay, MaxMachines, MaxSnapshots, FreePlan, Exec, Guest})`)
keeps its own JSON types. Test hooks: `InjectError`, `InjectDisconnect`,
`FailNextCreate`, `FailNextExecAfterStart`, `IdlePause`, `State`,
`Requests`, `Replays`, `File`, `PortToken`, `Leases`, `AddSnapshot`,
`Machines`, `Wakes`, `ProxyURL`. With a `GuestRunner` (`Guest`), each
machine runs a program from its create or start to its stop (a pause keeps
it), and the published ports are served by a port proxy at `ProxyURL`:
a private port needs its token (`401` without it, and no wake), a request
wakes a self-paused machine, a machine that a client paused or stopped
answers `503 machine_not_running`, and a pause closes the connections.
An explicit lifecycle operation ends the activity leases.
`instaboxtest/fakeguest` is a `GuestRunner` that runs the real guest
daemon in the test process from the `guest.env` that the driver writes.

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

Distribution. The base is Debian 13: `build.sh` passes
`DESKTOP_IMAGE=debian:trixie-slim` (`--distro IMAGE` selects another; the
slim image has no manuals, documentation, or translations, which keeps
the image within the import disk, [below](#what-the-documentation-does-not-state)). The
template `agent-browser` of the import machine is Debian 13, and the
import keeps the machine's shared libraries and dpkg database under the
image's programs, so the programs must be of the same release. The images
of core `cdb5021` and earlier had an Ubuntu 24.04 base; on a machine of
such a snapshot the image's Ubuntu programs run with the machine's Debian
13 libraries (`apt-get` stops with a segmentation fault, the Xfce panel
4.18 runs with the 4.20 library, the dbus tools fail;
[System upgrade](#system-upgrade), Limits, and
[Panel dialog, home, and environment](#panel-dialog-home-and-environment)).
That a Debian 13 image removes these faults is a container test result
([Container test](#container-test)), to be confirmed on a real machine.
The other backends (`chv`, `docker`) keep the default `ubuntu:24.04`
of `DESKTOP_IMAGE`: one package list serves both releases, and the steps
that differ check `ID` in `/etc/os-release`:

| Step | Ubuntu 24.04 | Debian 13 |
| :--- | :--- | :--- |
| dpkg path rules (no manuals, translations, documentation) | `/etc/dpkg/dpkg.cfg.d/excludes` of the container image (keeps copyright files and changelogs) | `/etc/dpkg/dpkg.cfg.d/docker` of `debian:trixie-slim`, and `deploy/rootfs/dpkg-excludes` as `lydiabox-excludes` for a base without such rules (both keep copyright files only) |
| `software-properties-common` (`add-apt-repository`) | installed | not in Debian 13; not needed |
| Chromium on `arm64` | deb of the `xtradeb/apps` PPA (Ubuntu's is a snap) | `chromium` of Debian main |
| Google Chrome on `amd64`, NodeSource Node.js, `gh`, `blender`, `bubblewrap`, fonts | same packages and sources | same |
| default user with UID 1000 | `ubuntu`, removed (`setup-guest.sh`) | none |

The image's `/etc/apt` replaces the machine's files of the same name. On
Debian 13 it has `debian.sources` (`trixie`, `trixie-updates`,
`trixie-security`, main), `nodesource.sources`, and
`google-chrome.sources`, with no Ubuntu source or key (the InstaBox
Dockerfile stops the build otherwise), and `/var/lib/apt/lists` is empty.
The InstaBox Dockerfile also removes `docker-apt-speedup` (dpkg's unsafe
I/O) of the Debian container image. Files of the template's `/etc/apt`
that the image does not have stay on the machine. The dpkg path rules
(`docker`, `lydiabox-excludes`) go to the machine too, so a package that
`apt-get` installs there gets no manuals or documentation, as with the
Ubuntu-based image.

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
| The image's own init (tini, s6) is not run. systemd, sshd, sudo, the UID 1000 user, `/data`, and the machine's shared libraries stay. | Verified with the Ubuntu-based image: the shared libraries (`*.so`), the dpkg database, and some programs (`dbus-daemon` 1.16.2) stay the machine's, while other files are the image's (`xfce4-panel` 4.18, `wrapper-2.0`, `/etc/xdg/xfce4/panel/default.xml`). So the base is Debian 13, the template's release ([Image](#image)). The entry point sets the files that matter to LydiaBox at each start. It mounts nothing (the `lydiabox-init` of the VM and docker drivers mounts `/run` and `/tmp` and is not used). It starts the system D-Bus only when `/run/dbus/system_bus_socket` is missing. The machine keeps its own `libdbus`, so the image's `dbus-run-session` of an Ubuntu-based image cannot run: the Xfce wrapper runs `dbus-daemon --session` itself (`/run/user/1000/lydiabox-session-bus`), not the bus of the machine's user manager (`/run/user/1000/bus`), whose services get `HOME=/data/home`. |
| The image's user with UID 1000 is the machine user. | The import keeps the machine's user database: UID 1000 has the account's name (for example `alice`), not `agent`, and the home `/data/home`. The image uses UID 1000 by number (supervisord `user=1000`); the entry point reads the name with `getent passwd 1000`. `HOME` is `/home/agent`, a link to `/data/lydiabox/home`, not to InstaBox's `/data/home`. |
| `ENV` reaches every session (exec, SSH, login shells). | `ENV HOME=/home/agent DISPLAY=:0 LYDIABOX_DATA_ROOT=/data`. InstaBox leaves out `HOME` and `PATH`: sessions get `HOME=/data/home`, and the unit gets no `HOME`. The entry point starts supervisord with a defined environment (`env -i`), not the unit's. |
| `EXPOSE` is not published. | The driver publishes the link port with `PublishPrivatePort`. |
| `/etc/instabox/image.json` records the image reference, digest, and config. | The driver can read it to check the snapshot's image. |

### Run-time layout

| Path | What | Owner |
| :--- | :--- | :--- |
| `/home/agent` | link to `/data/lydiabox/home`: LydiaBox's agent home, `HOME` of every LydiaBox program and PTY, the `home` root of the file API. The desktop session and the programs that the Desktop page starts (Terminal, Chrome, Blender) start in it with `PWD=/home/agent`, so that a shell and a terminal title show `~`, not `/data/lydiabox/home`. The Desktop page's programs use the Xfce session's own bus (the guest daemon has its address), not the user manager's bus, whose `xfconfd` writes into `/data/home` | link: root |
| `/data/lydiabox/home` | the agent home (mode 0700), copied once from the image's home seed (marker `/data/lydiabox/home-seeded`) | UID 1000 |
| `/workspace` | link to `/data/workspace`: the workspace and PTY directory, the `workspace` root of the file API | link: root |
| `/data/home` | InstaBox's own home (the passwd home of UID 1000, `HOME` of exec and SSH sessions) with the template's `.claude.json`, `.claude/skills`, `.agents/skills`, `.codex/AGENTS.md`, `AGENTS.md`, and `MACHINE.md`. LydiaBox does not use it and never changes it | UID 1000 |
| `/data/.lydiabox` | the driver's `guest.env` and the uploaded guest binary `bin/lydiabox-guest` (mode 0700) | UID 1000 |

- The core uses the guest paths `/home/agent` and `/workspace`, so both
  are links onto the persistent disk. A machine from an image of core
  `802e85c` or earlier has the old layout, `/home/agent -> /data/home`.
  At its first start with this image the entry point moves the link to
  `/data/lydiabox/home` in one step (a new link renamed over the old
  one), appends a line to `/data/lydiabox/home-moved.log`, and logs
  `moved the agent home: ...` in the journal. It moves nothing else:
  the files in `/data/home`, also those that LydiaBox wrote there before
  (harness sessions in `.claude/projects` and `.codex/sessions`, uploads
  to the home root, `.gnupg`, the old Xfce settings), stay there and do
  not move into the new home. LydiaBox's session list no longer shows
  those old sessions; copy a file by hand if it is still needed. Later
  starts find the new link and do nothing.
- `/usr/local/sbin/lydiabox-instabox-entry` (root): checks its
  preconditions (root, the programs, `/data`, a user with UID 1000) and
  exits with an `ERROR:` line in the journal when one is missing; reads the
  name of UID 1000; creates `/data/lydiabox`, `/data/lydiabox/home`,
  `/data/workspace`, and `/data/.lydiabox` owned by UID 1000 when they
  are missing; moves the old link; makes the links `/home/agent` and
  `/workspace` when they are missing (anything else there is an error);
  copies the home seed once without replacing files; sets the image's
  `/etc/codex/config.toml`, `/etc/claude-code/managed-settings.json`, and
  `/etc/xdg/xfce4/panel/default.xml` (from `/usr/local/share/lydiabox/etc`)
  and links `/usr/local/bin/claude`, `codex`, `opencode`, and `dsh` to
  `/usr/bin` when they differ, keeping the template's version once as
  `FILE.instabox-template` and logging each change; prepares
  `/tmp/.X11-unix` and `/run/user/1000`; and runs supervisord with
  `/etc/lydiabox/instabox-supervisord.conf` and a defined environment
  (`env -i` with `PATH`, `LANG`, `LC_ALL`, `TZ`, and the `LYDIABOX_*`
  variables of the configuration).
- supervisord starts, as UID 1000 (`USER` and `LOGNAME` are the name of
  UID 1000, `HOME=/home/agent`, `XDG_RUNTIME_DIR=/run/user/1000`): Xvfb on
  `LYDIABOX_DISPLAY` (default `:0`), Xfce
  (`/usr/local/bin/lydiabox-instabox-xfce`: `startxfce4` on a session bus
  of its own, `dbus-daemon --session` at
  `/run/user/1000/lydiabox-session-bus`, so that `xfconfd` and the other
  services of the bus get `HOME=/home/agent` and keep the Xfce settings in
  LydiaBox's home; supervisord stops the whole process group), and
  `/usr/local/bin/lydiabox-instabox-guest`. The program logs are in
  `/var/log/supervisor` (the guest daemon's in `lydiabox-guest.log`:
  under systemd supervisord cannot open `/dev/stdout`, a journal socket);
  the state changes of the programs are in the journal of the unit.
- `lydiabox-instabox-guest` waits for `/data/.lydiabox/guest.env`, exports
  its `LYDIABOX_*` lines (the file is read, never run) except the two
  file roots, which the image sets (`LYDIABOX_GUEST_HOME_ROOT=/data/lydiabox/home`,
  `LYDIABOX_GUEST_WORKSPACE_ROOT=/data/workspace`; a root in the file
  gets an `ignoring ...` line in the guest log), and starts
  `lydiabox-guest serve -workdir $LYDIABOX_GUEST_WORKSPACE_ROOT -display $LYDIABOX_DISPLAY`.
  When `/data/.lydiabox/bin/lydiabox-guest` exists and is executable, it
  runs that binary instead of the image's: the driver upgrades the guest
  with a file upload and a restart, with no sudo and no new snapshot
  ([Guest binary upgrade](#guest-binary-upgrade)).
- `/run/lydiabox` belongs to UID 1000, so that the daemon can create the
  link-mode sockets `host-9001.sock` (broker) and `host-9002.sock` (LLM
  gateway) there.
- The Xfce panel's default (`/etc/xdg/xfce4/panel/default.xml`, from
  `deploy/instabox/xfce4-panel.xml`) has only plugins that load in the
  panel's process; a new agent home starts with it.

### Supplier files and environment

The machine template adds harness settings and variables of its own.
Found on the machine of the first tenant (2026-11-16; names and lengths
only, no value of a credential was read):

| Supplier item | Effect before | What LydiaBox does now |
| :--- | :--- | :--- |
| `/data/home/.claude.json` with the MCP servers `instabox` and `open-computer-use`; `/data/home/.claude/skills`, `/data/home/.agents/skills` (`instabox`, `instabox-snapshots`); `AGENTS.md` linked as `.claude/CLAUDE.md` and `.codex/AGENTS.md` | Claude Code in LydiaBox loaded both MCP servers (`claude mcp list`: `instabox ✔ Connected`), the skills, and InstaBox's instructions | Not used: `HOME` is `/data/lydiabox/home` |
| `/etc/codex/config.toml` with `[mcp_servers.instabox]` (`env_vars` `INSTABOX_API_KEY`, `INSTABOX_API_URL`, `INSTABOX_MCP_ALLOW_DELETE`) and `[mcp_servers.open-computer-use]`, read by Codex in every home | Codex in LydiaBox loaded both MCP servers (`codex mcp list`) | The entry point sets the image's file at each start (`deploy/instabox/codex-config.toml`: only `check_for_update_on_startup = false`); the template's is kept as `/etc/codex/config.toml.instabox-template` |
| `/etc/claude-code/managed-settings.json` (`env.DISABLE_AUTOUPDATER=1`) | Claude Code did not update itself | The entry point sets the image's file with the same setting |
| `/usr/local/bin/claude`, `codex`, `opencode` linked to `/opt/npm` (Claude Code 2.1.288, Codex 0.160.0, updated by the machine user); `/usr/local/bin` is first in `PATH` | LydiaBox ran these instead of the image's (2.1.325, 0.169.0) | The entry point links them (and `dsh`) to the image's CLIs in `/usr/bin` |
| The user manager's session bus (`/run/user/1000/bus`) starts `xfconfd` with `HOME=/data/home` | Xfce read and wrote its settings in `/data/home/.config/xfce4`, also after `/home/agent` moved | Xfce runs on its own bus with `HOME=/home/agent` |
| Unit `instabox-image.service`: `EnvironmentFile=/etc/instabox/image.env` (the image `ENV`) and systemd's `INVOCATION_ID`, `JOURNAL_STREAM`, `SYSTEMD_EXEC_PID`, `MEMORY_PRESSURE_WATCH`, `MEMORY_PRESSURE_WRITE` | All reached supervisord, the guest daemon, and every PTY | The entry point starts supervisord with `env -i` and a defined list |
| supervisord's `SUPERVISOR_ENABLED`, `SUPERVISOR_GROUP_NAME`, `SUPERVISOR_PROCESS_NAME`, `SUPERVISOR_SERVER_URL`; the daemon's `LYDIABOX_GUEST_LISTEN`, `LYDIABOX_GUEST_*_ROOT`, `LYDIABOX_WEBRTC_UDP_PORT`, `LYDIABOX_DATA_ROOT`, `DEBIAN_FRONTEND` | Reached every PTY | The guest daemon gives PTYs only `ptymanager.Inherited` variables (below) |
| `/etc/profile.d/instabox-npm.sh` (`PATH` + `/opt/npm/bin`), `instabox-universal.sh` (`PATH` + `~/go/bin`, `~/.cargo/bin`, `~/.bun/bin`, `PNPM_HOME`, `~/.yarn/bin`; `JAVA_HOME`), `instabox-tmpdir.sh` (`TMPDIR=/data/tmp`), `instabox-opencode.sh` (`OPENCODE_DISABLE_AUTOUPDATE=1`); `/etc/environment` (`JAVA_HOME`) | Login shells only | Unchanged: the web terminal is a login shell (`bash -l`) and reads them; none holds a credential or an MCP setting. Harness PTYs run the harness directly, not through a login shell, and do not read them |
| `GEMINI_API_KEY` (14 characters) in a LydiaBox terminal | Not the supplier's: it is LydiaBox's placeholder `lydiabox-local` (`ptymanager.DefaultEnv`); the machine's environment has no `GEMINI_API_KEY`, `ANTHROPIC_*`, `OPENAI_*`, `CLAUDE_*`, `CODEX_*`, or MCP variable | Unchanged |

Environment of LydiaBox processes. supervisord and its programs get
`PATH`, `LANG`, `LC_ALL`, `TZ`, and the `LYDIABOX_*` variables of the
entry point, then the program's `environment=` (`HOME=/home/agent`,
`USER`, `LOGNAME`, `SHELL`, `DISPLAY`, `XDG_RUNTIME_DIR`). A PTY of the
guest daemon gets only these variables of the daemon
(`ptymanager.Inherited`): `HOME`, `USER`, `LOGNAME`, `SHELL`, `PATH`,
`LANG`, `LANGUAGE`, `LC_*`, `TZ`, `DISPLAY`, `XDG_RUNTIME_DIR`,
`DBUS_SESSION_BUS_ADDRESS`, the proxy variables, `SSL_CERT_FILE`, and
`SSL_CERT_DIR`; then LydiaBox's (`LYDIABOX_HOST_ADDR`,
`ANTHROPIC_BASE_URL`, `OPENAI_BASE_URL`, `GOOGLE_GEMINI_BASE_URL`, the
placeholder `GEMINI_API_KEY`, `TERM`, `COLORTERM`, `DISPLAY`,
`LYDIABOX_PTY_ID`) and the session's (`LYDIABOX_SESSION_ID`,
`LYDIABOX_HARNESS`, the binding's variables). The same rule applies in
the `chv` and `docker` backends.

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
| `LYDIABOX_GUEST_HOME_ROOT`, `LYDIABOX_GUEST_WORKSPACE_ROOT` | Not settings: the image sets the two file roots (`/data/lydiabox/home`, `/data/workspace`) and the wrapper ignores them in the file. The driver of core `802e85c` and earlier wrote `LYDIABOX_GUEST_HOME_ROOT=/data/home`; the next driver writes neither, so a machine with the old file gets a new file and one restart of the guest daemon at its next attach |
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
`127.0.0.1`, in four scenarios (`--scenario plain|machine|migrate|precond|all`,
default `all`); `deploy/instabox/test-checks.sh` has the layout,
desktop, and harness checks:

- `plain`: the image as it is (UID 1000 is `agent`).
- `machine`: the image as the import makes it
  ([deploy/instabox/machine-model](../deploy/instabox/machine-model)): no
  user `agent`, UID 1000 named `alice` with the home `/data/home` that has
  InstaBox's files (`.claude.json` with the `instabox` and
  `open-computer-use` MCP servers, skills, `AGENTS.md`), a
  `dbus-run-session` that fails as on the machine, the user manager's
  session bus at `/run/user/1000/bus` with `HOME=/data/home`, the
  template's `/etc/codex/config.toml` (with an InstaBox MCP server) and
  `/usr/local/bin/claude`, `codex`, `opencode` links to `/opt/npm`, no
  `HOME`, systemd's variables and supplier
  variables (`ANTHROPIC_API_KEY`, `INSTABOX_API_KEY`, `CLAUDE_CONFIG_DIR`,
  `CODEX_HOME`, `XDG_CONFIG_HOME`, test values) in the unit's
  environment, and a socket as the standard output of the entry point
  (as the journal). The image of core `23fbf68` fails it with the error
  of the real machine. The model also puts the template's shared
  libraries and dpkg database over the image's files: its stage
  `template` installs `dbus-bin`, `libdbus-1-3`, and `xfce4-panel` on
  `debian:trixie` (`TEMPLATE_IMAGE`) and copies every `*.so` file of the
  installed packages and `/var/lib/dpkg`. An image of another release then
  fails as on the machine: the model of the Ubuntu-based image of core
  `cdb5021` gives `libapt-private.so.0.0: no version information
  available` and a segmentation fault (exit 139) at `apt-get update`.
- `migrate`: `machine` on a disk of the old layout: `/home/agent` links
  to `/data/home`, the old seed marker exists, a harness transcript of
  LydiaBox is in `/data/home`, and `guest.env` has the old
  `LYDIABOX_GUEST_HOME_ROOT=/data/home`.
- `precond`: the entry point exits with a clear `ERROR:` line when UID
  1000 or `/data` is missing, or when `/home/agent` is not a link.

In `plain`, `machine`, and `migrate` it checks that:

- the Xvfb socket and display `:0` answer, and `xfwm4` and
  `xfce4-session` run as UID 1000;
- the guest wrapper waits for `guest.env`; `/home/agent` links to
  `/data/lydiabox/home` (mode 0700, with the seed), `/workspace` to
  `/data/workspace`, and both directories belong to UID 1000;
- with the model: `/data/home` is exactly as the template made it (file
  list, modes, owners, links, and checksums), and none of InstaBox's
  files is in LydiaBox's home; in `migrate` the link moved one time (one
  line in `home-moved.log` and one `moved the agent home` log line, also
  after the restart) and the old transcript is still in `/data/home`;
- Xfce runs on `/run/user/1000/lydiabox-session-bus`, `xfconfd` has
  `HOME=/home/agent`, the panel settings are in LydiaBox's home, and
  `/etc/codex/config.toml` has no MCP server (with the model: the
  template's file is kept as `config.toml.instabox-template`);
- the desktop has no window of type `_NET_WM_WINDOW_TYPE_DIALOG` (listed
  with `xprop`), and, when the image has `xfce4-panel`, no `wrapper-2.0`
  process and no `was not found` line in `xfce.log`;
- `claude` and `codex` on `PATH` are the image's `/usr/bin` CLIs, and
  `codex mcp list` and `claude mcp list` with `HOME=/home/agent` name no
  InstaBox MCP server (skipped on the slim image, which has no harness
  CLIs);
- after the test writes `guest.env` as UID 1000 (link listener and
  secret), `lydiabox-guest serve` runs as UID 1000 with the file's
  variables (and without non-`LYDIABOX_*` lines), with `HOME=/home/agent`,
  `LYDIABOX_GUEST_HOME_ROOT=/data/lydiabox/home`, `USER` the name of UID
  1000, and `XDG_RUNTIME_DIR=/run/user/1000`, without any of the unit's
  variables, and creates `/run/lydiabox/host-9001.sock`;
  `supervisorctl restart lydiabox-guest` works;
- the link probe (`internal/instabox/imagetest`, run with `go test`)
  connects over the real host link with the secret, calls `ping`, lists
  the MCP tools and takes a `computer_screenshot` (the daemon reaches X11),
  starts a PTY in `/data/workspace` that calls the host broker through
  `/run/lydiabox/host-9001.sock` and the link, and starts a PTY that
  reports the names of its variables: `HOME` is `/home/agent` in
  `/data/lydiabox/home`, and every variable is one of the defined PTY
  environment; then it starts a terminal with `desktop.launch` (the
  Desktop page's Terminal button): the shell's directory is
  `/data/lydiabox/home` with `PWD=/home/agent`, the window title shows
  `~`, and `xfce4-panel` (the parent of the panel's and the menu's
  terminals) has the same directory and `PWD`; the launched terminal
  leaves `/data/home` unchanged (the layout check after the restart);
- after `docker restart`, the desktop and the guest start again from the
  kept `guest.env`, the probe passes again, and the layout and desktop
  checks pass again;
- in `machine`, last: `/etc/apt` names no Ubuntu source or key,
  `/etc/os-release` is Debian 13, `debian.sources` has `trixie`, and
  `/var/lib/apt/lists` is empty; then, as root, `apt-get update` gives no
  `W:` or `E:` line and `apt-get install hello` installs a program that
  runs (this part prints `SKIP` when `deb.debian.org` cannot be reached).

Without Docker it prints `SKIP` and exits 0; without Go it skips only the
probe. Docker is not systemd: the `machine` scenario copies the facts of
the real machine, but not `instabox-image.service` or the overlay itself.
The model has only a part of the template's packages, and it keeps the
image's programs where the machine keeps some of its own (`dbus-daemon`).
It does not show the panel defect of the Ubuntu-based image (the
machine's Xfce 4.20 libraries under the image's 4.18 panel); it checks the
rule that prevents it: no plugin outside the panel's process.

## Driver

### Settings

| Variable | Default | Meaning |
| :--- | :--- | :--- |
| `LYDIABOX_VM_DRIVER` | `auto` | `instabox`; in the provider storage mode `auto` selects it when the API key or the snapshot is set |
| `LYDIABOX_STORAGE_MODE` | `juicefs` | Must be `provider` |
| `LYDIABOX_INSTABOX_API_KEY`, `LYDIABOX_INSTABOX_API_KEY_FILE` | none | The API key (`ibk_...`, scopes `machines:read` and `machines:write`; `machines:delete` only for `destroy`). Environment only: not in `lydiabox.yaml`, never logged, never written to the machine |
| `LYDIABOX_INSTABOX_SNAPSHOT` | none | ID (`sn_...`) or name of the imported guest image snapshot (`instabox.snapshot`) |
| `LYDIABOX_INSTABOX_API_URL` | `https://api.instabox.sh` | The API, a key broker, or the fake in tests (`instabox.api_url`) |
| `LYDIABOX_INSTABOX_IDLE_PAUSE` | `30m` | `autoSuspendAfterSeconds` of a new machine: 5m to 30m in whole minutes, or `0` (off, paid plans) (`instabox.idle_pause`) |
| `LYDIABOX_VM_CPUS`, `LYDIABOX_VM_MEM_MB` | `4`, `8192` | An InstaBox size: 2/4096, 4/8192, 8/16384, 16/32768, 32/65536 (the plan limits the choice) |
| `LYDIABOX_IDLE_TIMEOUT` | `20m` | The cell pauses the machine after this long without sessions or viewers |
| `LYDIABOX_WEBRTC_UDP_PORT` | `0` | Must be `0` or unset: a machine gets no inbound UDP |
| `LYDIABOX_TURN_URLS` or `LYDIABOX_CLOUDFLARE_TURN_KEY_ID`, and `LYDIABOX_WEBRTC_RELAY_ONLY=true` | none | The desktop stream relays through TURN on TCP or TLS; `lydiabox up` warns when no relay is set |

InstaBox has no region setting; the platform places machines. Start
validation refuses the `juicefs` mode, a missing key or snapshot, a size
that is not an InstaBox size, a GPU, and a UDP port.

### What the driver does

- The first use makes the machine `lbx-<16 hex of sha256(tenant)>` from
  the snapshot with the metadata `lydiabox_tenant=<tenant>`. The record in
  the state database keeps the machine ID; the create's
  `Idempotency-Key` is saved before the call. A second `lydiabox up` (also
  with a lost record: it finds the metadata) attaches to the same machine.
- At boot it publishes port 9000 as a private port, writes
  `/data/.lydiabox/guest.env` (mode `0600`) with the link secret, the
  file roots, and UDP port `0`, and dials
  `wss://9000-<id>.<domain>/` with `Authorization: Bearer <port token>`.
  The link secret and the port token are encrypted in the state database.
  When an adopted machine has other settings, the driver replaces the file
  and restarts the guest daemon through `exec`.
- While the cell is attached, it holds the activity lease `lydiabox-cell`;
  the cell's idle timeout pauses the machine (`suspend`). A pause that
  InstaBox makes by itself is seen within 30 seconds (at once when the
  link drops) and shown as `PAUSED`; the next use wakes the machine.
- Each start, resume, or wake logs one line `instabox: machine running`
  with `action` (`start`, `resume`, `wake`) and `took` (from the request to
  the `running` state); a wait for another operation logs one line first.
  The next line `guest link up` has `connect`, the time of the link's
  successful dial and handshake. A slow resume is therefore visible as a
  large `took` (InstaBox) or a large `connect` (port proxy).
- A resume that InstaBox could not do with the memory
  (`runtime.processesPreserved: false`) logs the warning `instabox: the
  resume did not keep the processes; the machine booted fresh`. The
  terminals of before the pause are gone: a reconnect gets `conflict`, and
  the session is `exited`.
- When the VM boots or resumes, the host drops its cached guest RPC
  connection, because the link of before ended at the pause or stop.
- At `SIGTERM` the machine continues; the lease is released, so the idle
  pause applies.
- Quota, capacity, payment, scope, and key errors show in `lastError` of
  `GET /api/v1/vm/status` and in the web status bar; the driver waits 30
  seconds (or `Retry-After`) before it calls the API again for the tenant.
- A deleted machine is an error that names it. The driver never deletes a
  machine.
- A `502` of the port proxy means that the machine runs but nothing
  listens on port 9000. The dial error then says "the InstaBox machine
  runs, but its guest daemon does not answer on port 9000"; after 45
  seconds of `502` the driver logs one line `instabox: the guest daemon
  does not answer; machine status` with the unit state, `supervisorctl
  status`, and the last 5 journal lines of `instabox-image.service`.

### Guest binary upgrade

The machine runs the guest binary of the cell, so one release of the
cell upgrades the guest of every machine. The
[system upgrade](#system-upgrade) brings the image's own files, links,
and packages to existing machines. A new snapshot is necessary only for
other changes (for example a new Chrome, Xfce, or harness CLI), and only
new machines get it.

- The cell finds its linux guest binary as the other drivers do
  (`LYDIABOX_GUEST_BINARY`, next to `lydiabox`, the runtime `bin`
  directory, or `PATH`) and reads its SHA-256 once. When there is no
  binary, the driver does no upgrade. When the binary is not an x86-64
  ELF file (for example a cell built on arm64), the driver logs one line
  `the guest binary is not a linux/amd64 executable` and does no
  upgrade: InstaBox machines are `linux/amd64`.
- At each attach (boot, start, resume, wake, or the first use after a
  start of the cell) the driver runs one `exec` that reports the SHA-256
  of the binary that the wrapper starts next, whether `guest.env` exists,
  the SHA-256 of the running daemon's binary (`/proc/PID/exe`), and the
  number of its PTYs. The record of the tenant keeps a hash of the machine
  ID and the cell's binary when the machine runs that binary. Then a later
  attach makes no API call for the upgrade.
- When the machine has another binary, the driver uploads the cell's
  binary to `/data/.lydiabox/bin/.lydiabox-guest.upload` (mode `0755`,
  owner UID 1000) and, in a second `exec`, checks its SHA-256 and moves it
  to `/data/.lydiabox/bin/lydiabox-guest` with `mv`. The wrapper never
  sees a part of a file. The binary is installed before `guest.env` is
  written, so the first daemon of a new machine is the new binary.
- Restart rule. A restart of the guest daemon ends its PTYs: the
  terminals and the harness sessions. The desktop programs (Terminal,
  Chrome, Blender) have their own sessions and continue. So the driver
  restarts the daemon only when the daemon has no PTY, for example after
  a start from stopped, or when no terminal and no harness session runs.
  The probe counts the children of the daemon that have a controlling
  terminal. When a PTY runs, the driver does not restart: it logs one
  line `the new guest binary is staged`, and the new binary runs from the
  next start of the daemon (a stop and start of the machine). The next
  attach looks again and restarts the daemon when it has no PTY. A resume
  keeps the memory and the old daemon, so the same rule applies.
- A failure of the upgrade (probe, upload, install, or restart) does not
  stop the attach: the driver logs a `WARN` line with the cause, the
  machine keeps the guest that runs, and the next attach tries again.
  Only an account error (key, payment, quota) stops the attach, as for
  the other API calls.
- Only the guest binary goes to the machine on this path. It holds no
  secret.

### System upgrade

InstaBox has no operation that replaces the root disk of a machine and
keeps `/data`. A machine keeps the system files of the snapshot that made
it, but changes to the root disk that a command makes in the machine
stay. So the driver brings the system files of each machine to the state
that the core of the cell expects, with no new snapshot.

- The system bundle is in the `lydiabox` binary (`go:embed`,
  `deploy/instabox/system.go`). It holds the image's own small files that
  `deploy/instabox/Dockerfile` copies (the entry point, the guest and Xfce
  wrappers, the supervisord configuration, `guest.env.example`, and the
  Codex, Claude Code, and panel settings in
  `/usr/local/share/lydiabox/etc`) and the root script
  `deploy/instabox/system-upgrade.sh`. The table `Files` in `system.go`
  gives the destination path and mode of each file. A test compares it
  with the `COPY` lines of the Dockerfile. The bundle is a tar archive
  that the cell makes in memory with sorted names, a fixed time, and owner
  `0:0`. Its identity is the SHA-256 of that archive.
- The marker `/etc/lydiabox/system.sha256` on the root disk holds the
  SHA-256 of the last bundle that was installed with success. The marker
  is on the root disk because it tells the state of the root disk.
- When it runs. The probe of the guest binary upgrade (one `exec`) also
  prints the marker and the SHA-256 of `/usr/local/bin/lydiabox-guest`.
  The driver installs the bundle when the marker is not the bundle's
  SHA-256, or when the image path does not have the guest binary that the
  wrapper starts. It does this after the guest binary step (so that it
  copies the new binary) and also when the cell has no guest binary. The
  record of the tenant keeps a hash of the machine ID, the bundle, and the
  cell's guest binary; then a later attach makes no API call for the
  system upgrade. A machine from a snapshot has no marker, so its first
  attach installs the bundle one time; the script then changes almost
  nothing.
- How. The driver uploads the archive to
  `/data/.lydiabox/.system-bundle.upload` (mode `0600`). Then one `exec`
  (time limit 5 minutes) runs `sudo -n`: root copies the upload into a
  new directory below `/run` that only root can write (`mktemp -d`),
  checks the SHA-256 of that copy, unpacks it there, and runs
  `system-upgrade.sh`. Root writes the marker only when the script
  succeeds. The temporary directory and the upload are removed in every
  case. The `exec` continues when the caller of the attach stops waiting,
  so that `apt-get` is not stopped in the middle.
- Steps of `system-upgrade.sh`, in this order. Each step checks first
  and changes a file only when it is not correct (write
  `NAME.lydiabox-new`, then `mv -f`). A second run changes nothing. Each
  change prints a `changed: ...` line, and the driver logs these lines.
  1. Install the bundle files at their destinations (owner root, the mode
     of the table).
  2. When `/data/.lydiabox/bin/lydiabox-guest` is executable and differs
     from `/usr/local/bin/lydiabox-guest`, copy it to the image path. The
     rename keeps the inode of a running daemon. Then `lbx`,
     `git-credential-lydiabox`, and the git credential helper run the
     guest of the cell.
  3. The links `/usr/local/bin/lbx` and
     `/usr/local/bin/git-credential-lydiabox` to
     `/usr/local/bin/lydiabox-guest`, and
     `git config --system credential.helper "/usr/local/bin/lydiabox-guest git-cred"`.
  4. Install the programs that are missing (now: `bwrap` of the package
     `bubblewrap`) with `apt-get install -y --no-install-recommends`
     (`DEBIAN_FRONTEND=noninteractive`). The check is the program on
     `PATH`, not the package database. `apt-get update` runs only when a
     program is missing. A failure stops the script with an `ERROR:` line.
     On a machine of an Ubuntu-based snapshot `apt-get` does not run (see
     Limits), so there this step can only find that nothing is missing. On
     a machine of a Debian 13-based snapshot it is expected to install
     the package (container test; to be confirmed on a real machine).
- When it takes effect. The script never restarts supervisord, the
  desktop, or the guest daemon, and it changes nothing below `/data`
  except its own temporary files. A new entry point, a new wrapper, or a
  new supervisord configuration takes effect at the next start of the
  machine (stop and start). The entry point copies the harness and panel
  settings from `/usr/local/share/lydiabox/etc` to `/etc` at that start
  too. A copied guest binary, a link, and a package are ready at once.
- Logs: `instabox: upgraded the system files of the machine ...
  sha256=<12 hex> changes=...` (`INFO`). A failure (probe, upload, hash,
  script) gives one `WARN` line with the last lines of the script output.
  The attach continues, no marker is written, and the cell does not try
  again until its next start: a failed `apt-get` can take minutes. Only an
  account error stops the attach.
- To add a step, add it at the end of `system-upgrade.sh`: check first,
  change only when necessary, print `changed: ...`, and exit with an
  error when the change fails. A new image file needs a `COPY` line in the
  Dockerfile, a row in `Files`, a name in the `go:embed` line, and a line
  in `Dockerfile.dockerignore` (the tests check the Dockerfile and the
  ignore file). A new program goes to `deploy/rootfs/Dockerfile` and to
  `programs` in the script. Any change of a bundle file gives a new
  SHA-256, so every machine runs the script one time more.
- To check a machine by hand (`instabox exec` or the web terminal):

  ```bash
  cat /etc/lydiabox/system.sha256
  readlink /usr/local/bin/lbx /usr/local/bin/git-credential-lydiabox
  sha256sum /usr/local/bin/lydiabox-guest /data/.lydiabox/bin/lydiabox-guest
  command -v bwrap
  ```

  The record of the tenant skips the probe of a machine that had the
  bundle. So a removed marker has an effect only at the first attach
  after a release that changes the bundle or the guest binary.
- Limits. The bundle does not upgrade Chrome, Xfce, the harness CLIs
  (`claude`, `codex`, `opencode`), or the shared libraries of the
  machine. The import keeps the template's `libapt-private` (apt 3.0.3)
  and the package database of the template under the image's `apt-get`.
  The result depends on the release of the snapshot's image:
  - Ubuntu-based snapshot (core `cdb5021` and earlier): `apt-get` stops
    with a segmentation fault, because the image's `apt-get` (Ubuntu
    24.04) cannot use the template's library. Seen on 2026-11-19 on a
    machine of the template `agent-browser` 2026-11-14.2815: `apt-get
    update` gives `libapt-private.so.0.0: no version information
    available` and `Segmentation fault`; `dpkg-query -W bubblewrap` finds
    no package although `/usr/bin/bwrap` is there. A step that needs
    `apt-get` fails there, so a program that such a snapshot does not have
    comes only with a new machine from a new snapshot, or as a file in the
    bundle. The agent in the machine cannot install Debian packages
    either.
  - Debian 13-based snapshot: the image's `apt-get` and `/etc/apt` are
    Debian 13's, the release of the template's library and package
    database. `sudo apt-get update` and `sudo apt-get install -y PACKAGE`
    are expected to work, for the system upgrade and for the agent
    (container test with the template's libraries and dpkg database; to
    be confirmed on a real machine). The package database still lists only
    the template's packages: `apt-get` sees the image's packages as not
    installed and can install a package of the image again.
- Tests: `internal/vmm/instabox/system_test.go` (fake API: a current
  marker costs one probe, a new bundle is installed and recorded, a
  failure is logged once for each process, the order after the guest
  install) and `make instabox-system-test` (the real command and script
  as UID 1000 with `sudo` in a `debian:trixie` container, or the image in
  `LYDIABOX_TEST_DOCKER_IMAGE`, for example `ubuntu:24.04`: an old guest
  binary, no `lbx` link, no `bubblewrap`; a second run changes nothing; a
  bad archive and a failed `apt-get` write no marker).

### Operator command

```bash
lydiabox instabox status                       # machine, state, size, idle pause, link URL
lydiabox instabox destroy                      # prints what it would delete, and stops
lydiabox instabox destroy -confirm m_...       # deletes the machine and its /data disk
lydiabox instabox destroy -forget              # removes the record of a machine that is gone
```

Stop `lydiabox up` for the tenant first. Snapshots of the machine stay.
The command uses the same environment as the cell (data directory, master
key, API key).

### Tests

- `internal/vmm/instabox`: each driver method against the fake API with
  the real guest daemon in the test process
  (`internal/instabox/instaboxtest/fakeguest`): an idempotent create across
  a crash and a restart, adoption by metadata, `409 operation_conflict`,
  account errors, a deleted machine, a self-pause, a guest restart after
  changed settings, and the guest binary upgrade (`upgrade_test.go`: the
  same binary, a new machine, a started machine, live PTYs, a failed
  upload or install, and an arm64 binary), and the system upgrade
  (`system_test.go`, [System upgrade](#system-upgrade)). The shell
  commands of the guest upgrade are played by the test; they were run
  once by hand in a Debian container, not on a real machine. The command
  and script of the system upgrade run in a Debian 13 container with
  `make instabox-system-test`, not on a real machine yet.
- `internal/cli` (`TestInstaboxCellEndToEnd`): `lydiabox up` as a cell on a
  UNIX socket, the driver selected by `auto`, and the fake port proxy
  (token check, wake of a self-paused machine, refusal of a paused one):
  wake on demand, files, a PTY session, desktop signaling, the LLM gateway
  and connector broker channels, idle hibernate and resume, a self-pause
  with the link open, and a restart that adopts the same machine, with a
  goroutine leak check.

## Verified on a real machine, 2026-11-16

Machine `lbx-0000000000000000` of the first tenant (template
`agent-browser@2026-11-14.2815`, snapshot of the image of core `23fbf68`,
free plan, 4 vCPU / 8 GiB), read through exec. The image of core `23fbf68`
did not start: supervisord stopped at
`Invalid user name agent in section 'program:xvfb'`, nothing listened on
port 9000, and the port proxy answered the driver `502 Bad Gateway`.

| Fact | Evidence |
| :--- | :--- |
| The import keeps the machine's user database; there is no user `agent` | `getent passwd 1000` is `alice:x:1000:1000::/data/home:/bin/bash`; `getent passwd agent` is empty; the import warned `image users not added, UID 1000 being the sandbox's user: agent(1000)`; the snapshot records `guestUser: alice`; the kernel command line has `instabox.user=alice` |
| Exec runs as the machine user through an SSH login | `id` is `uid=1000(alice) gid=1000(alice) groups=1000(alice),27(sudo),991(docker)`; `SSH_CLIENT` is set; `HOME=/data/home`, `DISPLAY=:0` (image `ENV`), `XDG_RUNTIME_DIR` and `DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1000/bus` are set |
| `sudo` works through exec, and so does the driver's restart | `sudo -n true` succeeds; `sudo supervisorctl -c /etc/lydiabox/instabox-supervisord.conf status` lists the programs |
| The import keeps the image's links `/home/agent` and `/workspace` | image of core `802e85c`: `/home/agent -> /data/home` and `/workspace -> /data/workspace`, both owned by root |
| `/data/home` has the machine's own files before the image starts | `.bashrc`, `.profile`, `.claude`, `.codex`, `.claude.json`, `AGENTS.md`, `MACHINE.md`; `/data` also has `config`, `state`, `tmp`, and a 4 GiB `swapfile` |
| The unit runs the entry point through a wrapper, as root, with the image `ENV` except `HOME` and `PATH` | `ExecStart=/usr/local/lib/instabox/image-run` (runs `/etc/instabox/image.argv`), `EnvironmentFile=-/etc/instabox/image.env` with `DEBIAN_FRONTEND`, `LANG`, `LC_ALL`, `TZ`, `DISPLAY`, `LYDIABOX_DATA_ROOT`; no `User=`; `WorkingDirectory=-/`; `After=instabox-data-init.service instabox-user.service`, not after `user@1000.service` |
| Restart settings of the unit | `Restart=on-failure`, `RestartSec=5`, `StartLimitBurst=5` in `StartLimitIntervalSec=10s` (a failing entry point restarts every 5 seconds without end); `KillMode=mixed` |
| The unit's standard output is a journal socket | supervisord could not open `stdout_logfile=/dev/stdout`: `spawnerr: unknown error making dispatchers for 'lydiabox-guest': ENXIO` |
| The machine keeps its own shared libraries | glibc 2.41 (Debian 13) of the template; `libdbus-1.so.3` is the template's 1.16.2, so the image's `dbus-run-session`, `dbus-launch`, `dbus-send`, `dbus-monitor`, and `dbus-uuidgen` fail with ``version `LIBDBUS_PRIVATE_1.14.10' not found``; Xvfb, Xfce, ffmpeg, Chrome, Blender, Node.js, and git load |
| The machine user lingers and has a session bus | `/var/lib/systemd/linger/alice`; `user@1000.service` runs from boot; `/run/user/1000/bus` exists; the image of core `802e85c` ran Xfce on it (the next image does not: [below](#panel-dialog-home-and-environment)) |
| No X server or desktop of the base machine runs at boot; `:0` is free | `/tmp/.X11-unix` is empty, no `Xvfb`, `Xorg`, or VNC process; `tigervncserver@.service` is disabled; only sshd listens (port 22) |
| The root disk is kept across stop and start | files that exec wrote under `/usr/local/sbin` and `/etc/lydiabox` were there after `stop` and `start`; `MACHINE.md` says both disks survive a stop |
| A private port with `Authorization: Bearer` reaches the machine from a server | the cell's WebSocket dials got `502` (not `401`) while nothing listened on port 9000 |

With the fixes of this page applied in place, after `systemctl restart`
and after a stop and start: all three programs `RUNNING` as `alice`,
`lydiabox-guest` listens on `*:9000` and answers `426` to a plain GET and
`101` to a WebSocket upgrade, Xfce runs on `/run/user/1000/bus`, and
`xdpyinfo` answers on `:0`.

### Desktop video through the relay

In the first tenant test (2026-11-16) the browser's ICE connection was
relay-only on both sides through `turn.example.com` over UDP, the input
data channel worked, and video RTP arrived with 8 to 14 % loss, but no
frame was decoded. The cause is the packet size, measured on the same
machine and from the tester's network:

| Fact | Evidence |
| :--- | :--- |
| Outbound UDP from the machine to the TURN server works, up to a 1500-byte IP packet; a fragmented datagram does not arrive | STUN Binding requests padded to a size: 1472-byte UDP payload (1500 IP) answered 4/4, 1473 and larger 0/4; `eth0` MTU 1500; ICMP `ping -M do -s 1472` to the TURN server passes |
| The tester's network passes at most a 1452-byte IP packet to the TURN server | the same STUN probe from the tester's Mac: 1424-byte payload answered, 1428 and larger not |
| ffmpeg's default RTP packet is 1472 bytes; relayed, it does not fit | 1472 + 10 or 16 (SRTP) + 4 (TURN ChannelData) + 28 (UDP, IPv4) = 1514 to 1520 bytes |
| Only keyframes have full-size packets | ffmpeg with the old arguments, 25 s on `:0`: 668 packets, 98 of them 1400 to 1472 bytes (14.7 %, the loss that the browser saw); keyframes 44 to 57 kB every 250 frames (10.4 s), P frames 250 to 500 bytes |
| Not the cause: codec, payload type, profile | ffmpeg 6.1.1 with libx264 in the machine makes Constrained Baseline level 4.0, payload type 96, SPS and PPS in a STAP-A before each IDR; the guest rewrites the payload type to the negotiated one; a pion receiver on the machine assembles the real stream (below) |

With the fixed guest (RTP packets of at most 1100 bytes, a keyframe every
48 frames), a pion receiver on the machine over loopback, with the real
ffmpeg and display, negotiated payload type 106
(`profile-level-id=42e01f;packetization-mode=1`), got packets of at most
1100 bytes, and assembled a 55 kB keyframe every 2.00 s. A still desktop
then needs about 265 kbit/s instead of 140 kbit/s (one keyframe every 2 s
instead of every 10.4 s); with motion the encoder keeps the 2500 kbit/s
target. `TestRelayedKeyframeFitsPathMTU` repeats the failure without a
browser: relay-only on both sides through an in-process TURN server, on a
path that drops packets above 1452 bytes.

### Panel dialog, home, and environment

Read on the same machine (`m_example0000000000000000000`, snapshot of
core `802e85c`) on 2026-11-16 and 2026-11-17, through exec:

| Fact | Evidence |
| :--- | :--- |
| The dialog "Are you sure that you want to remove ...?" comes from the panel plugins that run in `wrapper-2.0` processes | three `wrapper-2.0` processes (`libsystray.so`, `libpulseaudio-plugin.so`, `libactions.so`) each had a mapped 562x123 window of type `_NET_WM_WINDOW_TYPE_DIALOG` with `_NET_WM_STATE_MODAL` (`xprop -root _NET_CLIENT_LIST`, `xwininfo`); the user saw the top one, so its name changed between machines. The plugins that load in the panel's process opened none |
| The cause is a version mismatch of the panel and its library | `xfce4-panel --version` is `xfce4-panel 4.18.4 (Xfce 4.20)`: the program and `wrapper-2.0` are the image's (Ubuntu 4.18.4, their checksums differ from the machine's packages), but `libxfce4panel-2.0.so.4` and the plugin modules (`libactions.so`) are the machine's Debian 13 packages (4.20.4-1); the 4.20 library has one more provider function (`xfce_panel_plugin_provider_emit_hidden_event`) than the 4.18 one. The machine's dpkg database is Debian 13's, with Xfce 4.20 installed |
| Some files of the image replace the machine's, others do not | `/etc/xdg/xfce4/panel/default.xml` has the checksum of the image's file (`4f577a33...`), not of the machine's conffile (`9ddd36f5...`); `/usr/bin/xfce4-panel` and `wrapper-2.0` are the image's; the `.so` files and `/usr/bin/dbus-daemon` (1.16.2, the image has 1.14.10) are the machine's |
| Xfce keeps its settings in InstaBox's home, whatever `HOME` the session has | `xfconfd` is started by the user manager's bus (`/run/user/1000/bus`) with `HOME=/data/home` (`/proc/<pid>/environ`); after `/home/agent` moved to `/data/lydiabox/home` in the trial, the panel still read `/data/home/.config/xfce4/xfconf/xfce-perchannel-xml/xfce4-panel.xml` (with `systray`, `pulseaudio`, `actions`), and the three dialogs came back; `xfconfd` rewrote that file. The machine's `dbus-daemon --session` runs as UID 1000 with an address of its own |
| The default panel names plugins that are not installed | `xfce.log`: `Plugin "(null)-7"`, `"power-manager-plugin-9"`, and `"notification-plugin-10"` `was not found and has been removed from the configuration` at the first start |
| `polkit.service` does not exist | the journal has `Activation via systemd failed for unit 'polkit.service'` for `org.freedesktop.PolicyKit1`, asked by `systemd-logind` and `xfce4-session` when the panel started; no window. The image's panel has no `actions` plugin (log out, shut down), which asked for these actions |
| InstaBox's home is used by LydiaBox in the old layout | `/home/agent -> /data/home`; `claude mcp list` with `HOME=/data/home` connects `instabox` (`open-computer-use` fails to connect), `codex mcp list` lists both from `/etc/codex/config.toml` |
| `GEMINI_API_KEY` of 14 characters is LydiaBox's | it is only in LydiaBox's PTYs (`ptymanager.DefaultEnv`, `lydiabox-local`); the unit, `/etc/environment`, `/etc/profile.d`, and `/data/home/.profile` set no such variable |

The scripts and settings of this change, installed in place on that
machine (old layout, released guest binary of core `802e85c`) with one
`systemctl restart instabox-image.service` (2026-11-16 15:16 UTC):

- the journal has `moved the agent home: /home/agent linked to /data/home
  ...` (from the first trial restart at 15:11), `set /etc/codex/config.toml
  from the image`, `set /etc/xdg/xfce4/panel/default.xml from the image`,
  and `linked /usr/local/bin/claude to /usr/bin/claude` (also `codex`,
  `opencode`, `dsh`); `/home/agent -> /data/lydiabox/home` (mode 0700,
  owner `alice`); `/data/lydiabox/home-moved.log` has one line;
- the windows on `:0` (`xprop -root _NET_CLIENT_LIST`) are three
  `xfce4-panel` docks and the `xfdesktop` desktop, no dialog; no
  `wrapper-2.0` process; the new home's panel settings have
  `applicationsmenu`, `tasklist`, `pager`, `clock`, `showdesktop`,
  `launcher`, `directorymenu`, and `separator` only; no `was not found`
  line and no polkit line in the journal after the restart;
- `xfconfd` has `HOME=/home/agent` on `/run/user/1000/lydiabox-session-bus`;
- `/data/home` (file list, modes, owners, links, checksums) is the same
  before and after the restart;
- the guest daemon has `LYDIABOX_GUEST_HOME_ROOT=/data/lydiabox/home`
  (the old driver's `guest.env` still sets `/data/home`; the guest log has
  `ignoring LYDIABOX_GUEST_HOME_ROOT`) and none of systemd's variables;
- with the PTY environment of the new guest (`HOME=/home/agent`, in
  `/data/workspace`): `claude` is `/usr/lib/node_modules/@anthropic-ai/claude-code`
  2.1.325 and `claude mcp list` prints `No MCP servers configured`;
  `codex` is `/usr/lib/node_modules/@openai/codex` 0.169.0 and
  `codex mcp list` prints `No MCP servers configured yet`; the new home
  has no `.claude/skills`, `.agents`, or `AGENTS.md`.

In the first trial restart (15:11), with the image's panel default but
Xfce still on the user manager's bus, the three dialogs came back, and
`xfconfd` rewrote `/data/home/.config/xfce4/xfconf/xfce-perchannel-xml/xfce4-panel.xml`
(a file that LydiaBox's own desktop wrote at the first start of the
machine): the reason for Xfce's own bus.

## What the documentation does not state

Each item needs a check with a real account (see the checklist below).

1. **Inbound UDP.** Ports are HTTPS (and raw TCP behind TLS, paid, in
   preview); mosh's UDP is relayed by the SSH gateway only. Nothing says
   that a machine can receive UDP, so the WebRTC desktop stream must use
   the TURN relay ([turn-manual-test.md](turn-manual-test.md), the TURN key
   of `lydiabox cloudflare setup`). Outbound UDP to the TURN server is
   not stated; verified on a real machine, 2026-11-16: it works up to a
   1500-byte IP packet, and IP fragments do not pass
   ([above](#desktop-video-through-the-relay)).
2. **Root disk across stop and start.** Verified on a real machine,
   2026-11-16: the root disk is kept ([above](#verified-on-a-real-machine-2026-11-16)).
3. **Cold start, resume, and import times.** `readyAt - createdAt` is the
   create time, but no figures are given for create from a large
   snapshot, start from stopped, resume from paused, or the import of a
   multi-GB image ("a few minutes").
4. **Image size limits.** No limit on the image is stated, but the
   import machine (`agent-browser`) has a 16 GiB root disk that holds the
   pulled image (`/var/lib/containerd`) and the files laid out from it, so
   every MB of image costs about 2 MB of that disk. The image of core
   `dbb41c1` (8.36 GB in `docker images`, which counts the compressed and
   the unpacked layers) filled the disk and the import failed
   (2026-11-19). `install-harnesses.sh` removes what no program reads
   (`prune_node_modules`, [harness-formats.md](harness-formats.md#acp-agents)):
   the Ubuntu-based image of core `cdb5021` was 6.48 GB in `docker images`
   (1.66 GB compressed, 4.82 GB of layers; `/usr/lib/node_modules` 1962
   MB), the slim one 1.1 GB. The Debian 13 image (`debian:trixie-slim`
   base) is 6.60 GB in `docker images` (1.70 GB compressed, 4664 MiB of
   files; `/usr/lib/node_modules` 1962 MB). A `debian:trixie` base, with
   the translations and changelogs of the full container image, was 6.73
   GB, above the limit. Keep the full image at or below the 6.66 GB that
   imported (core `6109c08`: 1.71 GB compressed, 4691 MiB of files).
5. **Machine user of an imported image.** Verified on a real machine,
   2026-11-16: UID 1000 keeps the machine's name and home; exec runs as
   that user; the links stay
   ([above](#verified-on-a-real-machine-2026-11-16)); the files API writes
   as that user (the driver's `guest.env` belongs to `alice`, mode 0600).
   SSH was not checked.
6. **Display conflicts.** Verified on a real machine, 2026-11-16: nothing
   takes `:0` at boot. Which display `POST /v1/machines/{id}/desktop`
   (the base machine's VNC desktop) uses is not checked.
7. **Unit environment.** Verified on a real machine, 2026-11-16 (image
   `ENV` without `HOME` and `PATH`, `Restart=on-failure`, 5 s). Whether the
   create-time `env` reaches the unit is not checked (the driver sets none).
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
5. In the machine (done once on 2026-11-16; repeat for each new
   snapshot): `journalctl -u instabox-image.service -b` (a
   `machine user ... home /home/agent` line, no `ERROR:`),
   `sudo supervisorctl -c /etc/lydiabox/instabox-supervisord.conf status`
   (`xvfb`, `xfce`, and `lydiabox-guest` `RUNNING`),
   `sudo ss -ltnp | grep :9000` (`lydiabox-guest`),
   `curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:9000/` (`426`),
   `getent passwd 1000`, `readlink /home/agent /workspace`
   (`/data/lydiabox/home`, `/data/workspace`), and
   `sudo tail /var/log/supervisor/lydiabox-guest.log`. Desktop: as UID
   1000 with `DISPLAY=:0`, no `_NET_WM_WINDOW_TYPE_DIALOG` window among
   `xprop -root _NET_CLIENT_LIST`, and `pgrep -af wrapper-2.0` finds
   nothing. Harnesses: with `HOME=/home/agent` in `/data/workspace`,
   `codex mcp list` and `claude mcp list` name no `instabox` or
   `open-computer-use` server, and `readlink -f "$(command -v claude)"`
   is below `/usr/lib/node_modules`.
6. Write `guest.env` with `PutFileBytes` (WebSocket listener, link
   secret), publish port 9000 with `PublishPrivatePort`, and run the
   probe through the port proxy (fact 8):
   `LYDIABOX_IMAGE_LINK_URL=wss://9000-<id>.<domain>/ LYDIABOX_IMAGE_LINK_SECRET=... LYDIABOX_IMAGE_LINK_TOKEN=ibp_... go test ./internal/instabox/imagetest -v`. Run a PTY session and a desktop stream through TURN (fact 1:
   also try a direct UDP candidate and record whether it connects).
7. Exec: `Exec` and `ExecStream` with a 5-minute command (keepalive),
   a non-zero exit, and 2 MiB of output.
8. Files: upload and download 100 MB; `StatFile` of `/data/lydiabox/home`.
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
13. Driver, first start. Delete the machines of steps 4 to 12. Start a cell
    with the [driver settings](#settings) (`LYDIABOX_CELL_MODE=true`,
    `LYDIABOX_LISTEN_ADDR=unix:...`, a TURN key,
    `LYDIABOX_IDLE_TIMEOUT=10m`). Check that `lydiabox instabox status`
    shows no machine, that the first file request makes one machine
    `lbx-...` (record the time to the first answer), and that
    `/data/.lydiabox/guest.env` has mode `0600` and no API key, LLM key, or
    refresh token: a search for `ibk_` in `/data` and `/run/instabox`
    finds nothing.
14. Driver, sessions: a Studio session through the magpie gateway, a file
    upload in the Files page, and the desktop through TURN from a network
    without UDP. For the desktop, the picture shows within a few seconds;
    in `chrome://webrtc-internals` the inbound video has a codec and
    `framesDecoded` and `keyFramesDecoded` grow (a keyframe about every
    2 s); the guest log
    (`sudo tail /var/log/supervisor/lydiabox-guest.log`) has
    `desktop viewer joined`, `desktop viewer connected local=relay`,
    `desktop encoder started ... packetSize=1100 keyframeEvery=48`, and
    every 30 s `desktop stream` with `maxPacket` at most 1100 and
    `oversizePackets=0`.
15. Driver, power: leave the cell idle for `LYDIABOX_IDLE_TIMEOUT`; check
    that the machine is `suspended` without `autoSuspendedAt`, and that
    the next request resumes it (record the time). Check the activity
    lease `lydiabox-cell` while a session is open.
16. Driver, self-pause: stop the cell with `kill -9` (no lease release),
    wait for the lease TTL (15 minutes) and the idle pause; start the cell
    again; check `PAUSED` in `GET /api/v1/vm/status`, and that a request
    wakes the machine with `wake`.
17. Driver, restart: send `SIGTERM` to the cell, start it again, and check
    that it attaches to the same machine (no second `POST /v1/machines`)
    and that the activity lease was released at the stop.
18. Driver, errors: start a cell with a revoked key; check the clear
    message in the web status bar and that the cell makes at most one
    InstaBox call every 30 seconds while the page is open. Delete the
    machine in the InstaBox console; check the message, then
    `lydiabox instabox destroy -forget` and a new machine on the next use.
19. Clean up: `lydiabox instabox destroy -confirm <id>`, `DeleteSnapshot`,
    and revoke the key.
