# Conformance report

This report compares the implementation on `main` with
[design.md](design.md), section by section. It was written in Phase 6
(2026-11-10) and updated after the gap-closure work and the literal-conformance
work of the same day.

Status values:

- **done**: implemented as specified.
- **partial**: implemented, but a part is missing or not confirmed.
- **deviates**: implemented differently from the text, on purpose. The
  reason is given.
- **missing**: not implemented.

"Verified by" names Go tests (`go test ./...`), web tests (`npm run test`),
the integration run at the end of this document (**E2E**), or `unverified`.

## Summary

| Section | done | partial | deviates | missing |
| :--- | ---: | ---: | ---: | ---: |
| 1. Vision and pillars | 15 | 0 | 0 | 0 |
| 2. Non-negotiable rules | 9 | 0 | 0 | 0 |
| 3. Repository structure | 16 | 0 | 0 | 0 |
| 4.1 Configuration and bootstrap | 8 | 0 | 0 | 0 |
| 4.2 Storage | 13 | 0 | 0 | 0 |
| 4.3 VM engine | 12 | 0 | 0 | 0 |
| 4.4 Magpie and session bridge | 14 | 0 | 0 | 0 |
| 4.5 Desktop and computer use | 11 | 0 | 0 | 0 |
| 4.6 Connectors | 9 | 0 | 0 | 0 |
| 5. Web UI | 6 | 0 | 0 | 0 |
| 6. Phase gates | 6 | 0 | 0 | 0 |

Open items (not `done`):

| Item | Status | Reason |
| :--- | :--- | :--- |
| Real KVM boot | unverified | This macOS host has no `/dev/kvm`. The Phase 2 gate asks for payload generation and a mock lifecycle, which the tests cover: `TestBuildConfigPayload` and `TestTenantNetwork` (the `vm.create` JSON, per-tenant network and kernel command line), `TestLifecycleAgainstMockCloudHypervisor` (two `virtiofsd` processes, a fake `cloud-hypervisor` API socket: `vm.create`, `vm.boot`, `vm.pause`, `vm.resume`, `vm.shutdown`, adoption after a restart), `TestVsockHandshake` (`CONNECT <port>` / `OK` and the `vsock.sock_<port>` listeners), and `TestNetworkRules` (nftables and iptables rules). The kernel, initramfs, and squashfs build and a real boot were not run. |

Closed in the gap-closure work: root persistence with the `docker` driver,
host state database restore, `virtiofsd` on `linux/arm64`, the Antigravity
seed session, the dsh and Antigravity CLIs in the image, the web page file
layout, and per-tenant `chv` subnets.

Closed in the literal-conformance work: the S3 gateway at `/s3` (SigV4
verify-and-re-sign proxy), the literal gateway base URLs
(`http://127.0.0.1:3425` for dsh and the PTY environment, with `/v1` added
by the gateway for OpenAI-style paths), the host route `/mcp/connectors`,
the Antigravity CLI settings (checked against agy 1.5.4), and a
reclassification against the literal design text: the magpie web API, the
single WebRTC UDP port, and the extra packages.

## 1. Vision and architectural pillars

| Requirement | Status | Where | Verified by |
| :--- | :--- | :--- | :--- |
| One static `CGO_ENABLED=0` binary, `./lydiabox up`, embedded web UI | done | `cmd/lydiabox`, `internal/cli`, `internal/webui` | `TestSPAHandler`; E2E (UI served by the release binary) |
| Runtime binaries bootstrapped into `$DATA_DIR/runtime/` and supervised | done | `internal/bootstrap`, `internal/storage`, `internal/magpie` | `TestEnsureInstallsRequiredBinaries`; E2E (juicefs and magpie downloaded for darwin/arm64 and linux/arm64) |
| JuiceFS with `sqlite3://` metadata and background snapshot sync | done | `internal/storage` | `TestSyncUploadsOnlyChanges`; E2E |
| File access while the VM is `STOPPED`: Web UI, WebDAV | done | `internal/storage/files`, `internal/storage/webdav.go` | `TestFileAPICRUD`; E2E |
| Virtual S3 gateway | done | `internal/storage/s3gateway.go`, `internal/storage/s3proxy`, `internal/api/routes_s3.go` | `TestS3GatewaySupervisor`, `TestS3RouteVerifiesAndResigns`, `TestRoundTrip`; E2E (`aws s3` through port 8080 at `/s3`, steps 43 to 52) |
| Persistent root (squashfs + `root-upper.img` overlay) | done (`chv`) | `deploy/initramfs/init`, `internal/vmm/chv` | `TestBuildConfigPayload`; boot unverified |
| Persistent root with the `docker` driver: stop keeps the container; recreated only when the image, settings, or guest binary change, or on `resetRoot` | done | `internal/vmm/docker` | `TestStopKeepsContainer`, `TestBootStartsExistingContainer`, `TestRecreateOnChangedSpec`; E2E (a file in `/etc` survives stop and wake) |
| Five harnesses | done | `internal/harness/adapters` | `TestDiscoverAndParse`, `TestHotSwitchPairs` |
| magpie on the host, bridged into the VM, keys only on the host | done | `internal/magpie`, `internal/guest/credproxy` | `TestGatewayRoundTripOverUnixSockets`; E2E (`/api/hello` from the guest) |
| Session discovery while the VM is off, usage, model rebinding, hot-switch | done | `internal/harness` | `TestListWhileVMStopped`, `TestHotSwitchPairs` |
| Connectors with own OAuth clients, PATs, optional relay | done | `internal/connector` | `TestClientSources`, `TestPKCEAuthorizeCallback`, `TestBindToken` |
| Tokens encrypted on the host; git helper and connector MCP over vsock | done | `internal/store`, `internal/connector/broker`, `internal/guest/gitcred` | `TestGitCredEndToEnd`; E2E (MCP bridge tool list) |
| Antigravity session format | done (schema-free protobuf decoding; format inferred, see [harness-formats.md](harness-formats.md)) | `internal/harness/adapters` | `TestDiscoverAndParse` |
| X11 desktop, Chrome (CDP), Blender in the guest | done | `deploy/rootfs/Dockerfile` | `TestLocalAppsStartsChromeForCDP`; full image not built here |
| Computer-use MCP, WebRTC streaming, Observer and Takeover modes | done | `internal/guest/*`, `internal/desktop`, `web/src/pages/desktop` | `TestTakeoverBlocksAgentAndInjectsUserInput`, `TestViewerReceivesVideoAndSendsInput`; E2E |

## 2. Non-negotiable engineering rules

| Requirement | Status | Where | Verified by |
| :--- | :--- | :--- | :--- |
| Both binaries build with `CGO_ENABLED=0` (no `import "C"`) | done | `Makefile` (`export CGO_ENABLED := 0`) | `make build`, `make release`; `file dist/lydiabox-guest` shows a static ELF |
| `modernc.org/sqlite`, `pion/webrtc/v4`, `jezek/xgb`, `creack/pty` | done | `go.mod` | build |
| `tenantID string` on `storage.Provider` | done | `internal/storage/supervisor.go` | `TestTenantScoping` |
| `tenantID string` on `vmm.Driver` | done | `internal/vmm/driver.go` | `TestDockerLifecycleAndSockets` |
| `tenantID string` on `harness.Manager` | done | `internal/harness/manager*.go` | `TestCreateDeleteAndLeases` |
| `tenantID` on the connector broker | done | `internal/connector` (every method), `broker.New` (one broker per tenant) | `TestGitCred` |
| `chv` and `docker` drivers, `LYDIABOX_VM_DRIVER` (`auto`, `chv`, `docker`) | done | `internal/vmm/chv`, `internal/vmm/docker`, `internal/config` | `TestResolveVMDriver`; E2E (`docker`) |
| `juicefs` and `magpie` run only on the host | done | `internal/storage`, `internal/magpie`; the guest image has neither | Dockerfile review |
| Path-traversal sandboxing with `os.Root` on File APIs | done | `internal/storage/files`, `internal/harness/safefile` | `TestTraversalRejected`, `TestSymlinkEscapes`; E2E (`..` gives `400`) |

## 3. Repository structure

| Path | Status | Notes |
| :--- | :--- | :--- |
| `Makefile` | done | build, release, test, lint, `rootfs`, `guest-image`, `guest-image-slim`, `virtiofsd` |
| `go.mod` (`github.com/lydiabox/lydiabox`) | done | |
| `.env.example` | done | every `LYDIABOX_*` variable the code reads |
| `cmd/lydiabox`, `cmd/lydiabox-guest` (`git-cred` subcommand) | done | |
| `internal/config`, `internal/bootstrap`, `internal/store` | done | |
| `internal/storage` (FS REST API in `internal/storage/files`) | done | |
| `internal/vmm` (`driver.go`, `chv/`, `docker/`) | done | |
| `internal/magpie` | done | wirers in `internal/magpie/wire` |
| `internal/harness` (`types.go`, `bridge.go`, `adapters/`) | done | |
| `internal/connector` | done | |
| `internal/desktop`, `internal/api` | done | |
| `internal/guest/{rpc,x11,webrtc,mcp,ptymanager,credproxy}` | done | plus `cli`, `gitcred` |
| `deploy/initramfs/init`, `deploy/rootfs/{Dockerfile,supervisord.conf,build-artifacts.sh}` | done | plus `lydiabox-init`, `setup-guest.sh`, `install-harnesses.sh`, `kernel.config` |
| `web/` (`package.json`, `vite.config.ts`, `src/App.tsx`, `src/api/`) | done | |
| `web/src/pages/{Studio,Desktop,Files,Models,Connectors}Page.tsx` | done | page entry modules that compose the components in the per-area folders (`studio/`, `desktop/`, `files/`, `models/`, `connectors/`) |
| Extra packages | done | The design says "create the following layout", and the tree contains it. The packages outside the listed layout (`internal/httpx`, `internal/tenant`, `internal/version`, `internal/webui`, `internal/vmm/guestclient`, `internal/magpie/wire`, `internal/harness/safefile`, `internal/connector/broker`, `internal/guest/cli`, `internal/guest/gitcred`, `internal/storage/s3proxy`) are additions that keep files under 600 lines; the design does not forbid them |

## 4.1 Configuration and single-binary startup

| Requirement | Status | Where | Verified by |
| :--- | :--- | :--- | :--- |
| All 15 variables of the table with their defaults | done | `internal/config` | `TestDefaults`, `TestEnvOverridesYAML` |
| Optional `lydiabox.yaml` | done | `internal/config/sources.go` | `TestEnvOverridesYAML` |
| Generated auth token printed on first boot | done | `internal/config/secrets.go`, `internal/cli/up.go` | `TestEnsureSecrets` |
| Generated master key, `0600`, `$DATA_DIR/state/master.key` | done | `internal/config/secrets.go` | `TestEnsureSecrets`; E2E log |
| Bootstrap checks `runtime/bin` and downloads missing binaries per `GOARCH` | done | `internal/bootstrap` | `TestEnsureInstallsRequiredBinaries`, `TestDockerDriverSkipsVMMBinaries`; E2E |
| Pinned URLs, SHA256, `+x`, `LYDIABOX_MIRROR_*` | done | `internal/bootstrap/manifest.json` | `TestChecksumMismatchKeepsExistingBinary`, `TestMirrorOverride` |
| Pinned checksums for every binary | done (`virtiofsd`: amd64 uses the upstream zip; arm64 has no upstream binary and is built locally from the `v1.14.0` tag, manifest `checksumSource: "build"`) | `internal/bootstrap/manifest.json`, `internal/bootstrap/local.go`, `deploy/virtiofsd` | `TestLocallyBuiltBinary`, `TestLocallyBuiltBinaryMirror`, `TestBuildEntryValidation`; build run on this host (static aarch64 ELF in 59 s) |
| `lydiabox runtime build-rootfs` (also builds and installs `virtiofsd` on arm64), `runtime install-local`, `LYDIABOX_ROOTFS_URL` | done | `internal/cli/runtime.go`, `deploy/rootfs/build-artifacts.sh` | `TestRuntimeStatusAndBuildRootfs`, `TestFetchImages`; E2E (slim image build) |

## 4.2 Persistent POSIX storage and disaster recovery

| Requirement | Status | Where | Verified by |
| :--- | :--- | :--- | :--- |
| Check `.lydiabox-system/tenants/$TENANT/jfs-meta.json.gz` with the AWS SDK when `jfs.db` is missing | done | `internal/storage/objectstore.go`, `supervisor.go` | `TestRestoreFromBackup`; E2E (restore on an empty data directory) |
| `juicefs load` restore | done | `internal/storage/juicefs.go` | `TestRestoreFromBackup`; E2E |
| `juicefs format ... --storage=s3 --compress=lz4` | done | `internal/storage/juicefs.go` | `TestJuiceFSCommands`; E2E |
| `juicefs mount ... --cache-size=10240 --writeback`, supervised | done | `internal/storage/supervisor.go` | `TestMountRestartsAfterCrash`; E2E |
| Metadata `dump` + `VACUUM INTO` every 60 s when changed and on `SIGTERM` | done | `internal/storage/metasync.go` | `TestSyncUploadsOnlyChanges`; E2E (`lastUploadAt`) |
| Host state restore: `lydiabox.db.gz` is restored before the store opens when `$DATA_DIR/state/lydiabox.db` is missing (same master key needed; warning when the key is new) | done | `internal/storage/staterestore.go`, `internal/cli/up.go` | `TestRestoreState`; E2E (disaster recovery, steps 39 to 41) |
| `home/agent`, `workspace`, `system` with uid/gid 1000 | done | `internal/storage/provision.go` | `TestFreshStartFormatsMountsAndProvisions`; E2E (guest `id` and file owner) |
| 40 GiB sparse `root-upper.img` + `mkfs.ext4 -F -q` | done | `internal/storage/provision.go` | `TestOptionalRootUpper`; E2E (`bytes=42949672960`) |
| REST File API: list, text read/save, download, upload, rename, delete | done | `internal/storage/files` | `TestFileAPICRUD`, `TestMutations`; E2E |
| Works identically with the VM `STOPPED` or `RUNNING` | done | | E2E (files written by the guest seen by the API, and the reverse) |
| `juicefs webdav` on a loopback port, `/webdav/` with Basic Auth | done | `internal/storage/webdav.go` | `TestWebDAVProxyRewrites`, `TestWebDAVSupervisor`; E2E |
| Virtual S3 gateway: supervised `juicefs gateway` on the loopback `LYDIABOX_S3_GATEWAY_ADDR`, bucket `lydiabox`, access key `lydiabox`, secret key = admin token (public route, SigV4 checked by the proxy) | done | `internal/storage/s3gateway.go`, `internal/storage/child.go`, `internal/api/routes_s3.go` | `TestGatewayCommand`, `TestS3GatewaySupervisor`, `TestS3GatewayDisabled`, `TestS3GatewayPathAndBucket`, `TestS3RouteDisabledGateway`, `TestHealthAndStatus`; E2E (steps 43 to 52) |
| S3 gateway at `/s3` on `LYDIABOX_LISTEN_ADDR`: the proxy verifies SigV4 (header and presigned URLs, 15-minute skew, expiry), removes `/s3`, keeps `Host` and the query, and re-signs for the backend with the same scope; signed chunk chains (`STREAMING-AWS4-HMAC-SHA256-PAYLOAD[-TRAILER]`) are verified and rewritten; S3 XML errors | done | `internal/storage/s3proxy`, `internal/api/routes_s3.go` | `TestVerifyHeaderAuth`, `TestVerifyRejections`, `TestVerifyPresigned`, `TestErrorDocument`, `TestRoundTrip`, `TestResignMatchesBackend`, `TestChunkSignatureAWSExample`, `TestChunkedChainRewrite`, `TestS3RouteVerifiesAndResigns`; E2E (steps 43 to 52) |

## 4.3 Serverless agent VM engine

| Requirement | Status | Where | Verified by |
| :--- | :--- | :--- | :--- |
| Two `virtiofsd` daemons with the given flags | done | `internal/vmm/chv/driver.go` | `TestLifecycleAgainstMockCloudHypervisor` |
| `cloud-hypervisor --api-socket`, `PUT vm.create` payload (CPUs, shared memory, payload, 3 disks, 2 fs, vsock, net, devices) | done | `internal/vmm/chv/payload.go` | `TestBuildConfigPayload` |
| Auto-built read-only `lydiabox-guest.ext4` | done | `internal/vmm/chv/driver.go` | `TestLifecycleAgainstMockCloudHypervisor` |
| TAP `172.30.0.2/24`, NAT, no access to host private networks | done | `internal/vmm/chv/network.go` | `TestNetworkRules` |
| Per-tenant subnet: `default` keeps `172.30.0.0/24`; other tenants get `172.30.<n>.0/24` from a stable index in the store (`vm_networks`), passed to the initramfs as `lydiabox.ip` / `lydiabox.gw` | done | `internal/vmm/chv/payload.go`, `internal/store/vmnet.go`, `internal/cli/wire_vm.go` | `TestTenantNetwork`, `TestTenantSubnet` |
| Optional VFIO GPU device | done | `internal/vmm/chv/payload.go` | `TestBuildConfigPayload` |
| `vm.boot`, `vm.pause`, `vm.resume`, `vm.shutdown` | done | `internal/vmm/chv/client.go` | `TestLifecycleAgainstMockCloudHypervisor` |
| vsock `CONNECT <port>` / `OK`, and `vsock.sock_<port>` listeners | done | `internal/vmm/chv/vsock.go` | `TestVsockHandshake` |
| Initramfs: squashfs, ext4 upper, overlay, virtiofs, guest bind mount, `switch_root` | done | `deploy/initramfs/init` | shellcheck; boot unverified |
| `EnsureRunning` singleflight wake from terminal, session, desktop, MCP | done | `internal/vmm/controller.go` | `TestEnsureRunningIsSingleflight`; E2E (`wake`, session start) |
| Leases (PTY, desktop), idle timeout, `sync`, pause | done | `internal/vmm/controller.go` | `TestIdleTimerHibernatesWithoutLeases`; E2E (idle pause) |
| `docker` driver: same image and protocol over a bind-mounted UNIX socket, persistent root | done | `internal/vmm/docker` | `TestDockerLifecycleAndSockets`, `TestStopKeepsContainer`; E2E (steps 35 to 38) |

## 4.4 Magpie integration and session bridge

| Requirement | Status | Where | Verified by |
| :--- | :--- | :--- | :--- |
| magpie on `127.0.0.1:3425`, data in `$DATA_DIR/state/magpie` | done | `internal/magpie/supervisor.go` | `TestSupervisorLifecycleAndGatewayKey`; E2E (`/api/v1/models/gateway`) |
| Providers and routing managed "via Magpie API" (design sections 4.4 and 5) | done (LydiaBox drives the web API of `magpie serve --web=127.0.0.1:3426`, loopback only, random key. This API is not a versioned public interface, so a new magpie release can need changes in `internal/magpie/admin_http.go`; the pinned version in `internal/bootstrap/manifest.json` keeps them in step) | `internal/magpie/admin_http.go` | `TestHTTPAdmin` |
| Named gateway key for the VM, injected on the host side of the tunnel | done | `internal/magpie/gatewaykey.go`, `internal/guest/credproxy` | `TestGatewayKeyReplacesForeignKey`; E2E |
| Guest `127.0.0.1:3425` forwards Chat, Responses, Messages, Gemini; OpenAI and DeepSeek paths without `/v1` (`/chat/completions`, `/completions`, `/responses`, `/embeddings`, `/models`, `/models/...`) are forwarded as `/v1/...` (host side of the tunnel, where the listener's requests are parsed) | done | `internal/guest/credproxy` | `TestGatewayRoundTripOverUnixSockets`, `TestNormalizeOpenAIPath`, `TestGatewayAddsV1ForOpenAIPaths`; E2E (`/api/hello`) |
| claude-code wirer (`settings.json` env and model, MCP in `.claude.json`) | done | `internal/magpie/wire` | `TestWirersPreserveUserContent` |
| codex wirer (`base_url = .../v1`, model, `[mcp_servers.lydiabox]`) | done | `internal/magpie/wire` | `TestWirersPreserveUserContent` |
| dsh wirer (`OPENAI_BASE_URL`, `ANTHROPIC_BASE_URL`, `DEEPSEEK_BASE_URL` = `http://127.0.0.1:3425`); PTY `OPENAI_BASE_URL=http://127.0.0.1:3425` (`ptymanager.DefaultEnv`) | done | `internal/magpie/wire/others.go`, `internal/guest/ptymanager` | `TestEnvEdit`, `TestWirersPreserveUserContent`, `TestMultiAttachReplayAndInput` |
| antigravity wirer: `~/.antigravity/settings.json` (design path: endpoint, model), `~/.gemini/antigravity/mcp_config.json`, plus the CLI's own `~/.gemini/antigravity-cli/settings.json` (`modelProvider: "gemini"`, `customModelsConfig.customModels.<model>.modelName = "gemini-api://<model>"`), `~/.gemini/config/mcp_config.json`, and `GOOGLE_GEMINI_BASE_URL` / `GEMINI_API_KEY` | done (CLI keys from the published schema and docs, and checked with agy 1.5.4: `agy --model <m>` posted `/v1beta/models/<m>:streamGenerateContent` to a capture server on `127.0.0.1:3425`, and `agy mcp list` showed both servers; see [harness-formats.md](harness-formats.md)) | `internal/magpie/wire/others.go` | `TestWirersCreateMissingFiles`, `TestWirersPreserveUserContent` |
| opencode wirer (`/v1`, model, `mcp`) | done | `internal/magpie/wire/others.go` | `TestWirersPreserveUserContent` |
| `HarnessAdapter` interface as specified | done | `internal/harness/types.go` | build |
| Hot-switch: SIGINT, 3 s, SIGTERM | done | `internal/harness/bridge.go` | `TestHotSwitchPairs` |
| Transcript extraction (goal, files, last 30 actions, last 10 messages, issues) and git state | done | `internal/harness/adapters`, `internal/harness/git.go` | `TestDiscoverAndParse`, `TestExecGitSandbox` |
| `.agent/HANDOFF.md`, `.agent/state.json`, managed blocks, model binding | done | `internal/harness/handoff.go` | `TestUpsertManagedBlock`, `TestSwitchTwiceIsIdempotent` |
| Native seed session and new PTY in the same terminal room | done (Antigravity uses the continuation invocation that the design allows, because its native format is undocumented) | `internal/harness/adapters` | `TestSynthesizeHandoffSeeds`, `TestSessionsWebSocketRoom` |

## 4.5 Computer use, desktop streaming, and takeover

| Requirement | Status | Where | Verified by |
| :--- | :--- | :--- | :--- |
| Ubuntu 24.04 desktop image with the listed packages | done | `deploy/rootfs/Dockerfile` (`desktop`) | not built here (size); `desktop-slim` built and run |
| Five harness CLIs in the image | done (dsh: npm `@deepseek-ai/dsh`, the package of `github.com/deepseek-ai/deepseek-harness`, Node.js 24; agy: Google's installer `https://antigravity.google/cli/install.sh`, SHA512-checked native binary) | `deploy/rootfs/install-harnesses.sh`, `deploy/rootfs/Dockerfile` | installers run in an `ubuntu:24.04` container with Node.js 24.22: `claude`, `codex`, `opencode`, `dsh` 0.2.2-rc.1, `agy` 1.5.4 on PATH; the full `desktop` image was not built here |
| `Xvfb :0 1920x1080x24` + Xfce as `agent` (uid 1000) | done | `deploy/rootfs/supervisord.conf` | E2E (`xdpyinfo`: 1920x1080) |
| `xgb` + `xtest`: capture, mouse, keys, text | done | `internal/guest/x11` | `TestAgentActionsInjectAndEmitEvents`; E2E (`computer_screenshot`) |
| Observer / Takeover, `agent_action` events, 30 s wait or takeover message | done | `internal/guest/x11`, `internal/desktop` | `TestTakeoverBlocksAgentAndInjectsUserInput`; E2E (mode `PUT`) |
| `ffmpeg x11grab` with the given flags on first viewer, stopped at 0 viewers | done | `internal/guest/webrtc` | `TestFFmpegArgsMatchDesign` |
| RTP to `TrackLocalStaticRTP` (H.264), signaling over `/api/v1/desktop/ws` | done | `internal/guest/webrtc`, `internal/desktop` | `TestViewerReceivesVideoAndSendsInput`, `TestSignalingRelayAndBroadcast`; E2E (live video and takeover keyboard input in a browser) |
| WebRTC media transport | done (the design sets no port scheme; all media uses one UDP port, `LYDIABOX_WEBRTC_UDP_PORT`, default 50000, forwarded by the driver because the guest is behind NAT) | `internal/guest/webrtc`, drivers | `TestViewerReceivesVideoAndSendsInput`; E2E (step 19) |
| MCP on `127.0.0.1:8910/mcp` with the 9 tools | done | `internal/guest/mcp` | `TestProtocolHandshakeAndToolList`; E2E (tool list) |
| `browser_cdp_endpoint` auto-starts Chrome with the profile directory | done | `internal/guest/mcp` | `TestLocalAppsStartsChromeForCDP` |
| `blender_run_script` | done | `internal/guest/mcp` | `TestToolsDriveTheDesktop` (fake Blender); real Blender unverified |

## 4.6 Zero-trust connectors

| Requirement | Status | Where | Verified by |
| :--- | :--- | :--- | :--- |
| OAuth2 Authorization Code + PKCE (`authorize`, `callback`) | done | `internal/connector/oauth.go` | `TestPKCEAuthorizeCallback` |
| Token / PAT binding (`POST .../token`) | done | `internal/connector/token.go` | `TestBindToken` |
| GitHub (`repo`, `read:org`, `workflow`), Drive (`drive.file`, `drive.readonly`), Notion, Slack | done | `internal/connector/providers_builtin.go` | `TestAuthorizeValidation` |
| Encrypted `connector_accounts` | done | `internal/store/connectors.go` | `TestConnectorAccounts` |
| Automatic refresh | done | `internal/connector/token.go` | `TestRefreshOnExpirySingleFlight` |
| Upstream revoke on disconnect (Notion added in Phase 6) | done | `internal/connector/providers_builtin.go` | `TestDisconnectRevokes` |
| `credential.helper = lydiabox-guest git-cred`, token over port 9001 | done | `deploy/rootfs/setup-guest.sh`, `internal/guest/gitcred` | `TestGitCredEndToEnd`; E2E (helper runs in the guest; no account was bound, so no token was issued) |
| Connector MCP proxied to guest `127.0.0.1:8911/mcp` with `github_*`, `gdrive_*`, `notion_*`, `slack_*` | done | `internal/connector/broker`, `internal/guest/credproxy` | `TestMCPProtocol`, `TestGitHubTools`, `TestGDriveTools`; E2E (tool list in the guest) |
| Host Connector MCP Server at `/mcp/connectors` on the main listener (authenticated; `POST` JSON-RPC, `GET` 405), the same broker handler that the guest reaches at `127.0.0.1:8911/mcp` | done | `internal/api/routes_connectors.go`, `internal/cli/wire_connector.go` | `TestHostConnectorMCPRoute` |

## 5. Web UI

| Requirement | Status | Where | Verified by |
| :--- | :--- | :--- | :--- |
| Status bar: VM pill, Wake / Hibernate / Stop, CPU/RAM, "Synced Xs ago" | done | `web/src/components` | web tests; E2E (browser) |
| Studio: session list with harness and workspace filters, xterm.js, Switch modal with HANDOFF preview | done | `web/src/pages/studio` | web tests; E2E (browser) |
| Desktop: WebRTC video, overlay, Observer/Takeover, Chrome/Blender launch | done | `web/src/pages/desktop` | web tests; E2E (browser, signaling) |
| Files: tree, editor, drag-and-drop upload, download, WebDAV dialog | done | `web/src/pages/files` | web tests; E2E (browser) |
| Models: providers, routing groups, per-harness bindings | done | `web/src/pages/models` | web tests |
| Connectors: cards, OAuth / client / token drawers, audit log | done | `web/src/pages/connectors` | web tests |

## 6. Phase verification gates

| Gate | Status | Verified by |
| :--- | :--- | :--- |
| Phase 1: traversal rejection, AES-256-GCM, File API CRUD without a VM | done | `TestTraversalRejected`, `TestCipherRoundTrip`, `TestFileAPICRUD`; E2E |
| Phase 2: `linux/amd64` and `linux/arm64` builds; `chv` payload and mock lifecycle; RPC PTY + MCP over UNIX sockets | done | `make release`; `TestBuildConfigPayload`, `TestLifecycleAgainstMockCloudHypervisor`, `TestVsockHandshake`, `TestPTYRoundTripOverUnixSocket` |
| Phase 3: realistic logs for 5 harnesses, wiring, hot-switch between pairs | done | `TestDiscoverAndParse`, `TestHotSwitchPairs` |
| Phase 4: `git-cred get` over simulated vsock without secrets on guest disk | done | `TestGitCredEndToEnd` |
| Phase 5: web UI built and embedded | done | `make build`; E2E |
| Phase 6: `.env.example`, `README.md`, `go test -race`, `make build` static binaries | done | final validation below |

## Integration run

Date: 2026-11-10. Host: macOS 26 on Apple silicon (arm64), Docker Desktop
28.0.4 (4 GiB, 12 CPUs), no `/dev/kvm`.

### Setup

The macOS host has no macFUSE, so `juicefs mount` cannot run natively. A
native run of `./dist/lydiabox up` (driver `docker`) downloaded
`juicefs` and `magpie` for darwin/arm64, formatted the volume in MinIO, and
then stopped with `juicefs mount exited before ready` ("no FUSE mount
utility found"). Phase 6 adds a hint to that error. Also, Docker Desktop
cannot share UNIX sockets between macOS and containers, which the `docker`
driver needs. The run therefore used a privileged Docker-in-Docker
container as the Linux host: it has FUSE and its own Docker engine, and it
runs the `linux/arm64` release binary.

```sh
docker run -d --name lydiabox-e2e-minio -p 127.0.0.1:19000:9000 \
  -e MINIO_ROOT_USER=... -e MINIO_ROOT_PASSWORD=... minio server /data
make release                        # web-build + dist/linux-{amd64,arm64}
make guest-image-slim               # deploy/rootfs/Dockerfile target desktop-slim
docker run -d --privileged --name lydiabox-e2e-dind --network lydiabox-e2e \
  -p 127.0.0.1:18080:18080 -p 127.0.0.1:50000:50000/udp \
  -v "$PWD/dist:/opt/lydiabox:ro" -e DOCKER_TLS_CERTDIR= docker:28-dind
docker exec lydiabox-e2e-dind apk add --no-cache fuse fuse3 e2fsprogs
docker save lydiabox/guest:slim | docker exec -i lydiabox-e2e-dind docker load
# in the container:
LYDIABOX_DATA_DIR=/data LYDIABOX_LISTEN_ADDR=0.0.0.0:18080 \
LYDIABOX_S3_ENDPOINT=http://lydiabox-e2e-minio:9000 LYDIABOX_S3_BUCKET=lydiabox-dind \
LYDIABOX_S3_REGION=us-east-1 LYDIABOX_S3_ACCESS_KEY=... LYDIABOX_S3_SECRET_KEY=... \
LYDIABOX_VM_DRIVER=docker LYDIABOX_DOCKER_IMAGE=lydiabox/guest:slim \
LYDIABOX_AUTH_TOKEN=... /opt/lydiabox/linux-arm64/lydiabox up
```

The checks used `curl`, a small WebSocket client (`coder/websocket`,
subprotocol `lydiabox.pty.v1`), and the browser pane of the Claude desktop
app.

### Results

| # | Step | Result |
| :--- | :--- | :--- |
| 1 | First start: bootstrap downloads `juicefs` 1.4.2 and `magpie` 0.1.3531 (linux/arm64, SHA256 checked), formats `lydiabox-default`, mounts, creates the 40 GiB sparse `root-upper.img`, starts WebDAV, magpie, and the broker | pass |
| 2 | `GET /api/v1/health` 200; `GET /api/v1/status` without credentials 401; login with a wrong token 401; login 200 with cookie; `/auth/session` and `/status` with the cookie 200 (`vm.state` `STOPPED`, `storage.mounted` true) | pass |
| 3 | File API: `roots`, `mkdir` 201, `PUT text` 200, `GET text` 200, `list` 200, multipart upload of a folder (2 files, 3 MiB) 201, same upload again 409, download byte-identical, `Range` 206, `rename` 200, `..` path 400, delete 204, non-empty directory 409, recursive delete 204 | pass |
| 4 | WebDAV: `PROPFIND` without credentials 401; `PROPFIND Depth: 1` 207 with `/webdav/workspace/e2e/...` hrefs; `PUT` 201 and `GET` 200; the file is visible through the File API | pass |
| 5 | `GET /api/v1/sessions` (empty), `/models/gateway` (`running`, magpie 0.1.3531), `/models/bindings` (5 harnesses), `/models/providers`, `/connectors` (4 providers), `/connectors/audit` | pass |
| 6 | VM stopped: `/vm/status` `STOPPED`; `GET` and `PUT /desktop/mode` 503 `unavailable`; `hibernate` 409 | pass |
| 7 | Web UI: `/` and `/studio` serve the SPA; login screen and Studio, Files pages render in the browser | pass |
| 8 | `POST /vm/wake`: container started, `RUNNING` in 2.1 s | pass |
| 9 | `POST /sessions` `{"harness":"shell"}` 201; WebSocket `hello`, input `echo E2E-$((6*7)); id; pwd; ls; cat dav.txt` gives `E2E-42`, `uid=1000(agent)`, `/workspace/e2e`, the WebDAV file content | pass |
| 10 | In the guest: `127.0.0.1:3425/api/hello` answers through the tunnel (`{"name":"magpie"}`); MCP `127.0.0.1:8910` lists the 9 computer-use tools; `computer_screenshot` returns a JPEG; the connector bridge `127.0.0.1:8911` lists 15 tools; `Xvfb :0` at 1920x1080; `git credential fill` runs `lydiabox-guest git-cred` (no account, no token) | pass |
| 11 | A file written in the guest is read through the File API | pass |
| 12 | `/vm/status` shows `leases.pty = 1` while the shell runs | pass |
| 13 | `GET /desktop/mode` `OBSERVER`; `PUT TAKEOVER` 200; `PUT BOGUS` 400; `PUT OBSERVER` 200 | pass (after fix 2) |
| 14 | `POST /vm/hibernate` 200 `PAUSED` (container paused); again 409; `wake` resumes; the session stays live | pass |
| 15 | `POST /vm/stop` 200 in 1.4 s; container removed; session `exited`; `stop` again 409 | pass |
| 16 | Metadata backup: `jfs-meta.json.gz` and `lydiabox.db.gz` uploaded after 60 s and on `SIGTERM`; graceful shutdown unmounts JuiceFS | pass |
| 17 | Disaster recovery: a new container with an empty data directory and the same bucket restored the volume from `jfs-meta.json.gz`; all files from steps 3, 4, and 11 were present | pass |
| 18 | `POST /sessions` with the VM `STOPPED` wakes it (1.8 s) | pass |
| 19 | Desktop page in the browser: "Start desktop" wakes the VM, WebRTC video plays over UDP 50000 (H.264, `ffmpeg x11grab`); "Terminal" opens `xfce4-terminal`; in Takeover mode, clicks and key presses reach the guest (`echo ok` ran); Observer hands control back | pass (after fix 4) |
| 20 | Idle timeout (`LYDIABOX_IDLE_TIMEOUT=45s`): with no PTY and no viewer, the VM paused 45 s after the last lease ended | pass |
| 21 | `POST /sessions` while `PAUSED` resumes the VM; the Studio terminal in the browser runs `uname -m` (`aarch64`) | pass (after fix 4) |
| 22 | Shutdown: `vm/stop`, `SIGTERM` (final snapshot, unmount), containers and network removed | pass |

Virtual S3 gateway (added after the first run; same dind host with
`LYDIABOX_S3_GATEWAY_ADDR=0.0.0.0:8090`, published as `127.0.0.1:18090`,
AWS CLI 2 on macOS):

| # | Step | Result |
| :--- | :--- | :--- |
| 23 | Startup logs `juicefs gateway ready` on `0.0.0.0:8090`; `GET /api/v1/status` gives `"s3Gateway":{"enabled":true,"addr":"0.0.0.0:8090","bucket":"lydiabox","running":true}` | pass |
| 24 | `aws --endpoint-url http://127.0.0.1:18090 s3 ls` lists the bucket `lydiabox`; `s3 ls s3://lydiabox/workspace/s3check/` lists a file written through the File API; `s3 cp` downloads its content | pass |
| 25 | `s3 cp` upload; the File API reads the uploaded object; `s3 rm` deletes it and the File API gives 404 | pass |
| 26 | A wrong secret key gives `SignatureDoesNotMatch` | pass |
| 27 | Files page, "Mount locally": WebDAV and S3 sections (endpoint, bucket, access key, secret key note, AWS CLI command) | pass |
| 28 | `SIGTERM`: the gateway stops with the other processes | pass |

Steps 23 to 28 used the earlier layout (gateway on its own port). The
gap-closure run below replaces them, and the `/s3` run replaces the S3
steps 29 to 34 of the gap-closure run.

### Gap-closure run

Same host and setup, recreated from the description above: network
`lydiabox-e2e`, MinIO (`ghcr.io/coollabsio/minio`) as
`lydiabox-e2e-minio`, a privileged `docker:28-dind` container with `fuse`,
`fuse3`, and `e2fsprogs`, the `lydiabox/guest:slim` image rebuilt with
`make guest-image-slim` and loaded into it, and the `linux/arm64` binary from
`make release`. The settings are those of the first run plus
`LYDIABOX_META_SYNC_INTERVAL=10s`; the S3 gateway backend stays on the
default `127.0.0.1:8090` inside the container, and only port 18080 is
published. The AWS CLI 2.36 on macOS used `--endpoint-url
http://127.0.0.1:18080`, access key `lydiabox`, and the admin token as the
secret key.

| # | Step | Result |
| :--- | :--- | :--- |
| 29 | First start with the bucket `s3`: `juicefs gateway` stopped with `invalid bucket name "s3": Bucket name cannot be shorter than 3 characters`, and `lydiabox up` exited. The bucket is now `lydiabox` at `/lydiabox` (open items) | finding, fixed |
| 30 | Start: `GET /api/v1/status` gives `"s3Gateway":{"enabled":true,"addr":"127.0.0.1:8090","path":"/lydiabox","bucket":"lydiabox","running":true}` | pass |
| 31 | A file written with the File API (`PUT /fs/workspace/text`): `aws s3 ls s3://lydiabox/workspace/s3check/` lists it; `aws s3 cp s3://lydiabox/workspace/s3check/api.txt -` prints its content | pass |
| 32 | `aws s3 cp` upload of a small file; the File API reads it. A 20 MiB random file (multipart upload) uploaded and downloaded again is byte-identical (`cmp`) | pass |
| 33 | `aws s3 rm`; the File API then gives 404 | pass |
| 34 | A wrong secret key gives `SignatureDoesNotMatch`; unsigned `GET /lydiabox?list-type=2` and `PUT /lydiabox/...` without credentials give 403 from the gateway | pass |
| 35 | `POST /vm/wake`: the container is created with the label `lydiabox.spec=<hash>`. As root in the guest, `/etc/lydiabox-persist.txt` is written; `/tmp/should-vanish` is created | pass |
| 36 | `POST /vm/stop`: the container is `Exited (0)` and kept (same ID). `POST /vm/wake`: the same container starts (log "reusing the guest container"); `/etc/lydiabox-persist.txt` has the same content; `/tmp` is a new tmpfs without `should-vanish`; `Xvfb` runs; `GET /desktop/mode` 200; `POST /sessions` (`shell`) 201 | pass |
| 37 | `POST /vm/stop` with `{"force":true,"resetRoot":true}`: the container is removed; the next wake creates a new one without `/etc/lydiabox-persist.txt` | pass |
| 38 | After the restore in step 39, with `LYDIABOX_VM_CPUS=2`: wake logs "the image, VM settings, or guest binary changed; recreating the guest container" and the VM has 2 CPUs | pass |
| 39 | Disaster recovery: `vm/stop`, wait for the 10 s backup, `SIGTERM` (final snapshots of `jfs-meta.json.gz` and `lydiabox.db.gz`). `/data` moved away; a new `/data` holds only `state/master.key` | pass |
| 40 | New start: bootstrap downloads `juicefs` and `magpie` again; the log shows "restored the host state database from S3; stored connector tokens decrypt only with the master key that wrote them" (`lydiabox.db.gz`, 81920 bytes) and "restoring JuiceFS metadata from backup" | pass |
| 41 | After the restore: `GET /sessions` lists the shell session of step 36 (`exited`); `s3check/` with `api.txt` and `big.bin` is present; magpie starts with the restored gateway secrets (`/models/gateway` `running`) | pass |
| 42 | Shutdown: `vm/stop`, `SIGTERM` ("shutdown complete"); the dind and MinIO containers and the network removed | pass |

### `/s3` gateway run

Same kind of host, recreated for this check: network `lydiabox-s3v`, MinIO
(`ghcr.io/coollabsio/minio`) as `lydiabox-s3v-minio`, a privileged
`docker:28-dind` container with `fuse`, `fuse3`, and `e2fsprogs`, and the
`linux/arm64` binary from `make release`, with the settings of the first run
(bucket `lydiabox-s3v`). The VM was not started (the S3 checks do not need
it). Only port 18080 was published (as `127.0.0.1:28080`). The AWS CLI
2.36.34 on macOS used `--endpoint-url http://127.0.0.1:28080/s3`, access key
`lydiabox`, and the admin token as the secret key.

| # | Step | Result |
| :--- | :--- | :--- |
| 43 | Start: the log prints `S3 gateway: endpoint http://[::]:18080/s3`; `GET /api/v1/status` gives `"s3Gateway":{"enabled":true,"addr":"127.0.0.1:8090","path":"/s3","bucket":"lydiabox","running":true}` | pass |
| 44 | `aws s3 ls` (ListBuckets, `GET /s3/`) lists `lydiabox`; `aws s3api list-buckets` gives `lydiabox` | pass |
| 45 | A file written with the File API (`PUT /api/v1/fs/workspace/text?path=s3check/api.txt`): `aws s3 ls s3://lydiabox/workspace/s3check/` lists it; `aws s3 cp s3://lydiabox/workspace/s3check/api.txt -` prints its content | pass |
| 46 | `aws s3 cp` up and down of `a b+c!.txt` (a key that needs escaping): byte-identical (`cmp`); the File API reads the same content | pass |
| 47 | A 20 MiB random file: multipart upload (3 `UploadPart` requests, SHA-256 payload hashes) and download are byte-identical (`cmp`); `head-object` gives 20971520 bytes | pass |
| 48 | `aws s3 rm`; the File API then gives 404 | pass |
| 49 | A wrong secret key gives `SignatureDoesNotMatch` (403, `application/xml`); an unsigned `GET /s3/lydiabox?list-type=2` gives 403 `AccessDenied` from the proxy | pass |
| 50 | `aws s3 presign` (300 s): `curl` downloads the object (200). The same URL with another key gives `SignatureDoesNotMatch`; a URL presigned for 1 s and used after 3 s gives `AccessDenied` "Request has expired" | pass |
| 51 | `STREAMING-AWS4-HMAC-SHA256-PAYLOAD` upload of 3 MiB in four chunks, signed with the aws-sdk-go-v2 signer and stream signer (`.scratch` program): 200, and the download is byte-identical; the same body with one changed byte gives 403 `SignatureDoesNotMatch` from the proxy. The `-TRAILER` variant is refused by the backend with `XAmzContentSHA256Mismatch` also when sent directly to `juicefs gateway` on `127.0.0.1:8090` without the proxy, so `juicefs gateway` 1.4.2 does not accept signed trailers; the proxy's trailer rewrite is covered by `TestChunkedChainRewrite` | pass (signed trailer: backend limitation) |
| 52 | `/lydiabox/...` is no longer an S3 route (the web UI answers). `SIGTERM`: the gateway stops with the other processes ("shutdown complete"); the containers and the network removed | pass |

`rclone` is not installed on this host and was not run; its configuration
(`endpoint = http://<host>:8080/s3`, `force_path_style = true`) uses the same
path-style requests as the AWS CLI.

Other checks on this host:

- `make virtiofsd` path (`deploy/virtiofsd/build.sh`): virtiofsd `v1.14.0`
  built in 59 s; `file` reports a static, stripped aarch64 ELF; `virtiofsd
  --version` prints `virtiofsd 1.14.0` in the build stage.
- Harness installers (`deploy/rootfs/install-harnesses.sh` with the default
  build arguments) in `ubuntu:24.04` with Node.js 24.22.2: `claude`,
  `codex`, `opencode`, `dsh` (0.2.2-rc.1), and `agy` (1.5.4) are on PATH.
  Research for the two installers: the npm registry entry of
  `@deepseek-ai/dsh` names the repository `deepseek-ai/deepseek-harness`
  and a maintainer address at `deepseek.com`; `https://antigravity.google/cli/install.sh`
  is served by Google's Antigravity site, downloads the native build from
  Google's release service, and checks its SHA512 before it installs `agy`.

Not run on this host: the `chv` driver (no KVM), the full `desktop` image
(Chrome, Blender, and harness CLIs; too large for this Docker VM), a hot
switch between real harness CLIs, and OAuth against real providers.

### Bugs found and fixed

1. The slim image had no D-Bus system configuration (`dbus-daemon` without
   the `dbus` package), so supervisord gave up on `dbus`. Both image stages
   now install `dbus`.
2. Desktop mode errors repeated the code in the message
   (`"bad_request: mode must be ..."`). The relay now returns the guest
   message only.
3. Notion tokens were not revoked on disconnect. Disconnect now calls
   `POST https://api.notion.com/v1/oauth/revoke` when an OAuth client is
   configured (`TestDisconnectRevokes`).
4. Web UI: the terminal and desktop WebSockets never connected in a real
   browser ("Reconnecting…" forever). `ReconnectingSocket` spread
   `{factory: undefined}` from its wrappers over the default factory, so
   every attempt threw. Tests always injected a fake factory. Defaults now
   apply to undefined options, with a regression test.
5. `go build ./...`, `go vet ./...`, and `go test ./...` descended into
   `web/node_modules` (an npm package ships Go code). `web/go.mod` now
   excludes it.
6. A failed `juicefs mount` on a host without FUSE gave no hint. The error
   now names macFUSE (macOS) or `/dev/fuse` (Linux).
7. `TestRoomReattachAfterRestart` failed once under `-race`: two managers
   watch the same fake PTY, and the store can show the exit before the
   second manager drops its room. The test now waits for the conflict.

Not fixed: in Takeover mode, text inserted without key events (IME commit,
paste) does not reach the guest; typed keys do.

## Final validation

Run again after the literal-conformance work, from a clean state (`make clean`,
`go clean -testcache`, fresh `npm ci`) on macOS arm64 with Go 1.24.1 and
Node.js 24:

| Command | Result |
| :--- | :--- |
| `CGO_ENABLED=0 go build ./...` | pass |
| `CGO_ENABLED=0 go vet ./...` | pass, no findings |
| `CGO_ENABLED=0 go test -race -count=1 ./...` | pass, 30 packages (new: `internal/storage/s3proxy`) |
| `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./...` and `arm64` | pass |
| `make lint` (vet, gofmt, staticcheck, shellcheck) | pass, no findings |
| `make build` | pass; `dist/lydiabox-guest` is a static, stripped ELF |
| `make release` | pass; four static, stripped ELF binaries |
| `cd web && npm ci && npm run typecheck && npm run lint && npm run test && npm run build` | pass, 45 tests, no lint warnings |

No Go, TypeScript, script, HTML, or CSS source file has more than 600
lines (largest: `internal/vmm/controller.go`, 477 lines).
