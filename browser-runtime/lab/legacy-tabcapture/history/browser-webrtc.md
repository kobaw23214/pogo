# Docker browser WebRTC 升级

2026-10-09。目标是改善 Docker Web 版的滚动、拖动和文字显示，保持用户零安装、同一浏览器执行路径。本文区分实现和实际验收；私有测试证据保存在 `.localagent/checks/browser-webrtc-20261009/`。

## 调研结论

WebRTC 改进的是远端画面的编码、传输、解码和播放。网页仍在容器 Chromium 中渲染，不会变成用户设备上的原生网页。它不能消除网站耗时、跨网络传播时间或远端输入派发成本，也不保证任何网络下文字都无损。我们没有证据把此 Docker 路线等同于 ChatGPT/Codex 桌面的内部实现。

旧链路通过 CDP 输出 JPEG，再由 WebSocket 传输。即使服务端丢弃积压图片，逐帧确认仍会使帧率受往返时间影响。仅把 JPEG 解码后转成 WebRTC 会保留前段开销和有损压缩，因此本次直接使用 Chromium 原生标签页采集和原生视频编码。

官方依据：Chrome 的 [屏幕与标签页采集说明](https://developer.chrome.com/docs/extensions/how-to/web-platform/screen-capture) 描述 tabCapture 与流授权；[WebRTC 连接](https://webrtc.org/getting-started/peer-connections)及 [TURN](https://webrtc.org/getting-started/turn-server) 说明协商、候选和中继；[内容提示标准](https://www.w3.org/TR/mst-content-hint/)允许文字内容提示，但不保证无损。实际呈现确认使用 [requestVideoFrameCallback](https://developer.mozilla.org/en-US/docs/Web/API/HTMLVideoElement/requestVideoFrameCallback)。

## 实现

`同一个 Chromium Page → 原生 tabCapture → WebRTC → 用户浏览器 video`

- worker 改用 full headless Chromium。内部打包的 MV3 扩展只负责精确标签授权，用户无需安装插件。目标通过 CDP targetId 匹配，不靠页面标题猜测。
- 每个会话仍使用独立 BrowserContext。首次实际观看时才创建扩展授权页与私有 loopback 媒体页，不为仅由 Agent 使用的会话提前分配媒体页面。两页按精确对象排除在标签列表之外；后者建立 PeerConnection，使 Chromium 的 UDP 端口范围设置实际生效。
- Agent 继续通过现有 browser-use、Playwright/CDP 操作相同 Page。人工输入仍经已鉴权 WebSocket，保留逐标签租约、队列、取消、按键释放与不重放约束。第一阶段不迁移 DataChannel，避免同时改动画面和输入可靠性。
- 普通显示只使用 WebRTC，旧 JPEG 连续流退役。引用元素与 Agent 取证仍可取得单次有版本截图，它们不是第二条实时浏览路径。
- Chromium 的实际显示像素密度和会话 DPR 默认都设为 1.5（部署可改为 1 或 2，见「远端显示密度」），两者始终相同。客户端按实际显示密度请求 1–2 倍像素，采集不超过远端密度、不低于 CSS 像素；尺寸保持偶数，最高 3840×2160。仅调整会话 DPR 会出现“视频尺寸变大、内容却只是放大”的问题；编码前的单设备像素（1/DPR CSS 像素）黑白细纹检查已验证这一差异。输入坐标始终是 CSS 像素。Agent 截图按该 worker 明确的实际显示倍率归一为 CSS 尺寸，避免把额外显示像素传给模型，也不影响其它浏览器宿主。
- 默认最高 60fps；码率上限按实际编码像素计算（约 3.8 bit/像素/秒，限制在 2–12Mbps，1728×2160 已达 12Mbps），文字内容提示、优先保持分辨率。以操作跟手为优先：受控 2 CPU 单观看者测试中，真实两倍像素视频达到约 54–57fps，CPU 约占一个核的 91%；30fps 上限的相同测试约占 51–52%。这些是特定页面的测试，不能保证任意网站或多个并发观看者都达到上限。默认最多 4 个活跃视频 peer；同一标签同一身份的多个观察者共享原始采集流，各自独立编码连接。
- 多观察者共享该标签首次采集的像素尺寸；较晚加入的高分屏不会自动升高既有流分辨率。实际像素尺寸随媒体元数据返回，不按请求值虚报清晰度。
- 文档身份依据主框架实际文档加载标识；同一文档内的 `pushState`、`replaceState`、锚点与历史切换只更新页面信息和元素引用，不重建视频。真正的新文档、控制代次和 viewport 变化建立新的 media epoch；只有新 epoch 的实际呈现尺寸匹配后才开放输入，确认不会阻塞后续每一帧。空白新标签页不启动编码器。
- 必须更换视频时，在停止旧接收器前保留最后已呈现画面，直到新视频实际呈现。保留画面仅用于显示，不能授权点击、键盘或视口操作；不会按视频帧率重复读回像素。切换会话、标签或关闭页面时清除保留画面，避免把其它页面的内容带入当前标签。
- 同一有效租约内迟到的旧媒体输入被拒绝，不重放，也不因此重新连接整个 WebSocket。控制权、会话、取消或未知执行结果等其它失效仍按原有严格隔离规则处理。
- 断线、旧帧、旧租约和未确认结果均拒绝输入；不重发网站动作。诊断公开帧率、解码等性能指标，不记录 SDP、ICE 地址或凭据。

## Docker 部署

主服务和 worker 仍是两个必要容器。除了现有 HTTP/WebSocket，视频需要 UDP 端口；默认宿主与容器对应 `19780–19811/udp`。worker 管理接口 7788 仍不对外发布。

完整 Chromium 比旧 headless shell 需要更多进程/线程。受控 ARM64 容量检查中，单 worker 的 16 个独立页面（每页带一个跨域 iframe）使用 763 个线程、约 1.59GiB；原 512 进程/线程限额在第 10 个页面已不足。因此 worker 的 pids 上限改为 1024，保留 2 CPU、3GiB 内存上限、沙箱和无额外权限配置。此检查不代表任意 16 个真实网站都满足同样资源预算。

在 `w_deploy/.browser.env` 设置 `POGO_BROWSER_WEBRTC_ADVERTISE_IP` 为客户端能到达的服务器 IP。它只替换服务器 host candidate 的地址，不是网站访问代理；本机 Docker Desktop 验证可用 127.0.0.1，Linux 服务器必须改成实际可达地址。受限网络可配置部署方提供的 `POGO_BROWSER_WEBRTC_ICE_SERVERS`；TURN 是可选外部基础设施，不能假设公网用户总能直接访问 UDP。公网部署应保护 HTTP/WS 信令与凭据。局域网现有 HTTP 接收端不会仅因接收 WebRTC 被强制拒绝。

### 网络档位

`POGO_BROWSER_WEBRTC_NETWORK` 控制依赖链路容量的发送参数，取值 `auto`（默认）、`lan`、`wan`，其它值使 worker 启动失败。

- `auto`：只有 `POGO_BROWSER_WEBRTC_ADVERTISE_IP` 是 loopback 地址（127.0.0.0/8、::1）且 `POGO_BROWSER_WEBRTC_ICE_SERVERS` 为空时取 `lan`，也就是观看端和 worker 在同一台机器上的部署（如本机 Docker Desktop 使用 127.0.0.1）。私有（10/8、172.16/12、192.168/16）、链路本地（169.254/16、fe80::/10）和 ULA（fc00::/7）地址一律取 `wan`：它们也可能经 Wi‑Fi、VPN 或跨站点链路到达，而下面的数据只覆盖 loopback。公网地址、共享地址（如 CGNAT/VPN 常用的 100.64/10）、未设置地址或配置了 STUN/TURN 时同样取 `wan`。Linux 部署没有显式地址时，Compose 用部署脚本探测到的出口地址 `POGO_BROWSER_WEBRTC_AUTO_IP`（通常是私网地址）填充 `POGO_BROWSER_WEBRTC_ADVERTISE_IP`，所以这类部署按 `wan` 运行。
- `lan`：Chromium 屏幕内容 pacer 以带宽估计的 2.5 倍发送，排队上限 250ms（`WebRTC-ProbingScreenshareBwe/2.5,250,80,40,-60,3/`；Chromium 默认 1.0 倍、2875ms）。「标准」观看者的每个媒体 epoch 发送端带宽估计从 8Mbps 起步、不低于 4Mbps（写入发送端应用的 answer 视频 fmtp：`x-google-start-bitrate`、`x-google-min-bitrate`；默认从 300kbps 起步）。「响应优先」不加起点和下限：实测它让单线程软件 H.264 编码更多、更大的帧，滚动帧龄反而增加。两种模式的观看端默认都按 0 播放延迟显示（`WebRTC-ForceSendPlayoutDelay/min_ms:0,max_ms:0/`，见下文和「播放延迟」）。私网部署只有确认是有线、低抖动、容量充足的局域网时才显式设为 `lan`：4Mbps 下限、2.5 倍排空和 0 播放延迟都假设容量充足且没有抖动，拥挤的 Wi‑Fi、VPN 或跨站点私网上不一定成立，这些链路没有测过。
- `wan`：网络相关参数保持 Chromium 默认值。在 50ms RTT / 1% 丢包的仿真中，带宽估计在 2–5 秒内只有本机的约一半，强制高起点或快速排空可能超出真实链路。

`POGO_BROWSER_WEBRTC_PLAYOUT_DELAY` 单独控制 0 播放延迟，取值 `auto`（默认：`lan` 下为 `zero`，`wan` 下为 `default`）、`zero`、`default`，其它值使 worker 启动失败。设为 `default` 时观看端保留抖动缓冲，`lan` 的 pacer 和带宽估计起点不变；`wan` 下设为 `zero` 没有测过。

所有 field trial 由 `src/media.js` 的 `mediaFieldTrials()` 拼成一个 `--force-fieldtrials`；VP8 编码线程上限与网络无关，所有档位都启用（见「编码线程与 CPU」）。worker 启动时输出 `WebRTC network profile: lan|wan` 和 `WebRTC playout delay: zero|default`，不输出地址。

现有 Compose overlay 不转发 `POGO_BROWSER_WEBRTC_NETWORK` 和 `POGO_BROWSER_WEBRTC_PLAYOUT_DELAY`，部署都按 `auto` 解析：advertise 127.0.0.1 的本机部署得到 `lan` / `zero`，私网地址部署得到 `wan` / `default`。要显式启用 `lan`，或单独关掉 0 播放延迟，需要在 worker environment 中增加变量。这属于 Compose 合同变更：`w_deploy/release.py` 的 image-only 发布会因运行配置与 Compose 不一致而拒绝，需要按部署流程重建 worker。

已知限制：field trial 作用于整个 worker Chromium 进程。tabCapture 要求被采集页面和媒体页在同一个浏览器里，所以 trial 无法只限定在 pogo 的观看连接上，远端网页自己建立的 WebRTC 也会继承。`lan` 下，网页通过自己的 RTCPeerConnection 发送的视频（如网页会议、canvas 推流）同样请求 0 播放延迟，对端 Chrome 会在真实网络上放弃抖动缓冲；如果那条轨是屏幕内容，还会按带宽估计的 2.5 倍排空。带宽估计起点和下限逐个写入 pogo 观看者的 answer，不受影响。worker 没有摄像头，只有网页自己产生视频源并发送时才会触发。`POGO_BROWSER_WEBRTC_PLAYOUT_DELAY=default` 可以单独关掉 0 播放延迟；pacer 设置只能随 `wan` 一起关闭。

隔离 loopback 栈实测（2026-10-11；worker 2 CPU / 3GiB，viewer 1440×1000 DPR2，采集 1728×2160；ifeng 连续滚动 3 段各 2 秒，ifeng 与 Python 文档短滚动；默认 9 次（前 6 后 3），`lan` 3 次；各次中位数的范围；证据 `.localagent/checks/browser-webrtc-tier1-20261010/phase2/item1/`）：

| 「标准」VP8 | Chromium 默认 | `lan` |
|---|---|---|
| 连续滚动每包发送排队 | 0.73–1.06s | 14–104ms |
| 连续滚动 capture→receive p95 | 1.44–1.89s | 151–259ms |
| 连续滚动帧龄 p95（按回调 / 按捕获选帧） | 1.20–1.97s / 1.94–2.12s | 168–292ms / 168–291ms |
| 连续滚动输入期间帧间隔 p95 / 最大 | 176–916ms / 183–916ms | 68–100ms / 84–101ms |
| 短滚动每包发送排队 | 417–519ms | 3–30ms |
| 短滚动帧龄 p95（按捕获选帧） | 852–1005ms | 96–162ms |
| 新 epoch 第 1 秒每包发送排队 | 157–284ms | 0–20ms |
| 新 epoch 0.5 秒时带宽估计 | 1.7–3.8Mbps | 5.9–6.3Mbps |

滚动中的带宽估计仍约 5.6–9Mbps：改善来自更快排空 pacer，而不是估计变大。代价是持续滚动时编码帧更多（约 23–25 对 11–18 fps，编码前丢帧约 6–15% 对 43–64%），2 CPU 配额下 CFS 节流周期增加（编码线程上限之后大幅减少，见「编码线程与 CPU」）；滚动停止后不再有积压帧持续到达，ifeng 短滚动后约有一次 250–530ms 的编码恢复停顿。轻页面 pointer 套件 p50 不变（hover / 拖动约 101ms，滚轮约 168ms）。「响应优先」只启用 pacer：新 epoch 第 1 秒排队 393–402ms → 129ms，连续滚动帧龄 p50 185–263ms → 91–208ms。码率上限仍为 12Mbps：滚动中编码目标码率约 4.7–9.5Mbps，上限没有成为约束，提高它只会增大每帧字节和编码开销。

按 `send_delay_ms_per_packet` 判定压力（不看 `qualityLimitationReason`）的策略改动经同样 A/B 未采用：`wan` 档位下发送排队、capture→receive 与帧龄均无变化（连续滚动第 1 段 867/771ms → 783ms），因为排队来自 VP8 在接近 QP 上限时的超量帧，降低码率上限只让 QP 更高，帧并不明显变小；`lan` 档位下持续滚动的残余排队（约 100ms）还会误触发降级。

两镜像必须一起升级。首次从旧 JPEG 部署迁移时，release 使用 `--browser-webrtc-migration`，核对旧 Compose 合同、建立冷备份、执行实际验收；失败同时恢复旧镜像和旧端口配置。此标志只允许已知的媒体配置迁移，不跳过任意 Compose 差异。已经启用 WebRTC 的部署通过普通双镜像 release 更新，无须再次迁移端口配置。新的 ARM 最小包不携带本机 advertise IP、历史数据或 backups。

### 编码线程与 CPU

libwebrtc 按宿主机全部核数决定 libvpx VP8 编码线程数（读 `sysconf`，容器 CPU 配额和 cpuset 都不影响）：18 vCPU 的 Docker Desktop VM 上，1080p 以上采集用 8 个线程。它们的突发在 2 CPU 配额下提前用完每个 100ms 周期的配额，整个 worker（合成、采集、输入派发）随之被 CFS 暂停。worker 因此在所有网络档位都加 `WebRTC-VideoEncoderSettings/encoder_thread_limit:2/`。「响应优先」的 OpenH264 在 packetization-mode 1 下只有一个 slice，本来就单线程，不受影响。

上限固定为 2（`src/media.js` 的 `MEDIA_ENCODER_THREAD_LIMIT`），不随 `POGO_BROWSER_CPUS` 变化，也不按 cgroup 配额推导：理由来自默认 2 CPU 配额下的测量，配额 4 下编码不是瓶颈（见下文）。每个观看者有自己的编码器，提高配额对多个观看者仍然有效，但单个观看者的 VP8 编码最多 2 个线程。性能只在配额 2 和 4、采集最高 1728×2160 下测过。最大视口 2560×1600（采集 3456×2160）只在默认配置下做过单次检查，不是 A/B：blackout / latency / pointer QA 和一次 ifeng 真实站点 smoke 的正确性检查全部通过；滚动输入期间 2 个线程编码约 11ms/帧，没有 CFS 节流，而每采集帧的 viz CPU 约 34ms（1728×2160 下约 18ms）。这时限制帧率和输入派发的是合成，不是编码。与同一镜像、默认视口的同次检查相比：悬停的 CDP 派发约 37ms/事件（默认视口 19ms），轻页面 hover / 拖动 p50 约 186ms（84 / 101ms），滚轮与移动交错时派发跟不上 16ms 的输入节奏，输入排队，p50 约 0.7 秒（35ms）。

隔离 loopback 栈实测（2026-10-11；worker 2 CPU / 3GiB，`lan`，viewer 1440×1000 DPR2，采集 1728×2160；默认 8 线程在前后各测 3 次，限 2 线程 3 次；各次中位数；证据 `.localagent/checks/browser-webrtc-tier1-20261010/phase2/item2/`）：

| 「标准」VP8 | 默认 8 线程（前 / 后） | 限 2 线程 |
|---|---|---|
| 连续滚动输入期间被节流的 100ms 周期 | 66% / 76% | 18% |
| 连续滚动 PSI full | 181 / 221 ms/s | 7 ms/s |
| 连续滚动编码耗时 | 13.8 / 15.0 ms/帧 | 9.3 ms/帧 |
| 连续滚动编码 CPU | 32 / 35 ms/帧 | 18 ms/帧 |
| 连续滚动编码帧率 | 28.5 / 29.8 fps | 31.7 fps |
| ifeng 短滚动编码帧率 / 输入期间帧间隔 p95 | 19 / 24 fps，85 / 116ms | 28 fps，50ms |
| 轻页面 hover 输入→可见 p95 / 最大（3 次合并） | 135 / 151ms，251 / 169ms | 119ms，135ms |
| 轻页面拖动输入→可见 p95 / 最大（3 次合并） | 185 / 252ms，220 / 300ms | 135ms，151ms |

轻页面 hover、拖动和滚轮的 p50 不变。连续滚动的 capture→present 与帧龄仍由 pacer 残余排队（约 80–110ms/包）决定，变化在运行间离散范围内。限 3 线程也测过：节流 33%、PSI 21ms/s，轻页面 hover p95 134ms，均不如 2。「响应优先」各项不变。

Compose 默认仍是 `cpus: ${POGO_BROWSER_CPUS:-2}`，不增加 cpuset 选项：

- 配额提高到 4（线程上限不变）只消除剩余节流（连续滚动 13–15% → 0% 周期，PSI 7–9 → 0.6ms/s），画面和输入指标都在离散范围内：连续滚动各段 capture→present p95 162–265 对 224–260ms，ifeng 短滚动 85–87 对 77ms，轻页面 hover p95 118–135 对 134ms。修改默认值会改变所有未设置该变量的部署解析出的 Compose 配置，`w_deploy/release.py` 的 image-only 发布会因此拒绝，需要重建 worker。需要更多 CPU 的部署（例如多个并发观看者，未测）直接设置 `POGO_BROWSER_CPUS`；单个观看者的 VP8 编码线程上限仍是 2。
- cpuset 不能代替线程上限：libwebrtc 仍按宿主机核数选线程，只有 Chromium 自身的线程池随 cpuset 缩小。2 核 cpuset（配额仍为 2）没有 CFS 节流，但所有工作只能在 2 个核上排队：连续滚动运行队列等待 30–47 → 1078ms/s，编码 8.5–8.7 → 22ms/帧、32–33 → 21fps，轻页面 hover p50 85–101 → 219ms、p95 118–135 → 351ms。

### 远端显示密度

`POGO_BROWSER_DEVICE_SCALE_FACTOR` 设定 worker Chromium 的显示密度（`--force-device-scale-factor`），同时也是每个会话的 DPR。允许值为 `1`、`1.5`、`2`；未设置或为空时取 `1.5`（`src/media-config.js` 的 `MEDIA_DEFAULT_DEVICE_SCALE_FACTOR`，此前为 `2`）；其它值会让 worker 启动失败。worker 启动时输出 `Device scale factor: N`。三处使用同一个值：会话 DPR、采集上限（观看端请求 1–2 倍，实际采集不超过这个密度，否则只是放大），以及 Agent 截图归一到 CSS 尺寸时用的倍率。网页布局、CSS 视口和输入坐标不变。

现有 Compose overlay 不传这个变量，部署按代码默认值 1.5 运行，只换 worker 镜像即可生效。要回到 `2`（高分屏文字细节最多）或改用 `1`，在 worker 的 environment 中加入 `POGO_BROWSER_DEVICE_SCALE_FACTOR=2`（或 `1`）后重建 worker。和 `POGO_BROWSER_WEBRTC_NETWORK` 一样，这会改变 Compose 合同，`w_deploy/release.py` 的 image-only 发布会拒绝，需要按部署流程重建。

它省下的是远端合成器的成本。每采集一帧，viz 都要按显示密度重绘整个页面表面并读回：DPR 2 下 1280×1600 视口对应 2560×3200 像素，约 17ms，占 worker CPU 的最大份额；1.5 下是 1920×2400 像素，约 14ms。代价是观看端在高分屏（DPR 2）上看到的文字更软。

默认值改为 1.5 的依据（第二轮，当前代码含 0 播放延迟；隔离 loopback 栈，worker 2 CPU / 3GiB，`lan`；viewer 1440×1000 面板停靠，远端 CSS 视口 1280×1600，两种观看端都采集 1728×2160 或 1280×1600，与 `2` 相同；每臂 3 次，A/B/A，各次中位数，方括号为 1.5 臂的运行间范围；证据 `.localagent/checks/browser-webrtc-round2-20261011/ab/ab4-*.json`）：

| | 观看端 DPR 2：`2`（前 / 后） | `1.5` | 观看端 DPR 1：`2`（前 / 后） | `1.5` |
|---|---|---|---|---|
| 「标准」轻页面 hover 输入→可见 p50 / p95 | 70 / 102；69 / 86ms | 35 / 36ms | 35 / 35；35 / 36ms | 34 / 36ms |
| 「标准」轻页面拖动 / 滚轮 p50 | 84 / 119；71 / 120ms | 35 / 67ms | 35 / 52；35 / 52ms | 35 / 52ms |
| 「标准」hover CDP 派发 p50 | 17.6；18.3ms | 1.0ms | 1.4；0.8ms | 0.7ms |
| 60ms 处理的慢页面拖动 / hover p50 | 280 / 231；268 / 233ms | 253 / 219ms | 252 / 217；252 / 209ms | 223 / 202ms |
| viz CPU / 采集帧（轻页面 hover） | 17.1；17.5ms | 14.2ms | 12.9；13.1ms | 10.2ms |
| ifeng 短滚动 capture→present p95 | 66；74ms | 90ms [87–90] | 25；28ms | 47ms [23–79] |
| docs.python.org 短滚动 capture→present p95 | 122；140ms | 113ms [95–144] | 39；59ms | 31ms [30–45] |
| 连续滚动第 3 段 capture→present p95 | 247；244ms | 270ms [250–271] | 251；228ms | 238ms [235–267] |
| 文字梯度 / 奈奎斯特以内条纹（相对同会话 `2`） | 1 | 0.945–1.071 | 1 | 0.936–1.057 |

DPR 2 观看端的「标准」轻页面输入→可见约减半，派发不再排队；DPR 1 观看端只有慢页面和「响应优先」连续滚动略快（3–29ms）。代价是 ifeng 短滚动 capture→present p95 在 DPR 2 下高约 16–24ms（每包发送排队 2–5 → 10ms），连续滚动和 docs.python.org 在运行间范围内。最大视口（2560×1600，采集 3456×2160，DPR 2 沉浸式）下 hover p95 从 202 / 192 降到 140ms，滚轮与移动交错 p50 从 551 / 520 降到 290ms，文字梯度 0.93–1.01。正确性：A/B 中 171 次 QA 运行、小数 DPR 专项（四角与边缘、跨站 iframe、滚动后、奇数高度、首帧偶数尺寸）、blackout / latency / pointer 守卫全部通过。解码帧截图对在证据目录 `step2/screens/`。

以下是第一档时的测量（2026-10-11，0 播放延迟之前；证据 `.localagent/checks/browser-webrtc-tier1-20261010/phase2/item3/`），用于比较 `1`：

| 观看端 DPR 2 | `2`（前 / 后） | `1.5` | `1` |
|---|---|---|---|
| 编码尺寸 | 1728×2160 | 1728×2160（2160 行上限） | 1280×1600 |
| 「标准」连续滚动 viz CPU / 采集帧 | 17.0 / 16.8ms | 14.2ms | 3.4ms |
| 「标准」连续滚动 worker CPU | 1.4 核 | 1.2 核 | 0.6 核 |
| 「标准」连续滚动 capture→present p95（第 1 / 2 / 3 段） | 166 / 257 / 251ms；130 / 273 / 254ms | 210 / 275 / 255ms | 60 / 197 / 164ms |
| 「标准」ifeng 短滚动 capture→present p95 | 90 / 90ms | 89ms | 43ms |
| 「标准」轻页面 hover 输入→可见 p50 / p95 | 85 / 119ms；85 / 118ms | 36 / 53ms | 19 / 36ms |
| 「标准」轻页面拖动 p50 / p95 | 101 / 118ms；101 / 119ms | 52 / 69ms | 35 / 52ms |
| 「标准」轻页面滚轮 p50 / p95 | 135 / 185ms；137 / 186ms | 69 / 85ms | 68 / 85ms |
| 「响应优先」连续滚动编码帧率 / 编码耗时 | 4–7 fps / 55–113ms | 4–8 fps / 59–114ms | 25–34 fps / 15–16ms |
| 「响应优先」连续滚动第 2 段 capture→present p95 | 265 / 259ms | 230ms | 51ms |
| 「响应优先」轻页面 hover p50 / p95 | 85 / 119ms；86 / 119ms | 85 / 103ms | 52 / 69ms |
| 文字边缘梯度（相对 `2`） | 1 | 0.95–1.07 | 0.88–0.93 |

| 观看端 DPR 1（编码尺寸均为 1280×1600） | `2`（前 / 后） | `1` |
|---|---|---|
| 「标准」连续滚动 viz CPU / 采集帧，worker CPU | 11.9 / 11.7ms，1.0 核 | 3.3ms，0.7 核 |
| 「标准」连续滚动第 2 段每包发送排队 / capture→present p95 | 107 / 113ms，237 / 250ms | 120ms，244ms |
| 「标准」ifeng 短滚动 capture→present p95 | 60 / 59ms | 44ms |
| 「标准」轻页面 hover / 滚轮 p50 | 35 / 35ms，68 / 84ms | 34ms，53ms |
| 「响应优先」轻页面拖动 / 滚轮 p50 | 68 / 68ms，85 / 85ms | 51ms，68ms |
| 文字边缘梯度（相对 `2`） | 1 | 0.96–1.13 |

- `1`：远端合成成本下降约 80%。对 DPR 2 观看端，「标准」轻页面输入→可见约减半，「响应优先」连续滚动编码帧率提高约 7 倍（第 2 段中位数 4.7 → 33.9 fps），因为 H.264 要编码的像素少了 45%。代价是文字清晰度：同一个停靠面板里，高分屏看到的文字边缘梯度降到 `2` 的 0.88–0.93，10–90% 边缘过渡从 0.88 变成 1.37 个设备像素。0.5 CSS 像素细纹在 DPR 1 下无法表示，这是设计如此，不是回归。对 DPR 1 观看端，画面清晰度不变或略好，滚动和滚轮略快；连续滚动仍受 pacer 残余排队限制，没有变化。
- `1.5`：在 1280×1600 视口下，编码尺寸仍受 2160 行上限，和 `2` 相同，所以编码、码率和「响应优先」都不变。viz 从约 17ms 降到 14ms，「标准」轻页面输入→可见 p50 从 85–101ms 降到 36–52ms，CDP 派发 p50 从 18ms 降到 1.4ms。推测原因是 viz 每帧不再超过 16.7ms 的帧间隔，不再拖慢输入派发，这一点尚未验证。文字清晰度在 DPR 2 和 DPR 1 观看端都是 `2` 的 0.94–1.07。连续滚动没有改善。第二轮在当前代码上复测（上表），结论相同，因此改为默认值。
- 所有臂的 QA 正确性检查都通过（滚动量精确、按下/抬起成对、不重放），共 126 次运行，pointer 未到达 0；首帧尺寸校验通过，解码尺寸等于声明的采集尺寸。

默认 1.5 的保留条件：两种观看端下每个文字区域和奈奎斯特以内条纹都不低于同会话 `2` 的 0.93，全部正确性守卫通过，输入延迟收益超过运行间离散度。数据只覆盖单观看者、本机 loopback 和上述页面。

### 播放延迟

`POGO_BROWSER_WEBRTC_PLAYOUT_DELAY` 解析为 `zero` 时（`lan` 的默认），发送端在每个视频帧的 RTP playout-delay 扩展（`http://www.webrtc.org/experiments/rtp-hdrext/playout-delay`，Chromium 默认协商，两种模式实测都已协商）中写入 min=0、max=0。观看端 Chromium 因此走低延迟渲染：帧解码后立即呈现，不再为平滑抖动而留存。解析为 `default` 时（`wan` 的默认）不写这个扩展，抖动缓冲照常工作。网页端不设置 `RTCRtpReceiver.jitterBufferTarget`。`jitterBufferTargetDelay` 统计在启用后仍为 25–143ms，不反映实际留存，不能用来判断效果；应看 `jitterBufferDelay` 减去 `totalAssemblyTime` 的每帧值和 rVFC 的 receive→present。

隔离 loopback 栈实测（2026-10-11；条件同「编码线程与 CPU」，DPR 2 默认；未启用在前后各测 3 次、启用 3 次，各场景各次中位数，记为「前 / 后 → 启用」；证据 `.localagent/checks/browser-webrtc-tier1-20261010/phase2/item4/`）：

| | 未启用（前 / 后） | 启用 |
|---|---|---|
| 「标准」连续滚动接收端留存（第 1 / 2 / 3 段，ms/帧） | 16 / 61 / 83；13 / 44 / 76 | 1.6 / 1.8 / 2.3 |
| 「标准」连续滚动 receive→present p95（第 1 / 2 / 3 段） | 120 / 150 / 208；79 / 133 / 205ms | 12 / 15 / 19ms |
| 「标准」连续滚动 capture→present p50（第 1 / 2 / 3 段） | 177 / 199 / 228；97 / 189 / 230ms | 112 / 167 / 198ms |
| 「标准」每段滚动停止后的静止期 capture→present p50（第 1 / 2 / 3 段） | 174 / 164 / 232；97 / 171 / 237ms | 13 / 13 / 13ms |
| 「标准」ifeng 短滚动 capture→present p50 / p95 | 75 / 91；81 / 97ms | 30 / 75ms |
| 「响应优先」ifeng 短滚动 capture→present p50 / p95 | 92 / 117；91 / 106ms | 72 / 102ms |
| 「标准」轻页面 hover p95 / 拖动 p50 / 拖动 p95 | 118 / 101 / 119；119 / 101 / 135ms | 101 / 87 / 103ms |
| 「标准」轻页面滚轮+移动 p50 / p95 | 68 / 86；68 / 85ms | 35 / 53ms |
| 「响应优先」轻页面 hover p50 / p95 | 101 / 119；85 / 119ms | 69 / 87ms |
| 「响应优先」轻页面滚轮 p50 / p95 | 120 / 152；119 / 152ms | 85 / 103ms |
| 「标准」连续滚动帧间隔（3 段合并，每段不计第一个间隔）p95 / p99 / 最大 | 84 / 133 / 167；68 / 133 / 233ms | 83 / 117 / 151ms |
| 「响应优先」连续滚动帧间隔（3 段合并）p95 | 499 / 418ms | 266ms |

- 两种模式的接收端留存都降到每帧 0–2ms（轻页面各用例 5–17 → 0–0.4ms）。未启用时留存随连续滚动逐段累积，滚动停止后仍有 100–240ms。「标准」连续滚动呈现的帧数多约 13%。
- 守卫：三段合并的帧间隔（每段不计第一个间隔）p95 不变，p99 和最大值略好；每次运行中 ≥100ms 的间隔数（6 / 4 / 7、4 / 3 / 3 → 7 / 3 / 7）在离散范围内；冻结次数不变；「标准」输入期间没有超过 250ms 的间隔（「响应优先」两臂相同，受编码帧率限制）。单次运行有变差：连续滚动第 3 段启用后 3 次中 2 次的输入期间帧间隔 p95 为 100ms（未启用 6 次都是 67ms），单次最大 150–183ms（未启用 67–134ms）。这些最大值都是该段滚动开始后的第一个帧间隔：启用后 9 段中 8 段为 150–200ms，未启用为 17–151ms；把它计入合并样本，启用后 p95 / p99 / 最大为 100 / 151 / 200ms，未启用为 84 / 133–149 / 167–233ms。都低于 250ms 的冻结门槛。
- 连续滚动的 capture→present p95 不变（约 250ms），仍由发送端 pacer 残余排队决定。轻页面慢处理用例只快 3–30ms，主要成本在网页自身。
- 54 次运行的 QA 正确性检查全部通过，pointer 未到达 0。

## 验收边界

已完成的隔离采集实验证明 ARM64 Playwright 镜像能够原生采集、编码和通过 Docker UDP 映射连接本机浏览器；这不代表正式发布、广域网或物理输入体验已通过。

发布前必须完成：运行时/前端回归、真实构建界面的输入与引用、逐标签接管、沉浸 resize、断线与不重放、模型/工作流、现有会话保存，以及相同条件的旧 JPEG/新 WebRTC 性能对比。物理中文输入法、真实鼠标与外部网络体验需要单独记录，不能套用以前 Electron 的人工反馈。

视频生命周期修改还须验证连续显示：普通点击、焦点更新地址、同文档历史操作与连续路由更新不得重建媒体；真正跳转期间须始终显示当前视频或只读保留画面，并在新帧实际呈现后才恢复输入。页面自身长任务可能仍冻结内容更新，不能把保留画面误报为网站已经完成加载。此次黑屏回归的私有证据保存在 `.localagent/checks/browser-webrtc-blackout-fix-20261009/`。
