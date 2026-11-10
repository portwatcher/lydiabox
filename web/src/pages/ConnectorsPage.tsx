import { useCallback, useEffect, useState } from "react";
import { useSearchParams } from "react-router";
import { describeError } from "../api/errors";
import { connectors } from "../api/domains";
import type { Connector, ConnectorAccount, ConnectorProvider } from "../api/types";
import { ConfirmDialog } from "../components/Dialog";
import { ErrorView, LoadingView } from "../components/StateView";
import { useToast } from "../components/Toast";
import { useResource } from "../hooks/useResource";
import { AuditLog } from "./connectors/AuditLog";
import { ConnectorCard } from "./connectors/ConnectorCard";
import { ClientDrawer, TokenDrawer } from "./connectors/ConnectorDrawers";

const fallbackNames: Record<ConnectorProvider, string> = {
  github: "GitHub",
  gdrive: "Google Drive",
  notion: "Notion",
  slack: "Slack",
};

const oauthErrors: Record<string, string> = {
  access_denied: "access was denied on the provider's page",
  invalid_state: "the sign-in link expired or was opened in another browser",
  invalid_request: "the provider sent an incomplete answer",
  not_configured: "no OAuth client is configured",
  exchange_failed: "the provider did not issue a token",
  identity_failed: "the provider did not confirm the account",
  server_error: "LydiaBox could not store the account",
};

export default function ConnectorsPage() {
  const toast = useToast();
  const [params, setParams] = useSearchParams();
  const list = useResource(useCallback((s: AbortSignal) => connectors.list(s).then((r) => r.connectors), []), { pollMs: 30_000 });
  const [configuring, setConfiguring] = useState<Connector | null>(null);
  const [pasting, setPasting] = useState<Connector | null>(null);
  const [disconnecting, setDisconnecting] = useState<{ connector: Connector; account: ConnectorAccount } | null>(null);
  const [connecting, setConnecting] = useState<ConnectorProvider | null>(null);
  const { mutate } = list;

  const names: Record<string, string> = { ...fallbackNames };
  for (const c of list.data ?? []) {
    names[c.provider] = c.displayName;
  }

  // The OAuth callback redirects back with ?connected=<provider> or ?error=<code>&provider=<provider>.
  useEffect(() => {
    const connected = params.get("connected");
    const error = params.get("error");
    if (!connected && !error) {
      return;
    }
    const provider = connected ?? params.get("provider") ?? "";
    const name = Object.entries(fallbackNames).find(([key]) => key === provider)?.[1] ?? provider;
    if (connected) {
      toast.success(`${name} connected`);
    } else if (error) {
      const reason = oauthErrors[error] ?? `the provider returned "${error}"`;
      toast.error(`${name || "Connector"} not connected: ${reason}. Try Connect again.`);
    }
    setParams(new URLSearchParams(), { replace: true });
  }, [params, setParams, toast]);

  const replace = useCallback(
    (saved: Connector) => mutate((all) => (all ?? []).map((c) => (c.provider === saved.provider ? saved : c))),
    [mutate],
  );

  const connect = async (connector: Connector) => {
    setConnecting(connector.provider);
    try {
      const res = await connectors.authorize(connector.provider, { returnTo: "/connectors" });
      window.location.assign(res.authorizeUrl);
    } catch (err) {
      toast.error(describeError(err, `${connector.displayName} not connected`));
      setConnecting(null);
    }
  };

  let cards;
  if (list.loading && !list.data) {
    cards = <LoadingView label="Loading connectors…" />;
  } else if (list.error && !list.data) {
    cards = <ErrorView error={list.error} subsystem="Connectors" failed="Connectors not loaded" onRetry={list.reload} />;
  } else {
    cards = (
      <ul className="grid gap-3 sm:grid-cols-2">
        {(list.data ?? []).map((c) => (
          <ConnectorCard
            key={c.provider}
            connector={c}
            connecting={connecting === c.provider}
            onConnect={() => void connect(c)}
            onConfigure={() => setConfiguring(c)}
            onPasteToken={() => setPasting(c)}
            onDisconnect={(account) => setDisconnecting({ connector: c, account })}
          />
        ))}
      </ul>
    );
  }

  return (
    <div className="h-full overflow-y-auto">
      <div className="mx-auto flex max-w-6xl flex-col gap-4 p-4 sm:p-6">
        <h1 className="text-lg font-semibold tracking-tight">Connectors</h1>
        {cards}
        {list.data ? <AuditLog names={names} /> : null}
      </div>
      <ClientDrawer
        connector={configuring}
        onClose={() => setConfiguring(null)}
        onSaved={(saved) => {
          replace(saved);
          toast.success(`${saved.displayName} OAuth client saved`);
        }}
      />
      <TokenDrawer
        connector={pasting}
        onClose={() => setPasting(null)}
        onSaved={(saved) => {
          replace(saved);
          toast.success(`${saved.displayName} connected`);
        }}
      />
      <ConfirmDialog
        open={disconnecting !== null}
        onClose={() => setDisconnecting(null)}
        title={disconnecting ? `Disconnect ${disconnecting.account.accountLabel}?` : "Disconnect?"}
        confirmLabel="Disconnect"
        failed="Not disconnected"
        body={
          disconnecting
            ? `LydiaBox deletes the ${disconnecting.connector.displayName} credentials and revokes them upstream when the provider allows it. Agents lose access right away.`
            : null
        }
        onConfirm={async () => {
          if (!disconnecting) {
            return;
          }
          const { connector, account } = disconnecting;
          await connectors.disconnect(connector.provider, account.accountLabel);
          mutate((all) =>
            (all ?? []).map((c) =>
              c.provider === connector.provider ? { ...c, accounts: c.accounts.filter((a) => a.accountLabel !== account.accountLabel) } : c,
            ),
          );
          toast.success(`${account.accountLabel} disconnected`);
        }}
      />
    </div>
  );
}
