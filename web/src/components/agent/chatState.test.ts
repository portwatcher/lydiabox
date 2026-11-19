import { describe, expect, it } from "vitest";
import type { AgentEvent } from "../../api/events";
import { buildTimeline, chatReducer, emptyChat, lastReply } from "./chatState";

const ev = (e: Partial<AgentEvent> & Pick<AgentEvent, "key" | "kind">): AgentEvent => ({ time: "", text: "", model: "", ...e });

const tool = (id: string, name: string, kind: "shell" | "edit" | "read" | "computer" | "plan", extra: Partial<AgentEvent> = {}): AgentEvent =>
  ev({ key: `tool:${id}`, kind: "tool_call", tool: { id, name, kind, summary: "", input: "", path: "", diff: "" }, ...extra });

describe("chatReducer", () => {
  it("appends new keys, replaces known ones in place, and resets", () => {
    let s = chatReducer(emptyChat, { type: "events", reset: true, events: [ev({ key: "a", kind: "user", text: "1" }), ev({ key: "b", kind: "assistant", text: "2" })] });
    s = chatReducer(s, { type: "events", reset: false, events: [ev({ key: "a", kind: "user", text: "1b" }), ev({ key: "c", kind: "assistant", text: "3" })] });
    expect(s.order).toEqual(["a", "b", "c"]);
    expect(s.byKey.a.text).toBe("1b");
    s = chatReducer(s, { type: "events", reset: true, events: [ev({ key: "z", kind: "user" })] });
    expect(s.order).toEqual(["z"]);
    s = chatReducer(s, { type: "status", status: { sessionId: "x", state: "idle", live: true, synced: true, prompt: "", error: "" } });
    expect(s.status?.state).toBe("idle");
    expect(chatReducer(s, { type: "clear" })).toBe(emptyChat);
  });
});

describe("buildTimeline", () => {
  const events: AgentEvent[] = [
    ev({ key: "u1", kind: "user", text: "Fix the bug", time: "2026-11-19T10:00:00Z" }),
    ev({ key: "r1", kind: "reasoning", text: "Look first" }),
    tool("c1", "Read", "read", { tool: { id: "c1", name: "Read", kind: "read", summary: "", input: "", path: "/w/a.go", diff: "" } }),
    tool("c2", "Edit", "edit", { tool: { id: "c2", name: "Edit", kind: "edit", summary: "", input: "", path: "/w/a.go", diff: "-a\n+b" } }),
    ev({ key: "result:c1", kind: "tool_result", result: { callId: "c1", status: "ok", output: "x", images: [], durationMs: 5 } }),
    ev({ key: "result:c2", kind: "tool_result", result: { callId: "c2", status: "error", output: "no", images: [], durationMs: 5 } }),
    ev({ key: "plan:p", kind: "plan", plan: [{ text: "A", status: "completed" }, { text: "B", status: "in_progress" }] }),
    ev({ key: "usage:1", kind: "usage", model: "m1", usage: { inputTokens: 100, outputTokens: 10, cachedTokens: 0, reasoningTokens: 0, contextTokens: 100, contextWindow: 1000, costUsd: 0.01 } }),
    ev({ key: "a1", kind: "assistant", text: "Done.", time: "2026-11-19T10:00:42Z" }),
    ev({ key: "usage:2", kind: "usage", model: "m1", usage: { inputTokens: 120, outputTokens: 20, cachedTokens: 0, reasoningTokens: 0, contextTokens: 130, contextWindow: 0, costUsd: 0.02 } }),
    ev({ key: "end:1", kind: "turn_end" }),
    ev({ key: "u2", kind: "user", text: "Take a screenshot" }),
    tool("c3", "mcp__lydiabox__computer_screenshot", "computer"),
    ev({ key: "end:2", kind: "turn_end", text: "interrupted" }),
  ];
  const state = chatReducer(emptyChat, { type: "events", reset: true, events });
  const tl = buildTimeline(state);

  it("groups events into turns with merged tool results", () => {
    expect(tl.turns).toHaveLength(2);
    const [t1, t2] = tl.turns;
    expect(t1.prompt?.text).toBe("Fix the bug");
    expect(t1.items.map((i) => i.type)).toEqual(["reasoning", "tools", "plan", "text"]);
    const tools = t1.items[1];
    expect(tools.type === "tools" && tools.tools.map((t) => t.result?.status)).toEqual(["ok", "error"]);
    expect(t1.ended).toBe(true);
    expect(t2.interrupted).toBe(true);
    expect(t2.desktop).toBe(true);
    expect(t1.desktop).toBe(false);
  });

  it("sums the stats and keeps the files of a turn", () => {
    const s = tl.turns[0].stats;
    expect(s).toMatchObject({ durationMs: 42_000, inputTokens: 220, outputTokens: 30, model: "m1", tools: 2 });
    expect(s.costUsd).toBeCloseTo(0.03);
    expect(tl.turns[0].sources).toEqual([{ key: "/w/a.go", kind: "edit", label: "/w/a.go" }]);
  });

  it("keeps the newest plan, context, and model", () => {
    expect(tl.plan?.map((p) => p.status)).toEqual(["completed", "in_progress"]);
    expect(tl.context).toEqual({ tokens: 130, window: 0 });
    expect(tl.model).toBe("m1");
    expect(lastReply(tl.turns[0])).toBe("Done.");
    expect(lastReply(tl.turns[1])).toBe("");
  });

  it("puts agent output before any prompt into a turn of its own", () => {
    const s = chatReducer(emptyChat, { type: "events", reset: true, events: [ev({ key: "a", kind: "assistant", text: "Hello" })] });
    const t = buildTimeline(s);
    expect(t.turns).toHaveLength(1);
    expect(t.turns[0].prompt).toBeUndefined();
  });
});
