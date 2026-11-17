import { Box, Eye, Globe, MousePointer2, PanelRightClose, PanelRightOpen, SquareTerminal, Users } from "lucide-react";
import type { LucideIcon } from "lucide-react";
import type { DesktopMode, LaunchApp } from "../../api/types";
import { Button, cx } from "../../components/ui";
import { count } from "../../lib/format";

const apps: { app: LaunchApp; label: string; icon: LucideIcon }[] = [
  { app: "chrome", label: "Chrome", icon: Globe },
  { app: "blender", label: "Blender", icon: Box },
  { app: "terminal", label: "Terminal", icon: SquareTerminal },
];

function ModeSwitch({ mode, busy, disabled, onChange }: { mode: DesktopMode; busy: boolean; disabled: boolean; onChange: (m: DesktopMode) => void }) {
  const options: { value: DesktopMode; label: string; icon: LucideIcon }[] = [
    { value: "OBSERVER", label: "Observer", icon: Eye },
    { value: "TAKEOVER", label: "Takeover", icon: MousePointer2 },
  ];
  return (
    <div role="group" aria-label="Control mode" className="inline-flex rounded-md border border-border bg-bg p-0.5">
      {options.map(({ value, label, icon: Icon }) => {
        const active = mode === value;
        return (
          <button
            key={value}
            type="button"
            aria-pressed={active}
            disabled={disabled || busy}
            onClick={() => !active && onChange(value)}
            className={cx(
              "inline-flex h-7 items-center gap-1.5 rounded px-2.5 text-[13px] font-medium transition-colors duration-150 disabled:opacity-50",
              active
                ? value === "TAKEOVER"
                  ? "bg-accent-solid text-on-accent"
                  : "bg-surface-active text-fg"
                : "text-fg-muted hover:text-fg",
            )}
          >
            <Icon aria-hidden className="size-4" />
            {label}
          </button>
        );
      })}
    </div>
  );
}

interface DesktopToolbarProps {
  mode: DesktopMode;
  modeBusy: boolean;
  connected: boolean;
  viewers: number;
  sidebar: boolean;
  /** Keyboard hint of the takeover mode, shown beside the mode switch (outside the remote screen). */
  hint?: string | null;
  onModeChange: (mode: DesktopMode) => void;
  onLaunch: (app: LaunchApp, label: string) => void;
  onToggleSidebar: () => void;
}

/** Desktop controls. The VM state and power actions are in the status bar only. */
export function DesktopToolbar({ mode, modeBusy, connected, viewers, sidebar, hint = null, onModeChange, onLaunch, onToggleSidebar }: DesktopToolbarProps) {
  return (
    <div className="flex min-h-12 flex-wrap items-center gap-x-3 gap-y-2 border-b border-border-subtle px-3 py-2">
      <ModeSwitch mode={mode} busy={modeBusy} disabled={!connected} onChange={onModeChange} />
      {/* A keyboard hint; hidden at phone widths, where the toolbar wraps and touch has no Shift+Esc. */}
      {hint ? <span className="hidden text-xs text-fg-muted sm:inline">{hint}</span> : null}
      <div className="flex flex-wrap items-center gap-1">
        {apps.map(({ app, label, icon }) => (
          <Button key={app} size="sm" variant="ghost" icon={icon} disabled={!connected} onClick={() => onLaunch(app, label)}>
            {label}
          </Button>
        ))}
      </div>
      <span className="inline-flex items-center gap-1.5 text-xs text-fg-muted tabular-nums">
        <Users aria-hidden className="size-4" />
        {count(viewers, { one: "# viewer", other: "# viewers" })}
      </span>
      <Button
        size="sm"
        variant="ghost"
        className="ml-auto"
        icon={sidebar ? PanelRightClose : PanelRightOpen}
        aria-expanded={sidebar}
        aria-controls="desktop-feed"
        onClick={onToggleSidebar}
      >
        Activity
      </Button>
    </div>
  );
}
