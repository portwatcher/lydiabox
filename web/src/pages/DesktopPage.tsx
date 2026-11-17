import { Monitor, RotateCw } from "lucide-react";
import { useCallback, useEffect, useReducer, useRef, useState } from "react";
import { desktop } from "../api/domains";
import { availabilityOf, describeError } from "../api/errors";
import type { AgentAction, DesktopMode, LaunchApp } from "../api/types";
import { ErrorView, LoadingView, StateView } from "../components/StateView";
import { useToast } from "../components/Toast";
import { Banner, Button, cx } from "../components/ui";
import { VmWakeButton } from "../components/VmPower";
import { useSystem } from "../hooks/useSystem";
import { ActionFeed } from "./desktop/ActionFeed";
import { DesktopOverlay } from "./desktop/DesktopOverlay";
import { DesktopToolbar } from "./desktop/DesktopToolbar";
import { initialOverlay, overlayReducer } from "./desktop/overlay";
import { desktopStage, shouldConnect, shouldDisconnect } from "./desktop/stage";
import { useDesktopStream } from "./desktop/useDesktopStream";
import { useTakeoverInput } from "./desktop/useTakeoverInput";

export default function DesktopPage() {
  const toast = useToast();
  const { vmState, pending, refresh, fetchedAt } = useSystem();
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

  // Stream only a running VM. Opening the stream never starts the VM: the
  // start control does (the status bar, or the stage below).
  const stageInput = { vmState, pending, socket: stream.socket, stopped: stream.stopped, paused: stream.paused, live: false };
  const connectNow = probe.state === "ok" && shouldConnect(stageInput);
  useEffect(() => {
    if (connectNow) {
      connect();
    }
  }, [connectNow, connect]);

  // A stop, hibernate, or start elsewhere closes the stream before the guest goes down.
  const disconnectNow = shouldDisconnect({ vmState, pending });
  useEffect(() => {
    if (disconnectNow) {
      disconnect();
      dispatch({ type: "reset" });
    }
  }, [disconnectNow, disconnect]);

  // The server ended the stream for a user stop: read the new VM state now.
  useEffect(() => {
    if (stream.stopped) {
      refresh();
    }
  }, [stream.stopped, refresh]);

  // The server ended or refused the stream because the VM pauses (from any
  // source, also another tab or the idle pause). The page shows the
  // hibernating state and connects again only after a newer status says
  // that the VM runs: a reconnect must not wake the VM.
  const pausedSeenAt = useRef<number | null>(null);
  const clearPaused = stream.clearPaused;
  useEffect(() => {
    if (!stream.paused) {
      pausedSeenAt.current = null;
      return;
    }
    if (pausedSeenAt.current === null) {
      pausedSeenAt.current = fetchedAt;
      refresh();
      return;
    }
    if (fetchedAt !== pausedSeenAt.current) {
      clearPaused();
    }
  }, [stream.paused, fetchedAt, refresh, clearPaused]);

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
  // Live only once the video connection is up: before that the stage would
  // show a black screen.
  const live = connected && stream.stream !== null && stream.peer === "connected";
  const stage = desktopStage({ ...stageInput, live, peer: stream.peer === "new" ? undefined : stream.peer });

  let stageContent = null;
  switch (stage) {
    case "stopped":
    case "hibernating":
      stageContent = (
        <StateView
          icon={Monitor}
          title={stage === "hibernating" ? "The VM is hibernating" : "The VM is stopped"}
          action={<VmWakeButton variant="primary">Start desktop</VmWakeButton>}
        />
      );
      break;
    case "starting":
      stageContent = <LoadingView label="Starting the VM…" />;
      break;
    case "stopping":
      stageContent = <LoadingView label={pending === "hibernate" ? "Hibernating the VM…" : "Stopping the VM…"} />;
      break;
    case "lost":
      stageContent = (
        <StateView title="Desktop connection lost" action={<Button icon={RotateCw} onClick={stream.retry}>Reconnect</Button>}>
          The desktop stream failed to start.
        </StateView>
      );
      break;
    case "videoFailed":
      stageContent = (
        <StateView title="Desktop video not connected" action={<Button icon={RotateCw} onClick={stream.retry}>Reconnect</Button>}>
          The browser could not reach the video relay.
        </StateView>
      );
      break;
    case "connecting":
      stageContent = <LoadingView label={stream.socket === "reconnecting" ? "Reconnecting to the desktop…" : "Starting the desktop stream…"} />;
      break;
    case "live":
      break;
  }

  return (
    <div className="flex h-full min-h-0 flex-col">
      <DesktopToolbar
        mode={stream.mode}
        modeBusy={modeBusy}
        connected={connected}
        viewers={stream.viewers}
        sidebar={sidebar}
        hint={takeover && live ? (keyboardHeld ? "Shift+Esc releases the keyboard" : "Click the desktop to take control") : null}
        onModeChange={(m) => void changeMode(m)}
        onLaunch={launch}
        onToggleSidebar={() => setSidebar((s) => !s)}
      />
      {stream.error && stream.socket !== null && stage !== "videoFailed" ? (
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
