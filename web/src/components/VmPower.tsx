import { Cpu, Moon, Play, Square } from "lucide-react";
import { useState } from "react";
import type { ReactNode } from "react";
import type { VMState } from "../api/types";
import { useSystem } from "../hooks/useSystem";
import { formatMemory } from "../lib/format";
import { ConfirmDialog } from "./Dialog";
import { Button, Spinner, badgeTones, cx } from "./ui";
import type { Tone } from "./ui";

const stateTone: Record<VMState, Tone> = {
  RUNNING: "success",
  BOOTING: "warning",
  PAUSED: "accent",
  STOPPED: "neutral",
};

const statusDot: Record<Tone, string> = {
  success: "status-success",
  warning: "status-warning",
  accent: "status-info",
  neutral: "",
  danger: "status-error",
};

const pill = "badge h-6 rounded-full px-2.5 text-xs font-semibold tracking-wide";

/** The VM state as a labeled pill; the dot repeats the state, the label carries it. `unknown` shows that no status came. */
export function VmPill({
  state,
  unknown = false,
  transition = null,
}: {
  state: VMState;
  unknown?: boolean;
  /** A pause or stop that runs: shown instead of the state. */
  transition?: "pausing" | "stopping" | null;
}) {
  if (unknown) {
    return (
      <span className={cx(pill, badgeTones.warning)}>
        <span aria-hidden className={cx("status", statusDot.warning)} />
        <span className="sr-only">VM </span>
        NO STATUS
      </span>
    );
  }
  if (transition) {
    return (
      <span className={cx(pill, badgeTones.warning)}>
        <Spinner size="xs" className="" />
        <span className="sr-only">VM </span>
        {transition === "pausing" ? "PAUSING" : "STOPPING"}
      </span>
    );
  }
  const tone = stateTone[state];
  return (
    <span className={cx(pill, badgeTones[tone])}>
      {state === "BOOTING" ? <Spinner size="xs" className="" /> : <span aria-hidden className={cx("status", statusDot[tone])} />}
      <span className="sr-only">VM </span>
      {state}
    </span>
  );
}

interface VmWakeButtonProps {
  size?: "sm" | "md";
  variant?: "primary" | "secondary";
  /** Replaces the Start / Resume label. */
  children?: ReactNode;
}

/**
 * Starts a stopped VM ("Start") or resumes a paused one ("Resume") with
 * POST /api/v1/vm/wake: the explicit start, the only one that undoes a stop.
 */
export function VmWakeButton({ size = "md", variant = "secondary", children }: VmWakeButtonProps) {
  const { vmState, vmControl, pending, runVM, startBlocked } = useSystem();
  return (
    <Button
      size={size}
      variant={variant}
      icon={Play}
      busy={pending === "wake"}
      disabled={!vmControl || pending !== null || startBlocked !== null}
      title={vmControl ? (startBlocked ?? undefined) : unavailableTitle}
      onClick={() => void runVM("wake")}
    >
      {children ?? (vmState === "PAUSED" ? "Resume" : "Start")}
    </Button>
  );
}

const unavailableTitle = "VM power control is not available in this build";

/** Start or Resume / Hibernate / Stop controls for the current VM state. */
export function VmPower({ compact = false }: { compact?: boolean }) {
  const { vmState, vmControl, pending, runVM, outage, transition } = useSystem();
  const [confirmStop, setConfirmStop] = useState(false);
  const size = compact ? "sm" : "md";
  const unavailable = vmControl ? undefined : unavailableTitle;
  // During an outage the server in front of the VM refuses every call, and
  // during a pause or stop the next state is not known yet: no power
  // control shows. (A pending action of this tab shows its busy button.)
  const settled = !outage && (transition === null || pending !== null);
  const canWake = settled && (vmState === "STOPPED" || vmState === "PAUSED");
  const canHibernate = settled && vmState === "RUNNING";
  const canStop = settled && (vmState === "RUNNING" || vmState === "PAUSED");

  return (
    <div className="flex items-center gap-2">
      <VmPill state={vmState} unknown={outage?.kind === "unknown"} transition={pending === null ? transition : null} />
      {canWake ? <VmWakeButton size={size} variant="secondary" /> : null}
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

/** CPUs, memory, GPU, and driver of the VM. */
export function VmResources() {
  const { vmStatus, status, outage } = useSystem();
  if (outage) {
    // Not current while the status poll fails.
    return null;
  }
  const driver = vmStatus?.driver ?? status?.vm.driver;
  return (
    <span className="hidden items-center gap-1.5 text-xs text-base-content/70 tabular-nums md:inline-flex">
      <Cpu aria-hidden className="size-4" />
      {vmStatus && vmStatus.cpus > 0 ? (
        <span>
          {vmStatus.cpus} vCPU · {formatMemory(vmStatus.memMb)}
          {vmStatus.gpu ? " · GPU" : ""}
        </span>
      ) : null}
      {driver ? <span className="badge badge-soft badge-sm font-mono text-base-content/70">{driver}</span> : null}
    </span>
  );
}

/**
 * A start, resume, pause, or stop that runs. Pages start or resume the VM
 * when they need it, so this is the only VM state outside the Desktop page.
 */
export function VmActivity() {
  const { vmState, transition, outage } = useSystem();
  if (outage) {
    return null;
  }
  let label = null;
  if (transition === "pausing") {
    label = "Hibernating the VM…";
  } else if (transition === "stopping") {
    label = "Stopping the VM…";
  } else if (vmState === "BOOTING") {
    label = "Starting the VM…";
  }
  if (!label) {
    return null;
  }
  return (
    <span role="status" className="inline-flex items-center gap-1.5 text-xs text-base-content/70">
      <Spinner size="xs" className="" />
      {label}
    </span>
  );
}
