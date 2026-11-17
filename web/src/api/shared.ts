interface Flight<T> {
  promise: Promise<T>;
  controller: AbortController;
  callers: number;
}

function abortError(): DOMException {
  return new DOMException("The request was aborted.", "AbortError");
}

/**
 * Shares one request between callers that ask for the same key in the same
 * task (the Files listing and the folder tree both list the open folder in
 * the effects of one render, for example at a resume). A later caller gets
 * a new request, so a change made in between is never hidden. Each caller
 * keeps its own abort: an aborted caller rejects with an AbortError, and
 * the request is aborted only when every caller has aborted.
 */
export function sharedRequests<T>() {
  const flights = new Map<string, Flight<T>>();
  return (key: string, request: (signal: AbortSignal) => Promise<T>, signal?: AbortSignal): Promise<T> => {
    if (signal?.aborted) {
      return Promise.reject(abortError());
    }
    let flight = flights.get(key);
    if (!flight) {
      const controller = new AbortController();
      const created: Flight<T> = { promise: request(controller.signal), controller, callers: 0 };
      flight = created;
      flights.set(key, created);
      setTimeout(() => {
        if (flights.get(key) === created) {
          flights.delete(key);
        }
      }, 0);
    }
    const current = flight;
    current.callers++;
    return new Promise<T>((resolve, reject) => {
      let settled = false;
      const leave = () => {
        signal?.removeEventListener("abort", onAbort);
      };
      const onAbort = () => {
        if (settled) {
          return;
        }
        settled = true;
        leave();
        current.callers--;
        if (current.callers === 0) {
          if (flights.get(key) === current) {
            flights.delete(key);
          }
          current.controller.abort();
        }
        reject(abortError());
      };
      signal?.addEventListener("abort", onAbort);
      current.promise.then(
        (value) => {
          if (!settled) {
            settled = true;
            leave();
            resolve(value);
          }
        },
        (err: unknown) => {
          if (!settled) {
            settled = true;
            leave();
            reject(err);
          }
        },
      );
    });
  };
}
