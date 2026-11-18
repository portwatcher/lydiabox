import { Camera, Keyboard, MousePointer2, MousePointerClick, Move, Rocket, ScrollText, Type } from "lucide-react";
import type { LucideIcon } from "lucide-react";
import type { AgentActionKind } from "../../api/types";
import { StateView } from "../../components/StateView";
import { useNow } from "../../hooks/useResource";
import { formatAgo } from "../../lib/format";
import { describeAction } from "./overlay";
import type { FeedItem } from "./overlay";

const icons: Record<AgentActionKind, LucideIcon> = {
  move: MousePointer2,
  click: MousePointerClick,
  drag: Move,
  scroll: ScrollText,
  type: Type,
  key: Keyboard,
  screenshot: Camera,
  launch: Rocket,
};

export function ActionFeed({ feed }: { feed: FeedItem[] }) {
  const now = useNow(5000);
  if (feed.length === 0) {
    return <StateView title="No agent actions yet">Tool calls from the agent show here as they happen.</StateView>;
  }
  return (
    <ol aria-label="Recent agent actions" className="list">
      {feed.map((item) => {
        const Icon = icons[item.action.action];
        return (
          <li key={item.id} className="list-row gap-3 rounded-none px-4 py-2.5 after:border-base-content/10">
            <Icon aria-hidden className="mt-0.5 size-4 shrink-0 text-base-content/70" />
            <div className="flex min-w-0 flex-1 flex-col gap-0.5">
              <span className="text-sm break-words">{describeAction(item.action)}</span>
              <span className="flex items-center gap-2 text-xs text-base-content/70">
                {item.action.tool ? <code className="truncate">{item.action.tool}</code> : null}
                <span className="ml-auto shrink-0 tabular-nums">{formatAgo(now - item.receivedAt)}</span>
              </span>
            </div>
          </li>
        );
      })}
    </ol>
  );
}
