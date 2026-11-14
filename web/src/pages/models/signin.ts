import type { SubscriptionLogin } from "../../api/types";

/** The subscription sign-in dialog. Tokens never pass through this state. */
export type SignInState =
  | { phase: "idle" }
  | { phase: "starting" }
  | { phase: "open"; login: SubscriptionLogin; submitting: boolean; codeError: string | null }
  | { phase: "done"; account: string }
  | { phase: "failed"; error: string };

export type SignInAction =
  | { type: "start" }
  | { type: "update"; login: SubscriptionLogin }
  | { type: "submit" }
  | { type: "codeRefused"; error: string }
  | { type: "fail"; error: string }
  | { type: "reset" };

export const initialSignIn: SignInState = { phase: "idle" };

const OPEN_STATES = new Set(["starting", "installing", "waiting"]);

export function signInReducer(state: SignInState, action: SignInAction): SignInState {
  switch (action.type) {
    case "start":
      return { phase: "starting" };
    case "update": {
      const { login } = action;
      // A poll that answers after the user moved on is stale.
      if (state.phase === "open" && state.login.id !== login.id) {
        return state;
      }
      if (state.phase === "idle" || state.phase === "done" || state.phase === "failed") {
        return state;
      }
      if (OPEN_STATES.has(login.state)) {
        const codeError = state.phase === "open" && !state.submitting ? state.codeError : null;
        return { phase: "open", login, submitting: false, codeError };
      }
      if (login.state === "done") {
        return { phase: "done", account: login.account };
      }
      if (login.state === "canceled") {
        return initialSignIn;
      }
      return { phase: "failed", error: login.error || "The sign-in did not finish. Try again." };
    }
    case "submit":
      return state.phase === "open" ? { ...state, submitting: true, codeError: null } : state;
    case "codeRefused":
      return state.phase === "open" ? { ...state, submitting: false, codeError: action.error } : state;
    case "fail":
      return { phase: "failed", error: action.error };
    case "reset":
      return initialSignIn;
  }
}

/** The sign-in still waits on magpie or the user, so the dialog polls it. */
export function shouldPoll(state: SignInState): boolean {
  return state.phase === "open" && !state.submitting && OPEN_STATES.has(state.login.state);
}

/** The ID of a sign-in to cancel when the dialog closes, or null. */
export function pendingLoginId(state: SignInState): string | null {
  return state.phase === "open" && OPEN_STATES.has(state.login.state) ? state.login.id : null;
}

export type LoginStep = "installing" | "device" | "code" | "browser";

/** What the user does next for an open sign-in. */
export function loginStep(login: SubscriptionLogin): LoginStep {
  if (login.state !== "waiting") {
    return "installing";
  }
  if (login.userCode) {
    return "device";
  }
  return login.needsCode ? "code" : "browser";
}

/** Setup tokens are one line of non-space characters. */
export function setupTokenError(token: string): string | null {
  const t = token.trim();
  if (!t) {
    return "Paste the token that claude setup-token prints.";
  }
  if (/\s/.test(t)) {
    return "Paste the token as one line, without spaces.";
  }
  return null;
}
