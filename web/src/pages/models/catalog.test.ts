import { describe, expect, it } from "vitest";
import type { Binding, Model, Provider, Session, Subscription } from "../../api/types";
import { bindingsByModel, groupModels, groupsByModel, modelName, usageByModel, usageTotal, withBinding } from "./catalog";
import { statusRows } from "./ProviderStatus";

const m = (id: string, contextWindow = 0, name = ""): Model => ({ id, provider: id.split("/")[0], name, contextWindow });

function session(model: string, usage?: Session["usage"]): Session {
  return { id: model, harness: "codex", nativeId: "", title: "", workspaceDir: "", model, status: "idle", live: false, createdAt: "", updatedAt: "", usage };
}

describe("catalog", () => {
  it("names models by name or by the ID tail", () => {
    expect(modelName(m("openai/gpt-5.2"))).toBe("gpt-5.2");
    expect(modelName(m("openai/x", 0, "GPT X"))).toBe("GPT X");
    expect(modelName(m("plain"))).toBe("plain");
  });

  it("groups by provider in page order and filters by every word", () => {
    const list = [m("openai/gpt-b"), m("openai/gpt-a"), m("claude/sonnet"), m("kimi/k3")];
    const names = { openai: "OpenAI", claude: "Claude", kimi: "Kimi" };
    const groups = groupModels(list, names, "", ["claude", "openai"]);
    expect(groups.map((g) => g.provider)).toEqual(["claude", "openai", "kimi"]);
    expect(groups[1].models.map((x) => x.id)).toEqual(["openai/gpt-a", "openai/gpt-b"]);
    expect(groupModels(list, names, "openai B").flatMap((g) => g.models.map((x) => x.id))).toEqual(["openai/gpt-b"]);
    expect(groupModels(list, names, "nothing")).toEqual([]);
  });

  it("indexes bindings and routing groups by model", () => {
    const bindings: Binding[] = [
      { harness: "codex", model: "a/x", configPath: "", updatedAt: "" },
      { harness: "dsh", model: "a/x", configPath: "", updatedAt: "" },
      { harness: "opencode", model: "", configPath: "", updatedAt: "" },
    ];
    expect(bindingsByModel(bindings).get("a/x")).toEqual(["codex", "dsh"]);
    expect(bindingsByModel(bindings).has("")).toBe(false);
    const groups = groupsByModel([
      { name: "budget", strategy: "fallback", targets: ["a/x", "a/x", "b/y"] },
      { name: "fast", strategy: "round-robin", targets: ["a/x"] },
    ]);
    expect(groups.get("a/x")).toEqual(["budget", "fast"]);
    expect(groups.get("b/y")).toEqual(["budget"]);
  });

  it("replaces or adds one binding", () => {
    const one: Binding = { harness: "codex", model: "a/x", configPath: "", updatedAt: "" };
    expect(withBinding([one], { ...one, model: "b/y" })).toEqual([{ ...one, model: "b/y" }]);
    expect(withBinding([one], { ...one, harness: "dsh" })).toHaveLength(2);
  });

  it("sums session usage by model, most expensive first", () => {
    const rows = usageByModel([
      session("a/x", { inputTokens: 100, outputTokens: 10, costUsd: 0.5 }),
      session("a/x", { inputTokens: 50, outputTokens: 5, costUsd: 0.25 }),
      session("b/y", { inputTokens: 1000, outputTokens: 100, costUsd: 2 }),
      session("c/z", { inputTokens: 0, outputTokens: 0, costUsd: 0 }),
      session("d/w"),
    ]);
    expect(rows.map((r) => r.model)).toEqual(["b/y", "a/x"]);
    expect(rows[1]).toEqual({ model: "a/x", sessions: 2, inputTokens: 150, outputTokens: 15, costUsd: 0.75 });
    expect(usageTotal(rows)).toMatchObject({ sessions: 3, inputTokens: 1150, costUsd: 2.75 });
  });
});

describe("status rows", () => {
  const provider = (partial: Partial<Provider>): Provider => ({
    id: "p",
    kind: "openai",
    name: "P",
    baseUrl: "",
    enabled: true,
    hasKey: true,
    models: ["m"],
    status: "ok",
    lastError: "",
    ...partial,
  });
  const sub = (partial: Partial<Subscription>): Subscription => ({ kind: "claude", name: "Claude", providerId: "claude", enabled: true, accounts: [], models: [], ...partial });

  it("lists the gateway, signed-in subscriptions, then API providers", () => {
    const rows = statusRows(
      { status: "running", addr: "127.0.0.1:3425", version: "1.2" },
      false,
      [sub({ accounts: [{ user: "me@x", plan: "max", enabled: true, needsSignIn: true }], models: ["s"] }), sub({ kind: "codex", name: "ChatGPT", providerId: "" })],
      [provider({ id: "claude", subscription: "claude" }), provider({ id: "openai", name: "OpenAI", status: "error", lastError: "401" })],
    );
    expect(rows.map((r) => r.id)).toEqual(["gateway", "sub-claude", "openai"]);
    expect(rows[0]).toMatchObject({ health: "operational", detail: "127.0.0.1:3425 · v1.2" });
    expect(rows[1]).toMatchObject({ health: "outage", error: "Sign in again: me@x", models: 1 });
    expect(rows[2]).toMatchObject({ health: "outage", error: "401" });
  });

  it("marks an unreachable gateway as an error", () => {
    expect(statusRows(null, true, [], [])[0]).toMatchObject({ health: "outage" });
  });
});
