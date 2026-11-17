import { Link } from "react-router";
import { Banner } from "../../components/ui";

/**
 * Shown for agent sessions while no model is set up: the harness starts,
 * but the gateway refuses its model and every prompt fails.
 */
export function NoModelNotice({ onNavigate }: { onNavigate?: () => void }) {
  return (
    <Banner
      tone="warning"
      action={
        <Link to="/models" onClick={onNavigate} className="font-medium underline underline-offset-2">
          Set up a model
        </Link>
      }
    >
      No model is set up. An agent cannot answer until you add one.
    </Banner>
  );
}
