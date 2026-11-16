import type { StorageStatus } from "../api/types";
import { isZeroTime } from "./format";

/** What the status bar shows about the S3 sync. */
export type SyncState =
  | { kind: "none" }
  | { kind: "unmounted" }
  | { kind: "failed"; error: string }
  | { kind: "pending" }
  | { kind: "synced"; at: string };

/**
 * The juicefs mode always syncs to S3. The provider mode syncs only when the
 * state database backup has S3 settings (`syncEnabled`); without a sync there
 * is nothing to show. Older servers omit `syncEnabled`: a provider-mode server
 * that never synced and never failed has no sync.
 */
export function syncState(storage: StorageStatus): SyncState {
  const neverSynced = isZeroTime(storage.lastSyncAt);
  if (storage.mode === "provider") {
    const enabled = storage.syncEnabled ?? (Boolean(storage.lastSyncError) || !neverSynced);
    if (!enabled) {
      return { kind: "none" };
    }
  } else if (!storage.mounted) {
    return { kind: "unmounted" };
  }
  if (storage.lastSyncError) {
    return { kind: "failed", error: storage.lastSyncError };
  }
  if (neverSynced) {
    return { kind: "pending" };
  }
  return { kind: "synced", at: storage.lastSyncAt };
}
