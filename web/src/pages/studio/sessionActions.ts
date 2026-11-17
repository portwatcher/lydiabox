import type { Session, VMState } from "../../api/types";

/**
 * A change of the VM state makes the session list out of date: a stop or
 * pause ends live terminals, and a start or resume lets the guest be
 * scanned again and terminals attach.
 */
export function listOutOfDate(previous: VMState, next: VMState): boolean {
  return previous !== next;
}

/** An ended shell has no transcript, so deleting it loses nothing and needs no confirmation. */
export function deletesWithoutConfirm(session: Pick<Session, "harness" | "live">): boolean {
  return session.harness === "shell" && !session.live;
}
