# Wayland browser-use 单一路径

2026-10-12，W2 集成候选；本机正式发布状态以最终验收报告为准。

`Pogo browser-use → 同一真实 Chromium Page → 私有 Wayland/Pixman output → VP8 → Go/Pion WebRTC → 用户 video`

每个会话拥有独立 Chromium profile 和 Wayland host；同会话 Sheet 使用同一登录状态，每个 Sheet 对应单独的真实原生窗口与 output。人工接管、交还和 Agent 的 claim 都绑定当前 Sheet，而不是整个浏览器。Agent 仍通过已有工具使用 DOM、Playwright/CDP、网络和 DevTools；人工鼠标拖动/滚动走原生 Wayland seat，键盘与一次中文提交在焦点同步后使用 CDP。浏览器没有网页代理、VNC 或截图视频 fallback。

Sheet 切换保留 Chromium 页面、表单、滚动位置和页面 JavaScript 状态。客户端保留当前连接及最近两个隐藏 Sheet 的连接，隐藏前先完成已接受输入并释放键鼠，再暂停该页的采集编码；同页另一可见查看者仍可正常观看。切回健康页面时复用 WebSocket、WebRTC peer 和媒体代次，并保留上次已呈现画面。新的可见性确认与实际视频呈现完成后才恢复输入，旧确认不能授权当前页面。

隐藏期间真实文档、控制权或视口发生变化，旧媒体仍会失效；再次显示时建立符合当前页面的新媒体。隐藏连接超过五分钟或超出保活数量时回收，保留的本页画面只用于过渡。关闭 Sheet、会话失效及真实连接故障仍执行原有清理与恢复。

Rust 负责原生合成与输入，Python 只做私有控制/帧桥，Go/Pion 负责 WebRTC，Node 保持现有认证与 browser-use 合同。截图仍用于工具证据，不是用户实时画面路径。软件 Pixman 与 VP8 编码不依赖 GPU；本次默认密度 1.25，viewport 对齐 8 像素。旧的 standard/responsive 菜单已移除。当前生产只使用 VP8，避免发送超出 Chromium 实际协商接收级别的 H.264。

每个新媒体代次用随机像素挑战证明当前文档对应真实 output，并确认挑战已从原生 buffer 消失，之后才允许该代次传输；客户端在实际呈现且尺寸一致后确认输入权限。旧 peer、旧文档、旧 control epoch、队列中的输入都无法授权新页面。关闭、取消和销毁等待媒体、原生窗口及进程清理完成再确认。

线上仍是 app + browser 两个 ARM64 镜像。认证及输入 WebSocket 通过 app 的 19777；视频直接使用 browser 发布的 UDP 范围（默认 19780–19811），范围内一个 mux listener 供 peers 共享。默认 WAN 保留 TWCC/GCC 和有界 pacing；明确 loopback 的 LAN 配置使用低延迟参数。HTTP 反向代理不能替代 UDP/TURN。本次没有改为只开放 19777。

生产 worker 的 Dockerfile 不包含旧扩展与 tabCapture 源码。旧源码及哈希仅归档于 `browser-runtime/lab/legacy-tabcapture/`，不能由配置启用；原 W1 已验收镜像保留作完整发布事务的回滚依据。P0 WebCodecs 原型与性能历史也仅作研究证据。

本次验收必须包含完整 Node 套件、真实 WebRTC/DOM/像素交互、拖动、中文提交、多 Sheet、接管交还、会话恢复、模型与工作流 fresh gates，以及普通账户复开和旧数据校验。性能单列输入 ACK、首次实际可见改变、最终稳定画面、光学滚动 FPS、CPU 与字体/几何匹配的清晰度。合成中文事件与自动解码时间不能冒充物理输入法或物理 input-to-photon；本机通过不代表服务器124、WAN丢包或并发容量已经通过。
