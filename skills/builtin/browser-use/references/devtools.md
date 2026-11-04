<!-- browser-use 按需参考:本文件由 SKILL.md 链接,细节在此维护,正文保持精简。 -->

### 标准调试协议与网络观察

不熟悉当前调试能力时先查看帮助：

```json
{"action":"devtools","method":"help"}
{"action":"devtools","method":"targets.list"}
{"action":"devtools","method":"network.list","params":{"limit":50}}
{"action":"devtools","method":"console.list","params":{"limit":50}}
```

`targets.list` 列出当前标签页及已附加的 OOPIF、dedicated worker，包含 `target_ref`、类型、父目标、`ready`、`paused` 和覆盖范围。只对 `ready:true` 的目标操作。标准 CDP 默认作用于根页面；要检查 iframe 或 worker，把原样返回的 `target_ref` 放在顶层 `target`，不能拿元素 ref、tab ID 或原始 targetId 代替。

```json
{"action":"devtools","target":"<已就绪 worker 的 target_ref>","method":"Runtime.evaluate","params":{"expression":"self.location.href","returnByValue":true}}
{"action":"devtools","target":"<已就绪 iframe 的 target_ref>","method":"DOM.getDocument","params":{"depth":1}}
```

`network.list`、`console.list`、`events.list` 默认聚合当前标签页所属目标，记录带 `target_ref`。指定顶层 `target` 则只读该目标的记录。需要正文时，把列表中原样返回的 `request_id` 传给 `network.read`：

```json
{"action":"devtools","method":"network.read","params":{"request_id":"<列表返回的 request_id>"}}
```

读取日志不会再次请求网站，不要为了“查看响应”重放表单、点击提交或另发同 URL 请求。检查 HTTP 状态、失败原因、响应正文状态以及来源页签；`pending`、`truncated`、`unavailable`、`evicted`、`binary`、`streaming` 不能当成完整响应。列表可用 `cursor`、`limit` 分页，按返回的 `next_cursor` 继续；记录有容量限制，缺少旧记录不证明网站没有发送过请求。

需要进一步检查时，使用帮助中允许的标准页面 CDP 方法，例如：

```json
{"action":"devtools","method":"DOM.getDocument","params":{"depth":1}}
{"action":"devtools","method":"Runtime.evaluate","params":{"expression":"({title:document.title,readyState:document.readyState})","returnByValue":true}}
{"action":"devtools","method":"events.list","params":{"method":"Runtime.exceptionThrown","limit":50}}
```

协议节点、对象、脚本和调用栈 ID 都必须来自实际观察，并与对应 `target_ref` 一起使用，不能跨目标混用。导航、切换页签、目标重建、取消或控制权变化后重新发现目标。worker 不具有页面的全部 domain；协议拒绝时核对类型和方法，不把失败操作偷偷改发到根页面。当前递归覆盖实际已附加的 OOPIF 和 dedicated worker，不保证 shared/service worker、其他标签或附加前历史。查看 coverage 中初始化、容量拒绝和部分覆盖状态，未记录到不等于未发生。浏览器级和任意 Target/Context 路由仍不可直接访问。

所有标准 CDP 方法（如 `Runtime.evaluate`、`DOM.getDocument`，包括读取操作）都会使之前的元素引用和截图坐标失效。随后使用 `eN` 引用前，先重新 `snapshot` 或 `find` 并使用新引用；坐标操作前重新截图。`network.list`、`network.read`、`console.list`、`events.list`、`targets.list` 等日志或目标列表读取本身不会使这些引用失效。不要先尝试旧引用，再靠报错恢复。

`Runtime.evaluate` 能执行页面 JavaScript，不是只读沙箱。优先读取完成当前任务所需的状态；脚本写入、请求、点击或提交与普通页面操作具有同样的外部效果，仍受用户目标和控制权约束。不要输出密码、token、Cookie 或无关账号数据；脱敏不能识别所有编码或改名的敏感数据。

### 暂停中的任务

已派发的 click 或 `Runtime.evaluate` 命中断点时会及时返回 pending，原动作仍在等待。任务开始时也可能已经暂停。按当前观察推进：

1. 用 `targets.list` 确认暂停的目标，再对该 `target` 调用 `events.list`，按 `Debugger.paused` 筛选。保留真实 `callFrameId`、`location.scriptId` 和 `scopeChain`；不要重复点击或求值来启动原动作。
2. 从相关调用帧的 `local` 作用域取 `object.objectId`，用 `Runtime.getProperties`、`ownProperties:true` 读取实际属性名和值；需要时再读相关 `closure`。对象值只展开任务所需的那一层，使用返回的 `value.objectId`。已有实际变量名时，也可用 `Debugger.evaluateOnCallFrame`、`returnByValue:true` 直接读取；一种读取已提供完整字段时无需再走另一种。不猜局部变量名、辅助函数或不存在的 `local` 对象，不枚举整个全局对象或原型链。
3. 所需暂停状态已读清后，对同一目标调用一次普通 `Debugger.resume`，继续原动作。若再次命中断点，先读取新暂停事件；不重复派发原始操作。只有排查源码或暂停位置确有必要时，才用已观察的 scriptId 读取脚本；`callFrame.url` 为空不等于该帧无效。
4. 用已有动作回执、实际页面记录或已记录的完整响应，核验目标结果及所需记录身份，随后提交最终答案。只有当前证据不足时才补查其他通道；不要把进度说明留作最终交付。

```json
{"action":"devtools","target":"<暂停事件的 target_ref>","method":"Runtime.getProperties","params":{"objectId":"<实际 scopeChain 或属性值中的 objectId>","ownProperties":true}}
{"action":"devtools","target":"<同一暂停目标的 target_ref>","method":"Debugger.resume"}
```

协议外层 `ok:true` 不代表 JavaScript 求值成功；出现 `exceptionDetails` 时，按实际错误纠正读取方式，不把求值失败当作已取得目标数据。所属目标的暂停会冻结本标签页在途 CDP 的预算，新发起的诊断仍有超时。目标页被人工接管时，停止操作该页；任务需要接续该页且人工输入或登录挑战已完成时，可先读取新鲜 `tabs` 再自主评估是否 `claim`，否则保留人工控制并选择任务范围内其他页面。取消、连接断开或结果未知时停止后续操作，等待明确恢复后重新发现目标，不重放旧动作，也不用新开标签或普通恢复绕过取消。
