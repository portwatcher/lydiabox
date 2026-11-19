import { KeyRound, Pencil, Plus, RotateCw, Trash2 } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import type { FormEvent } from "react";
import { describeError } from "../../api/errors";
import { models } from "../../api/domains";
import type { Provider } from "../../api/types";
import { ConfirmDialog } from "../../components/Dialog";
import { providerHealth } from "../../components/product/health";
import { HealthLabel, Mark } from "../../components/product/Marks";
import { StateView } from "../../components/StateView";
import { useToast } from "../../components/Toast";
import { Button, IconButton, Panel, TextInput, cx } from "../../components/ui";
import { ProviderDialog } from "./ProviderDialog";

interface ProviderKeysProps {
  providers: Provider[];
  onChange: (update: (current: Provider[]) => Provider[]) => void;
  onModelsChanged: () => void;
}

/** The new key, typed in place under the row; the API never returns the old one. */
function RotateForm({ provider, onSaved, onCancel }: { provider: Provider; onSaved: (p: Provider) => void; onCancel: () => void }) {
  const [key, setKey] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const input = useRef<HTMLInputElement>(null);
  const id = `rotate-${provider.id}`;
  useEffect(() => input.current?.focus(), []);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!key.trim()) {
      setError("Enter the new API key.");
      return;
    }
    setBusy(true);
    setError(null);
    try {
      onSaved(await models.updateProvider(provider.id, { apiKey: key.trim() }));
    } catch (err) {
      setError(describeError(err, "Key not saved"));
      setBusy(false);
    }
  };

  return (
    <form onSubmit={(e) => void submit(e)} className="flex flex-col gap-1.5 px-4 pb-3 motion-safe:animate-fade-in">
      <label htmlFor={id} className="sr-only">
        New API key for {provider.name}
      </label>
      <div className="flex flex-wrap gap-2">
        <TextInput
          ref={input}
          id={id}
          type="password"
          autoComplete="off"
          spellCheck={false}
          placeholder="New API key"
          value={key}
          aria-invalid={error ? true : undefined}
          aria-describedby={error ? `${id}-error` : undefined}
          onChange={(e) => setKey(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Escape") {
              e.preventDefault();
              onCancel();
            }
          }}
          className="input-sm min-w-48 flex-1 font-mono"
        />
        <Button type="submit" size="sm" variant="primary" busy={busy}>
          Save key
        </Button>
        <Button size="sm" variant="ghost" disabled={busy} onClick={onCancel}>
          Cancel
        </Button>
      </div>
      {error ? (
        <p id={`${id}-error`} role="alert" className="text-xs text-error">
          {error}
        </p>
      ) : null}
    </form>
  );
}

function KeyRow({ provider, rotating, toggling, onRotate, onRotated, onCancelRotate, onEdit, onDelete, onToggle }: {
  provider: Provider;
  rotating: boolean;
  toggling: boolean;
  onRotate: () => void;
  onRotated: (p: Provider) => void;
  onCancelRotate: () => void;
  onEdit: () => void;
  onDelete: () => void;
  onToggle: () => void;
}) {
  const switchId = `pv-enabled-${provider.id}`;
  const health = providerHealth(provider);
  return (
    <li className="border-b border-base-content/10 last:border-b-0">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2 px-4 py-3">
        <Mark name={provider.name} />
        <div className="flex min-w-0 flex-1 basis-40 flex-col">
          <span className="truncate text-[13px] font-semibold" title={provider.name}>
            {provider.name}
          </span>
          <span className="truncate font-mono text-xs text-base-content/70" title={provider.baseUrl || provider.kind}>
            {provider.baseUrl || provider.kind}
          </span>
        </div>
        <span className={cx("inline-flex w-28 items-center gap-1.5 font-mono text-xs", provider.hasKey ? "text-base-content/70" : "text-warning")}>
          <KeyRound aria-hidden className="size-3.5 shrink-0" />
          {provider.hasKey ? <span aria-label="Key saved">••••••••</span> : <span className="font-sans font-medium">No key</span>}
        </span>
        <span className="w-28" title={provider.lastError || undefined}>
          <HealthLabel health={health} />
        </span>
        <input
          id={switchId}
          type="checkbox"
          role="switch"
          aria-label={`Route requests to ${provider.name}`}
          title="Route requests to this provider"
          checked={provider.enabled}
          disabled={toggling}
          onChange={onToggle}
          className="toggle toggle-primary toggle-sm"
        />
        <div className="flex items-center">
          <IconButton icon={RotateCw} label={provider.hasKey ? `Rotate the key of ${provider.name}` : `Add a key to ${provider.name}`} aria-expanded={rotating} onClick={rotating ? onCancelRotate : onRotate} />
          <IconButton icon={Pencil} label={`Edit ${provider.name}`} onClick={onEdit} />
          <IconButton icon={Trash2} tone="danger" label={`Delete ${provider.name}`} onClick={onDelete} />
        </div>
      </div>
      {provider.lastError && health === "outage" && !rotating ? (
        <p className="mx-4 mb-3 rounded-field bg-error/10 px-3 py-2 text-xs break-words text-error">{provider.lastError}</p>
      ) : null}
      {rotating ? <RotateForm provider={provider} onSaved={onRotated} onCancel={onCancelRotate} /> : null}
    </li>
  );
}

/** API-key providers: one row each with the key state, health, routing switch, and key actions. */
export function ProviderKeys({ providers, onChange, onModelsChanged }: ProviderKeysProps) {
  const toast = useToast();
  const [editing, setEditing] = useState<Provider | null>(null);
  const [adding, setAdding] = useState(false);
  const [deleting, setDeleting] = useState<Provider | null>(null);
  const [toggling, setToggling] = useState<string | null>(null);
  const [rotating, setRotating] = useState<string | null>(null);

  const replace = (saved: Provider) => onChange((all) => (all.some((p) => p.id === saved.id) ? all.map((p) => (p.id === saved.id ? saved : p)) : [...all, saved]));

  const toggle = async (provider: Provider) => {
    setToggling(provider.id);
    try {
      replace(await models.updateProvider(provider.id, { enabled: !provider.enabled }));
      onModelsChanged();
    } catch (err) {
      toast.error(describeError(err, `${provider.name} not updated`));
    } finally {
      setToggling(null);
    }
  };

  return (
    <Panel
      title="API keys"
      actions={
        <Button size="sm" variant="primary" icon={Plus} onClick={() => setAdding(true)}>
          Add provider
        </Button>
      }
    >
      {providers.length === 0 ? (
        <StateView title="No API keys yet" className="min-h-32" action={<Button icon={Plus} onClick={() => setAdding(true)}>Add provider</Button>}>
          Harnesses reach the provider through the gateway; the key stays on the host.
        </StateView>
      ) : (
        <ul>
          {providers.map((p) => (
            <KeyRow
              key={p.id}
              provider={p}
              rotating={rotating === p.id}
              toggling={toggling === p.id}
              onRotate={() => setRotating(p.id)}
              onCancelRotate={() => setRotating(null)}
              onRotated={(saved) => {
                setRotating(null);
                replace(saved);
                onModelsChanged();
                toast.success(`${saved.name} key saved`);
              }}
              onEdit={() => setEditing(p)}
              onDelete={() => setDeleting(p)}
              onToggle={() => void toggle(p)}
            />
          ))}
        </ul>
      )}
      <ProviderDialog
        open={adding || editing !== null}
        provider={editing}
        onClose={() => {
          setAdding(false);
          setEditing(null);
        }}
        onSaved={(saved) => {
          replace(saved);
          onModelsChanged();
          toast.success(`${saved.name} saved`);
        }}
      />
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
