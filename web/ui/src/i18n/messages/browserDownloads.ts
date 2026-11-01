export default {
  namespace: "browserDownloads",
  messages: {
    "zh-CN": {
      type_not_allowed: "文件类型不支持", invalid_file: "文件校验失败", empty_file: "下载文件为空",
      limit: "单个文件最大 {size}。接管后下载使用相同限制。", too_large: "文件超过下载上限", total_limit: "下载暂存空间不足", canceled: "下载已取消或中断", timeout: "下载超时", expired: "下载文件已过期", unavailable: "下载文件不可用", import_failed: "已下载，加入会话失败", rejected_file: "文件未能加入会话",
      title: "下载", hint: "下载完成后会加入本会话文件，可直接让 Agent 处理。", saved: "已加入本会话文件", in_progress: "下载中", completed: "正在加入会话", failed: "下载未完成", open: "打开文件", rejected: "有 {count} 个下载未能接收", label: "下载 · {count} 个文件", empty: "暂无下载文件", close: "关闭下载列表",
    },
    en: {
      type_not_allowed: "File type not allowed", invalid_file: "Downloaded file failed validation", empty_file: "Downloaded file is empty",
      limit: "Up to {size} per file. The same limit applies during takeover.", too_large: "File exceeds download limit", total_limit: "Download storage limit reached", canceled: "Download canceled or interrupted", timeout: "Download timed out", expired: "Downloaded file expired", unavailable: "Downloaded file unavailable", import_failed: "Downloaded, conversation import failed", rejected_file: "File could not be added to conversation",
      title: "Downloads", hint: "Completed downloads appear in this conversation's files, ready for the agent.", saved: "Added to conversation files", in_progress: "Downloading", completed: "Adding to conversation", failed: "Download incomplete", open: "Open file", rejected: "{count} downloads could not be received", label: "Downloads · {count} files", empty: "No downloads yet", close: "Close downloads",
    },
  },
};
