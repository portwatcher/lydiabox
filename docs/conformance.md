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
the integration run at the end of this document (**E2E**), the `chv` run
on KVM ([Integration run (chv on KVM)](#integration-run-chv-on-kvm),
**KVM run**), or `unverified`.

## Summary

| Section | done | partial | deviates | missing |
| :--- | ---: | ---: | ---: | ---: |
| 1. Vision and pillars | 15 | 0 | 0 | 0 |
| 2. Non-negotiable rules | 9 | 0 | 0 | 0 |
| 3. Repository structure | 16 | 0 | 0 | 0 |
| 4.1 Configuration and bootstrap | 8 | 0 | 0 | 0 |
| 4.2 Storage | 13 | 0 | 0 | 0 |
| 4.3 VM engine | 12 | 0 | 0 | 0 |
| 4.4 Magpie and harness sessions | 11 | 0 | 0 | 0 |
| 4.5 Desktop and computer use | 11 | 0 | 0 | 0 |
| 4.6 Connectors | 9 | 0 | 0 | 0 |
| 5. Web UI | 6 | 0 | 0 | 0 |
| 6. Phase gates | 6 | 0 | 0 | 0 |

Open items (not `done`): none. The last one, the real KVM boot, passed:
see [Integration run (chv on KVM)](#integration-run-chv-on-kvm) and
[kvm-test.md](kvm-test.md). The full `desktop` image (Chrome, Blender, the
harness CLIs) and WebRTC video and Takeover from a browser over the
forwarded UDP port were then checked on chv (the Chrome CDP check was
repeated on an idle host on 2026-11-11 and passed): see
[Integration run (chv on KVM), full desktop image](#integration-run-chv-on-kvm-full-desktop-image).
On 2026-11-11 the same checks (all 16 API checks, F1 to F9, and WebRTC)
passed on an amd64 KVM host (Google Cloud `n2-standard-8`, nested
virtualization, wake in 3.7 s) after two amd64 fixes: see
[Integration run (chv on KVM, amd64)](#integration-run-chv-on-kvm-amd64).
Not tested on real hardware, because no host had it: GPU passthrough
(checklist in [kvm-test.md](kvm-test.md#10-gpu-passthrough-vfio)). The
user decided to skip it. The GitHub connector passed every step of the
real-provider checklist (authorize, Cancel, expired state, grant, guest
`git-cred` and `git ls-remote`, connector MCP tools, Disconnect with
revoke at GitHub). On 2026-11-14 the user decided that this general flow
is sufficient and waived the real-provider check for Google Drive,
Notion, and Slack; these ran against fake providers only. See
[Connector run (GitHub, real provider)](#connector-run-github-real-provider).

Closed in the gap-closure work: root persistence with the `docker` driver,
host state database restore, `virtiofsd` on `linux/arm64`, the dsh and
Antigravity CLIs in the image, the web page file layout, and per-tenant
`chv` subnets.

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
| Persistent root (squashfs + `root-upper.img` overlay) | done (`chv`) | `deploy/initramfs/init`, `internal/vmm/chv` | `TestBuildConfigPayload`; KVM run (steps K11, K15) |
| Persistent root with the `docker` driver: stop keeps the container; recreated only when the image, settings, or guest binary change, or on `resetRoot` | done | `internal/vmm/docker` | `TestStopKeepsContainer`, `TestBootStartsExistingContainer`, `TestRecreateOnChangedSpec`; E2E (a file in `/etc` survives stop and wake) |
| Five harnesses | done | `internal/harness/adapters` | `TestDiscover`, `TestLaunchCommands` |
| magpie on the host, bridged into the VM, keys only on the host | done | `internal/magpie`, `internal/guest/credproxy` | `TestGatewayRoundTripOverUnixSockets`; E2E (`/api/hello` from the guest) |
| Session discovery while the VM is off, usage, model rebinding | done | `internal/harness`, `internal/api` | `TestListWhileVMStopped`, `TestModelsRoutes` |
| Connectors with own OAuth clients, PATs, optional relay | done | `internal/connector` | `TestClientSources`, `TestPKCEAuthorizeCallback`, `TestBindToken` |
| Tokens encrypted on the host; git helper and connector MCP over vsock | done | `internal/store`, `internal/connector/broker`, `internal/guest/gitcred` | `TestGitCredEndToEnd`; E2E (MCP bridge tool list) |
| Antigravity session format | done (schema-free protobuf decoding; format inferred, see [harness-formats.md](harness-formats.md)) | `internal/harness/adapters` | `TestDiscover` |
| X11 desktop, Chrome (CDP), Blender in the guest | done | `deploy/rootfs/Dockerfile` | `TestLocalAppsStartsChromeForCDP`; KVM full-image run (F5 to F7) |
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
| `chv` and `docker` drivers, `LYDIABOX_VM_DRIVER` (`auto`, `chv`, `docker`) | done | `internal/vmm/chv`, `internal/vmm/docker`, `internal/config` | `TestResolveVMDriver`; E2E (`docker`); KVM run (`chv`) |
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
| `internal/harness` (`types.go`, `adapters/`) | done | |
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
| Remote access through Cloudflare Zero Trust (addition): `lydiabox cloudflare setup|status|teardown`, supervised `cloudflared` (pinned, optional), Access JWT login, loopback listener with a tunnel, Cloudflare Realtime TURN key for the desktop stream (relay only unless a WebRTC setting overrides it) | done; the check with a real Cloudflare account is pending ([remote-access-manual-test.md](remote-access-manual-test.md)) | `internal/cloudflare`, `internal/api/access_auth.go`, `internal/cli/cloudflare.go`, `internal/cli/wire_remote.go`, `internal/config/remote.go` | `TestSetupCreatesEverything`, `TestSetupIsIdempotent`, `TestSetupFailures`, `TestTeardown` (fake API); `TestVerify`, `TestVerifyRejectsTampering`, `TestUnknownKeyRefreshesJWKS` (fake JWKS); `TestSupervisorConnectsAndStops`, `TestSupervisorRestartsWithBackoff` (fake cloudflared); `TestAccessLogin`, `TestAccessSameOriginCheck`, `TestAccessLimiterUsesConnectingIP`, `TestCloudflareSetupStatusTeardown`, `TestWireRemoteTunnel`, `TestSetupTURNKey`, `TestSetupTURNKeyFailure`, `TestWireRemoteStoredTURN`, `TestWireRemoteTURNPrecedence` |

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

### Provider storage mode (addition to the design)

`LYDIABOX_STORAGE_MODE=provider` keeps `/home/agent` and `/workspace` on the
machine disk (LydiaBox Cloud on InstaBox, [cloud-design.md](cloud-design.md)).
The `juicefs` mode above is unchanged.

| Requirement | Status | Where | Verified by |
| :--- | :--- | :--- | :--- |
| `lydiabox up` without S3 settings: no `juicefs` install or start, no WebDAV or `/s3` (both answer `501`), state backup only with S3 settings (one warning without them) | done | `internal/config/storage.go`, `internal/cli/storage_mode.go`, `internal/storage/statebackup.go`, `internal/api/routes_storage_mode.go` | `TestStorageMode`, `TestProviderModeWiring`, `TestStateBackupWithoutS3`, `TestStateBackupUploadsState` |
| One filesystem seam for host consumers (`agentfs.FS`): host mount with `os.Root` (`Local`) or guest RPC (`remotefs`) | done | `internal/agentfs`, `internal/agentfs/remotefs` | `TestTraversalRejected`, `TestTextReadWrite`, `TestMutations`, `TestFileAPICRUD`, `TestFileAPITraversal` (each in both modes) |
| Guest file methods (`fs.*`) in two configured roots with an `os.Root` sandbox; `..`, absolute paths, and escaping symlinks refused; atomic writes; streaming read and write | done | `internal/guest/guestfs`, `internal/guest/rpc/daemon/fs.go`, `internal/guest/rpc/fs.go` | `TestOperations`, `TestTraversalAndSymlinks`, `TestWriteKeepsModeAndRefusesNonRegular`, `TestLargeStreams` (24 MiB, Range reads), `TestWriteAbortAndLimits`, `TestTreeOperationsAndEscapes` |
| File operations run as the agent user when the guest daemon runs as root (per-thread `setfsuid`, `setfsgid`, groups) | done | `internal/guest/guestfs/identity_linux.go` | `TestRunsAsAgentUser` (Linux, root; run in a `golang:1.24` container) |
| A file request takes a `files` lease and wakes the machine; `503` after the wake timeout | done | `internal/agentfs/remotefs` | `TestFileAPIWakeTimeout`, `TestWakeTimeoutAndContext` |
| A file request does not start a machine that the user stopped (`503`, logged at info level) | done | `internal/agentfs/remotefs`, `internal/cli/wire_vm.go`, `internal/storage/files/http.go` | `TestStoppedMachineIsNotStarted` |
| Session discovery and purge in the guest; cached list in the state database while the machine is paused or stopped (`cached: true`) | done | `internal/harness/discovery_guest.go`, `internal/guest/rpc/daemon/harness.go`, `internal/store/discovery_cache.go` | `TestGuestDiscoveryMatchesLocal`, `TestGuestDiscoveryCacheWhileStopped`, `TestGuestPurge`, `TestDiscoveryCache` |
| Read-only session history: transcripts of all 5 harnesses as user, assistant, and tool entries (`GET /api/v1/sessions/{id}/transcript`), read on the host (`juicefs`) or in the guest with a wake (`provider`); newest entries kept under 3 MiB | done | `internal/harness/transcript.go`, `internal/harness/adapters/transcript.go`, `internal/guest/rpc/daemon/harness.go` | `TestTranscripts`, `TestTranscriptFit`, `TestGuestTranscript`, `TestSessionsHTTP` |
| ACP engine for all 5 harnesses: `lydiabox-guest acp` bridge (ACP client in a raw guest terminal, JSON lines to the host), full-access mode, MCP servers through `mcpServers`, `session/load` or `session/resume` plus transcript history, permission requests as approval cards (`POST …/permission`), engine switch with the same native session (`PATCH` `engine`), pinned `claude-agent-acp` 0.108.1 and `codex-acp` 2.7.0 in the image, run with the installed `claude` and `codex` (their bundled copies are pruned, so the image fits the InstaBox import disk) | done for Claude Code, Codex, opencode, and dsh: full turn and resume checked live in the guest image with a fake model gateway ([harness-formats.md](harness-formats.md#acp-agents)); Antigravity keeps the terminal engine (its ACP agent cannot use the gateway model); tool calls of a real agent not checked | `internal/acp`, `internal/guest/cli/cmd_acp.go`, `internal/harness/engine.go`, `internal/harness/events_acp.go`, `internal/harness/bridge_wire.go`, `deploy/rootfs/install-harnesses.sh`, `web/src/components/agent/PermissionCard.tsx` | `TestBridgeTurn`, `TestBridgeCancel`, `TestBridgeLoadAndResume`, `TestBridgeAgentExit`, `TestToolKindMapping`, `TestAgentFor`, `TestSessionsACP`, `permission.test.ts` |
| Structured session events of all 5 harnesses (messages, reasoning, tool calls with results, durations, diffs, screenshots, plan, usage, turn ends, errors) as a WebSocket stream with replay (`GET /api/v1/sessions/{id}/events`), parsed incrementally on the host (`juicefs`) or in the guest (`harness.events`, `provider`); agent state from the terminal (working, idle, approval prompt); prompt input with bracketed paste and interrupt | done | `internal/harness/adapters/events*.go`, `internal/harness/events*.go`, `internal/harness/room_activity.go`, `internal/harness/session_input.go`, `internal/api/routes_sessions_events.go`, `web/src/components/agent`, `web/src/pages/studio/ChatView.tsx` | `TestEventsPerHarness`, `TestEventsAppend`, `TestGuestEvents`, `TestGuestEventsOldGuest`, `TestApprovalPrompt`, `TestSessionsEventStream`, `TestSessionsEventStreamHistoryAndErrors`, `chatState.test.ts`, `components.test.ts`, `events.test.ts`; not run against real harness TUIs |
| Config wirers and connector file tools through the seam; only the placeholder gateway token is written | done | `internal/magpie/wire`, `internal/connector/broker` | `TestWirersProviderMatchesLocal` |
| Run on a hosted machine (InstaBox) | done against the fake API; not run on the real service | `internal/vmm/instabox`, `internal/instabox/instaboxtest/fakeguest` | `TestInstaboxCellEndToEnd` (files on the machine disk through the guest, wake on demand); real service: [instabox.md](instabox.md#first-check-with-a-real-account) steps 13 to 18 |

## 4.3 Serverless agent VM engine

| Requirement | Status | Where | Verified by |
| :--- | :--- | :--- | :--- |
| Two `virtiofsd` daemons with the given flags | done | `internal/vmm/chv/driver.go` | `TestLifecycleAgainstMockCloudHypervisor`; KVM run |
| `cloud-hypervisor --api-socket`, `PUT vm.create` payload (CPUs, shared memory, payload, 3 disks, 2 fs, vsock, net, devices; see [Implementation notes](#implementation-notes-against-the-design-text)) | done | `internal/vmm/chv/payload.go` | `TestBuildConfigPayload`; KVM run |
| Auto-built read-only `lydiabox-guest.ext4` | done | `internal/vmm/chv/driver.go` | `TestLifecycleAgainstMockCloudHypervisor`; KVM run |
| TAP `172.30.0.2/24`, NAT, no access to host private networks | done | `internal/vmm/chv/network.go` | `TestNetworkRules`, `TestFirewallForwardPolicyDrop`; KVM run (steps K7, K8) |
| Per-tenant subnet: `default` keeps `172.30.0.0/24`; other tenants get `172.30.<n>.0/24` from a stable index in the store (`vm_networks`), passed to the initramfs as `lydiabox.ip` / `lydiabox.gw` | done | `internal/vmm/chv/payload.go`, `internal/store/vmnet.go`, `internal/cli/wire_vm.go` | `TestTenantNetwork`, `TestTenantSubnet` |
| Optional VFIO GPU device | done | `internal/vmm/chv/payload.go` | `TestBuildConfigPayload` (the `devices` payload); real GPU: not available here, checklist in [kvm-test.md](kvm-test.md#10-gpu-passthrough-vfio) |
| `vm.boot`, `vm.pause`, `vm.resume`, `vm.shutdown` | done | `internal/vmm/chv/client.go` | `TestLifecycleAgainstMockCloudHypervisor`; KVM run |
| vsock `CONNECT <port>` / `OK`, and `vsock.sock_<port>` listeners | done | `internal/vmm/chv/vsock.go` | `TestVsockHandshake`; KVM run (steps K3, K9, K10) |
| Initramfs: squashfs, ext4 upper, overlay, virtiofs, guest bind mount, `switch_root` | done | `deploy/initramfs/init` | shellcheck; KVM run |
| `EnsureRunning` singleflight wake from terminal, session, desktop, MCP | done | `internal/vmm/controller.go` | `TestEnsureRunningIsSingleflight`; E2E (`wake`, session start) |
| Leases (PTY, desktop), idle timeout, `sync`, pause | done | `internal/vmm/controller.go` | `TestIdleTimerHibernatesWithoutLeases`; E2E (idle pause) |
| `docker` driver: same image and protocol over a bind-mounted UNIX socket, persistent root | done | `internal/vmm/docker` | `TestDockerLifecycleAndSockets`, `TestStopKeepsContainer`; E2E (steps 35 to 38) |
| InstaBox (addition, LydiaBox Cloud): API client (lifecycle, operations, idle pause, exec with SSE and keepalive, files, private ports, snapshots, CLI import) and the `linux/amd64` guest image for `instabox snapshot import` | client and image done; the image was checked on a real machine on 2026-11-16 and fixed for the InstaBox user model ([instabox.md](instabox.md#verified-on-a-real-machine-2026-11-16)); the rest of [the first check](instabox.md#first-check-with-a-real-account) is open | `internal/instabox`, `internal/instabox/instaboxtest`, `deploy/instabox` | client tests against the fake (`TestLifecycleStates`, `TestIdempotencyReplayAndConflict`, `TestRetryDisconnectReplaysCreate`, `TestExecRetriesOnlyWhenNotStarted`, `TestExecStream`, `TestFiles`, `TestPorts`, `TestSnapshots`, `TestImportSnapshot`, `TestAPIKeyNeverShown`); `TestOpenAPIDrift` against the live document; `deploy/instabox/test.sh` on the full and slim images (Docker, amd64 emulated on arm64): X11, Xfce, guest as UID 1000, `TestImageLink` over the real host link before and after a restart, also in the `machine` scenario (the InstaBox user model) |
| `instabox` VM driver (addition, LydiaBox Cloud): idempotent create or adoption, start, pause, wake, resume, stop, state mapping, private link port with its token, `guest.env` through the file API, activity lease while attached, state watcher for a self-pause, clear account errors with a 30 s block, no delete, guest binary upgrade with a restart only when no PTY runs, system upgrade of the root disk from a bundle in the binary (marker `/etc/lydiabox/system.sha256`); `lydiabox instabox status` and `destroy -confirm`; `auto` selects it in the provider mode | done against the fake API; nothing is checked against the real service ([instabox.md](instabox.md#first-check-with-a-real-account), steps 13 to 18) | `internal/vmm/instabox`, `deploy/instabox/system.go`, `internal/cli/wire_instabox.go`, `internal/cli/instabox.go`, `internal/config/instabox.go` | `TestCreateIsIdempotentAcrossRestarts`, `TestCreateReplaysTheSavedKey`, `TestLifecycleThroughTheGuestLink`, `TestOperationConflictIsWaitedFor`, `TestAccountErrorsAreClearAndStopRetries`, `TestDeletedMachineGivesAClearStateAndNoNewMachine`, `TestSelfPauseIsReportedAndWokenOnUse`, `TestChangedGuestSettingsRestartTheGuest`, `TestGuestUpgrade*` (6 tests), `TestSystemUpgrade*` (5 tests; `TestSystemUpgradeInDocker` with `make instabox-system-test`), `TestFilesMatchDockerfile`, `TestBundleIsDeterministic` (`deploy/instabox`), `TestInstaboxCommand`, `TestInstaboxSettings`; end to end: `TestInstaboxCellEndToEnd` (cell on a UNIX socket, PTY session, desktop signaling, 9001 and 9002, idle hibernate, self-pause, restart, no goroutine leak under `-race`) |
| Cell contract (addition): `LYDIABOX_CELL_MODE`, `_FILE` forms of the admin token, master key, and InstaBox key, `unix:` listen address, `GET /api/v1/ready`; a remote machine stays up at `SIGTERM` | done | `internal/config/cell.go`, `internal/api/server.go`, `internal/api/routes_core.go`, `internal/vmm/controller.go` | `TestCellModeAndUnixListener`, `TestUnixListenerAndReady`, `TestCloseLeavesARemoteMachineRunning`; gaps: [cloud-design.md §4.6](cloud-design.md#46-gaps-in-the-core) |

### Implementation notes against the design text

The design text of section 4.3 is kept as written. Cloud Hypervisor 54 and
virtiofsd 1.14 need three changes to the `vm.create` payload. The code
makes these changes; the `vm.create` row above is `done` with them.

| Design text | What Cloud Hypervisor 54 and virtiofsd 1.14 require | What the code does | Test |
| :--- | :--- | :--- | :--- |
| `cmdline`: `console=ttyS0 root=/dev/vda ro quiet` | On arm64, the Cloud Hypervisor serial port is a PL011 UART, `ttyAMA0`. With `ttyS0`, the guest has no console output. | `console=ttyS0` on amd64, as written; `console=ttyAMA0` on arm64 (`BaseCmdline` in `internal/vmm/chv/payload.go`) | `TestBuildConfigPayload`, `TestTenantNetwork`; KVM runs (arm64, amd64) |
| `disks`: `{"path": ..., "readonly": ...}` | Cloud Hypervisor 54 refuses `vm.boot` with "Image type required for disk" when `image_type` is not set. | Adds `"image_type": "Raw"` to the three disks | `TestBuildConfigPayload`; KVM run |
| `fs`: `"num_queues": 2` | virtiofsd serves one request queue. With 2, Cloud Hypervisor refuses the device with "Queue number is not correct". | `"num_queues": 1` for `lydia_home` and `lydia_workspace` (`FSQueues`) | `TestBuildConfigPayload`; KVM run |

## 4.4 Magpie integration and harness sessions

| Requirement | Status | Where | Verified by |
| :--- | :--- | :--- | :--- |
| magpie on `127.0.0.1:3425`, data in `$DATA_DIR/state/magpie` | done | `internal/magpie/supervisor.go` | `TestSupervisorLifecycleAndGatewayKey`; E2E (`/api/v1/models/gateway`) |
| Providers and routing managed "via Magpie API" (design sections 4.4 and 5) | done (LydiaBox drives the web API of `magpie serve --web=127.0.0.1:3426`, loopback only, random key. This API is not a versioned public interface, so a new magpie release can need changes in `internal/magpie/admin_http.go`; the pinned version in `internal/bootstrap/manifest.json` keeps them in step) | `internal/magpie/admin_http.go` | `TestHTTPAdmin` |
| Named gateway key for the VM, injected on the host side of the tunnel | done | `internal/magpie/gatewaykey.go`, `internal/guest/credproxy` | `TestGatewayKeyReplacesForeignKey`; E2E |
| Guest `127.0.0.1:3425` forwards Chat, Responses, Messages, Gemini; OpenAI and DeepSeek paths without `/v1` (`/chat/completions`, `/completions`, `/responses`, `/embeddings`, `/models`, `/models/...`) are forwarded as `/v1/...` (host side of the tunnel, where the listener's requests are parsed) | done | `internal/guest/credproxy` | `TestGatewayRoundTripOverUnixSockets`, `TestNormalizeOpenAIPath`, `TestGatewayAddsV1ForOpenAIPaths`; E2E (`/api/hello`) |
| claude-code wirer (`settings.json` env and model, MCP in `.claude.json`) | done | `internal/magpie/wire` | `TestWirersPreserveUserContent` |
| codex wirer (`base_url = .../v1`, model, `[mcp_servers.lydiabox]`) | done | `internal/magpie/wire` | `TestWirersPreserveUserContent` |
| dsh wirer (`OPENAI_BASE_URL`, `ANTHROPIC_BASE_URL`, `DEEPSEEK_BASE_URL` = `http://127.0.0.1:3425`); PTY `OPENAI_BASE_URL=http://127.0.0.1:3425` (`ptymanager.DefaultEnv`) | done | `internal/magpie/wire/dsh.go`, `internal/guest/ptymanager` | `TestEnvEdit`, `TestWirersPreserveUserContent`, `TestMultiAttachReplayAndInput` |
| dsh model: the overlay `~/.dsh/lydiabox.patch.yml` (pi-ai route `lydiabox` to the gateway, model from `DSH_MODEL` in the `acp` and `agent-default-model` rows), added by the launcher as `dsh <profile> --patch …`; session model over `DSH_MODEL`; the first gateway model when no model is bound | done (dsh 0.2.2-rc.5 against a fake gateway: `acp` and `headless` send the session model to `/v1/chat/completions`, as [harness-formats.md](harness-formats.md) shows; not yet checked with the real gateway in the VM) | `internal/magpie/wire/dsh.go`, `internal/harness/adapters/dsh.go`, `internal/acp/model.go`, `internal/cli/wire_harness.go` | `TestDSHWrapper`, `TestDSHFallbackModel`, `TestModelChoice`, `TestWirersPreserveUserContent` |
| antigravity wirer: `~/.antigravity/settings.json` (design path: endpoint, model), `~/.gemini/antigravity/mcp_config.json`, plus the CLI's own `~/.gemini/antigravity-cli/settings.json` (`modelProvider: "gemini"`, `customModelsConfig.customModels.<model>.modelName = "gemini-api://<model>"`), `~/.gemini/config/mcp_config.json`, and `GOOGLE_GEMINI_BASE_URL` / `GEMINI_API_KEY` | done (CLI keys from the published schema and docs, and checked with agy 1.5.4: `agy --model <m>` posted `/v1beta/models/<m>:streamGenerateContent` to a capture server on `127.0.0.1:3425`, and `agy mcp list` showed both servers; see [harness-formats.md](harness-formats.md)) | `internal/magpie/wire/others.go` | `TestWirersCreateMissingFiles`, `TestWirersPreserveUserContent` |
| OAuth subscriptions stay on the host (design section 1): Claude and ChatGPT sign-in through magpie, tokens in `$DATA_DIR/state/magpie`, harnesses keep the placeholder token | done (tested against a fake magpie and response bodies recorded from magpie v0.1.3531; not yet checked with real accounts, see [subscriptions-manual-test.md](subscriptions-manual-test.md)) | `internal/magpie/subscriptions.go`, `internal/magpie/admin_signin.go`, `internal/api/routes_subscriptions.go` | `TestHTTPAdminSignIn`, `TestSubscriptionRoutes`, `TestSubscriptionStoreStaysOnHost` |
| opencode wirer (`/v1`, model, `mcp`, image input: `modalities` and `attachment` on each gateway model entry) | done (images: opencode 1.18.17 against a fake gateway and MCP server; not yet checked with the real gateway in the VM) | `internal/magpie/wire/others.go` | `TestWirersPreserveUserContent`, `TestOpenCodeImageInput` |
| opencode and dsh session model without the `lydiabox/` provider prefix, both engines, also for models that an older guest binary or discovery cache reports | done | `internal/harness/model.go`, `internal/harness/manager_list.go`, `internal/harness/adapters/opencode.go`, `internal/harness/adapters/dsh.go` | `TestOCModel`, `TestDSHGatewayModel`, `TestGatewayModel`, `TestSessionModelWithoutGatewayPrefix` |
| `HarnessAdapter` interface as specified | done | `internal/harness/types.go` | build |

## 4.5 Computer use, desktop streaming, and takeover

| Requirement | Status | Where | Verified by |
| :--- | :--- | :--- | :--- |
| Ubuntu 24.04 desktop image with the listed packages | done | `deploy/rootfs/Dockerfile` (`desktop`) | KVM full-image run (F1 to F4: built for arm64 and booted on chv) |
| Five harness CLIs in the image | done (dsh: npm `@deepseek-ai/dsh`, the package of `github.com/deepseek-ai/deepseek-harness`, Node.js 24; agy: Google's installer `https://antigravity.google/cli/install.sh`, SHA512-checked native binary) | `deploy/rootfs/install-harnesses.sh`, `deploy/rootfs/Dockerfile` | installers run in an `ubuntu:24.04` container with Node.js 24.22: `claude`, `codex`, `opencode`, `dsh` 0.2.2-rc.1, `agy` 1.5.4 on PATH; KVM full-image run (F9: the five `--version` commands in the chv guest) |
| `Xvfb :0 1920x1080x24` + Xfce as `agent` (uid 1000) | done | `deploy/rootfs/supervisord.conf` | E2E (`xdpyinfo`: 1920x1080) |
| `xgb` + `xtest`: capture, mouse, keys, text | done | `internal/guest/x11` | `TestAgentActionsInjectAndEmitEvents`; E2E (`computer_screenshot`) |
| Observer / Takeover, `agent_action` events, 30 s wait or takeover message | done | `internal/guest/x11`, `internal/desktop` | `TestTakeoverBlocksAgentAndInjectsUserInput`; E2E (mode `PUT`) |
| `ffmpeg x11grab` with the given flags on first viewer, stopped at 0 viewers | done | `internal/guest/webrtc` | `TestFFmpegArgsMatchDesign`, `TestRelayedKeyframeFitsPathMTU` |
| RTP to `TrackLocalStaticRTP` (H.264), signaling over `/api/v1/desktop/ws` | done | `internal/guest/webrtc`, `internal/desktop` | `TestViewerReceivesVideoAndSendsInput`, `TestSignalingRelayAndBroadcast`; E2E (live video and takeover keyboard input in a browser) |
| WebRTC media transport | done (the design sets no port scheme; all media uses one UDP port, `LYDIABOX_WEBRTC_UDP_PORT`, default 50000, forwarded by the driver because the guest is behind NAT) | `internal/guest/webrtc`, drivers | `TestViewerReceivesVideoAndSendsInput`; E2E (step 19); KVM full-image run (W1 to W5: video and Takeover from a browser through the DNAT rule on chv) |
| WebRTC through a TURN relay with no inbound port (addition: STUN/TURN settings, Cloudflare Realtime TURN credentials, relay-only policy, TURN over TCP and TLS) | done; the check with a real TURN service is pending ([turn-manual-test.md](turn-manual-test.md)) | `internal/desktop/ice`, `internal/desktop/viewer.go`, `internal/guest/webrtc`, `internal/config/webrtc.go` | `TestRelayOnlyThroughTURNBehindNAT`, `TestGuestRelayCandidateWithoutBrowserTURN`, `TestGuestServerReflexiveWithBrowserRelay`, `TestNoTURNFailsBehindNAT` (pion vnet, symmetric NATs), `TestRelayOnlyTURNOverTCP`, `TestRelayOnlyTURNOverTLS` (Linux), `TestCloudflareMint`, `TestCredentialsRefreshBeforeExpiry`, `TestDefaultICEUnchanged` |
| MCP on `127.0.0.1:8910/mcp` with the 9 tools (23 with the browser tools) | done | `internal/guest/mcp` | `TestProtocolHandshakeAndToolList`; E2E (tool list) |
| `browser_cdp_endpoint` auto-starts Chrome with the profile directory | done | `internal/guest/mcp` | `TestLocalAppsStartsChromeForCDP`, `TestCDPEndpointWaitsForStartingChrome`; KVM full-image runs (F5: Chromium on arm64 and Google Chrome on amd64 start with the CDP flags; F6: the tool returns the endpoint of that browser, on arm64 repeated on an idle host; on amd64 after the `CHROME_CONFIG_HOME` fix, because Google Chrome refuses CDP on its default profile directory) |
| `blender_run_script` | done | `internal/guest/mcp` | `TestToolsDriveTheDesktop` (fake Blender); KVM full-image run (F7: Blender 4.0.2) |
| High-level browser use (addition): `browser_*` MCP tools on the visible desktop Chrome, element refs from `browser_snapshot`, real input events, tabs, history, text, JavaScript, page screenshots, waits; actions follow the takeover mode and emit `agent_action` events | done | `internal/guest/browser`, `internal/guest/mcp/browser_tools.go`, `internal/guest/x11` (`Act`, `Observe`) | fake CDP server (`internal/guest/browser/cdptest`): `TestOpenNavigatesCurrentTab`, `TestNewTabSelectCloseAndList`, `TestCurrentFollowsVisibleTabAndCreatesOne`, `TestClickDispatchesMouseEvents`, `TestTypeSelectPressScroll`, `TestTakeoverBlocksActionsButNotReads`, `TestReattachAndReconnect`; MCP: `TestBrowserToolsList`, `TestBrowserToolsCall`; `TestActFollowsTakeoverAndPublishes`; real Chrome: `TestRealChrome` (`LYDIABOX_TEST_CHROME`, Chrome 2026-11 on macOS, headless); Docker image run (below) |
| `lbx` CLI for every harness (addition): `lbx browser ...`, `lbx computer ...`, `lbx tools`, `lbx call`; `/usr/local/bin/lbx` in the image | done | `internal/guest/cli/cmd_lbx.go`, `cmd_lbx_ops.go`, `deploy/rootfs/setup-guest.sh` | `TestLbxBrowser`, `TestLbxComputerToolsAndCall`, `TestLbxDaemonDown` (real MCP server, fake desktop and Chrome); Docker image run (below) |
| Discovery in all five harnesses (addition): marked block in `~/.claude/CLAUDE.md`, `~/.codex/AGENTS.md`, `~/.config/opencode/AGENTS.md`, `~/.dsh/AGENTS.md`, `~/.gemini/GEMINI.md` | done; a session of each harness that uses the block without a prompt is not checked yet | `internal/magpie/wire/instructions.go` | `TestWirersPreserveUserContent` (golden files with user text before and after an old block), `TestMergeInstructions`, `TestInstructionsInEveryHarness` |

Docker image run of the browser use (2026-11-19): the `lydiabox/guest:instabox-base`
image (amd64, emulated on an arm64 Mac) with the new `lydiabox-guest`, as
the `agent` user. `lbx tools` lists 23 tools. `lbx browser open
https://example.com` returns after 4.3 s with the title; `lbx browser
snapshot` gives `[e1] link "Learn more" -> https://iana.org/help/example-domains`;
`lbx browser click e1` opens `https://www.iana.org/help/example-domains`
(`Example Domains`). `text`, `back`, `tabs`, `open --new-tab`, `close`,
`lbx browser screenshot`, and `lbx computer screenshot` also pass, and the
desktop screenshot shows the maximized Chrome on the Xfce desktop with the
IANA page.

## 4.6 Zero-trust connectors

| Requirement | Status | Where | Verified by |
| :--- | :--- | :--- | :--- |
| OAuth2 Authorization Code + PKCE (`authorize`, `callback`) | done | `internal/connector/oauth.go` | `TestPKCEAuthorizeCallback` (fake provider); real GitHub: full checklist passes, including grant, guest `git-cred`, MCP tools, and revoke ([connector run](#connector-run-github-real-provider)) |
| Token / PAT binding (`POST .../token`) | done | `internal/connector/token.go` | `TestBindToken` |
| GitHub (`repo`, `read:org`, `workflow`), Drive (`drive.file`, `drive.readonly`), Notion, Slack | done | `internal/connector/providers_builtin.go` | `TestAuthorizeValidation`; tools against fake provider APIs: `TestGitHubTools`, `TestGDriveTools`, `TestNotionTools`, `TestSlackTools`; real providers: [connectors-manual-test.md](connectors-manual-test.md) |
| Encrypted `connector_accounts` | done | `internal/store/connectors.go` | `TestConnectorAccounts` |
| Automatic refresh | done | `internal/connector/token.go` | `TestRefreshOnExpirySingleFlight` |
| Upstream revoke on disconnect (Notion added in Phase 6) | done | `internal/connector/providers_builtin.go` | `TestDisconnectRevokes` |
| `credential.helper = lydiabox-guest git-cred`, token over port 9001 | done | `deploy/rootfs/setup-guest.sh`, `internal/guest/gitcred` | `TestGitCredEndToEnd`; E2E (helper runs in the guest; no account was bound, so no token was issued) |
| Connector MCP proxied to guest `127.0.0.1:8911/mcp` with `github_*`, `gdrive_*`, `notion_*`, `slack_*` | done | `internal/connector/broker`, `internal/guest/credproxy` | `TestMCPProtocol`, `TestGitHubTools`, `TestGDriveTools`; E2E (tool list in the guest) |
| Host Connector MCP Server at `/mcp/connectors` on the main listener (authenticated; `POST` JSON-RPC, `GET` 405), the same broker handler that the guest reaches at `127.0.0.1:8911/mcp` | done | `internal/api/routes_connectors.go`, `internal/cli/wire_connector.go` | `TestHostConnectorMCPRoute` |

## 5. Web UI

| Requirement | Status | Where | Verified by |
| :--- | :--- | :--- | :--- |
| Status bar: VM pill, Wake / Hibernate / Stop, CPU/RAM, "Synced Xs ago" (only when the server syncs to S3: `storage.syncEnabled`) | done | `web/src/components`, `web/src/lib/sync.ts` | web tests (`sync.test.ts`); E2E (browser) |
| VM power controls only on the Desktop page (owner request, 2026-11-19; before that in the status bar); the status bar shows only a running transition ("Starting the VM…"); the Files page lists without `auto=1` and so starts or resumes the VM; a page that needs a VM stopped by the user offers Start in its error view | done | `web/src/pages/desktop/DesktopToolbar.tsx`, `web/src/pages/DesktopPage.tsx`, `web/src/components/StatusBar.tsx`, `web/src/pages/FilesPage.tsx` | web tests (`DesktopToolbar.test.ts`, `stage.test.ts`); browser check against a mocked API |
| A user stop holds until an explicit start: the desktop socket, a terminal reconnect, and file requests do not start the VM; the Desktop page shows a calm stopped state, does not retry, and connects again after the start; an idle pause still wakes on use | done | `internal/vmm/controller.go`, `internal/desktop`, `internal/harness/manager.go`, `web/src/pages/desktop/stage.ts`, `web/src/api/socket.ts` | `TestUserStopHoldsUntilStart`, `TestWakeQueuedBehindAStopDoesNotBoot`, `TestDesktopSocketDoesNotStartAVMThatTheUserStopped`, `TestDesktopSocketDuringStopDoesNotStartTheVM`, `TestDesktopStreamEndedByAUserStopSaysStopped`, `TestStoppedVMIsStartedOnlyByANewSession`, `TestUserStopIsNotUndoneByAWakeOnDemand`, `TestIdlePausedMachineWakesOnDemand`; web tests (`stage.test.ts`, `desktopSignal.test.ts`, `socket.test.ts`) |
| The user-stop hold survives a host restart (`vm_holds` in the state database); a failed or refused stop restores it; a host shutdown, `hibernate`, and the idle pause do not set it; an unreadable hold does not block a start | done | `internal/vmm/hold.go`, `internal/store/vmhold.go`, `internal/cli/wire_vm.go` | `TestUserStopHoldSurvivesAHostRestart`, `TestFailedStopRestoresTheStoredHold`, `TestShutdownAndHibernateDoNotHold`, `TestStoredHoldOfARunningVMIsDropped`, `TestBrokenHoldStoreDoesNotBlockAStart`, `TestVMHold` |
| A live terminal ended by a user stop ends with the `stopped` error, not `exit -1`; the Files folder tree shows no error for the expected `503` while the VM is stopped and loads again after a start | done | `internal/harness/room.go`, `web/src/pages/files/folderNode.ts`, `web/src/pages/FilesPage.tsx` | `TestTerminalEndedByAUserStopSaysStopped`; web test `folderNode.test.ts` |
| Guest link attempts that fail while the machine boots (HTTP 502 from the provider port) log at info level; one warning with the last cause when the link is still down after the boot time limit | done | `internal/vmm/link/run.go` | `TestBootTimeLinkFailuresAreInfo`, `TestLinkDownAfterBootTimeoutWarnsOnce` |
| Studio: session list with harness and workspace filters, xterm.js | done | `web/src/pages/studio` | web tests; E2E (browser) |
| Desktop: WebRTC video, overlay, Observer/Takeover, Chrome/Blender launch | done | `web/src/pages/desktop` | web tests; E2E (browser, signaling) |
| Files: tree, editor, drag-and-drop upload, download, WebDAV dialog | done | `web/src/pages/files` | web tests; E2E (browser) |
| Models: subscriptions (sign-in dialog), providers, routing groups; the model of a session is chosen in New session (preselected from the last start of the harness) and changed from the session header (`PATCH /api/v1/sessions/{id}`, `TestSetModelResumesWithNewModel`) | done | `web/src/pages/models` | web tests; browser check against a fake magpie |
| Connectors: cards, OAuth / client / token drawers, audit log | done | `web/src/pages/connectors` | web tests |
| Cloudflare Access login: no token prompt, email in the status bar, sign-out through `/cdn-cgi/access/logout`, notice for an email that is not allowed | done | `web/src/App.tsx`, `web/src/pages/LoginPage.tsx`, `web/src/components/StatusBar.tsx` | browser check against a mocked session route |

## 6. Phase verification gates

| Gate | Status | Verified by |
| :--- | :--- | :--- |
| Phase 1: traversal rejection, AES-256-GCM, File API CRUD without a VM | done | `TestTraversalRejected`, `TestCipherRoundTrip`, `TestFileAPICRUD`; E2E |
| Phase 2: `linux/amd64` and `linux/arm64` builds; `chv` payload and mock lifecycle; RPC PTY + MCP over UNIX sockets | done | `make release`; `TestBuildConfigPayload`, `TestLifecycleAgainstMockCloudHypervisor`, `TestVsockHandshake`, `TestPTYRoundTripOverUnixSocket` |
| Phase 3: realistic logs for 5 harnesses, wiring | done | `TestDiscover`, `TestWirersPreserveUserContent` |
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
(Chrome, Blender, and harness CLIs; too large for this Docker VM), and OAuth
against real providers.

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

### User-stop run (2026-11-17)

Setup as above (`docker:28-dind`, `linux/arm64` host binary,
`lydiabox/guest:slim`), with `LYDIABOX_STORAGE_MODE=provider` (no S3, no
`juicefs`) and without `magpie`. The checks used `curl`, a small WebSocket
client, and the browser pane.

| # | Step | Result |
| :--- | :--- | :--- |
| 1 | Studio: a live shell (`hello`, output `LIVE-42`), then `POST /vm/stop`: the terminal socket gets `error` `stopped` and close `1000`; the page shows "The VM is stopped." and does not reconnect; the session list shows `Exited`; the VM stays `STOPPED` | pass (after fix 1) |
| 2 | Host restart while the user stop holds: the log shows "the VM stays stopped until a start"; the desktop socket gets `stopped`; `GET /fs/workspace/list` `503` logged at `INFO`; the VM stays `STOPPED` | pass |
| 3 | Files page: Stop from the status bar; the folder tree shows no red error, also after a page reload; Wake loads the listing and the tree again without a reload | pass (after fix 2) |
| 4 | `hibernate` (container paused), then a file list, a desktop socket, and a new session each wake the VM | pass |

1. A live terminal ended by a user stop sent `exit` `-1`, and the terminal
   showed "[process exited with code -1]". The room now sends `stopped`.
2. The folder tree showed a red "Folder not loaded" for the expected `503`.
3. A client that went away during `POST /vm/stop` cancelled the shutdown
   (the request context reached `docker stop`), and the VM state stayed
   `RUNNING` with the container gone until a forced stop. The stop now runs
   to its end (limit 3 minutes, `TestVMStopOutlivesTheClient`).

A file list that the browser cancels was logged as a `500` at error level
(`client_gone=true`); the next run fixes it.

### Hibernate and log run (2026-11-17)

Setup as in the user-stop run. The checks used `curl`, a small WebSocket
client, and the browser pane.

| # | Step | Result |
| :--- | :--- | :--- |
| 1 | Studio: a live shell (`echo PID=$$` gives `45`), then `POST /vm/hibernate`: the VM is `PAUSED`; at the keepalive timeout (50 s with `docker pause`) the socket gets `error` `paused` and close `1000`; the log has one INFO "harness terminal detached by a VM pause" | pass |
| 2 | While paused: the session list shows the session as `idle` (not `exited`); the VM stays `PAUSED` | pass |
| 3 | A new socket for the same session wakes the VM and attaches to the same shell (`PID=45` again); `exit` then gives `exit` `0` | pass |
| 4 | Browser: Hibernate from the top bar with the shell open: the terminal shows "The VM is hibernating." (neutral), no exit code, no reconnect; Resume: the terminal attaches again and the unfinished input line of the shell is still there | pass |
| 5 | 30 session lists during `POST /vm/hibernate`: no WARN "guest session discovery failed" | pass |
| 6 | File lists that the client cancels after 20 ms and 60 ms while the VM wakes: no `request failed` line (the answer is `499`, logged at debug level) | pass |
| 7 | Dashboard-style stop: `POST /vm/stop` with the admin token, then SIGTERM: no WARN or ERROR | pass |

The docker driver does not close the guest port listeners at the host
shutdown, so step 7 cannot show the InstaBox ERROR "guest LLM gateway tunnel
stopped"; `TestGuestTunnelEndAtHostShutdownIsInfo` covers it.

### Resume after Hibernate on InstaBox (2026-11-17)

On `the test machine` a resume after a Hibernate with a live terminal took 59.9 s
(`BOOTING` to `guest link up`); InstaBox reported the machine `running` at
05:07:20 and the link was up 1.8 s later, so the time was spent before the
port proxy reached the guest. The host path has no wait of that length:
with the fake InstaBox (`TestInstaboxResumeAfterHibernateWithATerminal`, two
rounds with a live terminal) a resume takes about 1 s and the terminal
attaches again to the same shell. The driver now logs each start, resume,
and wake with its duration (`took`) and the link's `connect` time, and warns
when a resume did not keep the processes
(`TestInstaboxSlowResumeIsLogged`, `TestInstaboxFreshResumeEndsTheTerminal`).

### Studio, Desktop, and Files through a power cycle (2026-11-17)

Setup as in the user-stop run; the browser pane at 1024 x 768.

| # | Step | Result |
| :--- | :--- | :--- |
| 1 | Studio: new shell (starts the VM), `echo PID=$$` gives `46`, Hibernate: "The VM is hibernating." within 3 s (before: about 50 s with `docker pause`) | pass (after fix 1) |
| 2 | Resume: within 3 s the terminal attaches to the same shell, the list shows `Live` and no "Cached" label | pass (after fix 2) |
| 3 | Stop: the list shows `Exited` at once; after Wake the terminal says "This terminal ended when the VM stopped." | pass (after fixes 2 and 3) |
| 4 | Delete an ended shell: one click, no dialog | pass (after fix 4) |
| 5 | Top bar while `RUNNING`, `PAUSED`, `STOPPED`: one line, the page does not move | pass (after fix 5) |
| 6 | Files: Hibernate and Resume from the status bar; the tree and the listing stay | pass |
| 7 | Desktop: paused shows "The VM is hibernating" and Start desktop; Takeover: the keyboard hint is in the toolbar, not on the screen | pass (after fix 6) |
| 8 | Desktop: no WebRTC path in `dind`: "Starting the desktop stream…", then "Desktop video not connected" with Reconnect (before: a black screen without end) | pass (after fix 7) |

1. The host ends live terminals with `paused` when the VM is `PAUSED`.
2. The session list reloads on every VM state change and when a terminal
   attaches.
3. The stopped banner of a terminal changes when the VM runs again.
4. An ended shell (no transcript) is deleted without a dialog.
5. The top bar stays on one line from the `sm` width up (the CPU and
   memory text shows from `xl`).
6. The takeover hint moved into the toolbar.
7. The stage is live only when the WebRTC peer is connected, and a failed
   peer shows its own state.

### Host restart with live terminals (2026-11-17)

Setup as in the user-stop run. A shell (`bash` PID 43) and a second shell
running `while true; do echo tick-$(date +%s); sleep 1; done` (PID 52, the
stand-in for a long agent process).

| # | Step | Result |
| :--- | :--- | :--- |
| 1 | `SIGKILL` of the host process (the docker VM keeps running), host start: both sessions `running` and `live` before any client attached; log "harness terminal attached again" twice | pass |
| 2 | Attach: the shell answers `PID=43`; the loop still prints ticks; after Ctrl-C its shell answers `PID=52` | pass |
| 3 | Delete the loop session: its `bash` and `sleep` end in the guest | pass |
| 4 | `SIGTERM` (docker stops the VM): after the host start the session is `exited` with the time of the restart; no WARN or ERROR | pass (after the stopped pass at start) |
| 5 | 375 px: the top bar, Studio, Files, and Desktop through Wake, Hibernate, Resume, and Stop; ended shells show their end time | pass (after hiding the keyboard hint at phone widths) |

`TestInstaboxHostRestartKeepsLiveTerminals` covers the production case: a
graceful restart of an InstaBox cell, where the machine keeps running.

### Hibernate from another source (2026-11-17)

Setup as in the user-stop run. Tab A in the browser pane; the hibernate goes
through `POST /api/v1/vm/hibernate` with the admin token, as from another tab,
device, or the idle pause. The VM state was read every 2 s.

| # | Tab A | Result |
| :--- | :--- | :--- |
| 1 | Desktop with a connected stream | The stream ends with `paused`; the page shows "The VM is hibernating"; the VM stays `PAUSED` for 20 s | pass |
| 2 | Studio with a live shell | "The VM is hibernating."; the session is `idle`; the VM stays `PAUSED` for 30 s | pass |
| 3 | Files | The listing stays; no file request; the VM stays `PAUSED` for 30 s | pass |
| 4 | Desktop: Start desktop | The VM resumes and the stream connects again | pass |

### Production findings of release fda1c54 (2026-11-17)

Setup as in the user-stop run (docker driver in dind, provider storage mode,
browser pane at 1024 and 800 px). The models list was made empty in the tab
(no magpie locally), and a guest pause of 25 s made a start slow.

| # | Check | Result |
| :--- | :--- | :--- |
| 1 | Status bar and Desktop with a stopped VM | `Start`; Desktop "The VM is stopped" with `Start` | pass |
| 2 | Status bar and Desktop with a paused VM | `Resume`; Desktop "The VM is hibernating" with `Resume` | pass |
| 3 | Start that takes 28 s | "The VM provider is slow." shows after 15 s, not at 5 s; it goes at `RUNNING` | pass |
| 4 | Files: page open, then Hibernate and Resume | One list request at open, one at the resume (two before) | pass |
| 5 | New session dialog, no model | Claude Code: notice with "Set up a model" (opens `/models`); Shell: no notice, the shell starts | pass |
| 6 | Claude Code session in the list, no model | The session view shows the notice | pass |

The `/data/workspace` path form (InstaBox links) and the InstaBox maximum-run
pause are covered by unit tests (`internal/harness/discovery_roots_test.go`,
`internal/vmm/instabox/names_test.go`), not by this run.

### Missing harness, model gateway down, narrow widths (2026-11-18)

Setup as above (docker driver in dind, no magpie, slim guest image without
`claude`), with the Vite dev server against the cell. For the width checks
the slow-provider line, a long account email, and the takeover hint were
forced on by a local change that was not committed.

| # | Check | Result |
| :--- | :--- | :--- |
| 1 | New Claude Code session, `claude` not installed | `409 not_installed`; the dialog says "Session not started. Claude Code is not installed in the VM (the command "claude" was not found)."; one INFO log line, no ERROR | pass |
| 2 | Studio while `GET /api/v1/models` answers 503 | "The models list is not available. An agent cannot answer until the model gateway runs." with "Open Models"; no "No model is set up" | pass |
| 3 | Top bar at 320, 360, 640, 800, 1024, 1280 px, all extra items shown | Every tab in view, no horizontal scroll; the VM group wraps to a second row when the row is full | pass |
| 4 | Studio, Desktop, Files, Models, Connectors at 360 px | No horizontal scroll; Studio list, dialog, and live shell; Desktop panel closed by default; long file names cut with the full name in the tooltip; file view | pass |

### Stopped cell, deleted sessions, Desktop panel (2026-11-18)

Setup as above, with a small local proxy between the Vite dev server and the
cell that answers like the LydiaBox Cloud control plane (503 JSON with
`cell_stopped`, `cell_starting`, or the older "not running" text), or is
stopped to cut the server off.

| # | Check | Result |
| :--- | :--- | :--- |
| 1 | Desktop open, VM running; proxy answers `cell_stopped` | Within one poll: pill `STOPPED`, no power controls, the server's text with "Open the dashboard"; Desktop "The VM is stopped", stream closed, no start; Studio and Files show the server's text | pass |
| 2 | The same with the older "the agent computer is not running" text | Same result | pass |
| 3 | Proxy answers `cell_starting` | Pill `BOOTING`, "Starting the VM…", the server's text | pass |
| 4 | Proxy stopped while the VM runs | Still `RUNNING` at 6 s; `NO STATUS` and "The VM status is not available" at 16 s; no power controls | pass |
| 5 | Proxy back | `RUNNING` with Hibernate and Stop again, with no reload; the start screen also recovers by itself | pass |
| 6 | Delete a discovered Claude Code session without its transcript | The dialog says the transcript files stay; the session stays out of the list after two polls; the file stays in the guest | pass |
| 7 | Desktop panel open at 1280 px, window to 800 px, then 360 px, then 1280 px | Closes when narrow; at 360 px it opens over the stage with a close button; shows again at 1280 px | pass |

### State matrix (2026-11-18)

Setup as above (stand-in control-plane proxy, Vite dev server). Each state on
Studio, Desktop, Files, Models, Connectors, and the top bar: label, enabled
buttons, texts, and the API requests in a window of 5 to 12 s. Pausing and
stopping from another source were held open by a frozen guest container; the
provider cooldown came from a `/vm/status` answer changed in the tab.

| State | Result |
| :--- | :--- |
| `RUNNING` | All actions; Hibernate and Stop | pass |
| Pausing, stopping (API, other source) | `PAUSING` / `STOPPING`, no controls; Desktop closes its stream | pass |
| `PAUSED` | Resume and Stop; Desktop "The VM is hibernating"; opening Files resumes the VM (a user action) | pass |
| `STOPPED` after a user stop | Start; Files "The VM is stopped" with Start and change actions disabled (was "Files is not running" with actions enabled) | pass after fix |
| `BOOTING` (start held 30 s) | No controls; Desktop "Starting the VM…"; actions wait | pass |
| Provider cooldown | Start, Resume, New, New session disabled with the provider's reason | pass after fix |
| `cell_stopped`, `cell_starting`, no status | Banner with the VM state, page inert, no figures; only the status poll runs (was each page load, the folder tree, and the desktop probe) | pass after fix |
| Start screen during an outage | Same title, tries again by itself | pass after fix |

### Files page without a wake, gateway texts, Studio list (2026-11-18)

Setup as above, with the release build (no dev server).

| # | Check | Result |
| :--- | :--- | :--- |
| 1 | Fresh load of Files with the VM stopped (no user stop) | "The VM is stopped", Start; no file request in 12 s; the VM stays `STOPPED` | pass |
| 2 | Fresh load of Files with the VM paused | "The VM is hibernating", Resume; no file request in 15 s; the VM stays `PAUSED` (before the fix one `list?auto` went out, refused by the host without a wake) | pass after fix |
| 3 | Resume on the Files page | The VM resumes and the folder lists | pass |
| 4 | Studio during a hibernate and a resume through the API | The "Cached" label follows the pill within 0.2 s both times | pass |
| 5 | Models page without the gateway | "The model gateway is not available: the gateway did not start."; no "magpie" in the page | pass |

No WARN or ERROR in the cell log except the expected `magpie` and S3 setup
lines.

## Integration run (chv on KVM)

Date: 2026-11-10. Host: Apple M3 Pro (macOS 26), Lima 2.3.0 VM with
`vmType: vz` and `nestedVirtualization: true` (Ubuntu 24.04 arm64, kernel
6.8, 8 vCPUs, 12 GiB, 80 GiB disk). Nested KVM works: `/dev/kvm` is
present in the Lima VM. The guest ran with Cloud Hypervisor 54.0,
virtiofsd 1.14.0 (built from source), the `ch-6.12.8` kernel, and the
`desktop-slim` root filesystem. MinIO ran in Docker in the Lima VM. The
procedure is in [kvm-test.md](kvm-test.md).

Build times in the Lima VM (`build-artifacts.sh --slim`, stages in
parallel): 904 s in total; kernel compile 591 s, `desktop-slim` packages
398 s, virtiofsd `cargo build` 140 s, `mksquashfs` 88 s. A rebuild after a
change to `lydiabox-init` took 99 s. Sizes: `vmlinux` 23 MiB,
`initramfs.cpio.gz` 1 MiB, `rootfs.squashfs` 219 MiB. The kernel config
needed no change: `ch_defconfig` exists for arm64 in the fork, and
`kernel.config` merged without warnings.

Boot time (`POST /vm/wake` to `RUNNING`): 45 s to 85 s with 2 or 4 vCPUs.
The guest is slow under nested virtualization (`python3 -c pass` takes
3.5 s), and other processes loaded the Mac during the run (load average
about 150). With 4 vCPUs, one boot did not finish in the 120 s boot limit;
the final runs used 2 vCPUs and 2 GiB (`LYDIABOX_VM_CPUS=2`,
`LYDIABOX_VM_MEM_MB=2048`).

Final run (all fixes below applied), `LYDIABOX_IDLE_TIMEOUT=60s`:

| # | Step | Result |
| :--- | :--- | :--- |
| K1 | `POST /vm/wake` from `STOPPED`: `RUNNING` in 85 s; `GET /vm/status` shows `"driver":"chv"`, 2 CPUs, 2048 MiB | pass |
| K2 | `shell` session over the PTY WebSocket: `uname -a` gives `Linux lydiabox 6.12.8+ ... aarch64`; `id` gives `uid=1000(agent)`; `grep -c processor /proc/cpuinfo` gives 2 | pass |
| K3 | `mount`: `overlay on /` (lower `/mnt/ro`, upper `/mnt/rw/upper`), `lydia_home on /home/agent type virtiofs`, `lydia_workspace on /workspace type virtiofs` | pass |
| K4 | `PATH` includes `/usr/local/bin`; `lydiabox-guest` is found | pass (after fix 5) |
| K5 | A file written in the guest under `/workspace` is read with `GET /fs/workspace/text` | pass |
| K6 | A file written with `PUT /fs/workspace/text` is read in the guest | pass |
| K7 | `curl -sI https://example.com` in the guest: `HTTP/2 200` | pass (after fix 3) |
| K8 | From the guest, the host LAN address `192.168.5.15:8080`, the TAP host address `172.30.0.1:8080`, and the Docker bridge `172.17.0.1:9000` time out | pass |
| K9 | `curl http://127.0.0.1:3425/v1/models` in the guest reaches magpie through vsock port 9002: 40 of 40 requests give 200 | pass (after fix 6) |
| K10 | `lydiabox-guest git-cred get` reaches the host broker over vsock port 9001: exit 0, no output (broker 404, no account). With `LYDIABOX_HOST_ADDR=vsock://2:9555` it fails (`connection reset by peer`) | pass |
| K11 | Root persistence: `sudo tee /etc/lydiabox-kvm.txt`, `POST /vm/stop`, `POST /vm/wake` (69 s), the file has the same content | pass |
| K12 | `PUT /desktop/mode` `TAKEOVER` and `OBSERVER` 200; `Xvfb :0` runs | pass |
| K13 | `POST /vm/hibernate`: `PAUSED`, and `vm.info` on the Cloud Hypervisor API socket gives `"state":"Paused"`; `wake` resumes in 0.2 s; the same shell session still has its variable | pass |
| K14 | `POST /vm/stop`: `STOPPED` in 4 s; no `cloud-hypervisor` or `virtiofsd` process, no TAP device, no `lydiabox_*` nftables table, no `FORWARD` rules | pass |
| K15 | `DELETE /sessions/{id}` for every session (5 s for a live shell), then the VM pauses about 60 s after the last lease ends | pass (after fix 4) |
| K16 | `SIGTERM` to `lydiabox up`: the VM stops, virtiofsd logs "Client disconnected, shutting down", JuiceFS unmounts, "shutdown complete" | pass |

Not tested in this run: GPU passthrough (the Lima VM has no PCI GPU to
pass through), WebRTC video in a browser over the forwarded UDP port, and
an amd64 KVM host. WebRTC on chv was tested later with the full image:
see [Integration run (chv on KVM), full desktop image](#integration-run-chv-on-kvm-full-desktop-image).
The amd64 KVM host was tested later: see
[Integration run (chv on KVM, amd64)](#integration-run-chv-on-kvm-amd64).

### Bugs found and fixed (chv on KVM)

1. Cloud Hypervisor 54 refused `vm.boot` with "Image type required for
   disk". The disks now have `"image_type": "Raw"`.
2. Cloud Hypervisor refused the virtio-fs devices with "Queue number is
   not correct": virtiofsd serves one request queue. `num_queues` is now 1
   (design section 4.3 said 2).
3. The guest had no internet access on a host with Docker: Docker sets
   the iptables `FORWARD` policy to `DROP`, and an accept in the LydiaBox
   nftables table does not override a drop in another table. When the
   `FORWARD` policy is `DROP`, the driver now also inserts iptables accept
   rules for the TAP device (and removes them on stop); the private-network
   drops of the LydiaBox table still apply (`TestFirewallForwardPolicyDrop`).
4. `DELETE /api/v1/sessions/{id}` of a live shell never returned:
   `ptyConn.Wait` took the read lock that the terminal pump holds while it
   waits for output, so it ignored its context. `Wait` now reads the exit
   state without that lock (`TestPTYWaitDoesNotBlockBehindRead`). Also, an
   interactive shell ignores `SIGINT` and `SIGTERM`, so the session stayed
   alive and kept its PTY lease (the VM never went idle). The stop sequence
   now sends `SIGHUP` after `SIGTERM`.
5. In the VM, `PATH` was the initramfs value (`/bin:/sbin:/usr/bin:/usr/sbin`),
   so sessions did not find `/usr/local/bin/lydiabox-guest`.
   `lydiabox-init` now sets the standard `PATH`, `LANG`, `LC_ALL`, and
   `TZ`.
6. Most guest-to-host tunnel requests (magpie on port 9002) failed with an
   empty reply. The guest RPC dialer used a non-blocking `AF_VSOCK`
   connect and took the first writable event as the end of the connect,
   before the connection was established. It now connects in blocking mode
   (bounded by the kernel vsock connect timeout), as the git-cred dialer
   already did.
7. The arm64 serial console is `ttyAMA0` (PL011), so `console=ttyS0` gave
   no guest console output. The kernel command line now uses `ttyAMA0` on
   arm64.
8. `build-artifacts.sh --slim` built the full `desktop` stage into
   `rootfs.squashfs`. The `ROOTFS_STAGE` build argument now selects
   `desktop-slim`.

Open items:

- `vm.shutdown` stops the guest without a guest power-off, so the ext4
  upper layer is replayed from its journal at the next boot ("recovery
  complete"). The controller syncs the guest file systems first, and no
  data was lost in the run.
- The guest kernel logs a `pps pps0` line every second (from
  `ch_defconfig`); it fills `dmesg`.
- On nested virtualization with a loaded host, a boot can take more than
  the fixed 120 s boot limit.

## Integration run (chv on KVM), full desktop image

Date: 2026-11-10. The same Lima VM as above, with `networks: [{vzNAT:
true}]` added (the VM address on the Mac is `192.168.64.2`), the full
`desktop` image, `LYDIABOX_VM_CPUS=2`, `LYDIABOX_VM_MEM_MB=4096`. The
procedure is in [kvm-test.md](kvm-test.md#7-full-desktop-image) (steps 7
and 8). The root overlay of the slim run was removed before the first
boot.

Build (`build-artifacts.sh --no-virtiofsd`, without `--slim`; the kernel
and initramfs stages came from the build cache of the slim run): 1121 s in
total. `desktop` packages (Xfce, Blender 4.0.2, ffmpeg, and the others)
307 s, Node.js 52 s, Chromium 156 (`ppa:xtradeb/apps`, arm64) 109 s, the
five harness installers 162 s, copy of the root 98 s, `mksquashfs` 280 s.
No package failed on arm64; the Dockerfile needed no change. A rebuild
after a change to `supervisord.conf` took 1058 s (the package layers came
from the cache; most of the time was the copy, `mksquashfs`, and the
export under host load). `rootfs.squashfs`: 1,285,120,000 bytes
(1226 MiB), compared with 219 MiB for `desktop-slim`.

Host load: other processes on the Mac kept its load average between 180
and 290 during the run. The guest was very slow (a 2-vCPU guest under
nested virtualization on a loaded host). One time all wakes failed at the
120 s boot limit (the kernel took more than 100 s to reach user space);
after a restart of the Lima VM, the wake took 34 s.

| # | Step | Result |
| :--- | :--- | :--- |
| F1 | `POST /vm/wake` from `STOPPED` with an empty root overlay: `RUNNING` in 53 s, `"driver":"chv"`, 2 CPUs, 4096 MiB | pass |
| F2 | `Xvfb :0 -screen 0 1920x1080x24`, `dbus-run-session -- /usr/bin/startxfce4`, `xfce4-session`, `xfwm4`, `xfce4-panel`, `xfdesktop`, and `lydiabox-guest serve` run; `xdpyinfo`: `1920x1080` | pass (after fix 1; the panel and desktop were complete about 4 min after the wake) |
| F3 | `initialize` and `tools/list` on `http://127.0.0.1:8910/mcp` in the guest: 9 tools (`computer_screenshot`, `computer_mouse_click`, `computer_mouse_move`, `computer_mouse_drag`, `computer_scroll`, `computer_keyboard`, `desktop_launch_app`, `browser_cdp_endpoint`, `blender_run_script`) | pass |
| F4 | `computer_screenshot`: `image/png`, 1920x1080, all 256 byte values in the decoded pixels; the image shows the Xfce desktop with the panel | pass (after fix 1; before it, the image showed only "Unable to contact settings server") |
| F5 | `desktop_launch_app` `chrome`: `Launched chrome`; `/usr/lib/chromium/chromium ... --remote-debugging-port=9222 --remote-debugging-address=127.0.0.1 --user-data-dir=/home/agent/.config/google-chrome` runs; its window shows in the panel | pass |
| F6 | `browser_cdp_endpoint`, then `curl http://127.0.0.1:9222/json/version` | pass (repeated on 2026-11-11 on an idle host, with binaries from `929fa65`; Mac load average 6 to 8 at the wake and the Chrome start, about 20 to 25 at the first answer because of other processes). Wake 18 s; `xfwm4` and `xfce4-panel` 16 s after the wake. After `desktop_launch_app` `chrome`, one `chromium` process with `--remote-debugging-port=9222`; `browser_cdp_endpoint` calls 1 to 4 returned the 30 s timeout error, call 5 returned `ws://127.0.0.1:9222/devtools/browser/...` 173 s after the launch. `/json/version`: `Chrome/156.0.8078.73` with the same `webSocketDebuggerUrl`; `ss -ltnp`: `chromium` listens on `127.0.0.1:9222` only; `Target.createTarget` `about:blank` over that WebSocket (Node 24 in the guest) returned a `targetId`. First run (2026-11-10): partial. Other processes kept the Mac load average between 180 and 290, and the starved guest did not answer CDP in 25 minutes of calls |
| F7 | `blender_run_script` with `import bpy; print("BLENDER", bpy.app.version_string)`: output `BLENDER 4.0.2` ... `Blender quit` | pass |
| F8 | `desktop_launch_app` `terminal`, `computer_mouse_click` (400, 300) in the `xfce4-terminal` window, `computer_keyboard` `echo LYDIA$((6*7))KVM` + `Return`: the next screenshot shows `LYDIA42KVM` | pass |
| F9 | `claude --version` 2.1.322 (Claude Code), `codex --version` codex-cli 0.167.0, `dsh --version` 0.2.2-rc.1, `agy --version` 1.5.4, `opencode --version` 1.19.5; all on `PATH` (`/usr/bin`, `agy` in `/usr/local/bin`). On this slow guest they took 16 s to 426 s | pass |

WebRTC from a browser on the Mac (headless Chrome 157 on macOS, driven
over CDP: it signs in with `POST /api/v1/auth/login`, opens `/desktop`,
reads the `<video>` element, and sends mouse and key events). The page
URL is `http://192.168.64.2:8080`, so the guest advertises
`192.168.64.2:50000` as its ICE candidate.

| # | Step | Result |
| :--- | :--- | :--- |
| W1 | While the VM runs: table `lydiabox_lbx37a8eec1` has `iifname != "lbx37a8eec1" udp dport 50000 dnat to 172.30.0.2`; iptables `FORWARD` has `-o lbx37a8eec1 -p udp --dport 50000 -j ACCEPT` (Docker sets the `FORWARD` policy to `DROP`) | pass |
| W2 | Desktop page: `videoWidth` 1920, `videoHeight` 1080; `currentTime` 2.97 s, then 73.0 s 70 s later; `totalVideoFrames` 1, then 115. The first frame came 160 s after the page opened (55 s after the Lima VM restart); the guest encodes about 2 to 5 frames per second under this load | pass |
| W3 | `conntrack -L -p udp --dport 50000` on the Lima VM: `src=192.168.64.1 dst=192.168.64.2 sport=56844 dport=50000 src=172.30.0.2 dst=192.168.64.1 sport=50000 [ASSURED]`; the guest socket on UDP 50000 receives the browser packets | pass |
| W4 | Takeover button: mode `TAKEOVER`; a click at guest (400, 300) in the `xfce4-terminal` window and the keys `uname -n` + Enter; the guest screenshot shows `uname -n` and `lydiabox` | pass (after fix 2; before it, the line was `uuuuuuuuuuuuuuname -n`) |
| W5 | `POST /vm/stop`: no `lydiabox_*` nftables table (so no DNAT rule), no `lbx*` iptables rule, no TAP device, no `cloud-hypervisor` or `virtiofsd` process | pass |

### Bugs found and fixed (full desktop image)

1. The Xfce session had no session bus. `supervisord.conf` started it with
   `dbus-launch --exit-with-session`, which stops the bus when its stdin
   closes; supervisord gives programs no stdin. `xfce4-session` showed
   "Unable to contact settings server", and `xfwm4`, `xfce4-panel`, and
   `xfdesktop` did not start, so the desktop was black. The `desktop-slim`
   image and the docker driver use the same file. The session now starts
   with `dbus-run-session -- /usr/bin/startxfce4`.
2. Takeover typing added characters: a browser `keyup` that arrived late
   let the X server autorepeat the key. The browser repeats a held key
   itself, so the guest turns off X server autorepeat before each injected
   key press (`internal/guest/x11/xgb.go`).
3. `browser_cdp_endpoint` started Chrome again on each call that found no
   CDP answer. On this slow guest, Chrome needs much more than the 30 s
   wait of one call, so each retry added Chrome processes (guest load
   average 12.9 with 2 vCPUs), and the first Chrome did not open its CDP
   server in 21 minutes. The tool now starts Chrome only when no live
   process holds the profile lock (`SingletonLock`), and otherwise waits
   for the running Chrome (`TestCDPEndpointWaitsForStartingChrome`).

Open items:

- Under nested virtualization with 2 vCPUs, Chromium takes minutes to
  open its CDP port: 173 s on an idle Mac (2026-11-11), 14 minutes on the
  loaded Mac of the first run. On a usual host it starts in seconds.
  `browser_cdp_endpoint` waits 30 s for a Chrome; when Chrome is slower,
  the call returns an error, and a later call returns the endpoint of the
  same Chrome.
- The fixed 120 s boot limit (see the open items above) also stopped
  wakes on this loaded host.
- Not tested here, both waived by the user: GPU passthrough (checklist in
  [kvm-test.md](kvm-test.md#10-gpu-passthrough-vfio)), and the Google
  Drive, Notion, and Slack connectors against the real providers (GitHub
  passed: [connector run](#connector-run-github-real-provider)). The VFIO
  `devices` payload is covered by `TestBuildConfigPayload`, and the OAuth
  code paths by the fake-provider tests `TestPKCEAuthorizeCallback`,
  `TestAuthorizeValidation`, `TestClientSources`, `TestBindToken`,
  `TestRefreshOnExpirySingleFlight`, `TestDisconnectRevokes`,
  `TestGitAccount`, `TestGitCred`, `TestMCPProtocol`, `TestGitHubTools`,
  `TestGDriveTools`, `TestNotionTools`, and `TestSlackTools`.

## Integration run (chv on KVM, amd64)

Date: 2026-11-11. Host: Google Cloud `n2-standard-8` (8 vCPUs, 32 GiB,
100 GiB `pd-balanced`) in `us-central1-a` with nested virtualization and
the minimum CPU platform Intel Cascade Lake (`Intel(R) Xeon(R) CPU @
2.80GHz`, model 85, VT-x). Ubuntu 24.04.5, kernel `7.0.0-1017-gcp`.
`/dev/kvm` is present, and `/proc/cpuinfo` shows `vmx` for each CPU. The
guest ran with Cloud Hypervisor 54.0 and virtiofsd 1.14.0 (both downloaded
by `lydiabox up`, the upstream amd64 binaries), the `ch-6.12.8` kernel,
and the full `desktop` image, with `LYDIABOX_VM_CPUS=4`,
`LYDIABOX_VM_MEM_MB=4096`, and `LYDIABOX_IDLE_TIMEOUT=60s`. The binaries
came from `make release` on the Mac (`linux/amd64`). `lydiabox up`
listened on `127.0.0.1:8080` only, with a random token; MinIO ran in
Docker on the host. The procedure is
[kvm-test.md](kvm-test.md#9-amd64-kvm-host) (steps 2 to 8).

Build on the host (`build-artifacts.sh` without `--slim` and without
`--virtiofsd`, no build cache): 667 s in total. Kernel compile
(`bzImage`) 429 s, `desktop` packages 144 s, Node.js 30 s, Google Chrome
30 s, the five harness installers 116 s, copy of the root 45 s,
`mksquashfs` 230 s (the stages run in parallel). The kernel rebuild after
fix 1 took 373 s (the root came from the cache). Sizes: `vmlinux`
(`arch/x86/boot/compressed/vmlinux.bin`) 46 MiB, `initramfs.cpio.gz`
1 MiB, `rootfs.squashfs` 1,377,427,456 bytes (1314 MiB).

Wake time (`POST /vm/wake` from `STOPPED` to `RUNNING`): 3.7 s with an
empty root overlay, then 3.7 s, 4.1 s, and 3.7 s, all with 4 vCPUs. This
is well inside the 120 s boot limit. Resume from `PAUSED`: 4 ms.

| # | Check | Result |
| :--- | :--- | :--- |
| 1 | `POST /vm/wake`: `RUNNING` in 3.7 s, `"driver":"chv"`, 4 CPUs, 4096 MiB | pass (after fix 1; before it, the initramfs could not mount `rootfs.squashfs` and the wake stopped at the 120 s limit) |
| 2 | `uname -a`: `Linux lydiabox 6.12.8+ ... x86_64 GNU/Linux`; `id`: `uid=1000(agent)` | pass |
| 3 | `overlay on /` (lower `/mnt/ro`, upper `/mnt/rw/upper`), `lydia_home on /home/agent type virtiofs`, `lydia_workspace on /workspace type virtiofs` | pass |
| 4 | `grep -c processor /proc/cpuinfo`: 4; the guest shows the host CPU model; 3929 MiB total memory | pass |
| 5 | `echo hi >/workspace/a.txt` in the guest; `GET /fs/workspace/text?path=a.txt` gives `hi` | pass |
| 6 | `PUT /fs/workspace/text?path=b.txt`; `cat /workspace/b.txt` in the guest gives `x` | pass |
| 7 | `curl -sI https://example.com`: `HTTP/2 200` | pass |
| 8 | The host VPC address `10.128.0.2:8080`, `172.30.0.1:8080`, `172.17.0.1:9000`, the VPC gateway `10.128.0.1`, and the GCP metadata server `169.254.169.254` time out (curl exit 28 after 5 s) | pass |
| 9 | `curl http://127.0.0.1:3425/v1/models` (vsock 9002 to magpie): 20 of 20 requests give 200, `{"data":[],...,"object":"list"}` | pass |
| 10 | `lydiabox-guest git-cred get`: no output, exit 0 (broker 404). With `LYDIABOX_HOST_ADDR=vsock://2:9555`: `connection reset by peer`, exit 1 | pass |
| 11 | `sudo tee /etc/lydiabox-kvm.txt`, `POST /vm/stop` (0.2 s), `POST /vm/wake` (3.65 s): the file has `x` | pass |
| 12 | `POST /vm/hibernate`: `PAUSED`; `vm.info` gives `"state":"Paused"` | pass |
| 13 | `POST /vm/wake`: `RUNNING` in 4 ms; the same shell session still has its variable | pass |
| 14 | `PUT /desktop/mode` `TAKEOVER` and `OBSERVER`: 200; `Xvfb :0 -screen 0 1920x1080x24` runs | pass |
| 15 | `DELETE /sessions/{id}` for the live session (5 s); `PAUSED` 60 s after the last lease | pass |
| 16 | `POST /vm/stop`: `STOPPED` in 0.4 s; no `cloud-hypervisor` or `virtiofsd` process, no `lydiabox_*` nftables table, no `lbx*` TAP device, no `lbx*` iptables `FORWARD` rule. `SIGTERM` to `lydiabox up`: "shutdown complete", JuiceFS unmounted | pass |

amd64 differences, as expected: check 2 shows `x86_64`; `serial.log` has
the kernel and supervisord output on `ttyS0`; `vm.info` shows the same
`vm.create` payload as on arm64 except `cmdline`, which is
`console=ttyS0 root=/dev/vda ro quiet` (the design text): three `Raw`
disks (`rootfs.squashfs` and the guest binary disk read-only,
`root-upper.img` writable), two virtio-fs devices with `num_queues` 1 and
`queue_size` 1024, the TAP device with `172.30.0.1/24`, vsock CID 3, the
serial port to a file, and the console off.

Full desktop image (step 7):

| # | Check | Result |
| :--- | :--- | :--- |
| F1 | First `POST /vm/wake` with an empty root overlay: `RUNNING` in 3.7 s, `"driver":"chv"` | pass |
| F2 | `Xvfb :0 -screen 0 1920x1080x24`, `dbus-run-session -- /usr/bin/startxfce4`, `xfwm4`, `xfce4-panel`, `xfdesktop`, `lydiabox-guest serve` run about 20 s after the wake; `xdpyinfo`: `1920x1080` | pass |
| F3 | `tools/list`: 9 tools | pass |
| F4 | `computer_screenshot`: `image/png`, 1920x1080; the image shows the Xfce desktop with the panel | pass |
| F5 | `desktop_launch_app` `chrome`: `Launched chrome`; `/usr/bin/google-chrome-stable --remote-debugging-port=9222 --remote-debugging-address=127.0.0.1 --user-data-dir=/home/agent/.config/google-chrome ...` runs (Google Chrome 157.0.8103.50) | pass |
| F6 | `browser_cdp_endpoint`: `ws://127.0.0.1:9222/devtools/browser/...` on the first call, 6.3 s after the launch (1 tool call). `/json/version`: `Chrome/157.0.8103.50` with the same URL; `chrome` listens on `127.0.0.1:9222` only; `Target.createTarget` `about:blank` returned a `targetId` | pass (after fix 2; before it, Chrome ran but never opened port 9222, and 10 calls returned the 30 s timeout error) |
| F7 | `blender_run_script`: `BLENDER 4.0.2`, `Blender quit`, 5.1 s | pass |
| F8 | `desktop_launch_app` `terminal`, `computer_mouse_click` (400, 300), `computer_keyboard` `echo LYDIA$((6*7))KVM` + `Return`: the screenshot shows `LYDIA42KVM` | pass |
| F9 | `claude` 2.1.322 (0.4 s), `codex-cli` 0.167.1 (0.5 s), `dsh` 0.2.2-rc.1 (0.4 s), `agy` 1.5.5 (2.5 s), `opencode` 1.19.5 (1.7 s) | pass |

WebRTC from a browser on the Mac (step 8). The GCP VM has the external
address `203.0.113.10` behind 1:1 NAT. A firewall rule let UDP 50000 in
from the public address of the Mac only (`/32`); no TCP port was opened.
The browser (headless Chrome 157 on macOS, driven over CDP as in the
arm64 run) reached the API through `gcloud compute ssh -L
8080:127.0.0.1:8080`. The guest takes its ICE host candidate from the
host name of the page URL (`desktop.RequestIP`), so the page URL was
`http://lydiabox-gcp.test:8080`: Chrome mapped that name to `127.0.0.1`
(`--host-resolver-rules`, the tunnel), and `/etc/hosts` on the GCP VM
mapped it to the external address. The guest then advertised
`203.0.113.10:50000`.

| # | Check | Result |
| :--- | :--- | :--- |
| W1 | Table `lydiabox_lbx37a8eec1`: `iifname != "lbx37a8eec1" udp dport 50000 dnat to 172.30.0.2`; iptables `FORWARD` (policy `DROP`, Docker): `-o lbx37a8eec1 -p udp --dport 50000 -j ACCEPT` | pass |
| W2 | `videoWidth` 1920, `videoHeight` 1080; first frame 10 s after the page opened (`currentTime` 2.89 s, 72 frames); 60 s later `currentTime` 62.9 s and 1512 frames (about 24 frames per second) | pass |
| W3 | `conntrack`: `src=172.30.0.2 dst=<Mac public IP> sport=50000 dport=53090 src=<Mac public IP> dst=10.128.0.2 sport=53090 dport=50000 [ASSURED]`. The media used host UDP port 50000. The first packet of this flow came from the guest (an ICE check to the address of the browser behind its NAT), so masquerade made the entry, not the DNAT rule. A separate probe (`nc -u 203.0.113.10 50000` from the Mac while the guest ran) showed the inbound path: `src=<Mac public IP> dst=10.128.0.2 dport=50000 ... src=172.30.0.2 sport=50000` | pass |
| W4 | Takeover button: mode `TAKEOVER`; a click at guest (400, 300) in the `xfce4-terminal` window, then `uname -n` + Enter: the guest screenshot shows `uname -n` and `lydiabox`, each character once | pass |
| W5 | `POST /vm/stop`: no `lydiabox_*` table (no DNAT rule), no `lbx*` iptables rule, no TAP device | pass |

The VM ran for about 52 minutes (created 05:07 UTC, deleted 05:59 UTC);
it and the firewall rule were deleted after the run.

### Bugs found and fixed (chv on KVM, amd64)

1. The guest kernel had no SquashFS. The amd64 `ch_defconfig` of the
   Cloud Hypervisor kernel fork sets `# CONFIG_MISC_FILESYSTEMS is not
   set`, and SquashFS is in that menu, so `merge_config.sh` dropped
   `CONFIG_SQUASHFS=y` from `kernel.config` without an error. The
   initramfs stopped with "mounting /dev/vda on /mnt/ro failed: No such
   device", and the wake stopped at the 120 s limit. The arm64
   `ch_defconfig` turns the menu on. `kernel.config` now sets
   `CONFIG_MISC_FILESYSTEMS=y`, and the kernel stage of the Dockerfile
   stops when an option of `kernel.config` is not in the final `.config`
   (checked for `x86_64` and `arm64`).
2. Chrome did not open its CDP port. Google Chrome 136 and later ignore
   `--remote-debugging-port` when `--user-data-dir` is the default profile
   directory ("DevTools remote debugging requires a non-default data
   directory"), and `/home/agent/.config/google-chrome` (the design text)
   is the default of Google Chrome. Chromium on arm64 uses
   `~/.config/chromium`, so the arm64 runs did not show this. The guest
   now starts Chrome with `CHROME_CONFIG_HOME=/home/agent/.config/lydiabox-chrome`,
   which moves the default directory away; the profile keeps its path
   (`TestLocalAppsStartsChromeForCDP`).

Open items:

- Chrome shows "Restore pages? Chrome didn't shut down correctly" after a
  VM stop, because `vm.shutdown` stops the guest without a guest
  power-off (see the open items of the arm64 run).
- GPU passthrough is still not tested: the GCP VM has no GPU.

## Connector run (GitHub, real provider)

On 2026-11-11 and 2026-11-14, with [connectors-manual-test.md](connectors-manual-test.md)
on the KVM test VM (`chv` driver, full desktop image, binaries from
`929fa65`; the connector code is the same on `main`). The web UI was
at `http://localhost:8080` through the Lima port forward. The OAuth app
"LydiaBox local test" (owner `portwatcher`, "Expire user access tokens"
on) had the callback `http://localhost:8080/api/v1/connectors/github/callback`.
Its client ID and secret came from `LYDIABOX_GITHUB_CLIENT_ID` and
`LYDIABOX_GITHUB_CLIENT_SECRET`.

| Step | Result |
| :--- | :--- |
| `GET /api/v1/connectors` | pass: GitHub `"oauthConfigured": true`, `"oauthSource": "env"` |
| Connect | pass: the browser goes to `github.com/login/oauth/authorize` with `code_challenge_method=S256`, a `code_challenge`, a `state`, the registered redirect URI, and `scope=repo read:org workflow` |
| Cancel on the consent page | pass: back to `/connectors` with "access was denied on the provider's page"; host log `connector authorization failed` `error=access_denied` |
| Authorize 50 minutes after Connect | pass (negative check): the state lives 10 minutes, so the callback answers `invalid_state` and the page shows "the sign-in link expired"; no account and no audit entry |
| Connect and grant (2026-11-14) | pass: GitHub kept the grant of the earlier Authorize click, so it sent the browser back at once to `/connectors?connected=github`. The card shows account `portwatcher`, OAuth, scopes `read:org repo workflow`, and an expiry 8 hours later (expiring user token); status `ok` |
| Audit and token hiding | pass: one `connector.connected` entry (`authKind` `oauth2`, `source` `env`). `GET /api/v1/connectors/github` has no `token`, `accessToken`, or `refreshToken` field and no `gho_`/`ghu_`/`ghr_` value. The host databases `state/lydiabox.db` and `meta/jfs.db` (with WAL files) contain no GitHub token in clear text |
| `git-cred get` in the guest (`LYDIABOX_HOST_ADDR=vsock://2:9001`) | pass: `username=x-access-token` and a `password=` line; exit 0; two `connector.token.issued` audit entries with consumer `git-cred` |
| `git ls-remote` of the private repository `portwatcher/lydiabox` | pass: `HEAD` and `refs/heads/main` listed without a prompt (`GIT_TERMINAL_PROMPT=0`) |
| No token in a guest file | pass: `grep -r` for `gh[ousr]_` in `~` and `/workspace` finds nothing |
| Connector MCP in the guest (`127.0.0.1:8911`) | pass: `tools/list` gives the 15 connector tools; `github_list_repos`, `github_list_issues`, `github_get_file_contents` (`README.md`), and `github_create_issue` (issue #1, closed right after the test) succeed; four `connector.mcp.call` audit entries with `ok: true` |
| Host route `POST /mcp/connectors` | pass: `github_list_issues` with the admin token succeeds; without a token the answer is `401` |
| Disconnect | pass: `DELETE /api/v1/connectors/github?account=portwatcher` answers `204`; the card shows "Not connected"; audit `connector.disconnected` with `revoked: true`; "LydiaBox local test" is not in the 22 authorized OAuth apps at GitHub (Settings, Applications) |
| After Disconnect | pass: `git-cred get` prints nothing; the MCP call answers `isError: true` "connector is not connected: github" |

On 2026-11-14 the user decided that the general OAuth flow, shown end to
end on GitHub, is sufficient, and waived the real-provider check for
Google Drive, Notion, and Slack. These providers ran against fake
providers only (the tests listed in the open items of the full-image
run). No OAuth app was created at Google, Notion, or Slack.

Finding: step 1 of the checklist told the tester to set a new random
`LYDIABOX_MASTER_KEY`. On a data directory with stored secrets, the new
key overrides `state/master.key`, and `lydiabox up` stopped with
`store: decrypt magpie secret web-key: ... decryption failed`. The
checklist now says to keep the key that `lydiabox up` writes on its first
start.

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

The Go gates (build, vet, `go test -race` with 30 packages, both Linux
cross builds, `make lint`, `make build`) were run again after the fixes of
the full-image run, and again after the fixes of the amd64 run: all pass.

No Go, TypeScript, script, HTML, or CSS source file has more than 600
lines (largest: `internal/vmm/controller.go`, 477 lines).

### After the `instabox` driver (2026-11-15)

On macOS arm64 with Go 1.24.1 and Node.js 24:

| Command | Result |
| :--- | :--- |
| `CGO_ENABLED=0 go build ./...`, `go vet ./...` | pass, no findings |
| `CGO_ENABLED=0 go test -race -count=1 ./...` | pass, 41 packages with tests (new: `internal/vmm/instabox`); `internal/cli`, `internal/vmm/...`, and `internal/instabox/...` also three times in a row |
| `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build ./...` and `arm64` | pass |
| `make lint` (vet, gofmt, staticcheck, shellcheck) | pass, no findings |
| `make build` | pass |
| `cd web && npm ci && npm run typecheck && npm run lint && npm run test && npm run build` | pass, 53 tests, no lint warnings |

Largest source file: `internal/vmm/controller.go`, 504 lines.
