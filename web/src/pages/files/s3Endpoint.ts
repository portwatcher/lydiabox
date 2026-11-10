/** Hosts that mean "every interface" in a listen address. */
const WILDCARD = new Set(["", "0.0.0.0", "::", "[::]"]);

/** Hosts that only the server itself can reach. */
const LOOPBACK = new Set(["127.0.0.1", "localhost", "::1", "[::1]"]);

export interface S3Endpoint {
  url: string;
  /** True when the gateway listens on loopback only. */
  serverOnly: boolean;
}

/**
 * Turns the gateway listen address (`host:port`) into a URL for S3 clients.
 * A wildcard host is replaced with the host the browser uses for this page.
 */
export function s3Endpoint(addr: string, pageHostname: string): S3Endpoint {
  const colon = addr.lastIndexOf(":");
  const host = colon >= 0 ? addr.slice(0, colon) : addr;
  const port = colon >= 0 ? addr.slice(colon + 1) : "";
  const wildcard = WILDCARD.has(host);
  let shown = wildcard ? pageHostname : host;
  if (shown.includes(":") && !shown.startsWith("[")) {
    shown = `[${shown}]`;
  }
  return {
    url: port ? `http://${shown}:${port}` : `http://${shown}`,
    serverOnly: !wildcard && LOOPBACK.has(host),
  };
}
