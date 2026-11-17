import { Link } from "react-router";
import { Banner } from "../../components/ui";
import type { ModelListState } from "../../hooks/useModelOptions";

/** Why an agent cannot answer: no model is set up, or the model gateway does not run. */
export type ModelProblem = "none" | "unavailable";

/** The model problem for a loaded (or failed) models list, or null. */
export function modelProblem(state: ModelListState, count: number): ModelProblem | null {
  if (state === "loaded" && count === 0) {
    return "none";
  }
  return state === "unavailable" ? "unavailable" : null;
}

const texts: Record<ModelProblem, { body: string; link: string }> = {
  none: { body: "No model is set up. An agent cannot answer until you add one.", link: "Set up a model" },
  unavailable: { body: "The models list is not available. An agent cannot answer until the model gateway runs.", link: "Open Models" },
};

/**
 * Shown for agent sessions while an agent cannot reach a model: the
 * harness starts, but every prompt fails.
 */
export function NoModelNotice({ problem, onNavigate }: { problem: ModelProblem; onNavigate?: () => void }) {
  const t = texts[problem];
  return (
    <Banner
      tone="warning"
      action={
        <Link to="/models" onClick={onNavigate} className="font-medium underline underline-offset-2">
          {t.link}
        </Link>
      }
    >
      {t.body}
    </Banner>
  );
}
