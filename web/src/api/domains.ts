import { http, seg } from "./http";
import type {
  AgentHarness,
  AuditPage,
  AuthorizeResponse,
  Binding,
  Connector,
  ConnectorProvider,
  CreateProviderRequest,
  CreateSessionRequest,
  DesktopMode,
  DesktopModeState,
  GatewayStatus,
  HarnessID,
  Model,
  Provider,
  RoutingGroup,
  Session,
  SessionsResponse,
  SwitchPreview,
  SwitchRequest,
  SwitchResult,
  TokenRequest,
  UpdateProviderRequest,
} from "./types";

// Section 8: sessions.
export const sessions = {
  list: (filter: { harness?: HarnessID | ""; workspace?: string }, signal?: AbortSignal) =>
    http.get<SessionsResponse>("/api/v1/sessions", { harness: filter.harness, workspace: filter.workspace }, signal),
  create: (body: CreateSessionRequest) => http.post<Session>("/api/v1/sessions", body),
  remove: (id: string, purge = false) => http.delete(`/api/v1/sessions/${seg(id)}`, { purge: purge || undefined }),
  preview: (body: Omit<SwitchRequest, "dryRun">) =>
    http.post<SwitchPreview>("/api/v1/sessions/switch", { ...body, dryRun: true }),
  switch: (body: Omit<SwitchRequest, "dryRun">) =>
    http.post<SwitchResult>("/api/v1/sessions/switch", { ...body, dryRun: false }),
  wsPath: (id: string) => `/api/v1/sessions/${seg(id)}/ws`,
};

// Section 9: desktop.
export const desktop = {
  mode: (signal?: AbortSignal) => http.get<DesktopModeState>("/api/v1/desktop/mode", undefined, signal),
  setMode: (mode: DesktopMode) => http.put<DesktopModeState>("/api/v1/desktop/mode", { mode }),
  wsPath: "/api/v1/desktop/ws",
};

// Section 10: models.
export const models = {
  gateway: (signal?: AbortSignal) => http.get<GatewayStatus>("/api/v1/models/gateway", undefined, signal),
  providers: (signal?: AbortSignal) =>
    http.get<{ providers: Provider[] }>("/api/v1/models/providers", undefined, signal),
  createProvider: (body: CreateProviderRequest) => http.post<Provider>("/api/v1/models/providers", body),
  updateProvider: (id: string, body: UpdateProviderRequest) =>
    http.patch<Provider>(`/api/v1/models/providers/${seg(id)}`, body),
  deleteProvider: (id: string) => http.delete(`/api/v1/models/providers/${seg(id)}`),
  list: (signal?: AbortSignal) => http.get<{ models: Model[] }>("/api/v1/models", undefined, signal),
  routing: (signal?: AbortSignal) => http.get<{ groups: RoutingGroup[] }>("/api/v1/models/routing", undefined, signal),
  saveRouting: (groups: RoutingGroup[]) => http.put<{ groups: RoutingGroup[] }>("/api/v1/models/routing", { groups }),
  bindings: (signal?: AbortSignal) => http.get<{ bindings: Binding[] }>("/api/v1/models/bindings", undefined, signal),
  bind: (harness: AgentHarness, model: string) =>
    http.put<Binding>(`/api/v1/models/bindings/${seg(harness)}`, { model }),
};

// Section 11: connectors.
export const connectors = {
  list: (signal?: AbortSignal) => http.get<{ connectors: Connector[] }>("/api/v1/connectors", undefined, signal),
  get: (provider: ConnectorProvider) => http.get<Connector>(`/api/v1/connectors/${seg(provider)}`),
  setClient: (provider: ConnectorProvider, clientId: string, clientSecret: string) =>
    http.put<Connector>(`/api/v1/connectors/${seg(provider)}/client`, { clientId, clientSecret }),
  authorize: (provider: ConnectorProvider, body: { scopes?: string[]; returnTo?: string }) =>
    http.post<AuthorizeResponse>(`/api/v1/connectors/${seg(provider)}/authorize`, body),
  pasteToken: (provider: ConnectorProvider, body: TokenRequest) =>
    http.post<Connector>(`/api/v1/connectors/${seg(provider)}/token`, body),
  disconnect: (provider: ConnectorProvider, account: string) =>
    http.delete(`/api/v1/connectors/${seg(provider)}`, { account }),
  audit: (query: { provider?: ConnectorProvider | ""; before?: number; limit?: number }, signal?: AbortSignal) =>
    http.get<AuditPage>(
      "/api/v1/connectors/audit",
      { provider: query.provider, before: query.before, limit: query.limit },
      signal,
    ),
};
