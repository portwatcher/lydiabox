import { useCallback, useEffect, useRef, useState } from "react";
import { useServerDown } from "./useSystem";

export interface Resource<T> {
  data: T | undefined;
  /** The last error; cleared by the next successful load. */
  error: unknown;
  /** True until the first load settles. */
  loading: boolean;
  reload: () => void;
  /** Replaces the cached value after a local change, without a request. */
  mutate: (update: T | ((current: T | undefined) => T)) => void;
}

export interface ResourceOptions {
  /** Reloads on this interval while the tab is visible. */
  pollMs?: number;
  /** When false, nothing is requested. */
  enabled?: boolean;
}

/**
 * Loads `loader` and keeps the result. `loader` must be memoized
 * (useCallback); a new loader identity starts a fresh load.
 */
export function useResource<T>(loader: (signal: AbortSignal) => Promise<T>, options: ResourceOptions = {}): Resource<T> {
  const { pollMs, enabled = true } = options;
  const [data, setData] = useState<T | undefined>(undefined);
  const [error, setError] = useState<unknown>(null);
  const [loading, setLoading] = useState(enabled);
  const [generation, setGeneration] = useState(0);
  const loaderRef = useRef(loader);

  // While the server cannot answer, polls wait; when it answers again, the
  // data loads once at once.
  const down = useServerDown();
  const downRef = useRef(down);
  useEffect(() => {
    const was = downRef.current;
    downRef.current = down;
    if (was && !down) {
      setGeneration((g) => g + 1);
    }
  }, [down]);

  useEffect(() => {
    if (loaderRef.current !== loader) {
      loaderRef.current = loader;
      setData(undefined);
      setLoading(true);
    }
  }, [loader]);

  useEffect(() => {
    if (!enabled) {
      setLoading(false);
      return;
    }
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout> | null = null;
    let disposed = false;
    setLoading(true);

    const run = async () => {
      if (downRef.current) {
        // The server cannot answer: no request now; the end of the
        // outage loads again (see above).
        return;
      }
      try {
        const value = await loader(controller.signal);
        if (!disposed) {
          setData(value);
          setError(null);
        }
      } catch (err) {
        if (disposed || (err instanceof DOMException && err.name === "AbortError")) {
          return;
        }
        setError(err);
      } finally {
        if (!disposed) {
          setLoading(false);
          schedule();
        }
      }
    };

    const schedule = () => {
      if (!pollMs) {
        return;
      }
      timer = setTimeout(() => {
        if ((typeof document !== "undefined" && document.hidden) || downRef.current) {
          schedule();
          return;
        }
        void run();
      }, pollMs);
    };

    void run();
    return () => {
      disposed = true;
      controller.abort();
      if (timer) {
        clearTimeout(timer);
      }
    };
  }, [loader, pollMs, enabled, generation]);

  const reload = useCallback(() => setGeneration((g) => g + 1), []);
  const mutate = useCallback((update: T | ((current: T | undefined) => T)) => {
    setData((current) => (typeof update === "function" ? (update as (c: T | undefined) => T)(current) : update));
  }, []);

  return { data, error, loading, reload, mutate };
}

/** Re-renders every `intervalMs` and returns the current time. */
export function useNow(intervalMs = 1000): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const id = setInterval(() => setNow(Date.now()), intervalMs);
    return () => clearInterval(id);
  }, [intervalMs]);
  return now;
}
