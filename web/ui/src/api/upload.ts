import { hostAPIError } from "./hostErrorCopy";
import { gt, getLocale } from "@/i18n";
export interface UploadOptions {
  onProgress?: (loaded: number, total: number) => void;
  /** Transfer completion is not server-side installation completion. */
  onUploaded?: () => void;
  signal?: AbortSignal;
}

/** Pass File/FormData directly to the browser's network stack. Never read the
 * archive into JavaScript memory; all Skill uploads share this path. */
export function uploadFile<T>(
  path: string,
  file: File,
  opts: UploadOptions & { headers?: HeadersInit; raw?: boolean; contentType?: string } = {},
): Promise<T> {
  if (opts.signal?.aborted) return Promise.reject(new DOMException("aborted", "AbortError"));
  return new Promise<T>((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    let settled = false;
    const abort = () => xhr.abort();
    const finish = (result: () => void) => {
      if (settled) return;
      settled = true;
      opts.signal?.removeEventListener("abort", abort);
      xhr.upload.onprogress = null;
      xhr.upload.onload = null;
      xhr.onload = xhr.onerror = xhr.onabort = xhr.ontimeout = null;
      result();
    };
    xhr.open("POST", path);
    const headers = new Headers(opts.headers);
    if (!headers.has("Accept-Language")) headers.set("Accept-Language", getLocale());
    headers.forEach((value, key) => xhr.setRequestHeader(key, value));
    if (opts.raw) xhr.setRequestHeader("Content-Type", opts.contentType ?? "application/zip");
    xhr.upload.onprogress = (event) => {
      if (event.lengthComputable) opts.onProgress?.(event.loaded, event.total);
    };
    xhr.upload.onload = () => opts.onUploaded?.();
    xhr.onload = () => {
      const text = xhr.responseText ?? "";
      let data: unknown;
      try { data = JSON.parse(text); } catch { /* HTTP errors may be plain text. */ }
      if (xhr.status < 200 || xhr.status >= 300) {
        const error = typeof data === "object" && data !== null && "error" in data ? String(data.error) : text;
        finish(() => reject(Object.assign(hostAPIError(error || `HTTP ${xhr.status}`), { status: xhr.status })));
      } else if (typeof data !== "object" || data === null || Array.isArray(data)) {
        finish(() => reject(Object.assign(new Error(gt("uiCoverage.connection.uploadInvalidResult")), { status: xhr.status, uncertain: true })));
      } else {
        finish(() => resolve(data as T));
      }
    };
    xhr.onerror = () => finish(() => reject(new Error(gt("uiCoverage.connection.uploadDisconnected"))));
    xhr.ontimeout = () => finish(() => reject(new Error(gt("uiCoverage.connection.uploadTimedOut"))));
    xhr.onabort = () => finish(() => reject(new DOMException("aborted", "AbortError")));
    opts.signal?.addEventListener("abort", abort, { once: true });
    try {
      if (opts.raw) {
        xhr.send(file);
      } else {
        const form = new FormData();
        form.append("file", file, file.name);
        xhr.send(form);
      }
    } catch (error) {
      finish(() => reject(error));
    }
  });
}
