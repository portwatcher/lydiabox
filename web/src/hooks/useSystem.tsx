import { createContext, useCallback, useContext, useEffect, useMemo, useRef, useState } from "react";
import type { ReactNode } from "react";
import { availabilityOf, describeError } from "../api/errors";
import { getStatus, vm } from "../api/system";
import type { Status, VMState, VMStatus } from "../api/types";
import { useToast } from "../components/Toast";

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
  pending: VMAction | null;
  runVM: (action: VMAction) => Promise<boolean>;
  refresh: () => void;
}

const SystemContext = createContext<SystemState | null>(null);

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

  const vmState: VMState = pending === "wake" ? "BOOTING" : (vmStatus?.state ?? status?.vm.state ?? "STOPPED");
  const fast = vmState === "BOOTING" || pending !== null;

  useEffect(() => {
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout> | null = null;
    let disposed = false;

    const poll = async () => {
      try {
        const next = await getStatus(controller.signal);
        if (disposed) {
          return;
        }
        setStatus(next);
        setFetchedAt(Date.now());
        setError(null);
      } catch (err) {
        if (!disposed && !(err instanceof DOMException && err.name === "AbortError")) {
          setError(err);
        }
      }
      if (vmControlRef.current && !disposed) {
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
    () => ({ status, vmStatus, vmState, vmControl, fetchedAt, error, pending, runVM, refresh }),
    [status, vmStatus, vmState, vmControl, fetchedAt, error, pending, runVM, refresh],
  );

  return <SystemContext.Provider value={value}>{children}</SystemContext.Provider>;
}
