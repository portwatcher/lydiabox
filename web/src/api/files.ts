import { ApiError, NetworkError, buildUrl, errorFromResponse, http, seg } from "./http";
import { sharedRequests } from "./shared";
import type {
  Entry,
  ListResponse,
  RootName,
  RootsResponse,
  SaveTextRequest,
  TextFile,
  UploadResponse,
} from "./types";

const base = (root: RootName) => `/api/v1/fs/${seg(root)}`;

// The listing and the folder tree list the open folder at the same time.
const sharedList = sharedRequests<ListResponse>();

// Section 4: File Manager.
export const files = {
  roots: () => http.get<RootsResponse>("/api/v1/fs/roots"),
  list: (root: RootName, path: string, signal?: AbortSignal) =>
    sharedList(`${root}\n${path}`, (s) => http.get<ListResponse>(`${base(root)}/list`, { path }, s), signal),
  stat: (root: RootName, path: string) => http.get<Entry>(`${base(root)}/stat`, { path }),
  readText: (root: RootName, path: string, signal?: AbortSignal) =>
    http.get<TextFile>(`${base(root)}/text`, { path }, signal),
  writeText: (root: RootName, path: string, body: SaveTextRequest) =>
    http.put<Entry>(`${base(root)}/text`, body, { path }),
  mkdir: (root: RootName, path: string, parents = false) =>
    http.post<Entry>(`${base(root)}/mkdir`, { path, parents }),
  rename: (root: RootName, from: string, to: string, overwrite = false) =>
    http.post<Entry>(`${base(root)}/rename`, { from, to, overwrite }),
  remove: (root: RootName, path: string, recursive = false) =>
    http.delete(`${base(root)}/entry`, { path, recursive: recursive || undefined }),
  downloadUrl: (root: RootName, path: string, inline = false) =>
    buildUrl(`${base(root)}/download`, { path, inline: inline || undefined }),
};

/** One file to upload and the relative path it is stored under. */
export interface UploadItem {
  file: Blob;
  /** Relative path below the target dir, such as `photos/2026/a.txt`. */
  name: string;
}

export interface UploadProgress {
  loaded: number;
  total: number;
}

export interface UploadHandle {
  promise: Promise<UploadResponse>;
  abort: () => void;
}

/** Builds the multipart body: every part is named `file`, its filename the relative path. */
export function buildUploadForm(items: UploadItem[]): FormData {
  const form = new FormData();
  for (const item of items) {
    form.append("file", item.file, item.name);
  }
  return form;
}

/**
 * Uploads with XMLHttpRequest, which is the browser API that reports upload
 * progress. Errors are decoded from the same envelope as fetch requests.
 */
export function upload(
  root: RootName,
  dir: string,
  items: UploadItem[],
  options: { overwrite?: boolean; onProgress?: (p: UploadProgress) => void } = {},
): UploadHandle {
  const xhr = new XMLHttpRequest();
  const url = buildUrl(`${base(root)}/upload`, { dir, overwrite: options.overwrite || undefined });
  const promise = new Promise<UploadResponse>((resolve, reject) => {
    xhr.open("POST", url);
    xhr.withCredentials = true;
    xhr.setRequestHeader("Accept", "application/json");
    xhr.upload.onprogress = (event) => {
      if (event.lengthComputable && options.onProgress) {
        options.onProgress({ loaded: event.loaded, total: event.total });
      }
    };
    xhr.onerror = () => reject(new NetworkError("Unable to reach the LydiaBox server."));
    xhr.onabort = () => reject(new DOMException("Upload canceled", "AbortError"));
    xhr.onload = () => {
      const headers = new Headers();
      const retry = xhr.getResponseHeader("Retry-After");
      if (retry) {
        headers.set("Retry-After", retry);
      }
      const res = new Response(xhr.responseText || null, { status: xhr.status, headers });
      if (xhr.status >= 200 && xhr.status < 300) {
        res
          .json()
          .then((body: UploadResponse) => resolve(body))
          .catch(() => reject(new ApiError(xhr.status, "internal", "The server sent a response that is not JSON.")));
        return;
      }
      void errorFromResponse(res).then(reject);
    };
    xhr.send(buildUploadForm(items));
  });
  return { promise, abort: () => xhr.abort() };
}
