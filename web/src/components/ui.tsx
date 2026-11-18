import type { LucideIcon } from "lucide-react";
import { forwardRef } from "react";
import type { ButtonHTMLAttributes, InputHTMLAttributes, ReactNode, SelectHTMLAttributes } from "react";

export function cx(...classes: (string | false | null | undefined)[]): string {
  return classes.filter(Boolean).join(" ");
}

type Variant = "primary" | "secondary" | "ghost" | "danger";
type Size = "sm" | "md";

/** daisyUI button classes for each variant; also used on links that look like buttons. */
export const buttonVariants: Record<Variant, string> = {
  primary: "btn-primary",
  secondary: "btn-neutral",
  ghost: "btn-ghost hover:bg-base-content/10",
  danger: "btn-error",
};

const buttonSizes: Record<Size, string> = { sm: "btn-sm text-[13px]", md: "" };

/** daisyUI classes of a button-styled element, for links and labels. */
export function buttonClass(variant: Variant = "secondary", size: Size = "md", className?: string): string {
  return cx("btn font-medium", buttonVariants[variant], buttonSizes[size], className);
}

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
      className={buttonClass(variant, size, cx("whitespace-nowrap", className))}
      {...rest}
    >
      {busy ? <Spinner className="" /> : Icon ? <Icon aria-hidden className="size-4" /> : null}
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
        "btn btn-ghost btn-square text-base-content/70 hover:bg-base-content/10",
        tone === "danger" ? "hover:text-error" : "hover:text-base-content",
        buttonSizes[size],
        className,
      )}
      {...rest}
    >
      <Icon aria-hidden className="size-4" />
    </button>
  );
}

const spinnerSizes = { xs: "loading-xs", sm: "loading-sm" };

/** A daisyUI spinner. Muted by default; `className` replaces the color (pass "" for the current color). */
export function Spinner({ className, size = "sm" }: { className?: string; size?: keyof typeof spinnerSizes }) {
  return <span aria-hidden className={cx("loading loading-spinner", spinnerSizes[size], className ?? "text-base-content/70")} />;
}

export type Tone = "neutral" | "accent" | "success" | "warning" | "danger";

/** daisyUI soft badge colors for each tone. */
export const badgeTones: Record<Tone, string> = {
  neutral: "badge-soft text-base-content/70",
  accent: "badge-soft badge-info",
  success: "badge-soft badge-success",
  warning: "badge-soft badge-warning",
  danger: "badge-soft badge-error",
};

const alertTones: Record<Tone, string> = {
  neutral: "",
  accent: "alert-info",
  success: "alert-success",
  warning: "alert-warning",
  danger: "alert-error",
};

export function Badge({ tone = "neutral", icon: Icon, children, className }: { tone?: Tone; icon?: LucideIcon; children: ReactNode; className?: string }) {
  return (
    <span className={cx("badge badge-sm gap-1 px-1.5 font-medium whitespace-nowrap", badgeTones[tone], className)}>
      {Icon ? <Icon aria-hidden className="size-3" /> : null}
      {children}
    </span>
  );
}

/*
 * daisyUI draws the field border at 20% of the text color (1.8:1). The
 * border here is 45% (4.0:1 on base-100), so the field edge stays visible.
 */
const field = "w-full min-w-0 border-base-content/45 aria-invalid:border-error";

export const TextInput = forwardRef<HTMLInputElement, InputHTMLAttributes<HTMLInputElement>>(function TextInput(
  { className, ...rest },
  ref,
) {
  return <input ref={ref} className={cx("input", field, className)} {...rest} />;
});

export function Select({ className, children, ...rest }: SelectHTMLAttributes<HTMLSelectElement>) {
  return (
    <select className={cx("select", field, className)} {...rest}>
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
      <label htmlFor={id} className="text-[13px] font-medium">
        {label}
      </label>
      {children}
      {hint ? (
        <p id={`${id}-hint`} className="text-xs text-base-content/70">
          {hint}
        </p>
      ) : null}
      {error ? (
        <p id={`${id}-error`} role="alert" className="text-xs text-error">
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

/** A bordered card that holds one section of a page. */
export function Panel({ title, actions, children, className }: { title?: ReactNode; actions?: ReactNode; children: ReactNode; className?: string }) {
  return (
    <section className={cx("card border border-base-content/10 bg-base-200", className)}>
      {title || actions ? (
        <header className="flex min-h-12 flex-wrap items-center justify-between gap-2 border-b border-base-content/10 px-4 py-2">
          {title ? <h2 className="text-sm font-semibold">{title}</h2> : <span />}
          {actions ? <div className="flex flex-wrap items-center gap-2">{actions}</div> : null}
        </header>
      ) : null}
      {children}
    </section>
  );
}

/** A daisyUI soft alert in the page flow; danger is announced at once. */
export function Banner({ tone, icon: Icon, children, action }: { tone: Tone; icon?: LucideIcon; children: ReactNode; action?: ReactNode }) {
  return (
    <div
      role={tone === "danger" ? "alert" : "status"}
      className={cx("alert alert-soft flex flex-wrap items-center gap-3 px-3 py-2 text-sm", alertTones[tone])}
    >
      {Icon ? <Icon aria-hidden className="size-4 shrink-0" /> : null}
      <div className="min-w-0 flex-1">{children}</div>
      {action}
    </div>
  );
}
