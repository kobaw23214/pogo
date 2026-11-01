package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

func savedResultIDForTest(letter string) string { return "R" + strings.Repeat(letter, 64) }

func savedPageSourceForTest(store *SourceStore, callID, url string) *Source {
	return store.Next("browser", map[string]any{"url": url, "title": "Observed page"}, callID, "the same observed text", nil)
}

func savedReadHistoryForTest(t *testing.T, source *Source, read tools.ToolResultReadDetails) []llm.Message {
	t.Helper()
	args, err := json.Marshal(map[string]any{"result_id": read.ResultID, "view": read.View})
	if err != nil {
		t.Fatal(err)
	}
	block := llm.Block{Type: llm.BlockToolResult, ToolUseID: "kept-read", Text: "saved observation", Details: marshalDetails(read)}
	if source != nil {
		block.Source = marshalSource(source)
		block.Text = fmt.Sprintf("[cite:%s | tool_result | stored view]\nsaved observation", source.Label)
	}
	return []llm.Message{
		{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockToolUse, ID: block.ToolUseID, Name: tools.ToolResultReadName, Input: args}}},
		{Role: llm.RoleUser, Content: []llm.Block{block}},
	}
}

func TestSavedResultSourceRawReadReusesOriginalStamp(t *testing.T) {
	store := newSourceStore("")
	id := savedResultIDForTest("a")
	original := savedPageSourceForTest(store, "original-observation", "https://example.com/report")
	original.Time = 1234
	before, _ := json.Marshal(original)
	store.bindResult(id, original)
	for _, view := range []string{"text", "json"} {
		read := tools.ToolResultReadDetails{ResultID: id, OriginalTool: "browser", View: view, Total: 100, Complete: true}
		got := store.Next(tools.ToolResultReadName, read, "read-"+view, "different local page text", nil)
		if got != original || len(store.All()) != 1 {
			t.Fatalf("%s read invented a new observation: %+v", view, got)
		}
		if summary := savedResultSummary(read, got); !strings.Contains(summary, "original observation 1970-01-01T00:00:01.234Z") {
			t.Fatalf("recovery did not retain the original observation time: %s", summary)
		}
	}
	after, _ := json.Marshal(original)
	if string(before) != string(after) {
		t.Fatal("local recovery changed original evidence identity/time")
	}
}

func TestSavedResultSourceDirectoriesAndQueriesRemainLocal(t *testing.T) {
	for _, tc := range []struct {
		name, view string
		omitted    bool
	}{
		{name: "summary", view: "summary"},
		{name: "native directory", view: "native"},
		{name: "json directory", view: "json", omitted: true},
		{name: "local aggregate", view: "query"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newSourceStore("")
			id := savedResultIDForTest("b")
			original := savedPageSourceForTest(store, "observed", "https://example.com/data")
			store.bindResult(id, original)
			args := json.RawMessage(`{"result_id":"` + id + `","view":"query","pointer":"/rows","aggregate":{"op":"count"}}`)
			read := tools.ToolResultReadDetails{ResultID: id, OriginalTool: "browser", View: tc.view, Pointer: "/rows", Omitted: tc.omitted}
			local := store.Next(tools.ToolResultReadName, read, "local-read", "local metadata or calculation", args)
			if local == nil || local == original || local.Type != SourceTool || local.SavedResult == nil || local.SavedResult.ResultID != id {
				t.Fatalf("local view became a fresh webpage observation: %+v", local)
			}
			if local.SavedResult.Origin == nil || !sameResultSource(local.SavedResult.Origin, original) || local.SavedResult.Origin.SavedResult != nil {
				t.Fatalf("local view lost its nonrecursive parent stamp: %+v", local.SavedResult)
			}
			if tc.view == "query" {
				if string(local.SavedResult.Query) != string(args) {
					t.Fatal("query receipt lost the calculation arguments")
				}
				args[0] = ' '
				if local.SavedResult.Query[0] != '{' {
					t.Fatal("saved query arguments alias caller-owned bytes")
				}
			} else if len(local.SavedResult.Query) != 0 {
				t.Fatal("ordinary directory accidentally gained query semantics")
			}
		})
	}
}

func TestSavedResultSourceAmbiguityAndNonEvidenceArePreserved(t *testing.T) {
	store := newSourceStore("")
	id := savedResultIDForTest("c")
	first := savedPageSourceForTest(store, "first", "https://one.example/report")
	same := savedPageSourceForTest(store, "same-artifact", first.Locator)
	second := savedPageSourceForTest(store, "other", "https://two.example/report")
	store.bindResult(id, first)
	store.bindResult(id, same)
	read := tools.ToolResultReadDetails{ResultID: id, View: "text"}
	if origin, handled := store.restoredResultSource(read); !handled || origin != first {
		t.Fatal("identical source content was falsely made ambiguous")
	}
	store.bindResult(id, second)
	store.bindResult(id, first)
	if _, handled := store.restoredResultSource(read); handled {
		t.Fatal("a repeated binding erased a distinct-artifact ambiguity")
	}
	local := store.Next(tools.ToolResultReadName, read, "read-ambiguous", "saved bytes", nil)
	if local == nil || local.Type != SourceTool || local.SavedResult == nil || !local.SavedResult.OriginAmbiguous || local.SavedResult.Origin != nil {
		t.Fatalf("ambiguous content was attributed to a selected webpage: %+v", local)
	}

	nonEvidence := savedResultIDForTest("d")
	store.bindResult(nonEvidence, nil)
	before := len(store.All())
	for _, view := range []string{"text", "json", "query", "search", "summary", "native"} {
		got := store.Next(tools.ToolResultReadName, tools.ToolResultReadDetails{ResultID: nonEvidence, View: view}, "non-evidence", "todo or bookmark metadata", nil)
		if got != nil {
			t.Fatalf("%s recovery turned process metadata into evidence: %+v", view, got)
		}
	}
	if len(store.All()) != before {
		t.Fatal("non-evidence reads spent source labels")
	}
	store.bindResult("R-invalid", first)
	if _, found := store.resultOrigin("R-invalid"); found {
		t.Fatal("malformed result ID entered provenance mappings")
	}
}

// Use the protected reader and a committed observation, rather than manufacturing
// a search-shaped result. The nonzero offset is a match ordinal, not a byte range.
func savedSearchResultForTest(t *testing.T) (*tools.Registry, tools.Result, json.RawMessage, string) {
	t.Helper()
	registry, _ := budgetRegistryForTest(t)
	text := "unrelated preface\ntrace-目标 first observation\nordinary line\ntrace-目标 second observation\ntrace-目标 final observation\n"
	prepared := registry.PrepareResult(context.Background(), "mcp_logs_get_logs", tools.Result{Text: text}, nil)
	if !sourceResultID(prepared.ResultID) {
		t.Fatal("original observation was not committed")
	}
	args, err := json.Marshal(map[string]any{
		"result_id": prepared.ResultID, "view": "search", "pattern": "trace-目标",
		"context_lines": 0, "offset": 1, "limit": 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	reader, found := registry.Get(tools.ToolResultReadName)
	if !found {
		t.Fatal("protected reader is unavailable")
	}
	result, err := reader.Execute(context.Background(), args)
	if err != nil {
		t.Fatalf("real saved-text search failed: %v", err)
	}
	read, ok := result.Details.(tools.ToolResultReadDetails)
	if !ok || read.View != "search" || read.ResultID != prepared.ResultID || read.Offset != 1 || read.Pointer != "" || read.Total != 3 {
		t.Fatalf("real search did not return match-ordinal details: %+v", result.Details)
	}
	if !strings.Contains(result.Text, "second observation") || strings.Contains(result.Text, "first observation") {
		t.Fatalf("real search did not select the expected match: %s", result.Text)
	}
	return registry, result, args, text
}

func savedSearchHistoryForTest(t *testing.T, result tools.Result, args json.RawMessage, source *Source) []llm.Message {
	t.Helper()
	block := llm.Block{Type: llm.BlockToolResult, ToolUseID: "saved-search", Text: result.Text, Details: marshalDetails(result.Details)}
	if source != nil {
		block.Source = marshalSource(source)
		block.Text = fmt.Sprintf("[cite:%s | tool_result | stored search]\n%s", source.Label, result.Text)
	}
	return []llm.Message{
		{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockToolUse, ID: block.ToolUseID, Name: tools.ToolResultReadName, Input: args}}},
		{Role: llm.RoleUser, Content: []llm.Block{block}},
	}
}

func TestSavedResultSourceRealSearchRetainsOriginAsLocalView(t *testing.T) {
	registry, result, args, text := savedSearchResultForTest(t)
	a := New(Config{Client: &fakeClient{}, Registry: registry, Citations: true})
	read := result.Details.(tools.ToolResultReadDetails)
	original := a.sources.Next("mcp_logs_get_logs", tools.MCPDetails{Server: "logs", Tool: "get_logs"}, "original", text, nil)
	original.Time = 1234
	before, _ := json.Marshal(original)
	a.sources.bindResult(read.ResultID, original)
	local := a.sources.Next(tools.ToolResultReadName, read, "saved-search", result.Text, args)
	if local == nil || local == original || local.Label == original.Label || local.CallID != "saved-search" || local.Type != SourceTool || local.Type == original.Type {
		t.Fatalf("search was stamped as a fresh original observation: %+v", local)
	}
	ref := local.SavedResult
	if ref == nil || ref.View != "search" || ref.ResultID != read.ResultID || ref.Pointer != "" || ref.Origin == nil || ref.Origin == original || ref.Origin.SavedResult != nil || !sameResultSource(ref.Origin, original) || ref.Origin.Time != 1234 {
		t.Fatalf("search lost its detached original artifact identity: %+v", ref)
	}
	if string(ref.Query) != string(args) {
		t.Fatalf("validated search arguments were not retained: %s", ref.Query)
	}
	history := savedSearchHistoryForTest(t, result, args, local)
	if _, recovered := a.recoveredResultRead(history[0].Content[0], history[1].Content[0], original); recovered {
		t.Fatal("search match ordinal was accepted as raw text recovery")
	}
	if _, recovered := a.recoveredResultRead(history[0].Content[0], history[1].Content[0], local); recovered {
		t.Fatal("derived local search stamp was accepted as raw recovery")
	}
	if source, handled := a.sources.restoredResultSource(read); handled || source != nil {
		t.Fatal("search reused the original raw observation stamp")
	}
	// Ordinary raw recovery still resolves to the unchanged original stamp.
	reader, _ := registry.Get(tools.ToolResultReadName)
	rawArgs := json.RawMessage(`{"result_id":"` + read.ResultID + `","view":"text","limit":32768}`)
	raw, err := reader.Execute(context.Background(), rawArgs)
	if err != nil || a.sources.Next(tools.ToolResultReadName, raw.Details, "raw-read", raw.Text, rawArgs) != original {
		t.Fatalf("search changed later raw recovery attribution: %v", err)
	}
	after, _ := json.Marshal(original)
	if string(before) != string(after) {
		t.Fatal("local search changed the original observation or its timestamp")
	}
	args[0] = ' '
	if ref.Query[0] != '{' {
		t.Fatal("search receipt aliases caller-owned argument bytes")
	}
}

func TestSavedResultSourceRealSearchResumeRetainsParentAndRewind(t *testing.T) {
	registry, result, args, text := savedSearchResultForTest(t)
	read := result.Details.(tools.ToolResultReadDetails)
	store := newSourceStore("")
	original := store.Next("mcp_logs_get_logs", tools.MCPDetails{Server: "logs", Tool: "get_logs"}, "original", text, nil)
	original.Time = 1234
	store.bindResult(read.ResultID, original)
	local := store.Next(tools.ToolResultReadName, read, "saved-search", result.Text, args)
	history := savedSearchHistoryForTest(t, result, args, local)
	resumed := New(Config{Client: &fakeClient{}, Registry: registry, Citations: true})
	resumed.SetMessages(history)
	binding, found := resumed.sources.resultOrigin(read.ResultID)
	if !found || binding.ambiguous || binding.source == nil || !sameResultSource(binding.source, original) || binding.source.Time != 1234 || binding.source.CallID != "original" {
		t.Fatalf("kept search lost its parent after resume: %+v / %v", binding, found)
	}
	kept, found := resumed.sources.Get(local.Label)
	if !found || kept.Type != SourceTool || kept.SavedResult == nil || kept.SavedResult.View != "search" || string(kept.SavedResult.Query) != string(args) {
		t.Fatalf("kept search lost its local view contract: %+v", kept)
	}
	if _, found := resumed.sources.Get(original.Label); !found {
		t.Fatal("resumed parent is unreachable by its original citation label")
	}
	if _, recovered := resumed.recoveredResultRead(history[0].Content[0], history[1].Content[0], binding.source); recovered {
		t.Fatal("resume promoted local search into a raw recovery stamp")
	}
	next := resumed.sources.Next(tools.ToolResultReadName, read, "search-again", result.Text, args)
	if next == nil || next.SavedResult == nil || next.SavedResult.Origin == nil || !sameResultSource(next.SavedResult.Origin, original) {
		t.Fatal("subsequent search lost the restored parent")
	}
	resumed.SetMessages(nil)
	if _, found := resumed.sources.resultOrigin(read.ResultID); found || len(resumed.sources.All()) != 0 {
		t.Fatal("rewind retained an abandoned saved search or its origin")
	}
}

func TestSavedResultSourceRealSearchPreservesNilAndAmbiguousOrigins(t *testing.T) {
	_, result, args, _ := savedSearchResultForTest(t)
	read := result.Details.(tools.ToolResultReadDetails)
	t.Run("non-evidence", func(t *testing.T) {
		store := newSourceStore("")
		store.bindResult(read.ResultID, nil)
		if local := store.Next(tools.ToolResultReadName, read, "saved-search", result.Text, args); local != nil || len(store.All()) != 0 {
			t.Fatalf("search promoted non-evidence into a source: %+v", local)
		}
		resumed := newSourceStore("")
		resumed.observe(savedSearchHistoryForTest(t, result, args, nil))
		if binding, found := resumed.resultOrigin(read.ResultID); !found || binding.ambiguous || binding.source != nil {
			t.Fatalf("resume lost explicit nil search provenance: %+v / %v", binding, found)
		}
		if local := resumed.Next(tools.ToolResultReadName, read, "again", result.Text, args); local != nil {
			t.Fatal("resumed search promoted non-evidence into a source")
		}
	})
	t.Run("ambiguous", func(t *testing.T) {
		store := newSourceStore("")
		store.bindResult(read.ResultID, savedPageSourceForTest(store, "one", "https://one.example/log"))
		store.bindResult(read.ResultID, savedPageSourceForTest(store, "two", "https://two.example/log"))
		local := store.Next(tools.ToolResultReadName, read, "saved-search", result.Text, args)
		if local == nil || local.Type != SourceTool || local.SavedResult == nil || !local.SavedResult.OriginAmbiguous || local.SavedResult.Origin != nil {
			t.Fatalf("search selected one ambiguous original artifact: %+v", local)
		}
		resumed := newSourceStore("")
		resumed.observe(savedSearchHistoryForTest(t, result, args, local))
		if binding, found := resumed.resultOrigin(read.ResultID); !found || !binding.ambiguous || binding.source != nil {
			t.Fatalf("resume selected one ambiguous search origin: %+v / %v", binding, found)
		}
	})
}

func TestSavedResultSourceParallelBindingsCannotChooseOneOrigin(t *testing.T) {
	store := newSourceStore("")
	id := savedResultIDForTest("e")
	origins := []*Source{
		savedPageSourceForTest(store, "one", "https://one.example/"),
		savedPageSourceForTest(store, "two", "https://two.example/"),
	}
	var group sync.WaitGroup
	for i := range 64 {
		group.Add(1)
		go func(i int) {
			defer group.Done()
			store.bindResult(id, origins[i%2])
			store.resultOrigin(id)
		}(i)
	}
	group.Wait()
	if binding, ok := store.resultOrigin(id); !ok || !binding.ambiguous || binding.source != nil {
		t.Fatalf("parallel attribution arbitrarily selected an origin: %+v / %v", binding, ok)
	}
}

func TestSavedResultSourceObserveRestoresKeptReadsAndRewind(t *testing.T) {
	for _, view := range []string{"text", "json", "query"} {
		t.Run(view, func(t *testing.T) {
			store := newSourceStore("")
			id := savedResultIDForTest("f")
			original := savedPageSourceForTest(store, "original", "https://example.com/report")
			store.bindResult(id, original)
			read := tools.ToolResultReadDetails{ResultID: id, OriginalTool: "browser", View: view, Total: 100, Complete: true}
			kept := original
			if view == "query" {
				kept = store.Next(tools.ToolResultReadName, read, "kept-read", "aggregate result", json.RawMessage(`{"view":"query"}`))
			}
			history := savedReadHistoryForTest(t, kept, read)
			restored := newSourceStore("")
			restored.observe(history)
			binding, found := restored.resultOrigin(id)
			if !found || binding.ambiguous || binding.source == nil || !sameResultSource(binding.source, original) {
				t.Fatalf("kept %s read lost original provenance on resume: %+v / %v", view, binding, found)
			}
			if _, found := restored.Get(original.Label); !found {
				t.Fatal("restored parent stamp is not reachable by citation label")
			}
			restored.observe(nil)
			if _, found := restored.resultOrigin(id); found || len(restored.All()) != 0 {
				t.Fatal("rewind kept an abandoned result/source binding")
			}
		})
	}
	store := newSourceStore("")
	id := savedResultIDForTest("0")
	store.observe(savedReadHistoryForTest(t, nil, tools.ToolResultReadDetails{ResultID: id, View: "summary"}))
	if binding, found := store.resultOrigin(id); !found || binding.ambiguous || binding.source != nil {
		t.Fatalf("kept non-evidence read lost nil provenance: %+v / %v", binding, found)
	}
}

func TestSavedResultSourceObserveKeepsAmbiguity(t *testing.T) {
	store := newSourceStore("")
	id := savedResultIDForTest("1")
	first := savedPageSourceForTest(store, "one", "https://one.example/")
	second := savedPageSourceForTest(store, "two", "https://two.example/")
	blocks := []llm.Block{sourceResultBlock(t, *first), sourceResultBlock(t, *second)}
	for i := range blocks {
		blocks[i].ResultID = id
	}
	store.observe([]llm.Message{{Role: llm.RoleUser, Content: blocks}})
	if binding, found := store.resultOrigin(id); !found || !binding.ambiguous {
		t.Fatalf("resume selected one of multiple artifact origins: %+v", binding)
	}
}

func TestSavedResultSourceNativeRecoveryCountsMaterialWithoutNewTimestamp(t *testing.T) {
	registry, _ := budgetRegistryForTest(t)
	a := New(Config{Client: &fakeClient{}, Registry: registry, Citations: true})
	id := savedResultIDForTest("2")
	original := savedPageSourceForTest(a.sources, "original", "https://example.com/report")
	a.sources.bindResult(id, original)
	read := tools.ToolResultReadDetails{ResultID: id, OriginalTool: "browser", View: "text", Total: 10}
	history := savedReadHistoryForTest(t, original, read)
	call, result := history[0].Content[0], history[1].Content[0]
	labels := a.citationReadLabels([]llm.Block{call}, []llm.Block{result})
	if !reflect.DeepEqual(labels, []string{original.Label}) {
		t.Fatalf("trusted raw recovery did not count the original source label: %v", labels)
	}
	read.View, read.Omitted = "json", true
	result.Details = marshalDetails(read)
	if labels := a.citationReadLabels([]llm.Block{call}, []llm.Block{result}); len(labels) != 0 {
		t.Fatalf("directory-only read forced evidence citations: %v", labels)
	}
	read.View, read.Omitted, read.OriginalIsError = "text", false, true
	result.Details = marshalDetails(read)
	if labels := a.citationReadLabels([]llm.Block{call}, []llm.Block{result}); len(labels) != 0 {
		t.Fatalf("successful read of an original failure became a material read: %v", labels)
	}
	a.registry = tools.NewRegistry(&fakeTool{name: tools.ToolResultReadName})
	read.OriginalIsError = false
	result.Details = marshalDetails(read)
	if _, trusted := a.recoveredResultRead(call, result, original); trusted {
		t.Fatal("a same-named non-native tool gained recovery provenance")
	}
}
