// Locale-aware formatting. Everything goes through Intl; nothing is hand-built.

const ZERO_TIME_PREFIX = "0001-01-01";

/** True for the API's zero time (`0001-01-01T00:00:00Z`), which means "never". */
export function isZeroTime(value: string | undefined | null): boolean {
  return !value || value.startsWith(ZERO_TIME_PREFIX);
}

export function parseTime(value: string | undefined | null): number | null {
  if (isZeroTime(value)) {
    return null;
  }
  const ms = Date.parse(value ?? "");
  return Number.isNaN(ms) ? null : ms;
}

const relative = new Intl.RelativeTimeFormat(undefined, { numeric: "auto", style: "narrow" });

/** "12s ago", "5 min. ago", "yesterday". */
export function formatAgo(ms: number): string {
  const seconds = Math.max(0, Math.round(ms / 1000));
  if (seconds < 60) {
    return relative.format(-seconds, "second");
  }
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) {
    return relative.format(-minutes, "minute");
  }
  const hours = Math.round(minutes / 60);
  if (hours < 24) {
    return relative.format(-hours, "hour");
  }
  return relative.format(-Math.round(hours / 24), "day");
}

/** Relative time of an RFC 3339 value against `now`, or null for zero time. */
export function timeAgo(value: string | undefined | null, now: number): string | null {
  const ms = parseTime(value);
  return ms === null ? null : formatAgo(now - ms);
}

const dateTime = new Intl.DateTimeFormat(undefined, { dateStyle: "medium", timeStyle: "short" });

export function formatDateTime(value: string | undefined | null): string {
  const ms = parseTime(value);
  return ms === null ? "Never" : dateTime.format(ms);
}

const byteUnits = ["byte", "kilobyte", "megabyte", "gigabyte", "terabyte"] as const;

export function formatBytes(bytes: number): string {
  let value = bytes;
  let unit = 0;
  while (value >= 1024 && unit < byteUnits.length - 1) {
    value /= 1024;
    unit += 1;
  }
  return new Intl.NumberFormat(undefined, {
    style: "unit",
    unit: byteUnits[unit],
    unitDisplay: unit === 0 ? "long" : "short",
    maximumFractionDigits: unit === 0 || value >= 100 ? 0 : 1,
  }).format(value);
}

const compact = new Intl.NumberFormat(undefined, { notation: "compact", maximumFractionDigits: 1 });

export function formatCompact(value: number): string {
  return compact.format(value);
}

const usd = new Intl.NumberFormat(undefined, { style: "currency", currency: "USD", maximumFractionDigits: 2 });

export function formatUsd(value: number): string {
  return usd.format(value);
}

export function formatMemory(memMb: number): string {
  return formatBytes(memMb * 1024 * 1024);
}

const plural = new Intl.PluralRules(undefined);

/** Picks a message by plural category: `count(n, { one: "# file", other: "# files" })`. */
export function count(n: number, forms: { one: string; other: string; zero?: string }): string {
  if (n === 0 && forms.zero) {
    return forms.zero;
  }
  const form = plural.select(n) === "one" ? forms.one : forms.other;
  return form.replace("#", new Intl.NumberFormat().format(n));
}
