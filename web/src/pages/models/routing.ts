import type { RoutingGroup, RoutingStrategy } from "../../api/types";

// Editing state for routing groups (api.md section 10). Pure, so it is unit-tested.

export interface RoutingState {
  groups: RoutingGroup[];
  saved: RoutingGroup[];
}

export type RoutingAction =
  | { type: "load"; groups: RoutingGroup[] }
  | { type: "add" }
  | { type: "remove"; index: number }
  | { type: "rename"; index: number; name: string }
  | { type: "strategy"; index: number; strategy: RoutingStrategy }
  | { type: "addTarget"; index: number; target: string }
  | { type: "removeTarget"; index: number; target: number }
  | { type: "moveTarget"; index: number; from: number; to: number }
  | { type: "revert" };

export const emptyRouting: RoutingState = { groups: [], saved: [] };

function update(groups: RoutingGroup[], index: number, fn: (g: RoutingGroup) => RoutingGroup): RoutingGroup[] {
  return groups.map((g, i) => (i === index ? fn(g) : g));
}

function uniqueName(groups: RoutingGroup[]): string {
  const names = new Set(groups.map((g) => g.name));
  for (let n = 1; ; n += 1) {
    const name = n === 1 ? "group" : `group-${n}`;
    if (!names.has(name)) {
      return name;
    }
  }
}

export function routingReducer(state: RoutingState, action: RoutingAction): RoutingState {
  switch (action.type) {
    case "load":
      return { groups: action.groups, saved: action.groups };
    case "revert":
      return { ...state, groups: state.saved };
    case "add":
      return { ...state, groups: [...state.groups, { name: uniqueName(state.groups), strategy: "fallback", targets: [] }] };
    case "remove":
      return { ...state, groups: state.groups.filter((_, i) => i !== action.index) };
    case "rename":
      return { ...state, groups: update(state.groups, action.index, (g) => ({ ...g, name: action.name })) };
    case "strategy":
      return { ...state, groups: update(state.groups, action.index, (g) => ({ ...g, strategy: action.strategy })) };
    case "addTarget": {
      const target = action.target.trim();
      if (!target) {
        return state;
      }
      return {
        ...state,
        groups: update(state.groups, action.index, (g) =>
          g.targets.includes(target) ? g : { ...g, targets: [...g.targets, target] },
        ),
      };
    }
    case "removeTarget":
      return {
        ...state,
        groups: update(state.groups, action.index, (g) => ({ ...g, targets: g.targets.filter((_, i) => i !== action.target) })),
      };
    case "moveTarget":
      return {
        ...state,
        groups: update(state.groups, action.index, (g) => {
          if (action.to < 0 || action.to >= g.targets.length || action.from === action.to) {
            return g;
          }
          const targets = [...g.targets];
          const [moved] = targets.splice(action.from, 1);
          if (moved === undefined) {
            return g;
          }
          targets.splice(action.to, 0, moved);
          return { ...g, targets };
        }),
      };
  }
}

export function isDirty(state: RoutingState): boolean {
  return JSON.stringify(state.groups) !== JSON.stringify(state.saved);
}

/** Problems that block saving, one per group index. */
export function validateGroups(groups: RoutingGroup[]): Map<number, string> {
  const errors = new Map<number, string>();
  const seen = new Map<string, number>();
  groups.forEach((g, i) => {
    const name = g.name.trim();
    if (!name) {
      errors.set(i, "Enter a group name.");
    } else if (!/^[A-Za-z0-9._-]+$/.test(name)) {
      errors.set(i, "Use letters, numbers, dots, dashes, or underscores in the name.");
    } else if (seen.has(name)) {
      errors.set(i, `Choose another name; "${name}" is already used.`);
    } else if (g.targets.length === 0) {
      errors.set(i, "Add at least one model.");
    }
    seen.set(name, i);
  });
  return errors;
}
