import { AppWindow, Bot, FilePen, FileText, Globe, ListTodo, Monitor, Plug, Search, SquareTerminal, Wrench } from "lucide-react";
import type { LucideIcon } from "lucide-react";
import type { ToolKind } from "../../api/events";

/** The icon and the verb of each tool kind. The verb leads the row of a tool call. */
export const toolKinds: Record<ToolKind, { icon: LucideIcon; verb: string }> = {
  shell: { icon: SquareTerminal, verb: "Ran" },
  read: { icon: FileText, verb: "Read" },
  edit: { icon: FilePen, verb: "Edited" },
  search: { icon: Search, verb: "Searched" },
  web: { icon: Globe, verb: "Fetched" },
  mcp: { icon: Plug, verb: "Called" },
  computer: { icon: Monitor, verb: "Used the desktop" },
  browser: { icon: AppWindow, verb: "Used the browser" },
  plan: { icon: ListTodo, verb: "Updated the plan" },
  agent: { icon: Bot, verb: "Delegated" },
  other: { icon: Wrench, verb: "Called" },
};

/** "mcp__github__create_issue" → "github create_issue"; other names stay. */
export function toolLabel(name: string): string {
  const parts = name.split("__").filter(Boolean);
  if (parts.length >= 2 && parts[0].toLowerCase() === "mcp") {
    return parts.slice(1).join(" ");
  }
  return parts.join(" ");
}

/** 850 → "850ms", 12_400 → "12.4s", 95_000 → "1m 35s". */
export function formatDuration(ms: number): string {
  if (ms <= 0) {
    return "";
  }
  if (ms < 1000) {
    return `${Math.round(ms)}ms`;
  }
  const s = ms / 1000;
  if (s < 60) {
    return `${s < 10 ? s.toFixed(1) : Math.round(s)}s`;
  }
  const m = Math.floor(s / 60);
  return `${m}m ${Math.round(s - m * 60)}s`;
}

/** Counts the added and removed lines of a diff body. */
export function diffCounts(diff: string): { added: number; removed: number } {
  let added = 0;
  let removed = 0;
  for (const line of diff.split("\n")) {
    if (line.startsWith("+") && !line.startsWith("+++")) {
      added++;
    } else if (line.startsWith("-") && !line.startsWith("---")) {
      removed++;
    }
  }
  return { added, removed };
}
