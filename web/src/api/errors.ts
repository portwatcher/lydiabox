import { ApiError, NetworkError } from "./http";

/**
 * Why a subsystem cannot answer:
 * - `missing`: the route is not registered in this build (404 on a route).
 * - `not_implemented`: the route exists but the subsystem is not built in (501).
 * - `unavailable`: a dependency such as the VM or magpie is not running (502-504).
 */
export type Availability = "missing" | "not_implemented" | "unavailable";

/**
 * Classifies an error from a subsystem's primary route. A `404` on a list or
 * status route means the route itself is absent, so it counts as `missing`.
 */
export function availabilityOf(err: unknown): Availability | null {
  if (!(err instanceof ApiError)) {
    return null;
  }
  if (err.status === 501 || err.code === "not_implemented") {
    return "not_implemented";
  }
  if (err.status === 503 || err.status === 502 || err.status === 504 || err.code === "unavailable") {
    return "unavailable";
  }
  if (err.status === 404) {
    return "missing";
  }
  return null;
}

/** True when the request failed because the item (not the route) is gone. */
export function isNotFound(err: unknown): boolean {
  return err instanceof ApiError && err.status === 404;
}

export function isConflict(err: unknown): boolean {
  return err instanceof ApiError && err.status === 409;
}

/**
 * Turns an error into one sentence for the person: what failed, then what
 * to do. `failed` is the in-context failure, such as "File not saved".
 */
export function describeError(err: unknown, failed: string): string {
  if (err instanceof NetworkError) {
    return `${failed}. Unable to reach the server; check that LydiaBox is running, then try again.`;
  }
  if (err instanceof ApiError) {
    switch (err.code) {
      case "rate_limited":
        return `${failed}. Too many attempts; wait ${err.retryAfter ?? 60} seconds, then try again.`;
      case "unavailable":
        return `${failed}. ${sentence(err.message) || "A required service is not running."} Try again when it is back.`;
      case "not_implemented":
        return `${failed}. This build of LydiaBox does not include this feature.`;
      case "unauthorized":
        return `${failed}. Your session ended; sign in again.`;
      case "forbidden":
        return `${failed}. ${sentence(err.message) || "Access was denied."}`;
      case "too_large":
        return `${failed}. ${sentence(err.message) || "It is over the size limit."}`;
      case "internal":
        return `${failed}. The server reported an error; check the LydiaBox logs, then try again.`;
      default:
        return `${failed}. ${sentence(err.message)}`;
    }
  }
  if (err instanceof Error && err.message) {
    return `${failed}. ${sentence(err.message)}`;
  }
  return `${failed}. Try again.`;
}

function sentence(message: string): string {
  const trimmed = message.trim();
  if (!trimmed) {
    return "";
  }
  const capitalized = trimmed.charAt(0).toUpperCase() + trimmed.slice(1);
  return /[.!?]$/.test(capitalized) ? capitalized : `${capitalized}.`;
}
