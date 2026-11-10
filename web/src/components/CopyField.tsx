import { Check, Copy } from "lucide-react";
import { useEffect, useState } from "react";
import { cx } from "./ui";

async function copyText(text: string): Promise<boolean> {
  try {
    await navigator.clipboard.writeText(text);
    return true;
  } catch {
    return false;
  }
}

/** A read-only value with a copy button that confirms with an icon and label change. */
export function CopyField({ label, value, mono = true }: { label: string; value: string; mono?: boolean }) {
  const [copied, setCopied] = useState<boolean | null>(null);

  useEffect(() => {
    if (copied === null) {
      return;
    }
    const id = setTimeout(() => setCopied(null), 2000);
    return () => clearTimeout(id);
  }, [copied]);

  return (
    <div className="flex flex-col gap-1.5">
      <span className="text-[13px] font-medium text-fg">{label}</span>
      <div className="flex min-w-0 items-stretch rounded-md border border-border bg-bg">
        <code className={cx("min-w-0 flex-1 overflow-x-auto px-3 py-2 text-[13px] whitespace-nowrap text-fg", !mono && "font-sans")}>
          {value}
        </code>
        <button
          type="button"
          onClick={() => void copyText(value).then(setCopied)}
          aria-label={`Copy ${label.toLowerCase()}`}
          className="flex shrink-0 items-center gap-1.5 border-l border-border px-3 text-[13px] text-fg-muted hover:bg-surface-hover hover:text-fg"
        >
          {copied ? <Check aria-hidden className="size-4 text-success-fg" /> : <Copy aria-hidden className="size-4" />}
          <span aria-live="polite">{copied === null ? "Copy" : copied ? "Copied" : "Copy failed"}</span>
        </button>
      </div>
    </div>
  );
}
