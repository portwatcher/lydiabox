import type { AgentAction } from "../../api/types";

// Overlay state for `agent_action` events: the agent cursor, click ripples,
// keystroke pills, and the recent tool-call feed. Pure, so it is unit-tested.

export const RIPPLE_MS = 700;
export const PILL_MS = 2500;
export const FEED_MAX = 100;
export const PILLS_MAX = 4;

export interface Point {
  x: number;
  y: number;
}

export interface Ripple extends Point {
  id: number;
  at: number;
  double: boolean;
}

export interface Pill {
  id: number;
  text: string;
  at: number;
}

export interface FeedItem {
  id: number;
  action: AgentAction;
  receivedAt: number;
}

export interface OverlayState {
  cursor: Point | null;
  /** Start point of the last drag, drawn as a line to the cursor while it is fresh. */
  drag: (Point & { at: number }) | null;
  ripples: Ripple[];
  pills: Pill[];
  feed: FeedItem[];
  nextId: number;
}

export type OverlayEvent =
  | { type: "action"; action: AgentAction; now: number }
  | { type: "expire"; now: number }
  | { type: "reset" };

export const initialOverlay: OverlayState = { cursor: null, drag: null, ripples: [], pills: [], feed: [], nextId: 1 };

function truncate(text: string, max: number): string {
  return text.length > max ? `${text.slice(0, max - 1)}…` : text;
}

/** "ctrl+shift+t" to "Ctrl+Shift+T". */
export function formatKeys(keys: string): string {
  return keys
    .split("+")
    .map((part) => part.trim())
    .filter(Boolean)
    .map((part) => (part.length === 1 ? part.toUpperCase() : part.charAt(0).toUpperCase() + part.slice(1)))
    .join("+");
}

/** The pill text for an action, or null when the action has no pill. */
export function pillText(action: AgentAction): string | null {
  switch (action.action) {
    case "type":
      return action.text ? truncate(action.text, 40) : null;
    case "key":
      return action.keys ? formatKeys(action.keys) : null;
    case "launch":
      return action.text ? `Launch ${action.text}` : null;
    default:
      return null;
  }
}

/** One line for the feed. */
export function describeAction(action: AgentAction): string {
  const at = `${action.x}, ${action.y}`;
  switch (action.action) {
    case "move":
      return `Move to ${at}`;
    case "click": {
      const kind = action.double
        ? "Double-click"
        : action.button === "right"
          ? "Right-click"
          : action.button === "middle"
            ? "Middle-click"
            : "Click";
      return `${kind} at ${at}`;
    }
    case "drag":
      return action.fromX !== undefined && action.fromY !== undefined
        ? `Drag from ${action.fromX}, ${action.fromY} to ${at}`
        : `Drag to ${at}`;
    case "scroll":
      return `Scroll ${action.dy !== 0 ? (action.dy > 0 ? "down" : "up") : action.dx > 0 ? "right" : "left"} at ${at}`;
    case "type":
      return `Type "${truncate(action.text, 80)}"`;
    case "key":
      return `Press ${formatKeys(action.keys)}`;
    case "screenshot":
      return "Take a screenshot";
    case "launch":
      return `Launch ${action.text || "app"}`;
  }
}

const pointerActions = new Set(["move", "click", "drag", "scroll"]);

export function overlayReducer(state: OverlayState, event: OverlayEvent): OverlayState {
  switch (event.type) {
    case "reset":
      return initialOverlay;
    case "expire": {
      const ripples = state.ripples.filter((r) => event.now - r.at < RIPPLE_MS);
      const pills = state.pills.filter((p) => event.now - p.at < PILL_MS);
      const drag = state.drag && event.now - state.drag.at < PILL_MS ? state.drag : null;
      if (ripples.length === state.ripples.length && pills.length === state.pills.length && drag === state.drag) {
        return state;
      }
      return { ...state, ripples, pills, drag };
    }
    case "action": {
      const { action, now } = event;
      let id = state.nextId;
      const next: OverlayState = { ...state };
      if (pointerActions.has(action.action) && !(action.action === "scroll" && action.x === 0 && action.y === 0)) {
        next.cursor = { x: action.x, y: action.y };
      }
      if (action.action === "click") {
        next.ripples = [...state.ripples, { id: id++, x: action.x, y: action.y, at: now, double: action.double }];
      }
      if (action.action === "drag") {
        if (action.fromX !== undefined && action.fromY !== undefined) {
          next.drag = { x: action.fromX, y: action.fromY, at: now };
        }
        next.ripples = [...state.ripples, { id: id++, x: action.x, y: action.y, at: now, double: false }];
      }
      const text = pillText(action);
      if (text) {
        next.pills = [...state.pills, { id: id++, text, at: now }].slice(-PILLS_MAX);
      }
      next.feed = [{ id: id++, action, receivedAt: now }, ...state.feed].slice(0, FEED_MAX);
      next.nextId = id;
      return next;
    }
  }
}
