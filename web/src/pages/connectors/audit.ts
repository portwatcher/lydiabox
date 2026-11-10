import { isRecord } from "../../api/decode";
import type { AuditEntry } from "../../api/types";

const kindLabels: Record<string, string> = {
  "connector.connected": "Connected",
  "connector.disconnected": "Disconnected",
  "connector.token.issued": "Token issued",
  "connector.token.refreshed": "Token refreshed",
  "connector.token.refresh_failed": "Token refresh failed",
  "connector.mcp.call": "Tool call",
};

export function auditKindLabel(kind: string): string {
  return kindLabels[kind] ?? kind;
}

export function isFailureKind(kind: string): boolean {
  return kind.endsWith("failed");
}

/** Parses the `detail` JSON string into displayable key/value pairs. */
export function auditDetail(detail: string): [string, string][] {
  if (!detail) {
    return [];
  }
  let parsed: unknown;
  try {
    parsed = JSON.parse(detail);
  } catch {
    return [["detail", detail]];
  }
  if (!isRecord(parsed)) {
    return [["detail", String(parsed)]];
  }
  return Object.entries(parsed)
    .filter(([, value]) => value !== null && value !== undefined && value !== "")
    .map(([key, value]) => [key, typeof value === "string" ? value : JSON.stringify(value)]);
}

/** Adds newer entries in front of the list, newest first, without duplicates. */
export function mergeNewer(current: AuditEntry[], incoming: AuditEntry[]): AuditEntry[] {
  const top = current[0]?.id ?? -1;
  const newer = incoming.filter((e) => e.id > top);
  return newer.length === 0 ? current : [...newer, ...current];
}

/** Appends an older page behind the list, without duplicates. */
export function appendOlder(current: AuditEntry[], older: AuditEntry[]): AuditEntry[] {
  const seen = new Set(current.map((e) => e.id));
  return [...current, ...older.filter((e) => !seen.has(e.id))];
}
