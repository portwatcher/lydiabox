import { describe, expect, it } from "vitest";
import type { Model, Subscription } from "../../api/types";
import { SETTLE_DELAYS_MS, subscriptionListed } from "./settle";

const sub = (providerId: string, models: string[]): Subscription => ({
  kind: "codex",
  name: "ChatGPT",
  providerId,
  enabled: true,
  accounts: [{ user: "a@example.com", plan: "plus", enabled: true, needsSignIn: false }],
  models,
});
const model = (id: string): Model => ({ id, provider: id.split("/")[0], name: "", contextWindow: 0 });

describe("subscriptionListed", () => {
  it("waits while magpie has not listed the models of the account", () => {
    expect(subscriptionListed("codex", undefined, undefined)).toBe(false);
    expect(subscriptionListed("codex", [sub("", [])], [])).toBe(false);
    expect(subscriptionListed("codex", [sub("codex", [])], [model("codex/gpt-5.2")])).toBe(false);
    expect(subscriptionListed("codex", [sub("codex", ["gpt-5.2"])], [])).toBe(false);
    expect(subscriptionListed("codex", [sub("codex", ["gpt-5.2"])], [model("openai/gpt-5.2")])).toBe(false);
  });

  it("stops when the subscription and the catalog both show models", () => {
    expect(subscriptionListed("codex", [sub("codex", ["gpt-5.2"])], [model("codex/gpt-5.2")])).toBe(true);
    expect(subscriptionListed("claude", [sub("codex", ["gpt-5.2"])], [model("codex/gpt-5.2")])).toBe(false);
  });

  it("polls for at most half a minute, in increasing steps", () => {
    expect(SETTLE_DELAYS_MS.at(-1)).toBeLessThanOrEqual(30_000);
    expect([...SETTLE_DELAYS_MS].sort((a, b) => a - b)).toEqual(SETTLE_DELAYS_MS);
  });
});
