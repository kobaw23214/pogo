
export function formatSkillBytes(bytes?: number): string {
  if (bytes === undefined || !Number.isFinite(bytes) || bytes < 0) return "—";
  if (bytes >= 2 ** 30) return `${(bytes / 2 ** 30).toFixed(1)} GiB`;
  if (bytes >= 2 ** 20) return `${(bytes / 2 ** 20).toFixed(1)} MiB`;
  if (bytes >= 1024) return `${(bytes / 1024).toFixed(1)} KiB`;
  return `${bytes} B`;
}

export function skillFileError(file: Pick<File, "name" | "size">, limit: number): string {
  if (!file.name.toLowerCase().endsWith(".zip")) return "请选择 .zip 格式的 Skill 包。";
  if (!file.size) return "ZIP 文件为空，请重新选择。";
  // limit <= 0 是服务器声明的大小不限：不再做客户端预检。
  if (limit > 0 && file.size > limit) return `Skill 包超过 ${formatSkillBytes(limit)} 的上传上限。`;
  return "";
}

export function skillError(error: unknown): string {
  const text = error instanceof Error ? error.message : String(error);
  if (text.includes("version already installed")) return "这个 Skill 版本已安装，但内容不同。请使用新的版本号。";
  return text || "操作未完成，请重试。";
}
