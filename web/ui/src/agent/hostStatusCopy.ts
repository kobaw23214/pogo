import { gt } from "@/i18n";

// These exact strings are host-owned status fields from successful API replies,
// not user content or arbitrary MCP/provider errors. Unknown text stays verbatim.
const HOST_REASONS = new Map<string, string>([
  ["同容器进程沙箱", "runtimeLabel"],
  ["调用超时与输出大小限制；内存、CPU 和进程数量由监控控制，硬资源上限由整个 Pogo 容器共享。", "runtimeResourceLimits"],
  ["此 Skill 版本已停用，请上传新的版本。", "skillVersionRevoked"],
  ["文档超过预览长度，无法在线编辑", "documentTooLong"],
  ["仅支持编辑 SKILL.md，其他包内文件保持只读", "instructionOnly"],
  ["仅上传的普通 Skill 可编辑；内置、目录挂载及运行时包保持只读", "uploadedOnly"],
  ["Skill 路径不可安全写入，符号链接或异常占用文件保持只读", "unsafePath"],
  ["SKILL.md 不可读取或超过 256 KiB，无法在线编辑", "unreadableInstruction"],
  ["运行时 Skill 包由摘要固定，请重新导入新版本，不能直接改写", "pinnedPackage"],
  ["历史版本只读，可从此版本恢复为新版本", "historicalVersion"],
  ["参考文档只读；可让 Agent 使用 skill_save 保存完整的新版本", "referenceDocument"],
  ["此条目已不存在或不可读取", "entryUnavailable"],
  ["符号链接不支持浏览或预览", "symlink"],
  ["目录深度超过预览上限", "directoryTooDeep"],
  ["运行包仅支持预览 SKILL.md 和 references/ 文本文档", "packageTextOnly"],
  ["预览最多显示 256 KiB", "previewLimit"],
  ["此类型不支持预览", "unsupportedPreview"],
  ["此条目名称不支持安全预览", "unsafeName"],
  ["此文件不可读取", "fileUnreadable"],
  ["此文件不是可预览的 UTF-8 文本文档", "utf8TextOnly"],
  ["当前部署尚未配置 Office 编辑服务。", "officeNotConfigured"],
  ["Office 编辑服务暂时不可用，请稍后重试；聊天和浏览器仍可使用。", "officeUnavailable"],
  ["Office 配置或签名文件无效，请检查部署配置；聊天和浏览器仍可使用。", "officeConfigError"],
]);

export function knownHostReason(reason?: string | null): string {
  if (!reason) return "";
  const key = HOST_REASONS.get(reason);
  if (key) return gt(`settingsCoverage.hostReasons.${key}`);
  const removed = /^已按替换原则删除旧版本 v([^\r\n]+)。仍引用它的会话会保留，但不再能使用该 Skill。$/.exec(reason);
  return removed ? gt("settingsCoverage.hostReasons.previousVersionRemoved", { version: removed[1] }) : reason;
}
