import type { VMState } from "../../api/types";

/**
 * Whether the chat view shows "History not loaded" for a session that has
 * no events on the screen. A read error while the VM starts or resumes
 * (BOOTING) is not final: the first read stops its wait before a slow
 * start ends, and the next reads fail until the VM runs. The view then
 * keeps loading, and the next read after the start fills it.
 */
export function historyFailed(status: { synced: boolean; error: string } | null, vmState: VMState): boolean {
  if (!status || status.synced || status.error === "") {
    return false;
  }
  return vmState !== "BOOTING";
}
