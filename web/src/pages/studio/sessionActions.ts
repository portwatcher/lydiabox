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

/**
 * The text of the delete dialog. A delete without the transcript keeps the
 * files in the VM, and the session stays out of the list; a shell has no
 * transcript.
 */
export function deleteText(session: Pick<Session, "harness">): { body: string; purgeLabel: string | null } {
  if (session.harness === "shell") {
    return { body: "The terminal stops and the shell leaves the list.", purgeLabel: null };
  }
  return {
    body: "The terminal stops and the session leaves the list. Its transcript files stay in the VM.",
    purgeLabel: "Also delete the transcript files (cannot be undone)",
  };
}
