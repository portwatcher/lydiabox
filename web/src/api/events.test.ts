import { describe, expect, it } from "vitest";
import { fakeFactory } from "../test/fakeSocket";
import { EVENTS_PROTOCOL, EventsClient, decodeEvent, parseEventsMessage } from "./events";
import type { AgentEvent, EventStatus } from "./events";

describe("parseEventsMessage", () => {
  it("decodes events and drops malformed ones", () => {
    const msg = parseEventsMessage({
      type: "events",
      reset: true,
      events: [
        { key: "u1", kind: "user", text: "hi" },
        { key: "t1", kind: "tool_call", tool: { id: "c1", name: "Bash", kind: "shell", summary: "ls" } },
        { key: "r1", kind: "tool_result", result: { callId: "c1", status: "error", output: "boom", images: ["data:image/png;base64,AA", "javascript:x"], durationMs: 12 } },
        { key: "x", kind: "bogus" },
        { kind: "user" },
        "nope",
      ],
    });
    expect(msg?.type).toBe("events");
    if (msg?.type !== "events") {
      return;
    }
    expect(msg.reset).toBe(true);
    expect(msg.events.map((e) => e.key)).toEqual(["u1", "t1", "r1"]);
    expect(msg.events[1].tool?.kind).toBe("shell");
    expect(msg.events[2].result).toEqual({ callId: "c1", status: "error", output: "boom", images: ["data:image/png;base64,AA"], durationMs: 12 });
  });

  it("decodes the stored-copy flag of the status", () => {
    const cached = parseEventsMessage({ type: "status", sessionId: "codex:1", state: "offline", live: false, synced: false, cached: true });
    expect(cached).toMatchObject({ type: "status", synced: false, cached: true });
    const fresh = parseEventsMessage({ type: "status", sessionId: "codex:1", state: "idle", live: true, synced: true });
    expect(fresh).toMatchObject({ type: "status", synced: true, cached: false });
    expect(parseEventsMessage({ type: "status", cached: "yes" })).toMatchObject({ cached: false });
  });

  it("maps unknown tool kinds and states to safe defaults", () => {
    expect(decodeEvent({ key: "t", kind: "tool_call", tool: { kind: "laser" } })?.tool?.kind).toBe("other");
    const status = parseEventsMessage({ type: "status", sessionId: "codex:1", state: "dancing", live: true, synced: true });
    expect(status).toMatchObject({ type: "status", state: "offline", live: true, synced: true, cached: false, prompt: "" });
    expect(parseEventsMessage({ type: "nope" })).toBeNull();
    expect(parseEventsMessage(null)).toBeNull();
  });
});

describe("EventsClient", () => {
  it("passes events and status on and stops on not_found", () => {
    const { factory, sockets } = fakeFactory();
    const got: { reset: boolean; events: AgentEvent[] }[] = [];
    const statuses: EventStatus[] = [];
    const errors: string[] = [];
    const c = new EventsClient(
      "codex:abc",
      { onEvents: (reset, events) => got.push({ reset, events }), onStatus: (s) => statuses.push(s), onError: (code) => errors.push(code) },
      { factory, url: (p) => `ws://test${p}` },
    );
    c.connect();
    expect(sockets).toHaveLength(1);
    expect(sockets[0].url).toBe("ws://test/api/v1/sessions/codex%3Aabc/events");
    expect(sockets[0].protocol).toBe(EVENTS_PROTOCOL);
    sockets[0].open();
    sockets[0].receiveJson({ type: "events", reset: true, events: [{ key: "a", kind: "assistant", text: "x" }] });
    sockets[0].receiveJson({ type: "status", sessionId: "codex:abc", state: "working", live: true, synced: true });
    expect(got).toEqual([{ reset: true, events: [expect.objectContaining({ key: "a", text: "x" })] }]);
    expect(statuses[0]).toEqual({ sessionId: "codex:abc", state: "working", live: true, synced: true, cached: false, prompt: "", error: "", engine: "", agent: "", mode: "", queued: 0 });
    sockets[0].receiveJson({ type: "error", code: "not_found", message: "gone" });
    sockets[0].serverClose(1008);
    expect(errors).toEqual(["not_found"]);
    expect(sockets).toHaveLength(1);
    c.close();
  });
});
