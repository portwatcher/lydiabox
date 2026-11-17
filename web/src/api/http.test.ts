import { describe, expect, it, vi } from "vitest";
import { availabilityOf, describeError } from "./errors";
import { ApiError, HttpClient, NetworkError, buildUrl, isErrorEnvelope } from "./http";
import { buildUploadForm } from "./files";
import { sessions } from "./domains";

function jsonResponse(status: number, body: unknown, headers: Record<string, string> = {}): Response {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json", ...headers } });
}

describe("buildUrl", () => {
  it("drops empty values and encodes the rest", () => {
    expect(buildUrl("/api/v1/fs/workspace/list", { path: "a b/c", x: undefined, y: "", z: null })).toBe(
      "/api/v1/fs/workspace/list?path=a+b%2Fc",
    );
    expect(buildUrl("/x", { recursive: true, limit: 5 })).toBe("/x?recursive=true&limit=5");
    expect(buildUrl("/x")).toBe("/x");
  });
});

describe("HttpClient", () => {
  it("decodes JSON bodies and sends JSON with credentials", async () => {
    const fetch = vi.fn(async (_input: string, _init?: RequestInit) => jsonResponse(200, { authenticated: true }));
    const client = new HttpClient(fetch);
    await expect(client.post("/api/v1/auth/login", { token: "t" })).resolves.toEqual({ authenticated: true });
    const call = fetch.mock.calls[0];
    expect(call?.[0]).toBe("/api/v1/auth/login");
    const init = call?.[1];
    expect(init?.method).toBe("POST");
    expect(init?.credentials).toBe("same-origin");
    expect(init?.body).toBe(JSON.stringify({ token: "t" }));
    expect(new Headers(init?.headers).get("Content-Type")).toBe("application/json");
  });

  it("returns undefined for 204", async () => {
    const client = new HttpClient(async () => new Response(null, { status: 204 }));
    await expect(client.delete("/api/v1/sessions/x")).resolves.toBeUndefined();
  });

  it("decodes the error envelope", async () => {
    const client = new HttpClient(async () =>
      jsonResponse(409, { error: { code: "conflict", message: "file changed since it was read" } }),
    );
    const err = await client.get("/x").catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect(err).toMatchObject({ status: 409, code: "conflict", message: "file changed since it was read" });
  });

  it("falls back to a status-derived code when the body is not an envelope", async () => {
    const client = new HttpClient(async () => new Response("<html>", { status: 503, statusText: "Service Unavailable" }));
    const err = await client.get("/x").catch((e: unknown) => e);
    expect(err).toMatchObject({ status: 503, code: "unavailable" });
    expect(availabilityOf(err)).toBe("unavailable");
  });

  it("reads Retry-After on 429", async () => {
    const client = new HttpClient(async () =>
      jsonResponse(429, { error: { code: "rate_limited", message: "too many attempts" } }, { "Retry-After": "42" }),
    );
    const err = await client.post("/api/v1/auth/login", { token: "x" }).catch((e: unknown) => e);
    expect(err).toMatchObject({ retryAfter: 42 });
    expect(describeError(err, "Not signed in")).toContain("wait 42 seconds");
  });

  it("shows the text of a harness that is not installed", () => {
    const err = new ApiError(409, "not_installed", 'Claude Code is not installed in the VM (the command "claude" was not found)');
    expect(describeError(err, "Session not started")).toBe(
      'Session not started. Claude Code is not installed in the VM (the command "claude" was not found).',
    );
  });

  it("notifies unauthorized listeners on 401", async () => {
    const client = new HttpClient(async () => jsonResponse(401, { error: { code: "unauthorized", message: "no" } }));
    const listener = vi.fn();
    const off = client.onUnauthorized(listener);
    await client.get("/x").catch(() => undefined);
    expect(listener).toHaveBeenCalledTimes(1);
    off();
    await client.get("/x").catch(() => undefined);
    expect(listener).toHaveBeenCalledTimes(1);
  });

  it("wraps fetch failures in NetworkError", async () => {
    const client = new HttpClient(async () => {
      throw new TypeError("Failed to fetch");
    });
    const err = await client.get("/x").catch((e: unknown) => e);
    expect(err).toBeInstanceOf(NetworkError);
    expect(describeError(err, "Folder not loaded")).toMatch(/^Folder not loaded\. Unable to reach the server/);
  });
});

describe("availabilityOf", () => {
  it("classifies missing routes, unbuilt subsystems, and stopped dependencies", () => {
    expect(availabilityOf(new ApiError(404, "not_found", "no route"))).toBe("missing");
    expect(availabilityOf(new ApiError(501, "not_implemented", "x"))).toBe("not_implemented");
    expect(availabilityOf(new ApiError(503, "unavailable", "VM is not running"))).toBe("unavailable");
    expect(availabilityOf(new ApiError(400, "bad_request", "x"))).toBeNull();
    expect(availabilityOf(new Error("x"))).toBeNull();
  });
});

describe("isErrorEnvelope", () => {
  it("accepts only the documented shape", () => {
    expect(isErrorEnvelope({ error: { code: "x", message: "y" } })).toBe(true);
    expect(isErrorEnvelope({ error: "x" })).toBe(false);
    expect(isErrorEnvelope(null)).toBe(false);
  });
});

describe("endpoints", () => {
  it("escapes session IDs as one path segment", () => {
    expect(sessions.wsPath("codex:019a/6f")).toBe("/api/v1/sessions/codex%3A019a%2F6f/ws");
  });

  it("names every multipart part `file` with the relative path as filename", () => {
    const form = buildUploadForm([
      { file: new Blob(["a"]), name: "photos/2026/a.txt" },
      { file: new Blob(["b"]), name: "b.txt" },
    ]);
    const parts = form.getAll("file");
    expect(parts).toHaveLength(2);
    expect(parts.map((p) => (p instanceof File ? p.name : ""))).toEqual(["photos/2026/a.txt", "b.txt"]);
  });
});
