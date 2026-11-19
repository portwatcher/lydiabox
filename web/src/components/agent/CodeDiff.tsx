import { cx } from "../ui";
import { diffCounts } from "./toolKinds";

const MAX_LINES = 400;

/** A unified diff body: added lines on a green tint, removed lines on a red tint, each with its sign. */
export function CodeDiff({ diff, path }: { diff: string; path?: string }) {
  const lines = diff.split("\n");
  const { added, removed } = diffCounts(diff);
  const shown = lines.slice(0, MAX_LINES);
  return (
    <figure className="overflow-hidden rounded-box border border-base-content/10 bg-base-300">
      <figcaption className="flex items-center gap-2 border-b border-base-content/10 px-3 py-1.5 font-mono text-xs">
        <span className="min-w-0 flex-1 truncate" title={path}>
          {path || "Changes"}
        </span>
        <span className="text-success">+{added}</span>
        <span className="text-error">−{removed}</span>
      </figcaption>
      <pre className="max-h-96 overflow-auto py-1 font-mono text-xs leading-5">
        {shown.map((line, i) => {
          const sign = line[0];
          const tone =
            sign === "+" && !line.startsWith("+++")
              ? "bg-success/10 text-success"
              : sign === "-" && !line.startsWith("---")
                ? "bg-error/10 text-error"
                : line.startsWith("@@") || line.startsWith("***")
                  ? "text-info"
                  : "text-base-content/70";
          return (
            <div key={i} className={cx("px-3 whitespace-pre", tone)}>
              {line || " "}
            </div>
          );
        })}
        {lines.length > MAX_LINES ? <div className="px-3 text-base-content/70">{lines.length - MAX_LINES} more lines</div> : null}
      </pre>
    </figure>
  );
}
