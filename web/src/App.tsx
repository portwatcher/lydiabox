import { CloudOff } from "lucide-react";
import { Suspense, lazy, useCallback, useEffect, useState } from "react";
import { Navigate, Route, Routes } from "react-router";
import { ApiError, NetworkError, http } from "./api/http";
import { accessLogoutPath, auth } from "./api/system";
import type { AuthState } from "./api/types";
import { PageArea } from "./components/Outage";
import { StatusBar } from "./components/StatusBar";
import { ErrorView, LoadingView, StateView } from "./components/StateView";
import { ToastProvider } from "./components/Toast";
import { SystemProvider } from "./hooks/useSystem";
import { statusOutage } from "./lib/outage";
import { LoginPage } from "./pages/LoginPage";

const StudioPage = lazy(() => import("./pages/StudioPage"));
const DesktopPage = lazy(() => import("./pages/DesktopPage"));
const FilesPage = lazy(() => import("./pages/FilesPage"));
const ModelsPage = lazy(() => import("./pages/ModelsPage"));
const ConnectorsPage = lazy(() => import("./pages/ConnectorsPage"));

const AUTO_RETRY_MS = 5000;

function retriesByItself(error: unknown): boolean {
  return error instanceof NetworkError || (error instanceof ApiError && error.status >= 500);
}

type AuthStatus =
  | { kind: "checking" }
  | { kind: "signedOut"; session?: AuthState }
  | { kind: "signedIn"; session: AuthState }
  | { kind: "error"; error: unknown };

export function App() {
  const [state, setState] = useState<AuthStatus>({ kind: "checking" });

  const check = useCallback(() => {
    setState({ kind: "checking" });
    auth
      .session()
      .then((session) => setState(session.authenticated ? { kind: "signedIn", session } : { kind: "signedOut", session }))
      .catch((error: unknown) => setState({ kind: "error", error }));
  }, []);

  useEffect(() => {
    check();
    return http.onUnauthorized(() => setState({ kind: "signedOut" }));
  }, [check]);

  // The server does not answer (for example a cell that LydiaBox Cloud
  // stopped): try again by itself, without the loading screen, so the page
  // recovers when the server is back.
  useEffect(() => {
    if (state.kind !== "error" || !retriesByItself(state.error)) {
      return;
    }
    const id = setTimeout(() => {
      auth
        .session()
        .then((session) => setState(session.authenticated ? { kind: "signedIn", session } : { kind: "signedOut", session }))
        .catch((error: unknown) => setState({ kind: "error", error }));
    }, AUTO_RETRY_MS);
    return () => clearTimeout(id);
  }, [state]);

  const accessSignedIn = state.kind === "signedIn" && state.session.method === "access";
  const logout = useCallback(() => {
    auth
      .logout()
      .catch(() => undefined)
      .finally(() => {
        if (accessSignedIn) {
          // The admin cookie is gone; Cloudflare Access ends its own session.
          window.location.assign(accessLogoutPath);
          return;
        }
        setState({ kind: "signedOut" });
      });
  }, [accessSignedIn]);

  switch (state.kind) {
    case "checking":
      return <LoadingView label="Loading LydiaBox…" className="h-full" />;
    case "error":
      if (retriesByItself(state.error) && !statusOutage(state.error, false)) {
        // The server does not answer; the screen tries again by itself.
        return (
          <StateView icon={CloudOff} title="LydiaBox is not available" className="h-full">
            The server does not answer. LydiaBox tries again by itself.
          </StateView>
        );
      }
      return <ErrorView error={state.error} subsystem="LydiaBox" failed="LydiaBox not loaded" onRetry={check} className="h-full" />;
    case "signedOut":
      return <LoginPage session={state.session} onSignedIn={check} />;
    case "signedIn":
      return (
        <ToastProvider>
          <SystemProvider>
            <div className="flex h-dvh flex-col">
              <StatusBar email={state.session.email} onLogout={logout} />
              <PageArea>
                <Suspense fallback={<LoadingView label="Loading…" />}>
                  <Routes>
                    <Route path="/" element={<Navigate to="/studio" replace />} />
                    <Route path="/studio" element={<StudioPage />} />
                    <Route path="/desktop" element={<DesktopPage />} />
                    <Route path="/files" element={<FilesPage />} />
                    <Route path="/models" element={<ModelsPage />} />
                    <Route path="/connectors" element={<ConnectorsPage />} />
                    <Route path="*" element={<StateView title="Page not found">Choose a tab above.</StateView>} />
                  </Routes>
                </Suspense>
              </PageArea>
            </div>
          </SystemProvider>
        </ToastProvider>
      );
  }
}
