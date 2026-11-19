import { Check, ChevronDown } from "lucide-react";
import type { LucideIcon } from "lucide-react";
import { useEffect, useRef } from "react";
import { Spinner, cx } from "../ui";

export interface MenuItem {
  id: string;
  label: string;
  checked?: boolean;
  busy?: boolean;
}

/**
 * A small daisyUI dropdown on <details>: the summary is a real button, so
 * Enter and Space open it; Escape and an outside click close it.
 */
export function MenuButton({ label, icon: Icon, items, onSelect, align = "end", className }: { label: string; icon?: LucideIcon; items: MenuItem[]; onSelect: (id: string) => void; align?: "start" | "end"; className?: string }) {
  const ref = useRef<HTMLDetailsElement>(null);

  useEffect(() => {
    const close = (event: Event) => {
      const el = ref.current;
      if (!el?.open) {
        return;
      }
      if (event instanceof KeyboardEvent) {
        if (event.key === "Escape") {
          el.open = false;
          el.querySelector("summary")?.focus();
        }
        return;
      }
      if (event.target instanceof Node && !el.contains(event.target)) {
        el.open = false;
      }
    };
    document.addEventListener("pointerdown", close);
    document.addEventListener("keydown", close);
    return () => {
      document.removeEventListener("pointerdown", close);
      document.removeEventListener("keydown", close);
    };
  }, []);

  return (
    <details ref={ref} className={cx("dropdown", align === "end" ? "dropdown-end" : "", className)}>
      <summary className="btn btn-ghost btn-sm gap-1 px-2 text-[13px] font-medium text-base-content/80 hover:bg-base-content/10 [&::-webkit-details-marker]:hidden">
        {Icon ? <Icon aria-hidden className="size-4" /> : null}
        {label}
        <ChevronDown aria-hidden className="size-3.5" />
      </summary>
      <ul className="menu dropdown-content z-20 mt-1 w-52 rounded-box border border-base-content/10 bg-base-100 p-1 shadow-lg">
        {items.map((item) => (
          <li key={item.id}>
            <button
              type="button"
              aria-pressed={item.checked ?? false}
              disabled={item.busy}
              onClick={() => {
                if (ref.current) {
                  ref.current.open = false;
                }
                onSelect(item.id);
              }}
              className="flex items-center gap-2 text-[13px]"
            >
              <span className="inline-flex size-4 items-center justify-center">
                {item.busy ? <Spinner size="xs" /> : item.checked ? <Check aria-hidden className="size-4 text-info" /> : null}
              </span>
              {item.label}
            </button>
          </li>
        ))}
      </ul>
    </details>
  );
}
