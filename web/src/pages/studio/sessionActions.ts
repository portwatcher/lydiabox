import type { Session, VMState } from "../../api/types";

/**
 * A change of the VM state makes the session list out of date: a stop or
 * pause ends live terminals, and a start or resume lets the guest be
 * scanned again and terminals attach.
 */
export function listOutOfDate(previous: VMState, next: VMState): boolean {
  return previous !== next;
}

/**
 * Whether the session list disagrees with the VM state: a cached list while
 * the VM runs (the guest was not scanned yet after a start or resume), a
 * list with a detached terminal (stored as running, not live) while the VM
 * runs (the host attaches it again in a moment), or, in the provider
 * storage mode, a fresh list while the VM does not run (it was scanned just
 * before the pause or stop). The page then loads it again soon, so the
 * "Cached" label and the sessions follow the VM state.
 */
export function listDisagrees(vmState: VMState, cached: boolean, providerMode: boolean, detached = false): boolean {
  if (vmState === "RUNNING") {
    return cached || detached;
  }
  return providerMode && !cached && (vmState === "PAUSED" || vmState === "STOPPED");
}

/** Whether a session of the list has a terminal that a pause or host restart detached. */
export function hasDetached(list: Pick<Session, "status" | "live">[]): boolean {
  return list.some((s) => s.status === "running" && !s.live);
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
