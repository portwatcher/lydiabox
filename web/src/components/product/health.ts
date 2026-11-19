import { CircleAlert, CircleCheck, CircleDashed, CircleMinus, LoaderCircle } from "lucide-react";
import type { LucideIcon } from "lucide-react";
import type { GatewayState } from "../../api/types";
import type { Tone } from "../ui";

/** The health of one upstream: a provider, a subscription, or the gateway. */
export type Health = "operational" | "pending" | "outage" | "off" | "unknown";

export const healthInfo: Record<Health, { label: string; tone: Tone; icon: LucideIcon }> = {
  operational: { label: "Operational", tone: "success", icon: CircleCheck },
  pending: { label: "Listing models", tone: "warning", icon: LoaderCircle },
  outage: { label: "Error", tone: "danger", icon: CircleAlert },
  off: { label: "Off", tone: "neutral", icon: CircleMinus },
  unknown: { label: "Unknown", tone: "neutral", icon: CircleDashed },
};

/**
 * The health of a magpie provider (api.md section 10): `status` is `ok`,
 * `pending`, `error`, or `disabled`. A provider that is switched off is off,
 * whatever its last check said.
 */
export function providerHealth(p: { enabled: boolean; status: string; lastError?: string }): Health {
  if (!p.enabled || p.status === "disabled") {
    return "off";
  }
  switch (p.status) {
    case "ok":
      return "operational";
    case "pending":
      return "pending";
    case "error":
      return "outage";
    default:
      return p.lastError ? "outage" : "unknown";
  }
}

export function gatewayHealth(state: GatewayState | null): Health {
  switch (state) {
    case "running":
      return "operational";
    case "starting":
      return "pending";
    case "error":
      return "outage";
    case "stopped":
      return "off";
    default:
      return "unknown";
  }
}

/** "1 error · 3 operational": the counts of the states that occur, worst first. */
export function healthSummary(list: Health[]): string {
  const order: Health[] = ["outage", "pending", "operational", "off", "unknown"];
  const words: Record<Health, string> = {
    outage: "with errors",
    pending: "listing models",
    operational: "operational",
    off: "off",
    unknown: "unknown",
  };
  return order
    .map((h) => [h, list.filter((x) => x === h).length] as const)
    .filter(([, n]) => n > 0)
    .map(([h, n]) => `${n} ${words[h]}`)
    .join(" · ");
}
