import type { AgentHarness, Binding, Model, RoutingGroup, Session } from "../../api/types";

// Pure helpers of the model catalog on the Models page; unit-tested.

export interface ProviderGroup {
  provider: string;
  name: string;
  models: Model[];
}

/** The short name of a model: its `name`, or the ID after the provider prefix. */
export function modelName(m: Model): string {
  if (m.name) {
    return m.name;
  }
  const slash = m.id.indexOf("/");
  return slash >= 0 ? m.id.slice(slash + 1) : m.id;
}

/**
 * Groups models by provider, in the order of `providerOrder` (then by name),
 * and keeps only the models whose ID or name contains every word of `query`.
 */
export function groupModels(models: Model[], names: Record<string, string>, query = "", providerOrder: string[] = []): ProviderGroup[] {
  const words = query.toLowerCase().split(/\s+/).filter(Boolean);
  const groups = new Map<string, Model[]>();
  for (const m of models) {
    const hay = `${m.id} ${m.name} ${names[m.provider] ?? ""}`.toLowerCase();
    if (!words.every((w) => hay.includes(w))) {
      continue;
    }
    const list = groups.get(m.provider) ?? [];
    list.push(m);
    groups.set(m.provider, list);
  }
  const rank = (p: string) => {
    const i = providerOrder.indexOf(p);
    return i < 0 ? providerOrder.length : i;
  };
  return [...groups.entries()]
    .map(([provider, list]) => ({
      provider,
      name: names[provider] ?? provider,
      models: [...list].sort((a, b) => modelName(a).localeCompare(modelName(b))),
    }))
    .sort((a, b) => rank(a.provider) - rank(b.provider) || a.name.localeCompare(b.name));
}

/** The harnesses bound to each model ID. */
export function bindingsByModel(bindings: Binding[]): Map<string, AgentHarness[]> {
  const out = new Map<string, AgentHarness[]>();
  for (const b of bindings) {
    if (!b.model) {
      continue;
    }
    out.set(b.model, [...(out.get(b.model) ?? []), b.harness]);
  }
  return out;
}

/** The routing groups that send to each model ID. */
export function groupsByModel(groups: RoutingGroup[]): Map<string, string[]> {
  const out = new Map<string, string[]>();
  for (const g of groups) {
    for (const t of new Set(g.targets)) {
      out.set(t, [...(out.get(t) ?? []), g.name]);
    }
  }
  return out;
}

export interface ModelUsage {
  model: string;
  sessions: number;
  inputTokens: number;
  outputTokens: number;
  costUsd: number;
}

/**
 * Sums the token usage that harness sessions report (api.md section 8) by
 * model, the most expensive first, then the most tokens. Sessions without
 * usage do not count; a session without a model counts under "".
 */
export function usageByModel(sessions: Session[]): ModelUsage[] {
  const out = new Map<string, ModelUsage>();
  for (const s of sessions) {
    const u = s.usage;
    if (!u || (u.inputTokens === 0 && u.outputTokens === 0 && u.costUsd === 0)) {
      continue;
    }
    const row = out.get(s.model) ?? { model: s.model, sessions: 0, inputTokens: 0, outputTokens: 0, costUsd: 0 };
    row.sessions += 1;
    row.inputTokens += u.inputTokens;
    row.outputTokens += u.outputTokens;
    row.costUsd += u.costUsd;
    out.set(s.model, row);
  }
  return [...out.values()].sort(
    (a, b) => b.costUsd - a.costUsd || b.inputTokens + b.outputTokens - (a.inputTokens + a.outputTokens) || a.model.localeCompare(b.model),
  );
}

export function usageTotal(rows: ModelUsage[]): ModelUsage {
  return rows.reduce(
    (t, r) => ({
      model: "",
      sessions: t.sessions + r.sessions,
      inputTokens: t.inputTokens + r.inputTokens,
      outputTokens: t.outputTokens + r.outputTokens,
      costUsd: t.costUsd + r.costUsd,
    }),
    { model: "", sessions: 0, inputTokens: 0, outputTokens: 0, costUsd: 0 },
  );
}
