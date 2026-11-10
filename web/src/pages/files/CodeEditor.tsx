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

const theme = EditorView.theme(
  {
    "&": { height: "100%", backgroundColor: "var(--color-bg)", color: "var(--color-fg)", fontSize: "13px" },
    ".cm-scroller": { fontFamily: "var(--font-mono)", lineHeight: "1.55" },
    ".cm-content": { caretColor: "var(--color-fg)" },
    ".cm-cursor, .cm-dropCursor": { borderLeftColor: "var(--color-fg)" },
    "&.cm-focused": { outline: "none" },
    "&.cm-focused .cm-selectionBackground, .cm-selectionBackground, ::selection": {
      backgroundColor: "var(--color-accent-subtle)",
    },
    ".cm-gutters": {
      backgroundColor: "var(--color-bg)",
      color: "var(--color-fg-muted)",
      borderRight: "1px solid var(--color-border-subtle)",
    },
    ".cm-activeLine": { backgroundColor: "var(--color-bg-subtle)" },
    ".cm-activeLineGutter": { backgroundColor: "var(--color-bg-subtle)", color: "var(--color-fg)" },
    ".cm-matchingBracket": { backgroundColor: "var(--color-surface-active)", outline: "1px solid var(--color-border)" },
    ".cm-searchMatch": { backgroundColor: "var(--color-warning-subtle)" },
    ".cm-panels": { backgroundColor: "var(--color-bg-subtle)", color: "var(--color-fg)" },
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
