import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import { Combobox } from "./Combobox";
import { closedState, comboboxKey, groupRuns, orderOptions, visibleOptions } from "./comboboxKeys";
import type { ComboboxState } from "./comboboxKeys";

const ctx = { count: 3, total: 3, selected: -1 };
const open = (active: number): ComboboxState => ({ open: true, filtering: false, active });

describe("comboboxKey", () => {
  it("opens all options on ArrowDown at the current value, or at the first option", () => {
    expect(comboboxKey(closedState, "ArrowDown", false, ctx)).toEqual({ state: open(0), handled: true });
    expect(comboboxKey(closedState, "ArrowDown", false, { ...ctx, selected: 2 }).state).toEqual(open(2));
    expect(comboboxKey(closedState, "ArrowDown", true, ctx).state).toEqual(open(-1));
  });

  it("opens on ArrowUp at the last option", () => {
    expect(comboboxKey(closedState, "ArrowUp", false, ctx).state).toEqual(open(2));
  });

  it("moves the highlight with wrap", () => {
    expect(comboboxKey(open(0), "ArrowDown", false, ctx).state.active).toBe(1);
    expect(comboboxKey(open(2), "ArrowDown", false, ctx).state.active).toBe(0);
    expect(comboboxKey(open(-1), "ArrowDown", false, ctx).state.active).toBe(0);
    expect(comboboxKey(open(0), "ArrowUp", false, ctx).state.active).toBe(2);
    expect(comboboxKey(open(-1), "ArrowUp", false, ctx).state.active).toBe(2);
  });

  it("accepts the highlighted option on Enter and stops the form submit", () => {
    expect(comboboxKey(open(1), "Enter", false, ctx)).toEqual({ state: closedState, pick: 1, handled: true });
  });

  it("lets Enter submit the form when no option is highlighted", () => {
    expect(comboboxKey(open(-1), "Enter", false, ctx)).toEqual({ state: closedState, handled: false });
    expect(comboboxKey(closedState, "Enter", false, ctx).handled).toBe(false);
  });

  it("closes on Escape and keeps the dialog open; a closed list lets Escape through", () => {
    expect(comboboxKey(open(1), "Escape", false, ctx)).toEqual({ state: closedState, handled: true });
    expect(comboboxKey(closedState, "Escape", false, ctx).handled).toBe(false);
  });

  it("accepts the highlighted option on Tab and lets focus move", () => {
    expect(comboboxKey(open(2), "Tab", false, ctx)).toEqual({ state: closedState, pick: 2, handled: false });
    expect(comboboxKey(open(-1), "Tab", false, ctx)).toEqual({ state: closedState, handled: false });
  });

  it("ignores other keys", () => {
    const state = open(1);
    expect(comboboxKey(state, "a", false, ctx)).toEqual({ state, handled: false });
  });
});

describe("visibleOptions", () => {
  const options = [{ value: "/workspace" }, { value: "/workspace/app" }, { value: "/home/agent" }];

  it("shows all options when the list opens, also when the field has a value", () => {
    expect(visibleOptions(options, "/workspace/app", false)).toHaveLength(3);
  });

  it("filters by the typed text in the value or the label", () => {
    expect(visibleOptions(options, "APP", true).map((o) => o.value)).toEqual(["/workspace/app"]);
    expect(visibleOptions([{ value: "codex/gpt-5", label: "GPT 5" }], "gpt 5", true)).toHaveLength(1);
    expect(visibleOptions(options, " ", true)).toHaveLength(3);
  });
});

describe("orderOptions and groupRuns", () => {
  it("puts each group together and keeps flat indexes", () => {
    const ordered = orderOptions([
      { value: "a/1", group: "a" },
      { value: "b/1", group: "b" },
      { value: "a/2", group: "a" },
    ]);
    expect(ordered.map((o) => o.value)).toEqual(["a/1", "a/2", "b/1"]);
    const runs = groupRuns(ordered);
    expect(runs.map((r) => [r.group, r.items.map((i) => i.index)])).toEqual([
      ["a", [0, 1]],
      ["b", [2]],
    ]);
  });
});

describe("Combobox", () => {
  it("renders the WAI-ARIA combobox roles, closed", () => {
    const html = renderToStaticMarkup(
      createElement(Combobox, { id: "f", value: "/workspace", onChange: () => undefined, options: [{ value: "/workspace" }], label: "Folder" }),
    );
    expect(html).toContain('role="combobox"');
    expect(html).toContain('aria-expanded="false"');
    expect(html).toContain('aria-autocomplete="list"');
    expect(html).toMatch(/role="listbox"[^>]*aria-label="Folder"/);
    expect(html).toContain('popover="manual"');
    expect(html).not.toContain("aria-activedescendant");
    expect(html).not.toContain("<datalist");
  });
});
