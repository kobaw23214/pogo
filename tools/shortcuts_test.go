package tools

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/inputref"
	"github.com/yosukeno/pogo/shortcuts"
)

type shortcutBackendFixture struct {
	searchCalls, readCalls, createCalls, saves int
	search                                     shortcuts.SearchRequest
	read                                       shortcuts.ReadRequest
	requests                                   map[string]shortcuts.CreateRequest
	results                                    map[string]shortcuts.CreateResult
	failure                                    error
	invalidReceipt                             bool
}

func (f *shortcutBackendFixture) Search(_ context.Context, r shortcuts.SearchRequest) (shortcuts.SearchResult, error) {
	f.searchCalls++
	f.search = r
	return shortcuts.SearchResult{Entries: []shortcuts.ReferenceSummary{{RefID: "ref_host", Source: "shortcut", Kind: "shortcut", Title: "Existing"}}, Total: 1}, f.failure
}
func (f *shortcutBackendFixture) Read(_ context.Context, r shortcuts.ReadRequest) (shortcuts.ReadResult, error) {
	f.readCalls++
	f.read = r
	return shortcuts.ReadResult{Reference: shortcuts.ReferenceSummary{RefID: r.RefID, Source: "shortcut", Kind: "shortcut", Title: "Existing"}, Parts: []inputref.Part{{Type: "text", Text: "saved text"}}}, f.failure
}
func (f *shortcutBackendFixture) Create(ctx context.Context, r shortcuts.CreateRequest) (shortcuts.CreateResult, error) {
	f.createCalls++
	if f.failure != nil {
		return shortcuts.CreateResult{}, f.failure
	}
	if f.results == nil {
		f.results = map[string]shortcuts.CreateResult{}
		f.requests = map[string]shortcuts.CreateRequest{}
	}
	if old, ok := f.results[r.RequestID]; ok {
		if !reflect.DeepEqual(r, f.requests[r.RequestID]) {
			return shortcuts.CreateResult{}, errors.New("request_id conflict")
		}
		old.Reused = true
		return old, nil
	}
	compiled, err := shortcuts.Compile(ctx, "fixture", r.Graph, func(context.Context, string) (shortcuts.Reference, error) {
		return shortcuts.Reference{}, errors.New("unavailable")
	})
	if err != nil {
		return shortcuts.CreateResult{}, err
	}
	compiled.Root.ParentID = r.ParentID
	out := shortcuts.CreateResult{AgentID: "ag_fixture", Shortcut: compiled.Root, RequestID: r.RequestID, Revision: 1}
	if f.invalidReceipt {
		out.RequestID = "wrong"
	}
	f.saves++
	f.requests[r.RequestID] = r
	f.results[r.RequestID] = out
	return out, nil
}
func shortcutRequestJSON(t *testing.T, r shortcuts.CreateRequest) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
func shortcutCreateFixture() shortcuts.CreateRequest {
	return shortcuts.CreateRequest{RequestID: "request_1", Graph: shortcuts.Graph{RootID: "root", Nodes: []shortcuts.Node{{LocalID: "root", Title: "Root", Parts: []shortcuts.Part{{Kind: "text", Text: "before "}, {Kind: "node", NodeID: "child"}, {Kind: "text", Text: " after"}}}, {LocalID: "child", Title: "Child", Parts: []shortcuts.Part{{Kind: "text", Text: "nested body"}}}}}}
}

func TestShortcutToolsSeparateReadAndWrite(t *testing.T) {
	b := &shortcutBackendFixture{}
	search, read, create := &ShortcutSearch{Backend: b}, &ShortcutRead{Backend: b}, &ShortcutCreate{Backend: b}
	if !search.IsReadOnly() || !read.IsReadOnly() || create.IsReadOnly() || search.ExecutionMode() != Parallel || read.ExecutionMode() != Parallel || create.ExecutionMode() != Sequential {
		t.Fatal("incorrect capability or batch mode")
	}
	got, err := search.Execute(context.Background(), json.RawMessage(`{"query":"Existing"}`))
	if err != nil || b.search.Source != "all" || b.search.Limit != 30 || b.createCalls != 0 || !strings.Contains(got.Text, "ref_host") {
		t.Fatal(err, got)
	}
	got, err = read.Execute(context.Background(), json.RawMessage(`{"ref_id":"ref_host"}`))
	if err != nil || b.read.RefID != "ref_host" || b.createCalls != 0 || !strings.Contains(got.Text, "saved text") {
		t.Fatal(err, got)
	}
	for _, tool := range []Tool{search, read, create} {
		raw, _ := json.Marshal(tool.InputSchema())
		if strings.Contains(string(raw), `"$ref"`) || strings.Contains(string(raw), `"oneOf"`) {
			t.Fatal("recursive/composition schema", string(raw))
		}
	}
}

func TestShortcutCreateSavedReceiptAndIdempotentRetry(t *testing.T) {
	b := &shortcutBackendFixture{}
	tool := &ShortcutCreate{Backend: b}
	request := shortcutCreateFixture()
	raw := shortcutRequestJSON(t, request)
	first, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	details, ok := first.Details.(ShortcutDetails)
	if !ok || details.Type != "shortcut" || details.Action != "create" || !details.Saved || details.AgentID != "ag_fixture" || details.Shortcut == nil || details.Shortcut.ID == "root" || details.Shortcut.Parts[1].Parts[0].Text != "nested body" {
		t.Fatalf("invalid UI receipt: %+v", first.Details)
	}
	if strings.Contains(first.Text, "nested body") || !strings.Contains(first.Text, `"saved":true`) {
		t.Fatal("receipt duplicated authored body or omitted save", first.Text)
	}
	retry, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	repeat := retry.Details.(ShortcutDetails)
	if b.saves != 1 || !repeat.Reused || repeat.Shortcut.ID != details.Shortcut.ID {
		t.Fatal("retry duplicated root")
	}
	details.Shortcut.Parts[1].Parts[0].Text = "UI mutated"
	if b.results[request.RequestID].Shortcut.Parts[1].Parts[0].Text != "nested body" {
		t.Fatal("UI receipt aliases private store")
	}
	request.Nodes[1].Parts[0].Text = "changed"
	if out, err := tool.Execute(context.Background(), shortcutRequestJSON(t, request)); err == nil || out.Details != nil || b.saves != 1 {
		t.Fatal("different payload reused idempotency key")
	}
}

func TestShortcutCreateRejectsBeforeHostAndRetainsFailure(t *testing.T) {
	good := string(shortcutRequestJSON(t, shortcutCreateFixture()))
	cases := map[string]string{
		"unknown identity":     strings.TrimSuffix(good, "}") + `,"agent_id":"other"}`,
		"unknown nested field": strings.Replace(good, `"title":"Root"`, `"title":"Root","path":"/etc/passwd"`, 1),
		"trailing JSON":        good + ` {}`,
		"null nodes":           `{"request_id":"x","root_id":"r","nodes":null}`,
		"missing request id":   `{"root_id":"r","nodes":[]}`,
		"reserved folder":      strings.TrimSuffix(good, "}") + `,"parent_id":"@information"}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			b := &shortcutBackendFixture{}
			out, err := (&ShortcutCreate{Backend: b}).Execute(context.Background(), json.RawMessage(raw))
			if err == nil || b.createCalls != 0 || out.Details != nil {
				t.Fatal("invalid args reached host or rendered saved card", err)
			}
		})
	}
	t.Run("cycle", func(t *testing.T) {
		b := &shortcutBackendFixture{}
		r := shortcutCreateFixture()
		r.Nodes[1].Parts = []shortcuts.Part{{Kind: "node", NodeID: "root"}}
		out, err := (&ShortcutCreate{Backend: b}).Execute(context.Background(), shortcutRequestJSON(t, r))
		if err == nil || b.createCalls != 0 || out.Details != nil {
			t.Fatal("cycle reached host")
		}
	})
	t.Run("backend denied", func(t *testing.T) {
		b := &shortcutBackendFixture{failure: errors.New("reference unavailable")}
		out, err := (&ShortcutCreate{Backend: b}).Execute(context.Background(), json.RawMessage(good))
		if err == nil || err.Error() != "reference unavailable" || out.Details != nil {
			t.Fatal("backend error became success")
		}
	})
	t.Run("invalid host receipt", func(t *testing.T) {
		b := &shortcutBackendFixture{invalidReceipt: true}
		out, err := (&ShortcutCreate{Backend: b}).Execute(context.Background(), json.RawMessage(good))
		if err == nil || out.Details != nil {
			t.Fatal("invalid host receipt became UI success")
		}
	})
	t.Run("canceled or unavailable", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		b := &shortcutBackendFixture{}
		if _, err := (&ShortcutCreate{Backend: b}).Execute(ctx, json.RawMessage(good)); !errors.Is(err, context.Canceled) || b.createCalls != 0 {
			t.Fatal(err)
		}
		if _, err := (&ShortcutRead{}).Execute(context.Background(), json.RawMessage(`{"ref_id":"x"}`)); err == nil {
			t.Fatal("missing backend accepted")
		}
	})
}

func TestShortcutTypedNilBackendFailsClosed(t *testing.T) {
	var backend *shortcutBackendFixture
	if shortcutBackendAvailable(backend) {
		t.Fatal("typed nil backend was treated as a capability")
	}
	for _, tool := range []Tool{&ShortcutSearch{Backend: backend}, &ShortcutRead{Backend: backend}, &ShortcutCreate{Backend: backend}} {
		if _, err := tool.Execute(context.Background(), json.RawMessage(`{}`)); err == nil {
			t.Fatal("typed nil accepted")
		}
	}
}
