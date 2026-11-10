import { describe, expect, it } from "vitest";
import { s3Endpoint } from "./s3Endpoint";

describe("s3Endpoint", () => {
  it("replaces a wildcard host with the page host", () => {
    expect(s3Endpoint("0.0.0.0:8090", "box.example.com")).toEqual({ url: "http://box.example.com:8090", serverOnly: false });
    expect(s3Endpoint(":8090", "10.0.0.5")).toEqual({ url: "http://10.0.0.5:8090", serverOnly: false });
    expect(s3Endpoint("[::]:8090", "fd00::1")).toEqual({ url: "http://[fd00::1]:8090", serverOnly: false });
  });

  it("keeps a concrete host and flags loopback", () => {
    expect(s3Endpoint("127.0.0.1:8090", "box.example.com")).toEqual({ url: "http://127.0.0.1:8090", serverOnly: true });
    expect(s3Endpoint("[::1]:8090", "box.example.com")).toEqual({ url: "http://[::1]:8090", serverOnly: true });
    expect(s3Endpoint("192.168.1.2:9000", "box")).toEqual({ url: "http://192.168.1.2:9000", serverOnly: false });
  });
});
