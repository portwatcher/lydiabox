import { MessageSquare, SquareTerminal } from "lucide-react";
import { cx } from "../../components/ui";

export type SessionViewMode = "chat" | "terminal";

const modes: { id: SessionViewMode; label: string; icon: typeof MessageSquare }[] = [
  { id: "chat", label: "Chat", icon: MessageSquare },
  { id: "terminal", label: "Terminal", icon: SquareTerminal },
];

/** The switch between the chat view and the terminal of a harness session. */
export function ViewSwitch({ value, onChange }: { value: SessionViewMode; onChange: (mode: SessionViewMode) => void }) {
  return (
    <div role="group" aria-label="View" className="join">
      {modes.map((m) => (
        <button
          key={m.id}
          type="button"
          aria-pressed={value === m.id}
          onClick={() => onChange(m.id)}
          className={cx("btn join-item btn-sm gap-1.5 text-[13px] font-medium", value === m.id ? "btn-active btn-neutral" : "btn-ghost hover:bg-base-content/10")}
        >
          <m.icon aria-hidden className="size-4" />
          {m.label}
        </button>
      ))}
    </div>
  );
}
