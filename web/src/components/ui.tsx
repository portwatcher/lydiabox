import { LoaderCircle } from "lucide-react";
import type { LucideIcon } from "lucide-react";
import { forwardRef } from "react";
import type { ButtonHTMLAttributes, InputHTMLAttributes, ReactNode, SelectHTMLAttributes } from "react";

export function cx(...classes: (string | false | null | undefined)[]): string {
  return classes.filter(Boolean).join(" ");
}

type Variant = "primary" | "secondary" | "ghost" | "danger";
type Size = "sm" | "md";

const variants: Record<Variant, string> = {
  primary: "bg-accent-solid text-on-accent hover:bg-accent-solid-hover border border-transparent",
  secondary: "bg-surface text-fg border border-border hover:bg-surface-hover",
  ghost: "bg-transparent text-fg-muted border border-transparent hover:bg-surface-hover hover:text-fg",
  danger: "bg-danger-solid text-on-accent hover:bg-danger-solid-hover border border-transparent",
};

const sizes: Record<Size, string> = {
  sm: "h-8 px-2.5 text-[13px] gap-1.5",
  md: "h-9 px-3.5 text-sm gap-2",
};

export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: Variant;
  size?: Size;
  icon?: LucideIcon;
  busy?: boolean;
}

export const Button = forwardRef<HTMLButtonElement, ButtonProps>(function Button(
  { variant = "secondary", size = "md", icon: Icon, busy = false, className, children, disabled, type, ...rest },
  ref,
) {
  return (
    <button
      ref={ref}
      type={type ?? "button"}
      disabled={disabled || busy}
      aria-busy={busy || undefined}
      className={cx(
        "inline-flex shrink-0 items-center justify-center rounded-md font-medium whitespace-nowrap select-none",
        "transition-colors duration-150 disabled:cursor-not-allowed disabled:opacity-50",
        variants[variant],
        sizes[size],
        className,
      )}
      {...rest}
    >
      {busy ? (
        <LoaderCircle aria-hidden className="size-4 motion-safe:animate-spin" />
      ) : Icon ? (
        <Icon aria-hidden className="size-4" />
      ) : null}
      {children}
    </button>
  );
});

export interface IconButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  icon: LucideIcon;
  /** Accessible name and tooltip; names the action. */
  label: string;
  size?: Size;
  tone?: "default" | "danger";
}

export function IconButton({ icon: Icon, label, size = "sm", tone = "default", className, type, ...rest }: IconButtonProps) {
  return (
    <button
      type={type ?? "button"}
      aria-label={label}
      title={label}
      className={cx(
        "inline-flex shrink-0 items-center justify-center rounded-md text-fg-muted transition-colors duration-150",
        "hover:bg-surface-hover disabled:cursor-not-allowed disabled:opacity-50",
        tone === "danger" ? "hover:text-danger-fg" : "hover:text-fg",
        size === "sm" ? "size-8" : "size-9",
        className,
      )}
      {...rest}
    >
      <Icon aria-hidden className="size-4" />
    </button>
  );
}

export function Spinner({ className }: { className?: string }) {
  return <LoaderCircle aria-hidden className={cx("size-4 motion-safe:animate-spin text-fg-muted", className)} />;
}

export type Tone = "neutral" | "accent" | "success" | "warning" | "danger";

const tones: Record<Tone, string> = {
  neutral: "bg-surface-active text-fg-muted",
  accent: "bg-accent-subtle text-accent-fg",
  success: "bg-success-subtle text-success-fg",
  warning: "bg-warning-subtle text-warning-fg",
  danger: "bg-danger-subtle text-danger-fg",
};

export function Badge({ tone = "neutral", icon: Icon, children, className }: { tone?: Tone; icon?: LucideIcon; children: ReactNode; className?: string }) {
  return (
    <span
      className={cx(
        "inline-flex h-5 shrink-0 items-center gap-1 rounded px-1.5 text-xs font-medium whitespace-nowrap",
        tones[tone],
        className,
      )}
    >
      {Icon ? <Icon aria-hidden className="size-3" /> : null}
      {children}
    </span>
  );
}

const control =
  "h-9 w-full min-w-0 rounded-md border border-border-input bg-bg-subtle px-3 text-sm text-fg placeholder:text-fg-muted disabled:opacity-50";

export const TextInput = forwardRef<HTMLInputElement, InputHTMLAttributes<HTMLInputElement>>(function TextInput(
  { className, ...rest },
  ref,
) {
  return <input ref={ref} className={cx(control, className)} {...rest} />;
});

export function Select({ className, children, ...rest }: SelectHTMLAttributes<HTMLSelectElement>) {
  return (
    <select className={cx(control, "pr-8", className)} {...rest}>
      {children}
    </select>
  );
}

export interface FieldProps {
  id: string;
  label: string;
  hint?: ReactNode;
  error?: string | null;
  children: ReactNode;
}

/** A labeled control with optional helper text and an inline error below it. */
export function Field({ id, label, hint, error, children }: FieldProps) {
  return (
    <div className="flex flex-col gap-1.5">
      <label htmlFor={id} className="text-[13px] font-medium text-fg">
        {label}
      </label>
      {children}
      {hint ? (
        <p id={`${id}-hint`} className="text-xs text-fg-muted">
          {hint}
        </p>
      ) : null}
      {error ? (
        <p id={`${id}-error`} role="alert" className="text-xs text-danger-fg">
          {error}
        </p>
      ) : null}
    </div>
  );
}

/** Joins the hint and error ids for aria-describedby. */
export function describedBy(id: string, hint: boolean, error: boolean): string | undefined {
  const ids = [hint ? `${id}-hint` : "", error ? `${id}-error` : ""].filter(Boolean);
  return ids.length ? ids.join(" ") : undefined;
}

/** A bordered panel section. */
export function Panel({ title, actions, children, className }: { title?: ReactNode; actions?: ReactNode; children: ReactNode; className?: string }) {
  return (
    <section className={cx("rounded-lg border border-border-subtle bg-bg-subtle", className)}>
      {title || actions ? (
        <header className="flex min-h-12 flex-wrap items-center justify-between gap-2 border-b border-border-subtle px-4 py-2">
          {title ? <h2 className="text-sm font-semibold text-fg">{title}</h2> : <span />}
          {actions ? <div className="flex flex-wrap items-center gap-2">{actions}</div> : null}
        </header>
      ) : null}
      {children}
    </section>
  );
}

export function Banner({ tone, icon: Icon, children, action }: { tone: Tone; icon?: LucideIcon; children: ReactNode; action?: ReactNode }) {
  return (
    <div role={tone === "danger" ? "alert" : "status"} className={cx("flex flex-wrap items-center gap-3 rounded-md px-3 py-2 text-sm", tones[tone])}>
      {Icon ? <Icon aria-hidden className="size-4 shrink-0" /> : null}
      <div className="min-w-0 flex-1">{children}</div>
      {action}
    </div>
  );
}
