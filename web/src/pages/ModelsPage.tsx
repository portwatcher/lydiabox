import { CircleAlert, CircleCheck, CircleDashed, LoaderCircle } from "lucide-react";
import { useCallback } from "react";
import { availabilityOf } from "../api/errors";
import { models as modelsApi } from "../api/domains";
import type { GatewayState, Model } from "../api/types";
import { ErrorView, LoadingView, StateView } from "../components/StateView";
import { Badge, Panel } from "../components/ui";
import type { Tone } from "../components/ui";
import { useResource } from "../hooks/useResource";
import { formatCompact } from "../lib/format";
import { BindingsTable } from "./models/BindingsTable";
import { ProviderCards } from "./models/ProviderCards";
import { RoutingEditor } from "./models/RoutingEditor";
import { SubscriptionCards } from "./models/SubscriptionCards";

const gatewayInfo: Record<GatewayState, { label: string; tone: Tone; icon: typeof CircleCheck }> = {
  running: { label: "Running", tone: "success", icon: CircleCheck },
  starting: { label: "Starting", tone: "warning", icon: LoaderCircle },
  stopped: { label: "Stopped", tone: "neutral", icon: CircleDashed },
  error: { label: "Error", tone: "danger", icon: CircleAlert },
};

function ModelsTable({ list }: { list: Model[] }) {
  if (list.length === 0) {
    return (
      <StateView title="No models yet" className="min-h-32">
        Models appear here when a provider or a subscription is enabled.
      </StateView>
    );
  }
  return (
    <div className="max-h-96 overflow-auto">
      <table className="w-full text-sm">
        <thead className="sticky top-0 bg-bg-subtle">
          <tr className="border-b border-border-subtle text-left text-xs text-fg-muted">
            <th scope="col" className="px-4 py-2 font-medium">
              Model ID
            </th>
            <th scope="col" className="px-4 py-2 font-medium">
              Provider
            </th>
            <th scope="col" className="px-4 py-2 text-right font-medium">
              Context
            </th>
          </tr>
        </thead>
        <tbody>
          {list.map((m) => (
            <tr key={m.id} className="border-b border-border-subtle last:border-b-0">
              <td className="px-4 py-2 font-mono text-[13px] break-all">{m.id}</td>
              <td className="px-4 py-2 text-fg-muted">{m.provider}</td>
              <td className="px-4 py-2 text-right text-fg-muted tabular-nums">
                {m.contextWindow > 0 ? formatCompact(m.contextWindow) : ""}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

export default function ModelsPage() {
  const gateway = useResource(useCallback((s: AbortSignal) => modelsApi.gateway(s), []), { pollMs: 10_000 });
  const missing = ["missing", "not_implemented"].includes(availabilityOf(gateway.error) ?? "");
  const enabled = !missing;
  const providers = useResource(useCallback((s: AbortSignal) => modelsApi.providers(s).then((r) => r.providers), []), { enabled });
  const list = useResource(useCallback((s: AbortSignal) => modelsApi.list(s).then((r) => r.models), []), { enabled });
  const routing = useResource(useCallback((s: AbortSignal) => modelsApi.routing(s).then((r) => r.groups), []), { enabled });
  const bindings = useResource(useCallback((s: AbortSignal) => modelsApi.bindings(s).then((r) => r.bindings), []), { enabled });
  const subscriptions = useResource(useCallback((s: AbortSignal) => modelsApi.subscriptions(s).then((r) => r.subscriptions), []), { enabled });
  const reloadModels = list.reload;
  const { reload: reloadSubscriptions } = subscriptions;
  const { reload: reloadProviders } = providers;
  const subscriptionsChanged = useCallback(() => {
    reloadSubscriptions();
    reloadProviders();
    reloadModels();
  }, [reloadSubscriptions, reloadProviders, reloadModels]);
  const { mutate: mutateProviders } = providers;
  const { mutate: mutateRouting } = routing;
  const { mutate: mutateBindings } = bindings;

  if (gateway.loading && !gateway.data && !gateway.error) {
    return <LoadingView label="Loading models…" />;
  }
  if (missing) {
    return <ErrorView error={gateway.error} subsystem="Model management" failed="Models not loaded" onRetry={gateway.reload} />;
  }

  const status = gateway.data ? gatewayInfo[gateway.data.status] : null;
  const models = list.data ?? [];

  return (
    <div className="h-full overflow-y-auto">
      <div className="mx-auto flex max-w-6xl flex-col gap-4 p-4 sm:p-6">
        <div className="flex flex-wrap items-center gap-3">
          <h1 className="text-lg font-semibold tracking-tight">Models</h1>
          {status && gateway.data ? (
            <span className="flex flex-wrap items-center gap-2 text-xs text-fg-muted">
              <Badge tone={status.tone} icon={status.icon}>
                Gateway {status.label.toLowerCase()}
              </Badge>
              <code>{gateway.data.addr}</code>
              {gateway.data.version ? <span>version {gateway.data.version}</span> : null}
            </span>
          ) : gateway.error ? (
            <Badge tone="danger" icon={CircleAlert}>
              Gateway unreachable
            </Badge>
          ) : null}
        </div>

        {subscriptions.data ? (
          <SubscriptionCards subscriptions={subscriptions.data} onChanged={subscriptionsChanged} />
        ) : (
          <Panel title="Subscriptions">
            {subscriptions.error ? (
              <ErrorView error={subscriptions.error} subsystem="The model gateway" failed="Subscriptions not loaded" onRetry={subscriptions.reload} />
            ) : (
              <LoadingView label="Loading subscriptions…" />
            )}
          </Panel>
        )}

        {providers.data ? (
          <ProviderCards
            providers={providers.data.filter((p) => !p.subscription)}
            onChange={(update) => mutateProviders((current) => update(current ?? []))}
            onModelsChanged={reloadModels}
          />
        ) : (
          <Panel title="Providers">
            {providers.error ? (
              <ErrorView error={providers.error} subsystem="The model gateway" failed="Providers not loaded" onRetry={providers.reload} />
            ) : (
              <LoadingView label="Loading providers…" />
            )}
          </Panel>
        )}

        {bindings.data ? (
          <BindingsTable
            bindings={bindings.data}
            models={models}
            onSaved={(saved) =>
              mutateBindings((current) => {
                const all = current ?? [];
                return all.some((b) => b.harness === saved.harness)
                  ? all.map((b) => (b.harness === saved.harness ? saved : b))
                  : [...all, saved];
              })
            }
          />
        ) : (
          <Panel title="Default model per harness">
            {bindings.error ? (
              <ErrorView error={bindings.error} subsystem="Model bindings" failed="Bindings not loaded" onRetry={bindings.reload} />
            ) : (
              <LoadingView label="Loading bindings…" />
            )}
          </Panel>
        )}

        {routing.data ? (
          <RoutingEditor groups={routing.data} models={models} onSaved={(groups) => mutateRouting(groups)} />
        ) : (
          <Panel title="Routing groups">
            {routing.error ? (
              <ErrorView error={routing.error} subsystem="The model gateway" failed="Routing groups not loaded" onRetry={routing.reload} />
            ) : (
              <LoadingView label="Loading routing groups…" />
            )}
          </Panel>
        )}

        <Panel title="Available models">
          {list.data ? (
            <ModelsTable list={list.data} />
          ) : list.error ? (
            <ErrorView error={list.error} subsystem="The model gateway" failed="Models not loaded" onRetry={list.reload} />
          ) : (
            <LoadingView label="Loading models…" />
          )}
        </Panel>
      </div>
    </div>
  );
}
