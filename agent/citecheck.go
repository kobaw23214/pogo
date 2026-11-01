package agent

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

// citeAnchorRe matches one citation anchor, `[1](#cite:S3)`. The bracketed
// number is meaningless by contract — the label after #cite: is the join key.
// The terminator accepts ) or ] because a model that gets the wrapper wrong
// still wrote a label worth checking.
var citeAnchorRe = regexp.MustCompile(`\[\s*(\d+)\s*\]\(#cite:([^\s()\[\]]+)[)\]]`)

// citeAnchorWideRe matches a #cite: link whatever the bracketed display is.
// citeAnchorRe only sees numeric displays, so a made-up label like
// [p1](#cite:p1) — or a good label wearing a non-numeric display — would pass
// it unseen; this pass holds every #cite: link to the same contract.
var citeAnchorWideRe = regexp.MustCompile(`\[([^\]]*)\]\(#cite:([^\s()\[\]]+)[)\]]`)

// citeDisplayRe is the contract for the bracketed half of an anchor: digits,
// nothing else. The number itself is meaningless, but the shape is not.
var citeDisplayRe = regexp.MustCompile(`^\d+$`)

// pageMarkerRe matches a bare page marker, [p3] or [p4-6]. The trailing group
// eats an attached (#cite:...) anchor so [p1](#cite:S3) — a real anchor with a
// bad display, which the wide pass judges — is not also reported as bare.
var pageMarkerRe = regexp.MustCompile(`(?i)(\[p\d+(?:\s*[-–—]\s*\d+)?\])(\(#cite:[^\s()\[\]]+[)\]])?`)

// inlineCodeRe is one `…` span on a single line.
var inlineCodeRe = regexp.MustCompile("`[^`\n]*`")

// ValidateCitations returns the distinct labels the answer cites that are not
// in the registry. Code is stripped first — fenced blocks and inline spans —
// because an anchor inside a code sample is the model *showing* the format,
// which the section itself does, and must not be read as citing.
func ValidateCitations(text string, store *SourceStore) []string {
	stripped := citationCheckedText(text)
	var invalid []string
	flagged := make(map[string]bool)
	flag := func(label string) {
		if !flagged[label] {
			flagged[label] = true
			invalid = append(invalid, label)
		}
	}
	seen := make(map[string]bool)
	for _, m := range citeAnchorRe.FindAllStringSubmatch(stripped, -1) {
		label := strings.ToUpper(strings.TrimSpace(m[2]))
		if seen[label] {
			continue
		}
		seen[label] = true
		if _, valid := sourceLabelNumber(label); !valid {
			flag(label)
			continue
		}
		if _, ok := store.Get(label); !ok {
			flag(label)
		}
	}
	// Wide pass: anchors whose display is not numeric never matched above, so
	// re-read every #cite: link. A valid label under a bad display is still
	// flagged — the steer sends the answer back, which is the fix either way.
	for _, m := range citeAnchorWideRe.FindAllStringSubmatch(stripped, -1) {
		label := strings.ToUpper(strings.TrimSpace(m[2]))
		_, valid := sourceLabelNumber(label)
		if !strings.HasSuffix(m[0], ")") || !citeDisplayRe.MatchString(strings.TrimSpace(m[1])) || !valid {
			flag(label)
			continue
		}
		if _, ok := store.Get(label); !ok {
			flag(label)
		}
	}
	return invalid
}

// FindPageMarkers returns the distinct bare page markers ([p1], [p3-5]) in the
// answer, in order of first appearance. Code is stripped first, and a marker
// that fronts a real anchor — [p1](#cite:S3) — is the wide pass's problem,
// not a bare marker.
func FindPageMarkers(text string) []string {
	var markers []string
	seen := make(map[string]bool)
	for _, m := range pageMarkerRe.FindAllStringSubmatch(citationCheckedText(text), -1) {
		if m[2] != "" || seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		markers = append(markers, m[1])
	}
	return markers
}

// stripCodeSpans drops fenced code blocks line by line and inline code spans
// from what remains.
func stripCodeSpans(text string) string {
	var b strings.Builder
	var fence byte
	fenceLength := 0
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		if len(trimmed) > 0 && (trimmed[0] == '`' || trimmed[0] == '~') {
			marker := trimmed[0]
			n := len(trimmed) - len(strings.TrimLeft(trimmed, string(marker)))
			if n >= 3 && fence == 0 {
				fence, fenceLength = marker, n
				continue
			}
			if marker == fence && n >= fenceLength && strings.TrimSpace(trimmed[n:]) == "" {
				fence, fenceLength = 0, 0
				continue
			}
		}
		if fence != 0 {
			continue
		}
		b.WriteString(inlineCodeRe.ReplaceAllString(line, ""))
		b.WriteByte('\n')
	}
	return b.String()
}

// citeSteerNotice is the one retry the loop spends on a final answer that cites
// sources this session never produced. Shaped like the task-list closing
// reminder (todofinish.go): a user-role message at a legal boundary, marked
// [pogo] so it is never mistaken for something the user typed.
func citeSteerNotice(invalid []string) string {
	return "[pogo] citation check: your previous answer has unknown source labels or malformed citation anchors: " +
		strings.Join(invalid, ", ") + ". " +
		"Only cite labels from the [cite:...] headers of actual tool results, using [1](#cite:Sx) with a closing parenthesis. " +
		"If a result has no header, it has no label: describe its recorded status or evidence gap without inventing a replacement anchor. " +
		"Rewrite the complete requested answer using only labels whose actual results support " +
		"the claims; remove unsupported claims or state the evidence gap. Preserve valid " +
		"findings and limitations. Do not return only a correction acknowledgement."
}

// pageMarkerSteerNotice is the page-marker half of the one citation retry:
// bare [p1]-style markers are dead text a reader cannot follow, so the answer
// goes back once to have the pages woven into prose next to real anchors.
func pageMarkerSteerNotice(markers []string) string {
	return "[pogo] citation check: your previous answer contains bare page marker(s) " +
		strings.Join(markers, ", ") + " which a reader cannot follow. " +
		"Write page numbers as prose (e.g. \"page 3\") and attach the [1](#cite:Sx) anchor " +
		"of the tool result that page came from. Return the complete requested answer with " +
		"its supported findings and limitations, not only a correction acknowledgement."
}

// citationReadLabels selects successful, nonempty evidence reads from one
// completed batch. It does not classify every citable result: commands can also
// supply evidence, but an operation receipt alone must not
// force citations into a completion acknowledgement. Unknown tool semantics
// therefore do not opt a run into the missing-citation check.
func (a *Agent) citationReadLabels(calls, results []llm.Block) []string {
	if a.sources == nil {
		return nil
	}
	byID := make(map[string]llm.Block, len(calls))
	for _, call := range calls {
		byID[call.ID] = call
	}
	var labels []string
	for _, result := range results {
		call, ok := byID[result.ToolUseID]
		if !ok || result.IsError || strings.TrimSpace(StripCiteHeader(result.Text)) == "" {
			continue
		}
		var saved Source
		if json.Unmarshal(result.Source, &saved) != nil {
			continue
		}
		source, ok := a.sources.Get(saved.Label)
		if !ok || source.CallID != saved.CallID || !strings.HasPrefix(result.Text, "[cite:"+source.Label+" | ") {
			continue
		}
		read, recovered := a.recoveredResultRead(call, result, source)
		if saved.CallID != result.ToolUseID && !recovered {
			continue
		}
		eligible := false
		if recovered {
			// A directory or omission receipt is not a new body read. Preserve
			// the original label/time while counting actual recovered material.
			material := read.View == "text" && read.Total > read.Offset || read.View == "json" && !read.Omitted
			if material {
				switch source.Type {
				case SourceFileRead:
					eligible = strings.HasPrefix(source.Locator, "file://") && !a.citationInstructionPath(source.Title)
				case SourceSearch, SourceInformation:
					eligible = true
				case SourceWebPage:
					page, err := url.Parse(source.Locator)
					eligible = err == nil && page.Host != "" && (page.Scheme == "https" || page.Scheme == "http")
				case SourceMCP:
					original, _ := a.registry.Get(read.OriginalTool)
					if mcp, ok := original.(*tools.MCPTool); ok {
						eligible = mcp.IsReadOnly() && mcp.CatalogRole() != tools.MCPCatalogRoleDirectory
					}
				}
			}
			if eligible {
				labels = append(labels, source.Label)
			}
			continue
		}
		if a.citationDerivedRead(call, result, source) {
			labels = append(labels, source.Label)
			continue
		}
		switch source.Type {
		case SourceFileRead:
			eligible = a.citationFileRead(result.Details)
		case SourceSearch:
			var detail struct {
				Entries int `json:"entries"`
				Matches int `json:"matches"`
			}
			eligible = json.Unmarshal(result.Details, &detail) == nil && (detail.Entries > 0 || detail.Matches > 0)
		case SourceWebPage:
			var detail struct {
				OK                 bool `json:"ok"`
				HasReadableContent bool `json:"has_readable_content"`
			}
			page, err := url.Parse(source.Locator)
			// about:blank, error pages and status-only receipts are not material
			// reads. This is an automatic-review gate, not a ban on citing them.
			if call.Name == "browser" && err == nil && page.Host != "" && (page.Scheme == "https" || page.Scheme == "http") &&
				json.Unmarshal(result.Details, &detail) == nil && detail.OK && detail.HasReadableContent {
				// Navigation and actions can include a real post-action snapshot.
				// Classify the observed content, not just the requested operation.
				eligible = true
			}
		case SourceMCP:
			// Use the captured registry declaration, never a result's claim that
			// it is read-only. A directory describes tools rather than task data.
			tool, _ := a.registry.Get(call.Name)
			if mcp, ok := tool.(*tools.MCPTool); ok {
				eligible = mcp.IsReadOnly() && mcp.CatalogRole() != tools.MCPCatalogRoleDirectory
			}
		}
		if eligible {
			labels = append(labels, source.Label)
		}
	}
	return labels
}

// A bootstrap instruction file enables a tool; merely reading it must not make
// "open this page" an evidence-based report. Only known runtime locations are
// excluded, not arbitrary files called SKILL.md in the user's project.
func (a *Agent) citationFileRead(raw json.RawMessage) bool {
	var single tools.ReadDetails
	if json.Unmarshal(raw, &single) != nil {
		return false
	}
	if single.Path != "" {
		return !a.citationInstructionPath(single.Path)
	}
	var many tools.ReadManyDetails
	if json.Unmarshal(raw, &many) == nil {
		for _, file := range many.Files {
			if file.Path != "" && file.Error == "" && file.BodyLength > 0 && !a.citationInstructionPath(file.Path) {
				return true
			}
		}
	}
	return false
}

func (a *Agent) citationInstructionPath(path string) bool {
	if filepath.Base(path) != "SKILL.md" {
		return false
	}
	if strings.HasPrefix(path, "skill-runtime://") {
		return true
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(a.sources.cwd, path)
	}
	path = filepath.Clean(path)
	roots := []string{"/opt/skills", filepath.Join(os.TempDir(), "pogo-embedded-skills")}
	if cache, err := os.UserCacheDir(); err == nil && cache != "" {
		roots = append(roots, filepath.Join(cache, "pogo", "embedded-skills"))
	}
	if configured := os.Getenv("POGO_EMBEDDED_SKILLS_DIR"); configured != "" {
		roots = append(roots, configured)
	}
	for _, root := range roots {
		root = filepath.Clean(root)
		if path == root || strings.HasPrefix(path, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

// missingCitationNotice is deliberately a zero-citation check, not a semantic
// coverage score. The model must decide which claims the actual results support;
// the harness must never attach sources to arbitrary paragraphs on its behalf.
func missingCitationNotice(answer string, labels []string, store *SourceStore) string {
	structured := parseCitationJSONObject(answer) != nil
	if len(labels) == 0 || !structured && (!citationReviewableProse(answer) || citationOpeningAcknowledgement(answer, labels, store)) {
		return ""
	}
	if hasRegisteredCitation(citationCheckedText(answer), store) {
		return ""
	}
	if structured {
		return "[pogo] citation check: this task successfully read source material, but the declared JSON sources array contains no registered citation anchors."
	}
	// Keep a large read task's reminder small. These are examples of available
	// labels, not a whitelist: older reachable results remain legal citations.
	if len(labels) > 24 {
		labels = labels[len(labels)-24:]
	}
	return "[pogo] citation check: this task successfully read source material (" +
		strings.Join(labels, ", ") + "), but the answer has no inline source citations. " +
		"Review the complete answer against the existing results. For factual claims drawn from those results, " +
		"place [1](#cite:Sx) immediately after the supported sentence, short paragraph, list item or table row. " +
		"The Sources button is only an index and does not replace inline attribution. " +
		"Do not attach every source to every paragraph or invent a claim-to-source connection. " +
		"Recommendations, acknowledgements and statements of an evidence gap need no forced citation; " +
		"if the answer contains only these, preserve it. Narrow unsupported claims and retain material " +
		"failures or limitations. Return the complete requested answer, not a correction acknowledgement."
}

func citationReviewableProse(answer string) bool {
	text := strings.TrimSpace(stripCodeSpans(answer))
	if text == "" {
		return false
	}
	// Machine-readable object/array responses have no prose locations for
	// Markdown anchors. Do not break a strict structured-output contract merely
	// because tools supplied the returned values.
	if json.Valid([]byte(text)) {
		return false
	}
	// Exempt explicit acknowledgements rather than using a length threshold:
	// short Chinese sentences can still make concrete factual claims.
	switch strings.ToLower(strings.Trim(text, " \t\r\n.!。！")) {
	case "ok", "okay", "done", "thanks", "thank you", "understood", "got it", "opened",
		"好", "好的", "可以", "收到", "明白", "了解", "谢谢", "已完成", "完成了", "已打开", "打开了":
		return false
	}
	// A lone clarifying question need not cite the source of its wording.
	if !strings.ContainsAny(text, "\n。.!！;；") && (strings.HasSuffix(text, "?") || strings.HasSuffix(text, "？")) {
		return false
	}
	return true
}

const citationRewriteOnlyNotice = " Use only the existing conversation and results. This is one text-only rewrite: do not call tools, repeat actions, or fetch more evidence."

// A repair acknowledgement is not the requested answer. This narrow list does
// not reject a substantive uncited rewrite: the model may legitimately explain
// an evidence gap rather than inventing an attribution. Presence is not truth.
func citationRewriteAcknowledgement(answer string) bool {
	text := citeAnchorWideRe.ReplaceAllString(strings.TrimSpace(stripCodeSpans(answer)), "")
	text = strings.ToLower(strings.Trim(text, " \t\r\n.!。！"))
	switch text {
	case "ok", "okay", "done", "understood", "got it", "好的", "收到", "已完成", "完成了",
		"已补充引用", "已修正引用", "引用已补充", "引用已修正", "已添加引用", "已更正", "已修正",
		"已按来源重新整理，请参考", "已根据来源重新整理，请参考", "已按要求补充引用", "已添加来源引用",
		"citations added", "citations corrected", "citations updated", "i have added the citations", "i've added the citations":
		return true
	}
	return false
}

var citationWebLinkRe = regexp.MustCompile(`\[([^\]\n]+)\]\((https?://[^\s)]+)\)`)
var citationOpeningRe = regexp.MustCompile(`(?i)^(?:已打开|已经打开|opened|i have opened|i've opened)[ \t:：]*(.+)$`)

// Only a single opening confirmation whose target matches an observed page is
// exempt. A broad "已打开" prefix would also hide the factual report that can
// follow it, so reject additional clauses and unrelated target descriptions.
func citationOpeningAcknowledgement(answer string, labels []string, store *SourceStore) bool {
	text := strings.TrimSpace(stripCodeSpans(answer))
	text = citationWebLinkRe.ReplaceAllString(text, "$1")
	text = strings.TrimRight(text, "。.!！ ")
	match := citationOpeningRe.FindStringSubmatch(text)
	if match == nil {
		return false
	}
	target := strings.Trim(strings.ToLower(match[1]), " \t*\"'“”「」")
	if target == "" || strings.ContainsAny(target, "\n，,。!！?？;；") {
		return false
	}
	switch target {
	case "页面", "网页", "该页面", "该网页", "page", "the page", "website", "the website":
		return true
	}
	for _, label := range labels {
		source, ok := store.Get(label)
		if !ok || source.Type != SourceWebPage {
			continue
		}
		// The URL itself may contain dots; other prose containing a period is
		// not a single target. The source identity comes from a real tool result.
		if target == strings.ToLower(source.Locator) {
			return true
		}
		page, err := url.Parse(source.Locator)
		if err == nil && (target == strings.ToLower(page.Hostname()) || target == strings.ToLower(page.Host)) {
			return true
		}
		if strings.Contains(target, ".") {
			continue
		}
		title := strings.ToLower(source.Title)
		if target == title || (len([]rune(target)) >= 2 && strings.Contains(title, target)) {
			return true
		}
	}
	return false
}
