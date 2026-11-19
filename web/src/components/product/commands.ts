import type { LucideIcon } from "lucide-react";

export interface Command {
  id: string;
  group: string;
  label: string;
  /** Muted text after the label, for example a session's harness. */
  meta?: string;
  /** More words that find the command. */
  keywords?: string;
  icon?: LucideIcon;
  run: () => void;
}

/**
 * The commands that match every word of `query`, best first: a label that
 * starts with the query, then a word of the label that starts with it, then
 * any match. Ties keep their order. An empty query keeps every command.
 */
export function filterCommands(commands: Command[], query: string): Command[] {
  const q = query.trim().toLowerCase();
  if (!q) {
    return commands;
  }
  const words = q.split(/\s+/);
  const scored: { c: Command; score: number; i: number }[] = [];
  commands.forEach((c, i) => {
    const label = c.label.toLowerCase();
    const hay = `${label} ${c.meta ?? ""} ${c.keywords ?? ""} ${c.group}`.toLowerCase();
    if (!words.every((w) => hay.includes(w))) {
      return;
    }
    let score = 0;
    if (label.startsWith(q)) {
      score = 3;
    } else if (label.split(/[\s/._-]+/).some((part) => part.startsWith(words[0]))) {
      score = 2;
    } else if (label.includes(words[0])) {
      score = 1;
    }
    scored.push({ c, score, i });
  });
  return scored.sort((a, b) => b.score - a.score || a.i - b.i).map((s) => s.c);
}

/** Commands by group, in the order the groups first appear. */
export function groupCommands(commands: Command[]): { group: string; items: Command[] }[] {
  const out: { group: string; items: Command[] }[] = [];
  for (const c of commands) {
    const g = out.find((x) => x.group === c.group);
    if (g) {
      g.items.push(c);
    } else {
      out.push({ group: c.group, items: [c] });
    }
  }
  return out;
}

/** The next active index for an arrow key, wrapping at both ends. */
export function moveActive(index: number, length: number, key: string): number {
  if (length === 0) {
    return -1;
  }
  switch (key) {
    case "ArrowDown":
      return (index + 1) % length;
    case "ArrowUp":
      return index <= 0 ? length - 1 : index - 1;
    case "Home":
      return 0;
    case "End":
      return length - 1;
    default:
      return index;
  }
}

/**
 * Whether a keydown opens the palette: Cmd+K on macOS, Ctrl+K elsewhere,
 * but not inside a terminal (Ctrl+K edits the shell line) or the remote
 * desktop while it holds the keyboard.
 */
export function isPaletteKey(event: { key: string; metaKey: boolean; ctrlKey: boolean; altKey: boolean; shiftKey: boolean; defaultPrevented: boolean }, target: { closest?: (selector: string) => unknown } | null, mac: boolean): boolean {
  if (event.defaultPrevented || event.altKey || event.shiftKey || event.key.toLowerCase() !== "k") {
    return false;
  }
  if (mac ? !event.metaKey || event.ctrlKey : !event.ctrlKey || event.metaKey) {
    return false;
  }
  return !target?.closest?.('.xterm, [role="application"]');
}
