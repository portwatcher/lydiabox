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
