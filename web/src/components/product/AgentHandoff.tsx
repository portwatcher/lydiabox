import { ArrowLeftRight, Bot, User } from "lucide-react";
import { Button, cx } from "../ui";

export type Controller = "agent" | "user";

export interface AgentHandoffProps {
  /** Who controls the screen now. */
  controller: Controller;
  /** The agent's display name. */
  agent?: string;
  busy: boolean;
  disabled: boolean;
  onHandoff: (to: Controller) => void;
}

/**
 * Who has the controls, on a short track between the agent and you, and the
 * one action that hands them over. The thumb travels to the side in
 * control; with reduced motion it jumps.
 */
export function AgentHandoff({ controller, agent = "Agent", busy, disabled, onHandoff }: AgentHandoffProps) {
  const user = controller === "user";
  const status = user ? "You have control" : `${agent} has control`;
  return (
    <div role="group" aria-label="Control" className="inline-flex items-center gap-2">
      <span className="sr-only" aria-live="polite">
        {status}
      </span>
      <span aria-hidden title={status} className="relative inline-flex h-8 items-center gap-1 rounded-full border border-base-content/15 bg-base-100 px-1">
        <span
          className={cx(
            "absolute top-0.5 size-[26px] rounded-full motion-safe:transition-[left,background-color] motion-safe:duration-300 motion-safe:ease-out",
            user ? "left-[calc(100%-28px)] bg-primary" : "left-0.5 bg-base-content/15",
          )}
        />
        <span className={cx("relative inline-flex size-6 items-center justify-center", user ? "text-base-content/50" : "text-base-content")}>
          <Bot className="size-3.5" />
        </span>
        <span className="relative h-px w-5 bg-base-content/20">
          <span
            className={cx(
              "absolute -top-[2px] size-[5px] rounded-full bg-info",
              busy ? "motion-safe:animate-handoff" : "hidden",
              user ? "[animation-direction:reverse]" : "",
            )}
          />
        </span>
        <span className={cx("relative inline-flex size-6 items-center justify-center", user ? "text-primary-content" : "text-base-content/50")}>
          <User className="size-3.5" />
        </span>
      </span>
      <Button
        size="sm"
        variant={user ? "secondary" : "primary"}
        icon={ArrowLeftRight}
        busy={busy}
        disabled={disabled}
        onClick={() => onHandoff(user ? "agent" : "user")}
      >
        {user ? `Hand back to ${agent.toLowerCase() === "agent" ? "the agent" : agent}` : "Take over"}
      </Button>
    </div>
  );
}
