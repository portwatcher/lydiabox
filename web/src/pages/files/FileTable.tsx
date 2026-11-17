import { Download, File, FileSymlink, Folder, FolderPlus, Pencil, Trash2, Upload } from "lucide-react";
import { useState } from "react";
import type { DragEvent } from "react";
import { files } from "../../api/files";
import type { Entry, RootName } from "../../api/types";
import { StateView } from "../../components/StateView";
import { Button, IconButton, cx } from "../../components/ui";
import { useNow } from "../../hooks/useResource";
import { formatBytes, formatDateTime, timeAgo } from "../../lib/format";

interface FileTableProps {
  root: RootName;
  entries: Entry[];
  onOpen: (entry: Entry) => void;
  onRename: (entry: Entry) => void;
  onDelete: (entry: Entry) => void;
  onDrop: (data: DataTransfer) => void;
  onNewFolder: () => void;
  onUpload: () => void;
  dropEnabled: boolean;
  /** Why no file can change now (the VM that holds the files is stopped), or null. */
  blocked?: string | null;
}

function EntryIcon({ entry }: { entry: Entry }) {
  const Icon = entry.type === "dir" ? Folder : entry.type === "symlink" ? FileSymlink : File;
  return <Icon aria-hidden className={cx("size-4 shrink-0", entry.type === "dir" ? "text-accent-fg" : "text-fg-muted")} />;
}

/** Directory listing and drop target for files and folders. */
export function FileTable({ root, entries, onOpen, onRename, onDelete, onDrop, onNewFolder, onUpload, dropEnabled, blocked = null }: FileTableProps) {
  const now = useNow(30_000);
  const [dragging, setDragging] = useState(0);
  const off = blocked !== null;

  const dragProps = dropEnabled && !off
    ? {
        onDragEnter: (e: DragEvent) => {
          if (e.dataTransfer.types.includes("Files")) {
            e.preventDefault();
            setDragging((d) => d + 1);
          }
        },
        onDragOver: (e: DragEvent) => {
          if (e.dataTransfer.types.includes("Files")) {
            e.preventDefault();
            e.dataTransfer.dropEffect = "copy";
          }
        },
        onDragLeave: () => setDragging((d) => Math.max(0, d - 1)),
        onDrop: (e: DragEvent) => {
          e.preventDefault();
          setDragging(0);
          onDrop(e.dataTransfer);
        },
      }
    : {};

  return (
    <div {...dragProps} className="relative h-full min-h-0 overflow-auto">
      {entries.length === 0 ? (
        <StateView
          icon={Folder}
          title="This folder is empty"
          action={
            <>
              <Button icon={Upload} variant="primary" onClick={onUpload} disabled={off} title={blocked ?? undefined}>
                Upload files
              </Button>
              <Button icon={FolderPlus} onClick={onNewFolder} disabled={off} title={blocked ?? undefined}>
                New folder
              </Button>
            </>
          }
        >
          Drop files or folders here to upload them.
        </StateView>
      ) : (
        <table className="w-full table-fixed border-collapse text-sm">
          <thead className="sticky top-0 z-[1] bg-bg">
            <tr className="border-b border-border-subtle text-left text-xs text-fg-muted">
              <th scope="col" className="px-3 py-2 font-medium">
                Name
              </th>
              <th scope="col" className="hidden w-28 px-3 py-2 text-right font-medium sm:table-cell">
                Size
              </th>
              <th scope="col" className="hidden w-36 px-3 py-2 font-medium md:table-cell">
                Modified
              </th>
              <th scope="col" className="w-28 px-3 py-2">
                <span className="sr-only">Actions</span>
              </th>
            </tr>
          </thead>
          <tbody>
            {entries.map((entry) => (
              <tr key={entry.path} className="group border-b border-border-subtle hover:bg-surface-hover">
                <td className="px-3 py-1">
                  <button
                    type="button"
                    onClick={() => onOpen(entry)}
                    className="flex h-8 w-full min-w-0 items-center gap-2 text-left text-fg"
                    title={entry.name}
                  >
                    <EntryIcon entry={entry} />
                    <span className="truncate">{entry.name}</span>
                  </button>
                </td>
                <td className="hidden px-3 py-1 text-right text-fg-muted tabular-nums sm:table-cell">
                  {entry.type === "dir" ? "" : formatBytes(entry.size)}
                </td>
                <td className="hidden px-3 py-1 text-fg-muted tabular-nums md:table-cell" title={formatDateTime(entry.modTime)}>
                  {timeAgo(entry.modTime, now) ?? ""}
                </td>
                <td className="px-2 py-1">
                  <div className="flex justify-end gap-0.5 opacity-100 md:opacity-0 md:group-focus-within:opacity-100 md:group-hover:opacity-100">
                    {entry.type !== "dir" && !off ? (
                      <a
                        href={files.downloadUrl(root, entry.path)}
                        download={entry.name}
                        aria-label={`Download ${entry.name}`}
                        title={`Download ${entry.name}`}
                        className="inline-flex size-8 items-center justify-center rounded-md text-fg-muted hover:bg-surface-active hover:text-fg"
                      >
                        <Download aria-hidden className="size-4" />
                      </a>
                    ) : null}
                    <IconButton icon={Pencil} label={`Rename ${entry.name}`} onClick={() => onRename(entry)} disabled={off} />
                    <IconButton icon={Trash2} tone="danger" label={`Delete ${entry.name}`} onClick={() => onDelete(entry)} disabled={off} />
                  </div>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {dragging > 0 ? (
        <div className="pointer-events-none absolute inset-2 flex items-center justify-center rounded-lg border-2 border-dashed border-accent-solid bg-bg/90">
          <span className="flex items-center gap-2 text-sm font-medium text-fg">
            <Upload aria-hidden className="size-4" />
            Drop to upload here
          </span>
        </div>
      ) : null}
    </div>
  );
}
