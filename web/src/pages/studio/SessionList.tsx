import { CircleDot, Plus, Radio, Trash2 } from "lucide-react";
import { useId } from "react";
import { SESSION_HARNESSES } from "../../api/types";
import type { HarnessID, Session, SessionStatus } from "../../api/types";
import { ErrorView, LoadingView, StateView } from "../../components/StateView";
import { Badge, Button, IconButton, Select, TextInput, cx } from "../../components/ui";
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
  error: unknown;
  loading: boolean;
  onRetry: () => void;
  filter: SessionFilter;
  /** Raw workspace input; the parent debounces it into `filter`. */
  workspaceInput: string;
  onHarness: (harness: HarnessID | "") => void;
  onWorkspaceInput: (value: string) => void;
  workspaces: string[];
  selectedId: string | null;
  onSelect: (session: Session) => void;
  onDelete: (session: Session) => void;
  onNew: () => void;
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
    <li className={cx("group relative rounded-md", selected ? "bg-surface-active" : "hover:bg-surface-hover")}>
      <button
        type="button"
        onClick={onSelect}
        aria-current={selected ? "true" : undefined}
        className="flex w-full flex-col gap-1 rounded-md px-3 py-2.5 pr-10 text-left"
      >
        <span className="flex min-w-0 items-center gap-2">
          <span className="min-w-0 flex-1 truncate text-sm font-medium text-fg" title={sessionName(session)}>
            {sessionName(session)}
          </span>
          {session.live ? (
            <Badge tone="success" icon={Radio}>
              Live
            </Badge>
          ) : null}
        </span>
        <span className="flex min-w-0 flex-wrap items-center gap-x-2 gap-y-1 text-xs text-fg-muted">
          <Badge tone="accent">{harnessLabel(session.harness)}</Badge>
          {!session.live || session.status !== "running" ? (
            <Badge tone={info.tone} icon={CircleDot}>
              {info.label}
            </Badge>
          ) : null}
          <span className="tabular-nums">{timeAgo(session.updatedAt, now) ?? ""}</span>
        </span>
        <span className="truncate font-mono text-xs text-fg-muted" title={session.workspaceDir}>
          {session.workspaceDir}
        </span>
        <span className="flex flex-wrap gap-x-2 text-xs text-fg-muted">
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
  const { sessions, error, loading, onRetry, filter, workspaceInput, onHarness, onWorkspaceInput, workspaces, selectedId, onSelect, onDelete, onNew } = props;
  const now = useNow(15_000);
  const listId = useId();
  const filtered = filter.harness !== "" || filter.workspace !== "";

  let body;
  if (loading && !sessions) {
    body = <LoadingView label="Loading sessions…" />;
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
              onWorkspaceInput("");
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
          <Button size="sm" variant="primary" icon={Plus} onClick={onNew}>
            New session
          </Button>
        }
      >
        Sessions from every harness show here, even while the VM is off.
      </StateView>
    );
  } else {
    body = (
      <ul aria-label="Sessions" className="flex flex-col gap-0.5 p-2">
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
      <div className="flex flex-col gap-2 border-b border-border-subtle p-3">
        <div className="flex items-center justify-between gap-2">
          <h2 className="text-sm font-semibold">Sessions</h2>
          <Button size="sm" variant="primary" icon={Plus} onClick={onNew}>
            New
          </Button>
        </div>
        <div className="grid grid-cols-1 gap-2">
          <Select
            aria-label="Harness"
            value={filter.harness}
            onChange={(e) => {
              const value = SESSION_HARNESSES.find((h) => h === e.target.value) ?? "";
              onHarness(value);
            }}
          >
            <option value="">All harnesses</option>
            {SESSION_HARNESSES.map((h) => (
              <option key={h} value={h}>
                {harnessLabels[h]}
              </option>
            ))}
          </Select>
          <TextInput
            aria-label="Workspace"
            type="search"
            list={listId}
            value={workspaceInput}
            onChange={(e) => onWorkspaceInput(e.target.value)}
            placeholder="/workspace/app"
            spellCheck={false}
          />
          <datalist id={listId}>
            {workspaces.map((w) => (
              <option key={w} value={w} />
            ))}
          </datalist>
        </div>
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto">{body}</div>
    </div>
  );
}
