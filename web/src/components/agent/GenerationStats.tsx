import { formatCompact, formatUsd } from "../../lib/format";
import type { TurnStats } from "./chatState";
import { formatDuration } from "./toolKinds";

/** One line of totals under a finished turn: time, tokens, cost, and model, split by hairlines. */
export function GenerationStats({ stats, interrupted }: { stats: TurnStats; interrupted: boolean }) {
  const parts: { label: string; value: string }[] = [];
  const took = formatDuration(stats.durationMs);
  if (took) {
    parts.push({ label: interrupted ? "Stopped after" : "Done in", value: took });
  } else if (interrupted) {
    parts.push({ label: "", value: "Stopped" });
  }
  if (stats.inputTokens > 0 || stats.outputTokens > 0) {
    parts.push({ label: "Tokens", value: `${formatCompact(stats.inputTokens)} in · ${formatCompact(stats.outputTokens)} out` });
  }
  if (stats.costUsd > 0) {
    parts.push({ label: "Cost", value: formatUsd(stats.costUsd) });
  }
  if (stats.model) {
    parts.push({ label: "Model", value: stats.model });
  }
  if (parts.length === 0) {
    return null;
  }
  return (
    <dl className="flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-base-content/70">
      {parts.map((p, i) => (
        <div key={p.label || i} className="flex min-w-0 items-center gap-1 border-base-content/10 not-first:border-l not-first:pl-3">
          {p.label ? <dt>{p.label}</dt> : null}
          <dd className="truncate tabular-nums text-base-content">{p.value}</dd>
        </div>
      ))}
    </dl>
  );
}
