import { Globe } from "lucide-react";
import type { ReactNode } from "react";
import type { RemoteAccessStatus } from "../../api/types";
import type { Health } from "../../components/product/health";
import { HealthLabel, Mark } from "../../components/product/Marks";
import { CommandBlock, SnippetSteps } from "../../components/product/Snippet";
import { Panel } from "../../components/ui";

export const setupCommand = "lydiabox cloudflare setup -hostname agent.example.com -email me@example.com";

const tunnelHealth: Record<string, Health> = { connected: "operational", connecting: "pending", error: "outage", stopped: "off" };
const tunnelLabel: Record<string, string> = { connected: "Connected", connecting: "Connecting", error: "Error", stopped: "Stopped" };

/**
 * Whether the page shows remote access: always when it is on; when it is
 * off, only on a host that signs in with the admin token. A Cloudflare
 * Access sign-in means that the platform runs remote access itself.
 */
export function showRemoteAccess(status: RemoteAccessStatus | undefined, signedInWithAccess: boolean): boolean {
  if (!status) {
    return false;
  }
  return status.configured || !signedInWithAccess;
}

function Fact({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="flex min-w-0 flex-col gap-0.5">
      <dt className="text-xs text-base-content/70">{label}</dt>
      <dd className="truncate text-[13px]">{children}</dd>
    </div>
  );
}

/** Remote access through Cloudflare Zero Trust: its state, or the commands that set it up. */
export function RemoteAccess({ status }: { status: RemoteAccessStatus }) {
  const tunnel = status.tunnel;
  return (
    <Panel
      title={
        <span className="flex items-center gap-2">
          <Mark name="Cloudflare" icon={Globe} size="sm" />
          Remote access
        </span>
      }
      actions={status.configured ? <HealthLabel health={tunnel ? (tunnelHealth[tunnel.state] ?? "unknown") : "operational"} label={tunnel ? (tunnelLabel[tunnel.state] ?? tunnel.state) : "On"} /> : null}
    >
      {status.configured ? (
        <div className="flex flex-col gap-3 p-4">
          <dl className="grid gap-x-6 gap-y-3 sm:grid-cols-2 lg:grid-cols-3">
            {status.hostname ? (
              <Fact label="Address">
                <a className="link link-hover" href={`https://${status.hostname}`} target="_blank" rel="noreferrer">
                  {status.hostname}
                </a>
              </Fact>
            ) : null}
            {status.emails && status.emails.length > 0 ? <Fact label="Allowed">{status.emails.join(", ")}</Fact> : null}
            {status.teamDomain ? <Fact label="Access team">{status.teamDomain}</Fact> : null}
            {status.serviceTokenClientId ? (
              <Fact label="Service token">
                <code className="text-xs">{status.serviceTokenClientId}</code>
              </Fact>
            ) : null}
            {status.bypassPaths && status.bypassPaths.length > 0 ? <Fact label="Token only">{status.bypassPaths.join(", ")}</Fact> : null}
            <Fact label="Desktop relay">{status.turnInUse ? "Cloudflare TURN" : status.turnKeyId ? "TURN key not in use" : "Direct"}</Fact>
          </dl>
          {tunnel?.lastError ? <p className="rounded-field bg-error/10 px-3 py-2 text-xs break-words text-error">{tunnel.lastError}</p> : null}
        </div>
      ) : (
        <div className="p-4">
          <SnippetSteps
            steps={[
              {
                title: "Give the host a Cloudflare API token",
                children: (
                  <CommandBlock
                    label="environment variables"
                    code={["# Account ID and an API token with the permissions in docs/remote-access-manual-test.md", "export LYDIABOX_CLOUDFLARE_ACCOUNT_ID=…", "export LYDIABOX_CLOUDFLARE_API_TOKEN=…"].join("\n")}
                  />
                ),
              },
              {
                title: "Create the tunnel and the Access login",
                children: <CommandBlock label="setup command" code={setupCommand} />,
              },
              {
                title: "Restart LydiaBox",
                children: <CommandBlock label="start command" code="lydiabox up" />,
              },
            ]}
          />
        </div>
      )}
    </Panel>
  );
}
