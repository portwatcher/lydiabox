import { describe, expect, it } from "vitest";
import { modelsFor } from "./useModelOptions";

const list = [
  { id: "opus", provider: "claude-code", name: "opus", contextWindow: 0 },
  { id: "gpt-6", provider: "codex", name: "gpt-6", contextWindow: 0 },
];

describe("modelsFor", () => {
  it("keeps the models of the harness in guest-native mode", () => {
    expect(modelsFor(list, "codex", true).map((m) => m.id)).toEqual(["gpt-6"]);
  });
  it("keeps every gateway model otherwise", () => {
    expect(modelsFor(list, "codex", false)).toHaveLength(2);
  });
});
