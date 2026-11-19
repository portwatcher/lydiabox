import { CircleCheck, Hand, Moon } from "lucide-react";
import { useEffect, useState } from "react";
import type { AgentState } from "../../api/events";
import { Spinner, cx } from "../ui";
import { formatDuration } from "./toolKinds";

const labels: Record<AgentState, string> = {
  working: "Working",
  idle: "Ready",
  attention: "Needs your input",
  offline: "Not running",
};

const tones: Record<AgentState, string> = {
  working: "border-info/40 text-info",
  idle: "border-success/40 text-success",
  attention: "border-warning/50 text-warning",
  offline: "border-base-content/20 text-base-content/70",
};

/** A pill with the state of the agent: an icon and a word, and the time spent while it works. */
export function AgentStatus({ state, since }: { state: AgentState; since?: number }) {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (state !== "working") {
      return;
    }
    const id = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(id);
  }, [state]);
  const elapsed = state === "working" && since ? formatDuration(Math.max(0, now - since)) : "";
  return (
    <span className={cx("inline-flex h-7 items-center gap-1.5 rounded-full border px-2.5 text-xs font-medium whitespace-nowrap", tones[state])}>
      {state === "working" ? (
        <>
          <Spinner size="xs" className="motion-reduce:hidden" />
          <span aria-hidden className="size-2 rounded-full bg-current motion-safe:hidden" />
        </>
      ) : state === "attention" ? (
        <Hand aria-hidden className="size-3.5" />
      ) : state === "idle" ? (
        <CircleCheck aria-hidden className="size-3.5" />
      ) : (
        <Moon aria-hidden className="size-3.5" />
      )}
      {labels[state]}
      {elapsed ? <span className="tabular-nums text-base-content/70">{elapsed}</span> : null}
    </span>
  );
}
