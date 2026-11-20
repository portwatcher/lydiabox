import { describe, expect, it } from "vitest";
import { HARNESSES } from "../../api/types";
import { deleteText, deletesWithoutConfirm, hasDetached, listDisagrees, listOutOfDate } from "./sessionActions";

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

describe("deleteText", () => {
  it("offers the transcript only for an agent session and says that it stays", () => {
    const agent = deleteText({ harness: "claude-code" });
    expect(agent.body).toContain("leaves the list");
    expect(agent.body).toContain("transcript files stay in the VM");
    expect(agent.purgeLabel).toBe("Also delete the transcript files (cannot be undone)");
    expect(deleteText({ harness: "shell" }).purgeLabel).toBeNull();
  });
});

describe("listDisagrees", () => {
  it("loads again a cached list of a running VM and a fresh list of a paused or stopped one", () => {
    expect(listDisagrees("RUNNING", true, true)).toBe(true);
    expect(listDisagrees("RUNNING", false, true)).toBe(false);
    expect(listDisagrees("PAUSED", false, true)).toBe(true);
    expect(listDisagrees("STOPPED", false, true)).toBe(true);
    expect(listDisagrees("STOPPED", true, true)).toBe(false);
    expect(listDisagrees("BOOTING", false, true)).toBe(false);
  });

  it("never in the juicefs mode, where the list is never cached", () => {
    expect(listDisagrees("PAUSED", false, false)).toBe(false);
  });

  it("loads again a list with a detached terminal while the VM runs", () => {
    expect(listDisagrees("RUNNING", false, true, true)).toBe(true);
    expect(listDisagrees("RUNNING", false, false, true)).toBe(true);
    expect(listDisagrees("BOOTING", false, true, true)).toBe(false);
    expect(listDisagrees("PAUSED", true, true, true)).toBe(false);
  });
});

describe("hasDetached", () => {
  it("finds a session stored as running that is not live", () => {
    expect(hasDetached([{ status: "running", live: false }])).toBe(true);
    expect(hasDetached([{ status: "running", live: true }, { status: "exited", live: false }, { status: "idle", live: false }])).toBe(false);
  });
});
