import { describe, expect, it } from "vitest";
import { ApiError, NetworkError } from "../api/http";
import type { VMStatus } from "../api/types";
import { actionContinues, maybeCut } from "./vmAction";

const base: VMStatus = {
  state: "RUNNING",
  driver: "instabox",
  cpus: 2,
  memMb: 4096,
  bootedAt: "",
  leases: { pty: 0, desktop: 0 },
  idleTimeoutSeconds: 0,
  idleSince: "",
};

describe("maybeCut", () => {
  it("accepts a cut connection and the gateway errors of a proxy", () => {
    expect(maybeCut(new NetworkError("x"))).toBe(true);
    expect(maybeCut(new ApiError(524, "internal", ""))).toBe(true);
    expect(maybeCut(new ApiError(502, "unavailable", "Bad Gateway"))).toBe(true);
    expect(maybeCut(new ApiError(504, "unavailable", "Gateway Timeout"))).toBe(true);
  });

  it("refuses an answer of the server", () => {
    expect(maybeCut(new ApiError(409, "conflict", "hibernate while PAUSED"))).toBe(false);
    expect(maybeCut(new ApiError(503, "unavailable", "guest down"))).toBe(false);
    expect(maybeCut(new Error("x"))).toBe(false);
  });
});

describe("actionContinues", () => {
  it("reads the transition and the end state", () => {
    expect(actionContinues("stop", { ...base, transition: "stopping" })).toBe(true);
    expect(actionContinues("stop", { ...base, state: "STOPPED" })).toBe(true);
    expect(actionContinues("stop", base)).toBe(false);
    expect(actionContinues("stop", { ...base, transition: "pausing" })).toBe(false);
    expect(actionContinues("hibernate", { ...base, transition: "pausing" })).toBe(true);
    expect(actionContinues("hibernate", { ...base, state: "PAUSED" })).toBe(true);
    expect(actionContinues("hibernate", base)).toBe(false);
  });
});
