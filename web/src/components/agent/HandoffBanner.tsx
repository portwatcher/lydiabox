import { Hand, MonitorPlay, SquareTerminal } from "lucide-react";
import { Link } from "react-router";
import { Banner, Button, buttonClass } from "../ui";

/** The agent waits for an answer that only the terminal can give (an approval prompt). */
export function TerminalHandoff({ prompt, onOpenTerminal }: { prompt: string; onOpenTerminal: () => void }) {
  return (
    <Banner
      tone="warning"
      icon={Hand}
      action={
        <Button size="sm" variant="secondary" icon={SquareTerminal} onClick={onOpenTerminal}>
          Open terminal
        </Button>
      }
    >
      <p>The agent waits for your answer in the terminal.</p>
      {prompt ? <p className="font-mono text-xs break-words">{prompt}</p> : null}
    </Banner>
  );
}

/**
 * Before the first message, the harness shows a first-run screen (terms,
 * folder trust, sign-in method) in its terminal. Only the user answers it.
 */
export function SetupHandoff({ harness, prompt, held, onOpenTerminal }: { harness: string; prompt: string; held: boolean; onOpenTerminal: () => void }) {
  return (
    <div className="flex h-full min-h-48 flex-col items-center justify-center gap-3 px-6 py-10 text-center">
      <SquareTerminal aria-hidden className="size-6 text-warning" />
      <p className="text-sm font-medium text-balance">Finish the {harness} setup in the terminal</p>
      {prompt ? <p className="max-w-md font-mono text-xs break-words text-base-content/70">{prompt}</p> : null}
      {held ? <p className="text-sm text-base-content/70">Your message is sent when the setup is done.</p> : null}
      <Button variant="primary" icon={SquareTerminal} onClick={onOpenTerminal} className="mt-1">
        Open terminal
      </Button>
    </div>
  );
}

/** The agent works on the desktop: the user can watch it or take over there. */
export function DesktopHandoff() {
  return (
    <Banner
      tone="accent"
      icon={MonitorPlay}
      action={
        <Link to="/desktop" className={buttonClass("secondary", "sm")}>
          Take over
        </Link>
      }
    >
      The agent uses the desktop.
    </Banner>
  );
}
