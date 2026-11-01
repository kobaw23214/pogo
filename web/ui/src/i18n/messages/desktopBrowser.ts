export default {
  namespace: "desktopBrowser",
  messages: {
    "zh-CN": {
      title: "桌面浏览器", devtools: "开发者工具", refresh: "重新载入网页", connecting: "正在连接桌面网页…",
      unavailable: "桌面浏览器接口不可用，请重新打开应用。", surfaceFailed: "无法显示桌面网页：{message}",
      takeoverTools: "接管此页并等待页面空闲后，可引用元素或打开开发者工具。",
      readonly: "正在查看网页。接管后可直接输入、拖动和滚动。", nativeInput: "你正在控制网页，可直接输入、拖动和滚动。",
      hiddenForOverlay: "网页已暂时收起，以显示当前窗口。", picker: "点击网页元素加入对话，Esc 取消。",
      pickStale: "页面已变化，这次引用没有加入对话。请重新选取。", inspectFailed: "无法打开网页工具：{message}",
      invalidBridge: "桌面浏览器接口版本不兼容，请更新或重新打开应用。",
      openInDesktop: "此浏览器会话由桌面应用运行，请在 Pogo 桌面应用中打开此标签页。聊天和会话管理仍可在这里使用。",
    },
    en: {
      title: "Desktop browser", devtools: "Developer tools", refresh: "Reload page", connecting: "Connecting the desktop page…",
      unavailable: "Desktop browser interface unavailable. Reopen the app.", surfaceFailed: "Cannot display the desktop page: {message}",
      takeoverTools: "Take control of this tab and wait for it to become idle before referencing elements or opening developer tools.",
      readonly: "Viewing this page. Take control to type, drag and scroll directly.", nativeInput: "You have control. Type, drag and scroll directly in the page.",
      hiddenForOverlay: "The page is temporarily hidden while this window is displayed.", picker: "Click a page element to add it to chat. Esc cancels.",
      pickStale: "The page changed. This reference was not added; please select it again.", inspectFailed: "Cannot open page tools: {message}",
      invalidBridge: "Desktop browser interface version mismatch. Update or reopen the app.",
      openInDesktop: "This browser session runs in the desktop app. Open this tab in Pogo Desktop. Chat and session management remain available here.",
    },
  },
};
