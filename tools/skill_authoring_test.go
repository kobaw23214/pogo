package tools

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/skillauthor"
	"github.com/yosukeno/pogo/skillruntime"
)

type skillAuthorFixture struct {
	searchCalls, readCalls, saveCalls int
	search                            SkillSearchRequest
	read                              SkillReadRequest
	saved                             skillauthor.SaveRequest
	err                               error
	badReceipt                        bool
	badSource                         bool
	tooLargeRead                      bool
}

func authoredSummary() SkillSummary {
	return SkillSummary{ID: "as_0123456789abcdef0123456789abcdef", Name: "sample-skill", Description: "Use for sample tasks.", Revision: strings.Repeat("a", 64), Version: 1, Editable: true, Enabled: true, Source: "authored"}
}
func (f *skillAuthorFixture) Search(_ context.Context, in SkillSearchRequest) (SkillSearchResult, error) {
	f.searchCalls++
	f.search = in
	return SkillSearchResult{Skills: []SkillSummary{authoredSummary()}, Offset: in.Offset, Total: 1}, f.err
}
func (f *skillAuthorFixture) Read(_ context.Context, in SkillReadRequest) (SkillReadResult, error) {
	f.readCalls++
	f.read = in
	body := "# Sample"
	if f.tooLargeRead {
		body = strings.Repeat("x", maxSkillReadBytes+1)
	}
	return SkillReadResult{Skill: authoredSummary(), Files: []SkillFileInfo{{Path: "SKILL.md", SizeBytes: 8}}, Path: in.Path, Content: body, Revision: authoredSummary().Revision}, f.err
}
func (f *skillAuthorFixture) Save(_ context.Context, in skillauthor.SaveRequest) (SkillSaveResult, error) {
	f.saveCalls++
	f.saved = in
	out := SkillSaveResult{Type: "skill_authoring", Action: "save", Saved: true, AgentID: "ag_test", Skill: authoredSummary(), RequestID: in.RequestID, Enabled: true}
	if f.badReceipt {
		out.BehaviorTested = true
	}
	if f.badSource {
		out.Skill.Source = "embedded"
	}
	return out, f.err
}
func skillSaveArgs() json.RawMessage {
	return json.RawMessage(`{"request_id":"request-1","files":[{"path":"SKILL.md","content":"---\nname: sample-skill\ndescription: Use for sample tasks.\n---\n\nFollow the user's sample task.\n"}]}`)
}

func TestSkillAuthorReadDefaultsAndBoundaries(t *testing.T) {
	b := &skillAuthorFixture{}
	search := &SkillSearch{Backend: b}
	read := &RuntimeSkillRead{Backend: b}
	if !search.IsReadOnly() || !read.IsReadOnly() || search.ExecutionMode() != Parallel || read.ExecutionMode() != Sequential {
		t.Fatal("incorrect read capability")
	}
	if _, err := search.Execute(context.Background(), json.RawMessage(`{}`)); err != nil || b.search.Limit != 30 {
		t.Fatal(b.search, err)
	}
	if out, err := read.Execute(context.Background(), json.RawMessage(`{"skill":"sample-skill"}`)); err != nil || b.read.Path != "SKILL.md" || !strings.Contains(out.Text, `"revision"`) {
		t.Fatal(out, err)
	}
	for _, args := range []string{`{"skill":"x","path":"../SKILL.md"}`, `{"skill":"x","path":"/etc/passwd"}`, `{"skill":"x","path":"references/../../x"}`, `{"skill":"x","path":"references\\x.md"}`, `{"skill":"x","agent_id":"other"}`, `{"skill":"x"} {}`} {
		before := b.readCalls
		if _, err := read.Execute(context.Background(), json.RawMessage(args)); err == nil || b.readCalls != before {
			t.Fatal("invalid read reached host", args, err)
		}
	}
	b.tooLargeRead = true
	if _, err := read.Execute(context.Background(), json.RawMessage(`{"skill":"x"}`)); err == nil {
		t.Fatal("unbounded read accepted")
	}
	if _, err := (&RuntimeSkillRead{}).Execute(context.Background(), json.RawMessage(`{"skill":"x"}`)); err == nil {
		t.Fatal("absent runtime reader accepted")
	}
}

func TestSkillAuthorSaveReceiptAndInvalidInput(t *testing.T) {
	b := &skillAuthorFixture{}
	save := &SkillSave{Backend: b}
	if save.IsReadOnly() || save.ExecutionMode() != Sequential {
		t.Fatal("save not mutating barrier")
	}
	out, err := save.Execute(context.Background(), skillSaveArgs())
	if err != nil {
		t.Fatal(err)
	}
	detail, ok := out.Details.(SkillSaveResult)
	if !ok || !detail.Saved || detail.BehaviorTested || detail.Type != "skill_authoring" || !strings.Contains(out.Text, `"behavior_tested":false`) {
		t.Fatal("false success/test claim", out)
	}
	for _, args := range []string{
		strings.TrimSuffix(string(skillSaveArgs()), "}") + `,"agent_id":"other"}`,
		strings.Replace(string(skillSaveArgs()), `"path":"SKILL.md"`, `"path":"SKILL.md","executable":true`, 1),
		`{"request_id":"x","skill_id":"as_x","files":[]}`,
		`{"request_id":"x","expected_revision":"old","files":[]}`,
		`{"request_id":"x","files":[{"path":"../SKILL.md","content":"x"}]}`,
		`{"request_id":"x","files":[{"path":"tools.json","content":"{}"}]}`,
		string(skillSaveArgs()) + ` {}`,
	} {
		before := b.saveCalls
		if out, err := save.Execute(context.Background(), json.RawMessage(args)); err == nil || b.saveCalls != before || out.Details != nil {
			t.Fatal("invalid save reached host", args, err)
		}
	}
	b.badReceipt = true
	if out, err := save.Execute(context.Background(), skillSaveArgs()); err == nil || out.Details != nil {
		t.Fatal("invalid host receipt accepted")
	}
	b.badReceipt = false
	b.badSource = true
	if out, err := save.Execute(context.Background(), skillSaveArgs()); err == nil || out.Details != nil {
		t.Fatal("read-only Skill source rendered as an authored save")
	}
	b.badSource = false
	b.err = errors.New("revision conflict")
	if out, err := save.Execute(context.Background(), skillSaveArgs()); err == nil || out.Details != nil {
		t.Fatal("host failure rendered saved")
	}
}

func TestSkillAuthorUpdateRequiresCanonicalIDAndBundleRevision(t *testing.T) {
	b := &skillAuthorFixture{}
	tool := &SkillSave{Backend: b}
	var request skillauthor.SaveRequest
	if err := json.Unmarshal(skillSaveArgs(), &request); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []struct{ id, revision string }{
		{"as_x", strings.Repeat("a", 64)},
		{authoredSummary().ID, "latest"},
		{authoredSummary().ID, strings.Repeat("A", 64)},
		{authoredSummary().ID, strings.Repeat("a", 63)},
	} {
		request.SkillID, request.ExpectedRevision = invalid.id, invalid.revision
		raw, _ := json.Marshal(request)
		if out, err := tool.Execute(context.Background(), raw); err == nil || out.Details != nil || b.saveCalls != 0 {
			t.Fatal("invalid update reached host", invalid, err)
		}
	}
	request.SkillID, request.ExpectedRevision = authoredSummary().ID, authoredSummary().Revision
	raw, _ := json.Marshal(request)
	if _, err := tool.Execute(context.Background(), raw); err != nil || b.saveCalls != 1 {
		t.Fatal("canonical CAS update rejected", err)
	}
}

func TestSkillAuthorCanceledAndTypedNilDoNotCallHost(t *testing.T) {
	var nilBackend *skillAuthorFixture
	if skillAuthorBackendAvailable(nilBackend) {
		t.Fatal("typed nil accepted")
	}
	for _, tool := range []Tool{&SkillSearch{Backend: nilBackend}, &SkillSave{Backend: nilBackend}, &RuntimeSkillRead{Backend: nilBackend}} {
		if _, err := tool.Execute(context.Background(), skillSaveArgs()); err == nil {
			t.Fatal("unavailable backend accepted")
		}
	}
	b := &skillAuthorFixture{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tool := range []Tool{&SkillSearch{Backend: b}, &SkillSave{Backend: b}, &RuntimeSkillRead{Backend: b}} {
		if _, err := tool.Execute(ctx, skillSaveArgs()); !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	}
	if b.searchCalls+b.readCalls+b.saveCalls != 0 {
		t.Fatal("canceled call reached host")
	}
}

func TestSkillAuthorRegistryDoesNotGrantThroughProjection(t *testing.T) {
	b := &skillAuthorFixture{}
	empty := []string{}
	all := []string{SkillSaveName}
	for _, readOnly := range []bool{false, true} {
		r := New(Options{Cwd: t.TempDir(), ReadOnly: readOnly, Enabled: &empty, SkillAuthor: b, Synth: []Tool{nameStub{name: SkillSaveName}, nameStub{name: SkillSearchName}, &RuntimeSkillRead{Session: &skillruntime.BoundSession{}}}, MCP: []Tool{nameStub{name: SkillReadName}, nameStub{name: SkillSaveName}}})
		if _, ok := r.Get(SkillSaveName); ok {
			t.Fatal("save bypassed selection")
		}
		reader, _ := r.Get(SkillReadName)
		if reader.(*RuntimeSkillRead).Backend != b {
			t.Fatal("host reader replaced")
		}
		count := 0
		for _, tool := range r.All() {
			if tool.Name() == SkillReadName {
				count++
			}
		}
		if count != 1 {
			t.Fatal("multiple readers", count)
		}
		next, apply, err := r.WithNativeSelection(&all, nil)
		if err != nil {
			t.Fatal(err)
		}
		apply()
		_, ok := next.Get(SkillSaveName)
		if ok == readOnly {
			t.Fatal("read-only or selection broken")
		}
		if _, ok := next.WithMCP([]Tool{nameStub{name: SkillSaveName}}).Get(SkillSearchName); !ok {
			t.Fatal("read missing after projection")
		}
		if !readOnly {
			saved, _ := next.Get(SkillSaveName)
			next, apply, err = next.WithNativeSelection(&empty, nil)
			if err != nil {
				t.Fatal(err)
			}
			apply()
			if _, ok := next.Get(SkillSaveName); ok {
				t.Fatal("save survived revoke")
			}
			next, apply, err = next.WithNativeSelection(&all, nil)
			if err != nil {
				t.Fatal(err)
			}
			apply()
			again, _ := next.Get(SkillSaveName)
			if saved != again {
				t.Fatal("host save instance changed")
			}
		}
		if got, _ := next.Get(SkillReadName); got != reader {
			t.Fatal("reader lost")
		}
	}
	without := New(Options{Cwd: t.TempDir(), Enabled: &empty, Synth: []Tool{nameStub{name: SkillSaveName}, nameStub{name: SkillReadName}, nameStub{name: SkillSearchName}}})
	for _, name := range []string{SkillSaveName, SkillReadName, SkillSearchName} {
		if _, ok := without.Get(name); ok {
			t.Fatal("invented host capability", name)
		}
	}
}

func TestSkillAuthorSchemasAreFlatAndStrict(t *testing.T) {
	for _, tool := range []Tool{&SkillSearch{}, &SkillSave{}, &RuntimeSkillRead{}} {
		s := tool.InputSchema()
		raw, _ := json.Marshal(s)
		if s["additionalProperties"] != false || strings.Contains(string(raw), `"oneOf"`) || strings.Contains(string(raw), `"$ref"`) || reflect.ValueOf(s["required"]).IsNil() {
			t.Fatal(tool.Name(), string(raw))
		}
	}
}

func TestSkillAuthorMCPRefreshCannotShadowOrInventHostTools(t *testing.T) {
	var projected []Tool
	for _, name := range []string{SkillSearchName, SkillReadName, SkillSaveName} {
		projected = append(projected, &MCPTool{wireName: name})
	}
	empty := []string{}
	for _, backend := range []SkillAuthorBackend{nil, &skillAuthorFixture{}} {
		r := New(Options{Cwd: t.TempDir(), Enabled: &empty, SkillAuthor: backend, MCP: projected})
		for _, phase := range []*Registry{r, r.WithMCP(projected)} {
			if _, ok := phase.Get(SkillSaveName); ok {
				t.Fatal("MCP granted a withheld host save")
			}
			for _, name := range []string{SkillSearchName, SkillReadName} {
				tool, ok := phase.Get(name)
				if ok != (backend != nil) {
					t.Fatal("MCP invented or removed host read", name)
				}
				if _, remote := tool.(*MCPTool); remote {
					t.Fatal("MCP replaced scoped document access", name)
				}
			}
		}
	}
}

func TestSkillAuthorTrustedRuntimeReaderStillWorksWithoutAuthoringHost(t *testing.T) {
	reader := &RuntimeSkillRead{Session: &skillruntime.BoundSession{}}
	r := New(Options{Cwd: t.TempDir(), Synth: []Tool{reader, reader}})
	if got, ok := r.Get(SkillReadName); !ok || got != reader {
		t.Fatal("trusted runtime reader was dropped")
	}
	count := 0
	for _, tool := range r.All() {
		if tool.Name() == SkillReadName {
			count++
		}
	}
	if count != 1 {
		t.Fatal("trusted runtime projection duplicated its reader", count)
	}
}
