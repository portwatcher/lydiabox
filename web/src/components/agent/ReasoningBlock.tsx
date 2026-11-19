import { Brain, ChevronRight } from "lucide-react";

/** The reasoning of the agent, folded to one row; it opens to the full text. */
export function ReasoningBlock({ text, active }: { text: string; active: boolean }) {
  const first = text.split("\n").find((l) => l.trim()) ?? "";
  return (
    <details className="group min-w-0 text-sm">
      <summary className="flex min-h-8 cursor-pointer list-none items-center gap-2 rounded-box px-1 text-base-content/70 hover:text-base-content focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-base-content [&::-webkit-details-marker]:hidden">
        <ChevronRight aria-hidden className="size-3.5 shrink-0 group-open:rotate-90 motion-safe:transition-transform" />
        <Brain aria-hidden className="size-4 shrink-0" />
        <span className="shrink-0 font-medium">{active ? "Thinking" : "Thought"}</span>
        <span className="min-w-0 truncate group-open:hidden">{first.replace(/\*\*/g, "")}</span>
      </summary>
      <p className="mt-1 ml-3 border-l-2 border-base-content/10 pl-4 break-words whitespace-pre-wrap text-base-content/70">{text}</p>
    </details>
  );
}
