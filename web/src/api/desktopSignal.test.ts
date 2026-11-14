import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { fakeFactory } from "../test/fakeSocket";
import { DESKTOP_PROTOCOL, DesktopSignalClient, parseDesktopMessage } from "./desktopSignal";
import type { DesktopServerMessage } from "./types";

beforeEach(() => {
  vi.useFakeTimers();
});
afterEach(() => {
  vi.useRealTimers();
});

describe("parseDesktopMessage", () => {
  it("decodes hello with screen and ICE servers", () => {
    expect(
      parseDesktopMessage({
        type: "hello",
        mode: "OBSERVER",
        screen: { width: 1920, height: 1080 },
        iceServers: [{ urls: ["stun:stun.l.google.com:19302"] }, { bad: true }],
      }),
    ).toEqual({
      type: "hello",
      mode: "OBSERVER",
      screen: { width: 1920, height: 1080 },
      iceServers: [{ urls: ["stun:stun.l.google.com:19302"] }],
      iceTransportPolicy: "all",
    });
  });

  it("decodes the relay-only ICE policy and TURN credentials", () => {
    expect(
      parseDesktopMessage({
        type: "hello",
        mode: "TAKEOVER",
        screen: { width: 1920, height: 1080 },
        iceServers: [{ urls: ["turns:turn.example.com:443?transport=tcp"], username: "u", credential: "c" }],
        iceTransportPolicy: "relay",
      }),
    ).toEqual({
      type: "hello",
      mode: "TAKEOVER",
      screen: { width: 1920, height: 1080 },
      iceServers: [{ urls: ["turns:turn.example.com:443?transport=tcp"], username: "u", credential: "c" }],
      iceTransportPolicy: "relay",
    });
  });

  it("decodes agent_action including drag start", () => {
    const msg = parseDesktopMessage({
      type: "agent_action",
      action: "drag",
      x: 900,
      y: 500,
      fromX: 100,
      fromY: 120,
      button: "left",
      double: false,
      text: "",
      keys: "",
      dx: 0,
      dy: 0,
      tool: "computer_mouse_drag",
      timestamp: "2026-11-10T06:30:00.250Z",
    });
    expect(msg).toMatchObject({ type: "agent_action", action: "drag", x: 900, y: 500, fromX: 100, fromY: 120, tool: "computer_mouse_drag" });
  });

  it("rejects unknown types, modes, and actions", () => {
    expect(parseDesktopMessage({ type: "agent_action", action: "teleport" })).toBeNull();
    expect(parseDesktopMessage({ type: "mode", mode: "CHAOS" })).toBeNull();
    expect(parseDesktopMessage({ type: "unknown" })).toBeNull();
    expect(parseDesktopMessage({ type: "ice", candidate: "x" })).toBeNull();
  });

  it("decodes ice candidates with null fields", () => {
    expect(parseDesktopMessage({ type: "ice", candidate: { candidate: "candidate:1", sdpMid: "0", sdpMLineIndex: 0 } })).toEqual({
      type: "ice",
      candidate: { candidate: "candidate:1", sdpMid: "0", sdpMLineIndex: 0 },
    });
  });
});

describe("DesktopSignalClient", () => {
  it("connects with the desktop subprotocol and relays typed messages", () => {
    const { factory, sockets } = fakeFactory();
    const received: DesktopServerMessage[] = [];
    const client = new DesktopSignalClient({ onMessage: (m) => received.push(m), factory, url: (p) => `ws://h${p}` });
    client.connect();
    const ws = sockets[0];
    expect(ws?.url).toBe("ws://h/api/v1/desktop/ws");
    expect(ws?.protocol).toBe(DESKTOP_PROTOCOL);
    ws?.open();
    ws?.receiveJson({ type: "viewers", count: 2 });
    ws?.receiveJson({ type: "pong", id: 1 });
    expect(received).toEqual([{ type: "viewers", count: 2 }]);
    expect(client.send({ type: "launch", app: "blender" })).toBe(true);
    expect(ws?.json()).toEqual([{ type: "launch", app: "blender" }]);
  });
});
