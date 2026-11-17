import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fakeFactory } from "../test/fakeSocket";
import { PAUSED_CODE, PtyClient, PTY_PROTOCOL, parsePtyMessage } from "./pty";
import { ReconnectingSocket, STOPPED_CODE, backoffDelay, wsUrl } from "./socket";
import type { SocketState } from "./socket";

beforeEach(() => {
  vi.useFakeTimers();
});
afterEach(() => {
  vi.useRealTimers();
});

const noJitter = () => 0;

describe("wsUrl and backoff", () => {
  it("maps the page scheme to ws or wss", () => {
    expect(wsUrl("/api/v1/desktop/ws", { protocol: "https:", host: "box.example.com" })).toBe("wss://box.example.com/api/v1/desktop/ws");
    expect(wsUrl("/x", { protocol: "http:", host: "127.0.0.1:8080" })).toBe("ws://127.0.0.1:8080/x");
  });

  it("doubles from 0.5 s and caps at 10 s", () => {
    expect([0, 1, 2, 3, 4, 5, 6].map((n) => backoffDelay(n, noJitter))).toEqual([500, 1000, 2000, 4000, 8000, 10000, 10000]);
    expect(backoffDelay(0, () => 0.999)).toBe(749);
  });
});

describe("ReconnectingSocket", () => {
  it("reconnects after an unexpected close and resets attempts on open", () => {
    const { factory, sockets } = fakeFactory();
    const states: SocketState[] = [];
    const socket = new ReconnectingSocket({ url: () => "ws://h/x", protocol: "p", factory, random: noJitter, onState: (s) => states.push(s) });
    socket.start();
    sockets[0]?.open();
    sockets[0]?.serverClose();
    expect(socket.current).toBe("reconnecting");
    vi.advanceTimersByTime(499);
    expect(sockets).toHaveLength(1);
    vi.advanceTimersByTime(1);
    expect(sockets).toHaveLength(2);
    sockets[1]?.open();
    expect(states).toEqual(["connecting", "open", "reconnecting", "open"]);
  });

  it("uses the browser WebSocket when wrappers pass factory: undefined", () => {
    const { factory, sockets } = fakeFactory();
    const urls: string[] = [];
    vi.stubGlobal(
      "WebSocket",
      vi.fn(function (url: string, protocol: string) {
        urls.push(url);
        return factory(url, protocol);
      }),
    );
    try {
      const socket = new ReconnectingSocket({ url: () => "ws://h/x", protocol: "p", factory: undefined, random: noJitter });
      socket.start();
      expect(urls).toEqual(["ws://h/x"]);
      sockets[0]?.open();
      expect(socket.current).toBe("open");
    } finally {
      vi.unstubAllGlobals();
    }
  });

  it("gives up after maxAttempts consecutive failures and retries on demand", () => {
    const { factory, sockets } = fakeFactory();
    const socket = new ReconnectingSocket({ url: () => "ws://h/x", protocol: "p", factory, random: noJitter, maxAttempts: 2 });
    socket.start();
    sockets[0]?.serverClose();
    vi.advanceTimersByTime(500);
    sockets[1]?.serverClose();
    vi.advanceTimersByTime(1000);
    sockets[2]?.serverClose();
    expect(socket.current).toBe("failed");
    vi.advanceTimersByTime(60_000);
    expect(sockets).toHaveLength(3);
    socket.reconnectNow();
    expect(sockets).toHaveLength(4);
  });

  it("closes a connection that misses a pong", () => {
    const { factory, sockets } = fakeFactory();
    const socket = new ReconnectingSocket({
      url: () => "ws://h/x",
      protocol: "p",
      factory,
      random: noJitter,
      pingIntervalMs: 1000,
      pongTimeoutMs: 500,
    });
    socket.start();
    const first = sockets[0];
    first?.open();
    vi.advanceTimersByTime(1000);
    expect(first?.json()).toEqual([{ type: "ping", id: 1 }]);
    first?.receiveJson({ type: "pong", id: 1 });
    vi.advanceTimersByTime(1000);
    expect(first?.json()).toEqual([{ type: "ping", id: 1 }, { type: "ping", id: 2 }]);
    vi.advanceTimersByTime(500);
    expect(first?.closed?.code).toBe(4000);
    expect(socket.current).toBe("reconnecting");
  });

  it("does not reconnect after stop or finish", () => {
    const { factory, sockets } = fakeFactory();
    const socket = new ReconnectingSocket({ url: () => "ws://h/x", protocol: "p", factory });
    socket.start();
    sockets[0]?.open();
    socket.finish();
    sockets[0]?.serverClose(1000);
    vi.advanceTimersByTime(30_000);
    expect(sockets).toHaveLength(1);
    expect(socket.current).toBe("closed");
  });
});

describe("parsePtyMessage", () => {
  it("decodes every server message type", () => {
    expect(parsePtyMessage({ type: "hello", sessionId: "codex:1", replayBytes: 5, cols: 80, rows: 24 })).toEqual({
      type: "hello",
      sessionId: "codex:1",
      replayBytes: 5,
      cols: 80,
      rows: 24,
    });
    expect(parsePtyMessage({ type: "exit", code: 0, signal: "" })).toEqual({ type: "exit", code: 0, signal: "" });
    expect(parsePtyMessage({ type: "error", code: "unavailable", message: "VM is not running" })).toMatchObject({ code: "unavailable" });
    expect(parsePtyMessage({ type: "nope" })).toBeNull();
    expect(parsePtyMessage("hello")).toBeNull();
  });
});

describe("PtyClient", () => {
  it("speaks lydiabox.pty.v1: binary output, resize on open, keyboard input as binary", () => {
    const { factory, sockets } = fakeFactory();
    const output: number[][] = [];
    const client = new PtyClient("codex:abc", { onOutput: (d) => output.push(Array.from(d)) }, { factory, url: (p) => `ws://h${p}` });
    client.resize(120, 40);
    client.connect();
    const ws = sockets[0];
    expect(ws?.url).toBe("ws://h/api/v1/sessions/codex%3Aabc/ws");
    expect(ws?.protocol).toBe(PTY_PROTOCOL);
    expect(ws?.binaryType).toBe("arraybuffer");
    ws?.open();
    expect(ws?.json()).toEqual([{ type: "resize", cols: 120, rows: 40 }]);
    ws?.receiveBinary([104, 105]);
    expect(output).toEqual([[104, 105]]);
    client.write("ls\r");
    const last = ws?.sent.at(-1);
    expect(last).toBeInstanceOf(Uint8Array);
    client.signal("SIGINT");
    expect(ws?.json().at(-1)).toEqual({ type: "signal", signal: "SIGINT" });
  });

  it("adopts the session ID from hello and stops after exit", () => {
    const { factory, sockets } = fakeFactory();
    const exited = vi.fn();
    const client = new PtyClient("dsh:new-1", { onOutput: () => undefined, onExit: exited }, { factory, url: (p) => p });
    client.connect();
    sockets[0]?.open();
    sockets[0]?.receiveJson({ type: "hello", sessionId: "dsh:a91f", replayBytes: 0, cols: 80, rows: 24 });
    expect(client.sessionId).toBe("dsh:a91f");
    sockets[0]?.serverClose();
    vi.advanceTimersByTime(1000);
    // The reconnect says so, and uses the adopted session ID.
    expect(sockets[1]?.url).toBe("/api/v1/sessions/dsh%3Aa91f/ws?reconnect=1");
    sockets[1]?.open();
    sockets[1]?.receiveJson({ type: "exit", code: 0, signal: "" });
    sockets[1]?.serverClose(1000);
    vi.advanceTimersByTime(30_000);
    expect(exited).toHaveBeenCalledOnce();
    expect(sockets).toHaveLength(2);
    expect(client.state).toBe("closed");
  });

  it("does not reconnect after the server says the user stopped the VM", () => {
    const { factory, sockets } = fakeFactory();
    const errors = vi.fn();
    const client = new PtyClient("dsh:a91f", { onOutput: () => undefined, onError: errors }, { factory, url: (p) => p });
    client.connect();
    sockets[0]?.open();
    sockets[0]?.receiveJson({ type: "error", code: STOPPED_CODE, message: "the VM was stopped" });
    sockets[0]?.serverClose(1000);
    vi.advanceTimersByTime(60_000);
    expect(errors).toHaveBeenCalledOnce();
    expect(sockets).toHaveLength(1);
    expect(client.state).toBe("closed");
  });

  it("waits after a hibernate and attaches again on retry", () => {
    const { factory, sockets } = fakeFactory();
    const errors = vi.fn();
    const client = new PtyClient("dsh:a91f", { onOutput: () => undefined, onError: errors }, { factory, url: (p) => p });
    client.connect();
    sockets[0]?.open();
    sockets[0]?.receiveJson({ type: "hello", sessionId: "dsh:a91f", replayBytes: 0, cols: 80, rows: 24 });
    sockets[0]?.receiveJson({ type: "error", code: PAUSED_CODE, message: "the VM was hibernated" });
    sockets[0]?.serverClose(1000);
    vi.advanceTimersByTime(60_000);
    expect(errors).toHaveBeenCalledOnce();
    expect(sockets).toHaveLength(1);
    expect(client.state).toBe("closed");
    client.retry();
    expect(sockets).toHaveLength(2);
    expect(sockets[0]?.url).toBe("/api/v1/sessions/dsh%3Aa91f/ws");
    expect(sockets[1]?.url).toBe("/api/v1/sessions/dsh%3Aa91f/ws?reconnect=1");
  });
});
