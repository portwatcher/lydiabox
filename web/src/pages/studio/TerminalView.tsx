import { FitAddon } from "@xterm/addon-fit";
import { WebLinksAddon } from "@xterm/addon-web-links";
import { Terminal } from "@xterm/xterm";
import type { ITheme } from "@xterm/xterm";
import "@xterm/xterm/css/xterm.css";
import { RotateCw } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { PAUSED_CODE, PtyClient } from "../../api/pty";
import { STOPPED_CODE } from "../../api/socket";
import type { SocketState } from "../../api/socket";
import { Banner, Button, cx } from "../../components/ui";
import { useSystem } from "../../hooks/useSystem";

/** Reads the color tokens so the terminal matches the app palette. */
function terminalTheme(): ITheme {
  const css = getComputedStyle(document.documentElement);
  const token = (name: string) => css.getPropertyValue(name).trim();
  return {
    background: token("--color-bg"),
    foreground: token("--color-fg"),
    cursor: token("--color-fg"),
    cursorAccent: token("--color-bg"),
    selectionBackground: token("--color-accent-subtle"),
    black: "#1c1f24",
    red: "#fc918e",
    green: "#6ad895",
    yellow: "#f7c15f",
    blue: "#8ebcff",
    magenta: "#d6a4ff",
    cyan: "#6fd6dc",
    white: "#d5d9de",
    brightBlack: "#6b7079",
    brightRed: "#ffb3b0",
    brightGreen: "#93e8b3",
    brightYellow: "#ffd98a",
    brightBlue: "#b5d3ff",
    brightMagenta: "#e6c4ff",
    brightCyan: "#9de8ec",
    brightWhite: "#eceff2",
  };
}

const stateLabels: Record<SocketState, string> = {
  connecting: "Connecting…",
  open: "Connected",
  reconnecting: "Reconnecting…",
  closed: "Disconnected",
  failed: "Connection lost",
};

export interface TerminalViewProps {
  /** Session to attach to when the view mounts. */
  sessionId: string;
  onExit: () => void;
  /** Reports the terminal size, used for new sessions. */
  onSize?: (cols: number, rows: number) => void;
}

export function TerminalView({ sessionId, onExit, onSize }: TerminalViewProps) {
  const hostRef = useRef<HTMLDivElement>(null);
  const clientRef = useRef<PtyClient | null>(null);
  const initialId = useRef(sessionId);
  const callbacks = useRef({ onExit, onSize });
  useEffect(() => {
    callbacks.current = { onExit, onSize };
  });
  const [conn, setConn] = useState<SocketState>("connecting");
  const [exited, setExited] = useState<string | null>(null);
  const [serverError, setServerError] = useState<string | null>(null);
  const [vmStopped, setVmStopped] = useState(false);
  const [vmPaused, setVmPaused] = useState(false);
  const { vmState } = useSystem();
  const leftRunning = useRef(false);

  // A Hibernate detached the terminal; its process lives on in the paused
  // VM. Attach again once the VM has left RUNNING and runs again.
  useEffect(() => {
    if (!vmPaused) {
      leftRunning.current = false;
      return;
    }
    if (vmState !== "RUNNING") {
      leftRunning.current = true;
      return;
    }
    if (leftRunning.current) {
      leftRunning.current = false;
      setVmPaused(false);
      clientRef.current?.retry();
    }
  }, [vmPaused, vmState]);

  useEffect(() => {
    const host = hostRef.current;
    if (!host) {
      return;
    }
    const term = new Terminal({
      fontFamily: getComputedStyle(document.documentElement).getPropertyValue("--font-mono").trim() || "monospace",
      fontSize: 13,
      lineHeight: 1.15,
      cursorBlink: true,
      scrollback: 10_000,
      theme: terminalTheme(),
      allowProposedApi: false,
    });
    const fit = new FitAddon();
    term.loadAddon(fit);
    term.loadAddon(new WebLinksAddon());
    term.open(host);

    const client = new PtyClient(initialId.current, {
      onOutput: (data) => term.write(data),
      onHello: () => {
        // The server replays its ring buffer after hello; start from a clean screen.
        term.reset();
        setServerError(null);
        setExited(null);
        setVmPaused(false);
      },
      onExit: (msg) => {
        const detail = msg.signal ? `signal ${msg.signal}` : `code ${msg.code}`;
        term.write(`\r\n\x1b[2m[process exited with ${detail}]\x1b[0m\r\n`);
        setExited(detail);
        callbacks.current.onExit();
      },
      onError: (msg) => {
        if (msg.code === STOPPED_CODE) {
          setVmStopped(true);
          return;
        }
        if (msg.code === PAUSED_CODE) {
          setVmPaused(true);
          return;
        }
        setServerError(msg.message || msg.code);
      },
      onState: setConn,
    });
    clientRef.current = client;

    const encoder = new TextEncoder();
    const dataSub = term.onData((data) => client.write(encoder.encode(data)));
    const binarySub = term.onBinary((data) => {
      const bytes = new Uint8Array(data.length);
      for (let i = 0; i < data.length; i += 1) {
        bytes[i] = data.charCodeAt(i) & 0xff;
      }
      client.write(bytes);
    });

    const resize = () => {
      try {
        fit.fit();
      } catch {
        return;
      }
      client.resize(term.cols, term.rows);
      callbacks.current.onSize?.(term.cols, term.rows);
    };
    const observer = new ResizeObserver(() => resize());
    observer.observe(host);
    resize();
    client.connect();
    term.focus();

    return () => {
      observer.disconnect();
      dataSub.dispose();
      binarySub.dispose();
      client.close();
      clientRef.current = null;
      term.dispose();
    };
    // The view attaches once per mount; the parent remounts it for a new room.
  }, []);

  const showState = conn !== "open" && !exited && !vmStopped && !vmPaused;

  return (
    <div className="flex h-full min-h-0 flex-col">
      {serverError ? (
        <div className="px-3 pt-3">
          <Banner tone="danger">{serverError}</Banner>
        </div>
      ) : null}
      {vmStopped ? (
        <div className="px-3 pt-3">
          <Banner tone="neutral">The VM is stopped.</Banner>
        </div>
      ) : null}
      {vmPaused ? (
        <div className="px-3 pt-3">
          <Banner tone="neutral">The VM is hibernating.</Banner>
        </div>
      ) : null}
      {showState ? (
        <div className="px-3 pt-3">
          <Banner
            tone={conn === "failed" ? "danger" : "warning"}
            action={
              conn === "failed" ? (
                <Button size="sm" icon={RotateCw} onClick={() => clientRef.current?.retry()}>
                  Reconnect
                </Button>
              ) : null
            }
          >
            {conn === "failed" ? "Connection lost. The session may have ended or the VM stopped." : stateLabels[conn]}
          </Banner>
        </div>
      ) : null}
      <div
        ref={hostRef}
        role="application"
        aria-label="Terminal"
        className={cx("min-h-0 flex-1 bg-bg px-2 py-2", exited && "opacity-80")}
      />
    </div>
  );
}
