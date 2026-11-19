import { ArrowDown, Play } from "lucide-react";
import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import { describeError } from "../../api/errors";
import { sessions } from "../../api/domains";
import type { AgentState } from "../../api/events";
import type { Model, Session } from "../../api/types";
import { AgentStatus } from "../../components/agent/AgentStatus";
import { AgentTimeline } from "../../components/agent/AgentTimeline";
import { ContextMeter } from "../../components/agent/ContextMeter";
import { DesktopHandoff, TerminalHandoff } from "../../components/agent/HandoffBanner";
import { PlanCard } from "../../components/agent/PlanCard";
import { PromptComposer } from "../../components/agent/PromptComposer";
import { StarterPrompts } from "../../components/agent/StarterPrompts";
import { lastReply } from "../../components/agent/chatState";
import { useAgentEvents } from "../../components/agent/useAgentEvents";
import { LoadingView, StateView } from "../../components/StateView";
import { useToast } from "../../components/Toast";
import { Banner, Button } from "../../components/ui";
import { VmWakeButton } from "../../components/VmPower";
import { useSystem } from "../../hooks/useSystem";

interface ChatViewProps {
  session: Session;
  models: Model[];
  onOpenTerminal: () => void;
  onResume: () => void;
  /** Why a session cannot start now, or null. */
  resumeBlocked: string | null;
  /** The session got its native ID: select the new ID. */
  onAdopt: (id: string) => void;
}

/** After a send, the view shows "working" this long before the terminal reacts. */
const SEND_GRACE_MS = 4000;

/**
 * The structured view of a harness session: the conversation from the
 * session events, the state of the agent, and a prompt field that types
 * into the session terminal.
 */
export function ChatView({ session, models, onOpenTerminal, onResume, resumeBlocked, onAdopt }: ChatViewProps) {
  const toast = useToast();
  const { vmState } = useSystem();
  const { chat, timeline, socket, error } = useAgentEvents(session.id);
  const status = chat.status;
  const live = status ? status.live : session.live;
  const [draft, setDraft] = useState("");
  const [sending, setSending] = useState(false);
  const [sentAt, setSentAt] = useState(0);
  const [now, setNow] = useState(() => Date.now());
  const composer = useRef<HTMLTextAreaElement>(null);

  const graced = sentAt > 0 && now - sentAt < SEND_GRACE_MS;
  const state: AgentState = !live ? "offline" : status?.state === "idle" && graced ? "working" : (status?.state ?? "idle");
  const working = state === "working";

  useEffect(() => {
    if (!graced) {
      return;
    }
    const id = setTimeout(() => setNow(Date.now()), SEND_GRACE_MS);
    return () => clearTimeout(id);
  }, [graced]);

  // A provisional session got its native ID.
  useEffect(() => {
    if (status?.sessionId && status.sessionId !== session.id) {
      onAdopt(status.sessionId);
    }
  }, [status?.sessionId, session.id, onAdopt]);

  // The time the agent started to work, for the elapsed time.
  const [since, setSince] = useState(0);
  useEffect(() => {
    setSince((prev) => (working ? prev || Date.now() : 0));
  }, [working]);

  // Screen readers hear the reply when a turn ends, and a request for input.
  const [announce, setAnnounce] = useState("");
  const prevState = useRef(state);
  const lastTurn = timeline.turns[timeline.turns.length - 1];
  useEffect(() => {
    if (prevState.current === "working" && state === "idle") {
      setAnnounce(lastReply(lastTurn).slice(0, 400));
    } else if (state === "attention" && prevState.current !== "attention") {
      setAnnounce("The agent waits for your answer in the terminal.");
    }
    prevState.current = state;
  }, [state, lastTurn]);

  // Follow new output while the view is at the bottom.
  const scroller = useRef<HTMLDivElement>(null);
  const [atBottom, setAtBottom] = useState(true);
  useLayoutEffect(() => {
    const el = scroller.current;
    if (el && atBottom) {
      el.scrollTop = el.scrollHeight;
    }
  }, [chat, atBottom]);
  const onScroll = () => {
    const el = scroller.current;
    if (el) {
      setAtBottom(el.scrollHeight - el.scrollTop - el.clientHeight < 48);
    }
  };
  const toBottom = () => {
    scroller.current?.scrollTo({ top: scroller.current.scrollHeight, behavior: window.matchMedia("(prefers-reduced-motion: reduce)").matches ? "auto" : "smooth" });
  };

  const send = useCallback(async () => {
    const text = draft.trim();
    if (!text) {
      return;
    }
    setSending(true);
    try {
      await sessions.input(session.id, text);
      setDraft("");
      setSentAt(Date.now());
      setNow(Date.now());
      setAtBottom(true);
    } catch (err) {
      toast.error(describeError(err, "Message not sent"));
    } finally {
      setSending(false);
    }
  }, [draft, session.id, toast]);

  const interrupt = async () => {
    setSentAt(0);
    try {
      await sessions.interrupt(session.id);
    } catch (err) {
      toast.error(describeError(err, "Agent not stopped"));
    }
  };

  const pick = (prompt: string) => {
    setDraft(prompt);
    composer.current?.focus();
  };

  const contextWindow = timeline.context?.window || models.find((m) => m.id === (timeline.model || session.model))?.contextWindow || 0;
  const synced = status?.synced ?? false;
  const empty = timeline.turns.length === 0;
  const lastDesktop = lastTurn?.desktop ?? false;

  let body;
  if (error && empty) {
    body = <StateView title="Session not found">{error}</StateView>;
  } else if (empty && !synced && status?.error) {
    body = (
      <StateView title="History not loaded" action={vmState === "STOPPED" ? <VmWakeButton variant="primary" /> : undefined}>
        {status.error}
      </StateView>
    );
  } else if (empty && !synced) {
    body = <LoadingView label="Loading the conversation…" />;
  } else if (empty && live) {
    body = <StarterPrompts onPick={pick} />;
  } else if (empty) {
    body = <StateView title="No messages yet" />;
  } else {
    body = (
      <div className="mx-auto w-full max-w-3xl px-4 py-6">
        <AgentTimeline turns={timeline.turns} working={working} />
      </div>
    );
  }

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div role="status" className="sr-only">
        {announce}
      </div>
      <div className="relative min-h-0 flex-1">
        <div ref={scroller} onScroll={onScroll} className="h-full overflow-y-auto">
          {body}
        </div>
        {!atBottom ? (
          <Button size="sm" icon={ArrowDown} onClick={toBottom} className="absolute bottom-3 left-1/2 -translate-x-1/2 shadow-lg">
            Latest
          </Button>
        ) : null}
      </div>
      <div className="mx-auto flex w-full max-w-3xl flex-col gap-2 px-4 pt-2 pb-4">
        {socket === "reconnecting" || socket === "failed" ? <Banner tone="warning">{socket === "failed" ? "Connection lost. Reload the page." : "Reconnecting…"}</Banner> : null}
        {state === "attention" ? <TerminalHandoff prompt={status?.prompt ?? ""} onOpenTerminal={onOpenTerminal} /> : null}
        {live && working && lastDesktop ? <DesktopHandoff /> : null}
        {working && timeline.plan && timeline.plan.some((s) => s.status !== "completed") ? <PlanCard steps={timeline.plan} compact /> : null}
        <div className="flex min-h-7 items-center gap-3">
          <AgentStatus state={state} since={since} />
          <span className="flex-1" />
          {timeline.context ? <ContextMeter tokens={timeline.context.tokens} window={contextWindow} /> : null}
        </div>
        {live ? (
          <PromptComposer
            ref={composer}
            value={draft}
            onChange={setDraft}
            onSubmit={() => void send()}
            working={working}
            onInterrupt={() => void interrupt()}
            sending={sending}
            placeholder={empty ? "Describe a task" : "Reply to the agent"}
          />
        ) : (
          <div className="flex justify-center">
            <Button variant="primary" icon={Play} onClick={onResume} disabled={resumeBlocked !== null} title={resumeBlocked ?? undefined}>
              Continue session
            </Button>
          </div>
        )}
      </div>
    </div>
  );
}
