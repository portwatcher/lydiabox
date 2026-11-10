import { isRecord, num, str } from "./decode";
import { sessions } from "./domains";
import { ReconnectingSocket, wsUrl } from "./socket";
import type { SocketState, WebSocketFactory } from "./socket";
import type { PtyClientMessage, PtyServerMessage, PtySignal } from "./types";

export const PTY_PROTOCOL = "lydiabox.pty.v1";

/** Decodes one `lydiabox.pty.v1` text frame; unknown or malformed frames give null. */
export function parsePtyMessage(message: unknown): PtyServerMessage | null {
  if (!isRecord(message)) {
    return null;
  }
  switch (message.type) {
    case "hello":
      return {
        type: "hello",
        sessionId: str(message, "sessionId"),
        replayBytes: num(message, "replayBytes"),
        cols: num(message, "cols"),
        rows: num(message, "rows"),
      };
    case "resized":
      return { type: "resized", cols: num(message, "cols"), rows: num(message, "rows") };
    case "pong":
      return { type: "pong", id: num(message, "id") };
    case "exit":
      return { type: "exit", code: num(message, "code"), signal: str(message, "signal") };
    case "switched":
      return {
        type: "switched",
        fromSessionId: str(message, "fromSessionId"),
        toSessionId: str(message, "toSessionId"),
        handoffPath: str(message, "handoffPath"),
      };
    case "error":
      return { type: "error", code: str(message, "code"), message: str(message, "message") };
    default:
      return null;
  }
}

export interface PtyHandlers {
  onOutput: (data: Uint8Array) => void;
  onHello?: (msg: Extract<PtyServerMessage, { type: "hello" }>) => void;
  onExit?: (msg: Extract<PtyServerMessage, { type: "exit" }>) => void;
  onSwitched?: (msg: Extract<PtyServerMessage, { type: "switched" }>) => void;
  onError?: (msg: Extract<PtyServerMessage, { type: "error" }>) => void;
  onState?: (state: SocketState) => void;
}

export interface PtyClientOptions {
  factory?: WebSocketFactory;
  pingIntervalMs?: number;
  url?: (path: string) => string;
}

/**
 * Terminal room client. Binary frames carry PTY bytes in both directions;
 * text frames carry JSON control messages. After `switched` the server keeps
 * streaming the new session on the same socket; the client adopts the new ID
 * so a reconnect attaches to it. After `exit` the client does not reconnect.
 */
export class PtyClient {
  private id: string;
  private readonly socket: ReconnectingSocket;
  private readonly encoder = new TextEncoder();
  private size: { cols: number; rows: number } | null = null;

  constructor(sessionId: string, private readonly handlers: PtyHandlers, options: PtyClientOptions = {}) {
    this.id = sessionId;
    const toUrl = options.url ?? ((path: string) => wsUrl(path));
    this.socket = new ReconnectingSocket({
      url: () => toUrl(sessions.wsPath(this.id)),
      protocol: PTY_PROTOCOL,
      binaryType: "arraybuffer",
      factory: options.factory,
      pingIntervalMs: options.pingIntervalMs,
      onOpen: () => {
        if (this.size) {
          this.control({ type: "resize", ...this.size });
        }
      },
      onBinary: (data) => this.handlers.onOutput(new Uint8Array(data)),
      onJson: (raw) => this.dispatch(raw),
      onState: (state) => this.handlers.onState?.(state),
    });
  }

  get sessionId(): string {
    return this.id;
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

  /** Sends keyboard input as a binary frame. */
  write(data: string | Uint8Array): boolean {
    const bytes = typeof data === "string" ? this.encoder.encode(data) : data;
    return this.socket.sendBinary(bytes);
  }

  resize(cols: number, rows: number): void {
    if (cols <= 0 || rows <= 0) {
      return;
    }
    if (this.size && this.size.cols === cols && this.size.rows === rows) {
      return;
    }
    this.size = { cols, rows };
    this.control({ type: "resize", cols, rows });
  }

  signal(signal: PtySignal): void {
    this.control({ type: "signal", signal });
  }

  private control(message: PtyClientMessage): void {
    this.socket.sendJson(message);
  }

  private dispatch(raw: unknown): void {
    const msg = parsePtyMessage(raw);
    if (!msg) {
      return;
    }
    switch (msg.type) {
      case "hello":
        if (msg.sessionId) {
          this.id = msg.sessionId;
        }
        this.handlers.onHello?.(msg);
        return;
      case "exit":
        this.socket.finish();
        this.handlers.onExit?.(msg);
        return;
      case "switched":
        if (msg.toSessionId) {
          this.id = msg.toSessionId;
        }
        this.handlers.onSwitched?.(msg);
        return;
      case "error":
        this.handlers.onError?.(msg);
        return;
      default:
        return;
    }
  }
}
