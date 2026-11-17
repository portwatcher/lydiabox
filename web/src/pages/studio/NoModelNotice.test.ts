import { describe, expect, it } from "vitest";
import { modelProblem } from "./NoModelNotice";

describe("modelProblem", () => {
  it("says no model is set up only for a loaded, empty list", () => {
    expect(modelProblem("loaded", 0)).toBe("none");
    expect(modelProblem("loaded", 2)).toBeNull();
  });

  it("says the list is not available while the gateway does not run, never 'none'", () => {
    expect(modelProblem("unavailable", 0)).toBe("unavailable");
  });

  it("says nothing while loading or without a models subsystem", () => {
    expect(modelProblem("loading", 0)).toBeNull();
    expect(modelProblem("absent", 0)).toBeNull();
  });
});
