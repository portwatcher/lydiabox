import { SquareTerminal } from "lucide-react";
import { useState } from "react";
import { useNavigate } from "react-router";
import { describeError } from "../../api/errors";
import { sessions } from "../../api/domains";
import type { HarnessID } from "../../api/types";
import { useToast } from "../../components/Toast";
import { Mark } from "../../components/product/Marks";
import { Button, Panel } from "../../components/ui";
import { harnessLabel } from "../../lib/harness";

/** The sign-in command of each harness, as the VM terminal runs it (internal/harness/signin.go). */
const signInCommands: Record<Exclude<HarnessID, "shell">, string> = {
  "claude-code": "claude auth login",
  codex: "codex login --device-auth",
  opencode: "opencode auth login",
  antigravity: "agy",
  dsh: "~/.dsh/.env",
};

const order = Object.keys(signInCommands) as Exclude<HarnessID, "shell">[];

/**
 * Guest-native mode: each harness signs in itself inside the VM, and its
 * tokens stay there. A row opens a VM terminal that runs the sign-in.
 */
export function NativeSignIn() {
  const toast = useToast();
  const navigate = useNavigate();
  const [busy, setBusy] = useState<HarnessID | null>(null);

  const signIn = async (harness: HarnessID) => {
    setBusy(harness);
    try {
      const s = await sessions.create({
        harness: "shell",
        signIn: harness,
        workspaceDir: "/home/agent",
        title: `Sign in: ${harnessLabel(harness)}`,
        cols: 120,
        rows: 36,
      });
      navigate(`/studio?session=${encodeURIComponent(s.id)}&view=terminal`);
    } catch (err) {
      toast.error(describeError(err, "Terminal not opened"));
    } finally {
      setBusy(null);
    }
  };

  return (
    <Panel title="Sign in inside your VM">
      <ul className="divide-y divide-base-content/10">
        {order.map((h) => (
          <li key={h} className="flex flex-wrap items-center gap-3 px-4 py-3">
            <Mark name={harnessLabel(h)} />
            <div className="flex min-w-0 flex-1 flex-col">
              <span className="text-sm font-semibold">{harnessLabel(h)}</span>
              <code className="truncate text-xs text-base-content/70">{signInCommands[h]}</code>
            </div>
            <Button size="sm" icon={SquareTerminal} busy={busy === h} disabled={busy !== null} onClick={() => void signIn(h)}>
              Sign in
            </Button>
          </li>
        ))}
      </ul>
    </Panel>
  );
}
