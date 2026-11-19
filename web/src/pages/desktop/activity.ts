import type { DesktopMode } from "../../api/types";
import type { AgentPhase } from "../../components/product/AgentStatus";
import { describeAction } from "./overlay";
import type { FeedItem } from "./overlay";

/** The agent counts as working until this long after its last action. */
export const WORKING_MS = 6000;
/** A pause longer than this ends a run: the next action starts a new one. */
export const RUN_GAP_MS = 20_000;

export interface AgentActivity {
  phase: AgentPhase;
  /** The current activity, or what the phase means. */
  label: string;
  /** Start of the current run (client time), when there is one. */
  since: number | null;
  /** Actions in the current run. */
  steps: number;
}

/**
 * What the desktop agent does now, from the action feed (newest first) and
 * the control mode. Pure, so it is unit-tested.
 */
export function agentActivity(feed: FeedItem[], now: number, mode: DesktopMode, connected: boolean): AgentActivity {
  if (!connected) {
    return { phase: "offline", label: "Not connected", since: null, steps: 0 };
  }
  if (mode === "TAKEOVER") {
    return { phase: "paused", label: "You have control", since: null, steps: 0 };
  }
  const last = feed[0];
  if (!last || now - last.receivedAt >= WORKING_MS) {
    return { phase: "idle", label: "Watching", since: null, steps: 0 };
  }
  let start = last.receivedAt;
  let steps = 1;
  for (let i = 1; i < feed.length; i++) {
    if (feed[i - 1].receivedAt - feed[i].receivedAt > RUN_GAP_MS) {
      break;
    }
    start = feed[i].receivedAt;
    steps++;
  }
  return { phase: "working", label: describeAction(last.action), since: start, steps };
}
