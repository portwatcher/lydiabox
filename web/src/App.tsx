import { Suspense, lazy, useCallback, useEffect, useState } from "react";
import { Navigate, Route, Routes } from "react-router";
import { http } from "./api/http";
import { auth } from "./api/system";
import { StatusBar } from "./components/StatusBar";
import { ErrorView, LoadingView, StateView } from "./components/StateView";
import { ToastProvider } from "./components/Toast";
import { SystemProvider } from "./hooks/useSystem";
import { LoginPage } from "./pages/LoginPage";

const StudioPage = lazy(() => import("./pages/StudioPage"));
const DesktopPage = lazy(() => import("./pages/DesktopPage"));
const FilesPage = lazy(() => import("./pages/FilesPage"));
const ModelsPage = lazy(() => import("./pages/ModelsPage"));
const ConnectorsPage = lazy(() => import("./pages/ConnectorsPage"));

type AuthStatus = { kind: "checking" } | { kind: "signedOut" } | { kind: "signedIn" } | { kind: "error"; error: unknown };

export function App() {
  const [state, setState] = useState<AuthStatus>({ kind: "checking" });

  const check = useCallback(() => {
    setState({ kind: "checking" });
    auth
      .session()
      .then((s) => setState({ kind: s.authenticated ? "signedIn" : "signedOut" }))
      .catch((error: unknown) => setState({ kind: "error", error }));
  }, []);

  useEffect(() => {
    check();
    return http.onUnauthorized(() => setState({ kind: "signedOut" }));
  }, [check]);

  const logout = useCallback(() => {
    auth
      .logout()
      .catch(() => undefined)
      .finally(() => setState({ kind: "signedOut" }));
  }, []);

  switch (state.kind) {
    case "checking":
      return <LoadingView label="Loading LydiaBox…" className="h-full" />;
    case "error":
      return <ErrorView error={state.error} subsystem="LydiaBox" failed="LydiaBox not loaded" onRetry={check} className="h-full" />;
    case "signedOut":
      return <LoginPage onSignedIn={() => setState({ kind: "signedIn" })} />;
    case "signedIn":
      return (
        <ToastProvider>
          <SystemProvider>
            <div className="flex h-dvh flex-col">
              <StatusBar onLogout={logout} />
              <main className="min-h-0 flex-1">
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
              </main>
            </div>
          </SystemProvider>
        </ToastProvider>
      );
  }
}
