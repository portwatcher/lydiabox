# LydiaBox web workstation

The browser UI embedded in `lydiabox`: Vite, React 18, TypeScript (strict),
Tailwind CSS 4, Lucide icons, xterm.js, CodeMirror 6, and React Router. The
HTTP and WebSocket contract it uses is [docs/api.md](../docs/api.md).

## Development

Requirements: Node.js 22 or later.

```sh
cd web
npm ci
npm run dev        # http://localhost:5173
```

The dev server proxies `/api` (including WebSockets) and `/webdav` to
`http://127.0.0.1:8080`, where `./dist/lydiabox up` listens by default. Set
`LYDIABOX_DEV_BACKEND=http://host:port` to use another backend. Sign in with
the admin token (`$LYDIABOX_DATA_DIR/state/auth.token`).

| Script | Does |
| :--- | :--- |
| `npm run dev` | Vite dev server with the backend proxy |
| `npm run build` | Type check, then build into `web/dist` |
| `npm run typecheck` | `tsc --noEmit` |
| `npm run lint` | ESLint with typescript-eslint (no `any`, no `as unknown`, braces required) |
| `npm run test` | Vitest: API client, WebSocket clients, reducers |

`make web-build` (repository root) runs `npm ci && npm run build` and copies
`web/dist` into `internal/webui/dist`; the next `make build` embeds it.

## Layout

```text
src/api/          typed REST client, error envelope, PTY and desktop WebSocket clients
src/components/   app shell (status bar, VM power), dialogs, state views, form controls
src/hooks/        polling resources, system status context, model options
src/lib/          formatting (Intl), path helpers, video coordinate mapping
src/pages/studio      sessions list, xterm terminal, new session, harness switch
src/pages/desktop     WebRTC stream, HUD overlay, takeover input, activity feed
src/pages/files       folder tree, listing, upload, CodeMirror editor, WebDAV
src/pages/models      gateway, providers, routing groups, harness bindings
src/pages/connectors  connector cards, OAuth and token drawers, audit log
```

Colors are semantic tokens in `src/index.css` (`bg`, `surface`, `fg`,
`accent-*`, `danger-*`, ...); components never use the default Tailwind palette.

Every page handles a backend that does not have its subsystem yet: `404` on a
route or `501 not_implemented` shows "not available in this build", and
`503 unavailable` shows "not running" with a retry.
