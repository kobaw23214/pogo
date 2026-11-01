package agent

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

// SavedResultCitation describes a local view, not a fresh observation of the
// remote source. Query/filter and saved-text search arguments belong to the
// host-generated receipt; they are never interpreted as instructions or used to
// grant storage access. Query retains both forms for transcript compatibility.
type SavedResultCitation struct {
	ResultID        string          `json:"result_id"`
	View            string          `json:"view"`
	Pointer         string          `json:"pointer,omitempty"`
	Origin          *Source         `json:"origin,omitempty"`
	OriginAmbiguous bool            `json:"origin_ambiguous,omitempty"`
	Query           json.RawMessage `json:"query,omitempty"`
}

type resultSourceBinding struct {
	source    *Source
	ambiguous bool
}

func sourceResultID(id string) bool {
	if len(id) != 65 || id[0] != 'R' {
		return false
	}
	for _, c := range id[1:] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// A result ID hashes observation content, not its URL or time. Identical
// content can come from distinct artifacts. Keep that ambiguity instead of
// arbitrarily attributing a later read to the first or last webpage.
func (s *SourceStore) bindResult(id string, source *Source) {
	if !sourceResultID(id) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bindResultLocked(id, source)
}

func (s *SourceStore) bindResultLocked(id string, source *Source) {
	if !sourceResultID(id) {
		return
	}
	if s.resultSources == nil {
		s.resultSources = make(map[string]resultSourceBinding)
	}
	previous, exists := s.resultSources[id]
	if !exists {
		s.resultSources[id] = resultSourceBinding{source: source}
		return
	}
	if previous.ambiguous || sameResultSource(previous.source, source) {
		return
	}
	s.resultSources[id] = resultSourceBinding{ambiguous: true}
}

func sameResultSource(a, b *Source) bool {
	if a == nil || b == nil {
		return a == b
	}
	// Information locators contain the immutable version and snapshot hash.
	// Time and call ID deliberately do not make an identical observation fresh.
	return a.Type == b.Type && a.Locator == b.Locator && a.Hash == b.Hash
}

func (s *SourceStore) resultOrigin(id string) (resultSourceBinding, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	binding, ok := s.resultSources[id]
	return binding, ok
}

// restoredResultSource is used only with ToolResultReadDetails returned by the
// protected native reader. It must never parse a source label from result text
// or trust a model's result_id without that successful host read.
func (s *SourceStore) restoredResultSource(details tools.ToolResultReadDetails) (*Source, bool) {
	binding, known := s.resultOrigin(details.ResultID)
	if known && !binding.ambiguous && binding.source == nil {
		// Todo/bookmark observations do not become document evidence by being
		// saved and read again.
		return nil, true
	}
	// Search offsets count matches, not text bytes. Like query, search is a
	// derived local view and cannot reuse the raw observation's source stamp.
	if known && !binding.ambiguous && (details.View == "text" || details.View == "json" && !details.Omitted) {
		return binding.source, true
	}
	return nil, false
}

func (s *SourceStore) savedResultCitation(details tools.ToolResultReadDetails, args json.RawMessage) *SavedResultCitation {
	ref := &SavedResultCitation{ResultID: details.ResultID, View: details.View, Pointer: details.Pointer}
	if binding, known := s.resultOrigin(details.ResultID); known {
		ref.OriginAmbiguous = binding.ambiguous
		if binding.source != nil && !binding.ambiguous {
			// Copy only the original artifact stamp, never a recursive chain of
			// query citations. Ordinary text/json reads reuse that stamp directly.
			original := *binding.source
			original.SavedResult = nil
			ref.Origin = &original
		}
	}
	if details.View == "query" || details.View == "search" {
		// Successful query/search arguments have already passed the native reader's
		// bounded, strict decoder, including duplicate and unknown key checks.
		ref.Query = append(json.RawMessage(nil), args...)
	}
	return ref
}

// A kept text/json read may outlive the original tool result during compaction.
// Its trusted native details name the same base observation; its reused Source
// retains the original artifact identity. Rebuild that link without reading IDs
// out of arbitrary tool output or transferring access to a different store.
func (s *SourceStore) restoreReadResultBindingLocked(toolName string, block llm.Block, source *Source) {
	if toolName != tools.ToolResultReadName || block.IsError || source != nil && source.SavedResult != nil {
		return
	}
	var read tools.ToolResultReadDetails
	if json.Unmarshal(block.Details, &read) != nil {
		return
	}
	switch read.View {
	case "summary", "text", "json", "native", "query", "search":
	default:
		return // catalog/retention metadata cannot replace source bindings
	}
	if source == nil || read.View == "text" || read.View == "json" && !read.Omitted {
		s.bindResultLocked(read.ResultID, source)
	}
}

// Raw recovery reuses the original source label, so CallID intentionally names
// the original observation. Only a successful protected native read can explain
// that mismatch to the missing-citation check.
func (a *Agent) recoveredResultRead(call, result llm.Block, source *Source) (tools.ToolResultReadDetails, bool) {
	var read tools.ToolResultReadDetails
	if call.Name != tools.ToolResultReadName || result.IsError || a.sources == nil || a.registry == nil {
		return read, false
	}
	if reader, _ := a.registry.Get(call.Name); reader != nil {
		if _, native := reader.(*tools.ToolResultRead); !native {
			return read, false
		}
	} else {
		return read, false
	}
	if json.Unmarshal(result.Details, &read) != nil || read.OriginalIsError {
		return read, false
	}
	origin, handled := a.sources.restoredResultSource(read)
	return read, handled && origin != nil && origin.Label == source.Label && origin.CallID == source.CallID && origin.Hash == source.Hash
}

// rememberBudgetResult binds only host metadata, never a [cite:...] lookalike
// in the text. It also runs while reconstructing request views after resume.
func (a *Agent) rememberBudgetResult(id string, block llm.Block) {
	if a.sources == nil || !sourceResultID(id) {
		return
	}
	if len(block.Source) == 0 {
		a.sources.bindResult(id, nil)
		return
	}
	var saved Source
	if json.Unmarshal(block.Source, &saved) != nil {
		return
	}
	if source, ok := a.sources.Get(saved.Label); ok && source.CallID == saved.CallID && source.Hash == saved.Hash {
		_, known := a.sources.resultOrigin(id)
		a.sources.bindResult(id, source)
		if !known {
			a.noteResultCatalogSource(id, source)
		}
	}
}

func (a *Agent) noteResultCatalogSource(id string, source *Source) {
	if a.registry == nil || !sourceResultID(id) || source == nil || source.SavedResult != nil {
		return
	}
	observed := ""
	if source.Time > 0 {
		observed = time.UnixMilli(source.Time).UTC().Format(time.RFC3339Nano)
	}
	_ = a.registry.NoteResultSource(id, source.Title, source.Locator, observed)
}

func savedResultSummary(details tools.ToolResultReadDetails, source *Source) string {
	summary := fmt.Sprintf("%s | stored %s view", source.Title, details.View)
	if source.SavedResult == nil && source.Time > 0 {
		summary += " | original observation " + time.UnixMilli(source.Time).UTC().Format(time.RFC3339Nano)
	}
	return summary
}
