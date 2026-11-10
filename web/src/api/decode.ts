// Small runtime decoders for WebSocket JSON frames. Frames arrive untyped, so
// each field is checked before the message is handed to typed code.

export type Json = Record<string, unknown>;

export function isRecord(value: unknown): value is Json {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

export function str(record: Json, key: string, fallback = ""): string {
  const value = record[key];
  return typeof value === "string" ? value : fallback;
}

export function num(record: Json, key: string, fallback = 0): number {
  const value = record[key];
  return typeof value === "number" && Number.isFinite(value) ? value : fallback;
}

export function bool(record: Json, key: string): boolean {
  return record[key] === true;
}

export function oneOf<T extends string>(value: unknown, allowed: readonly T[]): T | null {
  for (const item of allowed) {
    if (item === value) {
      return item;
    }
  }
  return null;
}
