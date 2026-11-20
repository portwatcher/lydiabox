import type { ErrorEnvelope } from "./types";

/** An HTTP error from the API, decoded from the error envelope when present. */
export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  /** Seconds from `Retry-After`, set on `429 rate_limited`. */
  readonly retryAfter: number | undefined;

  constructor(status: number, code: string, message: string, retryAfter?: number) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
    this.retryAfter = retryAfter;
  }
}

/** The request never reached the server or the response was cut off. */
export class NetworkError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "NetworkError";
  }
}

export type QueryValue = string | number | boolean | undefined | null;
export type Query = Record<string, QueryValue>;

export interface RequestOptions {
  method?: "GET" | "POST" | "PUT" | "PATCH" | "DELETE" | "HEAD";
  query?: Query;
  /** JSON-encoded unless it is FormData. */
  body?: unknown;
  signal?: AbortSignal;
}

export function isErrorEnvelope(value: unknown): value is ErrorEnvelope {
  if (typeof value !== "object" || value === null || !("error" in value)) {
    return false;
  }
  const inner = value.error;
  return (
    typeof inner === "object" &&
    inner !== null &&
    "code" in inner &&
    typeof inner.code === "string" &&
    "message" in inner &&
    typeof inner.message === "string"
  );
}

/** Builds `path?query`, dropping undefined, null, and empty values. */
export function buildUrl(path: string, query?: Query): string {
  if (!query) {
    return path;
  }
  const params = new URLSearchParams();
  for (const [key, value] of Object.entries(query)) {
    if (value === undefined || value === null || value === "") {
      continue;
    }
    params.append(key, String(value));
  }
  const qs = params.toString();
  return qs ? `${path}?${qs}` : path;
}

const fallbackCodes: Record<number, string> = {
  400: "bad_request",
  401: "unauthorized",
  403: "forbidden",
  404: "not_found",
  409: "conflict",
  413: "too_large",
  415: "unsupported_media_type",
  429: "rate_limited",
  501: "not_implemented",
  502: "unavailable",
  503: "unavailable",
  504: "unavailable",
};

/** Decodes an error response body into an ApiError. */
export async function errorFromResponse(res: Response): Promise<ApiError> {
  const retryHeader = res.headers.get("Retry-After");
  const retryAfter = retryHeader ? Number.parseInt(retryHeader, 10) : undefined;
  const retry = retryAfter !== undefined && Number.isFinite(retryAfter) ? retryAfter : undefined;
  let body: unknown;
  try {
    body = await res.json();
  } catch {
    body = null;
  }
  if (isErrorEnvelope(body)) {
    return new ApiError(res.status, body.error.code, body.error.message, retry);
  }
  const code = fallbackCodes[res.status] ?? "internal";
  return new ApiError(res.status, code, res.statusText || `HTTP ${res.status}`, retry);
}

export type FetchLike = (input: string, init?: RequestInit) => Promise<Response>;

/** Waits `ms`; rejects with an AbortError when `signal` aborts first. */
export type Sleep = (ms: number, signal?: AbortSignal) => Promise<void>;

/**
 * The longest time that one request waits for a VM start. The server ends
 * a start after 10 minutes at the provider and 2 minutes for the guest.
 */
export const STARTING_LIMIT_MS = 15 * 60_000;

/**
 * Seconds to wait before the request is sent again, when the response says
 * that the VM still starts (`503` with `Retry-After`, docs/api.md section 1);
 * else null.
 */
export function startingRetry(res: Response): number | null {
  if (res.status !== 503) {
    return null;
  }
  const seconds = Number.parseInt(res.headers.get("Retry-After") ?? "", 10);
  return Number.isFinite(seconds) && seconds > 0 ? Math.min(seconds, 30) : null;
}

const defaultSleep: Sleep = (ms, signal) =>
  new Promise((resolve, reject) => {
    if (signal?.aborted) {
      reject(new DOMException("Aborted", "AbortError"));
      return;
    }
    const onAbort = () => {
      clearTimeout(id);
      reject(new DOMException("Aborted", "AbortError"));
    };
    const id = setTimeout(() => {
      signal?.removeEventListener("abort", onAbort);
      resolve();
    }, ms);
    signal?.addEventListener("abort", onAbort, { once: true });
  });

export class HttpClient {
  private fetchImpl: FetchLike;
  private sleep: Sleep;
  private unauthorizedListeners = new Set<() => void>();

  constructor(fetchImpl?: FetchLike, sleep?: Sleep) {
    this.fetchImpl = fetchImpl ?? ((input, init) => globalThis.fetch(input, init));
    this.sleep = sleep ?? defaultSleep;
  }

  setFetch(fetchImpl: FetchLike): void {
    this.fetchImpl = fetchImpl;
  }

  /** Registers a listener for `401` responses (the session expired). */
  onUnauthorized(listener: () => void): () => void {
    this.unauthorizedListeners.add(listener);
    return () => {
      this.unauthorizedListeners.delete(listener);
    };
  }

  async request<T>(path: string, options: RequestOptions = {}): Promise<T> {
    const res = await this.send(path, options);
    if (res.status === 204 || options.method === "HEAD") {
      return undefined as T;
    }
    const text = await res.text();
    if (text === "") {
      return undefined as T;
    }
    try {
      return JSON.parse(text) as T;
    } catch {
      throw new ApiError(res.status, "internal", "The server sent a response that is not JSON.");
    }
  }

  /**
   * Sends a request and returns the raw response; non-2xx statuses throw
   * ApiError. While the server answers that the VM still starts, the
   * request is sent again after `Retry-After`, up to STARTING_LIMIT_MS: a
   * page then waits for the start and does not show a failure.
   */
  async send(path: string, options: RequestOptions = {}): Promise<Response> {
    const started = Date.now();
    for (;;) {
      const res = await this.sendOnce(path, options);
      const wait = startingRetry(res);
      if (wait === null || Date.now() - started >= STARTING_LIMIT_MS) {
        return this.checked(res);
      }
      void res.body?.cancel().catch(() => undefined);
      await this.sleep(wait * 1000, options.signal);
    }
  }

  private async sendOnce(path: string, options: RequestOptions): Promise<Response> {
    const headers: Record<string, string> = { Accept: "application/json" };
    let body: BodyInit | undefined;
    if (options.body instanceof FormData) {
      body = options.body;
    } else if (options.body !== undefined) {
      headers["Content-Type"] = "application/json";
      body = JSON.stringify(options.body);
    }
    let res: Response;
    try {
      res = await this.fetchImpl(buildUrl(path, options.query), {
        method: options.method ?? "GET",
        headers,
        body,
        credentials: "same-origin",
        signal: options.signal,
      });
    } catch (err) {
      if (err instanceof DOMException && err.name === "AbortError") {
        throw err;
      }
      throw new NetworkError("Unable to reach the LydiaBox server.");
    }
    return res;
  }

  private async checked(res: Response): Promise<Response> {
    if (!res.ok) {
      const error = await errorFromResponse(res);
      if (res.status === 401) {
        this.unauthorizedListeners.forEach((listener) => listener());
      }
      throw error;
    }
    return res;
  }

  get<T>(path: string, query?: Query, signal?: AbortSignal): Promise<T> {
    return this.request<T>(path, { query, signal });
  }

  post<T>(path: string, body?: unknown, query?: Query): Promise<T> {
    return this.request<T>(path, { method: "POST", body, query });
  }

  put<T>(path: string, body?: unknown, query?: Query): Promise<T> {
    return this.request<T>(path, { method: "PUT", body, query });
  }

  patch<T>(path: string, body?: unknown): Promise<T> {
    return this.request<T>(path, { method: "PATCH", body });
  }

  async delete(path: string, query?: Query): Promise<void> {
    await this.request<undefined>(path, { method: "DELETE", query });
  }
}

/** The shared client every API module uses. */
export const http = new HttpClient();

/** Escapes one path segment, such as a session ID (`codex:019a...`). */
export function seg(value: string): string {
  return encodeURIComponent(value);
}
