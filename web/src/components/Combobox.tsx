import { Check, ChevronDown } from "lucide-react";
import { Fragment, useEffect, useId, useLayoutEffect, useMemo, useRef, useState } from "react";
import type { KeyboardEvent } from "react";
import { closedState, comboboxKey, groupRuns, orderOptions, visibleOptions } from "./comboboxKeys";
import type { ComboboxOption, ComboboxState } from "./comboboxKeys";
import { cx, fieldClass } from "./ui";

// @types/react 18 does not declare the popover attribute; React 18 passes it to the DOM.
declare module "react" {
  // The type parameter must match the declaration that this merges into.
  // eslint-disable-next-line @typescript-eslint/no-unused-vars
  interface HTMLAttributes<T> {
    popover?: "" | "auto" | "manual";
  }
}

export interface ComboboxProps {
  id: string;
  value: string;
  onChange: (value: string) => void;
  options: ComboboxOption[];
  /** Accessible name of the list and the toggle button; the visible label of the field. */
  label: string;
  invalid?: boolean;
  describedBy?: string;
}

/** Places the list below the field, or above it when there is more room there. */
function place(list: HTMLElement, anchor: HTMLElement) {
  const r = anchor.getBoundingClientRect();
  const gap = 4;
  const margin = 8;
  const below = window.innerHeight - r.bottom - gap - margin;
  const above = r.top - gap - margin;
  const up = below < 160 && above > below;
  list.style.left = `${r.left}px`;
  list.style.width = `${r.width}px`;
  list.style.maxHeight = `${Math.max(96, Math.min(288, up ? above : below))}px`;
  list.style.top = up ? "auto" : `${r.bottom + gap}px`;
  list.style.bottom = up ? `${window.innerHeight - r.top + gap}px` : "auto";
}

/** Scrolls the list so that the option is visible, without scrolling the page. */
function reveal(list: HTMLElement, option: HTMLElement | null) {
  if (!option) {
    return;
  }
  const top = option.offsetTop;
  const bottom = top + option.offsetHeight;
  if (top < list.scrollTop) {
    list.scrollTop = top;
  } else if (bottom > list.scrollTop + list.clientHeight) {
    list.scrollTop = bottom - list.clientHeight;
  }
}

/**
 * A text field with a list of choices (the WAI-ARIA combobox with list
 * autocomplete). The list opens with all choices on a click or ArrowDown
 * and filters as the user types; a typed value that is not in the list is
 * kept. The list is a popover in the top layer, so a dialog body does not
 * clip it.
 */
export function Combobox({ id, value, onChange, options, label, invalid, describedBy }: ComboboxProps) {
  const listId = useId();
  const outerRef = useRef<HTMLDivElement>(null);
  const anchorRef = useRef<HTMLDivElement>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  const listRef = useRef<HTMLDivElement>(null);
  const [state, setState] = useState<ComboboxState>(closedState);

  const all = useMemo(() => orderOptions(options), [options]);
  const shown = useMemo(() => (state.open ? visibleOptions(all, value, state.filtering) : []), [all, value, state]);
  const expanded = state.open && shown.length > 0;
  const optionId = (index: number) => `${listId}-o${index}`;

  const close = () => setState(closedState);
  const openAll = () => setState({ open: true, filtering: false, active: all.findIndex((o) => o.value === value) });
  const pick = (index: number) => {
    const option = shown[index];
    if (option) {
      onChange(option.value);
    }
    close();
  };

  useLayoutEffect(() => {
    const list = listRef.current;
    const anchor = anchorRef.current;
    if (!list || !anchor || typeof list.showPopover !== "function") {
      return;
    }
    if (!expanded) {
      if (list.matches(":popover-open")) {
        list.hidePopover();
      }
      return;
    }
    const follow = () => place(list, anchor);
    follow();
    if (!list.matches(":popover-open")) {
      list.showPopover();
    }
    window.addEventListener("resize", follow);
    window.addEventListener("scroll", follow, true);
    return () => {
      window.removeEventListener("resize", follow);
      window.removeEventListener("scroll", follow, true);
    };
  }, [expanded]);

  useEffect(() => {
    if (!expanded) {
      return;
    }
    const outside = (event: PointerEvent) => {
      const target = event.target;
      if (target instanceof Node && !outerRef.current?.contains(target)) {
        setState(closedState);
      }
    };
    document.addEventListener("pointerdown", outside, true);
    return () => document.removeEventListener("pointerdown", outside, true);
  }, [expanded]);

  useEffect(() => {
    const list = listRef.current;
    if (list && expanded && state.active >= 0) {
      reveal(list, document.getElementById(optionId(state.active)));
    }
    // optionId only depends on listId, which is stable.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [expanded, state.active]);

  const onKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    const result = comboboxKey({ ...state, open: expanded }, event.key, event.altKey, {
      count: shown.length,
      total: all.length,
      selected: all.findIndex((o) => o.value === value),
    });
    if (result.handled) {
      event.preventDefault();
      event.stopPropagation();
    }
    if (result.pick !== undefined) {
      pick(result.pick);
    } else if (result.state !== state) {
      setState(result.state);
    }
  };

  return (
    <div ref={outerRef} className="w-full min-w-0">
      <div ref={anchorRef} className={cx("input pe-1", fieldClass, invalid && "border-error")}>
        <input
          ref={inputRef}
          id={id}
          role="combobox"
          className="min-w-0 grow"
          value={value}
          onChange={(event) => {
            onChange(event.target.value);
            setState({ open: true, filtering: true, active: -1 });
          }}
          onClick={() => {
            if (!state.open) {
              openAll();
            }
          }}
          onKeyDown={onKeyDown}
          onBlur={(event) => {
            if (!(event.relatedTarget instanceof Node && outerRef.current?.contains(event.relatedTarget))) {
              close();
            }
          }}
          aria-expanded={expanded}
          aria-controls={listId}
          aria-autocomplete="list"
          aria-activedescendant={expanded && state.active >= 0 ? optionId(state.active) : undefined}
          aria-invalid={invalid ? true : undefined}
          aria-describedby={describedBy}
          autoComplete="off"
          spellCheck={false}
        />
        <button
          type="button"
          tabIndex={-1}
          aria-label={label}
          aria-expanded={expanded}
          aria-controls={listId}
          className="btn btn-ghost btn-square btn-xs text-base-content/70 hover:bg-base-content/10"
          onMouseDown={(event) => event.preventDefault()}
          onClick={() => {
            if (expanded) {
              close();
            } else {
              openAll();
            }
            inputRef.current?.focus();
          }}
        >
          <ChevronDown aria-hidden className={cx("size-4 transition-transform", expanded && "rotate-180")} />
        </button>
      </div>
      <div
        ref={listRef}
        id={listId}
        role="listbox"
        aria-label={label}
        popover="manual"
        className="fixed inset-auto m-0 overflow-y-auto rounded-box border border-base-content/20 bg-base-100 p-1 text-base-content shadow-lg"
      >
        {groupRuns(shown).map((run) => {
          const items = run.items.map(({ option, index }) => (
            <div
              key={option.value}
              id={optionId(index)}
              role="option"
              aria-selected={index === state.active}
              className={cx(
                "flex cursor-pointer items-center gap-2 rounded-field px-2 py-1.5 text-sm",
                index === state.active && "bg-base-content/10",
              )}
              onMouseDown={(event) => event.preventDefault()}
              onPointerMove={() => {
                if (state.active !== index) {
                  setState({ ...state, active: index });
                }
              }}
              onClick={() => pick(index)}
            >
              <span className="min-w-0 flex-1 truncate">{option.label ?? option.value}</span>
              {option.value === value ? <Check aria-hidden className="size-4 shrink-0" /> : null}
            </div>
          ));
          if (run.group === undefined) {
            return <Fragment key="g-none">{items}</Fragment>;
          }
          const headingId = `${listId}-g${run.items[0]?.index ?? 0}`;
          return (
            <div key={`g-${run.group}`} role="group" aria-labelledby={headingId}>
              <div id={headingId} role="presentation" className="px-2 pt-2 pb-1 text-xs font-medium text-base-content/70">
                {run.group}
              </div>
              {items}
            </div>
          );
        })}
      </div>
    </div>
  );
}
