import { FilePen, FileText, Globe } from "lucide-react";
import type { TurnSource } from "./chatState";

const icons = { read: FileText, edit: FilePen, web: Globe };
const verbs = { read: "Read", edit: "Changed", web: "Fetched" };

/** "/workspace/app/src/main.go" → ["main.go", "/workspace/app/src"]; a URL → [host, path]. */
export function splitSource(label: string): [string, string] {
  if (/^https?:\/\//.test(label)) {
    try {
      const u = new URL(label);
      return [u.host, u.pathname === "/" ? "" : u.pathname];
    } catch {
      return [label, ""];
    }
  }
  const i = label.lastIndexOf("/");
  return i < 0 ? [label, ""] : [label.slice(i + 1), label.slice(0, i)];
}

/** The files and pages that a turn read or changed, as small cards. At most `max`; the rest are counted. */
export function SourceCards({ sources, max = 6 }: { sources: TurnSource[]; max?: number }) {
  if (sources.length === 0) {
    return null;
  }
  const shown = sources.slice(0, max);
  return (
    <ul aria-label="Files" className="flex flex-wrap gap-1.5">
      {shown.map((s) => {
        const Icon = icons[s.kind];
        const [name, where] = splitSource(s.label);
        return (
          <li key={s.key} title={`${verbs[s.kind]} ${s.label}`} className="flex max-w-64 min-w-0 items-center gap-1.5 rounded-box border border-base-content/10 bg-base-100 px-2 py-1 text-xs">
            <Icon aria-hidden className={s.kind === "edit" ? "size-3.5 shrink-0 text-warning" : "size-3.5 shrink-0 text-base-content/70"} />
            <span className="sr-only">{verbs[s.kind]} </span>
            <span className="truncate font-medium">{name}</span>
            {where ? <span className="min-w-0 truncate text-base-content/70">{where}</span> : null}
          </li>
        );
      })}
      {sources.length > max ? <li className="flex items-center px-1 text-xs text-base-content/70">{sources.length - max} more</li> : null}
    </ul>
  );
}
