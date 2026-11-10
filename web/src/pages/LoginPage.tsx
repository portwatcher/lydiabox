import { LogIn } from "lucide-react";
import { useState } from "react";
import type { FormEvent } from "react";
import { ApiError } from "../api/http";
import { describeError } from "../api/errors";
import { auth } from "../api/system";
import { Button, Field, TextInput, describedBy } from "../components/ui";

function loginError(err: unknown): string {
  if (err instanceof ApiError && err.status === 401) {
    return "Token not recognized. Use the admin token from $LYDIABOX_DATA_DIR/state/auth.token or the first-boot log.";
  }
  return describeError(err, "Not signed in");
}

export function LoginPage({ onSignedIn }: { onSignedIn: () => void }) {
  const [token, setToken] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!token.trim()) {
      setError("Enter the admin token.");
      return;
    }
    setBusy(true);
    setError(null);
    try {
      const result = await auth.login(token.trim());
      if (result.authenticated) {
        onSignedIn();
        return;
      }
      setError("Token not recognized. Check the token, then try again.");
    } catch (err) {
      setError(loginError(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <main className="flex min-h-full items-center justify-center px-4 py-12">
      <form onSubmit={(e) => void submit(e)} className="flex w-full max-w-sm flex-col gap-5" noValidate>
        <div className="flex flex-col items-center gap-3 text-center">
          <img src="/favicon.svg" alt="" className="size-10" />
          <h1 className="text-xl font-semibold tracking-tight">Sign in to LydiaBox</h1>
        </div>
        <Field
          id="token"
          label="Admin token"
          hint="Printed by lydiabox up on first boot."
          error={error}
        >
          <TextInput
            id="token"
            type="password"
            autoComplete="current-password"
            autoFocus
            value={token}
            onChange={(e) => setToken(e.target.value)}
            aria-invalid={error ? true : undefined}
            aria-describedby={describedBy("token", true, Boolean(error))}
            spellCheck={false}
          />
        </Field>
        <Button type="submit" variant="primary" icon={LogIn} busy={busy} className="w-full">
          Sign in
        </Button>
      </form>
    </main>
  );
}
