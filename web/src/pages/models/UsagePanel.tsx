import { useMemo } from "react";
import type { Session } from "../../api/types";
import { StatCells } from "../../components/product/StatCells";
import { Panel } from "../../components/ui";
import { formatCompact, formatUsd } from "../../lib/format";
import { usageByModel, usageTotal } from "./catalog";

/**
 * Token use and cost that the harness sessions report, in total and by
 * model. Nothing shows when no session reports usage.
 */
export function UsagePanel({ sessions }: { sessions: Session[] }) {
  const rows = useMemo(() => usageByModel(sessions), [sessions]);
  if (rows.length === 0) {
    return null;
  }
  const total = usageTotal(rows);
  const maxCost = Math.max(...rows.map((r) => r.costUsd));
  const maxTokens = Math.max(...rows.map((r) => r.inputTokens + r.outputTokens));
  return (
    <Panel title="Usage">
      <div className="flex flex-col gap-4 p-4">
        <StatCells
          cells={[
            { label: "Sessions", value: total.sessions.toLocaleString() },
            { label: "Input tokens", value: formatCompact(total.inputTokens) },
            { label: "Output tokens", value: formatCompact(total.outputTokens) },
            { label: "Cost", value: formatUsd(total.costUsd) },
          ]}
        />
        <div className="overflow-x-auto">
          <table className="table table-sm">
            <thead>
              <tr className="border-base-content/10 text-xs text-base-content/70">
                <th scope="col" className="font-medium">
                  Model
                </th>
                <th scope="col" className="w-2/5 font-medium">
                  <span className="sr-only">Share</span>
                </th>
                <th scope="col" className="text-right font-medium">
                  Sessions
                </th>
                <th scope="col" className="text-right font-medium">
                  Tokens in / out
                </th>
                <th scope="col" className="text-right font-medium">
                  Cost
                </th>
              </tr>
            </thead>
            <tbody>
              {rows.map((r) => {
                const share = maxCost > 0 ? r.costUsd / maxCost : maxTokens > 0 ? (r.inputTokens + r.outputTokens) / maxTokens : 0;
                return (
                  <tr key={r.model || "-"} className="border-base-content/10 text-[13px]">
                    <td className="max-w-56 truncate font-mono text-xs" title={r.model}>
                      {r.model || "Default model"}
                    </td>
                    <td>
                      <span aria-hidden className="block h-1.5 rounded-full bg-base-content/10">
                        <span className="block h-full rounded-full bg-info" style={{ width: `${Math.max(2, Math.round(share * 100))}%` }} />
                      </span>
                    </td>
                    <td className="text-right tabular-nums">{r.sessions}</td>
                    <td className="text-right whitespace-nowrap text-base-content/70 tabular-nums">
                      {formatCompact(r.inputTokens)} / {formatCompact(r.outputTokens)}
                    </td>
                    <td className="text-right tabular-nums">{formatUsd(r.costUsd)}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
      </div>
    </Panel>
  );
}
