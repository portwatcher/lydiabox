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
import { harnessLabel, sessionName } from "../lib/harness";
import { NewSessionDialog } from "./studio/NewSessionDialog";
import { SessionList } from "./studio/SessionList";
import type { SessionFilter } from "./studio/SessionList";
import { TerminalView } from "./studio/TerminalView";

const CELL = { width: 7.8, height: 17 };

export default function StudioPage() {
  const toast = useToast();
  const [params, setParams] = useSearchParams();
  const selectedId = params.get("session");
  const [filter, setFilter] = useState<SessionFilter>({ harness: "", workspace: "" });
  const [workspaceInput, setWorkspaceInput] = useState("");
  const [workspaces, setWorkspaces] = useState<string[]>([]);
  const [room, setRoom] = useState<{ key: number; id: string } | null>(null);
  const [newOpen, setNewOpen] = useState(false);
  const [deleting, setDeleting] = useState<Session | null>(null);
  const [purge, setPurge] = useState(false);
  const termSize = useRef<{ cols: number; rows: number } | null>(null);
  const centerRef = useRef<HTMLDivElement>(null);
  const { models } = useModelOptions();

  useEffect(() => {
    const id = setTimeout(() => setFilter((f) => ({ ...f, workspace: workspaceInput.trim() })), 300);
    return () => clearTimeout(id);
  }, [workspaceInput]);

  const loader = useCallback((signal: AbortSignal) => sessions.list(filter, signal).then((r) => r.sessions), [filter]);
  const list = useResource(loader, { pollMs: 10_000 });

  useEffect(() => {
    if (!list.data) {
      return;
    }
    const seen = list.data.map((s) => s.workspaceDir);
    setWorkspaces((prev) => {
      const merged = Array.from(new Set([...prev, ...seen])).sort();
      return merged.length === prev.length ? prev : merged;
    });
  }, [list.data]);

  const [known, setKnown] = useState<Session | null>(null);
  const selected = useMemo(
    () => list.data?.find((s) => s.id === selectedId) ?? (known && known.id === selectedId ? known : null),
    [list.data, selectedId, known],
  );

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

  const attachedId = room?.id ?? null;
  const showTerminal = room !== null && attachedId === selectedId;

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
        model: session.model || undefined,
        resumeNativeId: session.harness === "shell" ? undefined : session.nativeId,
        cols,
        rows,
      });
      adopt(created);
    } catch (err) {
      toast.error(describeError(err, "Session not resumed"));
    }
  };

  const confirmDelete = async () => {
    if (!deleting) {
      return;
    }
    await sessions.remove(deleting.id, purge);
    if (deleting.id === selectedId) {
      select(null);
    }
    if (deleting.id === attachedId) {
      setRoom(null);
    }
    toast.success(`${sessionName(deleting)} deleted`);
    list.reload();
  };

  return (
    <div className="flex h-full min-h-0">
      <aside
        className={cx(
          "w-full shrink-0 border-r border-border-subtle bg-bg-subtle md:flex md:w-80 md:flex-col",
          selectedId ? "hidden" : "flex flex-col",
        )}
      >
        <SessionList
          sessions={list.data}
          error={list.error}
          loading={list.loading}
          onRetry={list.reload}
          filter={filter}
          workspaceInput={workspaceInput}
          onHarness={(harness: HarnessID | "") => setFilter((f) => ({ ...f, harness }))}
          onWorkspaceInput={setWorkspaceInput}
          workspaces={workspaces}
          selectedId={selectedId}
          onSelect={(s) => select(s.id)}
          onDelete={(s) => {
            setPurge(false);
            setDeleting(s);
          }}
          onNew={() => setNewOpen(true)}
        />
      </aside>
      <section ref={centerRef} className={cx("min-w-0 flex-1 flex-col", selectedId ? "flex" : "hidden md:flex")}>
        {selectedId ? (
          <header className="flex min-h-12 flex-wrap items-center gap-2 border-b border-border-subtle px-3 py-2">
            <Button size="sm" variant="ghost" icon={ArrowLeft} className="md:hidden" onClick={() => select(null)}>
              Sessions
            </Button>
            <h1 className="min-w-0 flex-1 truncate text-sm font-semibold" title={selected ? sessionName(selected) : selectedId}>
              {selected ? sessionName(selected) : selectedId}
            </h1>
            {selected ? <Badge tone="accent">{harnessLabel(selected.harness)}</Badge> : null}
            {selected?.model ? <Badge>{selected.model}</Badge> : null}
          </header>
        ) : null}
        <div className="min-h-0 flex-1">
          {showTerminal && room ? (
            <TerminalView
              key={room.key}
              sessionId={room.id}
              onExit={() => list.reload()}
              onSize={(cols, rows) => {
                termSize.current = { cols, rows };
              }}
            />
          ) : selected ? (
            <StateView
              icon={SquareTerminal}
              title={selected.status === "exited" ? "This session has ended" : "This session is not running"}
              action={
                <Button variant="primary" icon={Play} onClick={() => void resume(selected)}>
                  {selected.harness === "shell" ? "Start a new shell" : "Resume session"}
                </Button>
              }
            >
              {selected.harness === "shell" ? null : "Resuming starts the harness in the VM and wakes the VM if needed."}
            </StateView>
          ) : selectedId && list.data ? (
            <StateView title="Session not found" action={<Button onClick={() => select(null)}>Back to sessions</Button>}>
              It may have been deleted.
            </StateView>
          ) : (
            <StateView icon={SquareTerminal} title="Select a session" action={<Button variant="primary" icon={Play} onClick={() => setNewOpen(true)}>New session</Button>} />
          )}
        </div>
      </section>
      <NewSessionDialog
        open={newOpen}
        onClose={() => setNewOpen(false)}
        onCreated={adopt}
        workspaces={workspaces}
        models={models}
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
        body="The terminal stops and LydiaBox forgets this session."
        extra={
          <label className="flex items-center gap-2 text-sm text-fg">
            <input type="checkbox" checked={purge} onChange={(e) => setPurge(e.target.checked)} className="size-4 accent-danger-solid" />
            Also delete the harness transcript files (cannot be undone)
          </label>
        }
      />
    </div>
  );
}
