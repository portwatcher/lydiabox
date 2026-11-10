/**
 * A WebSocket that reconnects with capped exponential backoff and detects
 * dead connections with application-level `ping`/`pong` control frames
 * (`{ "type": "ping", "id": n }`), which both LydiaBox protocols share.
 */

export type SocketState = "connecting" | "open" | "reconnecting" | "closed" | "failed";

/** The subset of the browser WebSocket this module uses (lets tests inject a fake). */
export interface WebSocketLike {
  readonly readyState: number;
  binaryType: BinaryType;
  send(data: string | ArrayBufferLike | ArrayBufferView): void;
  close(code?: number, reason?: string): void;
  onopen: ((ev: Event) => void) | null;
  onmessage: ((ev: MessageEvent) => void) | null;
  onclose: ((ev: CloseEvent) => void) | null;
  onerror: ((ev: Event) => void) | null;
}

export type WebSocketFactory = (url: string, protocol: string) => WebSocketLike;

const OPEN = 1;

export const defaultFactory: WebSocketFactory = (url, protocol) => new WebSocket(url, protocol);

/** Converts an API path into a same-origin ws:// or wss:// URL. */
export function wsUrl(path: string, loc: Pick<Location, "protocol" | "host"> = window.location): string {
  const scheme = loc.protocol === "https:" ? "wss:" : "ws:";
  return `${scheme}//${loc.host}${path}`;
}

/** Delay before reconnect attempt `attempt` (0-based): 0.5 s doubling to 10 s, plus jitter. */
export function backoffDelay(attempt: number, random: () => number = Math.random): number {
  const base = Math.min(10_000, 500 * 2 ** attempt);
  return base + Math.floor(random() * 250);
}

export interface SocketOptions {
  /** Evaluated on every connect, so the target can change (PTY `switched`). */
  url: () => string;
  protocol: string;
  binaryType?: BinaryType;
  pingIntervalMs?: number;
  pongTimeoutMs?: number;
  /** Consecutive failed attempts before the state becomes `failed`. */
  maxAttempts?: number;
  factory?: WebSocketFactory;
  random?: () => number;
  onOpen?: () => void;
  onJson?: (message: unknown) => void;
  onBinary?: (data: ArrayBuffer) => void;
  onState?: (state: SocketState) => void;
}

export class ReconnectingSocket {
  private ws: WebSocketLike | null = null;
  private state: SocketState = "closed";
  private attempts = 0;
  private stopped = true;
  private finished = false;
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  private pingTimer: ReturnType<typeof setInterval> | null = null;
  private pongTimer: ReturnType<typeof setTimeout> | null = null;
  private pingId = 0;
  private awaitingPong: number | null = null;
  private readonly opts: Required<Omit<SocketOptions, "onOpen" | "onJson" | "onBinary" | "onState">> &
    Pick<SocketOptions, "onOpen" | "onJson" | "onBinary" | "onState">;

  constructor(options: SocketOptions) {
    // Defaults apply to options that are absent or explicitly undefined:
    // wrappers pass `factory: options.factory` through unchanged.
    this.opts = {
      ...options,
      binaryType: options.binaryType ?? "arraybuffer",
      pingIntervalMs: options.pingIntervalMs ?? 20_000,
      pongTimeoutMs: options.pongTimeoutMs ?? 10_000,
      maxAttempts: options.maxAttempts ?? 8,
      factory: options.factory ?? defaultFactory,
      random: options.random ?? Math.random,
    };
  }

  get current(): SocketState {
    return this.state;
  }

  start(): void {
    this.stopped = false;
    this.finished = false;
    this.attempts = 0;
    this.connect();
  }

  /** Closes for good; no reconnect. */
  stop(): void {
    this.stopped = true;
    this.clearTimers();
    const ws = this.ws;
    this.ws = null;
    if (ws) {
      ws.onopen = null;
      ws.onmessage = null;
      ws.onclose = null;
      ws.onerror = null;
      ws.close(1000, "client closed");
    }
    this.setState("closed");
  }

  /** Marks the stream as complete: the next close does not reconnect. */
  finish(): void {
    this.finished = true;
  }

  /** Drops the current connection and connects again now (after `failed`, or to follow a new URL). */
  reconnectNow(): void {
    this.attempts = 0;
    this.finished = false;
    this.stopped = false;
    this.clearTimers();
    const ws = this.ws;
    this.ws = null;
    if (ws) {
      ws.onclose = null;
      ws.onmessage = null;
      ws.onerror = null;
      ws.onopen = null;
      ws.close(1000, "reconnect");
    }
    this.connect();
  }

  sendJson(message: unknown): boolean {
    return this.sendRaw(JSON.stringify(message));
  }

  sendBinary(data: Uint8Array): boolean {
    return this.sendRaw(data);
  }

  private sendRaw(data: string | Uint8Array): boolean {
    if (!this.ws || this.ws.readyState !== OPEN) {
      return false;
    }
    this.ws.send(data);
    return true;
  }

  private connect(): void {
    this.setState(this.attempts === 0 ? "connecting" : "reconnecting");
    let ws: WebSocketLike;
    try {
      ws = this.opts.factory(this.opts.url(), this.opts.protocol);
    } catch {
      this.scheduleReconnect();
      return;
    }
    ws.binaryType = this.opts.binaryType;
    this.ws = ws;
    ws.onopen = () => {
      this.attempts = 0;
      this.setState("open");
      this.startPing();
      this.opts.onOpen?.();
    };
    ws.onmessage = (ev: MessageEvent) => this.handleMessage(ev.data);
    ws.onerror = () => {
      // onclose follows and decides about reconnecting.
    };
    ws.onclose = () => {
      if (this.ws !== ws) {
        return;
      }
      this.ws = null;
      this.clearTimers();
      if (this.stopped || this.finished) {
        this.setState("closed");
        return;
      }
      this.scheduleReconnect();
    };
  }

  private handleMessage(data: unknown): void {
    if (typeof data === "string") {
      let message: unknown;
      try {
        message = JSON.parse(data);
      } catch {
        return;
      }
      if (isPong(message)) {
        if (message.id === this.awaitingPong) {
          this.awaitingPong = null;
          if (this.pongTimer) {
            clearTimeout(this.pongTimer);
            this.pongTimer = null;
          }
        }
        return;
      }
      this.opts.onJson?.(message);
      return;
    }
    if (data instanceof ArrayBuffer) {
      this.opts.onBinary?.(data);
    }
  }

  private scheduleReconnect(): void {
    if (this.attempts >= this.opts.maxAttempts) {
      this.setState("failed");
      return;
    }
    const delay = backoffDelay(this.attempts, this.opts.random);
    this.attempts += 1;
    this.setState("reconnecting");
    this.reconnectTimer = setTimeout(() => {
      this.reconnectTimer = null;
      if (!this.stopped) {
        this.connect();
      }
    }, delay);
  }

  private startPing(): void {
    this.pingTimer = setInterval(() => {
      if (this.awaitingPong !== null) {
        return;
      }
      this.pingId += 1;
      const id = this.pingId;
      if (!this.sendJson({ type: "ping", id })) {
        return;
      }
      this.awaitingPong = id;
      this.pongTimer = setTimeout(() => {
        // No pong: the connection is dead even if the socket has not noticed.
        this.awaitingPong = null;
        this.ws?.close(4000, "ping timeout");
      }, this.opts.pongTimeoutMs);
    }, this.opts.pingIntervalMs);
  }

  private clearTimers(): void {
    if (this.reconnectTimer) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }
    if (this.pingTimer) {
      clearInterval(this.pingTimer);
      this.pingTimer = null;
    }
    if (this.pongTimer) {
      clearTimeout(this.pongTimer);
      this.pongTimer = null;
    }
    this.awaitingPong = null;
  }

  private setState(state: SocketState): void {
    if (this.state === state) {
      return;
    }
    this.state = state;
    this.opts.onState?.(state);
  }
}

function isPong(message: unknown): message is { type: "pong"; id: number } {
  return (
    typeof message === "object" &&
    message !== null &&
    "type" in message &&
    message.type === "pong" &&
    "id" in message &&
    typeof message.id === "number"
  );
}

/** Reads `type` from an untyped JSON value. */
export function messageType(message: unknown): string | null {
  if (typeof message === "object" && message !== null && "type" in message && typeof message.type === "string") {
    return message.type;
  }
  return null;
}
