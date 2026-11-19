import { useCallback, useEffect, useRef } from "react";
import type { Model, Subscription, SubscriptionKind } from "../../api/types";

/**
 * Magpie fills the catalog of a new subscription some seconds after the
 * sign-in. After a change, the page loads again at these delays (from the
 * change) until the catalog is complete or the last delay passes.
 */
export const SETTLE_DELAYS_MS = [1000, 2500, 4500, 7000, 10_000, 15_000, 22_000, 30_000];

/** True when the subscription of `kind` lists models and the catalog shows them. */
export function subscriptionListed(kind: SubscriptionKind, subscriptions: Subscription[] | undefined, models: Model[] | undefined): boolean {
  const sub = subscriptions?.find((s) => s.kind === kind);
  if (!sub || !sub.providerId || sub.models.length === 0) {
    return false;
  }
  return (models ?? []).some((m) => m.provider === sub.providerId);
}

/**
 * Returns `settle(done?)`: loads at once, then again at each settle delay
 * until `done()` is true. Without `done`, it loads at every delay. A new
 * call replaces the previous schedule.
 */
export function useSettle(reload: () => void): (done?: () => boolean) => void {
  const timers = useRef<ReturnType<typeof setTimeout>[]>([]);
  const clear = useCallback(() => {
    for (const t of timers.current) {
      clearTimeout(t);
    }
    timers.current = [];
  }, []);
  useEffect(() => clear, [clear]);

  return useCallback(
    (done?: () => boolean) => {
      clear();
      reload();
      timers.current = SETTLE_DELAYS_MS.map((ms) =>
        setTimeout(() => {
          if (done?.()) {
            clear();
            return;
          }
          reload();
        }, ms),
      );
    },
    [clear, reload],
  );
}
