# 浏览器自动复用登录态与模拟平台测试

> 历史测试记录：本文描述 2026-10-03 的协议。当前 `api_read` / `auth_status` 和旧 auth/auth-auto 在线评测已退役，不按本文启动当前产品验收；当前路径见 [单一路径迁移](browser-native-only.md)。旧候选端口和镜像也不代表当前源码。

更新：2026-10-03。Pogo 已移除额外的“授权接口读取”菜单与手填权限表单。用户请求浏览器完成当前站点任务后，`api_read` 直接复用该浏览器会话的登录态；没有接口白名单、返回字段授权名单或 Pogo 的15分钟权限有效期。

## 测试入口

[独立候选 Pogo](http://pogo-auth-lab.localhost:19778/manage)；登录账号 `admin`，密码在仓库根 `.localagent/browser-auth-lab-private/admin-password.txt`。浏览器 Cookie 不按端口隔离，所以人工入口使用独立 localhost 子域。

在 Pogo 的托管浏览器中访问 `http://mock-platform:5190/`。宿主机直接预览用 `http://127.0.0.1:5190/`。模拟账号 `demo / pogo-demo-2026`，模拟二次验证码 `246810`。真实系统密码由用户在浏览器接管输入。

一次完整模拟任务的提示词：

```text
/skill:browser-use 打开 http://mock-platform:5190/，使用合成账号 demo、密码 pogo-demo-2026 登录。登录后直接使用 api_read 读取 /api/orders，汇报订单总数和金额合计。不要用页面数字代替接口响应，也不要创建网站访问令牌或返回任何凭据。
```

正确结果是5笔订单、合计19,000。可打开 `/?scenario=bearer`，并明确要求“使用当前已选的令牌登录方式，不切换标准登录”来验证Bearer；打开 `/?scenario=mfa` 时保留二次验证方式，遇到验证码请求用户接管。网站测试控制台可以模拟过期和撤销；下一次读取应停止说明网站登录问题。

“引用元素”小手仍可把点击的网页元素加入当前输入框；保留已有草稿和附件，不触发网页点击或自动发送。沉浸模式同样保留引用。

## 自动读取如何工作

- `auth_status` 观察当前站点是否存在 Cookie、可用的 Bearer 候选键名。`credentials_present` 只表示凭据候选存在，不能证明网站已接受登录；访客也可能有跟踪 Cookie。
- `api_read` 只需要相对当前页面 origin 的 `path`。没有事先填接口、字段或请求Pogo授权的步骤，也没有 `credential_ref`。
- 当前页 localStorage 中有一个可识别的访问令牌时，自动使用 Bearer，并携带该请求 URL 的 Cookie；无 Bearer 候选则使用 Cookie。自动发现排除CSRF/XSRF、refresh、ID token。多个候选时模型用 `auth_status` 获取键名，再通过可选 `storage_key` 选择；用户无需填权限表单。
- 默认返回全部经过脱敏的 JSON，包括对象或数组。可选 `fields` 仅用于筛选顶层输出字段，不是权限配置。模型拿不到Cookie、Bearer值或响应Header。
- 网站登录态自然更新后，新读取自动使用新凭据；网站自身的登录有效期与账号权限仍由网站决定。

## 错误与人工接管

没有可用登录态返回 `AUTH_LOGIN_REQUIRED`；网站401返回 `AUTH_LOGIN_EXPIRED`；网站403返回 `AUTH_ACCESS_DENIED`。Pogo不自动重试同一凭据被拒的接口，也不自动重新登录。用户完成网站登录后，新的凭据会自动生效。

用户接管、取消、关闭浏览器会中止在途读取；请求期间Cookie/token、当前页或origin变化时拒绝返回旧身份的数据。登录数据不进入操作结果缓存，相同op_id不会重放请求或返回旧数据。

真实MFA、验证码仍由用户按网站要求处理。交还控制只恢复浏览器控制权，当前仍需要用户发送继续任务的消息；本次移除额外授权没有同时实现一键恢复任务。

## 实现边界

此能力是站点登录态复用。原始token导出、任意脚本执行、模型自定义headers/body、跨站凭据传递没有开放。`api_read` 当前为一次同站点GET，JSON最多64KiB，超时8秒，不跟随重定向，不导入响应Set-Cookie或自动refresh token。需要自定义CSRF头、sessionStorage、嵌套JSON令牌存储、特殊客户端证书或跨origin API的真实系统仍需适配。

这些是具体执行能力边界，不是用户可切换的额外授权模式；正常浏览、点击、填写和页面读取仍按浏览器工具工作。

## 验证与运行

```sh
cd pogo/browser-runtime
npm run auth:test
npm run auth:eval:test
npm run auth:eval:validate
node eval/auth-auto/run.js --mode validate --control noop

# 从pogo构建独立新adapter，不覆盖历史二进制。
cd ..
go build -o /tmp/pogo-browser-eval-agent-auth-auto-v2 ./cmd/browser-eval-agent
cd browser-runtime
node eval/auth-auto/run.js --mode model --agent /tmp/pogo-browser-eval-agent-auth-auto-v2 \
  --providers /absolute/private/providers.json --model glm-5.3 --repeat 3
```

新评测见 [eval/auth-auto/README.md](../browser-runtime/eval/auth-auto/README.md)，8项任务分为已有登录态、模型实际登录后读取、正确停止。成功必须有真实api_read的200响应、模型答案及服务端计数三方一致；reference应8/8，noop即使猜对固定答案也应0/8。

旧手动授权V1任务和历史成绩保持独立，其运行入口已退役；离线诊断仍可使用。没有把旧24/24改称新自动模式成绩。

候选构建/启动与真实UI和连续模型对话验收见 [lab/README.md](../browser-runtime/lab/README.md)。最新镜像和本次最终验证结果见根目录 `HANDOFF.md` 第15节。日常19777与候选19778是两个独立部署，查看该节确认实际更新范围。
