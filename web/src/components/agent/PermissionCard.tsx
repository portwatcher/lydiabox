import { Check, ShieldQuestion, X } from "lucide-react";
import { useState } from "react";
import type { Permission } from "../../api/events";
import { Button } from "../ui";

function outcomeText(p: Permission): { allowed: boolean; text: string } {
  if (p.outcome === "cancelled") {
    return { allowed: false, text: "Cancelled" };
  }
  const o = p.options.find((x) => x.id === p.outcome);
  const allowed = o ? o.kind.startsWith("allow") : false;
  return { allowed, text: o?.name ?? p.outcome };
}

/**
 * A request of the agent to run a tool: its title and one button per
 * answer. An answered request folds to one line with the answer.
 */
export function PermissionCard({ permission, onAnswer }: { permission: Permission; onAnswer?: (requestId: string, optionId: string) => Promise<void> }) {
  const [busy, setBusy] = useState<string | null>(null);
  if (permission.outcome) {
    const { allowed, text } = outcomeText(permission);
    const Icon = allowed ? Check : X;
    return (
      <p className="flex min-w-0 items-center gap-2 text-sm text-base-content/70">
        <Icon aria-hidden className={allowed ? "size-4 shrink-0 text-success" : "size-4 shrink-0 text-error"} />
        <span className="shrink-0">{text}</span>
        <span className="min-w-0 truncate font-mono text-xs" title={permission.title}>
          {permission.title}
        </span>
      </p>
    );
  }
  const answer = async (optionId: string) => {
    if (!onAnswer) {
      return;
    }
    setBusy(optionId);
    try {
      await onAnswer(permission.id, optionId);
    } finally {
      setBusy(null);
    }
  };
  return (
    <section role="group" aria-label="Permission request" className="flex min-w-0 flex-col gap-2 rounded-box border border-warning/50 bg-warning/5 p-3">
      <p className="flex min-w-0 items-start gap-2 text-sm">
        <ShieldQuestion aria-hidden className="mt-0.5 size-4 shrink-0 text-warning" />
        <span className="min-w-0 font-medium break-words">{permission.title || "The agent asks to run a tool"}</span>
      </p>
      <div className="flex flex-wrap gap-2">
        {permission.options.map((o) => (
          <Button
            key={o.id}
            size="sm"
            variant={o.kind === "allow_once" ? "primary" : o.kind.startsWith("allow") ? "secondary" : "ghost"}
            busy={busy === o.id}
            disabled={busy !== null || !onAnswer}
            onClick={() => void answer(o.id)}
          >
            {o.name}
          </Button>
        ))}
      </div>
    </section>
  );
}
