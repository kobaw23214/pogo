package main

import (
	"flag"
	"fmt"
	"io"
	"strings"
)

// flagOrder groups the help output logically instead of alphabetically. Any flag
// missing from this list still gets printed, so adding one cannot silently drop
// it from the help.
var flagOrder = []string{
	"p", "model", "models", "sessions", "C", "resume", "mode", "quiet",
	"max-turns", "soft-turns", "max-runs", "evaluate", "retries",
	"skills", "skill", "no-skills", "project-skills",
	"checkpoints-prune", "spill-prune", "spill-prune-days", "schedule",
	"web", "listen", "web-dev", "gate-timeout",
}

// hiddenFlags are registered but never printed: they exist for machinery to
// call back into pogo, not for people. -schedule-exec is how the OS
// scheduler runs a job; the user-facing half is -schedule.
var hiddenFlags = map[string]bool{
	"schedule-exec": true,
}

// usage prints bilingual help. Flag rows are generated from the actual flag set
// rather than hand-written, so the help cannot describe a flag that no longer
// exists.
func usage() {
	w := flag.CommandLine.Output()

	fmt.Fprint(w, `pogo — a minimal coding agent: one loop, seven tools, one protocol.
pogo — 极简 coding agent：一个循环、七个工具、一种协议。

USAGE / 用法
  pogo -web [flags]            serve the browser UI / 启动浏览器界面
  pogo [flags] -p "<prompt>"   run once, print the final answer, exit / 跑一次、打印最终回答后退出
  <stdin> | pogo [flags]       piped input becomes the prompt / 管道内容作为 prompt

  There is no interactive terminal mode; the browser UI is the interactive front end.
  不再有终端交互模式，交互使用请用浏览器界面。

FLAGS / 参数
`)
	printFlags(w)

	fmt.Fprint(w, `
ONE-SHOT COMMANDS / 一次性命令
  -p "/compact"      With -resume: replace the session's conversation with a summary.
                     配合 -resume：用摘要替换该会话的对话
  -p "/skill:<name> [args]"
                     Load a skill's full instructions into the prompt.
                     把某个 skill 的完整说明注入 prompt

ENVIRONMENT / 环境变量
  KIMI_API_KEY      Required for kimi models. / 使用 kimi 模型时必需
  ZHIPU_API_KEY     Required for zhipu models. / 使用 zhipu 模型时必需
  KIMI_BASE_URL     Override the kimi endpoint (proxy or mirror).
                    覆盖 kimi 端点（代理或镜像）
  ZHIPU_BASE_URL    Override the zhipu endpoint.
                    覆盖 zhipu 端点
  POGO_WEB_TOKEN    Pin the -web token instead of generating one per run.
                    固定 -web 的访问 token，否则每次启动随机生成
  POGO_SESSION_DIR  Where sessions are stored. Default ~/.pogo/sessions
                    会话存放目录，默认 ~/.pogo/sessions
  POGO_SCHEDULE_DIR Where scheduled jobs are stored. Default ~/.pogo/schedule
                    定时任务存放目录，默认 ~/.pogo/schedule
  POGO_PREFS_FILE   Where UI preferences live (spill retention days). Default ~/.pogo/prefs.json
                    界面设置存放文件（spill 保留天数），默认 ~/.pogo/prefs.json
  POGO_MCP_FILE     Where the MCP client config lives (external MCP servers pogo
                    connects to). Default ~/.pogo/mcp.json
                    MCP 客户端配置（pogo 要连的外部 MCP 服务器），默认 ~/.pogo/mcp.json
  POGO_SCHEDULE_BACKEND
                    Force the scheduler backend: launchd, systemd, cron or noop
                    (noop installs no OS trigger — development/tests only).
                    强制指定定时任务后端：launchd、systemd、cron 或 noop
                    （noop 不注册任何 OS 触发器，仅供开发/测试）

  Credentials are read from the environment only; no config file is consulted.
  密钥只从环境变量读取，不读任何配置文件。

EXAMPLES / 示例
  pogo -p "count the lines of Go code here"
  pogo -model glm-5.2 -p "这个项目的入口在哪"
  cat notes.md | pogo -p "extract the action items"
  git diff | pogo -p "review 这个 diff，只说有问题的地方"
  pogo -resume last -p "carry on from where we stopped"
  pogo -schedule add --cron "0 9 * * *" --prompt "summarize overnight CI failures"
  pogo -schedule list

  Scheduled jobs run headless with no approval gate — every tool call executes.
  定时任务以 headless 运行、没有审批门：所有工具调用都会直接执行。

  Piped input is placed before the -p instruction, so the ask stays last.
  管道内容置于 -p 指令之前，指令留在最后。
`)
}

// slashCommands are the words a -p prompt is screened against. /compact is the
// one that does something headless (`-resume <path> -p /compact`). The rest
// were commands of the removed interactive terminal mode; a prompt that is one
// of them is still refused rather than sent to the model as literal text, so a
// script that used to fail loudly does not quietly start getting answers.
// "/skill:" is listed bare because "/skill:<name>" is expanded, not screened.
var slashCommands = []string{
	"/model", "/models", "/usage", "/compact", "/skills", "/skill:",
	"/auto", "/strict", "/standard", "/help", "/exit", "/quit",
}

// commandWord reports the slash command a prompt is, or "".
//
// Only an exact leading word counts, so "/usr/local/bin is on PATH?" stays a prompt
// — the same rule the web layer applies for the same reason. No prefix expansion
// here on purpose: a scripted `-p` has no completion list to have shown the user
// what a guess would become.
func commandWord(prompt string) string {
	word, _, _ := strings.Cut(strings.TrimSpace(prompt), " ")
	for _, c := range slashCommands {
		if c == word {
			return word
		}
	}
	return ""
}

// printFlags renders one row per flag: English on the first line, Chinese on the
// second, with the default value appended when it is meaningful.
func printFlags(w io.Writer) {
	const indent = "  "
	const descCol = 22 // width of the name column, sized to the longest label

	seen := map[string]bool{}
	render := func(f *flag.Flag) {
		if f == nil || seen[f.Name] || hiddenFlags[f.Name] {
			return
		}
		seen[f.Name] = true

		argName, desc := flag.UnquoteUsage(f)
		label := "-" + f.Name
		if argName != "" {
			label += " <" + argName + ">"
		}

		en, zh, _ := strings.Cut(desc, "\n")
		if d := defaultOf(f); d != "" {
			en += " Default: " + d
		}

		pad := max(descCol-len(label), 1)
		fmt.Fprintf(w, "%s%s%s%s\n", indent, label, strings.Repeat(" ", pad), en)
		if zh != "" {
			fmt.Fprintf(w, "%s%s%s\n", indent, strings.Repeat(" ", descCol), zh)
		}
	}

	for _, name := range flagOrder {
		render(flag.Lookup(name))
	}
	// Anything not in flagOrder still shows up.
	flag.VisitAll(render)
}

// defaultOf returns a printable default, skipping the zero values where showing
// "false" or an empty string would be noise.
func defaultOf(f *flag.Flag) string {
	switch f.DefValue {
	case "", "false", "0":
		return ""
	}
	return f.DefValue
}
