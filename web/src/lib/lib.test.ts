import { describe, expect, it } from "vitest";
import { buttonName, contentBox, toGuest, wheelPixels } from "./coords";
import { count, isZeroTime, timeAgo } from "./format";
import { baseName, crumbs, joinPath, parentPath, uploadName, validName } from "./paths";

const frame = { width: 1920, height: 1080 };

describe("coords", () => {
  it("letterboxes a 16:9 frame inside a taller box", () => {
    expect(contentBox({ left: 0, top: 0, width: 960, height: 1000 }, frame)).toEqual({ left: 0, top: 230, width: 960, height: 540 });
  });

  it("maps client points to guest pixels and rejects the bars", () => {
    const box = { left: 100, top: 50, width: 960, height: 1000 };
    expect(toGuest(100, 280, box, frame)).toEqual({ x: 0, y: 0 });
    expect(toGuest(580, 550, box, frame)).toEqual({ x: 960, y: 540 });
    expect(toGuest(1060, 820, box, frame)).toEqual({ x: 1919, y: 1079 });
    expect(toGuest(580, 100, box, frame)).toBeNull();
  });

  it("scales line and page wheel deltas", () => {
    expect(wheelPixels(0, 3, 1, 800)).toEqual({ dx: 0, dy: 120 });
    expect(wheelPixels(0, 1, 2, 800)).toEqual({ dx: 0, dy: 800 });
    expect(wheelPixels(5, -10, 0, 800)).toEqual({ dx: 5, dy: -10 });
  });

  it("names mouse buttons", () => {
    expect([0, 1, 2, 3].map(buttonName)).toEqual(["left", "middle", "right", null]);
  });
});

describe("paths", () => {
  it("joins and splits root-relative paths", () => {
    expect(joinPath("", "a.txt")).toBe("a.txt");
    expect(joinPath("src/", "/pkg")).toBe("src/pkg");
    expect(parentPath("src/pkg/a.go")).toBe("src/pkg");
    expect(parentPath("a.go")).toBe("");
    expect(baseName("src/pkg/a.go")).toBe("a.go");
    expect(crumbs("a/b/c")).toEqual([
      { name: "a", path: "a" },
      { name: "b", path: "a/b" },
      { name: "c", path: "a/b/c" },
    ]);
  });

  it("validates single names", () => {
    expect(validName("notes.md")).toBe(true);
    expect(["", " ", ".", "..", "a/b", "a\\b"].map(validName)).toEqual([false, false, false, false, false, false]);
  });

  it("prefers the dropped entry path, then webkitRelativePath", () => {
    expect(uploadName({ name: "a.txt", webkitRelativePath: "" }, "/photos/2026/a.txt")).toBe("photos/2026/a.txt");
    expect(uploadName({ name: "a.txt", webkitRelativePath: "photos/a.txt" })).toBe("photos/a.txt");
    expect(uploadName({ name: "a.txt" })).toBe("a.txt");
  });
});

describe("format", () => {
  it("treats the API zero time as never", () => {
    expect(isZeroTime("0001-01-01T00:00:00Z")).toBe(true);
    expect(timeAgo("0001-01-01T00:00:00Z", Date.now())).toBeNull();
    expect(timeAgo("2026-11-10T06:29:41Z", Date.parse("2026-11-10T06:30:00Z"))).toMatch(/19/);
  });

  it("picks plural forms", () => {
    expect(count(1, { one: "# file", other: "# files" })).toBe("1 file");
    expect(count(3, { one: "# file", other: "# files" })).toBe("3 files");
  });
});
