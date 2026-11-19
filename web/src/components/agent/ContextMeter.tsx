import type { CSSProperties } from "react";
import { formatCompact } from "../../lib/format";
import { cx } from "../ui";

/** Share of the context window at which the meter warns: harnesses compact near it. */
export const CONTEXT_WARN = 0.8;

/**
 * The share of the context window that the last model call used, as a
 * ring with the percentage. Without a known window it shows the tokens.
 */
export function ContextMeter({ tokens, window }: { tokens: number; window: number }) {
  if (tokens <= 0) {
    return null;
  }
  if (window <= 0) {
    return (
      <span className="text-xs tabular-nums text-base-content/70" title="Tokens in the context">
        {formatCompact(tokens)} tokens
      </span>
    );
  }
  const share = Math.min(1, tokens / window);
  const pct = Math.round(share * 100);
  const tone = share >= 0.95 ? "text-error" : share >= CONTEXT_WARN ? "text-warning" : "text-base-content/70";
  const label = `Context ${pct}% used: ${formatCompact(tokens)} of ${formatCompact(window)} tokens`;
  return (
    <span className={cx("inline-flex items-center gap-1.5 text-xs tabular-nums", tone)} title={label}>
      <span
        role="meter"
        aria-label="Context window"
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={pct}
        aria-valuetext={label}
        className="radial-progress text-current [--size:1.25rem] [--thickness:3px]"
        style={{ "--value": pct } as CSSProperties}
      />
      {pct}%
    </span>
  );
}
