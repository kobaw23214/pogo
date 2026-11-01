package web

// Markdown export: the sidebar's per-session "export conversation" action.
//
// The JSON export next door (sessionio.go) is a backup format — records
// verbatim, for importing back. This is the reading copy: the same live
// branch a replay would show, laid out as a document — every message with
// its blocks (prose, thinking, tool calls and results, attachments), each
// turn's stats, and the session's envelope (model, workspace, totals) up
// top. Server-side rather than in the browser so the document is identical
// no matter which client asks for it, and so it can walk the record tree
// (the fork rules) rather than a UI-shaped snapshot.
//
// Faithfulness choices, so "all information" stays honest:
//   - the branch exported is the current one (forks that were rewound away
//     are not conversations anyone had; their cost still counts, below);
//   - text blocks render as the markdown they already are, while thinking
//     and tool payloads are fenced — a pasted document reads as a document,
//     a tool dump never eats the sections around it;
//   - token totals come from UsageTotals, which deliberately sums every
//     record in the file, abandoned branches included: the provider billed
//     those tokens whichever branch is live now.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/session"
)

// handleSessionExportMarkdown streams one session as a Markdown document.
// Same shape of envelope lookup as the JSON export — the listing's merged
// title/model/cwd, not a second implementation of the merge rules.
func (s *Server) handleSessionExportMarkdown(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("sid")
	if !validID(id) || !s.ownsSession(r, id) {
		writeErr(w, http.StatusNotFound, ErrNotFound)
		return
	}
	store, err := session.Open(s.mgr.transcriptPath(id))
	if err != nil {
		writeErr(w, http.StatusNotFound, ErrNotFound)
		return
	}

	var title, model, cwd string
	pinned := false
	if list, err := s.mgr.List(); err == nil {
		for _, info := range list {
			if info.ID == id {
				title, model, cwd, pinned = info.Title, info.Model, info.Cwd, info.Pinned
				break
			}
		}
	}

	md := renderSessionMarkdown(store, id, title, model, cwd, pinned)
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Disposition",
		fmt.Sprintf("attachment; filename=%q", exportFilename(id, title, ".md")))
	_, _ = w.Write([]byte(md))
}

// renderSessionMarkdown lays out the whole live branch as one document:
// envelope, then the conversation in order, then totals.
func renderSessionMarkdown(store *session.Store, id, title, model, cwd string, pinned bool) string {
	records := store.BranchRecords("")

	// tool_use id → name, so a result block (which carries only the id) can be
	// labelled. The map is built from the whole branch before anything renders:
	// a result always follows its call in file order, but "follows" is not a
	// guarantee this renderer should depend on for its labels.
	toolNames := map[string]string{}
	for _, rec := range records {
		if rec.Message == nil {
			continue
		}
		for _, b := range rec.Message.Content {
			if b.Type == llm.BlockToolUse && b.ID != "" {
				toolNames[b.ID] = b.Name
			}
		}
	}

	var conv strings.Builder
	var created, last int64
	var msgCount, toolCalls int
	var composition *session.Composition
	for _, rec := range records {
		if rec.Time > last {
			last = rec.Time
		}
		switch rec.Type {
		case "meta":
			if created == 0 {
				created = rec.Time
			}
		case "stats":
			if rec.Stats == nil {
				continue
			}
			if rec.Stats.Composition != nil {
				composition = rec.Stats.Composition
			}
			conv.WriteString(renderStatsLine(rec.Stats))
		case "message":
			if rec.Message == nil {
				continue
			}
			if created == 0 {
				created = rec.Time
			}
			msgCount++
			for _, blk := range rec.Message.Content {
				if blk.Type == llm.BlockToolUse {
					toolCalls++
				}
			}
			renderMessage(&conv, rec, toolNames)
		}
	}

	var b strings.Builder
	name := strings.TrimSpace(title)
	if name == "" {
		name = "(未命名会话)"
	}
	b.WriteString("# " + name + "\n\n")
	var rows []string
	addRow := func(k, v string) { rows = append(rows, "| "+k+" | "+v+" |") }
	addRow("会话 ID", id)
	if model != "" {
		addRow("模型", model)
	}
	if cwd != "" {
		addRow("工作目录", cwd)
	}
	if created > 0 {
		addRow("创建时间", fmtTime(created))
	}
	if last > 0 {
		addRow("最后活动", fmtTime(last))
	}
	if pinned {
		addRow("置顶", "是")
	}
	if meta := store.Meta(); meta != nil && len(meta.Skills) > 0 {
		addRow("技能", strings.Join(meta.Skills, ", "))
	}
	addRow("导出时间", time.Now().Format("2006-01-02 15:04:05"))
	b.WriteString("| 项目 | 值 |\n|---|---|\n")
	b.WriteString(strings.Join(rows, "\n"))
	b.WriteString("\n\n---\n\n")
	b.WriteString(conv.String())

	// Totals last, so the document leads with the conversation itself.
	b.WriteString("---\n\n## 会话统计\n\n")
	usage, delegated := store.UsageTotals()
	b.WriteString("- 消息数：" + strconv.Itoa(msgCount) + "（工具调用 " + strconv.Itoa(toolCalls) + " 次）\n")
	b.WriteString("- Token（直接）：输入 " + commize(usage.Input) +
		" · 输出 " + commize(usage.Output))
	if usage.CacheRead > 0 {
		b.WriteString(" · 缓存读取 " + commize(usage.CacheRead))
	}
	if usage.Reasoning > 0 {
		b.WriteString(" · 推理 " + commize(usage.Reasoning))
	}
	b.WriteString("\n")
	if delegated.Input > 0 || delegated.Output > 0 {
		b.WriteString("- Token（子代理）：输入 " + commize(delegated.Input) +
			" · 输出 " + commize(delegated.Output) + "\n")
	}
	if composition != nil {
		b.WriteString("- 上下文构成（最后一次快照，估算 tok）：系统与工具 " + commize(composition.Fixed) +
			" · 用户 " + commize(composition.User) +
			" · 助手 " + commize(composition.Assistant) +
			" · 工具参数 " + commize(composition.ToolArgs))
		if len(composition.Tools) > 0 {
			names := make([]string, 0, len(composition.Tools))
			for n := range composition.Tools {
				names = append(names, n)
			}
			sort.Strings(names)
			parts := make([]string, 0, len(names))
			for _, n := range names {
				parts = append(parts, n+" "+commize(composition.Tools[n]))
			}
			b.WriteString(" · 工具结果（" + strings.Join(parts, " / ") + "）")
		}
		b.WriteString(" · 合计 ≈" + commize(composition.Estimated))
		if composition.Measured > 0 {
			b.WriteString("（实测 " + commize(composition.Measured) + "）")
		}
		b.WriteString("\n")
	}

	return b.String()
}

// renderMessage writes one message record. A heading is emitted when the
// message carries conversation-facing content; a user message that is only
// tool results is the loop's bookkeeping, not a turn anyone spoke, so it
// renders as results under the call that asked for them.
func renderMessage(b *strings.Builder, rec session.Record, toolNames map[string]string) {
	msg := rec.Message
	hasProse := false
	for _, blk := range msg.Content {
		switch blk.Type {
		case llm.BlockText:
			if strings.TrimSpace(blk.Text) != "" {
				hasProse = true
			}
		case llm.BlockThinking, llm.BlockToolUse, llm.BlockImage, llm.BlockFile:
			hasProse = true
		}
	}
	if !hasProse {
		renderResults(b, *msg, toolNames)
		return
	}
	who := "🤖 助手"
	if msg.Role == llm.RoleUser {
		who = "👤 用户"
	}
	b.WriteString("## " + who + " · " + fmtShortTime(rec.Time) + "\n\n")
	bullet := false
	for _, blk := range msg.Content {
		switch blk.Type {
		case llm.BlockText:
			if t := strings.TrimSpace(blk.Text); t != "" {
				b.WriteString(t + "\n\n")
			}
			bullet = false
		case llm.BlockThinking:
			secs := ""
			if blk.DurationMS > 0 {
				secs = "（" + fmtMS(blk.DurationMS) + "）"
			}
			b.WriteString("<details><summary>💭 思考过程" + secs + "</summary>\n\n")
			b.WriteString(fence(blk.Text, ""))
			b.WriteString("</details>\n\n")
			bullet = false
		case llm.BlockToolUse:
			b.WriteString("**🔧 工具调用 `" + blk.Name + "`**\n\n")
			if len(blk.Input) > 0 {
				b.WriteString(fence(prettyJSON(blk.Input), "json"))
			}
			bullet = false
		case llm.BlockImage:
			dims := ""
			if blk.ImageW > 0 && blk.ImageH > 0 {
				dims = strconv.Itoa(blk.ImageW) + "×" + strconv.Itoa(blk.ImageH) + " · "
			}
			line := "- 🖼️ 图片 `" + blk.ImageRef + "`（" + dims + blk.ImageMime
			if blk.ImageName != "" {
				line += " · " + blk.ImageName
			}
			line += "）"
			if alt := strings.TrimSpace(blk.ImageAlt); alt != "" {
				line += "：" + alt
			}
			b.WriteString(line + "\n")
			bullet = true
		case llm.BlockFile:
			line := "- 📎 文件 `" + blk.FilePath + "`"
			if blk.FileSize > 0 {
				line += "（" + humanBytes(blk.FileSize)
				if blk.FileKind != "" {
					line += " · " + blk.FileKind
				}
				line += "）"
			}
			b.WriteString(line + "\n")
			bullet = true
		case llm.BlockToolResult:
			renderResultBlock(b, blk, toolNames)
			bullet = false
		}
	}
	// The blocks above all end with a blank line except attachment bullets,
	// which end a list; one blank line closes the list so the next section
	// starts as a section.
	if bullet {
		b.WriteString("\n")
	}
}

// renderResults writes a message that is nothing but tool results.
func renderResults(b *strings.Builder, msg llm.Message, toolNames map[string]string) {
	for _, blk := range msg.Content {
		if blk.Type == llm.BlockToolResult {
			renderResultBlock(b, blk, toolNames)
		}
	}
}

func renderResultBlock(b *strings.Builder, blk llm.Block, toolNames map[string]string) {
	name := toolNames[blk.ToolUseID]
	if name == "" {
		name = "(unknown)"
	}
	status := "成功"
	if blk.IsError {
		status = "出错"
	}
	dur := ""
	if blk.DurationMS > 0 {
		dur = " · " + fmtMS(blk.DurationMS)
	}
	b.WriteString("**`" + name + "` 结果** · " + status + dur + "\n\n")
	if strings.TrimSpace(blk.Text) != "" {
		b.WriteString(fence(blk.Text, ""))
	}
}

// renderStatsLine formats one turn's stats as the compact quote that follows
// the turn it describes. Empty for a turn with nothing to say.
func renderStatsLine(st *session.Stats) string {
	var parts []string
	if u := st.Usage; u != nil {
		in := "输入 " + commize(u.Input) + " tok"
		if u.CacheRead > 0 {
			in += "（缓存 " + commize(u.CacheRead) + "）"
		}
		parts = append(parts, in, "输出 "+commize(u.Output)+" tok")
		if u.Reasoning > 0 {
			parts = append(parts, "推理 "+commize(u.Reasoning))
		}
	}
	if d := st.Delegated; d != nil && (d.Input > 0 || d.Output > 0) {
		parts = append(parts, "子代理 输入 "+commize(d.Input)+" / 输出 "+commize(d.Output))
	}
	if t := st.Tools; t != nil && t.CallCount > 0 {
		s := "工具 " + strconv.Itoa(t.CallCount) + " 次"
		if t.Duration > 0 {
			s += " · " + fmtMS(t.Duration)
		}
		parts = append(parts, s)
	}
	if rt := st.Retry; rt != nil && rt.Count > 0 {
		parts = append(parts, "重试 "+strconv.Itoa(rt.Count))
	}
	if a := st.Approval; a != nil && (a.WaitTime > 0 || a.AskCount > 0) {
		s := "审批等待 " + fmtMS(a.WaitTime)
		if a.AskCount > 0 {
			s += "（" + strconv.Itoa(a.AskCount) + " 次询问）"
		}
		parts = append(parts, s)
	}
	if len(parts) == 0 {
		return ""
	}
	return "> 📊 本轮：" + strings.Join(parts, " · ") + "\n\n"
}

// fence wraps content in a backtick fence long enough that nothing inside it
// can close the fence early — a tool dump that itself contains ``` is the
// normal case, not an attack.
func fence(content, info string) string {
	content = strings.TrimRight(content, "\n")
	run := 3
	for strings.Contains(content, strings.Repeat("`", run)) {
		run++
	}
	open := strings.Repeat("`", run)
	if info != "" {
		open += info
	}
	return open + "\n" + content + "\n" + strings.Repeat("`", run) + "\n\n"
}

// prettyJSON indents a tool call's input for reading; the raw bytes if they
// do not re-indent (they came off disk, so this is damage control, not an
// expected path).
func prettyJSON(raw json.RawMessage) string {
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return string(raw)
	}
	return buf.String()
}

// fmtMS renders a millisecond duration at human scale.
func fmtMS(ms int64) string {
	switch {
	case ms < 1000:
		return strconv.FormatInt(ms, 10) + "ms"
	case ms < 60_000:
		return fmt.Sprintf("%.1fs", float64(ms)/1000)
	default:
		return fmt.Sprintf("%dm%02ds", ms/60_000, (ms%60_000)/1000)
	}
}

func commize(n int64) string {
	s := strconv.FormatInt(n, 10)
	if len(s) <= 3 {
		return s
	}
	var parts []string
	for len(s) > 3 {
		parts = append([]string{s[len(s)-3:]}, parts...)
		s = s[:len(s)-3]
	}
	return s + "," + strings.Join(parts, ",")
}

func fmtTime(ms int64) string { return time.UnixMilli(ms).Format("2006-01-02 15:04:05") }

func fmtShortTime(ms int64) string { return time.UnixMilli(ms).Format("01-02 15:04:05") }
