import { http } from "./http";
import type { AuthState, Status, VMStatus } from "./types";

// Section 2: authentication.
export const auth = {
  session: () => http.get<AuthState>("/api/v1/auth/session"),
  login: (token: string) => http.post<AuthState>("/api/v1/auth/login", { token }),
  logout: () => http.post<undefined>("/api/v1/auth/logout"),
};

/** Cloudflare Access ends its own session at this path of the app host. */
export const accessLogoutPath = "/cdn-cgi/access/logout";

// Section 3: status.
export const getStatus = (signal?: AbortSignal) => http.get<Status>("/api/v1/status", undefined, signal);

// Section 7: VM power.
export const vm = {
  status: (signal?: AbortSignal) => http.get<VMStatus>("/api/v1/vm/status", undefined, signal),
  wake: () => http.post<VMStatus>("/api/v1/vm/wake"),
  hibernate: () => http.post<VMStatus>("/api/v1/vm/hibernate"),
  stop: (force = false) => http.post<VMStatus>("/api/v1/vm/stop", { force }),
};
