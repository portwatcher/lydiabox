import { ChevronDown, CircleAlert } from "lucide-react";
import { useCallback, useEffect, useState } from "react";
import { describeError } from "../../api/errors";
import { connectors } from "../../api/domains";
import { CONNECTOR_PROVIDERS } from "../../api/types";
import type { AuditEntry, ConnectorProvider } from "../../api/types";
import { ErrorView, LoadingView, StateView } from "../../components/StateView";
import { useToast } from "../../components/Toast";
import { Badge, Button, Panel, Select } from "../../components/ui";
import { useNow } from "../../hooks/useResource";
import { formatDateTime, timeAgo } from "../../lib/format";
import { appendOlder, auditDetail, auditKindLabel, isFailureKind, mergeNewer } from "./audit";

const PAGE = 50;
const LIVE_MS = 5000;

export function AuditLog({ names }: { names: Record<string, string> }) {
  const toast = useToast();
  const now = useNow(30_000);
  const [provider, setProvider] = useState<ConnectorProvider | "">("");
  const [entries, setEntries] = useState<AuditEntry[] | null>(null);
  const [nextBefore, setNextBefore] = useState<number | undefined>(undefined);
  const [error, setError] = useState<unknown>(null);
  const [loadingMore, setLoadingMore] = useState(false);
  const [generation, setGeneration] = useState(0);

  // First page, then poll the newest page and merge entries above the current top.
  useEffect(() => {
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout> | null = null;
    setEntries(null);
    setError(null);
    setNextBefore(undefined);

    const first = async () => {
      try {
        const page = await connectors.audit({ provider, limit: PAGE }, controller.signal);
        setEntries(page.entries);
        setNextBefore(page.nextBefore);
        timer = setTimeout(() => void poll(), LIVE_MS);
      } catch (err) {
        if (!controller.signal.aborted) {
          setError(err);
        }
      }
    };
    const poll = async () => {
      if (!document.hidden) {
        try {
          const page = await connectors.audit({ provider, limit: PAGE }, controller.signal);
          setEntries((current) => mergeNewer(current ?? [], page.entries));
        } catch {
          // A missed poll is retried on the next tick.
        }
      }
      if (!controller.signal.aborted) {
        timer = setTimeout(() => void poll(), LIVE_MS);
      }
    };
    void first();
    return () => {
      controller.abort();
      if (timer) {
        clearTimeout(timer);
      }
    };
  }, [provider, generation]);

  const loadOlder = useCallback(async () => {
    if (nextBefore === undefined) {
      return;
    }
    setLoadingMore(true);
    try {
      const page = await connectors.audit({ provider, before: nextBefore, limit: PAGE });
      setEntries((current) => appendOlder(current ?? [], page.entries));
      setNextBefore(page.nextBefore);
    } catch (err) {
      toast.error(describeError(err, "Older entries not loaded"));
    } finally {
      setLoadingMore(false);
    }
  }, [nextBefore, provider, toast]);

  let body;
  if (error) {
    body = <ErrorView error={error} subsystem="The audit log" failed="Audit log not loaded" onRetry={() => setGeneration((g) => g + 1)} />;
  } else if (entries === null) {
    body = <LoadingView label="Loading audit log…" />;
  } else if (entries.length === 0) {
    body = (
      <StateView title={provider ? `No activity for ${names[provider] ?? provider}` : "No credential activity yet"} className="min-h-32">
        Every token issued to the VM and every connector tool call is recorded here.
      </StateView>
    );
  } else {
    body = (
      <>
        <div className="overflow-x-auto">
          <table className="table table-sm" aria-label="Audit entries">
            <thead>
              <tr className="border-base-content/10 text-xs text-base-content/70">
                <th scope="col" className="w-44 px-4 font-medium">
                  Event
                </th>
                <th scope="col" className="font-medium">
                  Details
                </th>
                <th scope="col" className="px-4 text-right font-medium">
                  Time
                </th>
              </tr>
            </thead>
            <tbody>
              {entries.map((entry) => (
                <tr key={entry.id} className="border-base-content/10 align-top">
                  <td className="px-4">
                    {isFailureKind(entry.kind) ? (
                      <Badge tone="danger" icon={CircleAlert}>
                        {auditKindLabel(entry.kind)}
                      </Badge>
                    ) : (
                      <Badge>{auditKindLabel(entry.kind)}</Badge>
                    )}
                  </td>
                  <td className="min-w-48">
                    <dl className="flex min-w-0 flex-wrap gap-x-3 gap-y-1 text-xs">
                      {auditDetail(entry.detail).map(([key, value]) => (
                        <div key={key} className="flex min-w-0 gap-1">
                          <dt className="text-base-content/70">{key}</dt>
                          <dd className="font-mono break-all">{value}</dd>
                        </div>
                      ))}
                    </dl>
                  </td>
                  <td className="px-4 text-right whitespace-nowrap">
                    <time dateTime={entry.createdAt} title={formatDateTime(entry.createdAt)} className="text-xs text-base-content/70 tabular-nums">
                      {timeAgo(entry.createdAt, now)}
                    </time>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
        {nextBefore !== undefined ? (
          <div className="flex justify-center p-3">
            <Button size="sm" icon={ChevronDown} busy={loadingMore} onClick={() => void loadOlder()}>
              Load older entries
            </Button>
          </div>
        ) : null}
      </>
    );
  }

  return (
    <Panel
      title="Credential audit log"
      actions={
        <Select
          aria-label="Filter by connector"
          value={provider}
          onChange={(e) => setProvider(CONNECTOR_PROVIDERS.find((p) => p === e.target.value) ?? "")}
          className="select-sm w-44"
        >
          <option value="">All connectors</option>
          {CONNECTOR_PROVIDERS.map((p) => (
            <option key={p} value={p}>
              {names[p] ?? p}
            </option>
          ))}
        </Select>
      }
    >
      {body}
    </Panel>
  );
}
