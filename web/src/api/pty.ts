import { isRecord, num, str } from "./decode";
import { sessions } from "./domains";
import { PAUSED_CODE, ReconnectingSocket, STOPPED_CODE, wsUrl } from "./socket";
import type { SocketState, WebSocketFactory } from "./socket";
import type { PtyClientMessage, PtyServerMessage, PtySignal } from "./types";

export const PTY_PROTOCOL = "lydiabox.pty.v1";

/**
 * A terminal socket that ends with this code keeps its process in the
 * paused VM; the client waits until the VM runs again and then calls `retry`.
 */
export { PAUSED_CODE };

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
 * text frames carry JSON control messages. The client adopts the session ID
 * from `hello` so a reconnect attaches to it. After `exit`, the `stopped`
 * error of a VM that the user stopped, or the `paused` error of a hibernated
 * VM, the client does not reconnect by itself.
 */
export class PtyClient {
  private id: string;
  /** Set by the first hello: later connects are reconnects. */
  private attached = false;
  private readonly socket: ReconnectingSocket;
  private readonly encoder = new TextEncoder();
  private size: { cols: number; rows: number } | null = null;

  constructor(sessionId: string, private readonly handlers: PtyHandlers, options: PtyClientOptions = {}) {
    this.id = sessionId;
    const toUrl = options.url ?? ((path: string) => wsUrl(path));
    this.socket = new ReconnectingSocket({
      // A reconnect says so: the server does not wake a paused VM for it.
      url: () => toUrl(sessions.wsPath(this.id) + (this.attached ? "?reconnect=1" : "")),
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
        this.attached = true;
        if (msg.sessionId) {
          this.id = msg.sessionId;
        }
        this.handlers.onHello?.(msg);
        return;
      case "exit":
        this.socket.finish();
        this.handlers.onExit?.(msg);
        return;
      case "error":
        if (msg.code === STOPPED_CODE || msg.code === PAUSED_CODE) {
          // A reconnect would ask for the stopped or paused VM again.
          this.socket.finish();
        }
        this.handlers.onError?.(msg);
        return;
      default:
        return;
    }
  }
}
