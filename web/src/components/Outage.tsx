import { CloudOff, Monitor } from "lucide-react";
import { useEffect, useRef } from "react";
import type { ReactNode } from "react";
import { useSystem } from "../hooks/useSystem";
import { splitLink } from "../lib/outage";
import type { Outage } from "../lib/outage";
import { StateView } from "./StateView";
import { Banner, cx } from "./ui";

const titles: Record<Outage["kind"], string> = {
  stopped: "The VM is stopped",
  starting: "The VM is starting",
  unknown: "The VM status is not available",
};

function outageBody(outage: Outage): { text: string; href: string | null } {
  switch (outage.kind) {
    case "unknown":
      return { text: "LydiaBox tries again by itself.", href: null };
    case "starting":
      // The server's text says to try again; the page does that by itself.
      return { text: "LydiaBox connects again by itself.", href: null };
  }
  return splitLink(outage.message);
}

function DashboardLink({ href }: { href: string }) {
  return (
    <a href={href} className="shrink-0 text-sm font-medium underline underline-offset-2">
      Open the dashboard
    </a>
  );
}

/** One page-sized state for an outage: the VM state as the title, the server's text, its dashboard link. */
export function OutageView({ outage }: { outage: Outage }) {
  const { text, href } = outageBody(outage);
  return (
    <StateView icon={outage.kind === "unknown" ? CloudOff : Monitor} title={titles[outage.kind]} action={href ? <DashboardLink href={href} /> : undefined}>
      {text}
    </StateView>
  );
}

/**
 * The page area during an outage: one banner with the VM state and the
 * server's text, and the page below it kept (so that unsaved text stays)
 * but dimmed and inert, because no action of it can work.
 */
export function PageArea({ children }: { children: ReactNode }) {
  const { outage } = useSystem();
  const pageRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (pageRef.current) {
      pageRef.current.inert = outage !== null;
    }
  }, [outage]);
  const body = outage ? outageBody(outage) : null;
  return (
    <main className="flex min-h-0 flex-1 flex-col">
      {outage && body ? (
        <div className="border-b border-border-subtle px-3 py-2 sm:px-4">
          <Banner tone="warning" icon={outage.kind === "unknown" ? CloudOff : Monitor} action={body.href ? <DashboardLink href={body.href} /> : undefined}>
            <span className="font-medium">{titles[outage.kind]}.</span> {body.text}
          </Banner>
        </div>
      ) : null}
      <div ref={pageRef} className={cx("min-h-0 flex-1", outage && "opacity-50 select-none")} aria-hidden={outage ? true : undefined}>
        {children}
      </div>
    </main>
  );
}
