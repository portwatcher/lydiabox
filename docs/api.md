# LydiaBox HTTP and WebSocket API

This document is the contract between the host control plane (`lydiabox up`)
and its clients (the web UI in `web/`, the CLI, and scripts). Every phase
implements its routes against this document. Change this file in the same
commit as any change to a route or a message shape.

Status legend:

- **Implemented (Phase 1)**: served by the current code, covered by tests.
- **Contract (Phase N)**: the shape is fixed here; the phase that owns the
  subsystem implements it.

| Area | Routes | Status |
| :--- | :--- | :--- |
| Health and status | `/api/v1/health`, `/api/v1/ready`, `/api/v1/status` | Implemented (Phase 1; `ready` for cells) |
| Session auth | `/api/v1/auth/*` | Implemented (Phase 1) |
| File Manager | `/api/v1/fs/*` | Implemented (Phase 1) |
| WebDAV gateway | `/webdav/` | Implemented (Phase 1) |
| Virtual S3 gateway | `/s3`, `/s3/` | Implemented (Phase 6) |
| Host connector MCP server | `/mcp/connectors` | Implemented (Phase 4 broker, Phase 6 host route) |
| Web UI | `/` | Implemented (Phase 1 placeholder, Phase 5 app) |
| VM power | `/api/v1/vm/*` | Implemented (Phase 2) |
| Sessions and PTY | `/api/v1/sessions*` | Implemented (Phase 3, guest PTYs from Phase 2) |
| Desktop | `/api/v1/desktop/*` | Implemented (Phase 2 guest and relay, Phase 5 UI) |
| Models | `/api/v1/models/*` | Implemented (Phase 3) |
| Connectors | `/api/v1/connectors/*` | Implemented (Phase 4) |

## 1. Conventions

- Base URL: `http://<LYDIABOX_LISTEN_ADDR>`. All API routes are under `/api/v1`.
  With `LYDIABOX_LISTEN_ADDR=unix:/path` the server listens on that UNIX
  socket (mode `0660`) instead; the Host header is not checked.
- Request and response bodies are JSON (`application/json; charset=utf-8`)
  unless a route says otherwise. Field names are `camelCase`.
- Times are RFC 3339 strings in UTC (`"2026-11-10T06:30:00.123Z"`). A zero
  time is `"0001-01-01T00:00:00Z"` and means "never".
- JSON request bodies are limited to 1 MiB unless a route says otherwise.
  Unknown fields and trailing data are rejected with `400`.
- The tenant is implicit: one process serves one tenant, `"default"` unless
  `LYDIABOX_TENANT_ID` sets another (cell mode, see the README). No route
  takes a tenant parameter.
- Errors use one envelope and a stable `code`:

```json
{ "error": { "code": "not_found", "message": "open workspace/missing.txt: no such file or directory" } }
```

| HTTP | `code` | Meaning |
| :--- | :--- | :--- |
| 400 | `bad_request` | Invalid input, invalid path, wrong file type |
| 401 | `unauthorized` | Missing or invalid credential |
| 403 | `forbidden` | Path escapes its root, cross-origin cookie request, permission denied |
| 404 | `not_found` | Unknown route, root, file, session, or provider |
| 409 | `conflict` | Target exists, or the file changed since it was read |
| 413 | `too_large` | Body or file over the limit |
| 415 | `unsupported_media_type` | File is not UTF-8 text |
| 429 | `rate_limited` | Too many failed authentication attempts (see `Retry-After`) |
| 499 | `canceled` | The client closed the request before the answer (`GET /api/v1/sessions` while the guest discovery waits, a File API request while the machine wakes); the client does not see it |
| 500 | `internal` | Unexpected server error. The server logs one `request failed` line at error level with the route, the status, and the cause (credentials removed) |
| 501 | `not_implemented` | Route exists but the subsystem is not in this build |
| 503 | `unavailable` | A dependency (VM, magpie, WebDAV backend) is not running. An expected state (the VM is not running, or the user stopped it) is logged at info level (`request refused`); other `503` answers are logged like a `500` |
| 504 | `timeout` | The VM did not reach `RUNNING` in time (`POST /api/v1/vm/wake`) |

## 2. Authentication

There is one admin secret, `LYDIABOX_AUTH_TOKEN` (auto-generated on first
boot into `$LYDIABOX_DATA_DIR/state/auth.token`). A request is authenticated
by any one of:

| Credential | Use |
| :--- | :--- |
| `Authorization: Bearer <token>` | CLI, scripts, non-browser WebSocket clients |
| Cookie `lydiabox_session` | Browser UI, set by `POST /api/v1/auth/login` |
| `Authorization: Basic base64("lydiabox:<token>")` | WebDAV clients |
| `Cf-Access-Jwt-Assertion` header or `CF_Authorization` cookie | Browser through Cloudflare Access (section 2.1) |

Rules:

- Every route requires a credential unless marked **public**.
- The session cookie value is `HMAC-SHA256(token, "lydiabox-session-v1")`
  in hex. It is `HttpOnly`, `SameSite=Lax`, `Path=/`, 30 days, and `Secure`
  when the request arrived over TLS or with `X-Forwarded-Proto: https`.
  Rotating the token invalidates every session.
- Cookie-authenticated requests that change state (any method other than
  `GET`, `HEAD`, `OPTIONS`) and cookie-authenticated WebSocket upgrades must
  carry an `Origin` (or `Referer`) whose host equals the request `Host`, or
  no such header. Otherwise the server answers `403 forbidden`.
- Browsers open WebSockets with the cookie. Tokens never go in query strings.
- After 10 failed attempts within one minute, a client IP gets `429` for the
  rest of that minute.
- Unauthenticated responses carry `WWW-Authenticate: Bearer realm="LydiaBox"`,
  or `Basic realm="LydiaBox"` under `/webdav/`.

### 2.1 Cloudflare Access — implemented

When Cloudflare Access login is on (`lydiabox cloudflare setup`, or the
`LYDIABOX_ACCESS_*` settings), Cloudflare signs every request that passes
the Access policy with an application token (a JWT). The server accepts it
from the `Cf-Access-Jwt-Assertion` header, or else from the
`CF_Authorization` cookie, and treats the request as the admin when all of
these are true:

- The RS256 signature matches a key of the team JWKS at
  `https://<team-domain>/cdn-cgi/access/certs`. The server caches the keys,
  fetches them again for an unknown key ID (at most once per 30 seconds),
  and refreshes them after one hour.
- `iss` is `https://<team-domain>` and `aud` contains the audience tag of
  the Access application.
- `exp`, `nbf`, and `iat` are valid, with 60 seconds of clock skew.
- `email` is on the allow list (an entry `@example.com` allows a domain).
  Service token logins have no email and do not count as the admin.

Other rules:

- An `Authorization` header always decides on its own: a request with a
  wrong Bearer or Basic credential gets `401`, even with a valid Access
  token.
- Access-authenticated requests get the same `Origin` check as the session
  cookie, because the browser sends the `CF_Authorization` cookie on
  cross-site requests too.
- For a request from loopback (cloudflared), the failed-attempt limiter uses
  the `Cf-Connecting-IP` header as the client IP, so one client behind the
  tunnel cannot lock out the others.
- The admin token keeps working for Bearer, Basic, and cookie clients.
- To sign out of Access, the UI opens `/cdn-cgi/access/logout`, which
  Cloudflare serves on the application hostname.

Non-browser clients through the tunnel send the Access service token headers
`CF-Access-Client-Id` and `CF-Access-Client-Secret` (printed by
`lydiabox cloudflare setup`) together with the admin token as usual. Paths
set up with `-bypass` (for example `/webdav`, `/s3`) skip Access and need
only the admin token.

### `POST /api/v1/auth/login` — public, implemented

Request: `{ "token": "<LYDIABOX_AUTH_TOKEN>" }`
Response `200`: `{ "authenticated": true, "method": "token", "access": false }`
and `Set-Cookie: lydiabox_session=...`
Errors: `401 unauthorized`, `429 rate_limited`.

### `POST /api/v1/auth/logout` — public, implemented

Clears the cookie. Response `204`.

### `GET /api/v1/auth/session` — public, implemented

Response `200`:

```json
{ "authenticated": true, "method": "access", "email": "me@example.com", "access": true }
```

- `method` is `token` (cookie, Bearer, or Basic) or `access` (section 2.1).
  It is omitted when `authenticated` is false.
- `email` is the Cloudflare Access email. When `authenticated` is false, an
  `email` means a valid Access login whose email is not on the allow list.
- `access` is true when Cloudflare Access login is on.

The UI calls this on load to decide between the login screen and the app.

## 3. Health and status

### `GET /api/v1/health` — public, implemented

Response `200`: `{ "status": "ok" }`. Liveness only; no dependency checks.

### `GET /api/v1/ready` — public, implemented

Response `200`: `{ "status": "ready", "tenantId": "alice" }`. The server
serves only after every subsystem has started, so an answer means the
cell is ready; a cell host checks the tenant ID to be sure that it reached
the right cell. The VM can be stopped or paused: it wakes on the first use,
but a VM that the user stopped starts only on an explicit start (section 7).

### `GET /api/v1/status` — implemented

```json
{
  "version": { "version": "v0.1.0", "commit": "6123f30", "buildDate": "2026-11-10T06:30:00Z",
               "goVersion": "go1.24.1", "platform": "linux/amd64" },
  "tenantId": "default",
  "vm": { "state": "STOPPED", "driver": "chv" },
  "storage": {
    "tenantId": "default",
    "mode": "juicefs",
    "mounted": true,
    "syncEnabled": true,
    "lastSyncAt": "2026-11-10T06:29:41Z",
    "lastUploadAt": "2026-11-10T06:20:41Z",
    "lastSyncError": "",
    "s3Gateway": { "enabled": true, "addr": "127.0.0.1:8090", "path": "/s3", "bucket": "lydiabox", "running": true }
  },
  "time": "2026-11-10T06:30:00Z"
}
```

- `vm.state` is one of `STOPPED`, `BOOTING`, `RUNNING`, `PAUSED`.
- `vm.driver` is the resolved backend: `chv` or `docker`.
- `storage.mode` is the storage mode, `juicefs` or `provider`
  (`LYDIABOX_STORAGE_MODE`). In the `provider` mode `mounted` is always
  false, `lastSyncAt` and `lastUploadAt` refer to the state database backup
  only (zero time without S3 settings), and `s3Gateway.enabled` is false.
- `storage.syncEnabled` is true when the server backs up to S3: always in
  the `juicefs` mode, and in the `provider` mode only with S3 settings. The
  UI shows no sync state when it is false. Older servers omit it; clients
  then treat a zero `lastSyncAt` without `lastSyncError` in the `provider`
  mode as no sync.
- `storage.lastSyncAt` is the last successful metadata backup check (the UI
  shows "Synced Xs ago" from `time - lastSyncAt`). `lastUploadAt` is the
  last time a changed snapshot was uploaded. `lastSyncError` is omitted when
  empty.
- `storage.s3Gateway` describes the virtual S3 gateway (section 5a).
  `addr` is the backend address, `LYDIABOX_S3_GATEWAY_ADDR`; `path` is the
  public path on this server. `enabled` is false (and the other fields
  empty) when the address is empty or `off`. `running` is false while the
  process restarts.

## 3a. Remote access — implemented

### `GET /api/v1/remote-access`

Response `200`:

```json
{
  "provider": "cloudflare",
  "configured": true,
  "hostname": "agent.example.com",
  "teamDomain": "myteam.cloudflareaccess.com",
  "emails": ["me@example.com"],
  "serviceTokenClientId": "0123abcd.access",
  "bypassPaths": ["/webdav"],
  "turnKeyId": "0123456789abcdef0123456789abcdef",
  "turnInUse": true,
  "tunnel": { "state": "connected", "restarts": 0, "startedAt": "2026-11-14T08:00:00Z" }
}
```

- `configured` is true when Cloudflare Access login is on. `hostname`,
  `serviceTokenClientId`, and `bypassPaths` come from
  `lydiabox cloudflare setup` and are omitted for a setup by
  `LYDIABOX_ACCESS_*` only.
- `turnKeyId` is the Cloudflare Realtime TURN key that setup stored.
  `turnInUse` is true when the desktop stream relays through it, that is,
  when no STUN or TURN setting replaces it (section 9).
- `tunnel` is omitted when LydiaBox does not run cloudflared. `state` is
  `connecting`, `connected` (at least one connection to the Cloudflare edge),
  `error` (with `lastError`, for example a missing binary), or `stopped`.

## 4. File Manager — implemented (Phase 1)

Two roots exist. Where they are depends on the storage mode
(`LYDIABOX_STORAGE_MODE`):

| `{root}` | `juicefs` mode (host directory) | `provider` mode (guest directory, default) | Guest path |
| :--- | :--- | :--- | :--- |
| `workspace` | `$DATA_DIR/mnt/jfs/workspace` | `LYDIABOX_GUEST_WORKSPACE_ROOT`, `/workspace` | `/workspace` |
| `home` | `$DATA_DIR/mnt/jfs/home/agent` | `LYDIABOX_GUEST_HOME_ROOT`, `/home/agent` | `/home/agent` |

- `juicefs` mode: the File API works on the host JuiceFS mount and does not
  need the VM.
- `provider` mode: the trees are on the machine disk, and the host reaches
  them through the guest daemon file methods (`fs.*` on the guest RPC). A
  request takes a `files` lease (section 7) and wakes a `PAUSED` or
  `STOPPED` machine, but not a machine that the user stopped: that request
  fails at once with `503 unavailable` ("the VM was stopped; start it to use
  these files"). When the machine does not answer within 90 s, the
  request fails with `503 unavailable` ("the agent machine did not start
  within 1m30s; retry the request"). Invalid paths fail with `400` without
  a wake. The guest applies the same path rules with its own `os.Root`
  sandbox and runs every file operation as the agent user.

Path rules (`path`, `dir`, `from`, `to`, and upload file names):

- Relative to the root, `/`-separated, no leading `/`. `""` or `"."` is the
  root itself.
- Rejected with `400 bad_request`: absolute paths, any `..` segment, `\`,
  NUL bytes, and percent-encoded traversal in upload file names.
- Symlinks are followed only while they stay inside the root. A symlink that
  resolves outside the root gives `403 forbidden`. Every lookup goes through
  Go `os.Root`, so the check is enforced by the kernel at each path step.
- The root itself cannot be renamed, deleted, or written as a file
  (`400 bad_request`).

`Entry` object:

```json
{ "name": "main.go", "path": "src/main.go", "type": "file", "size": 1832,
  "mode": "0644", "modTime": "2026-11-10T06:12:33.120457Z" }
```

`type` is `file`, `dir`, `symlink`, or `other`. For the root, `name` and
`path` are `""`. Files created through the API are owned by uid/gid 1000
when the host runs as root (`juicefs` mode), or by the agent user
(`provider` mode).

### `GET /api/v1/fs/roots`

```json
{ "roots": [ { "name": "workspace", "guestPath": "/workspace" },
             { "name": "home", "guestPath": "/home/agent" } ] }
```

In the `provider` mode `guestPath` is the directory that a running guest
reports (for example `/data/lydiabox/home` on InstaBox); this route does not wake
the machine, so it shows the last answer or the defaults.

### `GET /api/v1/fs/{root}/list?path=<dir>[&auto=1]`

Response `200`: directories first, then case-insensitive name order.
`auto=1` marks a list that must not wake the VM: in the `provider`
storage mode it answers `503 unavailable` while the VM does not serve,
instead of starting or resuming it. A list without the mark starts or
resumes the VM; the web Files page sends no mark.

```json
{ "root": "workspace", "dir": { "name": "", "path": "", "type": "dir", "size": 4096, "mode": "0755", "modTime": "..." },
  "entries": [ { "name": "src", "path": "src", "type": "dir", "size": 4096, "mode": "0755", "modTime": "..." } ] }
```

Errors: `404` missing, `400` not a directory.

### `GET /api/v1/fs/{root}/stat?path=<path>`

Response `200`: one `Entry` (a final symlink is described, not followed).

### `GET /api/v1/fs/{root}/text?path=<file>`

Reads a UTF-8 text file of at most 8 MiB.

```json
{ "name": "main.go", "path": "src/main.go", "type": "file", "size": 12, "mode": "0644",
  "modTime": "2026-11-10T06:12:33.120457Z", "content": "package main" }
```

Errors: `400` directory or special file, `413 too_large`,
`415 unsupported_media_type` (invalid UTF-8 or NUL bytes).

### `PUT /api/v1/fs/{root}/text?path=<file>`

Creates or atomically replaces a text file (temporary file, then
`renameat`). The parent directory must exist. An existing file keeps its mode.

Request:

```json
{ "content": "package main\n", "baseModTime": "2026-11-10T06:12:33.120457Z" }
```

`baseModTime` is optional. When present and the file exists with a different
`modTime`, the save fails with `409 conflict` so the editor can reload.
Response `200`: the new `Entry`.

### `GET /api/v1/fs/{root}/download?path=<file>[&inline=true]`

Streams the raw bytes. Supports `Range`, `If-Modified-Since`, and `HEAD`.
Headers: `Content-Disposition: attachment; filename="..."` (or `inline`),
`X-Content-Type-Options: nosniff`, `Content-Security-Policy: sandbox`.
Directories return `400`.

### `POST /api/v1/fs/{root}/upload?dir=<dir>[&overwrite=true]`

`multipart/form-data`. Every part named `file` is stored at
`<dir>/<filename>`. The `filename` may contain a relative path for folder
drag-and-drop (`FormData.append("file", blob, entry.webkitRelativePath)`),
and missing directories are created. Parts are streamed to disk. Other part
names are ignored. The request is limited to 10 GiB.

Without `overwrite=true`, an existing target gives `409 conflict`. Processing
stops at the first failing file; files stored before it remain, and the error
message states how many were stored.

Response `201`:

```json
{ "files": [ { "name": "a.txt", "path": "photos/2026/a.txt", "type": "file", "size": 5,
               "mode": "0644", "modTime": "...", "replaced": false } ] }
```

### `POST /api/v1/fs/{root}/mkdir`

Request: `{ "path": "src/pkg", "parents": true }`. With `parents`, missing
ancestors are created and an existing directory is not an error.
Response `201`: the `Entry`. Errors: `409` exists (without `parents`).

### `POST /api/v1/fs/{root}/rename`

Request: `{ "from": "a.txt", "to": "docs/a.txt", "overwrite": false }`.
Moves within one root. The destination parent must exist. A directory cannot
move into itself (`400`). An existing destination gives `409` unless
`overwrite` is true; a directory is never overwritten (`400`).
Response `200`: the `Entry` at the new path.

### `DELETE /api/v1/fs/{root}/entry?path=<path>[&recursive=true]`

Deletes a file, symlink (the link, not its target), or empty directory. A
non-empty directory needs `recursive=true`. Response `204`.

## 5. WebDAV gateway — implemented (Phase 1)

In the `provider` storage mode WebDAV and the S3 gateway (section 5a) are
not available: `/webdav`, `/webdav/...`, `/s3`, and `/s3/...` answer
`501 not_implemented` ("WebDAV and the /s3 gateway are not available in the
provider storage mode; use the File API (/api/v1/fs)").

`/webdav/` reverse-proxies a supervised `juicefs webdav` bound to a random
`127.0.0.1` port. The WebDAV root is the JuiceFS volume root, so
`/webdav/workspace/` and `/webdav/home/agent/` are the two user trees.

- Credentials: user `lydiabox`, password `LYDIABOX_AUTH_TOKEN` (Basic Auth).
  Bearer and cookie credentials also work.
- The proxy rewrites the `Destination` header, `Location` headers, and
  `<href>` elements of XML responses between `/webdav/...` and the backend.
- `503` when the backend is restarting.
- Mount examples: macOS Finder "Connect to Server" `http://host:8080/webdav/`;
  Linux `rclone mount :webdav: --webdav-url=http://host:8080/webdav/`.

## 5a. Virtual S3 gateway — implemented (Phase 6)

`/s3` and `/s3/...` serve a supervised `juicefs gateway` on
`LYDIABOX_S3_GATEWAY_ADDR` (default `127.0.0.1:8090`). The volume is one
bucket named `lydiabox`; object keys are paths from the volume root
(`workspace/...`, `home/agent/...`), as in WebDAV.

- Endpoint `http://host:8080/s3`, bucket `lydiabox`, path-style addressing.
  A request to `/s3/lydiabox/workspace/a.txt` is the object
  `workspace/a.txt` in the bucket `lydiabox`. `GET /s3/` is `ListBuckets`.
- Credentials: access key `lydiabox`, secret key `LYDIABOX_AUTH_TOKEN`
  (AWS Signature Version 4). The routes are public on this server: the
  proxy verifies the signature itself. Bearer, cookie, and CSRF checks do
  not apply.
- The proxy (`internal/storage/s3proxy`) verifies the client signature:
  the `Authorization: AWS4-HMAC-SHA256 ...` header, or a presigned URL
  (`X-Amz-Algorithm`, `X-Amz-Credential`, `X-Amz-Date`, `X-Amz-Expires`,
  `X-Amz-SignedHeaders`, `X-Amz-Signature`). It rebuilds the canonical
  request from the incoming request (method, path as sent, canonical query,
  signed headers, and `x-amz-content-sha256` or `UNSIGNED-PAYLOAD`) and
  compares the signatures in constant time. `X-Amz-Date` must be within 15
  minutes of the server clock; a presigned URL must not be expired
  (`X-Amz-Expires` up to 7 days).
- Then it removes `/s3` from the path, keeps the `Host` header and the
  query, and signs the request again for the backend with the same
  credential scope (date, region, service). It replaces the
  `Authorization` header or the `X-Amz-Signature` value.
- Bodies: `UNSIGNED-PAYLOAD`, a SHA-256 payload hash, and
  `STREAMING-UNSIGNED-PAYLOAD-TRAILER` pass through unchanged. For
  `STREAMING-AWS4-HMAC-SHA256-PAYLOAD` and
  `STREAMING-AWS4-HMAC-SHA256-PAYLOAD-TRAILER`, the proxy verifies the
  client's chunk signature chain and writes the chain for the new seed
  signature. Chunks are limited to 16 MiB.
- Errors are S3 XML documents (`Content-Type: application/xml`):
  `AccessDenied` 403 (no signature, expired or not yet valid presigned
  URL), `SignatureDoesNotMatch` 403, `InvalidAccessKeyId` 403,
  `RequestTimeTooSkewed` 403, and `InvalidRequest` 400 (malformed or
  unsupported authorization).
- `503` when the backend is restarting or the gateway is disabled.
- Examples:
  `aws --endpoint-url http://host:8080/s3 s3 ls s3://lydiabox/workspace/`;
  rclone with `provider = Minio`, `endpoint = http://host:8080/s3`, and
  `force_path_style = true`.

## 5b. Host connector MCP server — implemented (Phase 6)

`POST /mcp/connectors` serves the connector MCP server (JSON-RPC 2.0:
`initialize`, `tools/list`, `tools/call`) on the main listener. It is the
same handler that the guest reaches at `127.0.0.1:8911/mcp` (proxied over
vsock port 9001), with the `github_*`, `gdrive_*`, `notion_*`, and
`slack_*` tools of the tenant. The route needs authentication (Bearer
token or session cookie, as other API routes); tool calls use the stored
connector tokens and are recorded in the connector audit log.
`GET /mcp/connectors` returns `405` with `Allow: POST`: the server has no
SSE stream.

```sh
curl -H "Authorization: Bearer $LYDIABOX_AUTH_TOKEN" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' http://host:8080/mcp/connectors
```

## 6. Web UI — implemented (Phase 5; a placeholder page when `web/` is not built)

`GET /` and every path that is not an API, WebDAV, S3, MCP, or asset path returns the
SPA `index.html` (client-side routes `/studio`, `/desktop`, `/files`,
`/models`, `/connectors`). Files under `/assets/` are cached for one year.
Unknown `/api/...` paths return the JSON `404` envelope. Public.

## 7. VM power — implemented (Phase 2)

`VMStatus` object (extends the `vm` section of `/api/v1/status`):

```json
{
  "state": "RUNNING",
  "driver": "chv",
  "cpus": 4,
  "memMb": 8192,
  "gpu": "0000:01:00.0",
  "bootedAt": "2026-11-10T06:00:00Z",
  "leases": { "pty": 2, "desktop": 1 },
  "idleTimeoutSeconds": 1200,
  "idleSince": "0001-01-01T00:00:00Z",
  "lastError": ""
}
```

`idleSince` is the time the lease count dropped to 0, or the time the VM
became `RUNNING` without leases (zero time while leases exist). `leases`
always lists `pty` (running guest PTYs) and `desktop` (open desktop
sockets). In the `provider` storage mode, `files` counts File API requests
and downloads in progress (it is listed while it is not zero). After `idleTimeoutSeconds` without leases the VM is synced and
paused (`PAUSED`). `gpu` and `lastError` are omitted when empty;
`lastError` holds the last failed boot, transition, or state read (a later
transition that works clears it), for
example `InstaBox refused the API key: check LYDIABOX_INSTABOX_API_KEY
(InstaBox invalid_key)`; the web status bar shows it while the VM is not
running. `transition` (`pausing` or `stopping`, omitted otherwise) is set
while a pause or stop runs; `state` stays `RUNNING` until it ends.
`startBlockedUntil` (omitted otherwise) is set while the provider refuses a
start or resume until a known time (for example the InstaBox cooldown after
the maximum run time); `lastError` says why. `driver` is `chv`, `docker`, or `instabox`. With the `instabox`
driver, `PAUSED` also reports a pause that InstaBox made by itself (an idle
pause); the next request wakes the machine. A provider refusal (key,
payment, scope, quota, no capacity) answers `503` from `wake` and from the
routes that wake the VM, and the driver waits 30 seconds (or the
provider's `Retry-After`) before it tries again.

| Route | Effect | Response |
| :--- | :--- | :--- |
| `GET /api/v1/vm/status` | Read state | `200 VMStatus` |
| `POST /api/v1/vm/wake` | Explicit start (`Start`): ends a user stop, then boots from `STOPPED` or resumes from `PAUSED`; concurrent calls share one boot | `200 VMStatus` once `RUNNING`; `504` after 120 s |
| `POST /api/v1/vm/hibernate` | Flush filesystems (`sync`) and pause. The processes keep their memory; a live terminal detaches (`paused`, see section 8) and attaches again after the wake. A wake asked for during the pause waits for it and then resumes the VM | `200 VMStatus` (`PAUSED`) |
| `POST /api/v1/vm/stop` | Shutdown; body `{ "force": false, "resetRoot": false }` (optional). The guest filesystems are synced first (30 s limit); without `force` a failed sync aborts with `503`, with `force` the VM is stopped anyway. `resetRoot` (docker driver) then removes the stopped container, so the next boot starts from the image; it also works while `STOPPED`. The `chv` driver answers `409` to `resetRoot`. The stop runs to its end when the client goes away (limit 3 minutes) | `200 VMStatus` (`STOPPED`) |

`409 conflict` when the transition is not possible (for example hibernate
or stop while `STOPPED`). A user action that needs the guest (a new session,
opening a terminal, a file operation) wakes the VM itself; the UI does not
need to call `wake` first. Automatic requests (polls, reconnects) never wake
a paused VM: `docs/design.md` section 4.3 has the full table.

A user stop holds: from the start of `POST /api/v1/vm/stop` until the next
explicit start, no request wakes the VM on demand. The desktop socket, the
terminal socket of an existing session, and File API requests in `provider`
mode answer at once (desktop and terminal: `error` message with `code`
`stopped`, then close `1000`; File API: `503`), and the clients do not
retry. Only `POST /api/v1/vm/wake` and `POST /api/v1/sessions` (a new or
resumed session, which the user asked for) start the VM again. An idle
pause (`PAUSED` by the idle timer or by InstaBox) is not a user stop: the
next request wakes the machine. A host shutdown and `hibernate` are not
user stops either.

The hold is a row in the `vm_holds` table of the host state database
(`$DATA_DIR/state/lydiabox.db`: `tenant_id`, `held_at`), so a restart of
the host process (a release, or the idle exit of a cell) keeps it; in a
cell the state database backup carries it too. `stop` writes the row before
the shutdown, and a failed or refused stop puts back the previous value;
an explicit start deletes it. The host reads it after it reads the VM
state and before it serves requests. When the row cannot be read, or the
VM runs although the row is present (the stop did not finish), the VM is
not held, so a start always works.

## 8. Sessions and PTY — implemented (Phase 3)

Harness IDs: `claude-code`, `codex`, `dsh`, `antigravity`, `opencode`, plus
`shell` for a plain login shell.

`Session` object:

```json
{
  "id": "codex:019a6f2c-3b1d-7c0e-9f4a-2d8e5b6c7a10",
  "harness": "codex",
  "nativeId": "019a6f2c-3b1d-7c0e-9f4a-2d8e5b6c7a10",
  "title": "Fix flaky upload test",
  "workspaceDir": "/workspace/lydiabox",
  "model": "deepseek/deepseek-v4-pro",
  "status": "running",
  "live": true,
  "createdAt": "2026-11-10T05:10:00Z",
  "updatedAt": "2026-11-10T06:29:58Z",
  "usage": { "inputTokens": 182344, "outputTokens": 20311, "costUsd": 0.42 }
}
```

- `id` is `<harness>:<nativeId>`; escape it as one path segment.
- `status`: `running` (a PTY is attached), `idle` (discovered on disk, no
  PTY), `exited`. `live` is true while a PTY exists.
- `usage` is omitted when the harness transcript has no usage data.
- Discovery reads harness files from `home/agent` on the host
  (`juicefs` mode), so listing works while the VM is `STOPPED`. In the
  `provider` mode the guest runs the discovery on the machine disk
  (`harness.discover`); while the machine is `PAUSED` or `STOPPED`, the
  list comes from the last result, which the host keeps in the state
  database, and listing never wakes the machine. On-disk formats and what
  is verified are listed in [harness-formats.md](harness-formats.md).

### `GET /api/v1/sessions?harness=<id>&workspace=<guest dir>`

Both filters are optional. Response `200`, newest `updatedAt` first:

```json
{ "sessions": [Session], "cached": false }
```

`cached` is true when the discovered sessions come from the last stored
discovery (`provider` mode, machine not running, or the guest did not
answer). `cachedAt` is then the time of that discovery; it is omitted when
`cached` is false or no discovery was stored yet. Sessions that the host
started are always listed from its own metadata.

### `POST /api/v1/sessions`

Starts a harness PTY in the guest. It starts the VM, also one that the
user stopped: a new or resumed session is an explicit request.

```json
{ "harness": "claude-code", "workspaceDir": "/workspace/lydiabox", "model": "kimi/kimi-k3",
  "resumeNativeId": "", "title": "", "cols": 160, "rows": 48 }
```

`model`, `resumeNativeId` (resume an existing native session), and `title`
are optional. Response `201`: `Session`. Errors: `400` unknown harness or a
`workspaceDir` outside `/workspace` and `/home/agent`, `409 not_installed`
when the program of the harness is not installed in the VM (the message
names it, for example `Claude Code is not installed in the VM (the command
"claude" was not found)`; logged at info level), `503` when the VM or the
guest terminal service is not available.

Details:

- `workspaceDir` defaults to `/workspace` and is created when it is missing.
- `model` is passed to the harness (`--model`) and becomes the binding of
  the harness (section 10): the New session dialog preselects it next time,
  and the harness config uses it when the harness starts without a model. A
  model that is not a `provider/model` ID is passed but not bound.
- Before every start the harness config is pointed at the gateway again
  (`PUT /api/v1/models/bindings` rewrites the same keys).
- `codex`, `dsh`, `opencode`, and `antigravity` pick the native ID of a new
  session at runtime. The response then has a provisional `nativeId`
  (`new-<hex>`); after the harness writes its transcript, the next list
  shows the session under its real ID, and the old ID keeps resolving for
  the WebSocket. `claude-code` and resumed sessions keep their ID.
- A live session holds a `pty` lease, so the VM does not idle-pause.

### `PATCH /api/v1/sessions/{id}`

Changes the model of a live session. Body `{ "model": "kimi/kimi-k3",
"cols": 120, "rows": 40 }` (`cols` and `rows` default to the current
terminal size). The harness process ends like a delete (`SIGINT`, then
`SIGTERM` and `SIGHUP`), and the same native session resumes with the new
model, so the conversation stays; clients attach to the new terminal under
the same session ID. The model becomes the binding of the harness. The
harness of a session cannot change. Response `200 Session`.

`400` for an empty model or a shell session, `404` for an unknown session,
`409` when the session is not running (resume it with `POST` and the model
instead) or when the harness has not saved the session yet (a provisional
`nativeId`).

### `DELETE /api/v1/sessions/{id}[?purge=true]`

Terminates the PTY (`SIGINT`, 3 s, `SIGTERM`, 2 s, then `SIGHUP` for a
program that ignores both, such as an interactive shell, 2 s) and removes
host metadata. A terminal that is not attached (after a host restart or a
pause) is ended in the guest (`pty.kill`) without a wake; when the guest
cannot be reached, the next reconcile ends it. With `purge=true`, the native transcript files are also deleted
(`provider` mode: in the guest, which wakes the machine; `503` when it does
not start). Response `204`.

Without `purge`, the transcript stays, and discovery would find the session
again. The host records the delete (table `session_dismissed`, per tenant),
and the list leaves the session out until its transcript has activity after
the delete (for example `claude --resume` in a shell). A resume through
`POST /api/v1/sessions` with `resumeNativeId` and a purge remove the record. A
failed purge records nothing, so the session stays listed for another try.

### `GET /api/v1/sessions/{id}/transcript`

Returns the read-only history of a session from its native transcript:

```json
{"entries": [
  {"role": "user", "text": "Fix the flaky upload test", "time": "2026-11-10T05:10:05Z"},
  {"role": "tool", "tool": "shell", "text": "go test ./internal/storage/files/"},
  {"role": "assistant", "text": "The test now passes five times in a row."}
], "truncated": false}
```

`role` is `user`, `assistant`, or `tool`. A `tool` entry is a tool call:
`tool` is its name and `text` a short form of its input (the command, the
path, or the compact JSON, at most 400 characters). Tool results, reasoning,
and injected context (system reminders, `AGENTS.md`) are left out. `time`
is absent when the format has no time (Antigravity). The newest entries are
kept: texts over 32 KiB are cut, and older entries are dropped to keep the
texts under 3 MiB; `truncated` is then `true`.

In the `juicefs` storage mode the host reads the files without the VM. In the
`provider` mode the guest reads them (`harness.transcript`), which starts or
resumes the machine; `503` when it does not start or its guest program is
too old. `404` for an unknown session, a shell session, or a session whose
harness has not written a transcript yet; `400` for an invalid ID.

### `GET /api/v1/sessions/{id}/ws` — WebSocket

Subprotocol: `lydiabox.pty.v1`. Several clients may attach to one session
("terminal room"); all receive output and any of them may type.

- **Binary frames** carry raw terminal bytes: client → server is PTY input,
  server → client is PTY output.
- **Text frames** carry one JSON control message each.

On connect the server sends `hello`, then the ring-buffer replay (up to
1 MiB) as binary frames, then live output. A socket without the subprotocol
is closed with status `1008`. When the session has no running terminal, the
server sends an `error` message (`not_found`, `conflict` for a session that
is idle or exited, `unavailable` when the VM cannot be woken) and closes the
socket (`1008`, or `1013` for `unavailable`). When the user stopped the VM,
the server sends `error` with `code` `stopped` and closes with `1000`; the
socket does not start the VM, and the client does not reconnect. A live
terminal that ends because the user stopped the VM also ends with this
`stopped` error instead of an `exit` message. When the VM pauses
(`hibernate`, an idle pause, or a pause by the provider), the process stays
in the paused guest: the server sends `error` with `code` `paused` to each
live terminal as soon as the VM is `PAUSED` (also where the connection to
the frozen guest stays open, as with `docker` and `chv`), closes with
`1000`, and the session stays running. The client does not reconnect at
once; it reconnects when the VM is `RUNNING` again and gets the same
terminal. A client marks its automatic reconnects with `?reconnect=1`: such
a socket does not wake a paused VM (`error` `paused`, close `1000`), while
the first open of a terminal does. If the terminal did
not survive (the provider resumed the machine without its memory), the
reconnect gets `error` `conflict` and the session is `exited`.

A stream that ends without an exit status while the VM is not stopped or
paused (the host shuts down, or the guest link drops) does not end the
session: the socket closes without an `exit` message, the client
reconnects, and the session stays running. Each time the VM becomes
`RUNNING` (also at host start), the host compares the stored sessions with
the guest's terminals: a running terminal is attached again (the session
is live on the same process), an ended or missing one makes the session
`exited` with the end time, and a running `lbx-` terminal that no session
refers to is ended. When the VM stops, every session without a live
terminal is `exited`. The time of an `exited` session is its end time.
Invalid control messages give an
`error` message with `code` `bad_request`; the socket stays open. A client
that falls more than 512 frames behind is disconnected.

Client → server:

```json
{ "type": "resize", "cols": 160, "rows": 48 }
{ "type": "signal", "signal": "SIGINT" }
{ "type": "ping", "id": 7 }
```

`signal` accepts `SIGINT`, `SIGTERM`, `SIGHUP`, `SIGQUIT`.

Server → client:

```json
{ "type": "hello", "sessionId": "codex:019a...", "replayBytes": 52311, "cols": 160, "rows": 48 }
{ "type": "resized", "cols": 160, "rows": 48 }
{ "type": "pong", "id": 7 }
{ "type": "exit", "code": 0, "signal": "" }
{ "type": "error", "code": "unavailable", "message": "VM is not running" }
```

After `exit`, the server closes with status `1000`.

## 9. Desktop — implemented (Phase 2 guest and relay, Phase 5 UI)

Modes: `OBSERVER` (agent controls, browser input ignored) and `TAKEOVER`
(browser input injected, agent `computer_*` tools wait up to 30 s).
Screen coordinates are always in the guest frame, 1920 × 1080.

### `GET /api/v1/desktop/mode` and `PUT /api/v1/desktop/mode`

`GET` response `200`: `{ "mode": "OBSERVER", "changedAt": "...", "changedBy": "user" }`.
`PUT` request: `{ "mode": "TAKEOVER" }`; response `200` with the same shape.
`changedBy` is `user`, `agent`, or `system`. `503` when the VM is not
`RUNNING` (these routes do not wake it). `400` for a mode other than
`OBSERVER` or `TAKEOVER`.

### `GET /api/v1/desktop/ws` — WebSocket signaling

Subprotocol `lydiabox.desktop.v1`. Text frames only, one JSON object each.
The socket needs a running VM and takes a desktop lease; closing it
releases the lease (the guest stops `ffmpeg` when no viewer remains). It
never starts or resumes the VM: for a VM that pauses or is paused the server
sends `{ "type": "error", "code": "paused" }`, for another VM that does not
run (or that the user stopped, section 7) `{ "type": "error", "code":
"stopped" }`, and closes with `1000`. The same codes end an open stream
when the VM pauses (from any source) or the user stops it. The browser
does not reconnect after either code; it opens a new socket when the VM is
`RUNNING` again (the page's start control is `POST /api/v1/vm/wake`).

The browser creates the `RTCPeerConnection` with one `recvonly` H.264 video
transceiver and a data channel labelled `input`, then sends the offer.

Server → client:

```json
{ "type": "hello", "mode": "OBSERVER", "screen": { "width": 1920, "height": 1080 },
  "iceServers": [ { "urls": ["stun:stun.l.google.com:19302"] } ], "iceTransportPolicy": "all" }
{ "type": "answer", "sdp": "v=0..." }
{ "type": "ice", "candidate": { "candidate": "candidate:...", "sdpMid": "0", "sdpMLineIndex": 0 } }
{ "type": "mode", "mode": "TAKEOVER", "changedBy": "user", "changedAt": "2026-11-10T06:30:00Z" }
{ "type": "viewers", "count": 2 }
{ "type": "agent_action", "action": "click", "x": 812, "y": 440, "button": "left", "double": false,
  "text": "", "keys": "", "dx": 0, "dy": 0, "tool": "computer_mouse_click", "timestamp": "2026-11-10T06:30:00.250Z" }
{ "type": "pong", "id": 3 }
{ "type": "error", "code": "unavailable", "message": "desktop stream failed to start" }
```

`agent_action.action` is one of `move`, `click`, `drag`, `scroll`, `type`,
`key`, `screenshot`, `launch`. Fields that do not apply are zero or empty:
`x`/`y` for pointer actions (drag end point; drag start in `fromX`/`fromY`),
`dx`/`dy` for scroll, `text` for `type`, `keys` (for example `"ctrl+l"`)
for `key`, `text` holds the app name for `launch`.

Client → server:

```json
{ "type": "offer", "sdp": "v=0..." }
{ "type": "ice", "candidate": { "candidate": "candidate:...", "sdpMid": "0", "sdpMLineIndex": 0 } }
{ "type": "mode", "mode": "TAKEOVER" }
{ "type": "launch", "app": "chrome" }
{ "type": "ping", "id": 3 }
```

`launch.app` is `chrome`, `blender`, or `terminal`.

`hello.iceServers` are `RTCIceServer` objects; TURN entries have
`username` and `credential`. `hello.iceTransportPolicy` is `all`, or
`relay` when `LYDIABOX_WEBRTC_RELAY_ONLY` is set; the browser passes both
to `RTCPeerConnection`. When the TURN credentials are minted (Cloudflare
TURN or `LYDIABOX_TURN_SECRET`), the server sends a new `hello` with new
credentials before they expire; the browser then builds a new peer
connection and sends a new offer on the same socket.

`mode` and `agent_action` messages go to every socket of the tenant; a
`mode` change from one browser reaches all of them. `viewers` is sent on
every join and leave. An invalid message gives an `error` message with
`code` `bad_request`; the socket stays open. A socket without the
subprotocol is closed with status `1008`. A failed wake or stream start
gives an `error` message with `code` `unavailable`, then close `1011`; a
user stop gives `code` `stopped`, then close `1000`.

Media path: the guest sends all WebRTC media on one UDP port
(`LYDIABOX_WEBRTC_UDP_PORT`, default `50000`), which the VMM driver
forwards from the host (`chv`: nftables DNAT to `172.30.0.2`; `docker`:
published port). The guest advertises the address of the host the browser
connected to (the request `Host`) as its ICE host candidate, so the browser
needs UDP access to that port. With `chv`, a browser on the host itself
uses the guest address `172.30.0.2` directly.

TURN relay: when STUN or TURN settings are configured
(`LYDIABOX_STUN_URLS`, `LYDIABOX_TURN_URLS`, `LYDIABOX_TURN_SECRET`,
`LYDIABOX_CLOUDFLARE_TURN_KEY_ID`), the guest peer gets the same
`iceServers` as the browser, through the guest signaling RPC (in memory,
never on the guest disk). The guest then also gathers a server-reflexive
candidate (its address behind the host NAT) and a relay candidate. Either
side's relay is enough when the other side can send to it, so the stream
works with no inbound port on the host. With `iceTransportPolicy` `relay`
both peers use only relay candidates and the guest skips the forwarded
port, so `LYDIABOX_WEBRTC_UDP_PORT=0` is valid. TURN over UDP, TCP
(`?transport=tcp`), and TLS (`turns:`) are supported. Without these
settings, the guest gets no ICE server, as before.

Video: H.264 Constrained Baseline, `packetization-mode=1` (the guest's
track takes the payload type with `profile-level-id=42e01f` of the offer,
or the first H.264 type when the offer has none). The guest's RTP packets
are at most 1100 bytes, so that a relayed packet (SRTP tag, TURN header,
UDP, IP) fits a 1280-byte path without IP fragments; a keyframe comes
every 2 seconds, so a new viewer, or a viewer that lost a packet, waits at
most 2 seconds for a picture. Picture loss requests are counted in the
guest log, and lost packets are sent again on a NACK.

### Data channel `input` (browser → guest, peer-to-peer)

Messages are JSON text. The guest ignores them in `OBSERVER` mode.

```json
{ "t": "move", "x": 640, "y": 360 }
{ "t": "down", "button": "left" }
{ "t": "up", "button": "left" }
{ "t": "wheel", "dx": 0, "dy": -120 }
{ "t": "keydown", "code": "KeyA", "key": "a" }
{ "t": "keyup", "code": "KeyA", "key": "a" }
{ "t": "text", "text": "héllo" }
```

`button` is `left`, `middle`, or `right`. `code` and `key` are the DOM
`KeyboardEvent` values; the guest maps them to X11 keysyms.

## 10. Models and magpie — implemented (Phase 3)

Upstream API keys stay in the host magpie data directory. Responses never
include key values, only `hasKey`.

| Route | Request | Response |
| :--- | :--- | :--- |
| `GET /api/v1/models/gateway` | | `200 { "status": "running", "addr": "127.0.0.1:3425", "version": "0.1.3531" }` (`status`: `running`, `starting`, `stopped`, `error`) |
| `GET /api/v1/models/providers` | | `200 { "providers": [Provider] }` |
| `POST /api/v1/models/providers` | `{ "kind": "openai", "name": "OpenAI", "baseUrl": "", "apiKey": "sk-..." }` | `201 Provider` |
| `PATCH /api/v1/models/providers/{id}` | any of `name`, `baseUrl`, `apiKey`, `enabled` | `200 Provider` |
| `DELETE /api/v1/models/providers/{id}` | | `204` |
| `GET /api/v1/models` | | `200 { "models": [Model] }` |
| `GET /api/v1/models/routing` | | `200 { "groups": [RoutingGroup] }` |
| `PUT /api/v1/models/routing` | `{ "groups": [RoutingGroup] }` | `200 { "groups": [RoutingGroup] }` |
| `GET /api/v1/models/bindings` | | `200 { "bindings": [Binding] }` (one per harness, `model` empty when unbound) |
| `PUT /api/v1/models/bindings/{harness}` | `{ "model": "kimi/kimi-k3" }` | `200 Binding` (config file rewritten atomically in `home/agent`) |

A binding is the last model of a harness: every session start with a model
sets it (section 8). The web UI has no page to edit bindings; it reads them
to preselect the model of a new session.

```json
Provider:     { "id": "openai", "kind": "openai", "name": "OpenAI", "baseUrl": "", "enabled": true, "hasKey": true,
                "models": ["gpt-5.2"], "status": "ok", "lastError": "" }
Model:        { "id": "openai/gpt-5.2", "provider": "openai", "name": "gpt-5.2", "contextWindow": 400000 }
RoutingGroup: { "name": "budget", "strategy": "fallback", "targets": ["deepseek/deepseek-v4-pro", "kimi/kimi-k3"] }
Binding:      { "harness": "codex", "model": "deepseek/deepseek-v4-pro", "configPath": "/home/agent/.codex/config.toml",
                "updatedAt": "2026-11-10T06:00:00Z" }
```

`strategy` is `fallback` or `round-robin`. Model IDs are `provider/model`.

Details:

- The gateway is a supervised `magpie serve` on `LYDIABOX_MAGPIE_ADDR`
  (default `127.0.0.1:3425`) with data in
  `$LYDIABOX_DATA_DIR/state/magpie`; it restarts with backoff (1 s to 30 s).
  `GET /api/v1/models/gateway` answers `200` in every state. The other
  provider, model, and routing routes answer `503 unavailable` while magpie
  is not running. Bindings work without magpie.
- `POST /api/v1/models/providers`: without `baseUrl`, `kind` names a magpie
  preset (`openai`, `anthropic`, `deepseek`, …). With `baseUrl` (an absolute
  `http(s)` URL), the provider is a custom endpoint: `kind` `anthropic` or
  `gemini` selects that protocol, any other kind is OpenAI compatible and is
  reported as `openai-compatible`. The `id` is the lowercase `name` with
  other characters as `-`; `409 conflict` when it exists.
- Provider `status`: `ok`, `pending` (models not listed yet), `error` (see
  `lastError`), or `disabled`.
- `PUT /api/v1/models/routing` replaces the whole set: groups that are not in
  the body are deleted. Group names must be unique; each needs at least one
  `provider/model` target.
- `PUT /api/v1/models/bindings/{harness}`: `model` is required; an empty
  string removes the binding and leaves the model in the config file as it
  is. `400` for an unknown harness or a malformed model ID, `409` when the
  existing config file cannot be parsed (it is not changed).
- Harness configs keep user keys and comments: JSON files change only the
  managed keys, `config.toml` and `.env` are edited line by line. Every
  harness gets the MCP servers `lydiabox` (`http://127.0.0.1:8910/mcp`) and
  `lydiabox-connectors` (`http://127.0.0.1:8911/mcp`). Harnesses inside the
  VM send the placeholder token `lydiabox-local`; the host side of the
  gateway tunnel replaces it with the VM gateway key `lydiabox-vm`.
- Claude Code starts at the prompt: `~/.claude.json` gets
  `hasCompletedOnboarding` (no theme step or security notes) and
  `hasTrustDialogAccepted` for `/workspace`, `/home/agent`, and their InstaBox
  link targets `/data/workspace` and `/data/lydiabox/home` (no folder trust
  question; Claude Code also trusts subfolders). A value that the user set
  stays.

### 10.1 Subscriptions

A user can sign in to a Claude subscription (for Claude Code), a ChatGPT
subscription (for Codex), or a Google account with Antigravity (for
Antigravity) instead of adding an API key. magpie does the
sign-in on the host and keeps the access and refresh tokens in
`$LYDIABOX_DATA_DIR/state/magpie` (`logins.json`, and the vendor CLI files
in its private `home`). magpie refreshes them. No route returns a token, and
no token is written to `/home/agent`: harnesses keep the gateway URL and the
placeholder token, and bind to the subscription models (`claude/...`,
`codex/...`, `antigravity/...`) like any other model.

| Route | Request | Response |
| :--- | :--- | :--- |
| `GET /api/v1/models/subscriptions` | | `200 { "subscriptions": [Subscription] }` (always `claude`, `codex`, and `antigravity`) |
| `POST /api/v1/models/subscriptions/logins` | `{ "kind": "codex" }`, `{ "kind": "antigravity" }`, `{ "kind": "claude" }`, or `{ "kind": "claude", "setupToken": "sk-ant-oat01-..." }` | `201 Login` |
| `GET /api/v1/models/subscriptions/logins/{id}` | | `200 Login` |
| `POST /api/v1/models/subscriptions/logins/{id}/code` | `{ "code": "..." }` | `200 Login` |
| `DELETE /api/v1/models/subscriptions/logins/{id}` | | `204` (cancels the sign-in) |
| `DELETE /api/v1/models/subscriptions/{kind}?account=<user>` | | `204` (magpie deletes the stored sign-in) |

```json
Subscription: { "kind": "claude", "name": "Claude", "providerId": "claude", "enabled": true,
                "accounts": [{ "user": "me@example.com", "plan": "max", "enabled": true, "needsSignIn": false }],
                "models": ["claude-sonnet-4-5"] }
Login:        { "id": "YlNHut3ORIVR", "kind": "codex", "state": "waiting", "url": "https://auth.openai.com/codex/device",
                "userCode": "ABCD-1234", "needsCode": false, "installing": "", "account": "", "error": "" }
```

Details:

- `kind` `codex` uses the ChatGPT device code: open `url` and enter
  `userCode`. `kind` `claude` first installs Claude Code on the host
  (`state` `installing`), because magpie serves a Claude subscription
  through Anthropic's own Claude Code. When `needsCode` is true, send the
  code (or the address) that the vendor page shows at the end to
  `.../code`.
- `kind` `antigravity` uses the Google sign-in under Antigravity's own
  OAuth client. `needsCode` is true: after the sign-in, Google sends the
  browser to a `localhost` address of the host, which the browser of the
  user cannot load. Send that full address to `.../code`.
- Login `state`: `starting`, `installing`, `waiting`, `done` (`account` is
  set), `failed` (see `error`), or `canceled`. Poll `GET .../logins/{id}`
  while the state is `starting`, `installing`, or `waiting`.
- `setupToken` (Claude only) adds the account from `claude setup-token`
  directly; the response has `state` `done` and no `id`.
- `providerId` is empty until an account is signed in. The subscription
  provider is also in `GET /api/v1/models/providers` with `"subscription":
  "claude"`, `"codex"`, or `"antigravity"`. Its `PATCH` takes only `enabled`; `DELETE` on it
  answers `400` (remove its accounts instead).
- Removing the only Claude account that magpie's own Claude Code copy is
  signed in to answers `409 conflict`: magpie does not sign Claude Code
  out. Turn the subscription off (`PATCH .../providers/claude` with
  `"enabled": false`), or add another account first.

## 11. Connectors — implemented (Phase 4)

Providers: `github`, `gdrive`, `notion`, `slack`. Tokens are AES-256-GCM
encrypted in the host `connector_accounts` table and are never returned by
the API or written to the guest disk.

`Connector` object:

```json
{
  "provider": "github",
  "displayName": "GitHub",
  "oauthConfigured": true,
  "oauthSource": "env",
  "supportsOAuth": true,
  "supportsToken": true,
  "defaultScopes": ["repo", "read:org", "workflow"],
  "accounts": [
    { "accountLabel": "octocat", "authKind": "oauth2", "scopes": ["repo", "read:org", "workflow"],
      "expiresAt": "2026-11-10T14:00:00Z", "createdAt": "...", "updatedAt": "...", "status": "ok" }
  ]
}
```

`oauthSource` is `env` (`LYDIABOX_<PROVIDER>_CLIENT_ID`), `stored` (saved
through the client route below), `relay` (`LYDIABOX_OAUTH_RELAY_URL`), or
`none`. Account `status` is `ok`, `expired` (refresh failed), or `revoked`.

| Route | Request | Response |
| :--- | :--- | :--- |
| `GET /api/v1/connectors` | | `200 { "connectors": [Connector] }` |
| `GET /api/v1/connectors/{provider}` | | `200 Connector` |
| `PUT /api/v1/connectors/{provider}/client` | `{ "clientId": "...", "clientSecret": "..." }` | `200 Connector` (secret stored encrypted; env values win) |
| `POST /api/v1/connectors/{provider}/authorize` | `{ "scopes": [], "returnTo": "/connectors" }` (both optional) | `200 { "authorizeUrl": "https://github.com/login/oauth/authorize?...", "state": "..." }` |
| `GET /api/v1/connectors/{provider}/callback?code=&state=` | **public** | `302` to `returnTo` with `?connected=<provider>` or `?error=<code>&provider=<provider>` |
| `POST /api/v1/connectors/{provider}/token` | `{ "token": "ghp_...", "accountLabel": "", "scopes": [] }` | `201 Connector` (token validated against the provider; label defaults to the provider user name) |
| `DELETE /api/v1/connectors/{provider}?account=<label>` | | `204` (revokes upstream when the provider supports it) |
| `GET /api/v1/connectors/audit?provider=&before=<id>&limit=<n>` | | `200 { "entries": [AuditEntry], "nextBefore": 120 }` |

`authorize` uses Authorization Code with PKCE (S256). `state` is single-use,
bound to the session cookie, and expires after 10 minutes. The redirect URI
is `<origin>/api/v1/connectors/<provider>/callback`. `limit` defaults to
100 (maximum 1000); `nextBefore` is omitted on the last page.

Details:

- OAuth client precedence: `env`, then `stored`, then `relay`. A `PUT
  .../client` with an empty `clientId` and `clientSecret` removes the stored
  client. `authorize` answers `409 conflict` when `oauthSource` is `none`.
- Session binding: when `authorize` is cookie-authenticated, the callback
  must carry the same session cookie. A bearer-authenticated `authorize`
  creates a state without a binding. The PKCE verifier and the state stay in
  memory only; a restart invalidates pending authorizations.
- `returnTo` must be a same-origin path (`/...`, not `//...`); otherwise
  `400`. Callback `error` codes: `invalid_state`, `invalid_request`,
  `not_configured`, `exchange_failed`, `identity_failed`, `server_error`, or
  the provider's own code (for example `access_denied`).
- Default scopes: GitHub `repo read:org workflow`; Google Drive
  `drive.file drive.readonly` (with `access_type=offline`); Slack user
  scopes `channels:read groups:read chat:write search:read users:read`;
  Notion has no scopes.
- `token` validates the token against the provider identity endpoint
  (GitHub `/user`, Drive `about`, Notion `users/me`, Slack `auth.test`) and
  answers `400` when the provider rejects it. A pasted token has `authKind`
  `pat` and never expires.
- An expired `access_token` is refreshed with the stored `refresh_token`
  60 seconds before `expiresAt`; concurrent requests share one refresh. A
  failed refresh sets `status` to `expired`. A `401` from the provider during
  a tool call sets `status` to `revoked`. Connecting again resets `status`.
- `DELETE` without `account` removes the only account of the provider, and
  answers `400` when there are several. GitHub and Notion (OAuth client
  required), Google, and Slack tokens are revoked upstream; a GitHub or
  Notion token without an OAuth client is only deleted. The audit `detail`
  records `"revoked": true|false`.
- `audit` lists only `connector.*` kinds. `provider` filters on the
  `provider` field of `detail`.
- Relay mode (`LYDIABOX_OAUTH_RELAY_URL`): the authorize URL is
  `<relay>/v1/<provider>/authorize` with the same query (no `client_id`), and
  code and refresh grants are posted as forms to `<relay>/v1/<provider>/token`.
  The relay adds the client credentials and answers like the provider.

`AuditEntry`:

```json
{ "id": 121, "kind": "connector.token.issued", "detail": "{\"provider\":\"github\",\"account\":\"octocat\",\"consumer\":\"git-cred\",\"host\":\"github.com\"}",
  "createdAt": "2026-11-10T06:30:00Z" }
```

Audit kinds: `connector.connected`, `connector.disconnected`,
`connector.token.issued`, `connector.token.refreshed`,
`connector.token.refresh_failed`, `connector.mcp.call`. `detail` is a JSON
string and never contains token material.

The guest reaches the connector MCP server (`github_*`, `gdrive_*`,
`notion_*`, `slack_*` tools) and the `git-cred` broker over vsock port
9001. The `git-cred` broker is not a route on the public listener. The MCP
server is also on the public listener at `/mcp/connectors`, with API
authentication (section 5b).

### Guest broker (vsock port 9001)

The host broker (`internal/connector/broker`) serves plain HTTP/1.1 on the
guest's vsock port 9001 and, for the docker driver and for development, on
the `0600` UNIX socket `$LYDIABOX_DATA_DIR/run/host-9001.sock`. It has no
authentication: only the tenant's guest can reach these listeners. A remote
guest reaches it through the authenticated guest link instead. Inside the
guest, `LYDIABOX_HOST_ADDR` is `vsock://2:9001` or
`unix:///run/lydiabox/host-9001.sock` (docker, and the local bridge of the
guest link).

| Endpoint | Request | Response |
| :--- | :--- | :--- |
| `POST /git-cred` | `{ "protocol": "https", "host": "github.com", "path": "", "username": "" }` | `200 { "username": "x-access-token", "password": "<fresh token>" }`, `404` when no account serves the host or its refresh failed |
| `POST /mcp` | MCP JSON-RPC 2.0: `initialize`, `ping`, `tools/list`, `tools/call` | JSON-RPC response; notifications get `202` without a body |

`username`, when it names an account label, selects that account; otherwise
the first account with `status` `ok` is used. Every `/git-cred` answer writes
`connector.token.issued` (`consumer` `git-cred`, `host`), and every
`tools/call` writes `connector.mcp.call` (`tool`, `ok`, `error`).

Tools (each also takes an optional `account`):

| Tool | Arguments |
| :--- | :--- |
| `github_list_repos` | `visibility`, `per_page`, `page` |
| `github_list_issues` | `owner`, `repo`, `state`, `labels`, `per_page`, `page` |
| `github_create_issue` | `owner`, `repo`, `title`, `body`, `labels`, `assignees` |
| `github_list_pull_requests` | `owner`, `repo`, `state`, `per_page`, `page` |
| `github_create_pull_request` | `owner`, `repo`, `title`, `head`, `base`, `body`, `draft` |
| `github_get_file_contents` | `owner`, `repo`, `path`, `ref` |
| `gdrive_search` | `query`, `mime_type`, `page_size` |
| `gdrive_download_to_workspace` | `file_id`, `path` (relative to `/workspace`; a trailing `/` keeps the Drive name), `overwrite`, `export_mime_type` |
| `gdrive_upload_from_workspace` | `path`, `name`, `parent_id`, `file_id` (update), `mime_type` |
| `notion_search` | `query`, `filter` (`page` or `database`), `page_size` |
| `notion_read_page` | `page_id`, `max_blocks` |
| `notion_append_to_page` | `page_id`, `text` |
| `slack_list_channels` | `types`, `limit`, `cursor` |
| `slack_post_message` | `channel`, `text`, `thread_ts` |
| `slack_search_messages` | `query`, `count` |

Workspace paths of the Drive tools go through the File Manager sandbox
(`os.Root` on the tenant workspace): absolute paths, `..`, and symlinks that
leave `/workspace` are rejected.

## 12. Adding routes (for implementers)

1. Create `internal/api/routes_<subsystem>.go` with a type that implements
   `api.RouteRegistrar`:

   ```go
   type VMRoutes struct{ Controller *vmm.Controller }

   func (v VMRoutes) RegisterRoutes(r api.Router) {
       r.HandleFunc("GET /api/v1/vm/status", v.status)
       r.HandleFunc("POST /api/v1/vm/wake", v.wake)
   }
   ```

   Patterns use Go 1.22 `ServeMux` syntax. Routes require auth by default;
   pass `api.Public()` only for routes this document marks public.
   Registering a pattern twice panics at startup.
2. Build the subsystem in `internal/cli/wire_<subsystem>.go` (already
   present for `vm`, `harness`, `connector`, `desktop`) and return a
   `component` with its `Routes`, `Start`, and `Stop`. Do not edit
   `internal/cli/up.go` or another subsystem's files.
3. In handlers, read the tenant with `tenant.FromContext(r.Context())`, write
   responses with `httpx.WriteJSON` / `httpx.WriteError`, and decode bodies
   with `httpx.DecodeJSON`.
4. New tables go in `internal/store/migrations_<subsystem>.go` using the
   migration ID range of the phase (see `internal/store/migrations.go`), with
   tenant-scoped accessors in `internal/store/<subsystem>.go`.
5. Update this document and its status table in the same commit.
