# 浏览器任务评测

评估真实模型在受控网页中完成任务的能力。入口是 `run.js`，完整协议、指标定义和限制见 [browser-evaluation.md](../../docs/browser-evaluation.md)。本目录不由生产服务提供，也不需要重启 Docker。

旧 `auth` 与 `auth-auto` 认证评测已经退役：CLI 与程序化入口均拒绝启动 worker 或模型。历史任务、判题器和报告继续支持离线分析；新通用能力评测使用 [general-web](general-web/README.md) 或 [browser-use-v2](browser-use-v2/README.md)。

## 运行

在 `pogo/browser-runtime` 安装已有锁文件对应的 Node 依赖与 Chromium，Go 命令从 `pogo` 构建：

```sh
npm ci
npx playwright install chromium
npm run eval:test
npm run eval:validate
npm run eval:validate -- --control noop
```

参考脚本应全部通过；`noop` 不做网页操作，只宣称完成，应该全部判失败。两者均不调用模型。`noop` 命令退出 0 表示判分器正确拒绝了这些假完成声明。

```sh
cd ..
go build -o /tmp/pogo-browser-eval-agent ./cmd/browser-eval-agent
cd browser-runtime
npm run eval:model -- --agent /tmp/pogo-browser-eval-agent --model glm-5.3 \
  --providers /absolute/path/to/providers.json
```

运行前由当前环境提供模型所配置的 `key_env`，例如 `ZHIPU_API_KEY`。使用项目已有的凭据注入方式；不要将密钥写入参数、报告或提交到 Git。运行器不会主动读取生产 Docker 的环境，也不会自动部署或修改生产会话。

结果默认写入 `eval/runs/<时间与随机后缀>/`，已从 Git 和 Docker 构建上下文排除。打开其中的 `report.html` 查看离线报告；`summary.json` 可供程序比较，`traces/` 是逐步记录，`frames/` 是实际浏览器截图。`--out` 可指定新目录；已有目录拒绝覆盖。

## 选项

```sh
node eval/run.js --list
npm run eval:model -- --agent /tmp/pogo-browser-eval-agent --model glm-5.3 \
  --tasks catalog-a,checkout-b --seed 20261001 --repeat 3 --concurrency 2
```

- 默认 20 个任务、固定种子 `20261001`、每项 1 次、并发 2。重复时业务数据不变，但上下文、URL 和业务状态独立。
- 默认每项 40 轮、100,000 input+output tokens、180 秒，另外任务定义限制 35 个请求步骤。模型 token 用量依据服务商回报；最后一个请求可能跨过阈值，但超额响应中的动作不会执行。
- `--timeout-ms` 可降低时限，任务定义的 180 秒仍为上限。取消收尾允许额外 20 秒；硬停止会回收该评测拥有的进程组。
- `--total-tokens` / `--total-time-ms` 是**停止启动新任务的阈值**，默认不设。不撤销已运行任务；并发任务仍可使用各自剩余额度。不是硬总账单上限。
- `--no-frames` 可关闭截图归档。截图默认逐次工具操作后采集，耗时包含在任务耗时内。
- 任一模型任务失败、超时或未运行，退出码为 1；配置或启动错误为 2。已经生成的报告保留。
- Ctrl+C / SIGTERM 会停止新任务，取消正在运行的适配器并保存中断报告；SIGKILL 无法执行程序清理钩子。

模型适配器的 JSONL 接口见 [Go CLI 说明](../../cmd/browser-eval-agent/README.md)。外部适配器必须是可信本地程序；进程 watchdog 不是运行任意第三方二进制的操作系统沙箱。

## 比较两轮结果

```sh
node eval/compare.js /absolute/before/summary.json /absolute/after/summary.json \
  --out /absolute/new-comparison.html --json /absolute/new-comparison.json
```

对比是纯离线计算，输入和历史报告保持不变；输出拒绝覆盖。除任务通过率外，还比较模型工具调用、中途失败、受影响任务、恢复成功、耗时与用量，并列出任务、模型、提示词、预算和运行时配置是否一致。源码/工具声明指纹变化单独显示。

模型调用从 `tool_start`/`tool_end` 事件统计，和浏览器 execute 次数、batch 子步骤分开。旧报告缺事件时显示未测量。每项只跑一次的前后对比不足以证明因果提速，应在更大任务集和多次重复下再确认稳定性。
