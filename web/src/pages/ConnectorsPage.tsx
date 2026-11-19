import { useCallback, useEffect, useRef, useState } from "react";
import { useSearchParams } from "react-router";
import { describeError } from "../api/errors";
import { connectors } from "../api/domains";
import { auth, getRemoteAccess } from "../api/system";
import type { Connector, ConnectorAccount, ConnectorProvider } from "../api/types";
import { ErrorView, SkeletonList } from "../components/StateView";
import { useToast } from "../components/Toast";
import { Panel } from "../components/ui";
import { useResource } from "../hooks/useResource";
import { AuditLog } from "./connectors/AuditLog";
import { ConnectorCard } from "./connectors/ConnectorCard";
import { ClientDrawer, TokenDrawer } from "./connectors/ConnectorDrawers";
import { RemoteAccess, showRemoteAccess } from "./connectors/RemoteAccess";

const fallbackNames: Record<ConnectorProvider, string> = {
  github: "GitHub",
  gdrive: "Google Drive",
  notion: "Notion",
  slack: "Slack",
};

const oauthErrors: Record<string, string> = {
  access_denied: "Access was denied on the provider's page.",
  invalid_state: "The sign-in link expired or was opened in another browser.",
  invalid_request: "The provider sent an incomplete answer.",
  not_configured: "No OAuth client is configured.",
  exchange_failed: "The provider did not issue a token.",
  identity_failed: "The provider did not confirm the account.",
  server_error: "LydiaBox could not store the account.",
};

export default function ConnectorsPage() {
  const toast = useToast();
  const [params, setParams] = useSearchParams();
  const list = useResource(useCallback((s: AbortSignal) => connectors.list(s).then((r) => r.connectors), []), { pollMs: 30_000 });
  const remote = useResource(useCallback((s: AbortSignal) => getRemoteAccess(s), []));
  const session = useResource(useCallback(() => auth.session(), []));
  const [configuring, setConfiguring] = useState<Connector | null>(null);
  const [pasting, setPasting] = useState<Connector | null>(null);
  const [connecting, setConnecting] = useState<ConnectorProvider | null>(null);
  const [errors, setErrors] = useState<Partial<Record<ConnectorProvider, string>>>({});
  const [auditProvider, setAuditProvider] = useState<ConnectorProvider | "">("");
  const auditRef = useRef<HTMLDivElement>(null);
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
    const known = Object.keys(fallbackNames).find((key): key is ConnectorProvider => key === provider);
    const name = known ? fallbackNames[known] : provider;
    if (connected) {
      toast.success(`${name} connected`);
    } else if (error) {
      const reason = oauthErrors[error] ?? `The provider returned "${error}".`;
      if (known) {
        setErrors((all) => ({ ...all, [known]: reason }));
      } else {
        toast.error(`${name || "Connector"} not connected. ${reason}`);
      }
    }
    setParams(new URLSearchParams(), { replace: true });
  }, [params, setParams, toast]);

  const replace = useCallback(
    (saved: Connector) => mutate((all) => (all ?? []).map((c) => (c.provider === saved.provider ? saved : c))),
    [mutate],
  );

  const connect = async (connector: Connector) => {
    setConnecting(connector.provider);
    setErrors((all) => ({ ...all, [connector.provider]: undefined }));
    try {
      const res = await connectors.authorize(connector.provider, { returnTo: "/connectors" });
      window.location.assign(res.authorizeUrl);
    } catch (err) {
      setErrors((all) => ({ ...all, [connector.provider]: describeError(err, "Not connected") }));
      setConnecting(null);
    }
  };

  const disconnect = async (connector: Connector, account: ConnectorAccount) => {
    try {
      await connectors.disconnect(connector.provider, account.accountLabel);
      mutate((all) =>
        (all ?? []).map((c) =>
          c.provider === connector.provider ? { ...c, accounts: c.accounts.filter((a) => a.accountLabel !== account.accountLabel) } : c,
        ),
      );
      toast.success(`${account.accountLabel} disconnected`);
    } catch (err) {
      toast.error(describeError(err, "Not disconnected"));
    }
  };

  const showAudit = (provider: ConnectorProvider) => {
    setAuditProvider(provider);
    auditRef.current?.scrollIntoView({ behavior: window.matchMedia("(prefers-reduced-motion: reduce)").matches ? "auto" : "smooth", block: "start" });
  };

  const all = list.data ?? [];
  const connectedCount = all.filter((c) => c.accounts.length > 0).length;
  let cards;
  if (list.loading && !list.data) {
    cards = <SkeletonList label="Loading connectors…" rows={4} className="flex flex-col p-4" />;
  } else if (list.error && !list.data) {
    cards = <ErrorView error={list.error} subsystem="Connectors" failed="Connectors not loaded" onRetry={list.reload} />;
  } else {
    cards = (
      <ul>
        {all.map((c) => (
          <ConnectorCard
            key={c.provider}
            connector={c}
            connecting={connecting === c.provider}
            error={errors[c.provider] ?? null}
            onConnect={() => void connect(c)}
            onConfigure={() => setConfiguring(c)}
            onPasteToken={() => setPasting(c)}
            onDisconnect={(account) => disconnect(c, account)}
            onAudit={() => showAudit(c.provider)}
          />
        ))}
      </ul>
    );
  }

  return (
    <div className="h-full overflow-y-auto">
      <div className="mx-auto flex max-w-3xl flex-col gap-4 p-4 sm:p-6">
        <h1 className="text-lg font-semibold tracking-tight">Connectors</h1>
        <Panel
          title="Apps"
          actions={
            list.data ? (
              <span className="badge badge-sm badge-soft font-medium text-base-content/70 tabular-nums">
                {connectedCount} of {all.length} connected
              </span>
            ) : null
          }
        >
          {cards}
        </Panel>
        {remote.data && showRemoteAccess(remote.data, session.data?.method === "access") ? <RemoteAccess status={remote.data} /> : null}
        <div ref={auditRef} className="scroll-mt-4">
          {list.data ? <AuditLog names={names} provider={auditProvider} onProvider={setAuditProvider} /> : null}
        </div>
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
          setErrors((all) => ({ ...all, [saved.provider]: undefined }));
          toast.success(`${saved.displayName} connected`);
        }}
      />
    </div>
  );
}
