// Types mirror docs/api.md exactly. Keep field names and unions in sync with
// that document; it is the contract between the host and this UI.

/** Error envelope: `{ "error": { "code", "message" } }` (api.md section 1). */
export type ErrorCode =
  | "bad_request"
  | "unauthorized"
  | "forbidden"
  | "not_found"
  | "conflict"
  | "too_large"
  | "unsupported_media_type"
  | "rate_limited"
  | "internal"
  | "not_implemented"
  | "unavailable";

export interface ErrorEnvelope {
  error: { code: ErrorCode | string; message: string };
}

// Section 2: authentication.
export interface AuthState {
  authenticated: boolean;
}

// Section 3: health and status.
export type VMState = "STOPPED" | "BOOTING" | "RUNNING" | "PAUSED";
export type VMDriver = "chv" | "docker";

export interface VersionInfo {
  version: string;
  commit: string;
  buildDate: string;
  goVersion: string;
  platform: string;
}

export interface StorageStatus {
  tenantId: string;
  mounted: boolean;
  lastSyncAt: string;
  lastUploadAt: string;
  lastSyncError?: string;
  /** Virtual S3 gateway (api.md section 3); absent on older servers. */
  s3Gateway?: S3GatewayStatus;
}

export interface S3GatewayStatus {
  enabled: boolean;
  /** Backend listen address, for example `127.0.0.1:8090`. */
  addr: string;
  /** Public path on this server, `/s3`: the S3 endpoint is the origin + this path. */
  path: string;
  bucket: string;
  running: boolean;
}

export interface Status {
  version: VersionInfo;
  tenantId: string;
  vm: { state: VMState; driver: VMDriver | string };
  storage: StorageStatus;
  time: string;
}

// Section 4: File Manager.
export type RootName = "workspace" | "home";
export type EntryType = "file" | "dir" | "symlink" | "other";

export interface Entry {
  name: string;
  path: string;
  type: EntryType;
  size: number;
  mode: string;
  modTime: string;
}

export interface FsRoot {
  name: RootName;
  guestPath: string;
}

export interface RootsResponse {
  roots: FsRoot[];
}

export interface ListResponse {
  root: RootName;
  dir: Entry;
  entries: Entry[];
}

export interface TextFile extends Entry {
  content: string;
}

export interface SaveTextRequest {
  content: string;
  baseModTime?: string;
}

export interface UploadedEntry extends Entry {
  replaced: boolean;
}

export interface UploadResponse {
  files: UploadedEntry[];
}

export interface MkdirRequest {
  path: string;
  parents: boolean;
}

export interface RenameRequest {
  from: string;
  to: string;
  overwrite: boolean;
}

// Section 7: VM power.
export interface VMStatus {
  state: VMState;
  driver: VMDriver | string;
  cpus: number;
  memMb: number;
  gpu?: string;
  bootedAt: string;
  leases: { pty: number; desktop: number };
  idleTimeoutSeconds: number;
  idleSince: string;
  lastError?: string;
}

// Section 8: sessions and PTY.
export const HARNESSES = ["claude-code", "codex", "dsh", "antigravity", "opencode"] as const;
export type AgentHarness = (typeof HARNESSES)[number];
export type HarnessID = AgentHarness | "shell";
export const SESSION_HARNESSES: readonly HarnessID[] = [...HARNESSES, "shell"];

export type SessionStatus = "running" | "idle" | "exited";

export interface Usage {
  inputTokens: number;
  outputTokens: number;
  costUsd: number;
}

export interface Session {
  id: string;
  harness: HarnessID;
  nativeId: string;
  title: string;
  workspaceDir: string;
  model: string;
  status: SessionStatus;
  live: boolean;
  createdAt: string;
  updatedAt: string;
  usage?: Usage;
}

export interface SessionsResponse {
  sessions: Session[];
}

export interface CreateSessionRequest {
  harness: HarnessID;
  workspaceDir: string;
  model?: string;
  resumeNativeId?: string;
  title?: string;
  cols: number;
  rows: number;
}

export type PtySignal = "SIGINT" | "SIGTERM" | "SIGHUP" | "SIGQUIT";

export type PtyClientMessage =
  | { type: "resize"; cols: number; rows: number }
  | { type: "signal"; signal: PtySignal }
  | { type: "ping"; id: number };

export type PtyServerMessage =
  | { type: "hello"; sessionId: string; replayBytes: number; cols: number; rows: number }
  | { type: "resized"; cols: number; rows: number }
  | { type: "pong"; id: number }
  | { type: "exit"; code: number; signal: string }
  | { type: "error"; code: string; message: string };

// Section 9: desktop.
export type DesktopMode = "OBSERVER" | "TAKEOVER";
export type ChangedBy = "user" | "agent" | "system";

export interface DesktopModeState {
  mode: DesktopMode;
  changedAt: string;
  changedBy: ChangedBy;
}

export type AgentActionKind = "move" | "click" | "drag" | "scroll" | "type" | "key" | "screenshot" | "launch";
export type MouseButtonName = "left" | "middle" | "right";
export type LaunchApp = "chrome" | "blender" | "terminal";

export interface IceCandidate {
  candidate: string;
  sdpMid: string | null;
  sdpMLineIndex: number | null;
  usernameFragment?: string | null;
}

export interface IceServer {
  urls: string[] | string;
  username?: string;
  credential?: string;
}

export interface AgentAction {
  type: "agent_action";
  action: AgentActionKind;
  x: number;
  y: number;
  fromX?: number;
  fromY?: number;
  button: MouseButtonName | "";
  double: boolean;
  text: string;
  keys: string;
  dx: number;
  dy: number;
  tool: string;
  timestamp: string;
}

export type DesktopServerMessage =
  | {
      type: "hello";
      mode: DesktopMode;
      screen: { width: number; height: number };
      iceServers: IceServer[];
      iceTransportPolicy: RTCIceTransportPolicy;
    }
  | { type: "answer"; sdp: string }
  | { type: "ice"; candidate: IceCandidate }
  | { type: "mode"; mode: DesktopMode; changedBy: ChangedBy }
  | { type: "viewers"; count: number }
  | AgentAction
  | { type: "pong"; id: number }
  | { type: "error"; code: string; message: string };

export type DesktopClientMessage =
  | { type: "offer"; sdp: string }
  | { type: "ice"; candidate: IceCandidate }
  | { type: "mode"; mode: DesktopMode }
  | { type: "launch"; app: LaunchApp }
  | { type: "ping"; id: number };

/** Data channel `input` messages (browser to guest). */
export type InputMessage =
  | { t: "move"; x: number; y: number }
  | { t: "down"; button: MouseButtonName }
  | { t: "up"; button: MouseButtonName }
  | { t: "wheel"; dx: number; dy: number }
  | { t: "keydown"; code: string; key: string }
  | { t: "keyup"; code: string; key: string }
  | { t: "text"; text: string };

// Section 10: models and magpie.
export type GatewayState = "running" | "starting" | "stopped" | "error";

export interface GatewayStatus {
  status: GatewayState;
  addr: string;
  version: string;
}

export interface Provider {
  id: string;
  kind: string;
  name: string;
  baseUrl: string;
  enabled: boolean;
  hasKey: boolean;
  models: string[];
  status: string;
  lastError: string;
}

export interface CreateProviderRequest {
  kind: string;
  name: string;
  baseUrl: string;
  apiKey: string;
}

export interface UpdateProviderRequest {
  name?: string;
  baseUrl?: string;
  apiKey?: string;
  enabled?: boolean;
}

export interface Model {
  id: string;
  provider: string;
  name: string;
  contextWindow: number;
}

export type RoutingStrategy = "fallback" | "round-robin";

export interface RoutingGroup {
  name: string;
  strategy: RoutingStrategy;
  targets: string[];
}

export interface Binding {
  harness: AgentHarness;
  model: string;
  configPath: string;
  updatedAt: string;
}

// Section 11: connectors.
export const CONNECTOR_PROVIDERS = ["github", "gdrive", "notion", "slack"] as const;
export type ConnectorProvider = (typeof CONNECTOR_PROVIDERS)[number];
export type OAuthSource = "env" | "stored" | "relay" | "none";
export type AccountStatus = "ok" | "expired" | "revoked";

export interface ConnectorAccount {
  accountLabel: string;
  authKind: string;
  scopes: string[];
  expiresAt: string;
  createdAt: string;
  updatedAt: string;
  status: AccountStatus;
}

export interface Connector {
  provider: ConnectorProvider;
  displayName: string;
  oauthConfigured: boolean;
  oauthSource: OAuthSource;
  supportsOAuth: boolean;
  supportsToken: boolean;
  defaultScopes: string[];
  accounts: ConnectorAccount[];
}

export interface AuthorizeResponse {
  authorizeUrl: string;
  state: string;
}

export interface TokenRequest {
  token: string;
  accountLabel: string;
  scopes: string[];
}

export interface AuditEntry {
  id: number;
  kind: string;
  detail: string;
  createdAt: string;
}

export interface AuditPage {
  entries: AuditEntry[];
  nextBefore?: number;
}
