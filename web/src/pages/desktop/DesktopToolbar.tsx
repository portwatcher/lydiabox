import { Box, Globe, SquareTerminal, Users } from "lucide-react";
import type { LucideIcon } from "lucide-react";
import type { ReactNode } from "react";
import type { DesktopMode, LaunchApp } from "../../api/types";
import { AgentHandoff } from "../../components/product/AgentHandoff";
import { AgentStatus } from "../../components/product/AgentStatus";
import { Button } from "../../components/ui";
import { count } from "../../lib/format";
import type { AgentActivity } from "./activity";

const apps: { app: LaunchApp; label: string; icon: LucideIcon }[] = [
  { app: "chrome", label: "Chrome", icon: Globe },
  { app: "blender", label: "Blender", icon: Box },
  { app: "terminal", label: "Terminal", icon: SquareTerminal },
];

interface DesktopToolbarProps {
  mode: DesktopMode;
  modeBusy: boolean;
  connected: boolean;
  viewers: number;
  sidebar: boolean;
  /** What the agent does now; the pill opens and closes the activity panel. */
  activity: AgentActivity;
  /** Client time, for the run timer. */
  now: number;
  /** Keyboard hint of the takeover mode, shown beside the mode switch (outside the remote screen). */
  hint?: ReactNode;
  onModeChange: (mode: DesktopMode) => void;
  onLaunch: (app: LaunchApp, label: string) => void;
  onToggleSidebar: () => void;
  /** The VM state and power actions (only on this page: other pages start the VM when they need it). */
  machine?: ReactNode;
}

/** Desktop controls and the VM controls. */
export function DesktopToolbar({ mode, modeBusy, connected, viewers, sidebar, activity, now, hint = null, onModeChange, onLaunch, onToggleSidebar, machine }: DesktopToolbarProps) {
  return (
    <div className="flex min-h-12 flex-wrap items-center gap-x-3 gap-y-2 border-b border-base-content/10 px-3 py-2">
      <AgentHandoff
        controller={mode === "TAKEOVER" ? "user" : "agent"}
        busy={modeBusy}
        disabled={!connected}
        onHandoff={(to) => onModeChange(to === "user" ? "TAKEOVER" : "OBSERVER")}
      />
      <AgentStatus
        name="Agent"
        phase={activity.phase}
        label={activity.label}
        elapsed={activity.since === null ? null : now - activity.since}
        steps={activity.steps}
        aria-expanded={sidebar}
        aria-controls="desktop-feed"
        aria-label={`Agent: ${activity.label}. ${sidebar ? "Hide" : "Show"} activity`}
        title={sidebar ? "Hide activity" : "Show activity"}
        onClick={onToggleSidebar}
        className="max-w-80"
      />
      {/* A keyboard hint; hidden at phone widths, where the toolbar wraps and touch has no Shift+Esc. */}
      {hint ? <span className="hidden text-xs text-base-content/70 sm:inline">{hint}</span> : null}
      <div className="flex flex-wrap items-center gap-1">
        {apps.map(({ app, label, icon }) => (
          <Button key={app} size="sm" variant="ghost" icon={icon} disabled={!connected} onClick={() => onLaunch(app, label)}>
            {label}
          </Button>
        ))}
      </div>
      <span className="inline-flex items-center gap-1.5 text-xs text-base-content/70 tabular-nums">
        <Users aria-hidden className="size-4" />
        {count(viewers, { one: "# viewer", other: "# viewers" })}
      </span>
      <div className="ml-auto flex flex-wrap items-center gap-3">{machine}</div>
    </div>
  );
}
