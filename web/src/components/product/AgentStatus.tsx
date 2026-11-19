import { Bot, Hand, Unplug } from "lucide-react";
import type { ButtonHTMLAttributes } from "react";
import { count } from "../../lib/format";
import { cx } from "../ui";

export type AgentPhase = "offline" | "idle" | "working" | "paused";

/** "1:05" or "12s": the elapsed time of a run. */
export function formatElapsed(ms: number): string {
  const s = Math.max(0, Math.floor(ms / 1000));
  if (s < 60) {
    return `${s}s`;
  }
  const m = Math.floor(s / 60);
  if (m < 60) {
    return `${m}:${String(s % 60).padStart(2, "0")}`;
  }
  return `${Math.floor(m / 60)}:${String(m % 60).padStart(2, "0")}:${String(s % 60).padStart(2, "0")}`;
}

const ring: Record<AgentPhase, string> = {
  offline: "border-base-content/25",
  idle: "border-success/60",
  working: "border-info/30 border-t-info motion-safe:animate-spin",
  paused: "border-warning/70",
};

export interface AgentStatusProps extends Omit<ButtonHTMLAttributes<HTMLButtonElement>, "children"> {
  name: string;
  phase: AgentPhase;
  label: string;
  /** Elapsed time of the current run, in ms; null hides the timer. */
  elapsed: number | null;
  steps: number;
}

/**
 * A compact pill: the agent mark in a live ring, the current activity, and
 * the run timer. The ring turns while the agent works, holds still when it
 * is paused, and dims when the agent is not connected.
 */
export function AgentStatus({ name, phase, label, elapsed, steps, className, ...rest }: AgentStatusProps) {
  const Icon = phase === "paused" ? Hand : phase === "offline" ? Unplug : Bot;
  return (
    <button
      type="button"
      className={cx(
        "inline-flex h-8 max-w-full min-w-0 items-center gap-2 rounded-full border border-base-content/15 bg-base-100 ps-1 pe-3 text-[13px] hover:border-base-content/30",
        className,
      )}
      {...rest}
    >
      <span aria-hidden className="relative inline-flex size-6 shrink-0 items-center justify-center">
        <span className={cx("absolute inset-0 rounded-full border-2", ring[phase])} />
        <Icon className={cx("size-3.5", phase === "offline" ? "text-base-content/50" : "text-base-content")} />
      </span>
      <span className="font-medium whitespace-nowrap">{name}</span>
      <span
        className={cx("min-w-0 truncate", phase === "working" ? "text-base-content" : "text-base-content/70", phase === "paused" && "text-warning")}
      >
        {label}
      </span>
      {phase === "working" && elapsed !== null ? (
        <span className="hidden shrink-0 text-xs text-base-content/70 tabular-nums sm:inline">
          {formatElapsed(elapsed)}
          {steps > 1 ? ` · ${count(steps, { one: "# step", other: "# steps" })}` : ""}
        </span>
      ) : null}
    </button>
  );
}
