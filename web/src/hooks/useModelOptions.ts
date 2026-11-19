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

/** While the list is empty or the gateway is down, pickers load it again at this interval. */
export const EMPTY_POLL_MS = 10_000;

type Listener = () => void;
const listeners = new Set<Listener>();

/** Tells mounted model pickers that the catalog changed (sign-in, removal, new key). */
export function announceModelsChanged(): void {
  for (const l of [...listeners]) {
    l();
  }
}

/**
 * Model IDs for pickers. Falls back to an empty list when the models
 * subsystem is not available, so pickers accept free text instead.
 */
export function useModelOptions(enabled = true): { models: Model[]; state: ModelListState } {
  const [list, setList] = useState<Model[]>([]);
  const [state, setState] = useState<ModelListState>("loading");
  const [generation, setGeneration] = useState(0);

  useEffect(() => {
    const listener = () => setGeneration((g) => g + 1);
    listeners.add(listener);
    return () => {
      listeners.delete(listener);
    };
  }, []);

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
  }, [enabled, generation]);

  // A subscription that was just signed in lists its models some seconds
  // later; an empty list therefore loads again until models appear.
  const waiting = enabled && (state === "unavailable" || (state === "loaded" && list.length === 0));
  useEffect(() => {
    if (!waiting) {
      return;
    }
    const id = setInterval(() => {
      if (typeof document === "undefined" || !document.hidden) {
        setGeneration((g) => g + 1);
      }
    }, EMPTY_POLL_MS);
    return () => clearInterval(id);
  }, [waiting]);

  return { models: list, state };
}
