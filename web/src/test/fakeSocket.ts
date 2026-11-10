import type { WebSocketFactory, WebSocketLike } from "../api/socket";

/** An in-memory WebSocket for client tests. */
export class FakeSocket implements WebSocketLike {
  readyState = 0;
  binaryType: BinaryType = "blob";
  sent: (string | ArrayBufferLike | ArrayBufferView)[] = [];
  closed: { code?: number; reason?: string } | null = null;
  onopen: ((ev: Event) => void) | null = null;
  onmessage: ((ev: MessageEvent) => void) | null = null;
  onclose: ((ev: CloseEvent) => void) | null = null;
  onerror: ((ev: Event) => void) | null = null;

  constructor(
    readonly url: string,
    readonly protocol: string,
  ) {}

  send(data: string | ArrayBufferLike | ArrayBufferView): void {
    this.sent.push(data);
  }

  close(code?: number, reason?: string): void {
    this.closed = { code, reason };
    this.serverClose(code ?? 1000);
  }

  // Test controls.
  open(): void {
    this.readyState = 1;
    this.onopen?.(new Event("open"));
  }

  receiveJson(value: unknown): void {
    this.onmessage?.(new MessageEvent("message", { data: JSON.stringify(value) }));
  }

  receiveBinary(bytes: number[]): void {
    this.onmessage?.(new MessageEvent("message", { data: new Uint8Array(bytes).buffer }));
  }

  serverClose(code = 1006): void {
    if (this.readyState === 3) {
      return;
    }
    this.readyState = 3;
    this.onclose?.(new CloseEvent("close", { code }));
  }

  /** Text frames sent by the client, decoded. */
  json(): unknown[] {
    return this.sent.filter((d): d is string => typeof d === "string").map((d) => JSON.parse(d));
  }
}

export function fakeFactory(): { factory: WebSocketFactory; sockets: FakeSocket[] } {
  const sockets: FakeSocket[] = [];
  return {
    sockets,
    factory: (url, protocol) => {
      const socket = new FakeSocket(url, protocol);
      sockets.push(socket);
      return socket;
    },
  };
}
