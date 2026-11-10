# LydiaBox

LydiaBox (`lydiabox`) is a self-hostable personal agent VM and harness
orchestration platform. One static Go binary runs the control plane: an
S3-backed POSIX filesystem (JuiceFS), the agent VM (Cloud Hypervisor or a
Docker fallback), the magpie LLM gateway, connectors, and an embedded web
workstation. See [docs/design.md](docs/design.md) for the architecture and
[docs/api.md](docs/api.md) for the HTTP and WebSocket API.

## Status

| Phase | Scope | State |
| :--- | :--- | :--- |
| 1 | Config, encrypted store, runtime bootstrap, JuiceFS storage, File API, WebDAV | Done |
| 2 | Guest daemon, VMM drivers (`chv`, `docker`) | Planned |
| 3 | magpie gateway, harness session bridge | Planned |
| 4 | Connectors and credential broker | Done |
| 5 | Web UI | Planned (a placeholder page is embedded) |

## Quick start

Requirements: Linux (or macOS with macFUSE for development), FUSE, an S3,
R2, or MinIO bucket. `mkfs.ext4` is needed for the `chv` driver.

```sh
make build
cp .env.example .env   # fill the LYDIABOX_S3_* values
set -a; . ./.env; set +a
./dist/lydiabox up
```

On first boot, `lydiabox up` downloads `juicefs` and `magpie` (plus
`cloud-hypervisor` and `virtiofsd` when `/dev/kvm` is available) into
`~/.lydiabox/runtime/bin`, verifies their SHA256 checksums, formats or
restores the JuiceFS volume, and prints the generated admin token.

```sh
./dist/lydiabox status           # VM, mount, and S3 sync state
./dist/lydiabox runtime status   # runtime binaries and guest images
./dist/lydiabox version
```

Files are available while the VM is off: in the web UI, through the REST
API (`/api/v1/fs`), and over WebDAV at `http://<host>:8080/webdav/` (user
`lydiabox`, password = admin token).

## Connectors

GitHub, Google Drive, Notion, and Slack connect through OAuth (Authorization
Code with PKCE) or a pasted token. Set `LYDIABOX_<PROVIDER>_CLIENT_ID` and
`_CLIENT_SECRET`, save a client through `PUT /api/v1/connectors/<provider>/client`,
or set `LYDIABOX_OAUTH_RELAY_URL`. Register the callback URL
`http(s)://<host>/api/v1/connectors/<provider>/callback` with the provider.

```sh
curl -H "Authorization: Bearer $TOKEN" -d '{"token":"ghp_..."}' \
  http://localhost:8080/api/v1/connectors/github/token
```

Tokens are AES-256-GCM encrypted in the host state database and refresh
automatically. The guest never stores them: `git` inside the VM uses
`lydiabox-guest git-cred` (set it up with `lydiabox-guest git-cred install`),
which asks the host broker for a fresh token on each `git push` or `pull`.
The same broker serves the connector MCP server (`github_*`, `gdrive_*`,
`notion_*`, `slack_*`). The guest reaches it over vsock port 9001, or the
UNIX socket `$LYDIABOX_DATA_DIR/run/host-9001.sock`. Every token issue and
tool call is in the audit log (`GET /api/v1/connectors/audit`).

## Development

```sh
make build        # dist/lydiabox (host) + dist/lydiabox-guest (linux)
make build GOOS=linux GOARCH=arm64
make test         # CGO_ENABLED=0 tests, then the race detector
make lint         # go vet, gofmt, staticcheck, shellcheck
make web-build    # build web/ and embed it (Phase 5)
```

All binaries build with `CGO_ENABLED=0`. Runtime binary versions and
checksums are pinned in
[internal/bootstrap/manifest.json](internal/bootstrap/manifest.json).

## Layout

```text
cmd/lydiabox            host CLI and control plane (internal/cli)
cmd/lydiabox-guest      guest daemon entrypoint (internal/guest/cli)
internal/config         env + lydiabox.yaml loader, validation, secrets, data dir layout
internal/store          SQLite state DB, migrations, AES-256-GCM secret encryption
internal/bootstrap      pinned runtime binary downloader and guest image fetcher
internal/storage        JuiceFS supervisor, S3 metadata backup and restore, WebDAV proxy
internal/storage/files  sandboxed File Manager service and HTTP handlers
internal/connector      OAuth2 PKCE and token binding, refresh, revoke, audit
internal/connector/broker  guest broker: git credentials and connector MCP server
internal/guest/gitcred  guest git credential helper (vsock or UNIX socket)
internal/api            HTTP server, auth, route registration contract
internal/webui          embedded web UI (placeholder until Phase 5)
internal/httpx          JSON response and error conventions
internal/tenant         request tenant context
deploy/rootfs           guest image build script (Dockerfile in Phase 2)
```
