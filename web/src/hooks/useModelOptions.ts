import { useEffect, useState } from "react";
import { availabilityOf } from "../api/errors";
import { models } from "../api/domains";
import type { Model } from "../api/types";

/**
 * - `loading`: the list is not loaded yet.
 * - `loaded`: the list loaded (it can be empty: no model is set up).
 * - `unavailable`: the model gateway does not run (502-504), so the list is
 *   not known.
 * - `absent`: the models subsystem is not in this build, or another error.
 */
export type ModelListState = "loading" | "loaded" | "unavailable" | "absent";

/**
 * Model IDs for pickers. Falls back to an empty list when the models
 * subsystem is not available, so pickers accept free text instead.
 */
export function useModelOptions(enabled = true): { models: Model[]; state: ModelListState } {
  const [list, setList] = useState<Model[]>([]);
  const [state, setState] = useState<ModelListState>("loading");

  useEffect(() => {
    if (!enabled) {
      return;
    }
    const controller = new AbortController();
    models
      .list(controller.signal)
      .then((res) => {
        setList(res.models);
        setState("loaded");
      })
      .catch((err: unknown) => {
        if (err instanceof DOMException && err.name === "AbortError") {
          return;
        }
        setList([]);
        setState(availabilityOf(err) === "unavailable" ? "unavailable" : "absent");
      });
    return () => controller.abort();
  }, [enabled]);

  return { models: list, state };
}
