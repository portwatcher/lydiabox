import type { HarnessID } from "../api/types";

export const harnessLabels: Record<HarnessID, string> = {
  "claude-code": "Claude Code",
  codex: "Codex",
  dsh: "dsh",
  antigravity: "Antigravity",
  opencode: "OpenCode",
  shell: "Shell",
};

/**
 * What the first start of a harness asks in its terminal, when it asks
 * more than the screen title shows. Antigravity shows the color scheme
 * step until its terms are answered (agy 1.5.12 keeps the choice until
 * then), so LydiaBox cannot skip it.
 */
const setupSteps: Partial<Record<HarnessID, string>> = {
  antigravity: "Choose a color scheme. Then read the terms, and accept them if you agree.",
};

export function setupStepsOf(id: string): string {
  return id in setupSteps ? (setupSteps[id as HarnessID] ?? "") : "";
}

export function harnessLabel(id: string): string {
  return id in harnessLabels ? harnessLabels[id as HarnessID] : id;
}

/**
 * Harnesses whose sessions run in the chat view through their ACP agent
 * (engine "acp"). The others run in a terminal; their chat view reads the
 * transcript and types into the terminal. Antigravity's ACP agent cannot
 * use the LydiaBox model gateway (docs/harness-formats.md, "ACP agents").
 */
export const acpHarnesses: readonly HarnessID[] = ["claude-code", "codex", "dsh", "opencode"];

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
