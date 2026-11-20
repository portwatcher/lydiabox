import type { Session, VMState } from "../../api/types";

/**
 * What the bottom of the chat view offers: the prompt field ("composer"),
 * "Continue session" ("continue"), the Start or Resume button of the VM
 * ("wake"), or only the state line ("none").
 */
export type ChatFooter = "composer" | "continue" | "wake" | "none";

/**
 * The bottom of the chat view for a session.
 *
 * A session stored as running that is not live has a terminal that can
 * still run in the guest (a pause or a host restart detached it). A new
 * process would replace it, so there is no "Continue session": the open
 * of the session already asked for the wake, and the host attaches the
 * terminal again when the VM runs. While the VM starts or a pause or stop
 * runs, and while the VM runs but the terminal is not attached yet, only
 * the state line shows. A paused or stopped VM with no start in progress
 * (a user stop, or a start that failed) gets the Start or Resume button.
 *
 * A provisional session has no conversation to continue.
 */
export function chatFooter(session: Pick<Session, "status" | "provisional">, live: boolean, vmState: VMState, busy: boolean): ChatFooter {
  if (live) {
    return "composer";
  }
  if (session.status === "running") {
    if (!busy && (vmState === "PAUSED" || vmState === "STOPPED")) {
      return "wake";
    }
    return "none";
  }
  if (session.provisional) {
    return "none";
  }
  return "continue";
}
