import { ApiError, NetworkError } from "../api/http";
import type { VMStatus } from "../api/types";

/**
 * True when the request can have ended before the server answered: a cut
 * connection, or `502`, `504`, or `524` from a proxy in front of LydiaBox
 * (Cloudflare ends a request without an answer after 100 seconds). The
 * server can still do the work; the VM status tells.
 */
export function maybeCut(err: unknown): boolean {
  if (err instanceof NetworkError) {
    return true;
  }
  return err instanceof ApiError && (err.status === 502 || err.status === 504 || err.status === 524);
}

/**
 * True when the VM status shows that a hibernate or stop continues on the
 * server, or that it ended as asked: the page then shows the status, not
 * a failure.
 */
export function actionContinues(action: "hibernate" | "stop", detail: VMStatus): boolean {
  if (action === "hibernate") {
    return detail.transition === "pausing" || detail.state === "PAUSED";
  }
  return detail.transition === "stopping" || detail.state === "STOPPED";
}
