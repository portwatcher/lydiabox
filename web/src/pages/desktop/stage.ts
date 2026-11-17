import type { SocketState } from "../../api/socket";
import type { VMState } from "../../api/types";
import type { VMAction } from "../../hooks/useSystem";
import type { OutageKind } from "../../lib/outage";

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
  | "lost"
  /** The signaling works, but the browser could not open the video connection. */
  | "videoFailed"
  /** The status poll fails (the cell is stopped, or no status came): no stream, no start control. */
  | "unavailable";

export interface StageInput {
  vmState: VMState;
  pending: VMAction | null;
  socket: SocketState | null;
  /** The server ended the stream because the user stopped the VM. */
  stopped: boolean;
  /** The server ended or refused the stream because the VM pauses or is paused. */
  paused?: boolean;
  /** The video track is there and the peer connection is connected. */
  live: boolean;
  /** State of the WebRTC peer connection ("new" before the first offer). */
  peer?: RTCPeerConnectionState;
  /** Set while the status poll fails (useSystem().outage). */
  outage?: OutageKind | null;
}

/**
 * Whether the page opens the stream now. Only a running VM is streamed, and
 * never during a power action or after the server said that the user stopped
 * the VM or that it pauses: opening the stream must not undo a stop or a
 * hibernate.
 */
export function shouldConnect(s: StageInput): boolean {
  return s.vmState === "RUNNING" && s.pending === null && s.socket === null && !s.stopped && !s.paused && !s.outage;
}

/** Whether the page closes the stream: a power action runs, the VM does not run, or its state is not known. */
export function shouldDisconnect(s: Pick<StageInput, "vmState" | "pending" | "outage">): boolean {
  return s.vmState !== "RUNNING" || s.pending === "stop" || s.pending === "hibernate" || Boolean(s.outage);
}

export function desktopStage(s: StageInput): DesktopStage {
  if (s.outage === "starting") {
    return "starting";
  }
  if (s.outage) {
    return "unavailable";
  }
  if (s.pending === "stop" || s.pending === "hibernate") {
    return "stopping";
  }
  if (s.socket === null) {
    if (s.vmState === "BOOTING" || s.pending === "wake") {
      return "starting";
    }
    if (s.vmState === "PAUSED" || s.paused) {
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
  if (s.socket === "open" && s.peer === "failed") {
    return "videoFailed";
  }
  return s.live && s.socket === "open" ? "live" : "connecting";
}
