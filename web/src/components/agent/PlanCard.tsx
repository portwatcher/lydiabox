import { ChevronRight, CircleCheck, CircleDashed, CircleDot } from "lucide-react";
import type { PlanStep } from "../../api/events";
import { cx } from "../ui";

const stepIcons = { completed: CircleCheck, in_progress: CircleDot, pending: CircleDashed };
const stepText = { completed: "Done", in_progress: "In progress", pending: "To do" };

function Steps({ steps }: { steps: PlanStep[] }) {
  return (
    <ol className="flex flex-col gap-1">
      {steps.map((s, i) => {
        const Icon = stepIcons[s.status];
        return (
          <li key={i} className="flex min-w-0 items-start gap-2 text-sm">
            <Icon
              aria-hidden
              className={cx(
                "mt-0.5 size-4 shrink-0",
                s.status === "completed" ? "text-success" : s.status === "in_progress" ? "text-info" : "text-base-content/70",
              )}
            />
            <span className={cx("min-w-0 break-words", s.status === "completed" && "text-base-content/70 line-through", s.status === "in_progress" && "font-medium")}>
              {s.text}
            </span>
            <span className="sr-only">({stepText[s.status]})</span>
          </li>
        );
      })}
    </ol>
  );
}

function Progress({ steps }: { steps: PlanStep[] }) {
  const done = steps.filter((s) => s.status === "completed").length;
  return (
    <>
      <span className="text-xs tabular-nums text-base-content/70">
        {done} of {steps.length}
      </span>
      <progress className="progress progress-success h-1 w-16 shrink-0 sm:w-24" value={done} max={Math.max(1, steps.length)} aria-hidden />
    </>
  );
}

/**
 * The plan or to-do list of the agent: "3 of 6" over a thin bar, and each
 * step with its state. The compact form folds to one row with the current
 * step.
 */
export function PlanCard({ steps, compact = false }: { steps: PlanStep[]; compact?: boolean }) {
  if (compact) {
    const current = steps.find((s) => s.status === "in_progress") ?? steps.find((s) => s.status === "pending");
    return (
      <details className="group min-w-0 rounded-box border border-base-content/10 bg-base-100">
        <summary className="flex min-h-10 cursor-pointer list-none items-center gap-2 rounded-box px-3 text-sm hover:bg-base-content/5 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-base-content [&::-webkit-details-marker]:hidden">
          <ChevronRight aria-hidden className="size-3.5 shrink-0 text-base-content/70 group-open:rotate-90 motion-safe:transition-transform" />
          <span className="shrink-0 font-semibold">Plan</span>
          <Progress steps={steps} />
          <span className="min-w-0 flex-1 truncate group-open:hidden">{current?.text}</span>
        </summary>
        <div className="max-h-48 overflow-y-auto border-t border-base-content/10 px-3 py-2">
          <Steps steps={steps} />
        </div>
      </details>
    );
  }
  return (
    <section aria-label="Plan" className="flex min-w-0 flex-col gap-2 rounded-box border border-base-content/10 bg-base-100 p-3">
      <header className="flex items-center gap-3 text-sm">
        <h3 className="font-semibold">Plan</h3>
        <Progress steps={steps} />
      </header>
      <Steps steps={steps} />
    </section>
  );
}
