import { describe, expect, it } from "vitest";
import { chatFooter } from "./chatFooter";

const detached = { status: "running" as const };

describe("chatFooter", () => {
  it("shows the prompt field for a live session", () => {
    expect(chatFooter(detached, true, "RUNNING", false)).toBe("composer");
    expect(chatFooter({ status: "running", provisional: true }, true, "RUNNING", false)).toBe("composer");
  });

  it("shows no button for a detached terminal while the VM starts or a transition runs", () => {
    expect(chatFooter(detached, false, "BOOTING", false)).toBe("none");
    expect(chatFooter(detached, false, "BOOTING", true)).toBe("none");
    expect(chatFooter(detached, false, "PAUSED", true)).toBe("none");
    expect(chatFooter(detached, false, "STOPPED", true)).toBe("none");
  });

  it("offers Start or Resume for a detached terminal of a paused or stopped VM", () => {
    expect(chatFooter(detached, false, "PAUSED", false)).toBe("wake");
    expect(chatFooter(detached, false, "STOPPED", false)).toBe("wake");
    expect(chatFooter({ status: "running", provisional: true }, false, "PAUSED", false)).toBe("wake");
  });

  it("waits for the attach while the VM runs", () => {
    expect(chatFooter(detached, false, "RUNNING", false)).toBe("none");
    expect(chatFooter({ status: "running", provisional: true }, false, "RUNNING", false)).toBe("none");
  });

  it("continues an ended session that has a conversation", () => {
    expect(chatFooter({ status: "exited" }, false, "RUNNING", false)).toBe("continue");
    expect(chatFooter({ status: "idle" }, false, "PAUSED", false)).toBe("continue");
  });

  it("does not continue an ended provisional session", () => {
    expect(chatFooter({ status: "exited", provisional: true }, false, "RUNNING", false)).toBe("none");
    expect(chatFooter({ status: "exited", provisional: true }, false, "STOPPED", false)).toBe("none");
  });
});
