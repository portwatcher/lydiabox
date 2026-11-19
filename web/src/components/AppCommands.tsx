import { FolderTree, Moon, Monitor, Play, Plug, Sparkles, SquareTerminal } from "lucide-react";
import { useEffect, useMemo, useState } from "react";
import { useNavigate } from "react-router";
import { sessions as sessionsApi } from "../api/domains";
import type { Session } from "../api/types";
import { useSystem } from "../hooks/useSystem";
import { harnessLabel, sessionName } from "../lib/harness";
import { CommandPalette } from "./product/CommandPalette";
import { isPaletteKey } from "./product/commands";
import type { Command } from "./product/commands";

const pages = [
  { to: "/studio", label: "Studio", icon: SquareTerminal, keywords: "sessions harness terminal" },
  { to: "/desktop", label: "Desktop", icon: Monitor, keywords: "screen vm takeover chrome" },
  { to: "/files", label: "Files", icon: FolderTree, keywords: "workspace home upload" },
  { to: "/models", label: "Models", icon: Sparkles, keywords: "providers keys subscriptions gateway routing" },
  { to: "/connectors", label: "Connectors", icon: Plug, keywords: "github drive notion slack oauth remote access" },
];

/** The newest sessions first, at most `max`. */
export function recentSessions(list: Session[], max = 12): Session[] {
  return [...list].sort((a, b) => b.updatedAt.localeCompare(a.updatedAt)).slice(0, max);
}

export const isMac = typeof navigator !== "undefined" && /Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent);

/** The app-wide command palette: Cmd+K (macOS) or Ctrl+K opens it. */
export function AppCommands({ open, onOpenChange }: { open: boolean; onOpenChange: (open: boolean) => void }) {
  const navigate = useNavigate();
  const { vmState, vmControl, pending, runVM } = useSystem();
  const [sessions, setSessions] = useState<Session[] | null>(null);

  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if (isPaletteKey(event, event.target instanceof Element ? event.target : null, isMac)) {
        event.preventDefault();
        onOpenChange(!open);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [open, onOpenChange]);

  // Sessions load each time the palette opens; a failure leaves them out.
  useEffect(() => {
    if (!open) {
      return;
    }
    const controller = new AbortController();
    sessionsApi
      .list({}, controller.signal)
      .then((r) => setSessions(recentSessions(r.sessions)))
      .catch(() => setSessions((s) => s ?? []));
    return () => controller.abort();
  }, [open]);

  const commands = useMemo(() => {
    const out: Command[] = pages.map((p) => ({ id: `page-${p.to}`, group: "Pages", label: p.label, icon: p.icon, keywords: p.keywords, run: () => navigate(p.to) }));
    if (vmControl && !pending) {
      if (vmState === "STOPPED") {
        out.push({ id: "vm-start", group: "Actions", label: "Start the VM", icon: Play, keywords: "wake boot", run: () => void runVM("wake") });
      } else if (vmState === "PAUSED") {
        out.push({ id: "vm-resume", group: "Actions", label: "Resume the VM", icon: Play, keywords: "wake", run: () => void runVM("wake") });
      } else if (vmState === "RUNNING") {
        out.push({ id: "vm-hibernate", group: "Actions", label: "Hibernate the VM", icon: Moon, keywords: "pause sleep", run: () => void runVM("hibernate") });
      }
    }
    for (const s of sessions ?? []) {
      out.push({
        id: `session-${s.id}`,
        group: "Sessions",
        label: sessionName(s),
        meta: harnessLabel(s.harness),
        keywords: `${s.model} ${s.workspaceDir}`,
        icon: SquareTerminal,
        run: () => navigate(`/studio?session=${encodeURIComponent(s.id)}`),
      });
    }
    return out;
  }, [navigate, vmControl, pending, vmState, runVM, sessions]);

  return <CommandPalette open={open} onClose={() => onOpenChange(false)} commands={commands} loading={open && sessions === null} />;
}
