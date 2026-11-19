import { CornerDownLeft, Search } from "lucide-react";
import { useEffect, useId, useMemo, useRef, useState } from "react";
import { cx } from "../ui";
import { filterCommands, groupCommands, moveActive } from "./commands";
import type { Command } from "./commands";

interface CommandPaletteProps {
  open: boolean;
  onClose: () => void;
  commands: Command[];
  /** Shown while the command list is still loading more items. */
  loading?: boolean;
}

/**
 * A command palette on the native <dialog> (focus trap, Escape, focus
 * return). The input is a combobox that owns a listbox: the arrow keys move
 * the active option and Enter runs it.
 */
export function CommandPalette({ open, onClose, commands, loading = false }: CommandPaletteProps) {
  const ref = useRef<HTMLDialogElement>(null);
  const listRef = useRef<HTMLDivElement>(null);
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(0);
  const id = useId();
  const results = useMemo(() => filterCommands(commands, query), [commands, query]);
  const groups = useMemo(() => groupCommands(results), [results]);

  useEffect(() => {
    const dialog = ref.current;
    if (!dialog) {
      return;
    }
    if (open && !dialog.open) {
      setQuery("");
      setActive(0);
      dialog.showModal();
    } else if (!open && dialog.open) {
      dialog.close();
    }
  }, [open]);

  useEffect(() => {
    setActive(0);
  }, [query]);

  useEffect(() => {
    listRef.current?.querySelector(`[data-index="${active}"]`)?.scrollIntoView({ block: "nearest" });
  }, [active]);

  const run = (c: Command | undefined) => {
    if (!c) {
      return;
    }
    onClose();
    c.run();
  };

  let index = -1;
  return (
    <dialog
      ref={ref}
      aria-label="Command palette"
      onCancel={(e) => {
        e.preventDefault();
        onClose();
      }}
      onClick={(e) => {
        if (e.target === ref.current) {
          onClose();
        }
      }}
      className="modal modal-top items-start open:bg-base-300/70 sm:pt-[12vh]"
    >
      {open ? (
        <div className="modal-box mx-auto flex max-h-[min(32rem,80dvh)] w-full max-w-xl flex-col gap-0 overflow-hidden border border-base-content/15 bg-base-100 p-0 shadow-2xl sm:rounded-box">
          <label className="flex items-center gap-2 border-b border-base-content/10 px-4">
            <Search aria-hidden className="size-4 shrink-0 text-base-content/70" />
            <input
              autoFocus
              role="combobox"
              aria-expanded="true"
              aria-controls={`${id}-list`}
              aria-activedescendant={results[active] ? `${id}-${active}` : undefined}
              aria-autocomplete="list"
              aria-label="Search pages, sessions, and actions"
              placeholder="Search pages, sessions, and actions"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              onKeyDown={(e) => {
                if (["ArrowDown", "ArrowUp"].includes(e.key) || ((e.key === "Home" || e.key === "End") && e.ctrlKey)) {
                  e.preventDefault();
                  setActive((i) => moveActive(i, results.length, e.key));
                } else if (e.key === "Enter") {
                  e.preventDefault();
                  run(results[active]);
                }
              }}
              className="h-12 min-w-0 flex-1 bg-transparent text-sm outline-none placeholder:text-base-content/50"
            />
            <kbd className="kbd kbd-sm hidden sm:inline-flex">Esc</kbd>
          </label>
          <div ref={listRef} id={`${id}-list`} role="listbox" aria-label="Results" className="min-h-0 flex-1 overflow-y-auto p-1.5">
            {groups.map((g) => (
              <div key={g.group} role="group" aria-labelledby={`${id}-g-${g.group}`} className="pb-1">
                <div id={`${id}-g-${g.group}`} className="px-2.5 pt-2 pb-1 text-xs font-medium text-base-content/60">
                  {g.group}
                </div>
                {g.items.map((c) => {
                  index++;
                  const i = index;
                  const Icon = c.icon;
                  return (
                    <div
                      key={c.id}
                      id={`${id}-${i}`}
                      data-index={i}
                      role="option"
                      aria-selected={i === active}
                      onPointerMove={() => setActive(i)}
                      onClick={() => run(c)}
                      className={cx(
                        "flex h-10 cursor-pointer items-center gap-3 rounded-field px-2.5 text-[13px]",
                        i === active ? "bg-base-content/10" : "",
                      )}
                    >
                      {Icon ? <Icon aria-hidden className="size-4 shrink-0 text-base-content/70" /> : null}
                      <span className="min-w-0 truncate">{c.label}</span>
                      {c.meta ? <span className="min-w-0 truncate text-xs text-base-content/60">{c.meta}</span> : null}
                      {i === active ? <CornerDownLeft aria-hidden className="ms-auto size-3.5 shrink-0 text-base-content/60" /> : null}
                    </div>
                  );
                })}
              </div>
            ))}
            {results.length === 0 ? <p className="px-3 py-8 text-center text-sm text-base-content/70">{loading ? "Loading…" : "No match"}</p> : null}
          </div>
          <div className="hidden items-center gap-3 border-t border-base-content/10 px-4 py-2 text-xs text-base-content/60 sm:flex">
            <span className="inline-flex items-center gap-1">
              <kbd className="kbd kbd-xs">↑</kbd>
              <kbd className="kbd kbd-xs">↓</kbd> move
            </span>
            <span className="inline-flex items-center gap-1">
              <kbd className="kbd kbd-xs">↵</kbd> open
            </span>
          </div>
        </div>
      ) : null}
    </dialog>
  );
}
