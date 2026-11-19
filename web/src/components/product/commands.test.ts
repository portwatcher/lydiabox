import { describe, expect, it } from "vitest";
import type { Session } from "../../api/types";
import { recentSessions } from "../AppCommands";
import { filterCommands, groupCommands, isPaletteKey, moveActive } from "./commands";
import type { Command } from "./commands";

const cmd = (id: string, label: string, group = "Pages", extra: Partial<Command> = {}): Command => ({ id, label, group, run: () => undefined, ...extra });

describe("filterCommands", () => {
  const list = [cmd("a", "Studio"), cmd("b", "Desktop"), cmd("c", "Fix login bug", "Sessions", { meta: "Codex" }), cmd("d", "Models", "Pages", { keywords: "providers keys" }), cmd("e", "Start the VM", "Actions")];

  it("keeps every command for an empty query", () => {
    expect(filterCommands(list, "  ")).toBe(list);
  });

  it("ranks a prefix, then a word start, then any match", () => {
    expect(filterCommands([cmd("x", "Bestudio"), cmd("y", "My studio"), cmd("z", "Studio")], "studio").map((c) => c.id)).toEqual(["z", "y", "x"]);
  });

  it("matches meta, keywords, and the group, with every word", () => {
    expect(filterCommands(list, "codex").map((c) => c.id)).toEqual(["c"]);
    expect(filterCommands(list, "keys").map((c) => c.id)).toEqual(["d"]);
    expect(filterCommands(list, "start vm").map((c) => c.id)).toEqual(["e"]);
    expect(filterCommands(list, "start nothing")).toEqual([]);
  });

  it("groups in first-seen order", () => {
    expect(groupCommands(list).map((g) => [g.group, g.items.length])).toEqual([
      ["Pages", 3],
      ["Sessions", 1],
      ["Actions", 1],
    ]);
  });
});

describe("moveActive", () => {
  it("wraps at both ends", () => {
    expect(moveActive(2, 3, "ArrowDown")).toBe(0);
    expect(moveActive(0, 3, "ArrowUp")).toBe(2);
    expect(moveActive(-1, 3, "ArrowUp")).toBe(2);
    expect(moveActive(1, 3, "End")).toBe(2);
    expect(moveActive(1, 0, "ArrowDown")).toBe(-1);
  });
});

describe("isPaletteKey", () => {
  const key = (partial: Partial<KeyboardEvent> = {}) => ({ key: "k", metaKey: false, ctrlKey: false, altKey: false, shiftKey: false, defaultPrevented: false, ...partial });
  const el = (inside: boolean) => ({ closest: () => (inside ? {} : null) });

  it("takes Cmd+K on macOS and Ctrl+K elsewhere", () => {
    expect(isPaletteKey(key({ metaKey: true }), null, true)).toBe(true);
    expect(isPaletteKey(key({ ctrlKey: true }), null, true)).toBe(false);
    expect(isPaletteKey(key({ ctrlKey: true }), null, false)).toBe(true);
    expect(isPaletteKey(key({ ctrlKey: true, shiftKey: true }), null, false)).toBe(false);
    expect(isPaletteKey(key({ ctrlKey: true, key: "j" }), null, false)).toBe(false);
  });

  it("leaves the key to a terminal, the remote desktop, and handled events", () => {
    expect(isPaletteKey(key({ ctrlKey: true }), el(true), false)).toBe(false);
    expect(isPaletteKey(key({ ctrlKey: true }), el(false), false)).toBe(true);
    expect(isPaletteKey(key({ ctrlKey: true, defaultPrevented: true }), null, false)).toBe(false);
  });
});

describe("recentSessions", () => {
  it("puts the newest first and caps the list", () => {
    const s = (id: string, updatedAt: string) => ({ id, updatedAt }) as Pick<Session, "id" | "updatedAt">;
    const list = [s("a", "2026-01-01"), s("b", "2026-03-01"), s("c", "2026-02-01")].map((x) => ({ ...x, harness: "codex", nativeId: "", title: "", workspaceDir: "", model: "", status: "idle", live: false, createdAt: "" }) satisfies Session);
    expect(recentSessions(list, 2).map((x) => x.id)).toEqual(["b", "c"]);
  });
});
