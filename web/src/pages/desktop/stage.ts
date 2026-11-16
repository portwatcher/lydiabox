import type { SocketState } from "../../api/socket";
import type { VMState } from "../../api/types";
import type { VMAction } from "../../hooks/useSystem";

/** What the stage of the Desktop page shows when there is no live video. */
export type DesktopStage =
  | "live"
  /** The VM is stopped: the page offers the start control of the status bar. */
  | "stopped"
  | "hibernating"
  | "starting"
  | "stopping"
  /** The socket is opening or reconnecting, or about to open. */
  | "connecting"
  /** The socket gave up after its retries. */
  | "lost";

export interface StageInput {
  vmState: VMState;
  pending: VMAction | null;
  socket: SocketState | null;
  /** The server ended the stream because the user stopped the VM. */
  stopped: boolean;
  live: boolean;
}

/**
 * Whether the page opens the stream now. Only a running VM is streamed, and
 * never during a power action or after the server said that the user stopped
 * the VM: opening the stream must not undo a stop.
 */
export function shouldConnect(s: StageInput): boolean {
  return s.vmState === "RUNNING" && s.pending === null && s.socket === null && !s.stopped;
}

/** Whether the page closes the stream: a power action runs or the VM does not run. */
export function shouldDisconnect(s: Pick<StageInput, "vmState" | "pending">): boolean {
  return s.vmState !== "RUNNING" || s.pending === "stop" || s.pending === "hibernate";
}

export function desktopStage(s: StageInput): DesktopStage {
  if (s.pending === "stop" || s.pending === "hibernate") {
    return "stopping";
  }
  if (s.socket === null) {
    if (s.vmState === "BOOTING" || s.pending === "wake") {
      return "starting";
    }
    if (s.vmState === "PAUSED") {
      return "hibernating";
    }
    if (s.vmState === "STOPPED" || s.stopped) {
      return "stopped";
    }
    return "connecting";
  }
  if (s.socket === "failed") {
    return "lost";
  }
  return s.live && s.socket === "open" ? "live" : "connecting";
}
