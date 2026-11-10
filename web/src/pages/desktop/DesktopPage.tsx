import { Box, Eye, Globe, Monitor, MousePointer2, PanelRightClose, PanelRightOpen, Play, RotateCw, SquareTerminal, Users } from "lucide-react";
import type { LucideIcon } from "lucide-react";
import { useCallback, useEffect, useReducer, useRef, useState } from "react";
import { desktop } from "../../api/domains";
import { availabilityOf, describeError } from "../../api/errors";
import type { AgentAction, DesktopMode, LaunchApp } from "../../api/types";
import { ErrorView, LoadingView, StateView } from "../../components/StateView";
import { useToast } from "../../components/Toast";
import { VmPower } from "../../components/VmPower";
import { Banner, Button, cx } from "../../components/ui";
import { useSystem } from "../../hooks/useSystem";
import { count } from "../../lib/format";
import { ActionFeed } from "./ActionFeed";
import { DesktopOverlay } from "./DesktopOverlay";
import { initialOverlay, overlayReducer } from "./overlay";
import { useDesktopStream } from "./useDesktopStream";
import { useTakeoverInput } from "./useTakeoverInput";

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

export default function DesktopPage() {
  const toast = useToast();
  const { vmState } = useSystem();
  const [overlay, dispatch] = useReducer(overlayReducer, initialOverlay);
  const onAction = useCallback((action: AgentAction) => {
    dispatch({ type: "action", action, now: Date.now() });
  }, []);
  const stream = useDesktopStream(onAction);
  const [probe, setProbe] = useState<{ state: "checking" | "ok" | "error"; error?: unknown }>({ state: "checking" });
  const [sidebar, setSidebar] = useState(() => window.matchMedia("(min-width: 1024px)").matches);
  const [modeBusy, setModeBusy] = useState(false);
  const [keyboardHeld, setKeyboardHeld] = useState(false);
  const stageRef = useRef<HTMLDivElement>(null);
  const videoRef = useRef<HTMLVideoElement>(null);
  const prevVm = useRef(vmState);
  const { connect, disconnect, setMode } = stream;

  // The desktop routes may not be in this build yet: probe the mode route once.
  const runProbe = useCallback(() => {
    setProbe({ state: "checking" });
    desktop
      .mode()
      .then((m) => {
        setMode(m.mode);
        setProbe({ state: "ok" });
      })
      .catch((error: unknown) => {
        const kind = availabilityOf(error);
        // 503 means the VM is stopped, which the page handles itself.
        setProbe(kind === "unavailable" ? { state: "ok" } : { state: "error", error });
      });
  }, [setMode]);
  useEffect(runProbe, [runProbe]);

  // Connect on its own only when the VM already runs; connecting wakes a stopped VM.
  useEffect(() => {
    if (probe.state === "ok" && vmState === "RUNNING" && stream.socket === null) {
      connect();
    }
  }, [probe.state, vmState, stream.socket, connect]);

  // A stop or hibernate elsewhere ends the stream instead of waking the VM again.
  useEffect(() => {
    const was = prevVm.current;
    prevVm.current = vmState;
    if (was === "RUNNING" && (vmState === "STOPPED" || vmState === "PAUSED")) {
      disconnect();
      dispatch({ type: "reset" });
    }
  }, [vmState, disconnect]);

  useEffect(() => {
    if (overlay.ripples.length === 0 && overlay.pills.length === 0 && !overlay.drag) {
      return;
    }
    const id = setInterval(() => dispatch({ type: "expire", now: Date.now() }), 100);
    return () => clearInterval(id);
  }, [overlay.ripples.length, overlay.pills.length, overlay.drag]);

  useEffect(() => {
    if (videoRef.current) {
      videoRef.current.srcObject = stream.stream;
    }
  }, [stream.stream]);

  const takeover = stream.mode === "TAKEOVER";
  useTakeoverInput(stageRef, takeover && stream.inputReady, stream.screen, stream.sendInput, () => setKeyboardHeld(false));

  const changeMode = async (mode: DesktopMode) => {
    setModeBusy(true);
    try {
      const result = await desktop.setMode(mode);
      stream.setMode(result.mode);
      if (result.mode === "TAKEOVER") {
        stageRef.current?.focus({ preventScroll: true });
      }
    } catch (err) {
      toast.error(describeError(err, mode === "TAKEOVER" ? "Takeover not started" : "Control not handed back"));
    } finally {
      setModeBusy(false);
    }
  };

  const launch = (app: LaunchApp, label: string) => {
    if (!stream.launch(app)) {
      toast.error(`${label} not launched. The desktop is not connected; reconnect, then try again.`);
    }
  };

  if (probe.state === "checking") {
    return <LoadingView label="Loading desktop…" />;
  }
  if (probe.state === "error") {
    return <ErrorView error={probe.error} subsystem="The desktop" failed="Desktop not loaded" onRetry={runProbe} />;
  }

  const connected = stream.socket === "open";
  const live = connected && stream.stream !== null;

  let stageContent = null;
  if (stream.socket === null) {
    stageContent = (
      <StateView
        icon={Monitor}
        title={vmState === "PAUSED" ? "The VM is hibernating" : vmState === "BOOTING" ? "The VM is starting" : "The desktop is off"}
        action={
          <Button variant="primary" icon={Play} onClick={connect}>
            Start desktop
          </Button>
        }
      >
        Starting the desktop wakes the VM.
      </StateView>
    );
  } else if (stream.socket === "failed") {
    stageContent = (
      <StateView title="Desktop connection lost" action={<Button icon={RotateCw} onClick={stream.retry}>Reconnect</Button>}>
        The VM may have stopped, or the desktop stream failed to start.
      </StateView>
    );
  } else if (!live) {
    stageContent = <LoadingView label={stream.socket === "reconnecting" ? "Reconnecting to the desktop…" : "Starting the desktop stream…"} />;
  }

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="flex min-h-12 flex-wrap items-center gap-x-3 gap-y-2 border-b border-border-subtle px-3 py-2">
        <ModeSwitch mode={stream.mode} busy={modeBusy} disabled={!connected} onChange={(m) => void changeMode(m)} />
        <div className="flex flex-wrap items-center gap-1">
          {apps.map(({ app, label, icon }) => (
            <Button key={app} size="sm" variant="ghost" icon={icon} disabled={!connected} onClick={() => launch(app, label)}>
              {label}
            </Button>
          ))}
        </div>
        <span className="inline-flex items-center gap-1.5 text-xs text-fg-muted tabular-nums">
          <Users aria-hidden className="size-4" />
          {count(stream.viewers, { one: "# viewer", other: "# viewers" })}
        </span>
        <div className="ml-auto flex items-center gap-2">
          <VmPower compact />
          <Button
            size="sm"
            variant="ghost"
            icon={sidebar ? PanelRightClose : PanelRightOpen}
            aria-expanded={sidebar}
            aria-controls="desktop-feed"
            onClick={() => setSidebar((s) => !s)}
          >
            Activity
          </Button>
        </div>
      </div>
      {stream.error ? (
        <div className="px-3 pt-2">
          <Banner tone="danger">{stream.error}</Banner>
        </div>
      ) : null}
      <div className="relative flex min-h-0 flex-1">
        <div
          ref={stageRef}
          tabIndex={takeover && live ? 0 : -1}
          role={takeover ? "application" : undefined}
          aria-label={takeover ? "Remote desktop. Shift+Escape releases the keyboard." : "Remote desktop"}
          onFocus={() => setKeyboardHeld(true)}
          onBlur={() => setKeyboardHeld(false)}
          className={cx(
            "relative min-w-0 flex-1 overflow-hidden bg-black outline-none select-none",
            takeover && live ? "cursor-none touch-none" : "",
            takeover && live && keyboardHeld ? "ring-2 ring-accent-solid ring-inset" : "",
          )}
        >
          <video ref={videoRef} autoPlay playsInline muted className={cx("absolute inset-0 h-full w-full object-contain", !live && "invisible")} />
          {live ? <DesktopOverlay state={overlay} screen={stream.screen} /> : null}
          {stageContent ? <div className="absolute inset-0 bg-bg">{stageContent}</div> : null}
          {takeover && live ? (
            <span className="pointer-events-none absolute top-3 left-3 rounded-md bg-surface px-2 py-1 text-xs text-fg shadow">
              {keyboardHeld ? "Shift+Esc releases the keyboard" : "Click the desktop to take control"}
            </span>
          ) : null}
        </div>
        {sidebar ? (
          <aside
            id="desktop-feed"
            aria-label="Agent activity"
            className="absolute inset-y-0 right-0 z-10 flex w-80 max-w-full flex-col border-l border-border-subtle bg-bg-subtle lg:static"
          >
            <h2 className="border-b border-border-subtle px-4 py-3 text-sm font-semibold">Agent activity</h2>
            <div className="min-h-0 flex-1 overflow-y-auto">
              <ActionFeed feed={overlay.feed} />
            </div>
          </aside>
        ) : null}
      </div>
    </div>
  );
}
