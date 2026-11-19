import { Check, ChevronRight, CircleDashed, X } from "lucide-react";
import { useState } from "react";
import { Spinner, cx } from "../ui";
import type { ToolItem } from "./chatState";
import { CodeDiff } from "./CodeDiff";
import { formatDuration, toolKinds, toolLabel } from "./toolKinds";

type CallState = "running" | "ok" | "error" | "unknown";

function callState(item: ToolItem, active: boolean): CallState {
  const r = item.result;
  if (!r || r.status === "pending") {
    return active ? "running" : "unknown";
  }
  return r.status === "error" ? "error" : "ok";
}

const stateText: Record<CallState, string> = { running: "Running", ok: "Done", error: "Failed", unknown: "No result" };

function StateIcon({ state }: { state: CallState }) {
  switch (state) {
    case "running":
      return (
        <>
          <Spinner size="xs" className="text-info motion-reduce:hidden" />
          <CircleDashed aria-hidden className="size-3.5 text-info motion-safe:hidden" />
        </>
      );
    case "ok":
      return <Check aria-hidden className="size-3.5 text-success" />;
    case "error":
      return <X aria-hidden className="size-3.5 text-error" />;
    default:
      return <CircleDashed aria-hidden className="size-3.5 text-base-content/70" />;
  }
}

/** Screenshots of a result as thumbnails; a click shows one at full width. */
export function ResultImages({ images }: { images: string[] }) {
  const [open, setOpen] = useState<number | null>(null);
  if (images.length === 0) {
    return null;
  }
  return (
    <div className="flex flex-wrap gap-2">
      {images.map((src, i) => (
        <button
          key={i}
          type="button"
          onClick={() => setOpen(open === i ? null : i)}
          aria-expanded={open === i}
          aria-label={open === i ? "Show the screenshot smaller" : "Show the screenshot larger"}
          className={cx(
            "overflow-hidden rounded-box border border-base-content/10 bg-base-300 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-base-content",
            open === i ? "w-full" : "w-48",
          )}
        >
          <img src={src} alt="Screenshot from the tool" className="block h-auto w-full" />
        </button>
      ))}
    </div>
  );
}

function Block({ label, children }: { label: string; children: string }) {
  return (
    <div className="flex min-w-0 flex-col gap-1">
      <span className="text-xs font-medium text-base-content/70">{label}</span>
      <pre className="max-h-72 overflow-auto rounded-box bg-base-300 px-3 py-2 font-mono text-xs leading-5 break-words whitespace-pre-wrap">
        {children}
      </pre>
    </div>
  );
}

/**
 * One tool call: a row with the kind icon, the verb and target, the time it
 * took, and its state; it opens to the input, the output, and the diff of
 * an edit. Screenshots stay visible as thumbnails.
 */
export function ToolCallCard({ item, active }: { item: ToolItem; active: boolean }) {
  const { call, result } = item;
  const kind = toolKinds[call.kind];
  const Icon = kind.icon;
  const state = callState(item, active);
  const named = call.kind === "mcp" || call.kind === "other" || call.kind === "agent";
  const target = call.path || call.summary;
  const duration = result ? formatDuration(result.durationMs) : "";
  const showInput = call.input && !(call.kind === "edit" && call.diff);
  const images = result?.images ?? [];
  return (
    <div className="flex min-w-0 flex-col gap-2">
      <details className="group min-w-0 rounded-box border border-base-content/10 bg-base-100">
        <summary className="flex min-h-10 cursor-pointer list-none items-center gap-2 rounded-box px-3 py-1.5 text-sm hover:bg-base-content/5 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-base-content [&::-webkit-details-marker]:hidden">
          <ChevronRight aria-hidden className="size-3.5 shrink-0 text-base-content/70 group-open:rotate-90 motion-safe:transition-transform" />
          <Icon aria-hidden className="size-4 shrink-0 text-base-content/70" />
          <span className="shrink-0 font-medium">{named ? `${kind.verb} ${toolLabel(call.name)}` : kind.verb}</span>
          <span className="min-w-0 flex-1 truncate font-mono text-xs text-base-content/70" title={target}>
            {target}
          </span>
          {duration ? <span className="shrink-0 text-xs tabular-nums text-base-content/70">{duration}</span> : null}
          <span className="flex shrink-0 items-center" title={stateText[state]}>
            <StateIcon state={state} />
            <span className="sr-only">{stateText[state]}</span>
          </span>
        </summary>
        <div className="flex min-w-0 flex-col gap-3 border-t border-base-content/10 p-3">
          {call.kind === "edit" && call.diff ? <CodeDiff diff={call.diff} path={call.path} /> : null}
          {showInput ? <Block label="Input">{call.input}</Block> : null}
          {result?.output ? <Block label={state === "error" ? "Error" : "Output"}>{result.output}</Block> : null}
          {!showInput && !result?.output && !(call.kind === "edit" && call.diff) ? (
            <p className="text-xs text-base-content/70">{call.name}</p>
          ) : null}
        </div>
      </details>
      <ResultImages images={images} />
    </div>
  );
}

/**
 * Consecutive tool calls. Three or more fold into one row ("Used 4 tools")
 * that stays open while a call runs.
 */
export function ToolGroup({ tools, active }: { tools: ToolItem[]; active: boolean }) {
  const running = active && tools.some((t) => !t.result || t.result.status === "pending");
  const list = (
    <ol className="flex min-w-0 flex-col gap-1.5">
      {tools.map((t) => (
        <li key={t.key} className="min-w-0">
          <ToolCallCard item={t} active={active} />
        </li>
      ))}
    </ol>
  );
  if (tools.length < 3) {
    return list;
  }
  const failed = tools.filter((t) => t.result?.status === "error").length;
  return (
    <details open={running || undefined} className="group/tools min-w-0">
      <summary className="flex min-h-8 cursor-pointer list-none items-center gap-2 rounded-box px-1 text-sm text-base-content/70 hover:text-base-content focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-base-content [&::-webkit-details-marker]:hidden">
        <ChevronRight aria-hidden className="size-3.5 group-open/tools:rotate-90 motion-safe:transition-transform" />
        {running ? `Using ${tools.length} tools` : `Used ${tools.length} tools`}
        {failed > 0 ? <span className="text-error">{failed} failed</span> : null}
      </summary>
      <div className="pt-1.5">{list}</div>
    </details>
  );
}
