import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import type { AgentEvent, Permission } from "../../api/events";
import { decodeEvent } from "../../api/events";
import { engineFor } from "../../lib/harness";
import { buildTimeline, chatReducer, emptyChat } from "./chatState";
import { PermissionCard } from "./PermissionCard";

const perm = (outcome = ""): Permission => ({
  id: "perm1",
  title: "Run make test",
  toolCallId: "t1",
  options: [
    { id: "ok", name: "Allow", kind: "allow_once" },
    { id: "always", name: "Always allow", kind: "allow_always" },
    { id: "no", name: "Reject", kind: "reject_once" },
  ],
  outcome,
});

describe("permission requests", () => {
  it("decodes a permission event", () => {
    const e = decodeEvent({ key: "perm1", kind: "permission", permission: { id: "perm1", title: "Run", options: [{ id: "ok", name: "Allow", kind: "allow_once" }] } });
    expect(e?.permission).toEqual({ id: "perm1", title: "Run", toolCallId: "", options: [{ id: "ok", name: "Allow", kind: "allow_once" }], outcome: "" });
  });

  it("puts a request into its turn", () => {
    const events: AgentEvent[] = [
      { key: "u", kind: "user", text: "go", time: "", model: "" },
      { key: "perm1", kind: "permission", text: "", time: "", model: "", permission: perm() },
    ];
    const tl = buildTimeline(chatReducer(emptyChat, { type: "events", reset: true, events }));
    expect(tl.turns[0].items).toEqual([{ type: "permission", key: "perm1", permission: perm() }]);
  });

  it("shows one button per answer while it waits, and the answer after", () => {
    const open = renderToStaticMarkup(createElement(PermissionCard, { permission: perm(), onAnswer: () => Promise.resolve() }));
    expect(open).toContain('aria-label="Permission request"');
    expect(open.match(/<button/g)).toHaveLength(3);
    expect(open).toContain("btn-primary");
    const done = renderToStaticMarkup(createElement(PermissionCard, { permission: perm("always") }));
    expect(done).toContain("Always allow");
    expect(done).toContain("text-success");
    expect(done).not.toContain("<button");
    expect(renderToStaticMarkup(createElement(PermissionCard, { permission: perm("cancelled") }))).toContain("Cancelled");
  });

  it("starts chat sessions with the ACP engine", () => {
    expect(engineFor("codex", "chat")).toBe("acp");
    expect(engineFor("codex", "terminal")).toBe("pty");
    expect(engineFor("shell", "chat")).toBe("pty");
  });
});
