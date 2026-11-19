import type { LucideIcon } from "lucide-react";
import { cx } from "../ui";
import type { Health } from "./health";
import { healthInfo } from "./health";

/** One or two letters for a name: "Google Drive" → "GD", "openai" → "O". */
export function initials(name: string): string {
  const words = name
    .replace(/[^\p{L}\p{N}]+/gu, " ")
    .trim()
    .split(" ")
    .filter(Boolean);
  if (words.length === 0) {
    return "?";
  }
  if (words.length === 1) {
    return words[0].charAt(0).toUpperCase();
  }
  return (words[0].charAt(0) + words[1].charAt(0)).toUpperCase();
}

const markSizes = { sm: "size-6 text-[11px]", md: "size-8 text-xs", lg: "size-10 text-sm" };

/** A square tile with an icon or the initials of a provider, an app, or a connector. */
export function Mark({ name, icon: Icon, size = "md", className }: { name: string; icon?: LucideIcon; size?: keyof typeof markSizes; className?: string }) {
  return (
    <span
      aria-hidden
      className={cx(
        "rounded-field inline-flex shrink-0 items-center justify-center border border-base-content/10 bg-neutral font-semibold text-neutral-content",
        markSizes[size],
        className,
      )}
    >
      {Icon ? <Icon className={size === "sm" ? "size-3.5" : size === "md" ? "size-4" : "size-5"} /> : initials(name)}
    </span>
  );
}

const dotTones: Record<Health, string> = {
  operational: "bg-success",
  pending: "bg-warning",
  outage: "bg-error",
  off: "bg-base-content/40",
  unknown: "bg-base-content/40",
};

/** A health icon and its label; the color is never the only signal. */
export function HealthLabel({ health, label, className }: { health: Health; label?: string; className?: string }) {
  const info = healthInfo[health];
  const Icon = info.icon;
  const text = {
    success: "text-success",
    warning: "text-warning",
    danger: "text-error",
    neutral: "text-base-content/70",
    accent: "text-info",
  }[info.tone];
  return (
    <span className={cx("inline-flex items-center gap-1.5 text-xs font-medium whitespace-nowrap", text, className)}>
      <Icon aria-hidden className={cx("size-3.5", health === "pending" && "motion-safe:animate-spin")} />
      {label ?? info.label}
    </span>
  );
}

/** A small round status light, with a halo that pulses while `live`. */
export function StatusDot({ health, live = false }: { health: Health; live?: boolean }) {
  return (
    <span aria-hidden className="relative inline-flex size-2 shrink-0">
      {live ? <span className={cx("absolute inset-0 rounded-full opacity-60 motion-safe:animate-ping", dotTones[health])} /> : null}
      <span className={cx("relative inline-flex size-2 rounded-full", dotTones[health])} />
    </span>
  );
}
