import { availabilityOf } from "../../api/errors";

/** The load state of one folder in the folder tree. */
export interface NodeState {
  children: string[] | null;
  loading: boolean;
  /**
   * Why the last load failed. "unavailable" is an expected state (the VM is
   * stopped or hibernating): the main area explains it, so the tree shows no
   * error for it.
   */
  failure: "error" | "unavailable" | null;
}

export function loadingNode(prev: NodeState | undefined): NodeState {
  return { children: prev?.children ?? null, loading: true, failure: null };
}

export function loadedNode(children: string[]): NodeState {
  return { children, loading: false, failure: null };
}

export function failedNode(prev: NodeState | undefined, err: unknown): NodeState {
  return {
    children: prev?.children ?? [],
    loading: false,
    failure: availabilityOf(err) === "unavailable" ? "unavailable" : "error",
  };
}

/** Whether the tree shows the "Folder not loaded" error under the folder. */
export function showsLoadError(node: NodeState | undefined): boolean {
  return node?.failure === "error";
}
