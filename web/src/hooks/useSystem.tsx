import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from "react";
import type { ReactNode } from "react";
import { availabilityOf, describeError } from "../api/errors";
import { getStatus, vm } from "../api/system";
import type { Status, VMState, VMStatus } from "../api/types";
import { useToast } from "../components/Toast";
import { STATUS_GRACE_MS, statusOutage } from "../lib/outage";
import type { Outage } from "../lib/outage";
import { actionContinues, maybeCut } from "../lib/vmAction";

export type VMAction = "wake" | "hibernate" | "stop";

export interface SystemState {
  status: Status | null;
  /** Detailed VM status (section 7); null while the route is not available. */
  vmStatus: VMStatus | null;
  vmState: VMState;
  /** False when the VM power routes are not in this build (404/501). */
  vmControl: boolean;
  /** Client time when `status` was fetched, to advance server-relative clocks. */
  fetchedAt: number;
  /** Set when the last status poll failed. */
  error: unknown;
  /**
   * The status poll fails in a way that makes the last state no fact: the
   * cell is stopped or starts (LydiaBox Cloud), or no status came for
   * STATUS_GRACE_MS. Power controls are hidden while it is set; it clears
   * at the next status answer.
   */
  outage: Outage | null;
  /** A pause or stop that runs, started here (pending) or elsewhere (the VM status). */
  transition: "pausing" | "stopping" | null;
  /** Why a start or resume cannot work now (the provider refuses it until a time), or null. */
  startBlocked: string | null;
  pending: VMAction | null;
  /** The VM has been starting or resuming for more than SLOW_START_MS. */
  slowStart: boolean;
  runVM: (action: VMAction) => Promise<boolean>;
  refresh: () => void;
}

/** After this time, a start or resume is slow, and the status bar says that the provider is slow. */
export const SLOW_START_MS = 15_000;

const SystemContext = createContext<SystemState | null>(null);

// True while the server cannot answer (an outage): data hooks pause their
// polls and load again when it ends. False outside a SystemProvider.
const ServerDownContext = createContext(false);

/** Whether the server cannot answer now (useSystem().outage is set); safe outside a SystemProvider. */
export function useServerDown(): boolean {
  return useContext(ServerDownContext);
}

export function useSystem(): SystemState {
  const value = useContext(SystemContext);
  if (!value) {
    throw new Error("useSystem needs a SystemProvider");
  }
  return value;
}

const POLL_MS = 5000;
const FAST_POLL_MS = 1500;

const actionLabels: Record<VMAction, string> = {
  wake: "VM not started",
  hibernate: "VM not hibernated",
  stop: "VM not stopped",
};

export function SystemProvider({ children }: { children: ReactNode }) {
  const toast = useToast();
  const [status, setStatus] = useState<Status | null>(null);
  const [vmStatus, setVmStatus] = useState<VMStatus | null>(null);
  const [vmControl, setVmControl] = useState(true);
  const [fetchedAt, setFetchedAt] = useState(() => Date.now());
  const [error, setError] = useState<unknown>(null);
  const [pending, setPending] = useState<VMAction | null>(null);
  const [tick, setTick] = useState(0);
  const vmControlRef = useRef(true);
  const [failing, setFailing] = useState(false);
  const [graceOver, setGraceOver] = useState(false);

  // One lost poll changes nothing; a status that stays away for
  // STATUS_GRACE_MS is no longer shown as a fact.
  useEffect(() => {
    if (!failing) {
      setGraceOver(false);
      return;
    }
    const id = setTimeout(() => setGraceOver(true), STATUS_GRACE_MS);
    return () => clearTimeout(id);
  }, [failing]);
  const outage = useMemo(() => statusOutage(error, graceOver), [error, graceOver]);

  const reported: VMState = pending === "wake" ? "BOOTING" : (vmStatus?.state ?? status?.vm.state ?? "STOPPED");
  const vmState: VMState = outage?.kind === "stopped" ? "STOPPED" : outage?.kind === "starting" ? "BOOTING" : reported;
  const transition = outage
    ? null
    : pending === "hibernate"
      ? "pausing"
      : pending === "stop"
        ? "stopping"
        : (vmStatus?.transition ?? null);
  const startBlocked = !outage && vmStatus?.startBlockedUntil ? vmStatus.lastError || "The VM provider does not start the VM now." : null;
  const fast = vmState === "BOOTING" || pending !== null || transition !== null;

  // A start or resume can take minutes at a cloud provider: say so after
  // SLOW_START_MS, so that LydiaBox does not look stuck.
  const booting = vmState === "BOOTING";
  const [slowStart, setSlowStart] = useState(false);
  useEffect(() => {
    if (!booting) {
      setSlowStart(false);
      return;
    }
    const id = setTimeout(() => setSlowStart(true), SLOW_START_MS);
    return () => clearTimeout(id);
  }, [booting]);

  useEffect(() => {
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout> | null = null;
    let disposed = false;

    const poll = async () => {
      let statusOK = false;
      try {
        const next = await getStatus(controller.signal);
        if (disposed) {
          return;
        }
        setStatus(next);
        setFetchedAt(Date.now());
        setError(null);
        setFailing(false);
        statusOK = true;
      } catch (err) {
        if (!disposed && !(err instanceof DOMException && err.name === "AbortError")) {
          setError(err);
          setFailing(true);
        }
      }
      // After a failed status call the VM status call fails too: skip it.
      if (vmControlRef.current && !disposed && statusOK) {
        try {
          const detail = await vm.status(controller.signal);
          if (!disposed) {
            setVmStatus(detail);
          }
        } catch (err) {
          const kind = availabilityOf(err);
          if (!disposed && (kind === "missing" || kind === "not_implemented")) {
            vmControlRef.current = false;
            setVmControl(false);
            setVmStatus(null);
          }
        }
      }
      if (!disposed) {
        timer = setTimeout(() => void poll(), fast ? FAST_POLL_MS : POLL_MS);
      }
    };

    void poll();
    return () => {
      disposed = true;
      controller.abort();
      if (timer) {
        clearTimeout(timer);
      }
    };
  }, [fast, tick]);

  const refresh = useCallback(() => setTick((t) => t + 1), []);

  const runVM = useCallback(
    async (action: VMAction) => {
      setPending(action);
      try {
        const next = action === "wake" ? await vm.wake() : action === "hibernate" ? await vm.hibernate() : await vm.stop();
        setVmStatus(next);
        return true;
      } catch (err) {
        const kind = availabilityOf(err);
        if (kind === "missing" || kind === "not_implemented") {
          vmControlRef.current = false;
          setVmControl(false);
        }
        // A proxy can end a long hibernate or stop request while the
        // server continues it: show the VM status, not a failure.
        if (action !== "wake" && maybeCut(err)) {
          try {
            const detail = await vm.status();
            if (actionContinues(action, detail)) {
              setVmStatus(detail);
              return true;
            }
          } catch {
            // The status is not available either: report the failure.
          }
        }
        toast.error(describeError(err, actionLabels[action]));
        return false;
      } finally {
        setPending(null);
        setTick((t) => t + 1);
      }
    },
    [toast],
  );

  const value = useMemo<SystemState>(
    () => ({ status, vmStatus, vmState, vmControl, fetchedAt, error, outage, transition, startBlocked, pending, slowStart, runVM, refresh }),
    [status, vmStatus, vmState, vmControl, fetchedAt, error, outage, transition, startBlocked, pending, slowStart, runVM, refresh],
  );

  return (
    <SystemContext.Provider value={value}>
      <ServerDownContext.Provider value={outage !== null}>{children}</ServerDownContext.Provider>
    </SystemContext.Provider>
  );
}
