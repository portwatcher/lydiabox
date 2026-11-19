import { ArrowLeft, Play, SquareTerminal } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useSearchParams } from "react-router";
import { describeError } from "../api/errors";
import { sessions } from "../api/domains";
import type { HarnessID, Session } from "../api/types";
import { ConfirmDialog } from "../components/Dialog";
import { StateView } from "../components/StateView";
import { useToast } from "../components/Toast";
import { Badge, Button, cx } from "../components/ui";
import { useModelOptions } from "../hooks/useModelOptions";
import { useResource } from "../hooks/useResource";
import { useSystem } from "../hooks/useSystem";
import { acpHarnesses, engineFor, harnessLabel, sessionName } from "../lib/harness";
import { NewSessionDialog } from "./studio/NewSessionDialog";
import { NoModelNotice, modelProblem } from "./studio/NoModelNotice";
import { SessionList } from "./studio/SessionList";
import { SessionModelMenu } from "./studio/SessionModelMenu";
import { deleteText, deletesWithoutConfirm, listDisagrees, listOutOfDate } from "./studio/sessionActions";
import type { SessionFilter } from "./studio/SessionList";
import { ChatView } from "./studio/ChatView";
import { TerminalView } from "./studio/TerminalView";
import { ViewSwitch } from "./studio/ViewSwitch";
import type { SessionViewMode } from "./studio/ViewSwitch";

const CELL = { width: 7.8, height: 17 };

export default function StudioPage() {
  const toast = useToast();
  const [params, setParams] = useSearchParams();
  const selectedId = params.get("session");
  // Harness sessions open in the chat view; the terminal is one switch away.
  const viewMode: SessionViewMode = params.get("view") === "terminal" ? "terminal" : "chat";
  const [filter, setFilter] = useState<SessionFilter>({ harness: "", workspace: "" });
  const [harnesses, setHarnesses] = useState<HarnessID[]>([]);
  const [workspaces, setWorkspaces] = useState<string[]>([]);
  const [room, setRoom] = useState<{ key: number; id: string } | null>(null);
  const [newOpen, setNewOpen] = useState(false);
  const [deleting, setDeleting] = useState<Session | null>(null);
  const [purge, setPurge] = useState(false);
  const termSize = useRef<{ cols: number; rows: number } | null>(null);
  const centerRef = useRef<HTMLDivElement>(null);
  const { models, state: modelState } = useModelOptions();
  const noModel = modelProblem(modelState, models.length);

  const loader = useCallback((signal: AbortSignal) => sessions.list(filter, signal), [filter]);
  const list = useResource(loader, { pollMs: 10_000 });
  const listed = list.data?.sessions;
  const cachedAt = list.data?.cached ? (list.data.cachedAt ?? "") : null;
  const reloadList = list.reload;

  // A stop or pause ends live terminals; a start or resume lets the guest be
  // scanned again: load a fresh list at once.
  const { vmState, startBlocked, status } = useSystem();
  const prevState = useRef(vmState);
  const retries = useRef(0);
  useEffect(() => {
    if (listOutOfDate(prevState.current, vmState)) {
      retries.current = 0;
      reloadList();
    }
    prevState.current = vmState;
  }, [vmState, reloadList]);

  // The first list after a change can still show the old state (the guest
  // is not scanned yet): load it again after 2 s, at most 5 times, instead
  // of waiting for the 10 s poll.
  const disagrees = list.data !== undefined && listDisagrees(vmState, list.data.cached ?? false, status?.storage.mode === "provider");
  const listData = list.data;
  useEffect(() => {
    if (!disagrees || retries.current >= 5) {
      return;
    }
    const id = setTimeout(() => {
      retries.current += 1;
      reloadList();
    }, 2000);
    return () => clearTimeout(id);
  }, [disagrees, listData, reloadList]);

  useEffect(() => {
    if (!listed) {
      return;
    }
    const seenHarnesses = listed.map((s) => s.harness);
    setHarnesses((prev) => {
      const merged = Array.from(new Set([...prev, ...seenHarnesses]));
      return merged.length === prev.length ? prev : merged;
    });
    const seen = listed.map((s) => s.workspaceDir);
    setWorkspaces((prev) => {
      const merged = Array.from(new Set([...prev, ...seen])).sort();
      return merged.length === prev.length ? prev : merged;
    });
  }, [listed]);

  const [known, setKnown] = useState<Session | null>(null);
  const selected = useMemo(
    () => listed?.find((s) => s.id === selectedId) ?? (known && known.id === selectedId ? known : null),
    [listed, selectedId, known],
  );
  const selectedSession = useRef(selected);
  selectedSession.current = selected;

  const select = useCallback(
    (id: string | null, replace = false) => {
      setParams(
        (prev) => {
          const next = new URLSearchParams(prev);
          if (id) {
            next.set("session", id);
          } else {
            next.delete("session");
          }
          return next;
        },
        { replace },
      );
    },
    [setParams],
  );

  // Attach a terminal room when a live session is selected.
  useEffect(() => {
    if (selected?.live && room?.id !== selected.id) {
      setRoom((current) => (current?.id === selected.id ? current : { key: Date.now(), id: selected.id }));
    }
  }, [selected, room?.id]);

  const setViewMode = useCallback(
    (mode: SessionViewMode) => {
      setParams(
        (prev) => {
          const next = new URLSearchParams(prev);
          if (mode === "terminal") {
            next.set("view", "terminal");
          } else {
            next.delete("view");
          }
          return next;
        },
        { replace: true },
      );
    },
    [setParams],
  );

  // A provisional session got its native ID: follow it to the new ID.
  const adoptId = useCallback(
    (id: string) => {
      const current = selectedSession.current;
      if (current) {
        setKnown({ ...current, id });
      }
      setRoom((current) => (current ? { key: current.key, id } : current));
      select(id, true);
      reloadList();
    },
    [select, reloadList],
  );

  const attachedId = room?.id ?? null;
  const showTerminal = room !== null && attachedId === selectedId;

  // A live session of an ACP harness shows the view of its engine; the
  // switch moves it to the other engine (one front end at a time).
  const [engineBusy, setEngineBusy] = useState(false);
  const harnessSelected = selected !== null && selected.harness !== "shell";
  const engineSwitch = harnessSelected && selected.live && acpHarnesses.includes(selected.harness);
  const shownMode: SessionViewMode = engineSwitch ? (selected.engine === "acp" ? "chat" : "terminal") : viewMode;

  const size = useCallback(() => {
    if (termSize.current) {
      return termSize.current;
    }
    const el = centerRef.current;
    if (!el) {
      return { cols: 160, rows: 48 };
    }
    return {
      cols: Math.max(40, Math.floor((el.clientWidth - 16) / CELL.width)),
      rows: Math.max(12, Math.floor((el.clientHeight - 64) / CELL.height)),
    };
  }, []);

  // A model chosen for a session that does not run: "Continue session" uses it.
  const [modelChoice, setModelChoice] = useState<{ id: string; model: string } | null>(null);
  const [modelBusy, setModelBusy] = useState(false);
  const modelOf = (session: Session) => (modelChoice?.id === session.id ? modelChoice.model : session.model);

  const adopt = (session: Session) => {
    setKnown(session);
    setRoom({ key: Date.now(), id: session.id });
    select(session.id);
    list.reload();
  };

  const resume = async (session: Session) => {
    try {
      const { cols, rows } = size();
      const created = await sessions.create({
        harness: session.harness,
        workspaceDir: session.workspaceDir,
        model: modelOf(session) || undefined,
        resumeNativeId: session.harness === "shell" ? undefined : session.nativeId,
        engine: session.harness === "shell" ? undefined : engineFor(session.harness, viewMode),
        cols,
        rows,
      });
      adopt(created);
    } catch (err) {
      toast.error(describeError(err, "Session not resumed"));
    }
  };

  // A running session restarts its harness with the new model and keeps the
  // conversation; a session that does not run uses it when it continues.
  const changeModel = async (session: Session, model: string) => {
    if (!(showTerminal && session.live)) {
      setModelChoice({ id: session.id, model });
      return;
    }
    setModelBusy(true);
    try {
      adopt(await sessions.setModel(session.id, { model, ...size() }));
    } catch (err) {
      toast.error(describeError(err, "Model not changed"));
    } finally {
      setModelBusy(false);
    }
  };

  // The switch of a live ACP-harness session ends its harness process and
  // continues the same native session in the other engine.
  const changeView = async (mode: SessionViewMode) => {
    setViewMode(mode);
    if (!selected || !engineSwitch || mode === shownMode) {
      return;
    }
    setEngineBusy(true);
    try {
      adopt(await sessions.setEngine(selected.id, { engine: engineFor(selected.harness, mode), ...size() }));
    } catch (err) {
      toast.error(describeError(err, mode === "chat" ? "Chat not opened" : "Terminal not opened"));
    } finally {
      setEngineBusy(false);
    }
  };

  const remove = async (session: Session, withPurge: boolean) => {
    await sessions.remove(session.id, withPurge);
    if (session.id === selectedId) {
      select(null);
    }
    if (session.id === attachedId) {
      setRoom(null);
    }
    toast.success(`${sessionName(session)} deleted`);
    list.reload();
  };

  const deleteCopy = deleteText(deleting ?? { harness: "shell" });

  const confirmDelete = async () => {
    if (deleting) {
      await remove(deleting, purge);
    }
  };

  // Other sessions ask first (running terminal, transcript to keep or purge).
  const requestDelete = (session: Session) => {
    if (deletesWithoutConfirm(session)) {
      remove(session, false).catch((err: unknown) => toast.error(describeError(err, "Session not deleted")));
      return;
    }
    setPurge(false);
    setDeleting(session);
  };

  return (
    <div className="flex h-full min-h-0">
      <aside
        className={cx(
          "w-full shrink-0 border-r border-base-content/10 bg-base-200 md:flex md:w-80 md:flex-col",
          selectedId ? "hidden" : "flex flex-col",
        )}
      >
        <SessionList
          sessions={listed}
          cachedAt={cachedAt}
          error={list.error}
          loading={list.loading}
          onRetry={list.reload}
          filter={filter}
          onHarness={(harness: HarnessID | "") => setFilter((f) => ({ ...f, harness }))}
          onWorkspace={(workspace) => setFilter((f) => ({ ...f, workspace }))}
          harnesses={harnesses}
          workspaces={workspaces}
          selectedId={selectedId}
          onSelect={(s) => select(s.id)}
          onDelete={requestDelete}
          onNew={() => setNewOpen(true)}
          newBlocked={startBlocked}
        />
      </aside>
      <section ref={centerRef} className={cx("min-w-0 flex-1 flex-col", selectedId ? "flex" : "hidden md:flex")}>
        {selectedId ? (
          <header className="flex min-h-12 flex-wrap items-center gap-2 border-b border-base-content/10 px-3 py-2">
            <Button size="sm" variant="ghost" icon={ArrowLeft} className="md:hidden" onClick={() => select(null)}>
              Sessions
            </Button>
            <h1 className="min-w-32 flex-1 truncate text-sm font-semibold" title={selected ? sessionName(selected) : selectedId}>
              {selected ? sessionName(selected) : selectedId}
            </h1>
            {selected ? <Badge tone="accent">{harnessLabel(selected.harness)}</Badge> : null}
            {harnessSelected && (showTerminal || !selected.live) ? (
              <ViewSwitch value={shownMode} onChange={(mode) => void changeView(mode)} disabled={engineBusy || modelBusy} />
            ) : null}
            {selected && selected.harness !== "shell" ? (
              <SessionModelMenu value={modelOf(selected)} models={models} onChange={(m) => void changeModel(selected, m)} disabled={modelBusy} />
            ) : null}
          </header>
        ) : null}
        {noModel && selected && selected.harness !== "shell" ? (
          <div className="border-b border-base-content/10 px-3 py-2">
            <NoModelNotice problem={noModel} />
          </div>
        ) : null}
        <div className="min-h-0 flex-1">
          {harnessSelected && (shownMode === "chat" || !showTerminal) ? (
            <ChatView
              key={selected.id}
              session={selected}
              models={models}
              onOpenTerminal={() => setViewMode("terminal")}
              onResume={() => void resume(selected)}
              resumeBlocked={startBlocked}
              onAdopt={adoptId}
            />
          ) : showTerminal && room ? (
            <TerminalView
              key={room.key}
              sessionId={room.id}
              onExit={() => list.reload()}
              onAttach={() => list.reload()}
              onSize={(cols, rows) => {
                termSize.current = { cols, rows };
              }}
            />
          ) : selected ? (
            <StateView
              icon={SquareTerminal}
              title={selected.status === "exited" ? "This shell has ended" : "This shell is not running"}
              action={
                <Button variant="primary" icon={Play} onClick={() => void resume(selected)} disabled={startBlocked !== null} title={startBlocked ?? undefined}>
                  Start a new shell
                </Button>
              }
            />
          ) : selectedId && listed ? (
            <StateView title="Session not found" action={<Button onClick={() => select(null)}>Back to sessions</Button>}>
              It may have been deleted.
            </StateView>
          ) : (
            <StateView
              icon={SquareTerminal}
              title="Select a session"
              action={
                <Button variant="primary" icon={Play} onClick={() => setNewOpen(true)} disabled={startBlocked !== null} title={startBlocked ?? undefined}>
                  New session
                </Button>
              }
            />
          )}
        </div>
      </section>
      <NewSessionDialog
        open={newOpen}
        onClose={() => setNewOpen(false)}
        onCreated={adopt}
        workspaces={workspaces}
        models={models}
        noModel={noModel}
        size={size}
        defaultHarness={filter.harness}
      />
      <ConfirmDialog
        open={deleting !== null}
        onClose={() => setDeleting(null)}
        title={deleting ? `Delete ${sessionName(deleting)}?` : "Delete session?"}
        confirmLabel="Delete session"
        failed="Session not deleted"
        onConfirm={confirmDelete}
        body={deleteCopy.body}
        extra={
          deleteCopy.purgeLabel ? (
            <label className="flex items-center gap-2 text-sm text-base-content">
              <input type="checkbox" checked={purge} onChange={(e) => setPurge(e.target.checked)} className="checkbox checkbox-error checkbox-sm" />
              {deleteCopy.purgeLabel}
            </label>
          ) : undefined
        }
      />
    </div>
  );
}
