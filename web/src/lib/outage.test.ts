import { describe, expect, it } from "vitest";
import { ApiError, NetworkError } from "../api/http";
import { splitLink, statusOutage } from "./outage";

describe("statusOutage", () => {
  it("reads the codes of the control plane at once", () => {
    expect(statusOutage(new ApiError(503, "cell_stopped", "the agent computer is not running"), false)).toEqual({
      kind: "stopped",
      message: "The agent computer is not running.",
    });
    expect(statusOutage(new ApiError(503, "cell_starting", ""), false)).toEqual({
      kind: "starting",
      message: "The agent computer is starting.",
    });
  });

  it("reads the answers of the control plane before the codes", () => {
    const stopped = new ApiError(503, "unavailable", "the agent computer is not running; start it from the dashboard at //lydiabox.com/");
    expect(statusOutage(stopped, false)).toEqual({
      kind: "stopped",
      message: "The agent computer is not running; start it from the dashboard at //lydiabox.com/.",
    });
    const starting = new ApiError(503, "unavailable", "the agent computer is starting; try again in a few seconds");
    expect(statusOutage(starting, false)?.kind).toBe("starting");
  });

  it("makes a network error or a 5xx an unknown state only after the grace time", () => {
    expect(statusOutage(new NetworkError("down"), false)).toBeNull();
    expect(statusOutage(new NetworkError("down"), true)?.kind).toBe("unknown");
    expect(statusOutage(new ApiError(502, "unavailable", "bad gateway"), false)).toBeNull();
    expect(statusOutage(new ApiError(500, "internal", "boom"), true)?.kind).toBe("unknown");
  });

  it("ignores no error and client errors", () => {
    expect(statusOutage(null, true)).toBeNull();
    expect(statusOutage(new ApiError(401, "unauthorized", "no"), true)).toBeNull();
    expect(statusOutage(new ApiError(404, "not_found", "no"), true)).toBeNull();
  });
});

describe("splitLink", () => {
  it("takes the dashboard link out of the server text", () => {
    expect(splitLink("The agent computer is not running; start it from the dashboard at //lydiabox.com/.")).toEqual({
      text: "The agent computer is not running; start it from the dashboard.",
      href: "//lydiabox.com/",
    });
    expect(splitLink("The agent computer is starting.")).toEqual({ text: "The agent computer is starting.", href: null });
  });
});
