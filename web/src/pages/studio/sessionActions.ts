import type { Session, VMState } from "../../api/types";

/** The VM runs again (start, wake, resume): the session list is out of date. */
export function becameRunning(previous: VMState, next: VMState): boolean {
  return next === "RUNNING" && previous !== "RUNNING";
}

/** An ended shell has no transcript, so deleting it loses nothing and needs no confirmation. */
export function deletesWithoutConfirm(session: Pick<Session, "harness" | "live">): boolean {
  return session.harness === "shell" && !session.live;
}
