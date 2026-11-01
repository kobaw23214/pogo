package web

// Markdown import: the return trip of the reading copy (sessionmd.go).
//
// The document was written for humans, so the parse is deliberately
// best-effort — prose turns, thinking, and tool call/result pairs come back
// as real records; attachments and stats do not (the bytes and the counters
// never made it into the document). What the parser cannot recognise it keeps
// as prose rather than dropping: an import that reads a little oddly beats
// one that silently loses a turn.
//
// The format assumptions are the exporter's own, paired with renderMessage
// line by line: a turn heading carries the emoji prefix and short timestamp
// (any other "## …" line stays prose, which is what keeps an assistant reply
// that itself contains Markdown headings intact), tool payloads live in the
// variable-length fences fence() wrote, and a result pairs with the earliest
// still-unpaired call above it — the order the agent loop produced them in.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/session"
)

// mdTurn is one parsed conversation turn: a message plus when it happened.
type mdTurn struct {
	msg  llm.Message
	time int64
}

// handleSessionImportMarkdown is the sidebar's "import into this project"
// action: the document a previous "导出会话" produced comes back as one NEW
// session, written next to the originals — the file it came from is never
// touched. workspace (when the client sends one) names the folder the import
// was started from, resolved exactly like session creation's.
func (s *Server) handleSessionImportMarkdown(w http.ResponseWriter, r *http.Request, doc string, workspace *string) {
	title, model, cwd, turns, err := parseSessionMarkdown(doc)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if len(turns) == 0 {
		writeErr(w, http.StatusBadRequest, errors.New("not a session export: no conversation turns found"))
		return
	}
	if len(turns) > importMaxRecords {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("more than %d messages in one document", importMaxRecords))
		return
	}
	// The folder the import started from wins over the document's own 工作目录
	// row — "import into this project" must land in that project's group. No
	// workspace at all keeps the document's cwd; if neither names one, the
	// caller's base root is the fallback every session can honestly claim
	// (the owner's root in multi-user mode, the server root otherwise).
	base := s.mgr.cfg.Cwd
	var owner *identity.Scope
	if scope, ok := s.ownerScope(r); ok {
		owner = &scope
		base = s.mgr.UserRoot(scope)
	}
	if workspace != nil {
		abs, err := s.mgr.resolveWorkspaceUnder(base, *workspace)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		cwd = abs
	}
	if cwd == "" || !insideRoot(base, cwd) {
		cwd = base
	}

	records := make([]session.Record, 0, len(turns)+1)
	t0 := turns[0].time
	if t0 == 0 {
		t0 = time.Now().UnixMilli()
	}
	meta := &session.Meta{Cwd: cwd, Model: model}
	if title != "" {
		meta.Title = &title
	}
	records = append(records, session.Record{ID: "md-rec-000000", Type: "meta", Time: t0, Meta: meta})
	prev := records[0].ID
	for k := range turns {
		id := fmt.Sprintf("md-rec-%06d", k+1)
		when := turns[k].time
		if when == 0 {
			when = t0
		}
		records = append(records, session.Record{
			ID: id, ParentID: prev, Type: "message", Time: when, Message: &turns[k].msg,
		})
		prev = id
	}

	store, err := session.WriteImported(s.mgr.sessionDir(), records)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// The imported session lands in the importer's space, like every other
	// session they create.
	if owner != nil && !s.mgr.claimImported(*owner, sessionIDOf(store), title) {
		writeErr(w, http.StatusInternalServerError, errors.New("ownership could not be recorded"))
		return
	}
	if s.opts.Logger != nil {
		s.opts.Logger.Printf("imported session from markdown (%d turns)", len(turns))
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"imported": 1,
		"session_ids": []map[string]string{
			{"id": sessionIDOf(store), "title": title},
		},
		"skipped": []string{},
	})
}

// parseSessionMarkdown reads an exported Markdown document back into its
// title, the envelope fields it carried, and the conversation's turns in
// order.
func parseSessionMarkdown(doc string) (title, model, cwd string, turns []mdTurn, err error) {
	lines := strings.Split(strings.ReplaceAll(doc, "\r\n", "\n"), "\n")
	i := 0

	// The H1 is the title; "(未命名会话)" is the exporter's placeholder for
	// none, not a name anyone chose.
	for i < len(lines) {
		l := strings.TrimSpace(lines[i])
		i++
		if l == "" {
			continue
		}
		if !strings.HasPrefix(l, "# ") {
			return "", "", "", nil, fmt.Errorf("not a session export: expected the title heading, got %q", l)
		}
		title = strings.TrimSpace(strings.TrimPrefix(l, "# "))
		if title == "(未命名会话)" {
			title = ""
		}
		break
	}
	if i >= len(lines) && title == "" {
		return "", "", "", nil, errors.New("not a session export: the document is empty")
	}

	// The envelope table up to the first turn heading. The 项目/值 header is
	// the document's provenance marker — without it this is a random Markdown
	// file, not something export.md wrote.
	hasTable := false
	for i < len(lines) && !strings.HasPrefix(lines[i], "## ") {
		cells := mdTableRow(lines[i])
		if len(cells) == 2 {
			switch cells[0] {
			case "项目":
				hasTable = true
			case "模型":
				model = cells[1]
			case "工作目录":
				cwd = cells[1]
			}
		}
		i++
	}
	if !hasTable {
		return "", "", "", nil, errors.New("not a session export: the info table is missing")
	}

	toolSeq := 0
	for i < len(lines) {
		role, when, ok := mdTurnHeading(lines[i])
		if !ok {
			if strings.HasPrefix(lines[i], "## ") {
				break // "## 会话统计" — the conversation is over, totals stay behind
			}
			i++
			continue
		}
		i++
		start := i
		for i < len(lines) && !mdSectionBoundary(lines[i]) {
			i++
		}
		turns = append(turns, mdSectionTurns(role, when, lines[start:i], &toolSeq)...)
	}
	return title, model, cwd, turns, nil
}

// mdTableRow splits a Markdown table row into its cells; nil for any other
// line. Values containing "|" would split — nothing the exporter writes can.
func mdTableRow(l string) []string {
	if !strings.HasPrefix(l, "|") {
		return nil
	}
	var cells []string
	for _, p := range strings.Split(l, "|") {
		if p = strings.TrimSpace(p); p != "" {
			cells = append(cells, p)
		}
	}
	return cells
}

var mdHeadingRe = regexp.MustCompile(`^## (👤 用户|🤖 助手) · (\d{2}-\d{2} \d{2}:\d{2}:\d{2})$`)

// mdTurnHeading recognises a turn heading; any other "## …" line is prose
// that happens to start a line, not a boundary.
func mdTurnHeading(l string) (llm.Role, int64, bool) {
	m := mdHeadingRe.FindStringSubmatch(l)
	if m == nil {
		return "", 0, false
	}
	role := llm.RoleAssistant
	if m[1] == "👤 用户" {
		role = llm.RoleUser
	}
	return role, mdShortTime(m[2]), true
}

// mdSectionBoundary ends a section body: the next turn, or the totals block.
func mdSectionBoundary(l string) bool {
	if !strings.HasPrefix(l, "## ") {
		return false
	}
	if _, _, ok := mdTurnHeading(l); ok {
		return true
	}
	return strings.TrimSpace(strings.TrimPrefix(l, "## ")) == "会话统计"
}

// mdShortTime reads the heading's "01-02 15:04:05" — the exporter drops the
// year, so assume the current one and step back a year when that lands the
// timestamp in the future. Zero when even that fails; the caller's chain
// order still holds.
func mdShortTime(s string) int64 {
	t, err := time.ParseInLocation("01-02 15:04:05", s, time.Local)
	if err != nil {
		return 0
	}
	now := time.Now()
	t = time.Date(now.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), 0, time.Local)
	if t.After(now.Add(time.Minute)) {
		t = t.AddDate(-1, 0, 0)
	}
	return t.UnixMilli()
}

// mdItem is one recognised piece of a section body, in order.
type mdItem struct {
	kind    string // "text" | "thinking" | "call" | "result"
	text    string
	name    string // call/result: the tool name the label carried
	input   json.RawMessage
	isError bool
}

var (
	mdCallRe   = regexp.MustCompile("^\\*\\*🔧 工具调用 `([^`]+)`\\*\\*$")
	mdResultRe = regexp.MustCompile("^\\*\\*`([^`]+)` 结果\\*\\* · (成功|出错)")
)

// mdSectionItems walks one section body, splitting prose from the marked
// blocks. Stats quotes and attachment bullets are recognised and dropped —
// the counters and bytes never made it into the document.
func mdSectionItems(body []string) []mdItem {
	var items []mdItem
	var text []string
	flushText := func() {
		t := strings.TrimSpace(strings.Join(text, "\n"))
		text = nil
		// The "---" that closes the conversation sits inside the last
		// section's body; it is the document's divider, not prose.
		t = strings.TrimSpace(strings.TrimSuffix(t, "\n---"))
		if t != "" && t != "---" {
			items = append(items, mdItem{kind: "text", text: t})
		}
	}

	for j := 0; j < len(body); j++ {
		l := body[j]
		switch {
		case strings.HasPrefix(l, "> 📊 本轮："):
			// Per-turn stats: nothing to rebuild a counters record from.
		case strings.HasPrefix(l, "- 🖼️ 图片 `"), strings.HasPrefix(l, "- 📎 文件 `"):
			// Attachments are references into the old session's workspace;
			// the bytes never left it, so the block cannot come back.
		case strings.HasPrefix(l, "<details><summary>💭 思考过程"):
			flushText()
			thinking, nj := mdDetails(body, j)
			if thinking != "" {
				items = append(items, mdItem{kind: "thinking", text: thinking})
			}
			j = nj
		case mdCallRe.MatchString(l):
			flushText()
			m := mdCallRe.FindStringSubmatch(l)
			payload, nj := mdFence(body, j+1)
			j = nj
			items = append(items, mdItem{kind: "call", name: m[1], input: json.RawMessage(payload)})
		case mdResultRe.MatchString(l):
			flushText()
			m := mdResultRe.FindStringSubmatch(l)
			content, nj := mdFence(body, j+1)
			j = nj
			items = append(items, mdItem{kind: "result", name: m[1], text: content, isError: m[2] == "出错"})
		default:
			text = append(text, l)
		}
	}
	flushText()
	return items
}

// mdDetails reads a collapsed thinking block: <details> to </details>, with
// the thinking text in the fence inside.
func mdDetails(body []string, j int) (string, int) {
	for k := j + 1; k < len(body); k++ {
		if strings.TrimSpace(body[k]) == "</details>" {
			text, _ := mdFence(body[:k], j+1)
			return text, k
		}
	}
	return "", j
}

// mdFence reads the variable-length backtick fence the exporter's fence()
// wrote, starting just after a marker line. No fence following is a hand
// edit, not a crime: the block then has no payload. A fence that never
// closes swallows the rest of the section — damage, kept rather than dropped.
func mdFence(body []string, j int) (string, int) {
	for j < len(body) && strings.TrimSpace(body[j]) == "" {
		j++
	}
	if j >= len(body) {
		return "", j - 1
	}
	n := leadingBackticks(body[j])
	if n < 3 {
		return "", j - 1
	}
	// fence() lengthened the run past any run of backticks inside, so the
	// first all-backtick line at least this long is the close.
	var content []string
	for k := j + 1; k < len(body); k++ {
		if c := leadingBackticks(body[k]); c >= n && c == len(body[k]) {
			return strings.Join(content, "\n"), k
		}
		content = append(content, body[k])
	}
	return strings.Join(content, "\n"), len(body) - 1
}

func leadingBackticks(l string) int {
	n := 0
	for n < len(l) && l[n] == '`' {
		n++
	}
	return n
}

// mdSectionTurns assembles one section's items into messages. The exporter
// renders a turn as text and tool calls under one heading, with the next
// user message's results — which get no heading — right after, so a section
// yields at most two messages: the speaker's, then the results answering its
// calls.
func mdSectionTurns(role llm.Role, when int64, body []string, toolSeq *int) []mdTurn {
	var turns []mdTurn
	var cur []llm.Block
	var results []llm.Block
	var calls []string // tool_use ids still awaiting their result, in call order

	flushSpeaker := func() {
		if len(cur) > 0 {
			turns = append(turns, mdTurn{msg: llm.Message{Role: role, Content: cur}, time: when})
			cur = nil
		}
	}
	flushResults := func() {
		if len(results) > 0 {
			turns = append(turns, mdTurn{msg: llm.Message{Role: llm.RoleUser, Content: results}, time: when})
			results = nil
		}
	}

	for _, it := range mdSectionItems(body) {
		switch it.kind {
		case "text", "thinking":
			flushResults()
			bt := llm.BlockText
			if it.kind == "thinking" {
				bt = llm.BlockThinking
			}
			cur = append(cur, llm.Block{Type: bt, Text: it.text})
		case "call":
			flushResults()
			*toolSeq++
			id := fmt.Sprintf("md-tool-%06d", *toolSeq)
			// A payload that no longer parses as JSON cannot ride the wire;
			// the call keeps its name and an empty object instead.
			input := it.input
			if len(input) > 0 && !json.Valid(input) {
				input = json.RawMessage(`{}`)
			}
			cur = append(cur, llm.Block{Type: llm.BlockToolUse, ID: id, Name: it.name, Input: input})
			calls = append(calls, id)
		case "result":
			flushSpeaker()
			if len(calls) == 0 {
				continue // a result with no call above it could never pair on the wire
			}
			id := calls[0]
			calls = calls[1:]
			results = append(results, llm.Block{Type: llm.BlockToolResult, ToolUseID: id, IsError: it.isError, Text: it.text})
		}
	}
	flushSpeaker()
	flushResults()
	return turns
}
