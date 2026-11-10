import { Check } from "lucide-react";
import { useEffect, useState } from "react";
import { describeError } from "../../api/errors";
import { models as modelsApi } from "../../api/domains";
import { HARNESSES } from "../../api/types";
import type { AgentHarness, Binding, Model } from "../../api/types";
import { useToast } from "../../components/Toast";
import { Button, Panel, Select } from "../../components/ui";
import { formatDateTime } from "../../lib/format";
import { harnessLabels } from "../../lib/harness";

interface BindingsTableProps {
  bindings: Binding[];
  models: Model[];
  onSaved: (binding: Binding) => void;
}

function BindingRow({ harness, binding, models, onSaved }: { harness: AgentHarness; binding: Binding | undefined; models: Model[]; onSaved: (b: Binding) => void }) {
  const toast = useToast();
  const current = binding?.model ?? "";
  const [value, setValue] = useState(current);
  const [busy, setBusy] = useState(false);
  useEffect(() => setValue(current), [current]);

  const known = new Set(models.map((m) => m.id));
  const save = async () => {
    setBusy(true);
    try {
      const saved = await modelsApi.bind(harness, value);
      onSaved(saved);
      toast.success(value ? `${harnessLabels[harness]} now uses ${value}` : `${harnessLabels[harness]} unbound`);
    } catch (err) {
      toast.error(describeError(err, `${harnessLabels[harness]} binding not saved`));
    } finally {
      setBusy(false);
    }
  };
  const selectId = `bind-${harness}`;

  return (
    <tr className="border-b border-border-subtle last:border-b-0">
      <th scope="row" className="px-4 py-2 text-left font-medium">
        <label htmlFor={selectId}>{harnessLabels[harness]}</label>
      </th>
      <td className="px-4 py-2">
        <div className="flex items-center gap-2">
          <Select id={selectId} value={value} onChange={(e) => setValue(e.target.value)} className="min-w-48">
            <option value="">Not bound</option>
            {current && !known.has(current) ? <option value={current}>{current}</option> : null}
            {models.map((m) => (
              <option key={m.id} value={m.id}>
                {m.id}
              </option>
            ))}
          </Select>
          <Button size="md" icon={Check} busy={busy} disabled={value === current} onClick={() => void save()}>
            Apply
          </Button>
        </div>
      </td>
      <td className="hidden px-4 py-2 md:table-cell">
        {binding?.configPath ? (
          <code className="block truncate text-xs text-fg-muted" title={binding.configPath}>
            {binding.configPath}
          </code>
        ) : null}
      </td>
      <td className="hidden px-4 py-2 text-xs text-fg-muted tabular-nums lg:table-cell">
        {binding?.model ? formatDateTime(binding.updatedAt) : ""}
      </td>
    </tr>
  );
}

export function BindingsTable({ bindings, models, onSaved }: BindingsTableProps) {
  return (
    <Panel title="Default model per harness">
      <div className="overflow-x-auto">
        <table className="w-full text-sm">
          <thead>
            <tr className="border-b border-border-subtle text-left text-xs text-fg-muted">
              <th scope="col" className="px-4 py-2 font-medium">
                Harness
              </th>
              <th scope="col" className="px-4 py-2 font-medium">
                Model
              </th>
              <th scope="col" className="hidden px-4 py-2 font-medium md:table-cell">
                Config file
              </th>
              <th scope="col" className="hidden px-4 py-2 font-medium lg:table-cell">
                Updated
              </th>
            </tr>
          </thead>
          <tbody>
            {HARNESSES.map((h) => (
              <BindingRow key={h} harness={h} binding={bindings.find((b) => b.harness === h)} models={models} onSaved={onSaved} />
            ))}
          </tbody>
        </table>
      </div>
    </Panel>
  );
}
