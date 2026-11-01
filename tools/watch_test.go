package tools

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type fakeWatchBackend struct {
	calls []string
	err   error
}

func (b *fakeWatchBackend) call(kind string) (any, error) {
	b.calls = append(b.calls, kind)
	return map[string]any{"accepted": true, "kind": kind}, b.err
}
func (b *fakeWatchBackend) Watch(context.Context, json.RawMessage) (any, error) {
	return b.call("watch")
}
func (b *fakeWatchBackend) Test(context.Context, json.RawMessage) (any, error) { return b.call("test") }
func (b *fakeWatchBackend) Decide(context.Context, json.RawMessage) (any, error) {
	return b.call("decide")
}

func TestWatchDispatchAndReceipts(t *testing.T) {
	b := &fakeWatchBackend{}
	tool := &Watch{Backend: b}
	for _, raw := range []string{`{"action":"create","title":"通用哨兵","task":"判断任意来源变化","mode":"poll","script_path":"watchers/observe.py","args":["--custom"],"enabled":false}`, `{"action":"status","watch_id":"w_1"}`, `{"action":"retry","watch_id":"w_1"}`} {
		r, e := tool.Execute(context.Background(), json.RawMessage(raw))
		if e != nil {
			t.Fatal(e)
		}
		var v map[string]any
		if e = json.Unmarshal([]byte(r.Text), &v); e != nil || v["kind"] != "watch" {
			t.Fatalf("result=%#v %v", r, e)
		}
		d, ok := r.Details.(map[string]any)
		if !ok || d["type"] != "watch" {
			t.Fatalf("details=%#v", r.Details)
		}
	}
	if len(b.calls) != 3 {
		t.Fatalf("calls=%v", b.calls)
	}
}
func TestWatchRejectsInvalidAuthorityAndScriptBeforeBackend(t *testing.T) {
	base := map[string]any{"action": "create", "title": "watch", "task": "observe", "mode": "poll", "script_path": "observe.py"}
	cases := []string{`null`, `[]`, `{}`, `{"action":"unknown"}`, `{"action":"list","owner_id":"another"}`, `{"action":"list","enabled":true}`, `{"action":"status","watch_id":"w_1","workspace":"/private"}`, `{"action":"status","watch_id":"../../another"}`, `{"action":"resume","watch_id":"w_1","network":true}`, `{"action":"retry"}`, `{"action":"list"} {}`}
	mutations := []struct {
		k string
		v any
	}{
		{"owner_id", "other"}, {"agent_id", "other"}, {"batch_id", "other"}, {"script_dir", "/private"},
		{"script_path", "../x.py"}, {"script_path", "/tmp/x.py"}, {"script_path", "./x.py"}, {"script_path", `a\x.py`}, {"script_path", "C:/x.py"}, {"script_path", ""}, {"script_path", "x\x00.py"},
		{"mode", "http"}, {"task", ""}, {"title", strings.Repeat("长", 121)}, {"title", "line\nline"},
		{"interval_seconds", -1}, {"interval_seconds", 604801}, {"timeout_seconds", 301}, {"max_turns", 301}, {"debounce_ms", 10001},
		{"args", []string{strings.Repeat("a", 4097)}}, {"args", []string{"a\x00b"}}, {"args", make([]string, 33)}, {"enabled", nil}, {"network", "true"},
	}
	for _, m := range mutations {
		v := map[string]any{}
		for k, x := range base {
			v[k] = x
		}
		v[m.k] = m.v
		r, _ := json.Marshal(v)
		cases = append(cases, string(r))
	}
	b := &fakeWatchBackend{}
	tool := &Watch{Backend: b}
	for _, raw := range cases {
		if _, e := tool.Execute(context.Background(), json.RawMessage(raw)); e == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	if len(b.calls) != 0 {
		t.Fatalf("invalid requests reached backend: %v", b.calls)
	}
}
func TestWatchReadOnlyAndParallelAreArgumentSensitive(t *testing.T) {
	tool := &Watch{}
	for _, raw := range []string{`{"action":"list"}`, `{"action":"status","watch_id":"watch_1"}`} {
		if !WatchReadOnly(json.RawMessage(raw)) || tool.ExecutionModeForArgs(json.RawMessage(raw)) != Parallel {
			t.Fatalf("read rejected: %s", raw)
		}
	}
	for _, raw := range []string{`null`, `{"action":"list","network":true}`, `{"action":"pause","watch_id":"w_1"}`, `{"action":"resume","watch_id":"w_1"}`, `{"action":"retry","watch_id":"w_1"}`, `{"action":"remove","watch_id":"w_1"}`} {
		if WatchReadOnly(json.RawMessage(raw)) || tool.ExecutionModeForArgs(json.RawMessage(raw)) != Sequential {
			t.Fatalf("unsafe read: %s", raw)
		}
	}
	if (&WatchTest{}).ExecutionMode() != Sequential || (&WatchDecide{}).ExecutionMode() != Sequential {
		t.Fatal("test and decide must serialize")
	}
}
func TestWatchTestCannotOverrideSavedStateOrSpecification(t *testing.T) {
	b := &fakeWatchBackend{}
	tool := &WatchTest{Backend: b}
	for _, raw := range []string{`{"watch_id":"w_1"}`, `{"watch_id":"w_1","timeout_seconds":5}`, `{"script_path":"watchers/custom.sh","mode":"stream","args":["test"],"timeout_seconds":2}`} {
		if _, e := tool.Execute(context.Background(), json.RawMessage(raw)); e != nil {
			t.Fatalf("%s: %v", raw, e)
		}
	}
	before := len(b.calls)
	for _, raw := range []string{`{}`, `{"watch_id":"w_1","network":false}`, `{"watch_id":"w_1","mode":"poll"}`, `{"watch_id":"w_1","script_path":"x.py"}`, `{"watch_id":"w_1","state":{"x":9}}`, `{"script_path":"x.py","mode":"poll","timeout_seconds":31}`, `{"script_path":"x.py","mode":"poll","enabled":true}`} {
		if _, e := tool.Execute(context.Background(), json.RawMessage(raw)); e == nil {
			t.Errorf("accepted override: %s", raw)
		}
	}
	if len(b.calls) != before {
		t.Fatal("invalid dry run reached backend")
	}
}
func TestWatchDecideIsCurrentBatchOnlyAndDurableIntent(t *testing.T) {
	b := &fakeWatchBackend{}
	tool := &WatchDecide{Backend: b}
	for _, raw := range []string{`{"decision":"ignore"}`, `{"decision":"report","summary":"调查完成"}`, `{"decision":"defer","summary":"等待来源","defer_seconds":60}`} {
		if _, e := tool.Execute(context.Background(), json.RawMessage(raw)); e != nil {
			t.Fatalf("%s: %v", raw, e)
		}
	}
	before := len(b.calls)
	for _, raw := range []string{`{"decision":"investigate"}`, `{"decision":"report"}`, `{"decision":"defer"}`, `{"decision":"defer","defer_seconds":-1}`, `{"decision":"defer","defer_seconds":604801}`, `{"decision":"report","summary":"done","defer_seconds":60}`, `{"decision":"ignore","batch_id":"other"}`, `{"decision":"ignore","watch_id":"other"}`, `{"decision":"ignore","agent_id":"other"}`} {
		if _, e := tool.Execute(context.Background(), json.RawMessage(raw)); e == nil {
			t.Errorf("accepted decision: %s", raw)
		}
	}
	if len(b.calls) != before {
		t.Fatal("invalid decision reached backend")
	}
}
func TestWatchUnavailableCancellationAndBackendFailure(t *testing.T) {
	var typedNil *fakeWatchBackend
	for _, b := range []WatchBackend{nil, typedNil} {
		if _, e := (&Watch{Backend: b}).Execute(context.Background(), json.RawMessage(`{"action":"list"}`)); e == nil {
			t.Fatal("nil backend accepted")
		}
	}
	b := &fakeWatchBackend{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := (&Watch{Backend: b}).Execute(ctx, json.RawMessage(`{"action":"list"}`)); !errors.Is(e, context.Canceled) {
		t.Fatalf("cancel=%v", e)
	}
	if len(b.calls) != 0 {
		t.Fatal("canceled call reached backend")
	}
	boom := errors.New("storage unavailable")
	b.err = boom
	r, e := (&Watch{Backend: b}).Execute(context.Background(), json.RawMessage(`{"action":"list"}`))
	if !errors.Is(e, boom) || r.Text != "" || r.Details != nil {
		t.Fatalf("failure looked successful: %#v %v", r, e)
	}
}
func TestWatchSchemasAndOptionalFlags(t *testing.T) {
	list := NewWatchTools(&fakeWatchBackend{})
	names := []string{}
	for _, tool := range list {
		names = append(names, tool.Name())
		s := tool.InputSchema()
		if s["additionalProperties"] != false || reflect.ValueOf(s["required"]).IsNil() {
			t.Fatalf("invalid schema %s", tool.Name())
		}
	}
	if !reflect.DeepEqual(names, []string{WatchName, WatchTestName, WatchDecideName}) {
		t.Fatalf("names=%v", names)
	}
	var a WatchRequest
	if e := json.Unmarshal([]byte(`{"action":"create"}`), &a); e != nil || a.Network != nil || a.Enabled != nil {
		t.Fatalf("omission lost: %#v %v", a, e)
	}
	if e := json.Unmarshal([]byte(`{"action":"create","network":false,"enabled":true}`), &a); e != nil || a.Network == nil || *a.Network || a.Enabled == nil || !*a.Enabled {
		t.Fatalf("flags lost: %#v %v", a, e)
	}
}
