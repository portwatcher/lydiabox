import type { AgentEvent, EventStatus, PlanStep, ToolCall, ToolResult, TurnUsage } from "../../api/events";

/** The events of one session in order, and the stream status. */
export interface ChatState {
  order: string[];
  byKey: Record<string, AgentEvent>;
  status: EventStatus | null;
}

export type ChatAction =
  | { type: "events"; reset: boolean; events: AgentEvent[] }
  | { type: "status"; status: EventStatus }
  | { type: "clear" };

export const emptyChat: ChatState = { order: [], byKey: {}, status: null };

/** Adds new events, replaces events of a known key, and keeps the order of first arrival. */
export function chatReducer(state: ChatState, action: ChatAction): ChatState {
  switch (action.type) {
    case "clear":
      return emptyChat;
    case "status":
      return { ...state, status: action.status };
    case "events": {
      const order = action.reset ? [] : state.order.slice();
      const byKey: Record<string, AgentEvent> = action.reset ? {} : { ...state.byKey };
      for (const e of action.events) {
        if (!(e.key in byKey)) {
          order.push(e.key);
        }
        byKey[e.key] = e;
      }
      return { ...state, order, byKey };
    }
  }
}

/** A tool call with its result (from the call event or a tool_result event). */
export interface ToolItem {
  type: "tool";
  key: string;
  call: ToolCall;
  result?: ToolResult;
  time: string;
}

export type TurnItem =
  | { type: "text"; key: string; text: string }
  | { type: "reasoning"; key: string; text: string }
  | { type: "tools"; key: string; tools: ToolItem[] }
  | { type: "plan"; key: string; steps: PlanStep[] }
  | { type: "error"; key: string; text: string };

/** Totals of one turn. */
export interface TurnStats {
  durationMs: number;
  inputTokens: number;
  outputTokens: number;
  costUsd: number;
  model: string;
  tools: number;
}

/** A file or page that a turn read or changed. */
export interface TurnSource {
  key: string;
  kind: "read" | "edit" | "web";
  label: string;
}

export interface Turn {
  key: string;
  prompt?: { text: string; time: string };
  items: TurnItem[];
  ended: boolean;
  interrupted: boolean;
  stats: TurnStats;
  sources: TurnSource[];
  /** The turn used the desktop or a browser. */
  desktop: boolean;
}

export interface Timeline {
  turns: Turn[];
  plan: PlanStep[] | null;
  context: { tokens: number; window: number } | null;
  model: string;
}

function elapsed(from: string, to: string): number {
  const a = Date.parse(from);
  const b = Date.parse(to);
  return Number.isFinite(a) && Number.isFinite(b) && b > a ? b - a : 0;
}

function newTurn(key: string): Turn {
  return {
    key,
    items: [],
    ended: false,
    interrupted: false,
    stats: { durationMs: 0, inputTokens: 0, outputTokens: 0, costUsd: 0, model: "", tools: 0 },
    sources: [],
    desktop: false,
  };
}

function addSource(turn: Turn, call: ToolCall): void {
  let source: TurnSource | null = null;
  if ((call.kind === "read" || call.kind === "edit") && call.path) {
    source = { key: call.path, kind: call.kind, label: call.path };
  } else if (call.kind === "web" && /^https?:\/\//.test(call.summary)) {
    source = { key: call.summary, kind: "web", label: call.summary };
  }
  if (!source) {
    return;
  }
  const known = turn.sources.find((s) => s.key === source.key);
  if (!known) {
    turn.sources.push(source);
  } else if (source.kind === "edit") {
    known.kind = "edit";
  }
}

function addUsage(turn: Turn, usage: TurnUsage, model: string): void {
  turn.stats.inputTokens += usage.inputTokens;
  turn.stats.outputTokens += usage.outputTokens;
  turn.stats.costUsd += usage.costUsd;
  if (model) {
    turn.stats.model = model;
  }
}

/** Groups the events into turns: a user prompt and what the agent did after it. */
export function buildTimeline(state: ChatState): Timeline {
  const events = state.order.map((k) => state.byKey[k]).filter((e): e is AgentEvent => e !== undefined);
  const results = new Map<string, ToolResult>();
  for (const e of events) {
    if (e.kind === "tool_result" && e.result) {
      results.set(e.result.callId, e.result);
    }
  }
  const turns: Turn[] = [];
  let turn: Turn | null = null;
  let plan: PlanStep[] | null = null;
  let context: Timeline["context"] = null;
  let model = "";
  let start = "";
  let last = "";
  const current = (key: string): Turn => {
    if (!turn) {
      turn = newTurn(key);
      turns.push(turn);
    }
    return turn;
  };
  const finish = () => {
    if (turn && start && last) {
      turn.stats.durationMs = elapsed(start, last);
    }
  };
  for (const e of events) {
    if (e.kind === "user") {
      finish();
      turn = newTurn(e.key);
      turn.prompt = { text: e.text, time: e.time };
      turns.push(turn);
      start = e.time;
      last = e.time;
      continue;
    }
    if (e.model) {
      model = e.model;
    }
    if (e.kind === "tool_result") {
      if (e.time) {
        last = e.time;
      }
      continue;
    }
    const t = current(e.key);
    if (e.time) {
      last = e.time;
      start = start || e.time;
    }
    switch (e.kind) {
      case "assistant":
        t.items.push({ type: "text", key: e.key, text: e.text });
        t.ended = false;
        break;
      case "reasoning":
        t.items.push({ type: "reasoning", key: e.key, text: e.text });
        break;
      case "error":
        t.items.push({ type: "error", key: e.key, text: e.text });
        break;
      case "plan":
        if (e.plan) {
          plan = e.plan;
          t.items.push({ type: "plan", key: e.key, steps: e.plan });
        }
        break;
      case "tool_call": {
        if (!e.tool) {
          break;
        }
        const item: ToolItem = { type: "tool", key: e.key, call: e.tool, result: e.result ?? results.get(e.tool.id), time: e.time };
        const prev = t.items[t.items.length - 1];
        if (prev && prev.type === "tools") {
          prev.tools.push(item);
        } else {
          t.items.push({ type: "tools", key: e.key, tools: [item] });
        }
        t.stats.tools += 1;
        t.ended = false;
        addSource(t, e.tool);
        if (e.tool.kind === "computer" || e.tool.kind === "browser") {
          t.desktop = true;
        }
        break;
      }
      case "usage":
        if (e.usage) {
          addUsage(t, e.usage, e.model);
          if (e.usage.contextTokens > 0) {
            context = { tokens: e.usage.contextTokens, window: e.usage.contextWindow };
          }
        }
        break;
      case "turn_end":
        t.ended = true;
        t.interrupted = e.text === "interrupted";
        break;
      default:
        break;
    }
  }
  finish();
  return { turns, plan, context, model };
}

/** The text that a screen reader hears when a turn ends: its last reply. */
export function lastReply(turn: Turn | undefined): string {
  if (!turn) {
    return "";
  }
  for (let i = turn.items.length - 1; i >= 0; i--) {
    const item = turn.items[i];
    if (item.type === "text") {
      return item.text;
    }
  }
  return "";
}
