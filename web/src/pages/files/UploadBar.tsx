import { RotateCw, Upload, X } from "lucide-react";
import { useCallback, useRef, useState } from "react";
import { describeError, isConflict } from "../../api/errors";
import { upload } from "../../api/files";
import type { UploadHandle, UploadItem } from "../../api/files";
import type { RootName } from "../../api/types";
import { useToast } from "../../components/Toast";
import { Banner, Button } from "../../components/ui";
import { count, formatBytes } from "../../lib/format";

type UploadState =
  | { kind: "idle" }
  | { kind: "running"; files: number; loaded: number; total: number }
  | { kind: "conflict"; message: string }
  | { kind: "failed"; message: string };

interface Pending {
  root: RootName;
  dir: string;
  items: UploadItem[];
}

export interface Uploader {
  state: UploadState;
  start: (root: RootName, dir: string, items: UploadItem[], overwrite?: boolean) => void;
  replace: () => void;
  cancel: () => void;
  dismiss: () => void;
}

/** One upload at a time, with progress, cancel, and a replace-on-conflict retry. */
export function useUploader(onDone: () => void): Uploader {
  const toast = useToast();
  const [state, setState] = useState<UploadState>({ kind: "idle" });
  const handle = useRef<UploadHandle | null>(null);
  const pending = useRef<Pending | null>(null);

  const start = useCallback(
    (root: RootName, dir: string, items: UploadItem[], overwrite = false) => {
      if (items.length === 0 || handle.current) {
        return;
      }
      pending.current = { root, dir, items };
      const total = items.reduce((sum, i) => sum + i.file.size, 0);
      setState({ kind: "running", files: items.length, loaded: 0, total });
      const h = upload(root, dir, items, {
        overwrite,
        onProgress: (p) => setState({ kind: "running", files: items.length, loaded: p.loaded, total: p.total }),
      });
      handle.current = h;
      h.promise
        .then((res) => {
          setState({ kind: "idle" });
          pending.current = null;
          toast.success(count(res.files.length, { one: "# file uploaded", other: "# files uploaded" }));
        })
        .catch((err: unknown) => {
          if (err instanceof DOMException && err.name === "AbortError") {
            setState({ kind: "failed", message: "Upload canceled. Files stored before the cancel remain." });
            return;
          }
          if (isConflict(err)) {
            setState({ kind: "conflict", message: describeError(err, "Upload stopped") });
            return;
          }
          setState({ kind: "failed", message: describeError(err, "Upload stopped") });
        })
        .finally(() => {
          handle.current = null;
          onDone();
        });
    },
    [toast, onDone],
  );

  const replace = useCallback(() => {
    const p = pending.current;
    if (p) {
      start(p.root, p.dir, p.items, true);
    }
  }, [start]);

  const cancel = useCallback(() => handle.current?.abort(), []);
  const dismiss = useCallback(() => {
    pending.current = null;
    setState({ kind: "idle" });
  }, []);

  return { state, start, replace, cancel, dismiss };
}

export function UploadBar({ uploader }: { uploader: Uploader }) {
  const { state } = uploader;
  if (state.kind === "idle") {
    return null;
  }
  if (state.kind === "running") {
    const pct = state.total > 0 ? Math.round((state.loaded / state.total) * 100) : 0;
    return (
      <div className="flex flex-wrap items-center gap-3 border-b border-base-content/10 px-3 py-2 text-sm">
        <Upload aria-hidden className="size-4 text-base-content/70" />
        <span>
          Uploading {count(state.files, { one: "# file", other: "# files" })}…
        </span>
        <progress
          max={100}
          value={pct}
          aria-label="Upload progress"
          className="progress progress-primary h-1.5 w-auto min-w-24 flex-1"
        />
        <span className="text-xs text-base-content/70 tabular-nums">
          {formatBytes(state.loaded)} of {formatBytes(state.total)}
        </span>
        <Button size="sm" variant="ghost" icon={X} onClick={uploader.cancel}>
          Cancel
        </Button>
      </div>
    );
  }
  return (
    <div className="px-3 pt-2">
      <Banner
        tone={state.kind === "conflict" ? "warning" : "danger"}
        action={
          <div className="flex gap-2">
            {state.kind === "conflict" ? (
              <Button size="sm" icon={RotateCw} onClick={uploader.replace}>
                Replace existing files
              </Button>
            ) : null}
            <Button size="sm" variant="ghost" onClick={uploader.dismiss}>
              Dismiss
            </Button>
          </div>
        }
      >
        {state.message}
      </Banner>
    </div>
  );
}
