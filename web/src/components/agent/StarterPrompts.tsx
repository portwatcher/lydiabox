import { Bug, FileSearch, GitPullRequestArrow, NotebookPen } from "lucide-react";
import type { LucideIcon } from "lucide-react";

export interface StarterPrompt {
  icon: LucideIcon;
  title: string;
  prompt: string;
}

export const starterPrompts: StarterPrompt[] = [
  { icon: FileSearch, title: "Explain this project", prompt: "Explain how this project is organized and how to build and test it." },
  { icon: Bug, title: "Fix a failing test", prompt: "Run the tests, find the first failure, and fix its cause." },
  { icon: GitPullRequestArrow, title: "Review my changes", prompt: "Review the uncommitted changes for bugs and suggest fixes." },
  { icon: NotebookPen, title: "Update the docs", prompt: "Find documentation that is out of date with the code and update it." },
];

/** The empty state of a session: tasks to start with. A pick fills the prompt field; it does not send. */
export function StarterPrompts({ onPick }: { onPick: (prompt: string) => void }) {
  return (
    <div className="mx-auto flex w-full max-w-2xl flex-col gap-4 px-4 py-10">
      <h2 className="text-lg font-semibold">What should the agent do?</h2>
      <ul className="grid grid-cols-1 gap-2 sm:grid-cols-2">
        {starterPrompts.map((p) => (
          <li key={p.title}>
            <button
              type="button"
              onClick={() => onPick(p.prompt)}
              className="flex min-h-12 h-full w-full items-start gap-3 rounded-box border border-base-content/10 bg-base-100 p-3 text-left hover:border-base-content/30 hover:bg-base-content/5 focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-base-content motion-safe:transition-colors"
            >
              <p.icon aria-hidden className="mt-0.5 size-4 shrink-0 text-info" />
              <span className="text-sm font-medium">{p.title}</span>
            </button>
          </li>
        ))}
      </ul>
    </div>
  );
}
