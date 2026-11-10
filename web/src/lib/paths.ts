// Root-relative path helpers for the File Manager (api.md section 4): paths
// are `/`-separated, have no leading slash, and "" is the root itself.

export function joinPath(dir: string, name: string): string {
  const cleanDir = dir.replace(/^\/+|\/+$/g, "");
  const cleanName = name.replace(/^\/+/, "");
  return cleanDir ? `${cleanDir}/${cleanName}` : cleanName;
}

export function parentPath(path: string): string {
  const index = path.lastIndexOf("/");
  return index <= 0 ? "" : path.slice(0, index);
}

export function baseName(path: string): string {
  const index = path.lastIndexOf("/");
  return index < 0 ? path : path.slice(index + 1);
}

export interface Crumb {
  name: string;
  path: string;
}

/** Splits `a/b/c` into crumbs for `a`, `a/b`, `a/b/c`. */
export function crumbs(path: string): Crumb[] {
  const parts = path.split("/").filter(Boolean);
  return parts.map((name, i) => ({ name, path: parts.slice(0, i + 1).join("/") }));
}

/** A single file or folder name: no separators, not `.` or `..`. */
export function validName(name: string): boolean {
  const trimmed = name.trim();
  return trimmed !== "" && trimmed !== "." && trimmed !== ".." && !/[/\\\0]/.test(trimmed);
}

/** Extension in lower case without the dot, or "". */
export function extension(path: string): string {
  const name = baseName(path);
  const index = name.lastIndexOf(".");
  return index <= 0 ? "" : name.slice(index + 1).toLowerCase();
}

const imageExtensions = new Set(["png", "jpg", "jpeg", "gif", "webp", "svg", "avif", "bmp", "ico"]);

export function isImage(path: string): boolean {
  return imageExtensions.has(extension(path));
}

/** Upload names come from `webkitRelativePath` or a dropped entry's `fullPath`. */
export function uploadName(file: { name: string; webkitRelativePath?: string }, fullPath?: string): string {
  const candidate = fullPath || file.webkitRelativePath || file.name;
  return candidate.replace(/^\/+/, "");
}
