import { CircleAlert, CircleCheck, CircleDashed, ClipboardPaste, GitBranch, HardDrive, KeyRound, Link2, MessageSquare, NotebookText, Unplug } from "lucide-react";
import type { LucideIcon } from "lucide-react";
import type { AccountStatus, Connector, ConnectorAccount, ConnectorProvider } from "../../api/types";
import { Badge, Button } from "../../components/ui";
import type { Tone } from "../../components/ui";
import { formatDateTime, isZeroTime } from "../../lib/format";

const providerIcons: Record<ConnectorProvider, LucideIcon> = {
  github: GitBranch,
  gdrive: HardDrive,
  notion: NotebookText,
  slack: MessageSquare,
};

const accountStatus: Record<AccountStatus, { label: string; tone: Tone; icon: LucideIcon }> = {
  ok: { label: "Connected", tone: "success", icon: CircleCheck },
  expired: { label: "Expired", tone: "warning", icon: CircleAlert },
  revoked: { label: "Revoked", tone: "danger", icon: CircleAlert },
};

interface ConnectorCardProps {
  connector: Connector;
  connecting: boolean;
  onConnect: () => void;
  onConfigure: () => void;
  onPasteToken: () => void;
  onDisconnect: (account: ConnectorAccount) => void;
}

function AccountRow({ account, onDisconnect }: { account: ConnectorAccount; onDisconnect: () => void }) {
  const status = accountStatus[account.status];
  return (
    <li className="rounded-field flex flex-col gap-2 bg-base-200 px-3 py-2.5">
      <div className="flex flex-wrap items-center gap-2">
        <span className="min-w-0 flex-1 truncate text-sm font-medium" title={account.accountLabel}>
          {account.accountLabel}
        </span>
        <Badge tone={status.tone} icon={status.icon}>
          {status.label}
        </Badge>
        <Button size="sm" variant="ghost" icon={Unplug} onClick={onDisconnect} aria-label={`Disconnect ${account.accountLabel}`}>
          Disconnect
        </Button>
      </div>
      <div className="flex flex-wrap gap-x-3 gap-y-1 text-xs text-base-content/70">
        <span>{account.authKind === "oauth2" ? "OAuth" : "Token"}</span>
        {!isZeroTime(account.expiresAt) ? <span>Expires {formatDateTime(account.expiresAt)}</span> : null}
        {account.scopes.length > 0 ? (
          <span className="font-mono break-all">{account.scopes.join(" ")}</span>
        ) : null}
      </div>
    </li>
  );
}

export function ConnectorCard({ connector, connecting, onConnect, onConfigure, onPasteToken, onDisconnect }: ConnectorCardProps) {
  const Icon = providerIcons[connector.provider] ?? Link2;
  const connected = connector.accounts.length > 0;
  const canOAuth = connector.supportsOAuth && connector.oauthConfigured;

  return (
    <li className="card card-sm border border-base-content/10 bg-base-100">
      <div className="card-body gap-4">
        <div className="flex items-center gap-3">
          <span className="rounded-field flex size-9 shrink-0 items-center justify-center bg-neutral">
            <Icon aria-hidden className="size-5" />
          </span>
          <h3 className="min-w-0 flex-1 truncate text-sm font-semibold">{connector.displayName}</h3>
          {connected ? null : (
            <Badge icon={CircleDashed}>Not connected</Badge>
          )}
        </div>
        {connected ? (
          <ul className="flex flex-col gap-2">
            {connector.accounts.map((a) => (
              <AccountRow key={a.accountLabel} account={a} onDisconnect={() => onDisconnect(a)} />
            ))}
          </ul>
        ) : null}
        <div className="mt-auto flex flex-wrap gap-2">
          {connector.supportsOAuth ? (
            <Button
              variant={connected || !canOAuth ? "secondary" : "primary"}
              icon={Link2}
              busy={connecting}
              disabled={!canOAuth}
              title={canOAuth ? undefined : "Configure an OAuth client ID first"}
              onClick={onConnect}
            >
              {connected ? "Add account" : "Connect"}
            </Button>
          ) : null}
          {connector.supportsOAuth ? (
            <Button variant={!connected && !connector.oauthConfigured && !connector.supportsToken ? "primary" : "ghost"} icon={KeyRound} onClick={onConfigure}>
              {connector.oauthConfigured ? "OAuth client" : "Configure client ID"}
            </Button>
          ) : null}
          {connector.supportsToken ? (
            <Button variant="ghost" icon={ClipboardPaste} onClick={onPasteToken}>
              Paste token
            </Button>
          ) : null}
        </div>
      </div>
    </li>
  );
}
