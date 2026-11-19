import { useCallback, useEffect, useMemo, useState } from "react";
import { availabilityOf } from "../api/errors";
import { models as modelsApi, sessions as sessionsApi } from "../api/domains";
import { ErrorView, LoadingView } from "../components/StateView";
import { Panel } from "../components/ui";
import { useResource } from "../hooks/useResource";
import { ModelCatalog } from "./models/ModelCatalog";
import { ProviderKeys } from "./models/ProviderKeys";
import { ProviderStatus, statusRows } from "./models/ProviderStatus";
import { RoutingEditor } from "./models/RoutingEditor";
import { SubscriptionCards } from "./models/SubscriptionCards";
import { UsagePanel } from "./models/UsagePanel";

function Pending({ title, error, failed, onRetry, label }: { title: string; error: unknown; failed: string; onRetry: () => void; label: string }) {
  return (
    <Panel title={title}>
      {error ? <ErrorView error={error} subsystem="The model gateway" failed={failed} onRetry={onRetry} /> : <LoadingView label={label} />}
    </Panel>
  );
}

export default function ModelsPage() {
  const gateway = useResource(useCallback((s: AbortSignal) => modelsApi.gateway(s), []), { pollMs: 10_000 });
  const missing = ["missing", "not_implemented"].includes(availabilityOf(gateway.error) ?? "");
  const enabled = !missing;
  const providers = useResource(useCallback((s: AbortSignal) => modelsApi.providers(s).then((r) => r.providers), []), { enabled });
  const list = useResource(useCallback((s: AbortSignal) => modelsApi.list(s).then((r) => r.models), []), { enabled });
  const routing = useResource(useCallback((s: AbortSignal) => modelsApi.routing(s).then((r) => r.groups), []), { enabled });
  const subscriptions = useResource(useCallback((s: AbortSignal) => modelsApi.subscriptions(s).then((r) => r.subscriptions), []), { enabled });
  const bindings = useResource(useCallback((s: AbortSignal) => modelsApi.bindings(s).then((r) => r.bindings), []), { enabled });
  const sessions = useResource(useCallback((s: AbortSignal) => sessionsApi.list({}, s).then((r) => r.sessions), []), { enabled });
  const { reload: reloadModels } = list;
  const { reload: reloadSubscriptions } = subscriptions;
  const { reload: reloadProviders, mutate: mutateProviders } = providers;
  const { reload: reloadGateway } = gateway;
  const { mutate: mutateRouting } = routing;
  const subscriptionsChanged = useCallback(() => {
    reloadSubscriptions();
    reloadProviders();
    reloadModels();
  }, [reloadSubscriptions, reloadProviders, reloadModels]);

  // The time of the last answer, for the status panel.
  const [checkedAt, setCheckedAt] = useState<number | null>(null);
  useEffect(() => {
    if (gateway.data || gateway.error) {
      setCheckedAt(Date.now());
    }
  }, [gateway.data, gateway.error]);
  const [refreshing, setRefreshing] = useState(false);
  useEffect(() => {
    if (!providers.loading) {
      setRefreshing(false);
    }
  }, [providers.loading, providers.data, providers.error]);
  const refresh = useCallback(() => {
    setRefreshing(true);
    reloadGateway();
    subscriptionsChanged();
  }, [reloadGateway, subscriptionsChanged]);

  const names = useMemo(() => {
    const out: Record<string, string> = {};
    for (const p of providers.data ?? []) {
      out[p.id] = p.name;
    }
    for (const s of subscriptions.data ?? []) {
      if (s.providerId) {
        out[s.providerId] = s.name;
      }
    }
    return out;
  }, [providers.data, subscriptions.data]);
  const order = useMemo(
    () => [...(subscriptions.data ?? []).map((s) => s.providerId).filter(Boolean), ...(providers.data ?? []).map((p) => p.id)],
    [providers.data, subscriptions.data],
  );

  if (gateway.loading && !gateway.data && !gateway.error) {
    return <LoadingView label="Loading models…" />;
  }
  if (missing) {
    return <ErrorView error={gateway.error} subsystem="Model management" failed="Models not loaded" onRetry={gateway.reload} />;
  }

  const rows = statusRows(gateway.data ?? null, Boolean(gateway.error) && !gateway.data, subscriptions.data ?? [], providers.data ?? []);

  return (
    <div className="h-full overflow-y-auto">
      <div className="mx-auto flex max-w-6xl flex-col gap-4 p-4 sm:p-6">
        <h1 className="text-lg font-semibold tracking-tight">Models</h1>

        <ProviderStatus rows={rows} checkedAt={checkedAt} refreshing={refreshing} onRefresh={refresh} />

        {subscriptions.data ? (
          <SubscriptionCards subscriptions={subscriptions.data} onChanged={subscriptionsChanged} />
        ) : (
          <Pending title="Subscriptions" error={subscriptions.error} failed="Subscriptions not loaded" onRetry={subscriptions.reload} label="Loading subscriptions…" />
        )}

        {providers.data ? (
          <ProviderKeys
            providers={providers.data.filter((p) => !p.subscription)}
            onChange={(update) => mutateProviders((current) => update(current ?? []))}
            onModelsChanged={reloadModels}
          />
        ) : (
          <Pending title="API keys" error={providers.error} failed="Providers not loaded" onRetry={providers.reload} label="Loading providers…" />
        )}

        {list.data ? (
          <ModelCatalog
            models={list.data}
            names={names}
            order={order}
            routing={routing.data ?? []}
            bindings={bindings.data ?? []}
          />
        ) : (
          <Pending title="Models" error={list.error} failed="Models not loaded" onRetry={list.reload} label="Loading models…" />
        )}

        {sessions.data ? <UsagePanel sessions={sessions.data} /> : null}

        {routing.data ? (
          <RoutingEditor groups={routing.data} models={list.data ?? []} onSaved={(groups) => mutateRouting(groups)} />
        ) : (
          <Pending title="Routing groups" error={routing.error} failed="Routing groups not loaded" onRetry={routing.reload} label="Loading routing groups…" />
        )}
      </div>
    </div>
  );
}
