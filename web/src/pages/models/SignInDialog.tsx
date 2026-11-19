import { ExternalLink, TriangleAlert } from "lucide-react";
import { useEffect, useReducer, useRef, useState } from "react";
import type { FormEvent } from "react";
import { describeError } from "../../api/errors";
import { models } from "../../api/domains";
import type { SubscriptionKind, SubscriptionLogin } from "../../api/types";
import { CopyField } from "../../components/CopyField";
import { Dialog } from "../../components/Dialog";
import { Banner, Button, Field, Spinner, TextInput, buttonClass, describedBy } from "../../components/ui";
import { initialSignIn, loginStep, pendingLoginId, setupTokenError, shouldPoll, signInReducer } from "./signin";

const POLL_MS = 2000;

interface SignInDialogProps {
  /** Null closes the dialog. */
  kind: SubscriptionKind | null;
  name: string;
  onClose: () => void;
  onSignedIn: (account: string) => void;
}

function SignInLink({ url }: { url: string }) {
  return (
    <a
      href={url}
      target="_blank"
      rel="noopener noreferrer"
      className={buttonClass("primary", "md", "w-fit")}
    >
      <ExternalLink aria-hidden className="size-4" />
      Open sign-in page
    </a>
  );
}

function Waiting({ children }: { children: string }) {
  return (
    <p role="status" className="flex items-center gap-2 text-sm text-base-content/70">
      <Spinner />
      {children}
    </p>
  );
}

function OpenLogin({ login, name, code, setCode, codeError }: { login: SubscriptionLogin; name: string; code: string; setCode: (v: string) => void; codeError: string | null }) {
  const step = loginStep(login);
  const paste = step === "code" || step === "address";
  if (step === "installing") {
    return <Waiting>{login.installing ? `Installing ${login.installing} on the host…` : "Starting the sign-in…"}</Waiting>;
  }
  return (
    <div className="flex flex-col gap-4">
      {step === "device" ? (
        <>
          <p className="text-sm">Open the sign-in page and enter this code.</p>
          <CopyField label="Code" value={login.userCode} />
        </>
      ) : step === "code" ? (
        <p className="text-sm">Open the sign-in page. When it shows a code, paste the code here.</p>
      ) : step === "address" ? (
        <p className="text-sm">
          Open the sign-in page and sign in. The browser then opens a localhost page that does not load. Paste the address of that page here.
        </p>
      ) : (
        <p className="text-sm">Finish the sign-in to {name} in your browser.</p>
      )}
      {login.url ? <SignInLink url={login.url} /> : null}
      {paste ? (
        <Field id="si-code" label={step === "address" ? "Address" : "Code"} error={codeError}>
          <TextInput
            id="si-code"
            value={code}
            onChange={(e) => setCode(e.target.value)}
            autoComplete="off"
            spellCheck={false}
            aria-invalid={codeError ? true : undefined}
            aria-describedby={describedBy("si-code", false, Boolean(codeError))}
          />
        </Field>
      ) : (
        <Waiting>Waiting for the sign-in to finish…</Waiting>
      )}
    </div>
  );
}

/** Signs a subscription in through magpie. Tokens stay on the host. */
export function SignInDialog({ kind, name, onClose, onSignedIn }: SignInDialogProps) {
  const [state, dispatch] = useReducer(signInReducer, initialSignIn);
  const [method, setMethod] = useState<"browser" | "token">("browser");
  const [token, setToken] = useState("");
  const [tokenError, setTokenError] = useState<string | null>(null);
  const [code, setCode] = useState("");
  const stateRef = useRef(state);
  stateRef.current = state;
  const open = kind !== null;

  useEffect(() => {
    if (open) {
      dispatch({ type: "reset" });
      setMethod("browser");
      setToken("");
      setTokenError(null);
      setCode("");
    }
  }, [open, kind]);

  const polling = shouldPoll(state);
  useEffect(() => {
    if (!polling || state.phase !== "open") {
      return;
    }
    const id = state.login.id;
    const ctl = new AbortController();
    const timer = setTimeout(() => {
      models
        .login(id, ctl.signal)
        .then((login) => dispatch({ type: "update", login }))
        .catch((err: unknown) => {
          if (!ctl.signal.aborted) {
            dispatch({ type: "fail", error: describeError(err, "Sign-in status not loaded") });
          }
        });
    }, POLL_MS);
    return () => {
      clearTimeout(timer);
      ctl.abort();
    };
  }, [polling, state]);

  useEffect(() => {
    if (state.phase === "done") {
      dispatch({ type: "reset" });
      onSignedIn(state.account);
    }
  }, [state, onSignedIn]);

  const close = () => {
    const id = pendingLoginId(stateRef.current);
    if (id) {
      void models.cancelLogin(id).catch(() => undefined);
    }
    onClose();
  };

  const start = async () => {
    if (!kind) {
      return;
    }
    const useToken = kind === "claude" && method === "token";
    if (useToken) {
      const err = setupTokenError(token);
      setTokenError(err);
      if (err) {
        return;
      }
    }
    dispatch({ type: "start" });
    try {
      const login = await models.startLogin(kind, useToken ? token.trim() : undefined);
      setToken("");
      dispatch({ type: "update", login });
    } catch (err) {
      dispatch({ type: "fail", error: describeError(err, "Sign-in not started") });
    }
  };

  const submitCode = async () => {
    if (state.phase !== "open") {
      return;
    }
    if (!code.trim()) {
      const address = loginStep(state.login) === "address";
      dispatch({ type: "codeRefused", error: address ? "Paste the address of the page that did not load." : "Paste the code that the sign-in page shows." });
      return;
    }
    dispatch({ type: "submit" });
    try {
      const login = await models.submitLoginCode(state.login.id, code.trim());
      setCode("");
      dispatch({ type: "update", login });
    } catch (err) {
      dispatch({ type: "codeRefused", error: describeError(err, loginStep(state.login) === "address" ? "Address not accepted" : "Code not accepted") });
    }
  };

  const onSubmit = (event: FormEvent) => {
    event.preventDefault();
    if (state.phase === "open") {
      void submitCode();
    } else if (state.phase === "idle" || state.phase === "starting") {
      void start();
    } else if (state.phase === "failed") {
      dispatch({ type: "reset" });
    }
  };

  const openStep = state.phase === "open" ? loginStep(state.login) : null;
  const needsCode = openStep === "code" || openStep === "address";
  const primary =
    state.phase === "failed"
      ? { label: "Try again", busy: false }
      : state.phase === "open"
        ? needsCode
          ? { label: "Finish sign-in", busy: state.submitting }
          : null
        : { label: method === "token" && kind === "claude" ? "Add account" : "Sign in", busy: state.phase === "starting" };

  return (
    <Dialog
      open={open}
      onClose={close}
      title={`Sign in to ${name}`}
      footer={
        <>
          <Button onClick={close}>Cancel</Button>
          {primary ? (
            <Button type="submit" form="signin-form" variant="primary" busy={primary.busy}>
              {primary.label}
            </Button>
          ) : null}
        </>
      }
    >
      <form id="signin-form" onSubmit={onSubmit} className="flex flex-col gap-4" noValidate>
        {state.phase === "failed" ? <Banner tone="danger">{state.error}</Banner> : null}
        {state.phase === "open" ? (
          <OpenLogin login={state.login} name={name} code={code} setCode={setCode} codeError={state.codeError} />
        ) : state.phase === "idle" || state.phase === "starting" ? (
          kind === "claude" ? (
            <>
              <Banner tone="warning" icon={TriangleAlert}>
                Anthropic can suspend a Claude account that it sees used outside its own apps.
              </Banner>
              <fieldset className="flex flex-col gap-2" disabled={state.phase === "starting"}>
                <legend className="mb-1 text-[13px] font-medium">Method</legend>
                <label className="flex items-center gap-2 text-sm">
                  <input type="radio" name="si-method" checked={method === "browser"} onChange={() => setMethod("browser")} className="radio radio-primary radio-sm" />
                  Browser sign-in
                </label>
                <label className="flex items-center gap-2 text-sm">
                  <input type="radio" name="si-method" checked={method === "token"} onChange={() => setMethod("token")} className="radio radio-primary radio-sm" />
                  Setup token
                </label>
              </fieldset>
              {method === "token" ? (
                <Field id="si-token" label="Setup token" hint="Run claude setup-token on a computer with a browser, then paste the token." error={tokenError}>
                  <TextInput
                    id="si-token"
                    type="password"
                    autoComplete="off"
                    spellCheck={false}
                    value={token}
                    onChange={(e) => setToken(e.target.value)}
                    aria-invalid={tokenError ? true : undefined}
                    aria-describedby={describedBy("si-token", true, Boolean(tokenError))}
                  />
                </Field>
              ) : null}
            </>
          ) : kind === "antigravity" ? (
            <Banner tone="warning" icon={TriangleAlert}>
              Google can suspend an account that it sees used outside Antigravity.
            </Banner>
          ) : (
            <p className="text-sm">You get a code to enter on the ChatGPT sign-in page.</p>
          )
        ) : null}
      </form>
    </Dialog>
  );
}
