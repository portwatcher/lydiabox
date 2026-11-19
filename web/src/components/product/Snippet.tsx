import { Check, Copy } from "lucide-react";
import { useEffect, useState } from "react";
import type { ReactNode } from "react";
import { cx } from "../ui";

async function copyText(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    return false;
  }
}

/** Copy state that resets after two seconds: null (idle), true (copied), or false (failed). */
export function useCopy(): [boolean | null, (text: string) => void] {
  const [copied, setCopied] = useState<boolean | null>(null);
  useEffect(() => {
    if (copied === null) {
      return;
    }
    const id = setTimeout(() => setCopied(null), 2000);
    return () => clearTimeout(id);
  }, [copied]);
  return [copied, (text: string) => void copyText(text).then(setCopied)];
}

/** An icon button that copies `value`; it names what it copies. */
export function CopyButton({ value, label, className }: { value: string; label: string; className?: string }) {
  const [copied, copy] = useCopy();
  const name = copied === null ? `Copy ${label}` : copied ? "Copied" : "Copy failed";
  return (
    <button
      type="button"
      aria-label={name}
      title={name}
      onClick={() => copy(value)}
      className={cx("btn btn-ghost btn-square btn-sm text-base-content/70 hover:bg-base-content/10 hover:text-base-content", className)}
    >
      {copied ? <Check aria-hidden className="size-4 text-success" /> : <Copy aria-hidden className="size-4" />}
      <span className="sr-only" aria-live="polite">
        {copied === null ? "" : name}
      </span>
    </button>
  );
}

/**
 * A shell command block with a copy button. Lines that start with `#` are
 * comments and are muted; the copy takes the commands without them.
 */
export function CommandBlock({ code, label = "command" }: { code: string; label?: string }) {
  const lines = code.split("\n");
  const commands = lines.filter((l) => !l.trimStart().startsWith("#")).join("\n").trim();
  return (
    <div className="rounded-field relative flex min-w-0 items-start border border-base-content/10 bg-base-300">
      <pre className="min-w-0 flex-1 overflow-x-auto py-2.5 ps-3 pe-1 text-[13px] leading-6">
        <code>
          {lines.map((line, i) => (
            <span key={i} className={cx("block", line.trimStart().startsWith("#") ? "text-base-content/60" : "")}>
              {line.trimStart().startsWith("#") ? (
                line
              ) : (
                <>
                  <span aria-hidden className="text-base-content/50 select-none">
                    ${" "}
                  </span>
                  {line}
                </>
              )}
            </span>
          ))}
        </code>
      </pre>
      <CopyButton value={commands} label={label} className="m-1" />
    </div>
  );
}

export interface SnippetStep {
  title: string;
  /** Done steps show a check in place of their number. */
  done?: boolean;
  children: ReactNode;
}

/** Numbered setup steps on a vertical rail; a finished step shows a check. */
export function SnippetSteps({ steps }: { steps: SnippetStep[] }) {
  return (
    <ol className="flex flex-col">
      {steps.map((step, i) => (
        <li key={step.title} className="relative flex gap-3 pb-5 last:pb-0">
          {i < steps.length - 1 ? <span aria-hidden className="absolute top-7 bottom-1 left-3 w-px bg-base-content/15" /> : null}
          <span
            className={cx(
              "z-[1] inline-flex size-6 shrink-0 items-center justify-center rounded-full border text-xs font-semibold tabular-nums",
              step.done ? "border-success/40 bg-success/15 text-success" : "border-base-content/20 bg-base-100 text-base-content/70",
            )}
          >
            {step.done ? <Check aria-label="Done" className="size-3.5" /> : i + 1}
          </span>
          <div className="flex min-w-0 flex-1 flex-col gap-2 pt-0.5">
            <h3 className="text-[13px] font-semibold">{step.title}</h3>
            {step.children}
          </div>
        </li>
      ))}
    </ol>
  );
}
