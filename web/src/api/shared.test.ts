import { describe, expect, it, vi } from "vitest";
import { sharedRequests } from "./shared";

function deferred<T>() {
  let resolve!: (v: T) => void;
  let reject!: (e: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

const tick = () => new Promise((r) => setTimeout(r, 0));

describe("sharedRequests", () => {
  it("sends one request for callers in the same task", async () => {
    const shared = sharedRequests<string>();
    const d = deferred<string>();
    const run = vi.fn(() => d.promise);
    const a = shared("workspace\n", run, new AbortController().signal);
    const b = shared("workspace\n", run);
    const other = shared("home\n", () => Promise.resolve("home"));
    d.resolve("list");
    await expect(Promise.all([a, b, other])).resolves.toEqual(["list", "list", "home"]);
    expect(run).toHaveBeenCalledTimes(1);
  });

  it("sends a new request in a later task", async () => {
    const shared = sharedRequests<number>();
    let n = 0;
    const run = vi.fn(() => new Promise<number>(() => void n++));
    void shared("k", run);
    await tick();
    void shared("k", run);
    expect(run).toHaveBeenCalledTimes(2);
    expect(n).toBe(2);
  });

  it("aborts only the caller that aborts, and the request when all abort", async () => {
    const shared = sharedRequests<string>();
    const seen: { signal?: AbortSignal } = {};
    const d = deferred<string>();
    const run = (s: AbortSignal) => {
      seen.signal = s;
      s.addEventListener("abort", () => d.reject(new DOMException("aborted", "AbortError")));
      return d.promise;
    };
    const ca = new AbortController();
    const cb = new AbortController();
    const a = shared("k", run, ca.signal);
    const b = shared("k", run, cb.signal);
    ca.abort();
    await expect(a).rejects.toMatchObject({ name: "AbortError" });
    expect(seen.signal?.aborted).toBe(false);
    cb.abort();
    await expect(b).rejects.toMatchObject({ name: "AbortError" });
    expect(seen.signal?.aborted).toBe(true);
  });

  it("passes a failure to every caller and rejects an aborted signal at once", async () => {
    const shared = sharedRequests<string>();
    const run = vi.fn(() => Promise.reject(new Error("503")));
    const a = shared("k", run);
    const b = shared("k", run);
    await expect(a).rejects.toThrow("503");
    await expect(b).rejects.toThrow("503");
    const gone = new AbortController();
    gone.abort();
    await expect(shared("j", run, gone.signal)).rejects.toMatchObject({ name: "AbortError" });
    expect(run).toHaveBeenCalledTimes(1);
  });
});
