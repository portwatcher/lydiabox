import { ChevronRight, RefreshCw, Sparkles } from "lucide-react";
import { useState } from "react";
import type { GatewayStatus, Provider, Subscription } from "../../api/types";
import { gatewayHealth, healthSummary, providerHealth } from "../../components/product/health";
import type { Health } from "../../components/product/health";
import { HealthLabel, Mark } from "../../components/product/Marks";
import { IconButton, Panel, cx } from "../../components/ui";
import { useNow } from "../../hooks/useResource";
import { count, formatAgo } from "../../lib/format";

export interface StatusRow {
  id: string;
  name: string;
  detail: string;
  health: Health;
  models: number | null;
  error: string;
}

/** The gateway, then subscriptions with an account, then API-key providers. */
export function statusRows(gateway: GatewayStatus | null, gatewayError: boolean, subscriptions: Subscription[], providers: Provider[]): StatusRow[] {
  const rows: StatusRow[] = [
    {
      id: "gateway",
      name: "Model gateway",
      detail: gateway ? [gateway.addr, gateway.version ? `v${gateway.version}` : ""].filter(Boolean).join(" · ") : "",
      health: gatewayError ? "outage" : gatewayHealth(gateway?.status ?? null),
      models: null,
      error: gatewayError ? "The gateway does not answer." : "",
    },
  ];
  const byId = new Map(providers.map((p) => [p.id, p]));
  for (const s of subscriptions) {
    if (s.accounts.length === 0) {
      continue;
    }
    const p = byId.get(s.providerId);
    const relogin = s.accounts.filter((a) => a.needsSignIn).map((a) => a.user);
    rows.push({
      id: `sub-${s.kind}`,
      name: s.name,
      detail: s.accounts.map((a) => a.user).join(", "),
      health: relogin.length > 0 ? "outage" : p ? providerHealth(p) : s.enabled ? "operational" : "off",
      models: s.models.length,
      error: relogin.length > 0 ? `Sign in again: ${relogin.join(", ")}` : (p?.lastError ?? ""),
    });
  }
  for (const p of providers) {
    if (p.subscription) {
      continue;
    }
    rows.push({ id: p.id, name: p.name, detail: p.models.join(", "), health: providerHealth(p), models: p.models.length, error: p.lastError });
  }
  return rows;
}

function Row({ row }: { row: StatusRow }) {
  const [open, setOpen] = useState(false);
  const errorId = `status-error-${row.id}`;
  return (
    <li className="border-b border-base-content/10 last:border-b-0">
      <div className="grid grid-cols-[auto_minmax(0,1fr)_auto] items-center gap-x-3 gap-y-1 px-4 py-2.5 sm:grid-cols-[auto_minmax(0,1fr)_7rem_6rem]">
        {row.id === "gateway" ? <Mark name={row.name} icon={Sparkles} size="sm" /> : <Mark name={row.name} size="sm" />}
        <div className="flex min-w-0 flex-col">
          <span className="truncate text-[13px] font-medium">{row.name}</span>
          {row.detail ? (
            <span className="truncate text-xs text-base-content/70" title={row.detail}>
              {row.detail}
            </span>
          ) : null}
        </div>
        {row.error ? (
          <button
            type="button"
            aria-expanded={open}
            aria-controls={errorId}
            onClick={() => setOpen((o) => !o)}
            className="inline-flex items-center gap-0.5 justify-self-start rounded-field"
          >
            <HealthLabel health={row.health} />
            <ChevronRight aria-hidden className={cx("size-3.5 text-base-content/70 motion-safe:transition-transform", open && "rotate-90")} />
          </button>
        ) : (
          <HealthLabel health={row.health} />
        )}
        <span className="hidden text-right text-xs text-base-content/70 tabular-nums sm:block">
          {row.models === null ? "" : count(row.models, { one: "# model", other: "# models" })}
        </span>
      </div>
      {row.error && open ? (
        <p id={errorId} className="mx-4 mb-2.5 rounded-field bg-error/10 px-3 py-2 text-xs break-words text-error motion-safe:animate-fade-in">
          {row.error}
        </p>
      ) : null}
    </li>
  );
}

/** One line per upstream of the gateway, with its health and model count. */
export function ProviderStatus({ rows, checkedAt, refreshing, onRefresh }: { rows: StatusRow[]; checkedAt: number | null; refreshing: boolean; onRefresh: () => void }) {
  const now = useNow(5000);
  return (
    <Panel
      title={
        <span className="flex flex-wrap items-baseline gap-x-2">
          Status
          <span aria-live="polite" className="text-xs font-normal text-base-content/70">
            {healthSummary(rows.map((r) => r.health))}
          </span>
        </span>
      }
      actions={
        <>
          {checkedAt !== null ? <span className="text-xs text-base-content/70 tabular-nums">{formatAgo(Math.max(0, now - checkedAt))}</span> : null}
          <IconButton icon={RefreshCw} label="Check again" disabled={refreshing} className={refreshing ? "motion-safe:[&_svg]:animate-spin" : ""} onClick={onRefresh} />
        </>
      }
    >
      <ul>
        {rows.map((r) => (
          <Row key={r.id} row={r} />
        ))}
      </ul>
    </Panel>
  );
}
