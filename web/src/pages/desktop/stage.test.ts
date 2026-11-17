import { describe, expect, it } from "vitest";
import { desktopStage, shouldConnect, shouldDisconnect } from "./stage";
import type { StageInput } from "./stage";

const base: StageInput = { vmState: "RUNNING", pending: null, socket: null, stopped: false, live: false };

describe("desktop stage", () => {
  it("streams a running VM", () => {
    expect(shouldConnect(base)).toBe(true);
    expect(desktopStage(base)).toBe("connecting");
    expect(desktopStage({ ...base, socket: "open", live: true })).toBe("live");
  });

  it("waits for the video connection and reports a failed one", () => {
    const open = { ...base, socket: "open" as const };
    expect(desktopStage({ ...open, peer: "connecting" })).toBe("connecting");
    expect(desktopStage({ ...open, peer: "failed" })).toBe("videoFailed");
    expect(desktopStage({ ...open, peer: "connected", live: true })).toBe("live");
  });

  it("does not reconnect after the server said the VM pauses", () => {
    // The poll still says RUNNING: the page waits for a new status.
    const paused = { ...base, paused: true };
    expect(shouldConnect(paused)).toBe(false);
    expect(desktopStage(paused)).toBe("hibernating");
    expect(shouldConnect({ ...base, paused: false })).toBe(true);
  });

  it("shows a calm stopped state and does not reconnect after a user stop", () => {
    // The server ended the stream before the status poll saw the stop.
    const ended = { ...base, stopped: true };
    expect(shouldConnect(ended)).toBe(false);
    expect(desktopStage(ended)).toBe("stopped");
    // The poll then reports the stop.
    const stopped = { ...base, vmState: "STOPPED" as const };
    expect(shouldConnect(stopped)).toBe(false);
    expect(shouldDisconnect(stopped)).toBe(true);
    expect(desktopStage(stopped)).toBe("stopped");
    expect(desktopStage({ ...base, vmState: "PAUSED" })).toBe("hibernating");
  });

  it("closes the stream while a stop or hibernate runs", () => {
    for (const pending of ["stop", "hibernate"] as const) {
      const s = { ...base, pending, socket: "open" as const, live: true };
      expect(shouldDisconnect(s)).toBe(true);
      expect(shouldConnect({ ...s, socket: null })).toBe(false);
      expect(desktopStage(s)).toBe("stopping");
    }
  });

  it("connects again after an explicit start, with no reload", () => {
    const steps: StageInput[] = [
      { ...base, vmState: "STOPPED" },
      { ...base, vmState: "BOOTING", pending: "wake" },
      { ...base, vmState: "BOOTING" },
      base,
    ];
    expect(steps.map(desktopStage)).toEqual(["stopped", "starting", "starting", "connecting"]);
    expect(steps.map(shouldConnect)).toEqual([false, false, false, true]);
  });

  it("stop, start, stop from the page works each time", () => {
    for (let i = 0; i < 2; i++) {
      expect(shouldDisconnect({ vmState: "RUNNING", pending: "stop" })).toBe(true);
      expect(desktopStage({ ...base, vmState: "STOPPED" })).toBe("stopped");
      expect(shouldConnect({ ...base, vmState: "STOPPED" })).toBe(false);
      expect(shouldConnect(base)).toBe(true);
    }
  });

  it("offers a reconnect only when the socket gave up", () => {
    expect(desktopStage({ ...base, socket: "failed" })).toBe("lost");
    expect(desktopStage({ ...base, socket: "reconnecting" })).toBe("connecting");
  });
});
