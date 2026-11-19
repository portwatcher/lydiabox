import { Search } from "lucide-react";
import { useMemo, useState } from "react";
import { describeError } from "../../api/errors";
import { models as modelsApi } from "../../api/domains";
import { HARNESSES } from "../../api/types";
import type { AgentHarness, Binding, Model, RoutingGroup } from "../../api/types";
import { MenuButton } from "../../components/product/MenuButton";
import { Mark } from "../../components/product/Marks";
import { ModelCard } from "../../components/product/ModelCard";
import { StateView } from "../../components/StateView";
import { useToast } from "../../components/Toast";
import { Panel } from "../../components/ui";
import { count } from "../../lib/format";
import { harnessLabel } from "../../lib/harness";
import { bindingsByModel, groupModels, groupsByModel, modelName, withBinding } from "./catalog";

/** From this many models, a search field filters the catalog. */
const SEARCH_FROM = 9;

interface ModelCatalogProps {
  models: Model[];
  /** Provider ID to display name. */
  names: Record<string, string>;
  /** Provider IDs in the order of the page (subscriptions first). */
  order: string[];
  routing: RoutingGroup[];
  /** Null while the bindings did not load; the cards then have no "Use in" menu. */
  bindings: Binding[] | null;
  onBindings: (bindings: Binding[]) => void;
}

/** The models that the gateway serves, as cards grouped by provider. */
export function ModelCatalog({ models, names, order, routing, bindings, onBindings }: ModelCatalogProps) {
  const toast = useToast();
  const [query, setQuery] = useState("");
  const [busy, setBusy] = useState<AgentHarness | null>(null);
  const groups = useMemo(() => groupModels(models, names, query, order), [models, names, query, order]);
  const bound = useMemo(() => bindingsByModel(bindings ?? []), [bindings]);
  const routes = useMemo(() => groupsByModel(routing), [routing]);
  const maxContext = useMemo(() => Math.max(2_000_000, ...models.map((m) => m.contextWindow)), [models]);

  const bind = async (harness: AgentHarness, model: Model) => {
    if (!bindings) {
      return;
    }
    const current = bindings.find((b) => b.harness === harness)?.model ?? "";
    const next = current === model.id ? "" : model.id;
    setBusy(harness);
    try {
      const saved = await modelsApi.bind(harness, next);
      onBindings(withBinding(bindings, saved));
      toast.success(next ? `${harnessLabel(harness)} uses ${modelName(model)}` : `${harnessLabel(harness)} no longer bound to ${modelName(model)}`);
    } catch (err) {
      toast.error(describeError(err, `${harnessLabel(harness)} not bound`));
    } finally {
      setBusy(null);
    }
  };

  return (
    <Panel
      title={
        <span className="flex items-center gap-2">
          Models
          <span className="badge badge-sm badge-soft font-medium text-base-content/70 tabular-nums">{models.length}</span>
        </span>
      }
      actions={
        models.length >= SEARCH_FROM ? (
          <label className="input input-sm w-56 max-w-full border-base-content/45">
            <Search aria-hidden className="size-4 text-base-content/70" />
            <input type="search" aria-label="Filter models" placeholder="Filter" value={query} onChange={(e) => setQuery(e.target.value)} />
          </label>
        ) : null
      }
    >
      {models.length === 0 ? (
        <StateView title="No models yet" className="min-h-32">
          Models appear here when a provider or a subscription is enabled.
        </StateView>
      ) : groups.length === 0 ? (
        <StateView title="No match" className="min-h-32">
          No model contains “{query}”.
        </StateView>
      ) : (
        <div className="flex flex-col gap-5 p-4">
          {groups.map((g) => (
            <section key={g.provider} aria-labelledby={`models-${g.provider}`} className="flex flex-col gap-2.5">
              <h3 id={`models-${g.provider}`} className="flex items-center gap-2 text-[13px] font-semibold">
                <Mark name={g.name} size="sm" />
                {g.name}
                <span className="font-normal text-base-content/70">{count(g.models.length, { one: "# model", other: "# models" })}</span>
              </h3>
              <ul className="grid gap-3 sm:grid-cols-2 xl:grid-cols-3">
                {g.models.map((m) => {
                  const harnesses = bound.get(m.id) ?? [];
                  return (
                    <ModelCard
                      key={m.id}
                      id={m.id}
                      name={modelName(m)}
                      contextWindow={m.contextWindow}
                      maxContext={maxContext}
                      boundTo={harnesses.map(harnessLabel)}
                      groups={routes.get(m.id) ?? []}
                      action={
                        bindings ? (
                          <MenuButton
                            label="Use in"
                            items={HARNESSES.map((h) => ({ id: h, label: harnessLabel(h), checked: harnesses.includes(h), busy: busy === h }))}
                            onSelect={(id) => {
                              const harness = HARNESSES.find((h) => h === id);
                              if (harness) {
                                void bind(harness, m);
                              }
                            }}
                          />
                        ) : null
                      }
                    />
                  );
                })}
              </ul>
            </section>
          ))}
        </div>
      )}
    </Panel>
  );
}
