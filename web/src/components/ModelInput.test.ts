import { describe, expect, it } from "vitest";
import type { Model } from "../api/types";
import { listedModel } from "./ModelInput";

const codex: Model[] = [
  { id: "codex/gpt-5.5", provider: "codex", name: "gpt-5.5", contextWindow: 400000 },
  { id: "codex/gpt-5.5-mini", provider: "codex", name: "gpt-5.5-mini", contextWindow: 400000 },
];

describe("listedModel", () => {
  it("does not preselect a stored model that the gateway does not list", () => {
    expect(listedModel("claude/claude-opus-4-5", codex)).toBe("");
    expect(listedModel("claude/claude-opus-4-5", [])).toBe("");
  });

  it("preselects a stored model that the gateway lists", () => {
    expect(listedModel("codex/gpt-5.5", codex)).toBe("codex/gpt-5.5");
  });

  it("starts empty without a stored model", () => {
    expect(listedModel(undefined, codex)).toBe("");
  });
});
