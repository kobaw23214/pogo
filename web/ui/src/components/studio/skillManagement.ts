import { gt as tr } from "@/i18n";

export function formatSkillBytes(bytes?: number): string {
  if (bytes === undefined || !Number.isFinite(bytes) || bytes < 0) return "—";
  if (bytes >= 2 ** 30) return `${(bytes / 2 ** 30).toFixed(1)} GiB`;
  if (bytes >= 2 ** 20) return `${(bytes / 2 ** 20).toFixed(1)} MiB`;
  if (bytes >= 1024) return `${(bytes / 1024).toFixed(1)} KiB`;
  return `${bytes} B`;
}

export function skillFileError(file: Pick<File, "name" | "size">, limit: number): string {
  if (!file.name.toLowerCase().endsWith(".zip")) return tr('studioManagement.text510');
  if (!file.size) return tr('studioManagement.text511');
  // limit <= 0 是服务器声明的大小不限：不再做客户端预检。
  if (limit > 0 && file.size > limit) return tr('studioManagement.text512', { value0: formatSkillBytes(limit) });
  return "";
}

export function skillError(error: unknown): string {
  const text = error instanceof Error ? error.message : String(error);
  if (text.includes("version already installed")) return tr('studioManagement.text513');
  return text || tr('studioManagement.text514');
}
