import { CircleAlert, CircleCheck, X } from "lucide-react";
import { createContext, useCallback, useContext, useMemo, useRef, useState } from "react";
import type { ReactNode } from "react";
import { cx } from "./ui";

type ToastTone = "success" | "danger";

interface Toast {
  id: number;
  tone: ToastTone;
  message: string;
}

interface ToastApi {
  success: (message: string) => void;
  error: (message: string) => void;
}

const ToastContext = createContext<ToastApi | null>(null);

export function useToast(): ToastApi {
  const api = useContext(ToastContext);
  if (!api) {
    throw new Error("useToast needs a ToastProvider");
  }
  return api;
}

/** Status messages in a polite live region; errors stay until dismissed. */
export function ToastProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([]);
  const nextId = useRef(1);

  const dismiss = useCallback((id: number) => {
    setToasts((all) => all.filter((t) => t.id !== id));
  }, []);

  const push = useCallback(
    (tone: ToastTone, message: string) => {
      const id = nextId.current++;
      setToasts((all) => [...all.slice(-3), { id, tone, message }]);
      if (tone === "success") {
        setTimeout(() => dismiss(id), 4000);
      }
    },
    [dismiss],
  );

  const api = useMemo<ToastApi>(
    () => ({ success: (m) => push("success", m), error: (m) => push("danger", m) }),
    [push],
  );

  return (
    <ToastContext.Provider value={api}>
      {children}
      <div
        aria-live="polite"
        className="pointer-events-none fixed right-4 bottom-4 left-4 z-50 flex flex-col items-end gap-2 sm:left-auto sm:w-96"
      >
        {toasts.map((toast) => (
          <div
            key={toast.id}
            role={toast.tone === "danger" ? "alert" : "status"}
            className={cx(
              "pointer-events-auto flex w-full items-start gap-2 rounded-lg border border-border bg-surface px-3 py-2.5 text-sm shadow-lg",
              "motion-safe:animate-fade-in",
            )}
          >
            {toast.tone === "success" ? (
              <CircleCheck aria-hidden className="mt-0.5 size-4 shrink-0 text-success-fg" />
            ) : (
              <CircleAlert aria-hidden className="mt-0.5 size-4 shrink-0 text-danger-fg" />
            )}
            <p className="min-w-0 flex-1 text-pretty text-fg">{toast.message}</p>
            <button
              type="button"
              aria-label="Dismiss"
              onClick={() => dismiss(toast.id)}
              className="-m-1 rounded p-1 text-fg-muted hover:text-fg"
            >
              <X aria-hidden className="size-4" />
            </button>
          </div>
        ))}
      </div>
    </ToastContext.Provider>
  );
}
