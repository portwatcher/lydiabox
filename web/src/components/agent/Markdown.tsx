import type { ReactNode } from "react";

/**
 * A small Markdown renderer for agent replies: fenced code, headings,
 * lists, quotes, paragraphs, and inline code, bold, italic, and links. It
 * builds React elements (no HTML strings), and links keep only http, https,
 * and mailto targets.
 */
export function Markdown({ text }: { text: string }) {
  return <div className="flex min-w-0 flex-col gap-2 text-sm leading-6 break-words">{blocks(text)}</div>;
}

type Block =
  | { type: "code"; lang: string; body: string }
  | { type: "heading"; level: number; text: string }
  | { type: "list"; ordered: boolean; items: string[] }
  | { type: "quote"; text: string }
  | { type: "para"; text: string };

/** Splits Markdown into blocks. */
export function parseBlocks(text: string): Block[] {
  const out: Block[] = [];
  const lines = text.replace(/\r\n/g, "\n").split("\n");
  let i = 0;
  while (i < lines.length) {
    const line = lines[i];
    const fence = /^\s*(```|~~~)\s*([\w+-]*)/.exec(line);
    if (fence) {
      const body: string[] = [];
      i++;
      while (i < lines.length && !lines[i].trimStart().startsWith(fence[1])) {
        body.push(lines[i]);
        i++;
      }
      i++;
      out.push({ type: "code", lang: fence[2], body: body.join("\n") });
      continue;
    }
    const heading = /^(#{1,6})\s+(.*)$/.exec(line);
    if (heading) {
      out.push({ type: "heading", level: heading[1].length, text: heading[2] });
      i++;
      continue;
    }
    const item = /^\s*([-*+]|\d+[.)])\s+(.*)$/.exec(line);
    if (item) {
      const ordered = /\d/.test(item[1]);
      const items: string[] = [];
      while (i < lines.length) {
        const m = /^\s*([-*+]|\d+[.)])\s+(.*)$/.exec(lines[i]);
        if (m && /\d/.test(m[1]) === ordered) {
          items.push(m[2]);
        } else if (lines[i].trim() && /^\s{2,}/.test(lines[i]) && items.length) {
          items[items.length - 1] += " " + lines[i].trim();
        } else {
          break;
        }
        i++;
      }
      out.push({ type: "list", ordered, items });
      continue;
    }
    if (/^\s*>/.test(line)) {
      const quote: string[] = [];
      while (i < lines.length && /^\s*>/.test(lines[i])) {
        quote.push(lines[i].replace(/^\s*>\s?/, ""));
        i++;
      }
      out.push({ type: "quote", text: quote.join("\n") });
      continue;
    }
    if (!line.trim()) {
      i++;
      continue;
    }
    const para: string[] = [];
    while (i < lines.length && lines[i].trim() && !/^\s*(```|~~~|#{1,6}\s|>|[-*+]\s|\d+[.)]\s)/.test(lines[i])) {
      para.push(lines[i]);
      i++;
    }
    if (para.length === 0) {
      para.push(line);
      i++;
    }
    out.push({ type: "para", text: para.join("\n") });
  }
  return out;
}

function blocks(text: string): ReactNode[] {
  return parseBlocks(text).map((b, i) => {
    switch (b.type) {
      case "code":
        return (
          <pre key={i} className="overflow-x-auto rounded-box bg-base-300 px-3 py-2 font-mono text-xs leading-5">
            <code>{b.body}</code>
          </pre>
        );
      case "heading":
        return (
          <p key={i} role="heading" aria-level={Math.min(6, b.level + 2)} className="font-semibold">
            {inline(b.text)}
          </p>
        );
      case "list": {
        const items = b.items.map((it, j) => <li key={j}>{inline(it)}</li>);
        return b.ordered ? (
          <ol key={i} className="list-decimal pl-5">
            {items}
          </ol>
        ) : (
          <ul key={i} className="list-disc pl-5">
            {items}
          </ul>
        );
      }
      case "quote":
        return (
          <blockquote key={i} className="border-l-2 border-base-content/20 pl-3 text-base-content/70">
            {inline(b.text)}
          </blockquote>
        );
      default:
        return (
          <p key={i} className="whitespace-pre-wrap">
            {inline(b.text)}
          </p>
        );
    }
  });
}

const INLINE = /(`[^`\n]+`)|(\*\*[^*\n]+\*\*|__[^_\n]+__)|(\*[^*\s][^*\n]*\*|_[^_\s][^_\n]*_)|(\[[^\]\n]+\]\([^)\s]+\))/g;

/** Safe link targets: http, https, and mailto. */
export function safeHref(url: string): string | null {
  return /^(https?:\/\/|mailto:)/i.test(url) ? url : null;
}

export function inline(text: string): ReactNode[] {
  const out: ReactNode[] = [];
  let last = 0;
  let n = 0;
  for (const m of text.matchAll(INLINE)) {
    const at = m.index;
    if (at > last) {
      out.push(text.slice(last, at));
    }
    const s = m[0];
    if (m[1]) {
      out.push(
        <code key={n++} className="rounded bg-base-300 px-1 py-0.5 font-mono text-[0.85em]">
          {s.slice(1, -1)}
        </code>,
      );
    } else if (m[2]) {
      out.push(<strong key={n++}>{s.slice(2, -2)}</strong>);
    } else if (m[3]) {
      out.push(<em key={n++}>{s.slice(1, -1)}</em>);
    } else {
      const link = /^\[([^\]]+)\]\(([^)]+)\)$/.exec(s);
      const href = link ? safeHref(link[2]) : null;
      out.push(
        link && href ? (
          <a key={n++} href={href} target="_blank" rel="noreferrer noopener" className="link link-info">
            {link[1]}
          </a>
        ) : (
          s
        ),
      );
    }
    last = at + s.length;
  }
  if (last < text.length) {
    out.push(text.slice(last));
  }
  return out;
}
