import { useEffect, useState } from "react";
import { models } from "../api/domains";
import type { Model } from "../api/types";

/**
 * Model IDs for pickers. Falls back to an empty list when the models
 * subsystem is not available, so pickers accept free text instead.
 */
export function useModelOptions(enabled = true): { models: Model[]; available: boolean } {
  const [list, setList] = useState<Model[]>([]);
  const [available, setAvailable] = useState(false);

  useEffect(() => {
    if (!enabled) {
      return;
    }
    const controller = new AbortController();
    models
      .list(controller.signal)
      .then((res) => {
        setList(res.models);
        setAvailable(true);
      })
      .catch(() => {
        setList([]);
        setAvailable(false);
      });
    return () => controller.abort();
  }, [enabled]);

  return { models: list, available };
}
