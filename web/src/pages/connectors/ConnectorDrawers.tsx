import { useEffect, useState } from "react";
import type { FormEvent } from "react";
import { describeError } from "../../api/errors";
import { connectors } from "../../api/domains";
import type { Connector } from "../../api/types";
import { CopyField } from "../../components/CopyField";
import { Dialog } from "../../components/Dialog";
import { Banner, Button, Field, TextInput, describedBy } from "../../components/ui";

interface DrawerProps {
  connector: Connector | null;
  onClose: () => void;
  onSaved: (connector: Connector) => void;
}

/** Stores an OAuth client ID and secret for self-hosted OAuth apps. */
export function ClientDrawer({ connector, onClose, onSaved }: DrawerProps) {
  const [clientId, setClientId] = useState("");
  const [secret, setSecret] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [fieldError, setFieldError] = useState<string | null>(null);

  useEffect(() => {
    setClientId("");
    setSecret("");
    setError(null);
    setFieldError(null);
  }, [connector]);

  if (!connector) {
    return null;
  }
  const fromEnv = connector.oauthSource === "env";
  const callback = `${window.location.origin}/api/v1/connectors/${connector.provider}/callback`;

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!clientId.trim()) {
      setFieldError("Enter the client ID from your OAuth app.");
      return;
    }
    setFieldError(null);
    setBusy(true);
    setError(null);
    try {
      onSaved(await connectors.setClient(connector.provider, clientId.trim(), secret.trim()));
      onClose();
    } catch (err) {
      setError(describeError(err, "Client ID not saved"));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog
      open
      onClose={onClose}
      variant="drawer"
      title={`${connector.displayName} OAuth client`}
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button type="submit" form="client-form" variant="primary" busy={busy} disabled={fromEnv}>
            Save client
          </Button>
        </>
      }
    >
      <form id="client-form" onSubmit={(e) => void submit(e)} className="flex flex-col gap-4" noValidate>
        {fromEnv ? (
          <Banner tone="accent">
            The client is set by LYDIABOX_{connector.provider.toUpperCase()}_CLIENT_ID on the host. Environment values win over saved
            ones.
          </Banner>
        ) : null}
        {error ? <Banner tone="danger">{error}</Banner> : null}
        <CopyField label="Redirect URI for your OAuth app" value={callback} />
        <Field id="client-id" label="Client ID" error={fieldError}>
          <TextInput
            id="client-id"
            value={clientId}
            onChange={(e) => setClientId(e.target.value)}
            spellCheck={false}
            autoComplete="off"
            disabled={fromEnv}
            aria-invalid={fieldError ? true : undefined}
            aria-describedby={describedBy("client-id", false, Boolean(fieldError))}
          />
        </Field>
        <Field id="client-secret" label="Client secret" hint="Stored encrypted on the host.">
          <TextInput
            id="client-secret"
            type="password"
            value={secret}
            onChange={(e) => setSecret(e.target.value)}
            spellCheck={false}
            autoComplete="off"
            disabled={fromEnv}
            aria-describedby="client-secret-hint"
          />
        </Field>
      </form>
    </Dialog>
  );
}

/** Binds a personal access token; the server validates it with the provider. */
export function TokenDrawer({ connector, onClose, onSaved }: DrawerProps) {
  const [token, setToken] = useState("");
  const [label, setLabel] = useState("");
  const [scopes, setScopes] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [fieldError, setFieldError] = useState<string | null>(null);

  useEffect(() => {
    setToken("");
    setLabel("");
    setScopes(connector?.defaultScopes.join(", ") ?? "");
    setError(null);
    setFieldError(null);
  }, [connector]);

  if (!connector) {
    return null;
  }

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!token.trim()) {
      setFieldError("Paste the token.");
      return;
    }
    setFieldError(null);
    setBusy(true);
    setError(null);
    try {
      const saved = await connectors.pasteToken(connector.provider, {
        token: token.trim(),
        accountLabel: label.trim(),
        scopes: scopes
          .split(/[\s,]+/)
          .map((s) => s.trim())
          .filter(Boolean),
      });
      setToken("");
      onSaved(saved);
      onClose();
    } catch (err) {
      setError(describeError(err, `${connector.displayName} not connected`));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Dialog
      open
      onClose={onClose}
      variant="drawer"
      title={`Paste ${connector.displayName} token`}
      footer={
        <>
          <Button onClick={onClose}>Cancel</Button>
          <Button type="submit" form="token-form" variant="primary" busy={busy}>
            Connect with token
          </Button>
        </>
      }
    >
      <form id="token-form" onSubmit={(e) => void submit(e)} className="flex flex-col gap-4" noValidate>
        {error ? <Banner tone="danger">{error}</Banner> : null}
        <Field id="token-value" label="Token" hint="Checked with the provider, then stored encrypted on the host. It never reaches the VM disk." error={fieldError}>
          <TextInput
            id="token-value"
            type="password"
            value={token}
            onChange={(e) => setToken(e.target.value)}
            spellCheck={false}
            autoComplete="off"
            aria-invalid={fieldError ? true : undefined}
            aria-describedby={describedBy("token-value", true, Boolean(fieldError))}
          />
        </Field>
        <Field id="token-label" label="Account label" hint="Leave empty to use the provider's user name.">
          <TextInput id="token-label" value={label} onChange={(e) => setLabel(e.target.value)} aria-describedby="token-label-hint" />
        </Field>
        <Field id="token-scopes" label="Scopes" hint="Separate scopes with commas.">
          <TextInput id="token-scopes" value={scopes} onChange={(e) => setScopes(e.target.value)} spellCheck={false} aria-describedby="token-scopes-hint" />
        </Field>
      </form>
    </Dialog>
  );
}
