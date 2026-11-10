import type { UploadItem } from "../../api/files";
import { uploadName } from "../../lib/paths";

/** Minimal views of the File and Directory Entries API, so tests can fake them. */
export interface FileEntryLike {
  isFile: true;
  isDirectory: false;
  fullPath: string;
  file: (success: (file: File) => void, failure?: (err: unknown) => void) => void;
}

export interface DirEntryLike {
  isFile: false;
  isDirectory: true;
  fullPath: string;
  createReader: () => { readEntries: (success: (entries: EntryLike[]) => void, failure?: (err: unknown) => void) => void };
}

export type EntryLike = FileEntryLike | DirEntryLike;

function readFile(entry: FileEntryLike): Promise<File> {
  return new Promise((resolve, reject) => entry.file(resolve, reject));
}

/** readEntries returns batches (about 100 in Chrome); call it until it returns none. */
async function readAll(entry: DirEntryLike): Promise<EntryLike[]> {
  const reader = entry.createReader();
  const all: EntryLike[] = [];
  for (;;) {
    const batch = await new Promise<EntryLike[]>((resolve, reject) => reader.readEntries(resolve, reject));
    if (batch.length === 0) {
      return all;
    }
    all.push(...batch);
  }
}

/** Walks dropped entries depth-first; each file keeps its path relative to the drop. */
export async function collectEntries(entries: EntryLike[]): Promise<UploadItem[]> {
  const items: UploadItem[] = [];
  const walk = async (entry: EntryLike): Promise<void> => {
    if (entry.isFile) {
      const file = await readFile(entry);
      items.push({ file, name: uploadName(file, entry.fullPath) });
      return;
    }
    for (const child of await readAll(entry)) {
      await walk(child);
    }
  };
  for (const entry of entries) {
    await walk(entry);
  }
  return items;
}

function isEntryLike(value: unknown): value is EntryLike {
  return typeof value === "object" && value !== null && "isFile" in value && "isDirectory" in value && "fullPath" in value;
}

/**
 * Reads a drop. Folders come through `webkitGetAsEntry` (dropped files carry
 * no `webkitRelativePath`); plain file drops fall back to `files`.
 */
export async function collectDrop(data: DataTransfer): Promise<UploadItem[]> {
  const entries: EntryLike[] = [];
  for (const item of Array.from(data.items)) {
    if (item.kind !== "file") {
      continue;
    }
    const entry: unknown = item.webkitGetAsEntry?.();
    if (isEntryLike(entry)) {
      entries.push(entry);
    }
  }
  if (entries.length > 0) {
    return collectEntries(entries);
  }
  return Array.from(data.files).map((file) => ({ file, name: uploadName(file) }));
}

/** Files from an <input type="file"> (with or without `webkitdirectory`). */
export function collectInput(list: FileList | null): UploadItem[] {
  return Array.from(list ?? []).map((file) => ({ file, name: uploadName(file) }));
}
