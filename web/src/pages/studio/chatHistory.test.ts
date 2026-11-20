import { describe, expect, it } from "vitest";
import { historyFailed } from "./chatHistory";

describe("historyFailed", () => {
  it("keeps loading while the VM starts or resumes", () => {
    expect(historyFailed({ synced: false, error: "the machine does not run" }, "BOOTING")).toBe(false);
  });

  it("fails when the read failed and the VM is not in a start", () => {
    expect(historyFailed({ synced: false, error: "the VM is stopped" }, "STOPPED")).toBe(true);
    expect(historyFailed({ synced: false, error: "unavailable" }, "PAUSED")).toBe(true);
    expect(historyFailed({ synced: false, error: "unavailable" }, "RUNNING")).toBe(true);
  });

  it("does not fail without an error or after a read", () => {
    expect(historyFailed(null, "STOPPED")).toBe(false);
    expect(historyFailed({ synced: false, error: "" }, "STOPPED")).toBe(false);
    expect(historyFailed({ synced: true, error: "unavailable" }, "STOPPED")).toBe(false);
  });
});
