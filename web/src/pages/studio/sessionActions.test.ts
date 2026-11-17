import { describe, expect, it } from "vitest";
import { HARNESSES } from "../../api/types";
import { deletesWithoutConfirm, listOutOfDate } from "./sessionActions";

describe("listOutOfDate", () => {
  it("is true on every change of the VM state", () => {
    expect(listOutOfDate("PAUSED", "RUNNING")).toBe(true);
    expect(listOutOfDate("RUNNING", "STOPPED")).toBe(true);
    expect(listOutOfDate("RUNNING", "PAUSED")).toBe(true);
    expect(listOutOfDate("RUNNING", "RUNNING")).toBe(false);
  });
});

describe("deletesWithoutConfirm", () => {
  it("deletes an ended shell at once and asks for anything else", () => {
    expect(deletesWithoutConfirm({ harness: "shell", live: false })).toBe(true);
    expect(deletesWithoutConfirm({ harness: "shell", live: true })).toBe(false);
    expect(deletesWithoutConfirm({ harness: HARNESSES[0], live: false })).toBe(false);
  });
});
