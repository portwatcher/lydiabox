import { CircleAlert } from "lucide-react";
import type { Turn, TurnItem } from "./chatState";
import { GenerationStats } from "./GenerationStats";
import { Markdown } from "./Markdown";
import { PlanCard } from "./PlanCard";
import { ReasoningBlock } from "./ReasoningBlock";
import { SourceCards } from "./SourceCards";
import { ToolGroup } from "./ToolCallCard";

function Item({ item, active }: { item: TurnItem; active: boolean }) {
  switch (item.type) {
    case "text":
      return <Markdown text={item.text} />;
    case "reasoning":
      return <ReasoningBlock text={item.text} active={active} />;
    case "tools":
      return <ToolGroup tools={item.tools} active={active} />;
    case "plan":
      return <PlanCard steps={item.steps} />;
    case "error":
      return (
        <div role="note" className="flex items-start gap-2 rounded-box bg-error/10 px-3 py-2 text-sm text-error">
          <CircleAlert aria-hidden className="mt-0.5 size-4 shrink-0" />
          <span className="min-w-0 break-words whitespace-pre-wrap">{item.text}</span>
        </div>
      );
  }
}

/** One turn: the prompt of the user, then what the agent did on a rail, then the files it touched and its totals. */
export function TurnView({ turn, active }: { turn: Turn; active: boolean }) {
  // Only the newest plan of a turn is shown inline; while the turn works,
  // the chat view pins it above the prompt field instead.
  const lastPlan = active ? -1 : turn.items.map((it) => it.type).lastIndexOf("plan");
  const items = turn.items.filter((it, i) => it.type !== "plan" || i === lastPlan);
  const settled = !active;
  return (
    <article className="flex min-w-0 flex-col gap-3">
      {turn.prompt ? (
        <div className="flex justify-end">
          <p className="max-w-[85%] rounded-box bg-neutral px-3 py-2 text-sm break-words whitespace-pre-wrap text-neutral-content">{turn.prompt.text}</p>
        </div>
      ) : null}
      {items.length > 0 ? (
        <div className="flex min-w-0 flex-col gap-3 border-l border-base-content/10 pl-4">
          {items.map((item, i) => (
            <Item key={item.key} item={item} active={active && (item.type === "tools" || i === items.length - 1)} />
          ))}
        </div>
      ) : null}
      {settled && (turn.sources.length > 0 || turn.ended) ? (
        <div className="flex min-w-0 flex-col gap-2 pl-4">
          <SourceCards sources={turn.sources} />
          {turn.ended ? <GenerationStats stats={turn.stats} interrupted={turn.interrupted} /> : null}
        </div>
      ) : null}
    </article>
  );
}

/** The turns of a session in order. */
export function AgentTimeline({ turns, working }: { turns: Turn[]; working: boolean }) {
  return (
    <ol aria-label="Conversation" className="flex min-w-0 flex-col gap-8">
      {turns.map((t, i) => (
        <li key={t.key} className="min-w-0">
          <TurnView turn={t} active={working && i === turns.length - 1} />
        </li>
      ))}
    </ol>
  );
}
