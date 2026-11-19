/** One choice of a combobox. `label` is the visible text (the value when absent). */
export interface ComboboxOption {
  value: string;
  label?: string;
  /** Options with the same group show under one heading. */
  group?: string;
}

/**
 * The list state. `filtering` is false when the list opens from a click or
 * an arrow key, so it shows all options and not only the ones that match
 * the current value; typing sets it.
 */
export interface ComboboxState {
  open: boolean;
  filtering: boolean;
  /** Index of the highlighted option in the shown list, or -1. */
  active: number;
}

export const closedState: ComboboxState = { open: false, filtering: false, active: -1 };

/** Puts the options of each group together, in the order the groups first occur. */
export function orderOptions(options: ComboboxOption[]): ComboboxOption[] {
  const groups = new Map<string, ComboboxOption[]>();
  for (const option of options) {
    const key = option.group ?? "";
    const list = groups.get(key);
    if (list) {
      list.push(option);
    } else {
      groups.set(key, [option]);
    }
  }
  return [...groups.values()].flat();
}

/** The options to show: all of them, or the ones that contain the typed text. */
export function visibleOptions(options: ComboboxOption[], query: string, filtering: boolean): ComboboxOption[] {
  const q = query.trim().toLowerCase();
  if (!filtering || !q) {
    return options;
  }
  return options.filter((o) => o.value.toLowerCase().includes(q) || (o.label ?? "").toLowerCase().includes(q));
}

export interface OptionGroup {
  group: string | undefined;
  items: { option: ComboboxOption; index: number }[];
}

/** Splits an ordered list into runs of one group, keeping each option's index. */
export function groupRuns(options: ComboboxOption[]): OptionGroup[] {
  const runs: OptionGroup[] = [];
  options.forEach((option, index) => {
    const last = runs[runs.length - 1];
    if (last && last.group === option.group) {
      last.items.push({ option, index });
    } else {
      runs.push({ group: option.group, items: [{ option, index }] });
    }
  });
  return runs;
}

export interface KeyContext {
  /** Number of options shown now (when the list is open). */
  count: number;
  /** Number of all options, shown when the list opens. */
  total: number;
  /** Index of the current value in all options, or -1. */
  selected: number;
}

export interface KeyResult {
  state: ComboboxState;
  /** Index of the option to accept, in the shown list. */
  pick?: number;
  /** True when the key must not do its default action (submit, close the dialog). */
  handled: boolean;
}

/**
 * The WAI-ARIA combobox key map: ArrowDown and ArrowUp open the list or
 * move the highlight (with wrap); Enter accepts the highlighted option;
 * Escape closes the list; Tab accepts the highlighted option and moves on.
 */
export function comboboxKey(state: ComboboxState, key: string, altKey: boolean, ctx: KeyContext): KeyResult {
  const opened = (active: number): KeyResult => ({ state: { open: true, filtering: false, active }, handled: true });
  switch (key) {
    case "ArrowDown":
      if (!state.open) {
        if (altKey) {
          return opened(ctx.selected);
        }
        return opened(ctx.selected >= 0 ? ctx.selected : ctx.total > 0 ? 0 : -1);
      }
      if (ctx.count === 0) {
        return { state, handled: true };
      }
      return { state: { ...state, active: (state.active + 1) % ctx.count }, handled: true };
    case "ArrowUp":
      if (!state.open) {
        return opened(ctx.selected >= 0 ? ctx.selected : ctx.total - 1);
      }
      if (ctx.count === 0) {
        return { state, handled: true };
      }
      return { state: { ...state, active: state.active <= 0 ? ctx.count - 1 : state.active - 1 }, handled: true };
    case "Enter":
      if (state.open && state.active >= 0 && state.active < ctx.count) {
        return { state: closedState, pick: state.active, handled: true };
      }
      return { state: closedState, handled: false };
    case "Escape":
      if (state.open) {
        return { state: closedState, handled: true };
      }
      return { state, handled: false };
    case "Tab":
      if (state.open && state.active >= 0 && state.active < ctx.count) {
        return { state: closedState, pick: state.active, handled: false };
      }
      return { state: closedState, handled: false };
    default:
      return { state, handled: false };
  }
}
