import { X } from "lucide-react";
import { useEffect, useId, useRef, useState } from "react";
import type { ReactNode } from "react";
import { describeError } from "../api/errors";
import { Button, IconButton, cx } from "./ui";

export interface DialogProps {
  open: boolean;
  onClose: () => void;
  title: string;
  children: ReactNode;
  footer?: ReactNode;
  /** `modal` is centered; `drawer` slides in from the right edge. */
  variant?: "modal" | "drawer";
  width?: "sm" | "md" | "lg" | "xl";
}

const widths = { sm: "max-w-md", md: "max-w-lg", lg: "max-w-2xl", xl: "max-w-4xl" };

/**
 * Native <dialog> opened with showModal(): the browser provides the focus
 * trap, Escape to close, inert background, and focus return.
 */
export function Dialog({ open, onClose, title, children, footer, variant = "modal", width = "md" }: DialogProps) {
  const ref = useRef<HTMLDialogElement>(null);
  const titleId = useId();

  useEffect(() => {
    const dialog = ref.current;
    if (!dialog) {
      return;
    }
    if (open && !dialog.open) {
      dialog.showModal();
    } else if (!open && dialog.open) {
      dialog.close();
    }
  }, [open]);

  return (
    <dialog
      ref={ref}
      aria-labelledby={titleId}
      onCancel={(event) => {
        event.preventDefault();
        onClose();
      }}
      onClick={(event) => {
        // A click on the backdrop targets the dialog element itself.
        if (event.target === ref.current) {
          onClose();
        }
      }}
      className={cx(
        "bg-bg-subtle text-fg backdrop:bg-black/60 border border-border p-0 shadow-2xl",
        "motion-safe:open:animate-fade-in",
        variant === "drawer"
          ? "my-0 mr-0 ml-auto h-dvh max-h-dvh w-full max-w-md rounded-none border-y-0 border-r-0"
          : cx("m-auto w-[calc(100%-2rem)] rounded-xl", widths[width]),
      )}
    >
      {open ? (
        <div className={cx("flex flex-col", variant === "drawer" ? "h-full" : "max-h-[calc(100dvh-4rem)]")}>
          <header className="flex items-center justify-between gap-3 border-b border-border-subtle px-5 py-3">
            <h2 id={titleId} className="text-base font-semibold text-balance">
              {title}
            </h2>
            <IconButton icon={X} label="Close" onClick={onClose} />
          </header>
          <div className="min-h-0 flex-1 overflow-y-auto px-5 py-4">{children}</div>
          {footer ? (
            <footer className="flex flex-wrap items-center justify-end gap-2 border-t border-border-subtle px-5 py-3">
              {footer}
            </footer>
          ) : null}
        </div>
      ) : null}
    </dialog>
  );
}

export interface ConfirmProps {
  open: boolean;
  title: string;
  body: ReactNode;
  confirmLabel: string;
  onConfirm: () => Promise<void> | void;
  onClose: () => void;
  /** Failure sentence used when onConfirm throws, such as "Session not deleted". */
  failed: string;
  destructive?: boolean;
  /** Extra controls such as a "Delete transcript files" checkbox. */
  extra?: ReactNode;
}

/** Confirms an action; the title names it, the buttons repeat the verb. */
export function ConfirmDialog({ open, title, body, confirmLabel, onConfirm, onClose, failed, destructive = true, extra }: ConfirmProps) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (open) {
      setError(null);
      setBusy(false);
    }
  }, [open]);

  const run = async () => {
    setBusy(true);
    setError(null);
    try {
      await onConfirm();
      onClose();
    } catch (err) {
      setError(describeError(err, failed));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog
      open={open}
      onClose={onClose}
      title={title}
      width="sm"
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button variant={destructive ? "danger" : "primary"} busy={busy} onClick={() => void run()}>
            {confirmLabel}
          </Button>
        </>
      }
    >
      <div className="flex flex-col gap-3 text-sm text-fg-muted text-pretty">
        {body}
        {extra}
        {error ? (
          <p role="alert" className="text-danger-fg">
            {error}
          </p>
        ) : null}
      </div>
    </Dialog>
  );
}
