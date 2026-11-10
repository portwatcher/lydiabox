import { describe, expect, it } from "vitest";
import type { AgentAction, AuditEntry } from "../api/types";
import { appendOlder, auditDetail, mergeNewer } from "./connectors/audit";
import { initialOverlay, overlayReducer, PILL_MS, RIPPLE_MS, describeAction, formatKeys } from "./desktop/overlay";
import { collectEntries } from "./files/collectFiles";
import type { DirEntryLike, EntryLike, FileEntryLike } from "./files/collectFiles";
import { emptyRouting, isDirty, routingReducer, validateGroups } from "./models/routing";

function action(partial: Partial<AgentAction>): AgentAction {
  return {
    type: "agent_action",
    action: "move",
    x: 0,
    y: 0,
    button: "",
    double: false,
    text: "",
    keys: "",
    dx: 0,
    dy: 0,
    tool: "",
    timestamp: "",
    ...partial,
  };
}

describe("overlayReducer", () => {
  it("moves the cursor, adds a ripple per click, and expires it", () => {
    let s = overlayReducer(initialOverlay, { type: "action", action: action({ action: "click", x: 812, y: 440, button: "left" }), now: 1000 });
    expect(s.cursor).toEqual({ x: 812, y: 440 });
    expect(s.ripples).toHaveLength(1);
    expect(s.feed).toHaveLength(1);
    s = overlayReducer(s, { type: "expire", now: 1000 + RIPPLE_MS - 1 });
    expect(s.ripples).toHaveLength(1);
    s = overlayReducer(s, { type: "expire", now: 1000 + RIPPLE_MS });
    expect(s.ripples).toHaveLength(0);
    expect(s.feed).toHaveLength(1);
  });

  it("shows keystroke pills for type and key, newest last, capped", () => {
    let s = initialOverlay;
    for (let i = 0; i < 6; i += 1) {
      s = overlayReducer(s, { type: "action", action: action({ action: "key", keys: `ctrl+${i}` }), now: i });
    }
    expect(s.pills.map((p) => p.text)).toEqual(["Ctrl+2", "Ctrl+3", "Ctrl+4", "Ctrl+5"]);
    expect(s.cursor).toBeNull();
    s = overlayReducer(s, { type: "expire", now: 5 + PILL_MS });
    expect(s.pills).toHaveLength(0);
  });

  it("keeps the newest action first in the feed and returns the same state when nothing expires", () => {
    let s = overlayReducer(initialOverlay, { type: "action", action: action({ action: "screenshot" }), now: 1 });
    s = overlayReducer(s, { type: "action", action: action({ action: "type", text: "hello" }), now: 2 });
    expect(s.feed.map((f) => f.action.action)).toEqual(["type", "screenshot"]);
    const same = overlayReducer({ ...s, pills: [] }, { type: "expire", now: 3 });
    expect(same.pills).toEqual([]);
    expect(overlayReducer(same, { type: "expire", now: 4 })).toBe(same);
  });

  it("describes actions", () => {
    expect(describeAction(action({ action: "click", x: 1, y: 2, double: true }))).toBe("Double-click at 1, 2");
    expect(describeAction(action({ action: "click", x: 1, y: 2, button: "right" }))).toBe("Right-click at 1, 2");
    expect(describeAction(action({ action: "drag", x: 5, y: 6, fromX: 1, fromY: 2 }))).toBe("Drag from 1, 2 to 5, 6");
    expect(describeAction(action({ action: "scroll", dy: -120, x: 3, y: 4 }))).toBe("Scroll up at 3, 4");
    expect(formatKeys("ctrl+shift+t")).toBe("Ctrl+Shift+T");
  });
});

describe("routingReducer", () => {
  it("edits groups and tracks unsaved changes", () => {
    let s = routingReducer(emptyRouting, { type: "load", groups: [{ name: "budget", strategy: "fallback", targets: ["a/x"] }] });
    expect(isDirty(s)).toBe(false);
    s = routingReducer(s, { type: "addTarget", index: 0, target: " b/y " });
    s = routingReducer(s, { type: "addTarget", index: 0, target: "b/y" });
    expect(s.groups[0]?.targets).toEqual(["a/x", "b/y"]);
    s = routingReducer(s, { type: "moveTarget", index: 0, from: 1, to: 0 });
    expect(s.groups[0]?.targets).toEqual(["b/y", "a/x"]);
    s = routingReducer(s, { type: "moveTarget", index: 0, from: 0, to: -1 });
    expect(s.groups[0]?.targets).toEqual(["b/y", "a/x"]);
    s = routingReducer(s, { type: "strategy", index: 0, strategy: "round-robin" });
    s = routingReducer(s, { type: "add" });
    expect(s.groups[1]).toEqual({ name: "group", strategy: "fallback", targets: [] });
    expect(isDirty(s)).toBe(true);
    s = routingReducer(s, { type: "revert" });
    expect(isDirty(s)).toBe(false);
  });

  it("validates names, duplicates, and empty target lists", () => {
    const errors = validateGroups([
      { name: "a", strategy: "fallback", targets: ["x/y"] },
      { name: "a", strategy: "fallback", targets: ["x/y"] },
      { name: "", strategy: "fallback", targets: ["x/y"] },
      { name: "b c", strategy: "fallback", targets: ["x/y"] },
      { name: "d", strategy: "round-robin", targets: [] },
    ]);
    expect([...errors.keys()]).toEqual([1, 2, 3, 4]);
  });
});

describe("audit helpers", () => {
  const entry = (id: number): AuditEntry => ({ id, kind: "connector.token.issued", detail: "", createdAt: "" });

  it("merges newer entries on top and older pages below without duplicates", () => {
    expect(mergeNewer([entry(5), entry(4)], [entry(7), entry(6), entry(5)]).map((e) => e.id)).toEqual([7, 6, 5, 4]);
    expect(appendOlder([entry(5), entry(4)], [entry(4), entry(3)]).map((e) => e.id)).toEqual([5, 4, 3]);
  });

  it("parses detail JSON into pairs", () => {
    expect(auditDetail('{"provider":"github","account":"octocat","host":""}')).toEqual([
      ["provider", "github"],
      ["account", "octocat"],
    ]);
    expect(auditDetail("not json")).toEqual([["detail", "not json"]]);
  });
});

describe("collectEntries", () => {
  const file = (fullPath: string): FileEntryLike => ({
    isFile: true,
    isDirectory: false,
    fullPath,
    file: (ok) => ok(new File(["x"], fullPath.slice(fullPath.lastIndexOf("/") + 1))),
  });
  const dir = (fullPath: string, children: EntryLike[]): DirEntryLike => ({
    isFile: false,
    isDirectory: true,
    fullPath,
    createReader: () => {
      let done = false;
      return {
        readEntries: (ok) => {
          ok(done ? [] : children);
          done = true;
        },
      };
    },
  });

  it("walks dropped folders and keeps relative paths", async () => {
    const items = await collectEntries([dir("/photos", [file("/photos/a.jpg"), dir("/photos/2026", [file("/photos/2026/b.jpg")])]), file("/c.txt")]);
    expect(items.map((i) => i.name)).toEqual(["photos/a.jpg", "photos/2026/b.jpg", "c.txt"]);
  });
});
