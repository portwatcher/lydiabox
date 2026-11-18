import { closeBrackets, closeBracketsKeymap } from "@codemirror/autocomplete";
import { defaultKeymap, history, historyKeymap, indentWithTab } from "@codemirror/commands";
import { HighlightStyle, bracketMatching, foldGutter, indentOnInput, syntaxHighlighting } from "@codemirror/language";
import type { LanguageSupport } from "@codemirror/language";
import { highlightSelectionMatches, searchKeymap } from "@codemirror/search";
import { Compartment, EditorState } from "@codemirror/state";
import {
  EditorView,
  drawSelection,
  highlightActiveLine,
  highlightActiveLineGutter,
  keymap,
  lineNumbers,
} from "@codemirror/view";
import { tags } from "@lezer/highlight";
import { useEffect, useRef } from "react";
import { extension } from "../../lib/paths";

async function languageFor(path: string): Promise<LanguageSupport | null> {
  switch (extension(path)) {
    case "js":
    case "mjs":
    case "cjs":
    case "jsx":
      return (await import("@codemirror/lang-javascript")).javascript({ jsx: true });
    case "ts":
    case "mts":
    case "cts":
    case "tsx":
      return (await import("@codemirror/lang-javascript")).javascript({ jsx: true, typescript: true });
    case "json":
    case "jsonc":
      return (await import("@codemirror/lang-json")).json();
    case "md":
    case "markdown":
      return (await import("@codemirror/lang-markdown")).markdown();
    case "py":
      return (await import("@codemirror/lang-python")).python();
    case "go":
      return (await import("@codemirror/lang-go")).go();
    case "html":
    case "htm":
      return (await import("@codemirror/lang-html")).html();
    case "css":
      return (await import("@codemirror/lang-css")).css();
    case "yaml":
    case "yml":
      return (await import("@codemirror/lang-yaml")).yaml();
    default:
      return null;
  }
}

/** The editor reads the daisyUI theme colors, so it matches the app palette. */
const mix = (color: string, percent: number) => `color-mix(in oklab, var(--color-${color}) ${percent}%, transparent)`;

const theme = EditorView.theme(
  {
    "&": { height: "100%", backgroundColor: "var(--color-base-300)", color: "var(--color-base-content)", fontSize: "13px" },
    ".cm-scroller": { fontFamily: "var(--font-mono)", lineHeight: "1.55" },
    ".cm-content": { caretColor: "var(--color-base-content)" },
    ".cm-cursor, .cm-dropCursor": { borderLeftColor: "var(--color-base-content)" },
    "&.cm-focused": { outline: "none" },
    "&.cm-focused .cm-selectionBackground, .cm-selectionBackground, ::selection": {
      backgroundColor: mix("primary", 45),
    },
    ".cm-gutters": {
      backgroundColor: "var(--color-base-300)",
      color: mix("base-content", 70),
      borderRight: `1px solid ${mix("base-content", 10)}`,
    },
    ".cm-activeLine": { backgroundColor: "var(--color-base-200)" },
    ".cm-activeLineGutter": { backgroundColor: "var(--color-base-200)", color: "var(--color-base-content)" },
    ".cm-matchingBracket": { backgroundColor: mix("base-content", 15), outline: `1px solid ${mix("base-content", 20)}` },
    ".cm-searchMatch": { backgroundColor: mix("warning", 25) },
    ".cm-panels": { backgroundColor: "var(--color-base-200)", color: "var(--color-base-content)" },
  },
  { dark: true },
);

const highlight = HighlightStyle.define([
  { tag: [tags.keyword, tags.operatorKeyword, tags.modifier], color: "#d6a4ff" },
  { tag: [tags.string, tags.special(tags.string), tags.regexp], color: "#93e8b3" },
  { tag: [tags.number, tags.bool, tags.null, tags.atom], color: "#f7c15f" },
  { tag: [tags.comment, tags.lineComment, tags.blockComment], color: "#8b9099", fontStyle: "italic" },
  { tag: [tags.function(tags.variableName), tags.function(tags.propertyName)], color: "#8ebcff" },
  { tag: [tags.typeName, tags.className, tags.namespace], color: "#6fd6dc" },
  { tag: [tags.propertyName, tags.attributeName], color: "#b5d3ff" },
  { tag: [tags.heading], color: "#eceff2", fontWeight: "600" },
  { tag: [tags.link, tags.url], color: "#8ebcff", textDecoration: "underline" },
  { tag: tags.invalid, color: "#fc918e" },
]);

interface CodeEditorProps {
  path: string;
  /** Initial document; a new value (after reload) replaces the content. */
  value: string;
  onChange: (value: string) => void;
  onSave: () => void;
  label: string;
}

export default function CodeEditor({ path, value, onChange, onSave, label }: CodeEditorProps) {
  const hostRef = useRef<HTMLDivElement>(null);
  const viewRef = useRef<EditorView | null>(null);
  const language = useRef(new Compartment());
  const handlers = useRef({ onChange, onSave });
  useEffect(() => {
    handlers.current = { onChange, onSave };
  });

  useEffect(() => {
    const host = hostRef.current;
    if (!host) {
      return;
    }
    const view = new EditorView({
      parent: host,
      state: EditorState.create({
        doc: "",
        extensions: [
          lineNumbers(),
          highlightActiveLineGutter(),
          foldGutter(),
          history(),
          drawSelection(),
          indentOnInput(),
          bracketMatching(),
          closeBrackets(),
          highlightActiveLine(),
          highlightSelectionMatches(),
          syntaxHighlighting(highlight),
          language.current.of([]),
          keymap.of([
            {
              key: "Mod-s",
              preventDefault: true,
              run: () => {
                handlers.current.onSave();
                return true;
              },
            },
            ...closeBracketsKeymap,
            ...defaultKeymap,
            ...searchKeymap,
            ...historyKeymap,
            indentWithTab,
          ]),
          theme,
          EditorView.contentAttributes.of({ "aria-label": label }),
          EditorView.updateListener.of((update) => {
            if (update.docChanged) {
              handlers.current.onChange(update.state.doc.toString());
            }
          }),
        ],
      }),
    });
    viewRef.current = view;
    return () => {
      view.destroy();
      viewRef.current = null;
    };
  }, [label]);

  useEffect(() => {
    const view = viewRef.current;
    if (!view || view.state.doc.toString() === value) {
      return;
    }
    view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: value } });
  }, [value]);

  useEffect(() => {
    let cancelled = false;
    void languageFor(path).then((support) => {
      const view = viewRef.current;
      if (!cancelled && view) {
        view.dispatch({ effects: language.current.reconfigure(support ?? []) });
      }
    });
    return () => {
      cancelled = true;
    };
  }, [path]);

  return <div ref={hostRef} className="h-full min-h-0 overflow-hidden" />;
}
