import { Play, Wrench } from "lucide-react";
import { useCallback, useEffect, useRef } from "react";
import { sessions } from "../../api/domains";
import type { Session, TranscriptEntry } from "../../api/types";
import { ErrorView, LoadingView, StateView } from "../../components/StateView";
import { Button } from "../../components/ui";
import { useResource } from "../../hooks/useResource";

interface TranscriptViewProps {
  session: Session;
  onResume: () => void;
  /** Why a session cannot start now, or null. */
  resumeBlocked: string | null;
}

function Entry({ entry }: { entry: TranscriptEntry }) {
  if (entry.role === "tool") {
    return (
      <div className="flex min-w-0 items-baseline gap-2 font-mono text-xs text-base-content/70">
        <Wrench aria-hidden className="size-3.5 shrink-0 self-center" />
        <span className="shrink-0 text-base-content">{entry.tool}</span>
        <span className="min-w-0 truncate" title={entry.text}>
          {entry.text}
        </span>
      </div>
    );
  }
  if (entry.role === "user") {
    return (
      <div className="chat chat-end p-0">
        <div className="chat-bubble chat-bubble-neutral max-w-[85%] text-sm break-words whitespace-pre-wrap">{entry.text}</div>
      </div>
    );
  }
  return <div className="text-sm break-words whitespace-pre-wrap">{entry.text}</div>;
}

/** The read-only history of a session that has no running terminal. */
export function TranscriptView({ session, onResume, resumeBlocked }: TranscriptViewProps) {
  const id = session.id;
  const loader = useCallback((signal: AbortSignal) => sessions.transcript(id, signal), [id]);
  const transcript = useResource(loader);
  const endRef = useRef<HTMLDivElement>(null);
  const entries = transcript.data?.entries;

  useEffect(() => {
    if (entries) {
      endRef.current?.scrollIntoView({ block: "end" });
    }
  }, [entries]);

  let body;
  if (transcript.loading && !transcript.data) {
    body = <LoadingView label="Loading history…" />;
  } else if (transcript.error && !transcript.data) {
    body = <ErrorView error={transcript.error} subsystem="Sessions" failed="History not loaded" onRetry={transcript.reload} />;
  } else if (!entries || entries.length === 0) {
    body = <StateView title="No messages yet" />;
  } else {
    body = (
      <ol aria-label="History" className="mx-auto flex max-w-3xl flex-col gap-3 p-4">
        {transcript.data?.truncated ? <li className="text-center text-xs text-base-content/70">Older messages are not shown.</li> : null}
        {entries.map((e, i) => (
          <li key={i} className="flex min-w-0 flex-col">
            <Entry entry={e} />
          </li>
        ))}
      </ol>
    );
  }

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="min-h-0 flex-1 overflow-y-auto">
        {body}
        <div ref={endRef} />
      </div>
      <div className="flex justify-center border-t border-base-content/10 p-3">
        <Button variant="primary" icon={Play} onClick={onResume} disabled={resumeBlocked !== null} title={resumeBlocked ?? undefined}>
          Continue session
        </Button>
      </div>
    </div>
  );
}
