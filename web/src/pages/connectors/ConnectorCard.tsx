import { Check, ChevronDown, CircleAlert, ClipboardPaste, GitBranch, HardDrive, History, KeyRound, Link2, MessageSquare, NotebookText, RotateCw, Settings2, Unplug } from "lucide-react";
import type { LucideIcon } from "lucide-react";
import { useState } from "react";
import type { AccountStatus, Connector, ConnectorAccount, ConnectorProvider } from "../../api/types";
import { Mark } from "../../components/product/Marks";
import { Badge, Button, cx } from "../../components/ui";
import type { Tone } from "../../components/ui";
import { count, formatDateTime, isZeroTime } from "../../lib/format";

export const providerIcons: Record<ConnectorProvider, LucideIcon> = {
  github: GitBranch,
  gdrive: HardDrive,
  notion: NotebookText,
  slack: MessageSquare,
};

const accountStatus: Record<AccountStatus, { label: string; tone: Tone }> = {
  ok: { label: "Connected", tone: "success" },
  expired: { label: "Expired", tone: "warning" },
  revoked: { label: "Revoked", tone: "danger" },
};

/** The state of a connector row; pure, so it is unit-tested. */
export type ConnectorState = "connecting" | "error" | "attention" | "connected" | "ready" | "setup";

export function connectorState(c: Connector, connecting: boolean, error: string | null): ConnectorState {
  if (connecting) {
    return "connecting";
  }
  if (error) {
    return "error";
  }
  if (c.accounts.length > 0) {
    return c.accounts.some((a) => a.status !== "ok") ? "attention" : "connected";
  }
  return (c.supportsOAuth && c.oauthConfigured) || c.supportsToken ? "ready" : "setup";
}

interface ConnectorRowProps {
  connector: Connector;
  connecting: boolean;
  /** Why the last OAuth sign-in failed, or null. */
  error: string | null;
  onConnect: () => void;
  onConfigure: () => void;
  onPasteToken: () => void;
  onDisconnect: (account: ConnectorAccount) => Promise<void>;
  onAudit: () => void;
}

function AccountDetails({ account, onDisconnect }: { account: ConnectorAccount; onDisconnect: () => Promise<void> }) {
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  const status = accountStatus[account.status];
  const disconnect = async () => {
    setBusy(true);
    try {
      await onDisconnect();
    } finally {
      setBusy(false);
      setConfirming(false);
    }
  };
  return (
    <li className="flex flex-col gap-2 py-2.5 first:pt-0 last:pb-0">
      <div className="flex flex-wrap items-center gap-2">
        <span className="min-w-0 flex-1 truncate text-[13px] font-medium" title={account.accountLabel}>
          {account.accountLabel}
        </span>
        <Badge tone={status.tone}>{status.label}</Badge>
        {confirming ? (
          <span className="flex items-center gap-1">
            <Button size="sm" variant="danger" busy={busy} autoFocus onClick={() => void disconnect()}>
              Disconnect
            </Button>
            <Button size="sm" variant="ghost" disabled={busy} onClick={() => setConfirming(false)}>
              Keep
            </Button>
          </span>
        ) : (
          <Button size="sm" variant="ghost" icon={Unplug} aria-label={`Disconnect ${account.accountLabel}`} onClick={() => setConfirming(true)}>
            Disconnect
          </Button>
        )}
      </div>
      {confirming ? <p className="text-xs text-base-content/70">Agents lose access at once; the provider revokes the credentials when it can.</p> : null}
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1.5 text-xs text-base-content/70">
        <span>{account.authKind === "oauth2" ? "OAuth" : "Token"}</span>
        {!isZeroTime(account.expiresAt) ? <span>Expires {formatDateTime(account.expiresAt)}</span> : null}
        {account.scopes.map((s) => (
          <code key={s} className="rounded-selector bg-base-300 px-1.5 py-0.5 text-[11px] text-base-content/80">
            {s}
          </code>
        ))}
      </div>
    </li>
  );
}

/** One connector: its state, the action that moves it forward, and its accounts when opened. */
export function ConnectorCard({ connector, connecting, error, onConnect, onConfigure, onPasteToken, onDisconnect, onAudit }: ConnectorRowProps) {
  const [open, setOpen] = useState(false);
  const state = connectorState(connector, connecting, error);
  const canOAuth = connector.supportsOAuth && connector.oauthConfigured;
  const detailsId = `connector-${connector.provider}`;
  const first = connector.accounts[0];
  const hasAccounts = connector.accounts.length > 0;

  let action;
  if (hasAccounts && state !== "connecting") {
    action = (
      <Button
        size="sm"
        variant="ghost"
        aria-expanded={open}
        aria-controls={detailsId}
        onClick={() => setOpen((o) => !o)}
        className={state === "attention" ? "text-warning" : "text-success"}
      >
        {state === "attention" ? <CircleAlert aria-hidden className="size-4" /> : <Check aria-hidden className="size-4" />}
        {state === "attention" ? "Needs attention" : "Connected"}
        <ChevronDown aria-hidden className={cx("size-3.5 text-base-content/70 motion-safe:transition-transform", open && "rotate-180")} />
      </Button>
    );
  } else if (canOAuth) {
    action = (
      <Button size="sm" variant="primary" icon={state === "error" ? RotateCw : Link2} busy={connecting} onClick={onConnect}>
        {state === "error" ? "Try again" : "Connect"}
      </Button>
    );
  } else if (connector.supportsToken) {
    action = (
      <Button size="sm" variant="primary" icon={ClipboardPaste} onClick={onPasteToken}>
        Paste token
      </Button>
    );
  } else {
    action = (
      <Button size="sm" icon={Settings2} onClick={onConfigure}>
        Set up
      </Button>
    );
  }

  return (
    <li className="relative border-b border-base-content/10 last:border-b-0">
      <div className="flex min-h-15 items-center gap-3 px-4 py-2.5">
        <Mark name={connector.displayName} icon={providerIcons[connector.provider] ?? Link2} />
        <div className="flex min-w-0 flex-1 flex-col">
          <h3 className="truncate text-[13px] font-semibold">{connector.displayName}</h3>
          <span aria-live="polite" className={cx("truncate text-xs", state === "error" ? "text-error" : "text-base-content/70")}>
            {state === "connecting"
              ? "Authorizing…"
              : state === "error"
                ? error
                : first
                  ? [first.accountLabel, connector.accounts.length > 1 ? `+${connector.accounts.length - 1}` : "", first.scopes.length > 0 ? count(first.scopes.length, { one: "# scope", other: "# scopes" }) : ""]
                      .filter(Boolean)
                      .join(" · ")
                  : state === "setup"
                    ? "Needs an OAuth client"
                    : "Not connected"}
          </span>
        </div>
        {action}
      </div>
      {connecting ? <progress aria-label={`Connecting ${connector.displayName}`} className="progress progress-info absolute inset-x-0 bottom-0 h-0.5" /> : null}
      {open && hasAccounts ? (
        <div id={detailsId} className="mx-4 mb-3 flex flex-col gap-3 rounded-box bg-base-200 p-3 motion-safe:animate-fade-in">
          <ul className="flex flex-col divide-y divide-base-content/10">
            {connector.accounts.map((a) => (
              <AccountDetails key={a.accountLabel} account={a} onDisconnect={() => onDisconnect(a)} />
            ))}
          </ul>
          <div className="flex flex-wrap gap-1 border-t border-base-content/10 pt-2.5">
            {connector.supportsOAuth ? (
              <Button size="sm" variant="ghost" icon={Link2} disabled={!canOAuth} onClick={onConnect}>
                Add account
              </Button>
            ) : null}
            {connector.supportsToken ? (
              <Button size="sm" variant="ghost" icon={ClipboardPaste} onClick={onPasteToken}>
                Paste token
              </Button>
            ) : null}
            {connector.supportsOAuth ? (
              <Button size="sm" variant="ghost" icon={KeyRound} onClick={onConfigure}>
                OAuth client
              </Button>
            ) : null}
            <Button size="sm" variant="ghost" icon={History} className="ms-auto" onClick={onAudit}>
              Activity
            </Button>
          </div>
        </div>
      ) : null}
      {!hasAccounts && state !== "connecting" && (connector.supportsOAuth || connector.supportsToken) && state !== "setup" ? (
        <div className="-mt-1 flex flex-wrap gap-1 ps-15 pe-4 pb-2">
          {connector.supportsOAuth ? (
            <Button size="sm" variant="ghost" icon={KeyRound} className="h-7 min-h-7 text-xs" onClick={onConfigure}>
              {connector.oauthConfigured ? "OAuth client" : "Configure client ID"}
            </Button>
          ) : null}
          {connector.supportsToken && canOAuth ? (
            <Button size="sm" variant="ghost" icon={ClipboardPaste} className="h-7 min-h-7 text-xs" onClick={onPasteToken}>
              Paste token
            </Button>
          ) : null}
        </div>
      ) : null}
    </li>
  );
}
