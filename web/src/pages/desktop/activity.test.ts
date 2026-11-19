import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import type { AgentAction } from "../../api/types";
import { AgentCursor, tagWidth } from "../../components/product/AgentCursor";
import { formatElapsed } from "../../components/product/AgentStatus";
import { RUN_GAP_MS, WORKING_MS, agentActivity } from "./activity";
import { TRAIL_MAX, TRAIL_MS, initialOverlay, overlayReducer } from "./overlay";
import type { FeedItem } from "./overlay";

function action(partial: Partial<AgentAction>): AgentAction {
  return { type: "agent_action", action: "click", x: 1, y: 2, button: "left", double: false, text: "", keys: "", dx: 0, dy: 0, tool: "", timestamp: "", ...partial };
}

const item = (id: number, receivedAt: number, partial: Partial<AgentAction> = {}): FeedItem => ({ id, receivedAt, action: action(partial) });

describe("agentActivity", () => {
  it("is offline without a connection and paused in a takeover", () => {
    expect(agentActivity([item(1, 0)], 1, "OBSERVER", false).phase).toBe("offline");
    expect(agentActivity([item(1, 0)], 1, "TAKEOVER", true)).toMatchObject({ phase: "paused", label: "You have control" });
  });

  it("watches when the last action is old", () => {
    expect(agentActivity([], 0, "OBSERVER", true).phase).toBe("idle");
    expect(agentActivity([item(1, 0)], WORKING_MS, "OBSERVER", true).phase).toBe("idle");
  });

  it("works on the newest action and counts the run back to a long gap", () => {
    const feed = [item(4, 100_000, { action: "type", text: "hi" }), item(3, 90_000), item(2, 80_000), item(1, 80_000 - RUN_GAP_MS - 1)];
    expect(agentActivity(feed, 101_000, "OBSERVER", true)).toEqual({ phase: "working", label: 'Type "hi"', since: 80_000, steps: 3 });
  });
});

describe("cursor trail", () => {
  it("keeps the earlier cursor points for a short time", () => {
    let s = overlayReducer(initialOverlay, { type: "action", action: action({ action: "move", x: 10, y: 10 }), now: 0 });
    expect(s.trail).toEqual([]);
    s = overlayReducer(s, { type: "action", action: action({ action: "move", x: 20, y: 20 }), now: 100 });
    s = overlayReducer(s, { type: "action", action: action({ action: "move", x: 20, y: 20 }), now: 200 });
    expect(s.trail).toEqual([{ x: 10, y: 10, at: 100 }]);
    expect(overlayReducer(s, { type: "expire", now: 100 + TRAIL_MS }).trail).toEqual([]);
  });

  it("caps the trail", () => {
    let s = initialOverlay;
    for (let i = 0; i < TRAIL_MAX + 5; i++) {
      s = overlayReducer(s, { type: "action", action: action({ action: "move", x: i, y: i }), now: i });
    }
    expect(s.trail).toHaveLength(TRAIL_MAX);
    expect(s.trail[TRAIL_MAX - 1]).toMatchObject({ x: TRAIL_MAX + 3 });
  });

  it("draws the name tag and one segment per trail step", () => {
    const html = renderToStaticMarkup(createElement("svg", null, createElement(AgentCursor, { at: { x: 5, y: 5 }, trail: [{ x: 0, y: 0 }, { x: 2, y: 2 }], name: "Agent", unit: 1 })));
    expect(html).toContain(">Agent</text>");
    expect(html.match(/<line/g)).toHaveLength(2);
    expect(tagWidth("Agent")).toBeGreaterThan(30);
  });
});

describe("formatElapsed", () => {
  it("reads seconds, minutes, and hours", () => {
    expect(formatElapsed(-5)).toBe("0s");
    expect(formatElapsed(42_000)).toBe("42s");
    expect(formatElapsed(65_000)).toBe("1:05");
    expect(formatElapsed(3_725_000)).toBe("1:02:05");
  });
});
