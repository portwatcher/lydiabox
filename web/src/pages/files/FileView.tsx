import { Download, ExternalLink, FileWarning, RotateCw, Save } from "lucide-react";
import { Suspense, lazy, useCallback, useEffect, useState } from "react";
import { describeError, isConflict } from "../../api/errors";
import { files } from "../../api/files";
import { ApiError } from "../../api/http";
import type { Entry, RootName } from "../../api/types";
import { ErrorView, LoadingView, StateView } from "../../components/StateView";
import { useToast } from "../../components/Toast";
import { Banner, Button, buttonClass } from "../../components/ui";
import { formatBytes, formatDateTime } from "../../lib/format";
import { baseName, isImage } from "../../lib/paths";

const CodeEditor = lazy(() => import("./CodeEditor"));

const TEXT_LIMIT = 8 * 1024 * 1024;

type Loaded =
  | { kind: "loading" }
  | { kind: "text"; content: string; entry: Entry }
  | { kind: "binary"; entry: Entry | null; reason: "binary" | "large" }
  | { kind: "error"; error: unknown };

interface FileViewProps {
  root: RootName;
  path: string;
  onDirtyChange: (dirty: boolean) => void;
  onSaved: () => void;
  /** Why the file cannot be saved now (the VM that holds the files is stopped), or null. */
  blocked?: string | null;
}

function BinaryView({ root, path, entry, reason }: { root: RootName; path: string; entry: Entry | null; reason: "binary" | "large" }) {
  const name = baseName(path);
  const inlineUrl = files.downloadUrl(root, path, true);
  return (
    <div className="flex h-full min-h-0 flex-col items-center justify-center gap-4 overflow-auto p-6">
      {isImage(path) ? (
        <img src={inlineUrl} alt={name} className="rounded-field max-h-[70%] max-w-full border border-base-content/10 object-contain" />
      ) : (
        <StateView icon={FileWarning} title={reason === "large" ? "This file is too large to edit here" : "This file is not text"}>
          {entry ? `${formatBytes(entry.size)} · modified ${formatDateTime(entry.modTime)}` : null}
        </StateView>
      )}
      <div className="flex flex-wrap justify-center gap-2">
        <a
          href={files.downloadUrl(root, path)}
          download={name}
          className={buttonClass("primary")}
        >
          <Download aria-hidden className="size-4" />
          Download {name}
        </a>
        <a
          href={inlineUrl}
          target="_blank"
          rel="noreferrer"
          className={buttonClass("secondary")}
        >
          <ExternalLink aria-hidden className="size-4" />
          Open in new tab
        </a>
      </div>
    </div>
  );
}

export function FileView({ root, path, onDirtyChange, onSaved, blocked = null }: FileViewProps) {
  const toast = useToast();
  const [loaded, setLoaded] = useState<Loaded>({ kind: "loading" });
  const [draft, setDraft] = useState("");
  const [saving, setSaving] = useState(false);
  const [conflict, setConflict] = useState(false);
  const [saveError, setSaveError] = useState<string | null>(null);
  const [generation, setGeneration] = useState(0);

  useEffect(() => {
    const controller = new AbortController();
    setLoaded({ kind: "loading" });
    setConflict(false);
    setSaveError(null);
    files
      .readText(root, path, controller.signal)
      .then((file) => {
        const { content, ...entry } = file;
        setLoaded({ kind: "text", content, entry });
        setDraft(content);
      })
      .catch(async (error: unknown) => {
        if (controller.signal.aborted) {
          return;
        }
        if (error instanceof ApiError && (error.status === 415 || error.status === 413 || error.status === 400)) {
          const entry = await files.stat(root, path).catch(() => null);
          if (entry && entry.type === "dir") {
            setLoaded({ kind: "error", error });
            return;
          }
          const large = error.status === 413 || (entry !== null && entry.size > TEXT_LIMIT);
          setLoaded({ kind: "binary", entry, reason: large ? "large" : "binary" });
          return;
        }
        setLoaded({ kind: "error", error });
      });
    return () => controller.abort();
  }, [root, path, generation]);

  const dirty = loaded.kind === "text" && draft !== loaded.content;
  useEffect(() => {
    onDirtyChange(dirty);
  }, [dirty, onDirtyChange]);

  useEffect(() => {
    if (!dirty) {
      return;
    }
    const warn = (event: BeforeUnloadEvent) => event.preventDefault();
    window.addEventListener("beforeunload", warn);
    return () => window.removeEventListener("beforeunload", warn);
  }, [dirty]);

  const save = useCallback(
    async (overwrite = false) => {
      if (loaded.kind !== "text" || saving) {
        return;
      }
      setSaving(true);
      setSaveError(null);
      const content = draft;
      try {
        const entry = await files.writeText(root, path, {
          content,
          baseModTime: overwrite ? undefined : loaded.entry.modTime,
        });
        setLoaded({ kind: "text", content, entry });
        setConflict(false);
        toast.success(`${baseName(path)} saved`);
        onSaved();
      } catch (err) {
        if (isConflict(err)) {
          setConflict(true);
        } else {
          setSaveError(describeError(err, `${baseName(path)} not saved`));
        }
      } finally {
        setSaving(false);
      }
    },
    [loaded, saving, draft, root, path, toast, onSaved],
  );

  if (loaded.kind === "loading") {
    return <LoadingView label={`Opening ${baseName(path)}…`} />;
  }
  if (loaded.kind === "error") {
    return <ErrorView error={loaded.error} subsystem="Files" failed={`${baseName(path)} not opened`} onRetry={() => setGeneration((g) => g + 1)} />;
  }
  if (loaded.kind === "binary") {
    return <BinaryView root={root} path={path} entry={loaded.entry} reason={loaded.reason} />;
  }

  return (
    <div className="flex h-full min-h-0 flex-col">
      <div className="flex min-h-11 flex-wrap items-center gap-2 border-b border-base-content/10 px-3 py-1.5">
        <span className="text-xs text-base-content/70 tabular-nums">
          {dirty ? "Unsaved changes" : `Saved · ${formatDateTime(loaded.entry.modTime)}`}
        </span>
        <div className="ml-auto flex items-center gap-2">
          <a
            href={files.downloadUrl(root, path)}
            download={baseName(path)}
            className={buttonClass("ghost", "sm")}
          >
            <Download aria-hidden className="size-4" />
            Download
          </a>
          <Button size="sm" variant="primary" icon={Save} busy={saving} disabled={!dirty || blocked !== null} onClick={() => void save()} title={blocked ?? "Save (Ctrl+S)"}>
            Save
          </Button>
        </div>
      </div>
      {conflict ? (
        <div className="px-3 pt-2">
          <Banner
            tone="warning"
            action={
              <div className="flex gap-2">
                <Button size="sm" icon={RotateCw} onClick={() => setGeneration((g) => g + 1)}>
                  Reload from disk
                </Button>
                <Button size="sm" variant="danger" onClick={() => void save(true)}>
                  Overwrite
                </Button>
              </div>
            }
          >
            {baseName(path)} changed on disk since you opened it. Reload to see the new version (your edits are lost), or overwrite it with your edits.
          </Banner>
        </div>
      ) : null}
      {saveError ? (
        <div className="px-3 pt-2">
          <Banner tone="danger">{saveError}</Banner>
        </div>
      ) : null}
      <div className="min-h-0 flex-1">
        <Suspense fallback={<LoadingView label="Loading editor…" />}>
          <CodeEditor
            path={path}
            value={loaded.content}
            onChange={setDraft}
            onSave={() => void save()}
            label={`Contents of ${path}`}
          />
        </Suspense>
      </div>
    </div>
  );
}
