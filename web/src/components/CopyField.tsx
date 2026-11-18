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
      <span className="text-[13px] font-medium">{label}</span>
      <div className="join w-full min-w-0">
        <code
          className={cx(
            "join-item min-w-0 flex-1 overflow-x-auto border border-base-content/20 bg-base-300 px-3 py-2 text-[13px] whitespace-nowrap",
            !mono && "font-sans",
          )}
        >
          {value}
        </code>
        <button
          type="button"
          onClick={() => void copyText(value).then(setCopied)}
          aria-label={`Copy ${label.toLowerCase()}`}
          className="btn join-item h-auto border-base-content/20 bg-base-100 font-medium text-base-content/70 hover:bg-base-content/10 hover:text-base-content"
        >
          {copied ? <Check aria-hidden className="size-4 text-success" /> : <Copy aria-hidden className="size-4" />}
          <span aria-live="polite">{copied === null ? "Copy" : copied ? "Copied" : "Copy failed"}</span>
        </button>
      </div>
    </div>
  );
}
