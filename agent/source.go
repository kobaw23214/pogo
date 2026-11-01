package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

// Source is the registration record of one exploration artifact: a tool call's
// output. The label is assigned by the harness, never by the model — the prompt
// contract (CitationSection) allows the model to cite a label exactly as it
// appeared in a [cite:...] header and forbids inventing one, which is what makes
// a citation a join key into this registry rather than a claim.
type Source struct {
	Label   string `json:"label"` // "S12", unique and increasing across the session
	Type    string `json:"type"`  // file_read|file_write|command_exec|search_result|web_page|mcp_tool|tool|subagent
	Title   string `json:"title"` // short name: relative path, command digest, server/tool, page title
	Locator string `json:"locator"`
	CallID  string `json:"call_id"`
	// Excerpt is the first ~300 runes of the raw output, for hover cards and for
	// a later consistency check that never has to ask the model.
	Excerpt string `json:"excerpt"`
	// Hash is the first 12 hex chars of the full output's sha256: a snapshot
	// identity, so "the output changed since you cited it" is checkable.
	Hash string `json:"hash"`
	// FullPath names the spill file holding the whole output when it did not fit
	// (BashDetails.FullOutputPath and the same field on MCP/Synth/browser).
	FullPath string `json:"full_path,omitempty"`
	Time     int64  `json:"ts"`
	// Information identifies the exact host-read library version. It is an
	// internal reference, never a browser URL or another session's file grant.
	Information *tools.InformationCitation `json:"information,omitempty"`
	// SavedResult distinguishes local queries/directories from the underlying
	// artifact. Text/json recovery reuses the original stamp without restamping
	// its observation time or pretending that it contacted the source again.
	SavedResult *SavedResultCitation `json:"saved_result,omitempty"`
}

// The source types. A tool's Details shape decides its type, not its name, so a
// declared (synth) tool and an MCP tool land on the same footing as the
// built-ins that produce the same kind of artifact.
const (
	SourceFileRead    = "file_read"
	SourceFileWrite   = "file_write"
	SourceCommand     = "command_exec"
	SourceSearch      = "search_result"
	SourceWebPage     = "web_page"
	SourceMCP         = "mcp_tool"
	SourceTool        = "tool"
	SourceSubagent    = "subagent"
	SourceInformation = "information"
)

// SourceStore is the session's source registry. Next is called from tool
// goroutines — a parallel batch stamps its calls concurrently — so every method
// takes the mutex; the label sequence is the one thing about a batch that must
// stay race-free.
type SourceStore struct {
	mu      sync.Mutex
	cwd     string
	seq     int
	byLabel map[string]*Source
	all     []*Source
	// An unsupported label already used in an answer must never acquire a
	// different meaning when a later tool call reaches that sequence number.
	reserved      map[string]struct{}
	resultSources map[string]resultSourceBinding
}

func newSourceStore(cwd string) *SourceStore {
	return &SourceStore{cwd: cwd, byLabel: make(map[string]*Source), reserved: make(map[string]struct{})}
}

// Next registers one finished call and returns its stamp, or nil when the call
// is not evidence: the task list is process state, not an artifact, so a todo
// result is never citable (the prompt contract says so).
//
// args identifies browser metadata operations and the subagent's delegated
// brief, which lives in the arguments rather than the result details.
func (s *SourceStore) Next(toolName string, details any, callID, rawOutput string, args json.RawMessage) *Source {
	if toolName == todoToolName {
		return nil
	}
	if toolName == tools.ToolResultReadName {
		switch details.(type) {
		case tools.ResultCatalogDetails, tools.ResultRetentionDetails:
			return nil // host navigation/retention metadata is not source evidence
		}
		if read, ok := details.(tools.ToolResultReadDetails); ok {
			if source, handled := s.restoredResultSource(read); handled {
				return source
			}
		}
	}
	if toolName == tools.InformationToolName {
		if d, ok := details.(tools.InformationDetails); !ok || d.Information == nil {
			// Directory/search metadata is not evidence of a document's body.
			return nil
		}
	}
	// Saved navigation targets are not an observation of the target webpages.
	// Download receipts, however, can establish a host-observed state or failure.
	var browserAction string
	if toolName == "browser" {
		browserAction = tools.BrowserActionOf(args)
		if browserAction == tools.BrowserActionBookmarks {
			return nil
		}
	}
	src := &Source{
		Type:    SourceTool,
		Title:   toolName,
		Locator: fmt.Sprintf("tool://%s/%s", toolName, callID),
		CallID:  callID,
		Excerpt: clipRunes(rawOutput, 300),
		Time:    time.Now().UnixMilli(),
	}
	sum := sha256.Sum256([]byte(rawOutput))
	src.Hash = hex.EncodeToString(sum[:])[:12]

	switch d := details.(type) {
	case tools.ToolResultReadDetails:
		if toolName == tools.ToolResultReadName {
			src.SavedResult = s.savedResultCitation(d, args)
			src.Title = "已保存的工具结果 · " + d.View
			src.Locator = "tool://tool-result/" + d.ResultID
			if d.View == "query" {
				src.Title = "保存结果的本地查询与统计"
			}
			if src.SavedResult.Origin != nil {
				src.Title += " · " + src.SavedResult.Origin.Title
			}
		}
	case tools.ReadDetails:
		src.Type = SourceFileRead
		src.Title = s.rel(d.Path)
		src.Locator = fileLocator(src.Title, d.FirstLine, d.ShownLines)
	case tools.InformationDetails:
		src.Type = SourceInformation
		src.Title = d.Title
		if d.Information != nil {
			stamp := *d.Information
			src.Information = &stamp
			src.Locator = fmt.Sprintf("information://%s?version=%d#%s", url.PathEscape(stamp.EntryID), stamp.Version, stamp.SnapshotSHA256)
			var output struct {
				Content string `json:"content"`
			}
			if json.Unmarshal([]byte(rawOutput), &output) == nil {
				src.Excerpt = clipRunes(output.Content, 300)
			}
		}
	case tools.ReadManyDetails:
		src.Type = SourceFileRead
		src.Title = fmt.Sprintf("%d files", len(d.Files))
		if len(d.Files) == 1 {
			src.Title = s.rel(d.Files[0].Path)
			src.Locator = fileLocator(src.Title, 0, 0)
		}
	case tools.WriteDetails:
		src.Type = SourceFileWrite
		src.Title = s.rel(d.Path)
		src.Locator = "file://" + src.Title
	case tools.EditDetails:
		src.Type = SourceFileWrite
		src.Title = s.rel(d.Path)
		src.Locator = "file://" + src.Title
	case tools.BashDetails:
		src.Type = SourceCommand
		src.Title = clipRunes(oneLine(d.Command), 60)
		src.Locator = "cmd://" + clipRunes(oneLine(d.Command), 40)
		src.FullPath = d.FullOutputPath
	case tools.LsDetails:
		src.Type = SourceSearch
		src.Title = s.rel(d.Path)
		if src.Title == "" {
			src.Title = "."
		}
		src.Locator = "file://" + src.Title
	case tools.FindDetails:
		src.Type = SourceSearch
		src.Title = d.Pattern
		if d.Path != "" {
			src.Locator = "file://" + s.rel(d.Path)
		}
	case tools.GrepDetails:
		src.Type = SourceSearch
		src.Title = d.Pattern
		if d.Include != "" {
			src.Title += " (" + d.Include + ")"
		}
		if d.Path != "" {
			src.Locator = "file://" + s.rel(d.Path)
		}
	case map[string]any:
		// The browser tool's details are a map (see tools.browserDetails); url and
		// title are the two keys a page source is made of.
		url, _ := d["url"].(string)
		title, _ := d["title"].(string)
		src.Type = SourceWebPage
		src.Title = title
		src.FullPath, _ = d["full_output_path"].(string)
		if src.Title == "" {
			src.Title = url
		}
		if src.Title == "" {
			src.Title = toolName
		}
		if url != "" {
			src.Locator = url
		}
	case tools.MCPDetails:
		src.Type = SourceMCP
		src.Title = d.Server + "/" + d.Tool
		src.Locator = "mcp://" + d.Server + "/" + d.Tool
		src.FullPath = d.FullOutputPath
	case tools.SynthDetails:
		src.FullPath = d.FullOutputPath
	case tools.SubagentDetails:
		src.Type = SourceSubagent
		src.Title = subagentTitle(args, d)
	}
	if toolName == "browser" && browserAction == tools.BrowserActionDownloads {
		// A receipt can be cited for its recorded status, never as webpage or
		// file contents. Give even failed calls an identity tied to this call,
		// independent of the active page and of later download status changes.
		src.Type = SourceTool
		src.Title = "浏览器下载状态（主机记录）"
		src.Locator = "tool://browser-downloads/" + url.PathEscape(callID)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	for {
		// A restored high-water mark must never wrap and reuse an old label.
		if s.seq == int(^uint(0)>>1) {
			return nil
		}
		s.seq++
		src.Label = fmt.Sprintf("S%d", s.seq)
		if _, reserved := s.reserved[src.Label]; !reserved {
			break
		}
	}
	s.byLabel[src.Label] = src
	s.all = append(s.all, src)
	return src
}

// Get resolves a citation label. Case-insensitive: the model is held to the
// exact label by the prompt, but validation normalizes before asking, so a
// lowercased s3 still finds S3 rather than being flagged.
func (s *SourceStore) Get(label string) (*Source, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	src, ok := s.byLabel[strings.ToUpper(strings.TrimSpace(label))]
	return src, ok
}

// All is the registry in label order, for an interface that lists sources.
func (s *SourceStore) All() []*Source {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*Source(nil), s.all...)
}

// sourceLabelNumber recognizes the exact label shape Next emits. Real sources
// can pass S9999; neither recovery nor validation may impose a four-digit cap.
func sourceLabelNumber(label string) (int, bool) {
	if !strings.HasPrefix(label, "S") {
		return 0, false
	}
	n, err := strconv.Atoi(label[1:])
	return n, err == nil && n > 0 && label == fmt.Sprintf("S%d", n)
}

// reserveCitations prevents an unknown anchor from becoming accidentally true
// after a later call. Reservations are not sources: Get/All still expose only
// real results. It runs before executing this assistant message's tool calls.
func (s *SourceStore) reserveCitations(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reserveCitationsLocked(text)
}

func (s *SourceStore) reserveCitationsLocked(text string) {
	for _, match := range citeAnchorWideRe.FindAllStringSubmatch(stripCodeSpans(text), -1) {
		// A bare S84, a code example or a malformed link is not a live anchor.
		if !strings.HasSuffix(match[0], ")") {
			continue
		}
		label := strings.ToUpper(match[2])
		if _, ok := sourceLabelNumber(label); ok {
			s.reserved[label] = struct{}{}
		}
	}
}

// observe rebuilds the registry from the reachable history so resumed citations
// resolve and a rewind cannot cite an abandoned tool result. SetMessages is the
// funnel for resume, rewind and reload. The sequence only increases: abandoned
// labels leave gaps rather than later pointing to a different artifact.
func (s *SourceStore) observe(msgs []llm.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()

	byLabel := make(map[string]*Source)
	s.resultSources = make(map[string]resultSourceBinding)
	callNames := make(map[string]string)
	blockedLabels := make(map[string]bool)
	for _, m := range msgs {
		if len(m.SourceState) > 0 {
			// Unsupported/corrupt metadata cannot establish evidence. Its quoted
			// anchors still must not acquire a new meaning on a later tool call.
			s.reserveCitationsLocked(m.Text())
			s.observeSourceCheckpointLocked(m.SourceState, byLabel, blockedLabels)
		}
		if m.Role == llm.RoleAssistant {
			s.reserveCitationsLocked(m.Text())
		}
		for _, b := range m.Content {
			if b.Type == llm.BlockToolUse {
				callNames[b.ID] = b.Name
			}
			if b.Type != llm.BlockToolResult {
				continue
			}
			if len(b.Source) == 0 {
				s.bindResultLocked(b.ResultID, nil)
				s.restoreReadResultBindingLocked(callNames[b.ToolUseID], b, nil)
				continue
			}
			var src Source
			if err := json.Unmarshal(b.Source, &src); err != nil {
				continue
			}
			// Require the exact label shape emitted by Next. Sscanf also accepts
			// suffixes and signed numbers, which must not enter the registry.
			n, valid := sourceLabelNumber(src.Label)
			if !valid {
				continue
			}
			if n > s.seq {
				s.seq = n
			}
			if blockedLabels[src.Label] {
				s.ambiguousCheckpointBlockLocked(callNames[b.ToolUseID], b, &src)
				continue
			}
			// Replayed stamps can occur more than once. Preserve the first
			// reachable record rather than silently changing its identity.
			if existing, exists := byLabel[src.Label]; exists {
				if !sourceCheckpointSameIdentity(existing, &src) {
					s.invalidateCheckpointLabelLocked(src.Label, byLabel, blockedLabels)
					s.ambiguousCheckpointBlockLocked(callNames[b.ToolUseID], b, &src)
					continue
				}
				s.bindResultLocked(b.ResultID, existing)
				s.restoreReadResultBindingLocked(callNames[b.ToolUseID], b, existing)
				continue
			}
			byLabel[src.Label] = &src
			s.bindResultLocked(b.ResultID, &src)
			s.restoreReadResultBindingLocked(callNames[b.ToolUseID], b, &src)
			if ref := src.SavedResult; ref != nil && sourceResultID(ref.ResultID) {
				if ref.OriginAmbiguous {
					s.resultSources[ref.ResultID] = resultSourceBinding{ambiguous: true}
				} else if ref.Origin != nil {
					original := *ref.Origin
					original.SavedResult = nil
					if blockedLabels[original.Label] {
						ref.Origin, ref.OriginAmbiguous = nil, true
						s.resultSources[ref.ResultID] = resultSourceBinding{ambiguous: true}
						continue
					}
					if n, valid := sourceLabelNumber(original.Label); valid {
						if n > s.seq {
							s.seq = n
						}
						if existing, exists := byLabel[original.Label]; exists {
							if !sourceCheckpointSameIdentity(existing, &original) {
								s.invalidateCheckpointLabelLocked(original.Label, byLabel, blockedLabels)
								ref.Origin, ref.OriginAmbiguous = nil, true
								s.resultSources[ref.ResultID] = resultSourceBinding{ambiguous: true}
								continue
							}
							s.bindResultLocked(ref.ResultID, existing)
							s.bindResultLocked(ref.ResultID, &original)
						} else {
							byLabel[original.Label] = &original
							s.bindResultLocked(ref.ResultID, &original)
						}
					}
				}
			}
		}
	}
	all := make([]*Source, 0, len(byLabel))
	for _, source := range byLabel {
		all = append(all, source)
	}
	// Parallel tool results need not be persisted in stamp order.
	sort.Slice(all, func(i, j int) bool {
		left, _ := strconv.Atoi(all[i].Label[1:])
		right, _ := strconv.Atoi(all[j].Label[1:])
		return left < right
	})
	s.byLabel, s.all = byLabel, all
}

// rel shortens an absolute path under the session's working directory, for
// titles and locators a person reads. A path the tool reported relative already
// is returned as-is; one outside the root keeps its absolute form rather than
// growing a ../ ladder.
func (s *SourceStore) rel(p string) string {
	if p == "" || s.cwd == "" || !filepath.IsAbs(p) {
		return p
	}
	r, err := filepath.Rel(s.cwd, p)
	if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return p
	}
	return filepath.ToSlash(r)
}

// fileLocator is the file:// form, with the shown line range when one was read.
func fileLocator(path string, first, shown int) string {
	if first > 0 && shown > 0 {
		return fmt.Sprintf("file://%s#L%d-%d", path, first, first+shown-1)
	}
	return "file://" + path
}

// subagentTitle is the delegated brief, clipped; the details' mode is the
// fallback for a call whose arguments never arrived (a review refusal).
func subagentTitle(args json.RawMessage, d tools.SubagentDetails) string {
	var a struct {
		Task string `json:"task"`
	}
	if len(args) > 0 && json.Unmarshal(args, &a) == nil && strings.TrimSpace(a.Task) != "" {
		return clipRunes(oneLine(a.Task), 60)
	}
	if d.Mode != "" {
		return "subagent: " + d.Mode
	}
	return "subagent"
}

// citeSummary is the third field of the [cite:...] header: the one-line digest
// that lets the model pick a label without opening the source. bash answers
// "which command, and did it succeed"; read answers "which lines"; everything
// else falls back to the source's title.
func citeSummary(details any, src *Source) string {
	switch d := details.(type) {
	case tools.ToolResultReadDetails:
		return savedResultSummary(d, src)
	case tools.BashDetails:
		return fmt.Sprintf("exit %d | %s", d.ExitCode, clipRunes(oneLine(d.Command), 60))
	case tools.ReadDetails:
		if d.ShownLines > 0 {
			return fmt.Sprintf("%s:%d-%d", src.Title, d.FirstLine, d.FirstLine+d.ShownLines-1)
		}
	case tools.InformationDetails:
		if d.Information != nil {
			return fmt.Sprintf("%s | version %d", src.Title, d.Information.Version)
		}
	}
	return src.Title
}

// StripCiteHeader removes the stamped first line from a persisted tool result's
// text. The live event stream carries the raw output (the stamp is for the
// model, not the tool card), and this is what keeps a reloaded session's cards
// identical to the live ones instead of gaining a metadata line.
func StripCiteHeader(text string) string {
	if !strings.HasPrefix(text, "[cite:") {
		return text
	}
	nl := strings.IndexByte(text, '\n')
	if nl < 0 || !strings.HasSuffix(text[:nl], "]") {
		return text
	}
	return text[nl+1:]
}

// oneLine flattens whitespace runs, so a multi-line command stays inside the
// single line the header format has for it.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// clipRunes truncates by runes — a byte cut can split a multi-byte character
// and hand the model replacement glyphs.
func clipRunes(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
