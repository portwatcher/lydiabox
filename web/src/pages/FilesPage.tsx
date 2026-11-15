import { ChevronRight, FolderPlus, FolderUp, HardDriveDownload, Upload } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import { useSearchParams } from "react-router";
import { files } from "../api/files";
import type { Entry, RootName } from "../api/types";
import { ConfirmDialog } from "../components/Dialog";
import { ErrorView, LoadingView } from "../components/StateView";
import { useToast } from "../components/Toast";
import { Button, cx } from "../components/ui";
import { useResource } from "../hooks/useResource";
import { useSystem } from "../hooks/useSystem";
import { baseName, crumbs, joinPath, parentPath, validName } from "../lib/paths";
import { collectDrop, collectInput } from "./files/collectFiles";
import { FileTable } from "./files/FileTable";
import { FileTree } from "./files/FileTree";
import { FileView } from "./files/FileView";
import { NameDialog } from "./files/NameDialog";
import { UploadBar, useUploader } from "./files/UploadBar";
import { WebDavDialog } from "./files/WebDavDialog";

const rootLabels: Record<RootName, string> = { workspace: "/workspace", home: "/home/agent" };

function nameError(value: string): string | null {
  return validName(value) ? null : "Enter a name without slashes. It cannot be . or ..";
}

function RootSwitch({ root, onChange, className }: { root: RootName; onChange: (r: RootName) => void; className?: string }) {
  return (
    <div role="group" aria-label="Root" className={cx("inline-flex rounded-md border border-border bg-bg p-0.5", className)}>
      {(["workspace", "home"] as const).map((r) => (
        <button
          key={r}
          type="button"
          aria-pressed={root === r}
          onClick={() => onChange(r)}
          className={cx(
            "h-7 flex-1 rounded px-2.5 font-mono text-xs transition-colors duration-150",
            root === r ? "bg-surface-active text-fg" : "text-fg-muted hover:text-fg",
          )}
        >
          {rootLabels[r]}
        </button>
      ))}
    </div>
  );
}

export default function FilesPage() {
  const toast = useToast();
  const [params, setParams] = useSearchParams();
  const root: RootName = params.get("root") === "home" ? "home" : "workspace";
  const dir = params.get("dir") ?? "";
  const file = params.get("file");
  const [version, setVersion] = useState(0);
  const [dirty, setDirty] = useState(false);
  const [guarded, setGuarded] = useState<(() => void) | null>(null);
  const [mkdirOpen, setMkdirOpen] = useState(false);
  const [renaming, setRenaming] = useState<Entry | null>(null);
  const [deleting, setDeleting] = useState<Entry | null>(null);
  const [davOpen, setDavOpen] = useState(false);
  // WebDAV and the /s3 gateway need the host volume of the juicefs storage mode.
  const mountable = useSystem().status?.storage.mode !== "provider";
  const fileInput = useRef<HTMLInputElement>(null);
  const folderInput = useRef<HTMLInputElement>(null);

  const loader = useCallback((signal: AbortSignal) => files.list(root, dir, signal), [root, dir]);
  const listing = useResource(loader, { enabled: file === null });
  const reloadListing = listing.reload;

  // After a change: reload the listing and every open folder in the tree.
  const bump = useCallback(() => {
    setVersion((v) => v + 1);
    reloadListing();
  }, [reloadListing]);
  const uploader = useUploader(bump);

  useEffect(() => {
    folderInput.current?.setAttribute("webkitdirectory", "");
  }, []);

  const navigate = useCallback(
    (next: { root?: RootName; dir?: string; file?: string | null }) => {
      const go = () => {
        const p = new URLSearchParams();
        const nextRoot = next.root ?? root;
        if (nextRoot !== "workspace") {
          p.set("root", nextRoot);
        }
        const nextDir = next.dir ?? (next.root && next.root !== root ? "" : dir);
        if (nextDir) {
          p.set("dir", nextDir);
        }
        if (next.file) {
          p.set("file", next.file);
        }
        setParams(p);
      };
      if (dirty && file !== null && next.file !== file) {
        setGuarded(() => go);
        return;
      }
      go();
    },
    [root, dir, file, dirty, setParams],
  );

  const openEntry = (entry: Entry) => {
    if (entry.type === "dir") {
      navigate({ dir: entry.path, file: null });
    } else {
      navigate({ dir, file: entry.path });
    }
  };

  const startUpload = (items: ReturnType<typeof collectInput>) => {
    uploader.start(root, dir, items);
  };

  const trail = crumbs(file ?? dir);

  let content;
  if (file !== null) {
    content = <FileView key={`${root}:${file}`} root={root} path={file} onDirtyChange={setDirty} onSaved={bump} />;
  } else if (listing.loading && !listing.data) {
    content = <LoadingView label="Loading files…" />;
  } else if (listing.error && !listing.data) {
    content = <ErrorView error={listing.error} subsystem="Files" failed="Folder not loaded" onRetry={listing.reload} />;
  } else {
    content = (
      <FileTable
        root={root}
        entries={listing.data?.entries ?? []}
        onOpen={openEntry}
        onRename={setRenaming}
        onDelete={setDeleting}
        onDrop={(data) => void collectDrop(data).then(startUpload)}
        onNewFolder={() => setMkdirOpen(true)}
        onUpload={() => fileInput.current?.click()}
        dropEnabled={uploader.state.kind !== "running"}
      />
    );
  }

  return (
    <div className="flex h-full min-h-0">
      <aside className="hidden w-64 shrink-0 flex-col border-r border-border-subtle bg-bg-subtle md:flex">
        <div className="border-b border-border-subtle p-3">
          <RootSwitch root={root} onChange={(r) => navigate({ root: r, file: null })} className="w-full" />
        </div>
        <div className="min-h-0 flex-1 overflow-y-auto">
          <FileTree root={root} rootLabel={rootLabels[root]} current={file ? parentPath(file) : dir} onOpen={(d) => navigate({ dir: d, file: null })} version={version} />
        </div>
      </aside>
      <section className="flex min-w-0 flex-1 flex-col">
        <div className="flex min-h-12 flex-wrap items-center gap-2 border-b border-border-subtle px-3 py-2">
          <RootSwitch root={root} onChange={(r) => navigate({ root: r, file: null })} className="md:hidden" />
          <nav aria-label="Breadcrumb" className="flex min-w-0 flex-[1_1_16rem] flex-wrap items-center gap-1 text-sm">
            <button
              type="button"
              onClick={() => navigate({ dir: "", file: null })}
              className="shrink-0 rounded px-1.5 py-1 font-mono text-[13px] text-fg-muted hover:bg-surface-hover hover:text-fg"
            >
              {rootLabels[root]}
            </button>
            {trail.map((c, i) => {
              const last = i === trail.length - 1;
              return (
                <span key={c.path} className="flex min-w-0 items-center gap-1">
                  <ChevronRight aria-hidden className="size-3.5 text-fg-muted" />
                  {last ? (
                    <span aria-current="page" className="px-1.5 py-1 font-medium break-all text-fg">
                      {c.name}
                    </span>
                  ) : (
                    <button
                      type="button"
                      onClick={() => navigate({ dir: c.path, file: null })}
                      className="rounded px-1.5 py-1 break-all text-fg-muted hover:bg-surface-hover hover:text-fg"
                    >
                      {c.name}
                    </button>
                  )}
                </span>
              );
            })}
          </nav>
          {file === null ? (
            <div className="flex flex-wrap items-center gap-1">
              <Button size="sm" variant="ghost" icon={FolderPlus} onClick={() => setMkdirOpen(true)}>
                New folder
              </Button>
              <Button size="sm" variant="ghost" icon={Upload} disabled={uploader.state.kind === "running"} onClick={() => fileInput.current?.click()}>
                Upload files
              </Button>
              <Button size="sm" variant="ghost" icon={FolderUp} disabled={uploader.state.kind === "running"} onClick={() => folderInput.current?.click()}>
                Upload folder
              </Button>
            </div>
          ) : null}
          {mountable ? (
            <Button size="sm" icon={HardDriveDownload} onClick={() => setDavOpen(true)}>
              Mount locally
            </Button>
          ) : null}
        </div>
        <UploadBar uploader={uploader} />
        <div className="min-h-0 flex-1">{content}</div>
      </section>
      <input
        ref={fileInput}
        type="file"
        multiple
        hidden
        onChange={(e) => {
          startUpload(collectInput(e.target.files));
          e.target.value = "";
        }}
      />
      <input
        ref={folderInput}
        type="file"
        multiple
        hidden
        onChange={(e) => {
          startUpload(collectInput(e.target.files));
          e.target.value = "";
        }}
      />
      <NameDialog
        open={mkdirOpen}
        title="New folder"
        label="Folder name"
        submitLabel="Create folder"
        initial=""
        failed="Folder not created"
        validate={nameError}
        onClose={() => setMkdirOpen(false)}
        onSubmit={async (name) => {
          await files.mkdir(root, joinPath(dir, name));
          toast.success(`Folder ${name} created`);
          bump();
        }}
      />
      <NameDialog
        open={renaming !== null}
        title={renaming ? `Rename ${renaming.name}` : "Rename"}
        label="New name"
        submitLabel="Rename"
        initial={renaming?.name ?? ""}
        failed="Not renamed"
        validate={nameError}
        onClose={() => setRenaming(null)}
        onSubmit={async (name) => {
          if (!renaming || name === renaming.name) {
            return;
          }
          await files.rename(root, renaming.path, joinPath(parentPath(renaming.path), name));
          toast.success(`Renamed to ${name}`);
          bump();
        }}
      />
      <ConfirmDialog
        open={deleting !== null}
        onClose={() => setDeleting(null)}
        title={deleting ? `Delete ${deleting.name}?` : "Delete?"}
        confirmLabel={deleting?.type === "dir" ? "Delete folder" : "Delete file"}
        failed="Not deleted"
        body={
          deleting?.type === "dir"
            ? "The folder and everything in it are deleted. This cannot be undone."
            : "The file is deleted. This cannot be undone."
        }
        onConfirm={async () => {
          if (!deleting) {
            return;
          }
          await files.remove(root, deleting.path, deleting.type === "dir");
          toast.success(`${deleting.name} deleted`);
          bump();
        }}
      />
      <ConfirmDialog
        open={guarded !== null}
        onClose={() => setGuarded(null)}
        title={file ? `Discard changes to ${baseName(file)}?` : "Discard changes?"}
        confirmLabel="Discard changes"
        failed="Not discarded"
        body="Your unsaved edits are lost."
        onConfirm={() => {
          const go = guarded;
          setDirty(false);
          go?.();
        }}
      />
      {mountable ? <WebDavDialog open={davOpen} onClose={() => setDavOpen(false)} /> : null}
    </div>
  );
}
