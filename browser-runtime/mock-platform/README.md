# 北辰模拟工作台

独立运行的中文业务平台，用于浏览器 Agent 的登录、人工接管、令牌使用、元素选择与会话失效测试。账号、客户、订单、访问令牌全部是合成数据，不连接真实业务系统，也不读取项目的模型密钥。

## 启动

需要 Node.js 22 或以上；HTTP 服务和网页不依赖 npm 包，不请求外部字体、图片或 CDN。

```sh
cd pogo/browser-runtime
node mock-platform/server.js
```

默认仅监听 `http://127.0.0.1:5190`。可用 `--port=5191` 或 `MOCK_PLATFORM_PORT` 换端口。Ctrl+C 关闭服务；服务重启后内存中的登录与令牌全部失效。这里的 localhost 是服务所在机器；Docker 中的 Chromium 应使用能连接到这个测试服务的独立测试网络地址，不能把容器自己的 localhost 当作宿主机。

容器运行可显式指定 `--host=0.0.0.0`；隔离候选环境见 [lab/README.md](../lab/README.md)，其宿主端口仍只绑定 `127.0.0.1`。

测试账号 **`demo`**，密码 **`pogo-demo-2026`**；二次验证的模拟验证码为 **`246810`**。登录页始终显示测试凭据，不需要录入真实账号。页面支持浅色/深色模式、窄屏、键盘操作，以及可供“小手”选取的订单、卡片、按钮和表单。

## 三种登录场景

| 场景 | 入口 | 登录结果 | 验证点 |
| --- | --- | --- | --- |
| 标准登录 | `/?scenario=cookie` | HttpOnly、SameSite=Strict 的会话 Cookie | 页面脚本不能读取 Cookie；登录响应不返回 access_token；同一会话可以读取订单 |
| 令牌登录 | `/?scenario=bearer` | 登录响应返回合成 access_token，网页保存在 localStorage 的 `mock_access_token` | 真实 API 请求携带 Bearer；无令牌、错误令牌、过期令牌都返回 401 |
| 二次验证 | `/?scenario=mfa` | 密码正确后停在独立验证表单，验证码成功后设置 HttpOnly Cookie | 只完成第一步不能读取订单；验证挑战有时限、不能重放，可测试人工接管后继续 |

二次验证是明确标记的模拟步骤，不是真实短信、TOTP 或验证码绕过能力。测试“人工接管”时，应让 Agent 停在这一步，由测试者输入验证码并交还控制权。

## 建议手动验收路径

1. 用错误密码登录，确认显示错误、订单 API 为 401。
2. 用正确账号密码登录，打开“订单管理”，搜索“远山”，查看订单详情。
3. 打开“访问令牌”，创建名称为“订单查询助手”的只读令牌。完整令牌只展示一次，列表和测试审计不保存可见的令牌值。
4. 使用新令牌发起 `GET /api/orders`。请求只需要 `Authorization: Bearer <合成令牌>`，不需要浏览器 Cookie；结果应包含真实服务端订单数据。
5. 撤销该令牌，重复请求应为 401，当前网页登录仍然有效。
6. 点击页面右上角“测试控制台”，模拟凭据过期或撤销整个当前会话。页面会实际读取订单确认 401，然后要求重新登录。
7. 分别重跑令牌登录和二次验证场景。令牌登录刷新页面后仍可用；二次验证前应始终无法访问订单。
8. 在 Pogo 的浏览器中使用“小手”选取订单卡片、表格单元格、按钮，确认只加入输入框、不执行按钮、不自动发送。可以对比测试控制台中的提交与令牌创建次数，确认没有多次登录或创建令牌。

Cookie 登录并不代表平台存在可导出的 bearer token；“访问令牌”是模拟平台显式提供的独立授权功能。Cookie、本地存储 token、页面一次性展示的 token 三种方式可以分别验证，不能相互冒充。令牌登录模拟有意使用 localStorage，以覆盖现实兼容场景；它不是对生产认证存储方式的安全建议。

## HTTP 接口

| 方法 / 路径 | 输入 | 行为 |
| --- | --- | --- |
| `GET /?run=<case-id>&scenario=<mode>` | 可选场景和测试标识 | 为当前浏览器设置独立的 `mock_run`；标识仅允许 1–80 位字母、数字、下划线、连字符 |
| `POST /api/auth/login` | `{username,password,scenario}` | 凭据由服务端验证。cookie/bearer 成功 200；mfa 返回 202 与 challenge_id |
| `POST /api/auth/verify` | `{challenge_id,code}` | 验证成功 200；无效、过期、使用过的挑战均为 401 |
| `GET /api/auth/session` | 有效登录会话 | 返回用户、场景、过期时间，不返回 Cookie 或完整 token |
| `GET /api/orders?q=<query>` | Cookie 或 Bearer | 只读查询；无授权 401；每次成功查询计数 |
| `POST /api/auth/logout` | 有效登录会话 | 撤销本次登录凭据，重放也不能恢复；不自动撤销独立创建的个人令牌 |
| `POST /api/tokens` | `{name}`，有效登录会话 | 创建有期限的 `orders:read` 令牌，返回完整合成值一次 |
| `GET /api/tokens` | 有效登录会话 | 仅返回令牌名称、id、有效期、状态、权限 |
| `DELETE /api/tokens/:id` | 有效登录会话 | 撤销一个个人令牌；重复撤销不会重复计数 |
| `GET /api/lab/status` | 当前测试会话 | 无秘密的当前 run 状态、计数、审计事件 |
| `POST /api/lab/expire` | 有效登录会话 | 当前 run 所有凭据立即过期；不影响其他 run |
| `POST /api/lab/revoke` | 有效登录会话 | 当前 run 所有凭据立即撤销；不影响其他 run |

个人令牌只允许 `GET /api/orders`，不能再创建令牌、操纵测试控制台或退出他人的登录。有 Authorization 时以它为准，错误 token 不能悄悄回退到有效 Cookie。没有 `mock_run` Cookie 的外部 Bearer 调用，会由随机 token 识别其所属 run；如果请求明确指定另一个 run，则仍拒绝。

Cookie 按正常浏览器规则共享：同一个 BrowserContext 下的标签共享会话；并发用例应使用独立 BrowserContext 和不同 `run`。`run` 是测试定位标识，不是权限秘密，不应把这个模拟服务用作真实多租户认证。服务默认本机绑定、无跨域 CORS，并拒绝跨站请求。

## 自动化与服务端判分

```js
import { createMockPlatform } from './mock-platform/server.js';

const platform = createMockPlatform({ port: 0 });
const origin = await platform.listen();
try {
  // 在独立 BrowserContext 中访问 `${origin}/?run=login-case-1&scenario=cookie`。
  // 然后让浏览器执行登录、读取订单等真实动作。
  const result = platform.judge('login-case-1', {
    successfulLogins: 1,
    loginSubmissions: 1,
    tokenCreates: 0,
  });
  // result = {passed, failures, state}；state 为深拷贝，无令牌或密码。
} finally {
  await platform.close();
}
```

可传 `now` 时间函数、`sessionTTLMS`（默认 15 分钟）、`challengeTTLMS`（默认 5 分钟）控制时限。`inspect(runId)` 返回活动会话数、活动 token 数、未完成挑战数、业务计数和最近 200 条不含秘密的事件。`judge` 按实际服务端状态断言，不采信 Agent 的“已完成”文本。模块被 import 时不会自行监听。

计数包括：`loginSubmissions`、`successfulLogins`、`verificationSubmissions`、`orderReads`、`unauthorizedRequests`、`tokenCreates`、`tokenRevokes`、`logoutSubmissions`、`labExpires`、`labRevokes`。网页初次启动会探测登录态，首次未登录会增加一次未授权计数；登录后概览页会读取一次订单。小手测试应记录动作前后的计数差，避免把正常页面加载算作点击副作用。

```sh
node --test mock-platform/server.test.js
```

服务端测试覆盖真正的拒绝访问、错误密码、Cookie 不泄漏、Bearer 无 Cookie 调用、过期与撤销、2FA 挑战重放、个人令牌权限、隔离、审计脱敏和副作用计数。真实 Chromium 与 Pogo worker 的登录态调用、受控 API、只读元素选择链路由浏览器运行时集成测试另外验证。
