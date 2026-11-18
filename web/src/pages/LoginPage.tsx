import { LogIn } from "lucide-react";
import { useState } from "react";
import type { FormEvent } from "react";
import { ApiError } from "../api/http";
import { describeError } from "../api/errors";
import { accessLogoutPath, auth } from "../api/system";
import type { AuthState } from "../api/types";
import { Button, Field, TextInput, describedBy } from "../components/ui";

function loginError(err: unknown): string {
  if (err instanceof ApiError && err.status === 401) {
    return "Token not recognized. Use the admin token from $LYDIABOX_DATA_DIR/state/auth.token or the first-boot log.";
  }
  return describeError(err, "Not signed in");
}

// The token form shows only when the request is not signed in. When
// Cloudflare Access already signed the request in, the app never renders
// this page (App.tsx). An Access login whose email is not on the allow list
// arrives here with session.email set.
export function LoginPage({ session, onSignedIn }: { session?: AuthState; onSignedIn: () => void }) {
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
      <form onSubmit={(e) => void submit(e)} className="card w-full max-w-sm border border-base-content/10 bg-base-200" noValidate>
        <div className="card-body gap-5">
          <div className="flex flex-col items-center gap-3 text-center">
            <img src="/favicon.svg" alt="" className="size-10" />
            <h1 className="text-xl font-semibold tracking-tight">Sign in to LydiaBox</h1>
          </div>
          {session?.email ? (
            <p className="text-sm text-base-content/70">
              Cloudflare Access signed you in as <span className="font-medium break-all text-base-content">{session.email}</span>, but this email
              is not on the allow list. Sign in with the admin token, or{" "}
              <a href={accessLogoutPath} className="link font-medium text-base-content underline-offset-2">
                sign out of Cloudflare Access
              </a>
              .
            </p>
          ) : null}
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
        </div>
      </form>
    </main>
  );
}
