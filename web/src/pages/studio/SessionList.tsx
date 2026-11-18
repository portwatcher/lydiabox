import { CircleDot, History, Plus, Radio, Trash2 } from "lucide-react";
import { SESSION_HARNESSES } from "../../api/types";
import type { HarnessID, Session, SessionStatus } from "../../api/types";
import { ErrorView, SkeletonList, StateView } from "../../components/StateView";
import { Badge, Button, IconButton, Select } from "../../components/ui";
import type { Tone } from "../../components/ui";
import { useNow } from "../../hooks/useResource";
import { formatCompact, formatUsd, timeAgo } from "../../lib/format";
import { harnessLabel, harnessLabels, sessionName } from "../../lib/harness";

export interface SessionFilter {
  harness: HarnessID | "";
  workspace: string;
}

const statusInfo: Record<SessionStatus, { label: string; tone: Tone }> = {
  running: { label: "Running", tone: "success" },
  idle: { label: "Idle", tone: "neutral" },
  exited: { label: "Exited", tone: "neutral" },
};

interface SessionListProps {
  sessions: Session[] | undefined;
  /** Time of the last scan when the list is cached ("" when unknown); null when it is live. */
  cachedAt?: string | null;
  error: unknown;
  loading: boolean;
  onRetry: () => void;
  filter: SessionFilter;
  onHarness: (harness: HarnessID | "") => void;
  onWorkspace: (workspace: string) => void;
  /** Harnesses of the sessions seen so far. */
  harnesses: HarnessID[];
  /** Folders of the sessions seen so far, sorted. */
  workspaces: string[];
  selectedId: string | null;
  onSelect: (session: Session) => void;
  onDelete: (session: Session) => void;
  onNew: () => void;
  /** Why a new session cannot start now (the provider refuses a start), or null. */
  newBlocked?: string | null;
}

function UsageLine({ session }: { session: Session }) {
  if (!session.usage) {
    return null;
  }
  const { inputTokens, outputTokens, costUsd } = session.usage;
  return (
    <span className="tabular-nums">
      {formatCompact(inputTokens)} in · {formatCompact(outputTokens)} out
      {costUsd > 0 ? ` · ${formatUsd(costUsd)}` : ""}
    </span>
  );
}

function SessionRow({ session, selected, onSelect, onDelete, now }: { session: Session; selected: boolean; onSelect: () => void; onDelete: () => void; now: number }) {
  const info = statusInfo[session.status];
  return (
    <li className="group relative">
      <button
        type="button"
        onClick={onSelect}
        aria-current={selected ? "true" : undefined}
        className="flex w-full flex-col items-stretch gap-1 py-2.5 pr-10 pl-3 text-left"
      >
        <span className="flex min-w-0 items-center gap-2">
          <span className="min-w-0 flex-1 truncate text-sm font-medium" title={sessionName(session)}>
            {sessionName(session)}
          </span>
          {session.live ? (
            <Badge tone="success" icon={Radio}>
              Live
            </Badge>
          ) : null}
        </span>
        <span className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 text-xs text-base-content/70">
          <Badge tone="accent">{harnessLabel(session.harness)}</Badge>
          {!session.live || session.status !== "running" ? (
            <Badge tone={info.tone} icon={CircleDot}>
              {info.label}
            </Badge>
          ) : null}
          <span className="tabular-nums">{timeAgo(session.updatedAt, now) ?? ""}</span>
        </span>
        <span className="truncate font-mono text-xs text-base-content/70" title={session.workspaceDir}>
          {session.workspaceDir}
        </span>
        <span className="flex flex-wrap gap-x-2 text-xs text-base-content/70">
          {session.model ? (
            <span className="truncate" title={session.model}>
              {session.model}
            </span>
          ) : null}
          <UsageLine session={session} />
        </span>
      </button>
      <IconButton
        icon={Trash2}
        tone="danger"
        label={`Delete ${sessionName(session)}`}
        onClick={onDelete}
        className="absolute top-1.5 right-1.5 opacity-100 md:opacity-0 md:group-focus-within:opacity-100 md:group-hover:opacity-100"
      />
    </li>
  );
}

export function SessionList(props: SessionListProps) {
  const { sessions, cachedAt = null, error, loading, onRetry, filter, onHarness, onWorkspace, harnesses, workspaces, selectedId, onSelect, onDelete, onNew, newBlocked = null } = props;
  const now = useNow(15_000);
  // A menu shows only the values that sessions have; the selected value stays
  // in it even if no listed session has it now.
  const harnessOptions = SESSION_HARNESSES.filter((h) => h === filter.harness || harnesses.includes(h));
  const folders = filter.workspace !== "" && !workspaces.includes(filter.workspace) ? [...workspaces, filter.workspace].sort() : workspaces;
  const filtered = filter.harness !== "" || filter.workspace !== "";

  let body;
  if (loading && !sessions) {
    body = <SkeletonList label="Loading sessions…" />;
  } else if (error && !sessions) {
    body = <ErrorView error={error} subsystem="Sessions" failed="Sessions not loaded" onRetry={onRetry} />;
  } else if (sessions && sessions.length === 0) {
    body = filtered ? (
      <StateView
        title="No sessions match these filters"
        action={
          <Button
            size="sm"
            onClick={() => {
              onHarness("");
              onWorkspace("");
            }}
          >
            Clear filters
          </Button>
        }
      />
    ) : (
      <StateView
        title="No sessions yet"
        action={
          <Button size="sm" variant="primary" icon={Plus} onClick={onNew} disabled={newBlocked !== null} title={newBlocked ?? undefined}>
            New session
          </Button>
        }
      >
        Sessions from every harness show here, even while the VM is off.
      </StateView>
    );
  } else {
    body = (
      <ul aria-label="Sessions" className="menu w-full gap-0.5 p-2">
        {(sessions ?? []).map((s) => (
          <SessionRow
            key={s.id}
            session={s}
            now={now}
            selected={s.id === selectedId}
            onSelect={() => onSelect(s)}
            onDelete={() => onDelete(s)}
          />
        ))}
      </ul>
    );
  }

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="flex flex-col gap-2 border-b border-base-content/10 p-3">
        <div className="flex items-center justify-between gap-2">
          <h2 className="text-sm font-semibold">Sessions</h2>
          {/* Without a scan and without sessions there is nothing that could be out of date. */}
          {cachedAt !== null && (cachedAt !== "" || (sessions?.length ?? 0) > 0) ? (
            <span
              className="mr-auto inline-flex items-center gap-1 text-xs text-base-content/70 tabular-nums"
              title="The machine is paused or stopped. This list is from its last scan."
            >
              <History aria-hidden className="size-3.5" />
              {cachedAt ? `Cached ${timeAgo(cachedAt, now) ?? ""}` : "Cached"}
            </span>
          ) : null}
          <Button size="sm" variant="primary" icon={Plus} onClick={onNew} disabled={newBlocked !== null} title={newBlocked ?? undefined}>
            New
          </Button>
        </div>
        {/* With one value, a filter cannot narrow the list; a set filter stays visible so it can be cleared. */}
        <div className="grid grid-cols-1 gap-2 empty:hidden">
          {harnessOptions.length > 1 || filter.harness !== "" ? (
            <Select
              aria-label="Harness"
              value={filter.harness}
              onChange={(e) => {
                const value = SESSION_HARNESSES.find((h) => h === e.target.value) ?? "";
                onHarness(value);
              }}
            >
              <option value="">All harnesses</option>
              {harnessOptions.map((h) => (
                <option key={h} value={h}>
                  {harnessLabels[h]}
                </option>
              ))}
            </Select>
          ) : null}
          {folders.length > 1 || filter.workspace !== "" ? (
            <Select aria-label="Folder" value={filter.workspace} onChange={(e) => onWorkspace(e.target.value)}>
              <option value="">All folders</option>
              {folders.map((w) => (
                <option key={w} value={w}>
                  {w}
                </option>
              ))}
            </Select>
          ) : null}
        </div>
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto">{body}</div>
    </div>
  );
}
