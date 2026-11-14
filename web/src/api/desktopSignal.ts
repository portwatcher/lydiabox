import { bool, isRecord, num, oneOf, str } from "./decode";
import type { Json } from "./decode";
import { desktop } from "./domains";
import { ReconnectingSocket, wsUrl } from "./socket";
import type { SocketState, WebSocketFactory } from "./socket";
import type {
  AgentAction,
  AgentActionKind,
  ChangedBy,
  DesktopClientMessage,
  DesktopMode,
  DesktopServerMessage,
  IceCandidate,
  IceServer,
  MouseButtonName,
} from "./types";

export const DESKTOP_PROTOCOL = "lydiabox.desktop.v1";

const MODES: readonly DesktopMode[] = ["OBSERVER", "TAKEOVER"];
const CHANGED_BY: readonly ChangedBy[] = ["user", "agent", "system"];
const ACTIONS: readonly AgentActionKind[] = ["move", "click", "drag", "scroll", "type", "key", "screenshot", "launch"];
const BUTTONS: readonly MouseButtonName[] = ["left", "middle", "right"];

function decodeCandidate(value: unknown): IceCandidate | null {
  if (!isRecord(value)) {
    return null;
  }
  const mid = value.sdpMid;
  const index = value.sdpMLineIndex;
  return {
    candidate: str(value, "candidate"),
    sdpMid: typeof mid === "string" ? mid : null,
    sdpMLineIndex: typeof index === "number" ? index : null,
  };
}

function decodeIceServers(value: unknown): IceServer[] {
  if (!Array.isArray(value)) {
    return [];
  }
  const servers: IceServer[] = [];
  for (const item of value) {
    if (!isRecord(item)) {
      continue;
    }
    const urls = item.urls;
    if (typeof urls === "string" || (Array.isArray(urls) && urls.every((u) => typeof u === "string"))) {
      const server: IceServer = { urls };
      if (typeof item.username === "string") {
        server.username = item.username;
      }
      if (typeof item.credential === "string") {
        server.credential = item.credential;
      }
      servers.push(server);
    }
  }
  return servers;
}

function decodeAgentAction(m: Json): AgentAction | null {
  const action = oneOf(m.action, ACTIONS);
  if (!action) {
    return null;
  }
  const decoded: AgentAction = {
    type: "agent_action",
    action,
    x: num(m, "x"),
    y: num(m, "y"),
    button: oneOf(m.button, BUTTONS) ?? "",
    double: bool(m, "double"),
    text: str(m, "text"),
    keys: str(m, "keys"),
    dx: num(m, "dx"),
    dy: num(m, "dy"),
    tool: str(m, "tool"),
    timestamp: str(m, "timestamp"),
  };
  if (typeof m.fromX === "number") {
    decoded.fromX = m.fromX;
  }
  if (typeof m.fromY === "number") {
    decoded.fromY = m.fromY;
  }
  return decoded;
}

/** Decodes one `lydiabox.desktop.v1` frame; unknown or malformed frames give null. */
export function parseDesktopMessage(message: unknown): DesktopServerMessage | null {
  if (!isRecord(message)) {
    return null;
  }
  switch (message.type) {
    case "hello": {
      const mode = oneOf(message.mode, MODES) ?? "OBSERVER";
      const screen = isRecord(message.screen) ? message.screen : {};
      return {
        type: "hello",
        mode,
        screen: { width: num(screen, "width", 1920), height: num(screen, "height", 1080) },
        iceServers: decodeIceServers(message.iceServers),
        iceTransportPolicy: message.iceTransportPolicy === "relay" ? "relay" : "all",
      };
    }
    case "answer":
      return { type: "answer", sdp: str(message, "sdp") };
    case "ice": {
      const candidate = decodeCandidate(message.candidate);
      return candidate ? { type: "ice", candidate } : null;
    }
    case "mode": {
      const mode = oneOf(message.mode, MODES);
      return mode ? { type: "mode", mode, changedBy: oneOf(message.changedBy, CHANGED_BY) ?? "system" } : null;
    }
    case "viewers":
      return { type: "viewers", count: num(message, "count") };
    case "agent_action":
      return decodeAgentAction(message);
    case "pong":
      return { type: "pong", id: num(message, "id") };
    case "error":
      return { type: "error", code: str(message, "code"), message: str(message, "message") };
    default:
      return null;
  }
}

export interface DesktopSignalOptions {
  onMessage: (message: DesktopServerMessage) => void;
  onState?: (state: SocketState) => void;
  factory?: WebSocketFactory;
  url?: (path: string) => string;
}

/** Signaling socket for the desktop stream. Every (re)connect starts with `hello`. */
export class DesktopSignalClient {
  private readonly socket: ReconnectingSocket;

  constructor(options: DesktopSignalOptions) {
    const toUrl = options.url ?? ((path: string) => wsUrl(path));
    this.socket = new ReconnectingSocket({
      url: () => toUrl(desktop.wsPath),
      protocol: DESKTOP_PROTOCOL,
      factory: options.factory,
      onJson: (raw) => {
        const msg = parseDesktopMessage(raw);
        if (msg) {
          options.onMessage(msg);
        }
      },
      onState: options.onState,
    });
  }

  get state(): SocketState {
    return this.socket.current;
  }

  connect(): void {
    this.socket.start();
  }

  close(): void {
    this.socket.stop();
  }

  retry(): void {
    this.socket.reconnectNow();
  }

  send(message: DesktopClientMessage): boolean {
    return this.socket.sendJson(message);
  }
}
