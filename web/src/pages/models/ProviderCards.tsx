import { CircleAlert, CircleCheck, KeyRound, Pencil, Plus, Trash2 } from "lucide-react";
import { useState } from "react";
import { describeError } from "../../api/errors";
import { models } from "../../api/domains";
import type { Provider } from "../../api/types";
import { ConfirmDialog } from "../../components/Dialog";
import { StateView } from "../../components/StateView";
import { useToast } from "../../components/Toast";
import { Badge, Button, IconButton, Panel } from "../../components/ui";
import { count } from "../../lib/format";
import { ProviderDialog } from "./ProviderDialog";

interface ProviderCardsProps {
  providers: Provider[];
  onChange: (update: (current: Provider[]) => Provider[]) => void;
  onModelsChanged: () => void;
}

function ProviderCard({ provider, onEdit, onDelete, onToggle, toggling }: { provider: Provider; onEdit: () => void; onDelete: () => void; onToggle: () => void; toggling: boolean }) {
  const failing = provider.status !== "ok" && provider.status !== "";
  const switchId = `pv-enabled-${provider.id}`;
  return (
    <li className="card card-sm border border-base-content/10 bg-base-100">
      <div className="card-body gap-3">
        <div className="flex items-start gap-2">
          <div className="flex min-w-0 flex-1 flex-col gap-0.5">
            <h3 className="truncate text-sm font-semibold" title={provider.name}>
              {provider.name}
            </h3>
            <span className="truncate font-mono text-xs text-base-content/70" title={provider.baseUrl || provider.kind}>
              {provider.baseUrl || provider.kind}
            </span>
          </div>
          <IconButton icon={Pencil} label={`Edit ${provider.name}`} onClick={onEdit} />
          <IconButton icon={Trash2} tone="danger" label={`Delete ${provider.name}`} onClick={onDelete} />
        </div>
        <div className="flex flex-wrap gap-1.5">
          {provider.hasKey ? (
            <Badge tone="success" icon={KeyRound}>
              Key saved
            </Badge>
          ) : (
            <Badge tone="warning" icon={KeyRound}>
              No key
            </Badge>
          )}
          {failing ? (
            <Badge tone="danger" icon={CircleAlert}>
              {provider.status}
            </Badge>
          ) : provider.status === "ok" ? (
            <Badge tone="success" icon={CircleCheck}>
              Reachable
            </Badge>
          ) : null}
          <Badge>{count(provider.models.length, { one: "# model", other: "# models" })}</Badge>
        </div>
        {provider.lastError ? <p className="text-xs break-words text-error">{provider.lastError}</p> : null}
        <label htmlFor={switchId} className="mt-auto flex items-center gap-2 text-[13px] text-base-content">
          <input
            id={switchId}
            type="checkbox"
            role="switch"
            checked={provider.enabled}
            disabled={toggling}
            onChange={onToggle}
            className="toggle toggle-primary toggle-sm"
          />
          Route requests to this provider
        </label>
      </div>
    </li>
  );
}

export function ProviderCards({ providers, onChange, onModelsChanged }: ProviderCardsProps) {
  const toast = useToast();
  const [editing, setEditing] = useState<Provider | null>(null);
  const [adding, setAdding] = useState(false);
  const [deleting, setDeleting] = useState<Provider | null>(null);
  const [toggling, setToggling] = useState<string | null>(null);

  const upsert = (saved: Provider) => {
    onChange((all) => (all.some((p) => p.id === saved.id) ? all.map((p) => (p.id === saved.id ? saved : p)) : [...all, saved]));
    onModelsChanged();
    toast.success(`${saved.name} saved`);
  };

  const toggle = async (provider: Provider) => {
    setToggling(provider.id);
    try {
      const saved = await models.updateProvider(provider.id, { enabled: !provider.enabled });
      onChange((all) => all.map((p) => (p.id === saved.id ? saved : p)));
      onModelsChanged();
    } catch (err) {
      toast.error(describeError(err, `${provider.name} not updated`));
    } finally {
      setToggling(null);
    }
  };

  return (
    <Panel
      title="Providers"
      actions={
        <Button size="sm" variant="primary" icon={Plus} onClick={() => setAdding(true)}>
          Add provider
        </Button>
      }
    >
      {providers.length === 0 ? (
        <StateView title="No providers yet" className="min-h-32" action={<Button icon={Plus} onClick={() => setAdding(true)}>Add provider</Button>}>
          Add an LLM provider and its API key. Harnesses reach it through the gateway; the key stays on the host.
        </StateView>
      ) : (
        <ul className="grid gap-3 p-4 sm:grid-cols-2 xl:grid-cols-3">
          {providers.map((p) => (
            <ProviderCard
              key={p.id}
              provider={p}
              toggling={toggling === p.id}
              onEdit={() => setEditing(p)}
              onDelete={() => setDeleting(p)}
              onToggle={() => void toggle(p)}
            />
          ))}
        </ul>
      )}
      <ProviderDialog open={adding || editing !== null} provider={editing} onClose={() => { setAdding(false); setEditing(null); }} onSaved={upsert} />
      <ConfirmDialog
        open={deleting !== null}
        onClose={() => setDeleting(null)}
        title={deleting ? `Delete ${deleting.name}?` : "Delete provider?"}
        confirmLabel="Delete provider"
        failed="Provider not deleted"
        body="Its saved key is erased. Routing groups and bindings that use its models stop working until you change them."
        onConfirm={async () => {
          if (!deleting) {
            return;
          }
          await models.deleteProvider(deleting.id);
          const id = deleting.id;
          onChange((all) => all.filter((p) => p.id !== id));
          onModelsChanged();
          toast.success(`${deleting.name} deleted`);
        }}
      />
    </Panel>
  );
}
