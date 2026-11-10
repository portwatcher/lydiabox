import { LoaderCircle, Moon, Play, Square } from "lucide-react";
import { useState } from "react";
import type { VMState } from "../api/types";
import { useSystem } from "../hooks/useSystem";
import { ConfirmDialog } from "./Dialog";
import { Button, cx } from "./ui";
import type { Tone } from "./ui";

const stateTone: Record<VMState, Tone> = {
  RUNNING: "success",
  BOOTING: "warning",
  PAUSED: "accent",
  STOPPED: "neutral",
};

const toneClass: Record<Tone, string> = {
  success: "bg-success-subtle text-success-fg",
  warning: "bg-warning-subtle text-warning-fg",
  accent: "bg-accent-subtle text-accent-fg",
  neutral: "bg-surface-active text-fg-muted",
  danger: "bg-danger-subtle text-danger-fg",
};

/** The VM state as a labeled pill; the dot repeats the state, the label carries it. */
export function VmPill({ state }: { state: VMState }) {
  return (
    <span
      className={cx(
        "inline-flex h-6 items-center gap-1.5 rounded-full px-2.5 text-xs font-semibold tracking-wide",
        toneClass[stateTone[state]],
      )}
    >
      {state === "BOOTING" ? (
        <LoaderCircle aria-hidden className="size-3 motion-safe:animate-spin" />
      ) : (
        <span aria-hidden className="size-1.5 rounded-full bg-current" />
      )}
      <span className="sr-only">VM </span>
      {state}
    </span>
  );
}

/** Wake / Hibernate / Stop controls for the current VM state. */
export function VmPower({ compact = false }: { compact?: boolean }) {
  const { vmState, vmControl, pending, runVM } = useSystem();
  const [confirmStop, setConfirmStop] = useState(false);
  const size = compact ? "sm" : "md";
  const unavailable = vmControl ? undefined : "VM power control is not available in this build";
  const canWake = vmState === "STOPPED" || vmState === "PAUSED";
  const canHibernate = vmState === "RUNNING";
  const canStop = vmState === "RUNNING" || vmState === "PAUSED";

  return (
    <div className="flex items-center gap-2">
      <VmPill state={vmState} />
      {canWake ? (
        <Button
          size={size}
          variant="secondary"
          icon={Play}
          busy={pending === "wake"}
          disabled={!vmControl || pending !== null}
          title={unavailable}
          onClick={() => void runVM("wake")}
        >
          {vmState === "PAUSED" ? "Resume" : "Wake"}
        </Button>
      ) : null}
      {canHibernate ? (
        <Button
          size={size}
          variant="ghost"
          icon={Moon}
          busy={pending === "hibernate"}
          disabled={!vmControl || pending !== null}
          title={unavailable}
          onClick={() => void runVM("hibernate")}
        >
          Hibernate
        </Button>
      ) : null}
      {canStop ? (
        <Button
          size={size}
          variant="ghost"
          icon={Square}
          busy={pending === "stop"}
          disabled={!vmControl || pending !== null}
          title={unavailable}
          onClick={() => setConfirmStop(true)}
        >
          Stop
        </Button>
      ) : null}
      <ConfirmDialog
        open={confirmStop}
        onClose={() => setConfirmStop(false)}
        title="Stop the VM?"
        confirmLabel="Stop VM"
        failed="VM not stopped"
        body="Running terminals and desktop apps end. Files in /workspace and /home/agent stay saved."
        onConfirm={async () => {
          await runVM("stop");
        }}
      />
    </div>
  );
}
