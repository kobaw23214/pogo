package agent

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/session"
	"github.com/yosukeno/pogo/tools"
)

// Compaction rehydration: state the host holds exactly is carried across a
// compaction instead of being left to the summary.
//
// A summary is the right container for what happened and why. It is the wrong
// one for four things the transcript already holds verbatim, and which a
// paraphrase degrades in ways that are invisible until they cost something:
//
//   - loaded Skill instructions, which are rules to follow word for word. A
//     summary that drops or rewords one rule changes the work; and for a
//     Skill-bound group (skillview.go) the Skill's evidence is also what keeps
//     its tools declared.
//   - the latest task list, which is the plan's exact state.
//   - which files were written or edited, which a summary lists only as well
//     as the summariser remembered them.
//   - the shell commands that ran, which a summary paraphrases: a test
//     command recalled with one path or flag wrong still runs, and answers a
//     different question.
//
// So the continuation carries them as the host has them: each restored Skill
// as its own explicit-invocation block (InputSkill, the same provenance a
// skill atom has), re-read through the registry's trusted Skill attribution
// or copied from the host-loaded atom; the task list, file paths and one-line
// commands with their exit status in a <restored_state> note after the
// summary. Claude Code's post-compaction re-injection of the plan, recent
// files and invoked skills is the same idea.
//
// Bounded, because the point is a smaller prompt: a Skill over
// rehydrateSkillBytes, or past rehydrateSkillTotalBytes together with newer
// ones, becomes a pointer to load it again rather than a truncated rule set,
// and CompactWithCommit drops restoration before refusing a compaction as
// not smaller. Anything still visible in the verbatim tail is not repeated.

const (
	// rehydrateSkillBytes bounds one restored Skill's text.
	rehydrateSkillBytes = 20 << 10
	// rehydrateSkillTotalBytes bounds all restored Skill text together.
	rehydrateSkillTotalBytes = 60 << 10
	// rehydrateMaxFiles bounds the modified-file list.
	rehydrateMaxFiles = 20
	// rehydrateRequestBytes bounds the person's later messages carried
	// verbatim, newest first. User text measured 0.4%–0.9% of the large
	// sessions in this project, so the bound is for pastes, not for talk.
	rehydrateRequestBytes = 24 << 10
	// rehydrateRequestMessageBytes bounds one carried message. A longer one —
	// a pasted log, say — keeps its start and its end; the summary has the rest.
	rehydrateRequestMessageBytes = 4 << 10
	// rehydrateMaxCommands bounds the shell-command list, and
	// rehydrateCommandBytes the commands worth listing: one-line commands a
	// model would re-run verbatim. A script is not one; it stays the
	// summary's to describe.
	rehydrateMaxCommands  = 12
	rehydrateCommandBytes = 300
)

// shellCommand is one bash call the head made, as the model wrote it.
type shellCommand struct {
	// status is what the paired result says: "exit N", "timed out",
	// "background", "failed" or "ran" when the result has no details.
	status, workdir, command string
}

func (c shellCommand) key() string { return c.workdir + "\x00" + c.command }

// rehydration is the state one compaction carries over.
type rehydration struct {
	todos    []tools.TodoItem
	files    []string
	skills   []restoredSkill
	pointers []restoredSkill
	// requests are the person's messages after the pinned opening request,
	// verbatim and oldest first; omitted counts the older ones the budget
	// dropped, across compactions.
	requests []string
	omitted  int
	// commands are the shell commands the head ran, newest first.
	commands []shellCommand
}

// restoredSkill is one Skill to restore (text set) or point to (location set).
type restoredSkill struct {
	name, location, text string
}

func (r rehydration) empty() bool {
	return r.noteEmpty() && !r.hasRequests()
}

// noteEmpty reports whether there is anything for the <restored_state> note.
func (r rehydration) noteEmpty() bool {
	return len(r.todos) == 0 && len(r.files) == 0 && len(r.commands) == 0 && len(r.skills) == 0 && len(r.pointers) == 0
}

func (r rehydration) hasRequests() bool { return len(r.requests) > 0 || r.omitted > 0 }

// withoutSkillText keeps the note and turns every restored Skill into a
// pointer: the fallback when the full restoration would not shrink the prompt.
func (r rehydration) withoutSkillText() rehydration {
	out := rehydration{todos: r.todos, files: r.files, commands: r.commands, requests: r.requests, omitted: r.omitted}
	for _, s := range append(append([]restoredSkill(nil), r.skills...), r.pointers...) {
		if s.location != "" {
			out.pointers = append(out.pointers, restoredSkill{name: s.name, location: s.location})
		}
	}
	return out
}

// withoutRequests drops the person's later messages, the next fallback: the
// summary still describes them, only no longer word for word.
func (r rehydration) withoutRequests() rehydration {
	r.requests, r.omitted = nil, 0
	return r
}

// skillLoad is one piece of history evidence that a Skill's instructions
// entered the conversation.
type skillLoad struct {
	name, location string
	// text is the verbatim host-loaded instruction block of a skill atom;
	// empty when the Skill is re-read from its registered document instead.
	text string
	doc  tools.SkillDocument
	// readable marks doc as valid for SkillInvocation.
	readable bool
}

// skillLoads lists the Skills msgs loaded, oldest first, one entry per Skill
// name holding its newest evidence. The evidence is the same that loads a
// Skill-bound group (skillview.go), generalized to every registered Skill:
// a skill atom, a /skill expansion of a registered document, or a successful
// read of one. Text that merely looks like an invocation proves nothing and
// is not restored.
func (a *Agent) skillLoads(msgs []llm.Message) []skillLoad {
	var loads []skillLoad
	record := func(l skillLoad) {
		for i, seen := range loads {
			if seen.name == l.name {
				loads = append(loads[:i], loads[i+1:]...)
				break
			}
		}
		loads = append(loads, l)
	}
	reads := map[string]llm.Block{}
	for _, m := range msgs {
		for _, b := range m.Content {
			switch b.Type {
			case llm.BlockToolUse:
				if b.Name == "read" && strings.Contains(string(b.Input), "SKILL.md") {
					reads[b.ID] = b
				}
			case llm.BlockToolResult:
				call, ok := reads[b.ToolUseID]
				if !ok || b.IsError || a.registry == nil {
					continue
				}
				for _, doc := range a.registry.SkillDocumentsLoadedByRead(call.Input, b.Details) {
					record(skillLoad{name: doc.Name, location: doc.Path, doc: doc, readable: true})
				}
			case llm.BlockText:
				if m.Role != llm.RoleUser {
					continue
				}
				name, location, isInvocation := parseSkillInvocation(strings.TrimSpace(b.Text))
				if b.InputSkill != "" {
					l := skillLoad{name: b.InputSkill, text: strings.TrimSpace(b.Text)}
					if isInvocation && name == b.InputSkill {
						l.location = location
					}
					if l.text != "" {
						record(l)
					}
					continue
				}
				if isInvocation && a.registry != nil {
					if doc, ok := a.registry.SkillDocumentAt(location); ok && doc.Name == name {
						record(skillLoad{name: name, location: doc.Path, doc: doc, readable: true})
					}
				}
			}
		}
	}
	return loads
}

// collectRehydration reads what the head holds and the tail does not. A head
// that starts with an earlier continuation also contributes that note's state
// where nothing newer replaced it, so repeated compactions keep it too: a task
// list nobody updated since, files modified and commands run before the first
// compaction, and Skills that were only pointed to. Restored Skill blocks need no such help;
// they are skill atoms, which skillLoads already reads.
func (a *Agent) collectRehydration(head, tail []llm.Message) rehydration {
	var r rehydration
	prev := previousRestoredState(head)
	if _, inTail := newestTodoList(tail); !inTail {
		if todos, ok := newestTodoList(head); ok {
			r.todos = todos
		} else {
			r.todos = prev.todos
		}
	}
	tailFiles := modifiedFileSet(tail)
	r.files = modifiedFiles(head, tailFiles)
	for _, path := range prev.files {
		if len(r.files) >= rehydrateMaxFiles {
			break
		}
		if !tailFiles[path] && !slices.Contains(r.files, path) {
			r.files = append(r.files, path)
		}
	}
	tailCommands := map[string]bool{}
	for _, c := range shellCommands(tail) {
		tailCommands[c.key()] = true
	}
	for _, c := range append(shellCommands(head), prev.commands...) {
		if len(r.commands) >= rehydrateMaxCommands {
			break
		}
		if !tailCommands[c.key()] && !slices.ContainsFunc(r.commands, func(seen shellCommand) bool { return seen.key() == c.key() }) {
			r.commands = append(r.commands, c)
		}
	}
	headLoads, tailLoads := a.skillLoads(head), a.skillLoads(tail)
	r.skills, r.pointers = a.restorableSkills(headLoads, tailLoads)
	known := map[string]bool{}
	for _, l := range append(headLoads, tailLoads...) {
		known[l.name] = true
	}
	var carried []restoredSkill
	for _, p := range prev.pointers {
		if !known[p.name] {
			known[p.name] = true
			carried = append(carried, p)
		}
	}
	r.pointers = append(carried, r.pointers...)
	r.requests, r.omitted = laterRequests(head, prev)
	return r
}

// toolProtocolNoticePrefix opens the correction the loop injects after a
// malformed text tool call (llm.ToolProtocolIssue.Error). Every other
// harness-injected message wears a session.IsHarnessNotice prefix.
const toolProtocolNoticePrefix = "tool protocol error ("

// laterRequests is what the person said after the pinned opening request, as
// they said it: the earlier continuation's carried messages, then every user
// message in the head that a person sent — typed text, steering, a skill atom
// as its marker — oldest first, bounded newest first. Harness notices and
// tool results are not the person's words. The tail is not read: it is kept
// verbatim anyway.
//
// This is constraint pinning in its plainest form. Only the opening request
// used to be pinned, so "do not touch vendor/" said halfway through a session
// existed after a compaction only if the summariser kept it — the loss the
// pinning study measured at 0% violations kept versus 30% lost.
func laterRequests(head []llm.Message, prev rehydration) ([]string, int) {
	requests, omitted := append([]string(nil), prev.requests...), prev.omitted
	start, pinned := 0, false
	if len(head) > 0 && isContinuation(head[0]) {
		// The opening request is the continuation's <original_request>, and
		// the continuation's own text is the summary's, not the person's.
		start, pinned = 1, true
	}
	for _, m := range head[start:] {
		if m.Role != llm.RoleUser {
			continue
		}
		words := strings.TrimSpace(userWords(m))
		if words == "" {
			continue
		}
		if !pinned {
			// The same message firstUserText pins.
			pinned = true
			continue
		}
		if session.IsHarnessNotice(words) || strings.HasPrefix(words, toolProtocolNoticePrefix) {
			continue
		}
		requests = append(requests, boundedRequest(words))
	}
	keep, total := len(requests), 0
	for i := len(requests) - 1; i >= 0; i-- {
		if total+len(requests[i]) > rehydrateRequestBytes {
			break
		}
		total += len(requests[i])
		keep = i
	}
	return requests[keep:], omitted + keep
}

// isContinuation reports whether m is the output of an earlier compaction,
// recognized like previousSummary does: by the fixed opening sentence.
func isContinuation(m llm.Message) bool {
	return m.Role == llm.RoleUser && strings.Contains(m.Text(), continuationOpening)
}

// boundedRequest keeps a long message's start and end, cut on rune
// boundaries, with the size of what was left out in between.
func boundedRequest(text string) string {
	if len(text) <= rehydrateRequestMessageBytes {
		return text
	}
	head := runePrefix(text, rehydrateRequestMessageBytes*3/4)
	tail := runeSuffix(text, rehydrateRequestMessageBytes/8)
	return head + fmt.Sprintf("\n[… %d bytes omitted …]\n", len(text)-len(head)-len(tail)) + tail
}

func runePrefix(s string, n int) string {
	for n > 0 && n < len(s) && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func runeSuffix(s string, n int) string {
	i := len(s) - n
	for i > 0 && i < len(s) && !utf8.RuneStart(s[i]) {
		i++
	}
	return s[i:]
}

// restorableSkills splits the head's Skills that the tail does not show into
// restored text and pointers, newest first against the budgets, and returns
// both oldest first.
func (a *Agent) restorableSkills(loads, tailLoads []skillLoad) (restored, pointers []restoredSkill) {
	inTail := map[string]bool{}
	for _, l := range tailLoads {
		inTail[l.name] = true
	}
	type decision struct {
		skill   restoredSkill
		restore bool
	}
	decisions := make([]decision, len(loads))
	total := 0
	for i := len(loads) - 1; i >= 0; i-- {
		l := loads[i]
		if inTail[l.name] {
			continue
		}
		text := l.text
		if text == "" && l.readable {
			if invocation, err := a.registry.SkillInvocation(l.doc); err == nil {
				text = strings.TrimSpace(invocation)
			}
		}
		d := decision{skill: restoredSkill{name: l.name, location: l.location}}
		if text != "" && len(text) <= rehydrateSkillBytes && total+len(text) <= rehydrateSkillTotalBytes {
			d.skill.text, d.restore = text, true
			total += len(text)
		}
		decisions[i] = d
	}
	for i, d := range decisions {
		switch {
		case inTail[loads[i].name]:
		case d.restore:
			restored = append(restored, d.skill)
		case d.skill.location != "":
			pointers = append(pointers, d.skill)
		}
	}
	return restored, pointers
}

// modifiedFiles lists the paths of successful write and edit calls, newest
// first, without the ones in skip, up to rehydrateMaxFiles.
func modifiedFiles(msgs []llm.Message, skip map[string]bool) []string {
	failed := failedCalls(msgs)
	seen := map[string]bool{}
	var out []string
	for i := len(msgs) - 1; i >= 0 && len(out) < rehydrateMaxFiles; i-- {
		if msgs[i].Role != llm.RoleAssistant {
			continue
		}
		content := msgs[i].Content
		for j := len(content) - 1; j >= 0 && len(out) < rehydrateMaxFiles; j-- {
			path, ok := modifiedPath(content[j], failed)
			if !ok || seen[path] || skip[path] {
				continue
			}
			seen[path] = true
			out = append(out, path)
		}
	}
	return out
}

// modifiedFileSet is every path msgs modified.
func modifiedFileSet(msgs []llm.Message) map[string]bool {
	failed := failedCalls(msgs)
	out := map[string]bool{}
	for _, m := range msgs {
		for _, b := range m.Content {
			if path, ok := modifiedPath(b, failed); ok {
				out[path] = true
			}
		}
	}
	return out
}

// modifiedPath is the file a successful write or edit call named.
func modifiedPath(b llm.Block, failed map[string]bool) (string, bool) {
	if b.Type != llm.BlockToolUse || (b.Name != "write" && b.Name != "edit") || failed[b.ID] {
		return "", false
	}
	var args struct {
		Path string `json:"path"`
	}
	if json.Unmarshal(b.Input, &args) != nil {
		return "", false
	}
	path := strings.TrimSpace(args.Path)
	return path, path != ""
}

func failedCalls(msgs []llm.Message) map[string]bool {
	failed := map[string]bool{}
	for _, m := range msgs {
		for _, b := range m.Content {
			if b.Type == llm.BlockToolResult && b.IsError {
				failed[b.ToolUseID] = true
			}
		}
	}
	return failed
}

// shellCommands lists the bash calls msgs made, newest first, each command
// and workdir once with its newest status, as the model wrote them. A call
// whose result is missing, a multi-line command and one over
// rehydrateCommandBytes are left out: the first has no outcome to report, the
// others are scripts, which nobody re-runs from a list.
func shellCommands(msgs []llm.Message) []shellCommand {
	results := map[string]llm.Block{}
	for _, m := range msgs {
		for _, b := range m.Content {
			if b.Type == llm.BlockToolResult {
				results[b.ToolUseID] = b
			}
		}
	}
	seen := map[string]bool{}
	var out []shellCommand
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role != llm.RoleAssistant {
			continue
		}
		content := msgs[i].Content
		for j := len(content) - 1; j >= 0; j-- {
			b := content[j]
			if b.Type != llm.BlockToolUse || b.Name != "bash" {
				continue
			}
			result, ok := results[b.ID]
			var args struct {
				Command string `json:"command"`
				Workdir string `json:"workdir"`
			}
			if !ok || json.Unmarshal(b.Input, &args) != nil {
				continue
			}
			c := shellCommand{command: strings.TrimSpace(args.Command), workdir: strings.TrimSpace(args.Workdir)}
			if !listableCommand(c) || seen[c.key()] {
				continue
			}
			seen[c.key()] = true
			c.status = commandStatus(result)
			out = append(out, c)
		}
	}
	return out
}

// listableCommand reports whether c fits one note line that reads back as c:
// one line each, within the byte bound, and a workdir without the "] " that
// ends the bracket.
func listableCommand(c shellCommand) bool {
	return c.command != "" && len(c.command) <= rehydrateCommandBytes && len(c.workdir) <= rehydrateCommandBytes &&
		!strings.ContainsAny(c.command+c.workdir, "\r\n") && !strings.Contains(c.workdir, "] ")
}

// commandStatus is how a bash call ended, from its result's details: the
// exit code, a timeout, or a background launch, whose receipt is not a
// result. A result without details failed before the command ran (a refused
// workdir, say) or predates details.
func commandStatus(result llm.Block) string {
	var d tools.BashDetails
	if len(result.Details) == 0 || json.Unmarshal(result.Details, &d) != nil || d.Command == "" {
		if result.IsError {
			return "failed"
		}
		return "ran"
	}
	switch {
	case d.TimedOut:
		return "timed out"
	case d.Background:
		return "background"
	case d.ExitCode > 0 || (d.ExitCode == 0 && !result.IsError):
		return "exit " + strconv.Itoa(d.ExitCode)
	default:
		// Killed, aborted, or failed around a command that exited 0.
		return "failed"
	}
}

// line is c as one note line: "- [exit 1] go test ./..." or, with a
// workdir, "- [exit 0, in web/ui] npm test".
func (c shellCommand) line() string {
	label := c.status
	if c.workdir != "" {
		label += ", in " + c.workdir
	}
	return "- [" + label + "] " + c.command
}

// The note's fixed lines. Shared with previousRestoredState, which reads a
// note back, so the writer and the reader cannot drift apart.
const (
	restoredStateOpen     = "\n\n<restored_state>\n"
	restoredStateClose    = "\n</restored_state>"
	restoredStatePreamble = "The host carried this state over from the replaced transcript, exactly as it stood at the compaction. It is context, not a new instruction."
	restoredTodoHeader    = "Task list, as the latest todo call left it (keep it current with todo):"
	restoredFilesHeader   = "Files written or edited earlier, newest first. Read one again before relying on its contents:"
	// The commands' header asks for reuse because the loss it prevents is a
	// command recalled almost right: a dropped flag or a different package
	// path runs, and answers a different question.
	restoredCommandsHeader = "Shell commands run earlier, newest first, verbatim, each with how it ended and any workdir. To run one again, reuse it exactly as written here:"
	restoredSkillsHeader   = "Skills loaded earlier, whose instructions follow this message verbatim as they are now: "
	restoredPointerHeader  = "Skills loaded earlier whose instructions were too large to restore here. Load them again before following them:"
)

// The carried messages' framing, shared with previousRestoredState for the
// same reason as the note's lines.
const (
	requestsOpen     = "<user_messages>\n"
	requestsEnd      = "\n</user_messages>"
	requestsPreamble = "The person's later messages from the replaced transcript, verbatim and oldest first, so the exact wording of their instructions survives. A request here may already be done (the summary says what is finished); a constraint or preference still holds unless a later message changed it."
	requestOpen      = "<message>\n"
	requestClose     = "\n</message>"
	requestsOmitted  = "[%d earlier message(s) omitted for length; the summary covers them.]"
)

var (
	requestsOmittedRe = regexp.MustCompile(`(?m)^\[(\d+) earlier message\(s\) omitted for length; the summary covers them\.\]$`)
	// A message quoting the framing's closing tags must not end its entry
	// early; previousRestoredState reverses the escape.
	requestEscaper   = strings.NewReplacer("</message>", `<\/message>`, "</user_messages>", `<\/user_messages>`)
	requestUnescaper = strings.NewReplacer(`<\/message>`, "</message>", `<\/user_messages>`, "</user_messages>")
)

// requestsBlock frames the person's later messages for continuationWith, or
// is "" when there are none.
func (r rehydration) requestsBlock() string {
	if !r.hasRequests() {
		return ""
	}
	var b strings.Builder
	b.WriteString(requestsOpen + requestsPreamble)
	if r.omitted > 0 {
		b.WriteString("\n\n" + fmt.Sprintf(requestsOmitted, r.omitted))
	}
	for _, text := range r.requests {
		b.WriteString("\n\n" + requestOpen + requestEscaper.Replace(text) + requestClose)
	}
	b.WriteString(requestsEnd + "\n\n")
	return b.String()
}

// previousRequests reads the carried messages back out of a continuation's
// text, from between the pinned request and the opening sentence.
func previousRequests(text string, opening int) (requests []string, omitted int) {
	region := text[:opening]
	if k := strings.Index(region, "\n</original_request>"); k >= 0 {
		region = region[k+len("\n</original_request>"):]
	}
	i := strings.Index(region, requestsOpen)
	if i < 0 {
		return nil, 0
	}
	body := region[i+len(requestsOpen):]
	if j := strings.Index(body, requestsEnd); j >= 0 {
		body = body[:j]
	}
	preamble := body
	if k := strings.Index(body, requestOpen); k >= 0 {
		preamble = body[:k]
	}
	if m := requestsOmittedRe.FindStringSubmatch(preamble); m != nil {
		omitted, _ = strconv.Atoi(m[1])
	}
	for rest := body; ; {
		k := strings.Index(rest, requestOpen)
		if k < 0 {
			break
		}
		rest = rest[k+len(requestOpen):]
		end := strings.Index(rest, requestClose)
		if end < 0 {
			break
		}
		requests = append(requests, requestUnescaper.Replace(rest[:end]))
		rest = rest[end+len(requestClose):]
	}
	return requests, omitted
}

// note is the <restored_state> block that follows the summary, or "".
func (r rehydration) note() string {
	if r.noteEmpty() {
		return ""
	}
	var b strings.Builder
	b.WriteString(restoredStateOpen + restoredStatePreamble)
	if len(r.todos) > 0 {
		b.WriteString("\n\n" + restoredTodoHeader + "\n")
		b.WriteString(tools.RenderTodos(r.todos))
	}
	if len(r.files) > 0 {
		b.WriteString("\n\n" + restoredFilesHeader)
		for _, path := range r.files {
			b.WriteString("\n- " + oneLine(path))
		}
	}
	if len(r.commands) > 0 {
		b.WriteString("\n\n" + restoredCommandsHeader)
		for _, c := range r.commands {
			b.WriteString("\n" + c.line())
		}
	}
	if len(r.skills) > 0 {
		names := make([]string, 0, len(r.skills))
		for _, s := range r.skills {
			names = append(names, s.name)
		}
		b.WriteString("\n\n" + restoredSkillsHeader + strings.Join(names, ", ") + ".")
	}
	if len(r.pointers) > 0 {
		b.WriteString("\n\n" + restoredPointerHeader)
		for _, s := range r.pointers {
			b.WriteString("\n- " + oneLine(s.name) + ": " + oneLine(s.location))
		}
	}
	b.WriteString(restoredStateClose)
	return b.String()
}

// todoLineRe reads one tools.RenderTodos line.
var todoLineRe = regexp.MustCompile(`^\d+\. \[([a-z_]+)\] (.+)$`)

// commandLineRe reads one shellCommand.line: the status (which never holds
// "," or "]"), the workdir when there was one, and the command.
var commandLineRe = regexp.MustCompile(`^- \[([^\],]+)(?:, in (.+?))?\] (.+)$`)

// previousRestoredState reads the note an earlier compaction left in a
// continuation, recognized the way previousSummary recognizes one: the fixed
// opening sentence first, then the note after the summary's end, so a
// <restored_state> tag inside the pinned request is never read as state.
func previousRestoredState(msgs []llm.Message) rehydration {
	var r rehydration
	if len(msgs) == 0 || msgs[0].Role != llm.RoleUser || len(msgs[0].Content) == 0 {
		return r
	}
	text := msgs[0].Content[0].Text
	opening := strings.Index(text, continuationOpening)
	if opening < 0 {
		return r
	}
	r.requests, r.omitted = previousRequests(text, opening)
	end := strings.Index(text[opening:], "\n</summary>")
	if end < 0 {
		return r
	}
	rest := text[opening+end:]
	i := strings.Index(rest, restoredStateOpen)
	if i < 0 {
		return r
	}
	body := rest[i+len(restoredStateOpen):]
	if j := strings.Index(body, restoredStateClose); j >= 0 {
		body = body[:j]
	}
	for _, section := range strings.Split(body, "\n\n") {
		header, lines, _ := strings.Cut(section, "\n")
		switch header {
		case restoredTodoHeader:
			for _, line := range strings.Split(lines, "\n") {
				if m := todoLineRe.FindStringSubmatch(line); m != nil && validTodoStatus(m[1]) {
					r.todos = append(r.todos, tools.TodoItem{Task: m[2], Status: m[1]})
				}
			}
		case restoredFilesHeader:
			for _, line := range strings.Split(lines, "\n") {
				if path, ok := strings.CutPrefix(line, "- "); ok && strings.TrimSpace(path) != "" {
					r.files = append(r.files, path)
				}
			}
		case restoredCommandsHeader:
			for _, line := range strings.Split(lines, "\n") {
				if m := commandLineRe.FindStringSubmatch(line); m != nil {
					r.commands = append(r.commands, shellCommand{status: m[1], workdir: m[2], command: m[3]})
				}
			}
		case restoredPointerHeader:
			for _, line := range strings.Split(lines, "\n") {
				entry, ok := strings.CutPrefix(line, "- ")
				name, location, found := strings.Cut(entry, ": ")
				if ok && found && name != "" && location != "" {
					r.pointers = append(r.pointers, restoredSkill{name: name, location: location})
				}
			}
		}
	}
	return r
}

func validTodoStatus(status string) bool {
	switch status {
	case tools.TodoPending, tools.TodoInProgress, tools.TodoCompleted, tools.TodoCancelled, tools.TodoBlocked:
		return true
	}
	return false
}

// skillBlocks are the restored Skills, each its own explicit-invocation block
// so interfaces show it as a Skill and skillview counts it as loaded. The
// separator leads because the wire joins a message's text blocks directly.
func (r rehydration) skillBlocks() []llm.Block {
	blocks := make([]llm.Block, 0, len(r.skills))
	for _, s := range r.skills {
		blocks = append(blocks, llm.Block{Type: llm.BlockText, Text: "\n\n" + s.text, InputSkill: s.name})
	}
	return blocks
}
