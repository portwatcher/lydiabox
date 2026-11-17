import { describe, expect, it } from "vitest";
import { ApiError } from "../../api/http";
import { failedNode, loadedNode, loadingNode, showsLoadError } from "./folderNode";

describe("folder tree node", () => {
  it("shows no error when the VM is stopped", () => {
    const stopped = new ApiError(503, "unavailable", "the VM was stopped; start it to use it");
    const node = failedNode(loadedNode(["src"]), stopped);
    expect(node.failure).toBe("unavailable");
    expect(showsLoadError(node)).toBe(false);
    // The folders loaded before the stop stay.
    expect(node.children).toEqual(["src"]);
  });

  it("shows an error for other failures", () => {
    expect(showsLoadError(failedNode(undefined, new ApiError(500, "internal", "boom")))).toBe(true);
    expect(showsLoadError(failedNode(undefined, new TypeError("Failed to fetch")))).toBe(true);
  });

  it("clears the failure on a new load", () => {
    const failed = failedNode(undefined, new ApiError(500, "internal", "boom"));
    expect(showsLoadError(loadingNode(failed))).toBe(false);
  });
});
