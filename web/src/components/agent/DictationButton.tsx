import { Mic, MicOff, Square } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { cx } from "../ui";
import { recognitionCtor, transcriptOf } from "./speech";
import type { Recognition } from "./speech";

type DictationState = "idle" | "listening" | "denied" | "error";

export interface DictationButtonProps {
  /** Final words, to append to the field. */
  onFinal: (text: string) => void;
  /** Words not final yet ("" when none). */
  onInterim: (text: string) => void;
  disabled?: boolean;
}

/**
 * Dictation through the browser's speech recognition. It is absent when the
 * browser has none. Escape on the button stops listening.
 */
export function DictationButton({ onFinal, onInterim, disabled }: DictationButtonProps) {
  const [state, setState] = useState<DictationState>("idle");
  const rec = useRef<Recognition | null>(null);
  // The recognition outlives renders: it calls the latest callbacks.
  const handlers = useRef({ onFinal, onInterim });
  handlers.current = { onFinal, onInterim };
  const Ctor = recognitionCtor();

  useEffect(() => () => rec.current?.abort(), []);

  if (!Ctor) {
    return null;
  }

  const stop = () => {
    rec.current?.stop();
  };

  const start = () => {
    const r = new Ctor();
    r.lang = navigator.language || "en-US";
    r.continuous = true;
    r.interimResults = true;
    r.onresult = (e) => {
      const { final, interim } = transcriptOf(e);
      if (final) {
        handlers.current.onFinal(final);
      }
      handlers.current.onInterim(interim);
    };
    r.onerror = (e) => {
      setState(e.error === "not-allowed" || e.error === "service-not-allowed" ? "denied" : e.error === "no-speech" || e.error === "aborted" ? "idle" : "error");
    };
    r.onend = () => {
      handlers.current.onInterim("");
      rec.current = null;
      setState((s) => (s === "listening" ? "idle" : s));
    };
    rec.current = r;
    try {
      r.start();
      setState("listening");
    } catch {
      setState("error");
    }
  };

  const listening = state === "listening";
  const label = listening ? "Stop dictation" : state === "denied" ? "Microphone blocked: allow it in the browser to dictate" : state === "error" ? "Dictation failed: try again" : "Dictate";
  const Icon = listening ? Square : state === "denied" ? MicOff : Mic;
  return (
    <button
      type="button"
      onClick={listening ? stop : start}
      onKeyDown={(e) => {
        if (e.key === "Escape" && listening) {
          e.preventDefault();
          rec.current?.abort();
        }
      }}
      disabled={disabled}
      aria-pressed={listening}
      aria-label={label}
      title={label}
      className={cx(
        "btn btn-square btn-ghost btn-sm hover:bg-base-content/10",
        listening ? "text-error" : state === "denied" || state === "error" ? "text-warning" : "text-base-content/70",
      )}
    >
      <Icon aria-hidden className={cx("size-4", listening && "motion-safe:animate-pulse")} />
    </button>
  );
}
