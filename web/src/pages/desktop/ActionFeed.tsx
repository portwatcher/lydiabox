import { Camera, Keyboard, MousePointer2, MousePointerClick, Move, Rocket, ScrollText, Type } from "lucide-react";
import type { LucideIcon } from "lucide-react";
import type { AgentActionKind } from "../../api/types";
import { StateView } from "../../components/StateView";
import { cx } from "../../components/ui";
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
  const now = useNow(2000);
  if (feed.length === 0) {
    return <StateView title="No agent actions yet">Tool calls from the agent show here as they happen.</StateView>;
  }
  return (
    <ol aria-label="Recent agent actions" className="flex flex-col px-4 py-3">
      {feed.map((item, i) => {
        const Icon = icons[item.action.action];
        const fresh = i === 0 && now - item.receivedAt < 6000;
        return (
          <li key={item.id} className="relative flex gap-3 pb-3 last:pb-0">
            {i < feed.length - 1 ? <span aria-hidden className="absolute top-7 bottom-0 left-3 w-px bg-base-content/10" /> : null}
            <span
              aria-hidden
              className={cx(
                "z-[1] inline-flex size-6 shrink-0 items-center justify-center rounded-full border",
                fresh ? "border-info/50 bg-info/15 text-info" : "border-base-content/15 bg-base-100 text-base-content/70",
              )}
            >
              <Icon className="size-3.5" />
            </span>
            <div className="flex min-w-0 flex-1 flex-col gap-0.5 pt-0.5">
              <span className="text-[13px] break-words">{describeAction(item.action)}</span>
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
