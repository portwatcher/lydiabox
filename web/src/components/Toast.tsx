import { CircleAlert, CircleCheck, X } from "lucide-react";
import { createContext, useCallback, useContext, useMemo, useRef, useState } from "react";
import type { ReactNode } from "react";

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
        className="toast toast-end toast-bottom pointer-events-none z-50 w-auto whitespace-normal max-sm:left-4 sm:w-96"
      >
        {toasts.map((toast) => (
          <div
            key={toast.id}
            role={toast.tone === "danger" ? "alert" : "status"}
            className="alert pointer-events-auto w-full items-start gap-2 border-base-content/20 bg-base-100 px-3 py-2.5 shadow-lg motion-safe:animate-fade-in"
          >
            {toast.tone === "success" ? (
              <CircleCheck aria-hidden className="mt-0.5 size-4 shrink-0 text-success" />
            ) : (
              <CircleAlert aria-hidden className="mt-0.5 size-4 shrink-0 text-error" />
            )}
            <p className="min-w-0 text-pretty">{toast.message}</p>
            <button
              type="button"
              aria-label="Dismiss"
              onClick={() => dismiss(toast.id)}
              className="btn btn-ghost btn-square btn-xs -my-0.5 text-base-content/70 hover:text-base-content"
            >
              <X aria-hidden className="size-4" />
            </button>
          </div>
        ))}
      </div>
    </ToastContext.Provider>
  );
}
