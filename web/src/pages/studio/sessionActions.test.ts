import { describe, expect, it } from "vitest";
import { HARNESSES } from "../../api/types";
import { becameRunning, deletesWithoutConfirm } from "./sessionActions";

describe("becameRunning", () => {
  it("is true only on the change to RUNNING", () => {
    expect(becameRunning("PAUSED", "RUNNING")).toBe(true);
    expect(becameRunning("BOOTING", "RUNNING")).toBe(true);
    expect(becameRunning("STOPPED", "RUNNING")).toBe(true);
    expect(becameRunning("RUNNING", "RUNNING")).toBe(false);
    expect(becameRunning("RUNNING", "PAUSED")).toBe(false);
  });
});

describe("deletesWithoutConfirm", () => {
  it("deletes an ended shell at once and asks for anything else", () => {
    expect(deletesWithoutConfirm({ harness: "shell", live: false })).toBe(true);
    expect(deletesWithoutConfirm({ harness: "shell", live: true })).toBe(false);
    expect(deletesWithoutConfirm({ harness: HARNESSES[0], live: false })).toBe(false);
  });
});
