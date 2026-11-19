import { createElement } from "react";
import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it } from "vitest";
import type { Connector, ConnectorAccount, RemoteAccessStatus } from "../../api/types";
import { ConnectorCard, connectorState } from "./ConnectorCard";
import { RemoteAccess, setupCommand, showRemoteAccess } from "./RemoteAccess";

const account = (partial: Partial<ConnectorAccount> = {}): ConnectorAccount => ({
  accountLabel: "octo",
  authKind: "oauth2",
  scopes: ["repo", "read:user"],
  expiresAt: "",
  createdAt: "",
  updatedAt: "",
  status: "ok",
  ...partial,
});

const connector = (partial: Partial<Connector> = {}): Connector => ({
  provider: "github",
  displayName: "GitHub",
  oauthConfigured: true,
  oauthSource: "stored",
  supportsOAuth: true,
  supportsToken: true,
  defaultScopes: [],
  accounts: [],
  ...partial,
});

const noop = () => undefined;
const props = { connecting: false, error: null, onConnect: noop, onConfigure: noop, onPasteToken: noop, onDisconnect: () => Promise.resolve(), onAudit: noop };

describe("connector state", () => {
  it("orders connecting, error, accounts, then setup", () => {
    expect(connectorState(connector(), true, "x")).toBe("connecting");
    expect(connectorState(connector(), false, "x")).toBe("error");
    expect(connectorState(connector({ accounts: [account()] }), false, null)).toBe("connected");
    expect(connectorState(connector({ accounts: [account(), account({ status: "expired" })] }), false, null)).toBe("attention");
    expect(connectorState(connector(), false, null)).toBe("ready");
    expect(connectorState(connector({ oauthConfigured: false, supportsToken: false }), false, null)).toBe("setup");
    expect(connectorState(connector({ oauthConfigured: false }), false, null)).toBe("ready");
  });

  it("shows the account line and the connected toggle", () => {
    const html = renderToStaticMarkup(createElement(ConnectorCard, { ...props, connector: connector({ accounts: [account()] }) }));
    expect(html).toContain("octo · 2 scopes");
    expect(html).toContain('aria-expanded="false"');
    expect(html).toContain("Connected");
  });

  it("offers Connect, a token, or setup by what the connector supports", () => {
    expect(renderToStaticMarkup(createElement(ConnectorCard, { ...props, connector: connector() }))).toContain("Connect");
    const token = renderToStaticMarkup(createElement(ConnectorCard, { ...props, connector: connector({ provider: "notion", oauthConfigured: false }) }));
    expect(token).toContain("Paste token");
    const setup = renderToStaticMarkup(createElement(ConnectorCard, { ...props, connector: connector({ oauthConfigured: false, supportsToken: false }) }));
    expect(setup).toContain("Set up");
  });

  it("shows the sign-in error with a retry and a progress line while connecting", () => {
    const failed = renderToStaticMarkup(createElement(ConnectorCard, { ...props, error: "Access was denied.", connector: connector() }));
    expect(failed).toContain("Access was denied.");
    expect(failed).toContain("Try again");
    const busy = renderToStaticMarkup(createElement(ConnectorCard, { ...props, connecting: true, connector: connector() }));
    expect(busy).toContain("<progress");
    expect(busy).toContain("Authorizing");
  });
});

describe("remote access", () => {
  const off: RemoteAccessStatus = { provider: "cloudflare", configured: false, turnInUse: false };

  it("hides the setup on a host that signs in through Cloudflare Access", () => {
    expect(showRemoteAccess(undefined, false)).toBe(false);
    expect(showRemoteAccess(off, false)).toBe(true);
    expect(showRemoteAccess(off, true)).toBe(false);
    expect(showRemoteAccess({ ...off, configured: true }, true)).toBe(true);
  });

  it("shows the setup commands when it is off", () => {
    const html = renderToStaticMarkup(createElement(RemoteAccess, { status: off }));
    expect(html).toContain(setupCommand);
    expect(html).toContain("lydiabox up");
  });

  it("shows the address and the tunnel state when it is on", () => {
    const html = renderToStaticMarkup(
      createElement(RemoteAccess, {
        status: { ...off, configured: true, hostname: "agent.example.com", emails: ["me@example.com"], turnInUse: true, tunnel: { state: "error", lastError: "no binary", restarts: 2, startedAt: "" } },
      }),
    );
    expect(html).toContain("https://agent.example.com");
    expect(html).toContain("Cloudflare TURN");
    expect(html).toContain("no binary");
    expect(html).not.toContain(setupCommand);
  });
});
