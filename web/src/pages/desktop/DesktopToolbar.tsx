import { Box, Eye, Globe, MousePointer2, PanelRightClose, PanelRightOpen, SquareTerminal, Users } from "lucide-react";
import type { LucideIcon } from "lucide-react";
import type { ReactNode } from "react";
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
    <div role="group" aria-label="Control mode" className="tabs tabs-box tabs-sm flex-nowrap p-0.5">
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
              "tab h-7 gap-1.5 px-2.5 text-[13px] font-medium",
              // The fill is explicit: daisyUI drops it on a disabled tab, and the mode shows also while the desktop is not connected.
              active ? (value === "TAKEOVER" ? "tab-active bg-primary text-primary-content" : "tab-active bg-base-100") : "text-base-content/70 hover:text-base-content",
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
  hint?: ReactNode;
  onModeChange: (mode: DesktopMode) => void;
  onLaunch: (app: LaunchApp, label: string) => void;
  onToggleSidebar: () => void;
  /** The VM state and power actions (only on this page: other pages start the VM when they need it). */
  machine?: ReactNode;
}

/** Desktop controls and the VM controls. */
export function DesktopToolbar({ mode, modeBusy, connected, viewers, sidebar, hint = null, onModeChange, onLaunch, onToggleSidebar, machine }: DesktopToolbarProps) {
  return (
    <div className="flex min-h-12 flex-wrap items-center gap-x-3 gap-y-2 border-b border-base-content/10 px-3 py-2">
      <ModeSwitch mode={mode} busy={modeBusy} disabled={!connected} onChange={onModeChange} />
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
      <Button
        size="sm"
        variant="ghost"
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
