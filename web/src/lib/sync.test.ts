import { describe, expect, it } from "vitest";
import type { StorageStatus } from "../api/types";
import { syncState } from "./sync";

const zero = "0001-01-01T00:00:00Z";
const at = "2026-11-17T08:00:00Z";

function storage(over: Partial<StorageStatus>): StorageStatus {
  return { tenantId: "default", mounted: true, lastSyncAt: zero, lastUploadAt: zero, ...over };
}

describe("syncState in the juicefs mode", () => {
  it("keeps the mount, failure, pending and synced states", () => {
    expect(syncState(storage({ mode: "juicefs", mounted: false, syncEnabled: true }))).toEqual({ kind: "unmounted" });
    expect(syncState(storage({ mode: "juicefs", syncEnabled: true, lastSyncError: "boom" }))).toEqual({ kind: "failed", error: "boom" });
    expect(syncState(storage({ mode: "juicefs", syncEnabled: true }))).toEqual({ kind: "pending" });
    expect(syncState(storage({ mode: "juicefs", syncEnabled: true, lastSyncAt: at }))).toEqual({ kind: "synced", at });
  });

  it("treats a server without mode or syncEnabled as juicefs", () => {
    expect(syncState(storage({}))).toEqual({ kind: "pending" });
    expect(syncState(storage({ mounted: false }))).toEqual({ kind: "unmounted" });
  });
});

describe("syncState in the provider mode", () => {
  const provider = { mode: "provider" as const, mounted: false };

  it("shows nothing without an S3 backup", () => {
    expect(syncState(storage({ ...provider, syncEnabled: false }))).toEqual({ kind: "none" });
    expect(syncState(storage({ ...provider, syncEnabled: false, lastSyncAt: "" }))).toEqual({ kind: "none" });
  });

  it("shows the backup state when an S3 backup exists", () => {
    expect(syncState(storage({ ...provider, syncEnabled: true }))).toEqual({ kind: "pending" });
    expect(syncState(storage({ ...provider, syncEnabled: true, lastSyncAt: at }))).toEqual({ kind: "synced", at });
    expect(syncState(storage({ ...provider, syncEnabled: true, lastSyncError: "denied" }))).toEqual({ kind: "failed", error: "denied" });
  });

  it("infers the backup from the times on older servers", () => {
    expect(syncState(storage({ ...provider }))).toEqual({ kind: "none" });
    expect(syncState(storage({ ...provider, lastSyncAt: at }))).toEqual({ kind: "synced", at });
    expect(syncState(storage({ ...provider, lastSyncError: "denied" }))).toEqual({ kind: "failed", error: "denied" });
  });
});
