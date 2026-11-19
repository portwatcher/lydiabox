/** The part of the Web Speech API recognition object that dictation uses. */
export interface Recognition {
  lang: string;
  continuous: boolean;
  interimResults: boolean;
  start(): void;
  stop(): void;
  abort(): void;
  onresult: ((event: RecognitionResultEvent) => void) | null;
  onerror: ((event: { error: string }) => void) | null;
  onend: (() => void) | null;
}

export interface RecognitionResultEvent {
  resultIndex: number;
  results: ArrayLike<{ isFinal: boolean; length: number; [index: number]: { transcript: string } }>;
}

export type RecognitionCtor = new () => Recognition;

/** The browser's speech recognition, or null when it has none (Firefox). */
export function recognitionCtor(win: object | undefined = typeof window === "undefined" ? undefined : window): RecognitionCtor | null {
  if (!win) {
    return null;
  }
  const w = win as { SpeechRecognition?: RecognitionCtor; webkitSpeechRecognition?: RecognitionCtor };
  return w.SpeechRecognition ?? w.webkitSpeechRecognition ?? null;
}

/** Splits the results of a recognition event into final and interim text. */
export function transcriptOf(event: RecognitionResultEvent): { final: string; interim: string } {
  let final = "";
  let interim = "";
  for (let i = event.resultIndex; i < event.results.length; i++) {
    const r = event.results[i];
    const text = r.length > 0 ? r[0].transcript : "";
    if (r.isFinal) {
      final += text;
    } else {
      interim += text;
    }
  }
  return { final, interim };
}
