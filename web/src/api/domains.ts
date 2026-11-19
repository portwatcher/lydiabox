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
  Subscription,
  SubscriptionKind,
  SubscriptionLogin,
  SessionsResponse,
  TokenRequest,
  Transcript,
  UpdateProviderRequest,
} from "./types";

// Section 8: sessions.
export const sessions = {
  list: (filter: { harness?: HarnessID | ""; workspace?: string }, signal?: AbortSignal) =>
    http.get<SessionsResponse>("/api/v1/sessions", { harness: filter.harness, workspace: filter.workspace }, signal),
  create: (body: CreateSessionRequest) => http.post<Session>("/api/v1/sessions", body),
  transcript: (id: string, signal?: AbortSignal) => http.get<Transcript>(`/api/v1/sessions/${seg(id)}/transcript`, undefined, signal),
  setModel: (id: string, body: { model: string; cols: number; rows: number }) => http.patch<Session>(`/api/v1/sessions/${seg(id)}`, body),
  remove: (id: string, purge = false) => http.delete(`/api/v1/sessions/${seg(id)}`, { purge: purge || undefined }),
  wsPath: (id: string) => `/api/v1/sessions/${seg(id)}/ws`,
  eventsPath: (id: string) => `/api/v1/sessions/${seg(id)}/events`,
  input: (id: string, text: string) => http.post<undefined>(`/api/v1/sessions/${seg(id)}/input`, { text }),
  interrupt: (id: string) => http.post<undefined>(`/api/v1/sessions/${seg(id)}/interrupt`, {}),
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
  subscriptions: (signal?: AbortSignal) =>
    http.get<{ subscriptions: Subscription[] }>("/api/v1/models/subscriptions", undefined, signal),
  startLogin: (kind: SubscriptionKind, setupToken?: string) =>
    http.post<SubscriptionLogin>("/api/v1/models/subscriptions/logins", { kind, setupToken }),
  login: (id: string, signal?: AbortSignal) =>
    http.get<SubscriptionLogin>(`/api/v1/models/subscriptions/logins/${seg(id)}`, undefined, signal),
  submitLoginCode: (id: string, code: string) =>
    http.post<SubscriptionLogin>(`/api/v1/models/subscriptions/logins/${seg(id)}/code`, { code }),
  cancelLogin: (id: string) => http.delete(`/api/v1/models/subscriptions/logins/${seg(id)}`),
  removeAccount: (kind: SubscriptionKind, account: string) =>
    http.delete(`/api/v1/models/subscriptions/${seg(kind)}`, { account }),
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
