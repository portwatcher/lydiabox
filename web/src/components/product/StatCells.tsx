import type { ReactNode } from "react";
import { cx } from "../ui";

export interface StatCell {
  label: string;
  value: ReactNode;
  /** A short note under the value, for example a unit or a share. */
  note?: ReactNode;
}

/** A row of figures split by hairlines; two columns below 640 px. */
export function StatCells({ cells, className }: { cells: StatCell[]; className?: string }) {
  return (
    <dl
      className={cx(
        "grid grid-cols-2 gap-px overflow-hidden rounded-box border border-base-content/10 bg-base-content/10",
        cells.length >= 4 ? "sm:grid-cols-4" : cells.length === 3 ? "sm:grid-cols-3" : "",
        className,
      )}
    >
      {cells.map((c) => (
        <div key={c.label} className="flex min-w-0 flex-col gap-0.5 bg-base-100 px-4 py-3">
          <dt className="text-xs text-base-content/70">{c.label}</dt>
          <dd className="truncate text-lg font-semibold tracking-tight tabular-nums">{c.value}</dd>
          {c.note ? <dd className="truncate text-xs text-base-content/70">{c.note}</dd> : null}
        </div>
      ))}
    </dl>
  );
}

/**
 * A horizontal meter: `value` of `max`. It shows a ratio on a log scale when
 * `log` is set, so that 8K and 2M contexts both read.
 */
export function Meter({ value, max, log = false, label, className }: { value: number; max: number; log?: boolean; label: string; className?: string }) {
  const ratio = max <= 0 || value <= 0 ? 0 : log ? Math.log10(value + 1) / Math.log10(max + 1) : value / max;
  const pct = Math.round(Math.min(1, Math.max(0, ratio)) * 100);
  return (
    <span
      role="meter"
      aria-label={label}
      aria-valuemin={0}
      aria-valuemax={max}
      aria-valuenow={value}
      className={cx("relative block h-1.5 overflow-hidden rounded-full bg-base-content/10", className)}
    >
      <span className="absolute inset-y-0 left-0 rounded-full bg-info motion-safe:transition-[width] motion-safe:duration-300" style={{ width: `${pct}%` }} />
    </span>
  );
}
