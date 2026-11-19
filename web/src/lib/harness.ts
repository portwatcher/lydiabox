import type { HarnessID } from "../api/types";

export const harnessLabels: Record<HarnessID, string> = {
  "claude-code": "Claude Code",
  codex: "Codex",
  dsh: "dsh",
  antigravity: "Antigravity",
  opencode: "OpenCode",
  shell: "Shell",
};

export function harnessLabel(id: string): string {
  return id in harnessLabels ? harnessLabels[id as HarnessID] : id;
}

/**
 * Harnesses whose sessions run in the chat view through their ACP agent
 * (engine "acp"). The others run in a terminal; their chat view reads the
 * transcript and types into the terminal.
 */
export const acpHarnesses: readonly HarnessID[] = ["claude-code", "codex", "dsh", "antigravity", "opencode"];

/** The engine of a new or continued session of a harness in a view. */
export function engineFor(harness: HarnessID, view: "chat" | "terminal"): "acp" | "pty" {
  return view === "chat" && acpHarnesses.includes(harness) ? "acp" : "pty";
}

/** Short display name for a session without a title. */
export function sessionName(session: { title: string; nativeId: string; harness: string }): string {
  if (session.title.trim()) {
    return session.title.trim();
  }
  const short = session.nativeId.length > 8 ? session.nativeId.slice(0, 8) : session.nativeId;
  return `${harnessLabel(session.harness)} ${short}`;
}
