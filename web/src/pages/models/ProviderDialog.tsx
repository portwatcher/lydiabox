import { useEffect, useState } from "react";
import type { FormEvent } from "react";
import { describeError } from "../../api/errors";
import { models } from "../../api/domains";
import type { Provider, UpdateProviderRequest } from "../../api/types";
import { Dialog } from "../../components/Dialog";
import { Banner, Button, Field, Select, TextInput, describedBy } from "../../components/ui";

/** The magpie presets that the dialog offers (docs/api.md, POST /api/v1/models/providers). */
const KINDS: { value: string; label: string }[] = [
  { value: "openai", label: "OpenAI" },
  { value: "anthropic", label: "Anthropic" },
  { value: "google", label: "Google" },
  { value: "deepseek", label: "DeepSeek" },
  { value: "moonshot", label: "Moonshot" },
  { value: "openrouter", label: "OpenRouter" },
  { value: "openai-compatible", label: "OpenAI compatible" },
];

interface ProviderDialogProps {
  open: boolean;
  /** Null adds a provider. */
  provider: Provider | null;
  onClose: () => void;
  onSaved: (provider: Provider) => void;
}

/** Add or edit a provider. The key field is write-only: the API never returns keys. */
export function ProviderDialog({ open, provider, onClose, onSaved }: ProviderDialogProps) {
  const editing = provider !== null;
  const [kind, setKind] = useState("openai");
  const [name, setName] = useState("");
  const [baseUrl, setBaseUrl] = useState("");
  const [apiKey, setApiKey] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [fieldErrors, setFieldErrors] = useState<{ name?: string; baseUrl?: string; apiKey?: string }>({});

  useEffect(() => {
    if (open) {
      setKind(provider?.kind ?? "openai");
      setName(provider?.name ?? "");
      setBaseUrl(provider?.baseUrl ?? "");
      setApiKey("");
      setError(null);
      setFieldErrors({});
    }
  }, [open, provider]);

  const validate = () => {
    const errors: typeof fieldErrors = {};
    if (!name.trim()) {
      errors.name = "Enter a display name.";
    }
    if (baseUrl.trim() && !/^https?:\/\//.test(baseUrl.trim())) {
      errors.baseUrl = "Enter a URL that starts with http:// or https://.";
    }
    if (!editing && !apiKey.trim() && kind !== "openai-compatible") {
      errors.apiKey = "Enter the API key.";
    }
    setFieldErrors(errors);
    return Object.keys(errors).length === 0;
  };

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!validate()) {
      return;
    }
    setBusy(true);
    setError(null);
    try {
      let saved: Provider;
      if (provider) {
        const patch: UpdateProviderRequest = {};
        if (name.trim() !== provider.name) {
          patch.name = name.trim();
        }
        if (baseUrl.trim() !== provider.baseUrl) {
          patch.baseUrl = baseUrl.trim();
        }
        if (apiKey.trim()) {
          patch.apiKey = apiKey.trim();
        }
        saved = Object.keys(patch).length > 0 ? await models.updateProvider(provider.id, patch) : provider;
      } else {
        saved = await models.createProvider({ kind: kind.trim(), name: name.trim(), baseUrl: baseUrl.trim(), apiKey: apiKey.trim() });
      }
      setApiKey("");
      onSaved(saved);
      onClose();
    } catch (err) {
      setError(describeError(err, editing ? "Provider not saved" : "Provider not added"));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog
      open={open}
      onClose={onClose}
      title={provider ? `Edit ${provider.name}` : "Add provider"}
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button type="submit" form="provider-form" variant="primary" busy={busy}>
            {editing ? "Save provider" : "Add provider"}
          </Button>
        </>
      }
    >
      <form id="provider-form" onSubmit={(e) => void submit(e)} className="flex flex-col gap-4" noValidate>
        {error ? <Banner tone="danger">{error}</Banner> : null}
        {!editing ? (
          <Field id="pv-kind" label="Kind">
            <Select id="pv-kind" value={kind} onChange={(e) => setKind(e.target.value)}>
              {KINDS.map((k) => (
                <option key={k.value} value={k.value}>
                  {k.label}
                </option>
              ))}
            </Select>
          </Field>
        ) : null}
        <Field id="pv-name" label="Name" error={fieldErrors.name}>
          <TextInput
            id="pv-name"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="OpenAI"
            aria-invalid={fieldErrors.name ? true : undefined}
            aria-describedby={describedBy("pv-name", false, Boolean(fieldErrors.name))}
          />
        </Field>
        <Field id="pv-url" label="Base URL" hint="Leave empty for the provider's default endpoint." error={fieldErrors.baseUrl}>
          <TextInput
            id="pv-url"
            type="url"
            value={baseUrl}
            onChange={(e) => setBaseUrl(e.target.value)}
            placeholder="https://api.example.com/v1"
            spellCheck={false}
            aria-invalid={fieldErrors.baseUrl ? true : undefined}
            aria-describedby={describedBy("pv-url", true, Boolean(fieldErrors.baseUrl))}
          />
        </Field>
        <Field
          id="pv-key"
          label="API key"
          hint={
            editing
              ? provider.hasKey
                ? "A key is saved. Enter a new key to replace it, or leave empty to keep it."
                : "No key is saved."
              : "Stored on the host only; LydiaBox never shows it again."
          }
          error={fieldErrors.apiKey}
        >
          <TextInput
            id="pv-key"
            type="password"
            autoComplete="off"
            value={apiKey}
            onChange={(e) => setApiKey(e.target.value)}
            spellCheck={false}
            aria-invalid={fieldErrors.apiKey ? true : undefined}
            aria-describedby={describedBy("pv-key", true, Boolean(fieldErrors.apiKey))}
          />
        </Field>
      </form>
    </Dialog>
  );
}
