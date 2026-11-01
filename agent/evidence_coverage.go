package agent

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/yosukeno/pogo/internal/evidencebounds"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

const (
	evidenceMaxCalls      = 512
	evidenceMaxFragments  = 1024
	evidenceMaxBytes      = 512 << 10
	evidenceMaxSelections = 128
	evidenceMaxHits       = 32768
	evidenceMaxPages      = 2048
)

// A fragment is body actually included in a successful model request, never a
// full UI result or a value fetched from storage by the verifier. Ranges are
// UTF-8 bytes in the named domain, not positions in the JSON transport encoding.
type evidenceBodyFragment struct {
	ResultID, CallID, SourceLabel, View, Domain, Pointer, Text string
	Start, End                                                 int
	HasRange, CompleteValue                                    bool
	StartsLine, EndsLine                                       bool
}

type EvidenceSelectionCoverage struct {
	SelectorKey string `json:"selector_key"`
	ResultID    string `json:"result_id"`
	CallID      string `json:"call_id"`
	SourceLabel string `json:"source_label,omitempty"`
	View        string `json:"view"`
	RangeUnit   string `json:"range_unit,omitempty"`
	Pointer     string `json:"pointer,omitempty"`
	Pattern     string `json:"pattern,omitempty"`
	Total       int    `json:"scanned_matches"`
	Delivered   int    `json:"delivered_matches"`
	// This is a gap in this selector's row delivery. Another observed read may
	// independently contain the same body; the count is not global absence.
	Undelivered              int                      `json:"undelivered_matches"`
	KnownViaOtherBody        int                      `json:"known_via_other_body,omitempty"`
	OtherBodyCountLowerBound bool                     `json:"other_body_count_lower_bound,omitempty"`
	OtherBody                []evidencebounds.Witness `json:"other_body,omitempty"`
	ScanComplete             bool                     `json:"scan_complete"`
	Complete                 bool                     `json:"delivery_complete"`
	Unknown                  bool                     `json:"unknown,omitempty"`
	NextRequest              json.RawMessage          `json:"next_request,omitempty"`
}

// Host-only status: this is delivery accounting, not an entailment verdict or
// permission to claim that the remote source was complete.
type EvidenceCoverageStatus struct {
	TrackedSelections        int                         `json:"tracked_selections"`
	IncompleteSelections     int                         `json:"incomplete_selections"`
	Incomplete               bool                        `json:"incomplete"`
	CheckpointSent           bool                        `json:"checkpoint_sent"`
	CheckpointAnswerRestored bool                        `json:"checkpoint_answer_restored"`
	CheckpointFailure        string                      `json:"checkpoint_failure,omitempty"`
	FragmentCount            int                         `json:"fragment_count"`
	FragmentBytes            int                         `json:"fragment_bytes"`
	Partial                  []EvidenceSelectionCoverage `json:"partial,omitempty"`
	CrossSearchRelations     []evidencebounds.Relation   `json:"cross_search_relations,omitempty"`
	BodyReferences           []EvidenceBodyReference     `json:"body_references,omitempty"`
}

type EvidenceBodyReference struct {
	ID          string `json:"id"`
	CallID      string `json:"call_id"`
	SourceLabel string `json:"source_label"`
}

type evidenceSelection struct {
	receipt       EvidenceSelectionCoverage
	args          map[string]any
	hits          map[int]bool
	positions     map[int]evidencebounds.Hit
	rowsRequested bool
	originalRows  map[int]int
}

type evidenceLedger struct {
	agent                      *Agent
	calls                      map[string]llm.Block
	seen                       map[string]bool
	bodies                     []evidenceBodyFragment
	selections                 map[string]*evidenceSelection
	order                      []string
	bytes, hits                int
	incomplete, checkpointSent bool
	checkpointAnswerRestored   bool
	checkpointFailure          string
	relationCache              *evidencebounds.Report
	relationCacheIncomplete    bool
}

func newEvidenceLedger(a *Agent) *evidenceLedger {
	return &evidenceLedger{agent: a, calls: map[string]llm.Block{}, seen: map[string]bool{}, selections: map[string]*evidenceSelection{}}
}

func (l *evidenceLedger) Register(calls []llm.Block) {
	if l == nil || l.agent == nil || l.agent.registry == nil {
		return
	}
	l.relationCache = nil
	reader, _ := l.agent.registry.Get(tools.ToolResultReadName)
	if _, ok := reader.(*tools.ToolResultRead); !ok {
		return
	}
	for _, c := range calls {
		if c.Name != tools.ToolResultReadName || c.ID == "" {
			continue
		}
		if len(l.calls) >= evidenceMaxCalls {
			l.incomplete = true
			continue
		}
		if old, exists := l.calls[c.ID]; exists {
			if !bytes.Equal(old.Input, c.Input) {
				l.incomplete = true
				delete(l.calls, c.ID)
			}
			continue
		}
		c.Input = append(json.RawMessage(nil), c.Input...)
		l.calls[c.ID] = c
	}
}

// ObservePrompt must be called only after this exact prompt's Stream succeeds.
// The caller resets this ledger for every run and fresh human steering scope.
func (l *evidenceLedger) ObservePrompt(prompt []llm.Message) {
	if l == nil {
		return
	}
	counts := map[string]int{}
	for _, m := range prompt {
		for _, b := range m.Content {
			if b.Type == llm.BlockToolUse {
				counts[b.ID]++
			}
		}
	}
	for _, m := range prompt {
		for _, b := range m.Content {
			if b.Type != llm.BlockToolResult || b.IsError {
				continue
			}
			call, ok := l.calls[b.ToolUseID]
			if !ok {
				continue
			}
			if counts[call.ID] != 1 {
				l.incomplete = true
				continue
			}
			stamp, read, ok := l.trusted(call, b)
			if !ok {
				continue
			}
			body := StripCiteHeader(b.Text)
			sum := sha256.Sum256([]byte(body))
			identity := call.ID + ":" + hex.EncodeToString(sum[:])
			if l.seen[identity] {
				continue
			}
			if len(l.seen) >= evidenceMaxPages {
				l.incomplete = true
				continue
			}
			l.seen[identity] = true
			l.relationCache = nil
			if !l.observe(call, body, stamp, read) {
				l.incomplete = true
			}
		}
	}
}

func (l *evidenceLedger) trusted(call, b llm.Block) (*Source, tools.ToolResultReadDetails, bool) {
	var read tools.ToolResultReadDetails
	var saved Source
	if l.agent == nil || l.agent.registry == nil || l.agent.sources == nil || json.Unmarshal(b.Details, &read) != nil || !sourceResultID(read.ResultID) || json.Unmarshal(b.Source, &saved) != nil {
		return nil, read, false
	}
	reader, _ := l.agent.registry.Get(call.Name)
	if _, ok := reader.(*tools.ToolResultRead); !ok {
		return nil, read, false
	}
	src, ok := l.agent.sources.Get(saved.Label)
	if !ok || src.CallID != saved.CallID || src.Hash != saved.Hash || src.Time != saved.Time || !strings.HasPrefix(b.Text, "[cite:"+src.Label+" | ") {
		return nil, read, false
	}
	binding, known := l.agent.sources.resultOrigin(read.ResultID)
	if !known || binding.ambiguous || binding.source == nil {
		return nil, read, false
	}
	if src.SavedResult != nil {
		ref := src.SavedResult
		if src.CallID != b.ToolUseID || ref.ResultID != read.ResultID || ref.View != read.View || ref.Pointer != read.Pointer || ref.Origin == nil || ref.OriginAmbiguous || !sameResultSource(binding.source, ref.Origin) || binding.source.CallID != ref.Origin.CallID || binding.source.Time != ref.Origin.Time {
			return nil, read, false
		}
	} else if (read.View != "text" && read.View != "json") || !sameResultSource(binding.source, src) || binding.source.CallID != src.CallID {
		return nil, read, false
	}
	if original, _ := l.agent.registry.Get(read.OriginalTool); original != nil {
		if mcp, ok := original.(*tools.MCPTool); ok && mcp.CatalogRole() == tools.MCPCatalogRoleDirectory {
			return nil, read, false
		}
	}
	return src, read, true
}

func (l *evidenceLedger) Fragments() []evidenceBodyFragment {
	if l == nil {
		return nil
	}
	return append([]evidenceBodyFragment(nil), l.bodies...)
}
func (l *evidenceLedger) Incomplete() bool { return l != nil && l.incomplete }

func (l *evidenceLedger) addFragment(f evidenceBodyFragment) {
	if f.Text == "" || !utf8.ValidString(f.Text) {
		return
	}
	if f.HasRange && (f.Start < 0 || f.End-f.Start != len(f.Text)) {
		l.incomplete = true
		return
	}
	for i, old := range l.bodies {
		if old.ResultID == f.ResultID && old.Domain == f.Domain && old.Pointer == f.Pointer && old.Start == f.Start && old.End == f.End && old.HasRange == f.HasRange && old.Text == f.Text {
			if f.StartsLine && !old.StartsLine || f.EndsLine && !old.EndsLine {
				l.bodies[i].StartsLine = old.StartsLine || f.StartsLine
				l.bodies[i].EndsLine = old.EndsLine || f.EndsLine
				l.relationCache = nil
			}
			return
		}
	}
	if len(l.bodies) >= evidenceMaxFragments || l.bytes+len(f.Text) > evidenceMaxBytes {
		l.incomplete = true
		return
	}
	l.bodies = append(l.bodies, f)
	l.bytes += len(f.Text)
	l.relationCache = nil
}

func evidenceJSON(text string) (map[string]any, bool) {
	dec := json.NewDecoder(strings.NewReader(text))
	dec.UseNumber()
	var value map[string]any
	if dec.Decode(&value) != nil || value == nil {
		return nil, false
	}
	var tail any
	return value, dec.Decode(&tail) == io.EOF
}
func evidenceInt(value any) (int, bool) {
	n, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	v, e := strconv.Atoi(string(n))
	return v, e == nil && v >= 0
}
func evidenceString(v any) string { s, _ := v.(string); return s }
func evidencePointer(base, key string) string {
	return base + "/" + strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
}

func (l *evidenceLedger) observe(call llm.Block, body string, src *Source, read tools.ToolResultReadDetails) bool {
	fragment := evidenceBodyFragment{ResultID: read.ResultID, CallID: call.ID, SourceLabel: src.Label, View: read.View, Pointer: read.Pointer}
	if read.View == "summary" || read.View == "native" && read.Pointer == "" {
		return true
	}
	if read.View == "text" && strings.HasPrefix(body, "Result "+read.ResultID+"; text bytes [") {
		var start, end, total int
		var complete bool
		line := strings.SplitN(body, "\n", 2)[0]
		prefix := "Result " + read.ResultID + "; text bytes ["
		if _, err := fmt.Sscanf(strings.TrimPrefix(line, prefix), "%d,%d) of %d; range_complete=%t.", &start, &end, &total, &complete); err != nil || start != read.Offset || total != read.Total || end < start || end > total {
			return false
		}
		at := strings.Index(body, "\n---\n")
		if at < 0 {
			return false
		}
		text := body[at+5:]
		if len(text) != end-start {
			return false
		}
		fragment.Domain = "stored_text"
		fragment.Pointer = ""
		fragment.Start, fragment.End, fragment.HasRange, fragment.Text = start, end, true, text
		fragment.StartsLine, fragment.EndsLine = start == 0, end == total
		l.addFragment(fragment)
		return true
	}
	page, ok := evidenceJSON(body)
	if !ok || page["result_id"] != read.ResultID || page["view"] != read.View || evidenceString(page["pointer"]) != read.Pointer {
		return false
	}
	if page["model_budget_omitted"] == true || page["omitted"] == true {
		return l.observeSelection(call, page, src, read, nil)
	}
	if text, ok := page["value_fragment"].(string); ok {
		if read.View == "native" && !evidenceNativeBodyPointer(read.Pointer) {
			return true
		}
		start, sok := evidenceInt(page["offset"])
		end, eok := evidenceInt(page["end_offset"])
		if !eok {
			if next, yes := evidenceInt(page["next_offset"]); yes {
				end, eok = next, true
			} else if total, yes := evidenceInt(page["stored_string_bytes"]); yes && page["complete"] == true {
				end, eok = total, true
			}
		}
		if !sok || !eok || len(text) != end-start || start != read.Offset {
			return false
		}
		fragment.Domain = read.View
		if read.View == "text" {
			fragment.Domain = "stored_text"
			fragment.Pointer = ""
			fragment.StartsLine, fragment.EndsLine = start == 0, end == read.Total
		}
		fragment.Start, fragment.End, fragment.HasRange, fragment.Text = start, end, true, text
		l.addFragment(fragment)
		return true
	}
	if read.View == "search" || read.View == "query" {
		return l.observeSelection(call, page, src, read, &fragment)
	}
	domain := read.View
	if value, exists := page["value"]; exists {
		l.stringsInValue(fragment, domain, read.Pointer, value, 0)
		return true
	}
	if items, ok := page["items"].([]any); ok {
		offset, valid := evidenceInt(page["offset"])
		if !valid || offset != read.Offset {
			return false
		}
		for i, item := range items {
			l.stringsInValue(fragment, domain, read.Pointer+"/"+strconv.Itoa(offset+i), item, 0)
		}
	}
	return true
}

func (l *evidenceLedger) stringsInValue(base evidenceBodyFragment, domain, pointer string, value any, depth int) {
	if depth > 32 {
		l.incomplete = true
		return
	}
	switch v := value.(type) {
	case string:
		if domain == "native" && !evidenceNativeBodyPointer(pointer) {
			return
		}
		base.Domain, base.Pointer, base.Text, base.CompleteValue = domain, pointer, v, true
		l.addFragment(base)
	case []any:
		for i, x := range v {
			l.stringsInValue(base, domain, pointer+"/"+strconv.Itoa(i), x, depth+1)
		}
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			l.stringsInValue(base, domain, evidencePointer(pointer, k), v[k], depth+1)
		}
	}
}

func evidenceNativeBodyPointer(pointer string) bool {
	if pointer == "/structuredContent" || strings.HasPrefix(pointer, "/structuredContent/") {
		return true
	}
	parts := strings.Split(pointer, "/")
	if len(parts) < 4 || parts[0] != "" || parts[1] != "content" {
		return false
	}
	if _, err := strconv.Atoi(parts[2]); err != nil {
		return false
	}
	return len(parts) == 4 && parts[3] == "text" || len(parts) == 5 && parts[3] == "resource" && parts[4] == "text"
}

func (l *evidenceLedger) observeSelection(call llm.Block, page map[string]any, src *Source, read tools.ToolResultReadDetails, base *evidenceBodyFragment) bool {
	if read.View != "search" && read.View != "query" {
		return true
	}
	if page["scan_complete"] != true {
		return true
	}
	var args map[string]any
	raw := call.Input
	if src.SavedResult != nil && len(src.SavedResult.Query) > 0 {
		raw = src.SavedResult.Query
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if dec.Decode(&args) != nil || args["result_id"] != read.ResultID || args["view"] != read.View {
		return false
	}
	total, ok := evidenceInt(page["total_matches"])
	pattern := evidenceString(args["pattern"])
	rowsRequested := true
	groupBy, _ := args["group_by"].([]any)
	grouped := read.View == "query" && len(groupBy) > 0
	if read.View == "search" {
		if pattern == "" || read.Pattern != pattern || page["pattern"] != nil && page["pattern"] != pattern {
			return false
		}
	} else {
		total, ok = evidenceInt(page["matched_rows"])
		if grouped {
			total, ok = evidenceInt(page["total_groups"])
			if page["groups_included"] != true || page["rows_included"] != false {
				return false
			}
		} else if include, exists := args["include_rows"].(bool); exists {
			rowsRequested = include
		} else {
			_, aggregate := args["aggregate"]
			rowsRequested = !aggregate
		}
	}
	if !ok || total != read.Total {
		return false
	}
	canonical := make(map[string]any, len(args))
	for k, value := range args {
		canonical[k] = value
	}
	if !grouped {
		delete(canonical, "aggregate")
	}
	canonical["offset"], canonical["limit"] = 0, 1
	if read.View == "search" {
		canonical["context_lines"] = 0
	} else if grouped {
		canonical["include_rows"] = false
	} else {
		canonical["include_rows"] = true
	}
	encoded, _ := json.Marshal(canonical)
	key, normalized := tools.ToolResultRequestKey(encoded)
	if !normalized {
		return false
	}
	selection := l.selections[key]
	if selection == nil {
		if len(l.selections) >= evidenceMaxSelections {
			l.incomplete = true
			return true
		}
		selection = &evidenceSelection{receipt: EvidenceSelectionCoverage{SelectorKey: key, ResultID: read.ResultID, CallID: call.ID, SourceLabel: src.Label, View: read.View, Pointer: read.Pointer, Pattern: pattern, Total: total, ScanComplete: true}, args: args, hits: map[int]bool{}, positions: map[int]evidencebounds.Hit{}}
		if grouped {
			selection.receipt.RangeUnit = "computed groups; not original source rows"
		}
		selection.originalRows = make(map[int]int)
		l.selections[key] = selection
		l.order = append(l.order, key)
	}
	if selection.receipt.Total != total {
		selection.receipt.Unknown = true
		l.incomplete = true
		return true
	}
	selection.rowsRequested = selection.rowsRequested || rowsRequested
	offset, valid := evidenceInt(page["offset"])
	if !valid || offset != read.Offset {
		return false
	}
	items, _ := page["items"].([]any)
	displayed := page["page_displayed"] != false && page["model_budget_omitted"] != true && page["omitted"] != true
	if displayed && base != nil {
		shown, valid := evidenceInt(page["shown_items"])
		if !valid || shown != len(items) || offset+shown > total {
			return false
		}
		for i, value := range items {
			row, ok := value.(map[string]any)
			if !ok {
				return false
			}
			if read.View == "search" {
				index, iok := evidenceInt(row["match_index"])
				start, sok := evidenceInt(row["context_start"])
				end, eok := evidenceInt(row["context_end"])
				ms, mok := evidenceInt(row["match_start"])
				me, nok := evidenceInt(row["match_end"])
				text, tok := row["text"].(string)
				if !iok || index != offset+i || !sok || !eok || !mok || !nok || !tok || len(text) != end-start || ms < start || me < ms || me > end || me-ms != len(pattern) || text[ms-start:me-start] != pattern {
					return false
				}
				f := *base
				f.Domain, f.Pointer, f.Text, f.Start, f.End, f.HasRange = "stored_text", "", text, start, end, true
				f.StartsLine, f.EndsLine = row["context_truncated"] != true, row["context_truncated"] != true
				l.addFragment(f)
				if !selection.hits[index] && l.hits >= evidenceMaxHits {
					selection.receipt.Unknown, l.incomplete = true, true
					continue // Byte anchors obey the same cap as native row ordinals.
				}
				position := evidencebounds.Hit{Ordinal: index, Start: ms, End: me}
				if old, exists := selection.positions[index]; exists && old != position {
					selection.receipt.Unknown, l.incomplete = true, true
					return false
				}
				selection.positions[index] = position
				if row["context_truncated"] == true {
					selection.receipt.Unknown = true
				}
			} else if grouped {
				index, valid := evidenceInt(row["group_index"])
				if !valid || index != offset+i || row["original_index"] != nil || row["aggregate"] == nil {
					return false
				}
				// A displayed computed group is not a raw array row. Its strings
				// must not establish source-body or cross-search quote coverage.
			} else {
				index, ok := evidenceInt(row["original_index"])
				if !ok || index < 0 || row["pointer_suffix"] != "/"+strconv.Itoa(index) {
					return false
				}
				if ordinal, exists := selection.originalRows[index]; exists && ordinal != offset+i {
					selection.receipt.Unknown, l.incomplete = true, true
					return false
				}
				selection.originalRows[index] = offset + i
				pointer := read.Pointer + "/" + strconv.Itoa(index)
				if value, exists := row["value"]; exists {
					l.stringsInValue(*base, "json", pointer, value, 0)
				}
				if fields, ok := row["fields"].(map[string]any); ok {
					for pointerSuffix, value := range fields {
						field, ok := value.(map[string]any)
						if ok && field["present"] == true {
							l.stringsInValue(*base, "json", pointer+pointerSuffix, field["value"], 0)
						}
					}
				}
			}
			if !selection.hits[offset+i] {
				if l.hits >= evidenceMaxHits {
					l.incomplete = true
					selection.receipt.Unknown = true
					continue
				}
				selection.hits[offset+i] = true
				l.hits++
			}
		}
	}
	return true
}

func (l *evidenceLedger) selectionReceipt(s *evidenceSelection) EvidenceSelectionCoverage {
	r := s.receipt
	r.Delivered = len(s.hits)
	r.Undelivered = r.Total - r.Delivered
	r.Complete = !r.Unknown && r.Delivered == r.Total
	if !r.Complete && s.rowsRequested && !r.Unknown {
		missing := 0
		for missing < r.Total && s.hits[missing] {
			missing++
		}
		args := map[string]any{}
		for k, v := range s.args {
			args[k] = v
		}
		groupBy, _ := args["group_by"].([]any)
		grouped := r.View == "query" && len(groupBy) > 0
		if !grouped {
			delete(args, "aggregate")
		}
		args["offset"], args["limit"] = missing, 20
		if grouped {
			args["include_rows"] = false
		} else if r.View == "query" {
			args["include_rows"] = true
		}
		r.NextRequest, _ = json.Marshal(args)
		if len(r.NextRequest) > 2048 {
			r.NextRequest = nil
		}
	}
	return r
}
func (l *evidenceLedger) Snapshot() EvidenceCoverageStatus {
	out := EvidenceCoverageStatus{}
	if l == nil {
		return out
	}
	out.TrackedSelections = len(l.selections)
	out.Incomplete = l.incomplete
	out.CheckpointSent = l.checkpointSent
	out.CheckpointAnswerRestored = l.checkpointAnswerRestored
	out.CheckpointFailure = l.checkpointFailure
	out.FragmentCount = len(l.bodies)
	out.FragmentBytes = l.bytes
	relations := l.literalRelations()
	coverage := map[string]evidencebounds.Coverage{}
	if relations != nil {
		out.Incomplete = out.Incomplete || relations.Incomplete
		for _, c := range relations.Coverage {
			for _, alias := range c.IDs {
				coverage[alias] = c
			}
		}
		for _, relation := range relations.Relations {
			if relation.Kind != "unknown" || len(relation.Witnesses) > 0 {
				if len(out.CrossSearchRelations) < 6 {
					out.CrossSearchRelations = append(out.CrossSearchRelations, relation)
				}
			}
		}
	}
	for _, key := range l.order {
		s := l.selections[key]
		if !s.rowsRequested {
			continue
		}
		r := l.selectionReceipt(s)
		if known, ok := coverage[key]; ok {
			r.KnownViaOtherBody, r.OtherBodyCountLowerBound = known.OtherBodyCount, known.OtherBodyCountLowerBound
			r.OtherBody = known.OtherBody
		}
		if !r.Complete {
			out.IncompleteSelections++
			if r.Delivered > 0 && len(out.Partial) < 3 {
				out.Partial = append(out.Partial, r)
			}
		}
	}
	refs := map[string]bool{}
	for _, partial := range out.Partial {
		for _, witness := range partial.OtherBody {
			for _, id := range witness.FragmentIDs {
				refs[id] = true
			}
		}
	}
	for _, relation := range out.CrossSearchRelations {
		for _, witness := range relation.Witnesses {
			for _, id := range append(append([]string(nil), witness.Left.FragmentIDs...), witness.Right.FragmentIDs...) {
				refs[id] = true
			}
		}
	}
	for i, body := range l.bodies {
		id := evidenceBodyID(i)
		if refs[id] {
			out.BodyReferences = append(out.BodyReferences, EvidenceBodyReference{id, body.CallID, body.SourceLabel})
		}
	}
	return out
}

func evidenceBodyID(index int) string { return fmt.Sprintf("body_%06d", index+1) }

func (l *evidenceLedger) literalRelations() *evidencebounds.Report {
	if l.relationCache != nil && l.relationCacheIncomplete == l.incomplete {
		return l.relationCache
	}
	in := evidencebounds.Input{Incomplete: l.incomplete}
	for i, body := range l.bodies {
		if body.Domain != "stored_text" || !body.HasRange || body.Pointer != "" {
			continue
		}
		in.Fragments = append(in.Fragments, evidencebounds.Fragment{ID: evidenceBodyID(i), Domain: evidencebounds.Domain{ResultID: body.ResultID, Kind: body.Domain, Pointer: body.Pointer}, Start: body.Start, End: body.End, Text: body.Text, StartsLine: body.StartsLine, EndsLine: body.EndsLine})
	}
	for _, key := range l.order {
		s := l.selections[key]
		if s.receipt.View != "search" {
			continue
		}
		search := evidencebounds.Search{ID: key, Domain: evidencebounds.Domain{ResultID: s.receipt.ResultID, Kind: "stored_text"}, Pattern: s.receipt.Pattern, Total: s.receipt.Total, ScanComplete: s.receipt.ScanComplete && !s.receipt.Unknown}
		for _, hit := range s.positions {
			search.Hits = append(search.Hits, hit)
		}
		sort.Slice(search.Hits, func(i, j int) bool { return search.Hits[i].Ordinal < search.Hits[j].Ordinal })
		in.Searches = append(in.Searches, search)
	}
	lefts := 0
	for _, left := range in.Searches {
		if left.Total <= len(left.Hits) || len(left.Hits) == 0 {
			continue
		}
		if lefts >= 3 {
			break
		}
		lefts++
		for _, right := range in.Searches {
			if len(in.Relations) >= 24 {
				break
			}
			if left.ID != right.ID && left.Domain == right.Domain {
				in.Relations = append(in.Relations, evidencebounds.RelationRequest{LeftID: left.ID, RightID: right.ID})
			}
		}
	}
	report := evidencebounds.Analyze(in)
	l.relationCache = &report
	l.relationCacheIncomplete = l.incomplete
	return l.relationCache
}

// RejectCheckpointAnswer records the caller's restoration of an earlier complete
// answer when a checkpoint follow-up loses the requested answer structure. It
// does not certify that the restored answer's scope or claims are supported.
func (l *evidenceLedger) RejectCheckpointAnswer(reason string) {
	if l == nil || !l.checkpointSent {
		return
	}
	l.checkpointAnswerRestored = true
	if l.checkpointFailure != "" {
		return
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "answer_structure_not_preserved"
	}
	if len(reason) > 128 {
		reason = reason[:128]
		for !utf8.ValidString(reason) {
			reason = reason[:len(reason)-1]
		}
	}
	l.checkpointFailure = reason
}

func (l *evidenceLedger) CoverageNotice() (string, bool) {
	if l == nil || l.checkpointSent {
		return "", false
	}
	status := l.Snapshot()
	if len(status.Partial) == 0 {
		return "", false
	}
	l.checkpointSent = true
	partial := append([]EvidenceSelectionCoverage(nil), status.Partial...)
	for i := range partial {
		if len(partial[i].OtherBody) > 2 {
			partial[i].OtherBody = partial[i].OtherBody[:2]
		}
	}
	data, _ := json.Marshal(partial)
	if len(data) > 4096 {
		one := partial[0]
		one.NextRequest = nil
		if len(one.Pointer) > 512 {
			one.Pointer = ""
		}
		if len(one.Pattern) > 512 {
			one.Pattern = ""
		}
		partial = []EvidenceSelectionCoverage{one}
		data, _ = json.Marshal(partial)
	}
	var chosen []evidencebounds.Relation
	for _, kind := range []string{"counterexample", "verified_disjoint", "unknown"} {
		for _, relation := range status.CrossSearchRelations {
			if relation.Kind != kind {
				continue
			}
			if len(relation.Witnesses) > 1 {
				relation.Witnesses = relation.Witnesses[:1]
			}
			chosen = []evidencebounds.Relation{relation}
			break
		}
		if len(chosen) > 0 {
			break
		}
	}
	needed := map[string]bool{}
	for _, p := range partial {
		for _, w := range p.OtherBody {
			for _, id := range w.FragmentIDs {
				needed[id] = true
			}
		}
	}
	for _, relation := range chosen {
		for _, w := range relation.Witnesses {
			for _, id := range append(append([]string(nil), w.Left.FragmentIDs...), w.Right.FragmentIDs...) {
				needed[id] = true
			}
		}
	}
	var refs []EvidenceBodyReference
	for _, ref := range status.BodyReferences {
		if needed[ref.ID] {
			refs = append(refs, ref)
		}
	}
	payload := struct {
		Selections []EvidenceSelectionCoverage `json:"selector_delivery"`
		Relations  []evidencebounds.Relation   `json:"cross_search_relations,omitempty"`
		References []EvidenceBodyReference     `json:"body_references,omitempty"`
	}{partial, chosen, refs}
	if encoded, err := json.Marshal(payload); err == nil && len(encoded) <= 6144 {
		data = encoded
	}
	if len(data) > 6144 {
		// The event snapshot retains full bounded evidence. A model checkpoint
		// must still fit when future witness/reference shapes become larger.
		var counts []map[string]any
		for _, p := range partial {
			row := map[string]any{"scanned_matches": p.Total, "delivered_matches": p.Delivered, "undelivered_matches": p.Undelivered, "known_via_other_body": p.KnownViaOtherBody, "delivery_complete": p.Complete}
			if sourceResultID(p.ResultID) {
				row["result_id"] = p.ResultID
			}
			if len(p.SelectorKey) <= 256 {
				row["selector_key"] = p.SelectorKey
			}
			if len(p.View) <= 16 {
				row["view"] = p.View
			}
			counts = append(counts, row)
		}
		data, _ = json.Marshal(map[string]any{"selector_delivery": counts, "receipt_details_omitted": true})
		if len(data) > 6144 {
			data = []byte(`{"receipt_details_omitted":true,"reason":"receipt_budget"}`)
		}
	}
	return "[pogo] saved-result coverage checkpoint: the current task's actual model-request receipts contain only part of these selected saved results: " + string(data) + ". A completed scan/count is not delivery of all matching rows. Each undelivered_matches count applies only to that selector; another observed read may independently contain the same body. Cross-search receipts compare exact literals on the same complete delivered line only: a counterexample refutes exclusion, verified_disjoint proves only that bounded literal relation, and unknown proves no exclusion. Native selector delivery gaps remain unchanged even when other body witnesses are known. Do not infer or add properties of undelivered matches from counts, scan completion, or a decision not to rely on them. Any such property needs actual delivered body evidence from this or another read; otherwise leave it undetermined. If the existing answer is already supported within the observed scope, preserve its supported facts, exact quotations and scope without adding explanations about unseen content. Read only a necessary saved continuation with tool_result when extra detail is required; otherwise keep conclusions within the delivered evidence. Do not repeat the original tool/action, do not read unrelated pages, and do not claim remote-source completeness. Return the complete answer requested by the user after this check. Preserve the full requested format: for raw or fenced JSON, return the complete JSON with all keys, value types and schema intact. Do not reply with only an acknowledgement, a statement that the previous answer is unchanged, or a reference back to an earlier answer. This is one delivery checkpoint, not a semantic judgment of your wording.", true
}
