import { bool, isRecord, num, oneOf, str } from "./decode";
import type { Json } from "./decode";
import { sessions } from "./domains";
import { ReconnectingSocket, wsUrl } from "./socket";
import type { SocketState, WebSocketFactory } from "./socket";

/** WebSocket subprotocol of the session event stream (docs/api.md section 8). */
export const EVENTS_PROTOCOL = "lydiabox.events.v1";

export const EVENT_KINDS = ["user", "assistant", "reasoning", "tool_call", "tool_result", "plan", "usage", "turn_end", "error", "permission"] as const;
export type EventKind = (typeof EVENT_KINDS)[number];

export const TOOL_KINDS = ["shell", "read", "edit", "search", "web", "mcp", "computer", "browser", "plan", "agent", "other"] as const;
export type ToolKind = (typeof TOOL_KINDS)[number];

export type ResultStatus = "ok" | "error" | "pending";
export type StepStatus = "pending" | "in_progress" | "completed";
/** "setup": before the first message, the terminal shows a first-run screen (terms, folder trust) that only the user can answer. */
export type AgentState = "working" | "idle" | "attention" | "setup" | "offline";

export interface ToolCall {
  id: string;
  name: string;
  kind: ToolKind;
  summary: string;
  input: string;
  path: string;
  diff: string;
}

export interface ToolResult {
  callId: string;
  status: ResultStatus;
  output: string;
  /** data: URLs (screenshots). */
  images: string[];
  durationMs: number;
}

export interface PlanStep {
  text: string;
  status: StepStatus;
}

export interface TurnUsage {
  inputTokens: number;
  outputTokens: number;
  cachedTokens: number;
  reasoningTokens: number;
  contextTokens: number;
  contextWindow: number;
  costUsd: number;
}

export interface PermissionOption {
  id: string;
  name: string;
  /** allow_once, allow_always, reject_once, or reject_always. */
  kind: string;
}

/** A request of a chat (ACP) agent to run a tool. */
export interface Permission {
  id: string;
  title: string;
  toolCallId: string;
  options: PermissionOption[];
  /** The chosen option ID, "cancelled", or "" while it waits. */
  outcome: string;
}

export interface AgentEvent {
  key: string;
  kind: EventKind;
  time: string;
  text: string;
  model: string;
  tool?: ToolCall;
  result?: ToolResult;
  plan?: PlanStep[];
  usage?: TurnUsage;
  permission?: Permission;
}

export interface EventStatus {
  sessionId: string;
  state: AgentState;
  live: boolean;
  synced: boolean;
  prompt: string;
  error: string;
  /** "acp" while a chat agent runs the session. */
  engine: string;
  agent: string;
  mode: string;
  queued: number;
}

export type EventsMessage =
  | { type: "events"; reset: boolean; events: AgentEvent[] }
  | ({ type: "status" } & EventStatus)
  | { type: "error"; code: string; message: string }
  | { type: "pong" };

function record(value: unknown): Json {
  return isRecord(value) ? value : {};
}

function decodeTool(value: unknown): ToolCall | undefined {
  if (!isRecord(value)) {
    return undefined;
  }
  return {
    id: str(value, "id"),
    name: str(value, "name"),
    kind: oneOf(value.kind, TOOL_KINDS) ?? "other",
    summary: str(value, "summary"),
    input: str(value, "input"),
    path: str(value, "path"),
    diff: str(value, "diff"),
  };
}

function decodeResult(value: unknown): ToolResult | undefined {
  if (!isRecord(value)) {
    return undefined;
  }
  const images = Array.isArray(value.images) ? value.images.filter((i): i is string => typeof i === "string" && i.startsWith("data:image/")) : [];
  return {
    callId: str(value, "callId"),
    status: oneOf(value.status, ["ok", "error", "pending"] as const) ?? "ok",
    output: str(value, "output"),
    images,
    durationMs: num(value, "durationMs"),
  };
}

function decodeUsage(value: unknown): TurnUsage | undefined {
  if (!isRecord(value)) {
    return undefined;
  }
  return {
    inputTokens: num(value, "inputTokens"),
    outputTokens: num(value, "outputTokens"),
    cachedTokens: num(value, "cachedTokens"),
    reasoningTokens: num(value, "reasoningTokens"),
    contextTokens: num(value, "contextTokens"),
    contextWindow: num(value, "contextWindow"),
    costUsd: num(value, "costUsd"),
  };
}

function decodePermission(value: unknown): Permission | undefined {
  if (!isRecord(value)) {
    return undefined;
  }
  const options = Array.isArray(value.options)
    ? value.options.map((o) => record(o)).map((o) => ({ id: str(o, "id"), name: str(o, "name"), kind: str(o, "kind") }))
    : [];
  return { id: str(value, "id"), title: str(value, "title"), toolCallId: str(value, "toolCallId"), options, outcome: str(value, "outcome") };
}

/** Decodes one event; events with an unknown kind or no key give null. */
export function decodeEvent(value: unknown): AgentEvent | null {
  if (!isRecord(value)) {
    return null;
  }
  const kind = oneOf(value.kind, EVENT_KINDS);
  const key = str(value, "key");
  if (!kind || !key) {
    return null;
  }
  const plan = Array.isArray(value.plan)
    ? value.plan.map((s) => record(s)).map((s) => ({ text: str(s, "text"), status: oneOf(s.status, ["pending", "in_progress", "completed"] as const) ?? "pending" }))
    : undefined;
  return {
    key,
    kind,
    time: str(value, "time"),
    text: str(value, "text"),
    model: str(value, "model"),
    tool: decodeTool(value.tool),
    result: decodeResult(value.result),
    plan,
    usage: decodeUsage(value.usage),
    permission: decodePermission(value.permission),
  };
}

/** Decodes one `lydiabox.events.v1` text frame; unknown frames give null. */
export function parseEventsMessage(message: unknown): EventsMessage | null {
  if (!isRecord(message)) {
    return null;
  }
  switch (message.type) {
    case "events": {
      const list = Array.isArray(message.events) ? message.events : [];
      const events = list.map(decodeEvent).filter((e): e is AgentEvent => e !== null);
      return { type: "events", reset: bool(message, "reset"), events };
    }
    case "status":
      return {
        type: "status",
        sessionId: str(message, "sessionId"),
        state: oneOf(message.state, ["working", "idle", "attention", "setup", "offline"] as const) ?? "offline",
        live: bool(message, "live"),
        synced: bool(message, "synced"),
        prompt: str(message, "prompt"),
        error: str(message, "error"),
        engine: str(message, "engine"),
        agent: str(message, "agent"),
        mode: str(message, "mode"),
        queued: num(message, "queued"),
      };
    case "error":
      return { type: "error", code: str(message, "code"), message: str(message, "message") };
    case "pong":
      return { type: "pong" };
    default:
      return null;
  }
}

export interface EventsHandlers {
  onEvents: (reset: boolean, events: AgentEvent[]) => void;
  onStatus: (status: EventStatus) => void;
  onError?: (code: string, message: string) => void;
  onState?: (state: SocketState) => void;
}

export interface EventsClientOptions {
  factory?: WebSocketFactory;
  pingIntervalMs?: number;
  url?: (path: string) => string;
}

/**
 * Event stream client. Every (re)connect starts with a reset that holds all
 * events read so far, so a reconnect needs no cursor. A `not_found` or
 * `bad_request` error ends the stream.
 */
export class EventsClient {
  private readonly socket: ReconnectingSocket;

  constructor(sessionId: string, private readonly handlers: EventsHandlers, options: EventsClientOptions = {}) {
    const toUrl = options.url ?? ((path: string) => wsUrl(path));
    this.socket = new ReconnectingSocket({
      url: () => toUrl(sessions.eventsPath(sessionId)),
      protocol: EVENTS_PROTOCOL,
      factory: options.factory,
      pingIntervalMs: options.pingIntervalMs,
      onJson: (raw) => this.dispatch(raw),
      onState: (state) => this.handlers.onState?.(state),
    });
  }

  connect(): void {
    this.socket.start();
  }

  close(): void {
    this.socket.stop();
  }

  private dispatch(raw: unknown): void {
    const msg = parseEventsMessage(raw);
    if (!msg) {
      return;
    }
    switch (msg.type) {
      case "events":
        this.handlers.onEvents(msg.reset, msg.events);
        return;
      case "status":
        this.handlers.onStatus({
          sessionId: msg.sessionId,
          state: msg.state,
          live: msg.live,
          synced: msg.synced,
          prompt: msg.prompt,
          error: msg.error,
          engine: msg.engine,
          agent: msg.agent,
          mode: msg.mode,
          queued: msg.queued,
        });
        return;
      case "error":
        if (msg.code === "not_found" || msg.code === "bad_request") {
          this.socket.finish();
        }
        this.handlers.onError?.(msg.code, msg.message);
        return;
      default:
        return;
    }
  }
}
