import { ApiError, NetworkError } from "../api/http";

/**
 * Why the status poll does not answer:
 * - `stopped`: the server in front of the cell says that the agent computer
 *   is not running (LydiaBox Cloud stops the whole cell process).
 * - `starting`: the same server says that it starts.
 * - `unknown`: the server cannot be reached or answers 5xx; the last state
 *   is no longer a fact.
 */
export type OutageKind = "stopped" | "starting" | "unknown";

export interface Outage {
  kind: OutageKind;
  /** One sentence for the person, from the server when it gave one. */
  message: string;
}

/** A failing status poll counts as `unknown` only after this time, so that one lost poll changes nothing. */
export const STATUS_GRACE_MS = 10_000;

/** Codes of the control plane in front of a cell (LydiaBox Cloud). */
export const CELL_STOPPED_CODE = "cell_stopped";
export const CELL_STARTING_CODE = "cell_starting";

// The answers of the control plane before it sent the codes above.
const legacyStopped = "the agent computer is not running";
const legacyStarting = "the agent computer is starting";

function sentence(message: string, fallback: string): string {
  const t = message.trim();
  if (!t) {
    return fallback;
  }
  const s = t.charAt(0).toUpperCase() + t.slice(1);
  return /[.!?]$/.test(s) ? s : `${s}.`;
}

/**
 * Splits a server text that ends with a link ("... start it from the
 * dashboard at //lydiabox.com/") into the text without it and the link.
 */
export function splitLink(message: string): { text: string; href: string | null } {
  const m = /^(.*?)\s+at\s+((?:https?:)?\/\/\S+?)\.?$/.exec(message.trim());
  if (!m) {
    return { text: message, href: null };
  }
  return { text: sentence(m[1], message), href: m[2] };
}

/**
 * Classifies the error of the last status poll. `graceOver` is true when
 * the polls have failed for STATUS_GRACE_MS or longer. Returns null when
 * the error is not an outage (or there is none yet).
 */
export function statusOutage(err: unknown, graceOver: boolean): Outage | null {
  if (err instanceof ApiError) {
    const message = err.message.trim().toLowerCase();
    if (err.code === CELL_STOPPED_CODE || (err.status === 503 && message.startsWith(legacyStopped))) {
      return { kind: "stopped", message: sentence(err.message, "The agent computer is not running.") };
    }
    if (err.code === CELL_STARTING_CODE || (err.status === 503 && message.startsWith(legacyStarting))) {
      return { kind: "starting", message: sentence(err.message, "The agent computer is starting.") };
    }
    if (err.status >= 500 && graceOver) {
      return { kind: "unknown", message: "The VM status is not available. LydiaBox tries again." };
    }
    return null;
  }
  if (err instanceof NetworkError && graceOver) {
    return { kind: "unknown", message: "The VM status is not available: the server cannot be reached. LydiaBox tries again." };
  }
  return null;
}
