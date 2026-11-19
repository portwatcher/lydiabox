import { Link2, Route } from "lucide-react";
import { formatCompact } from "../../lib/format";
import { Badge, cx } from "../ui";
import { CopyButton } from "./Snippet";
import { Meter } from "./StatCells";

export interface ModelCardProps {
  id: string;
  name: string;
  /** Context window in tokens; 0 when the gateway does not know it. */
  contextWindow: number;
  /** The largest context in the list, for the meter scale. */
  maxContext: number;
  /** Labels of the harnesses that used this model last. */
  boundTo: string[];
  /** Routing groups that send to this model. */
  groups: string[];
  className?: string;
}

/** One model: name, ID to copy, context window, and where it is in use. */
export function ModelCard({ id, name, contextWindow, maxContext, boundTo, groups, className }: ModelCardProps) {
  const inUse = boundTo.length > 0;
  return (
    <li
      className={cx(
        "card card-sm border bg-base-100 motion-safe:transition-colors",
        inUse ? "border-info/40" : "border-base-content/10 hover:border-base-content/25",
        className,
      )}
    >
      <div className="card-body gap-3">
        <div className="flex items-start gap-1">
          <div className="flex min-w-0 flex-1 flex-col">
            <h4 className="truncate text-sm font-semibold" title={name}>
              {name}
            </h4>
            <code className="truncate text-xs text-base-content/70" title={id}>
              {id}
            </code>
          </div>
          <CopyButton value={id} label={`model ID ${id}`} className="-me-1 -mt-1" />
        </div>
        {contextWindow > 0 ? (
          <div className="flex items-center gap-3 text-xs text-base-content/70">
            <Meter value={contextWindow} max={maxContext} log label={`Context window of ${name}`} className="flex-1" />
            <span className="shrink-0 tabular-nums">{formatCompact(contextWindow)} context</span>
          </div>
        ) : null}
        <div className="mt-auto flex min-h-5 flex-wrap items-center gap-1.5">
          {boundTo.map((h) => (
            <Badge key={h} tone="accent" icon={Link2}>
              {h}
            </Badge>
          ))}
          {groups.map((g) => (
            <Badge key={g} icon={Route}>
              {g}
            </Badge>
          ))}
        </div>
      </div>
    </li>
  );
}
