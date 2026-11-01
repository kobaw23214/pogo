import type { MessageInputPart } from "@/api/types";
import { composerInputParts } from "./orderedInput";
import type { ComposerPart } from "./composerParts";

export interface StarterDraftFile {
  key: string;
  name: string;
  status: "uploading" | "ready" | "error";
  error?: string;
  path?: string;
  file?: Blob;
  members?: { file?: Blob; name: string; dir: string; path?: string }[];
}

/** Keep bytes by reference, but detach mutable upload state from the composer. */
export function captureStarterFiles(parts: readonly ComposerPart[], files: readonly StarterDraftFile[]): Map<string, StarterDraftFile> {
  const keys = new Set(parts.flatMap(part => part.type === "file" ? [part.key] : []));
  return new Map(files.filter(file => keys.has(file.key)).map(file => [file.key, {
    ...file, ...(file.members ? { members: file.members.map(member => ({ ...member })) } : {}),
  }]));
}

/** Upload only when Save is confirmed; keep successful uploads for a retry. */
export async function prepareStarterFileParts(
  parts: readonly ComposerPart[],
  files: Map<string, StarterDraftFile>,
  upload: (file: Blob, name: string, dir?: string) => Promise<{ path: string }>,
): Promise<MessageInputPart[]> {
  const resolved = new Map<string, { path: string; as_file?: boolean }[]>();
  // Check the whole draft before uploading anything. A failed reference must
  // remain visible, rather than yielding a partially saved shortcut.
  for (const part of parts) {
    if (part.type !== "file") continue;
    const file = files.get(part.key);
    if (part.status === "error" || part.status === "uploading" || file?.status === "error" || file?.status === "uploading") {
      throw new Error(file?.error || `文件尚未准备好：${part.label}`);
    }
    if (!part.path && !file?.path && !file?.file && !file?.members?.length) throw new Error(`无法找到文件：${part.label}`);
    if (file?.members?.some(member => !member.path && !member.file)) throw new Error(`文件夹中有文件不可用：${part.label}`);
  }
  for (const part of parts) {
    if (part.type !== "file" || resolved.has(part.key)) continue;
    const file = files.get(part.key);
    if (part.path && (!file || (file.path && part.path !== file.path))) {
      resolved.set(part.key, [{ path: part.path }]);
    } else if (file?.members) {
      const paths: { path: string; as_file: true }[] = [];
      for (const member of file.members) {
        if (!member.path) member.path = (await upload(member.file!, member.name, member.dir || undefined)).path;
        paths.push({ path: member.path, as_file: true });
      }
      resolved.set(part.key, paths);
    } else if (file) {
      if (!file.path) file.path = (await upload(file.file!, file.name)).path;
      resolved.set(part.key, [{ path: file.path }]);
    } else {
      resolved.set(part.key, [{ path: part.path! }]);
    }
  }
  return composerInputParts(parts, key => resolved.get(key) ?? []);
}
