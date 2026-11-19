import { Search } from "lucide-react";
import { useMemo, useState } from "react";
import type { Binding, Model, RoutingGroup } from "../../api/types";
import { Mark } from "../../components/product/Marks";
import { ModelCard } from "../../components/product/ModelCard";
import { StateView } from "../../components/StateView";
import { Panel } from "../../components/ui";
import { count } from "../../lib/format";
import { harnessLabel } from "../../lib/harness";
import { bindingsByModel, groupModels, groupsByModel, modelName } from "./catalog";

/** From this many models, a search field filters the catalog. */
const SEARCH_FROM = 9;

interface ModelCatalogProps {
  models: Model[];
  /** Provider ID to display name. */
  names: Record<string, string>;
  /** Provider IDs in the order of the page (subscriptions first). */
  order: string[];
  routing: RoutingGroup[];
  /**
   * The model that each harness used last (every session start sets it).
   * The cards show it; they do not change it: a session chooses its model
   * (docs/design.md section 5).
   */
  bindings: Binding[];
}

/** The models that the gateway serves, as cards grouped by provider. */
export function ModelCatalog({ models, names, order, routing, bindings }: ModelCatalogProps) {
  const [query, setQuery] = useState("");
  const groups = useMemo(() => groupModels(models, names, query, order), [models, names, query, order]);
  const bound = useMemo(() => bindingsByModel(bindings), [bindings]);
  const routes = useMemo(() => groupsByModel(routing), [routing]);
  const maxContext = useMemo(() => Math.max(2_000_000, ...models.map((m) => m.contextWindow)), [models]);

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
                {g.models.map((m) => (
                  <ModelCard
                    key={m.id}
                    id={m.id}
                    name={modelName(m)}
                    contextWindow={m.contextWindow}
                    maxContext={maxContext}
                    boundTo={(bound.get(m.id) ?? []).map(harnessLabel)}
                    groups={routes.get(m.id) ?? []}
                  />
                ))}
              </ul>
            </section>
          ))}
        </div>
      )}
    </Panel>
  );
}
