package web

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/url"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/yosukeno/pogo/agent"
	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/skillauthor"
	"github.com/yosukeno/pogo/skills"
	"github.com/yosukeno/pogo/tools"
)

func authoredRequest(key, name, body string) skillauthor.SaveRequest {
	return skillauthor.SaveRequest{RequestID: key, Files: []skillauthor.File{
		{Path: "SKILL.md", Content: "---\nname: " + name + "\ndescription: Repeat a verified synthetic report workflow.\n---\n\n" + body + "\n\nRead [the example](references/example.sql).\n"},
		{Path: "references/example.sql", Content: "SELECT 'synthetic-only';\n"},
	}}
}

func authoredCreatorAgentBody(t *testing.T, h *harness, name string) map[string]any {
	t.Helper()
	for _, sk := range h.mgr.Skills() {
		if sk.Name == "skill-creator" && sk.Source == "embedded" {
			return studioAgentCapsBody(name, "ready", map[string]any{
				"skills": []string{skillIDOf(sk)}, "tools": []string{tools.SkillSaveName},
				"skill_tool_bindings": []string{"skill-creator"},
			})
		}
	}
	for _, sk := range bindingTestSkills(t) {
		if sk.Name == "skill-creator" {
			h.mgr.SetSkills(append(h.mgr.Skills(), sk))
			return studioAgentCapsBody(name, "ready", map[string]any{
				"skills": []string{skillIDOf(sk)}, "tools": []string{tools.SkillSaveName},
				"skill_tool_bindings": []string{"skill-creator"},
			})
		}
	}
	t.Fatal("trusted skill-creator Skill is unavailable")
	return nil
}

func authoredFixture(t *testing.T) (*harness, *Session, *skillAuthorBackend) {
	t.Helper()
	h := newHarness(t, func(int) (llm.Response, error) {
		t.Fatal("direct host tests must not invoke a provider")
		return plainTurn()
	})
	a := postStudioAgent(t, h, authoredCreatorAgentBody(t, h, "Skill author"), 201)
	sid := createStudioConversationBody(t, h, a.ID, "{}").ID
	s := h.session(sid)
	return h, s, h.mgr.skillAuthorBackend(s)
}

func authoredHTTP(t *testing.T, h *harness, method, endpoint string, body any, want int) []byte {
	t.Helper()
	raw := ""
	if body != nil {
		data, _ := json.Marshal(body)
		raw = string(data)
	}
	r := h.do(method, endpoint, raw)
	defer r.Body.Close()
	data, _ := io.ReadAll(r.Body)
	if r.StatusCode != want {
		t.Fatalf("%s %s: %d wanted %d (%s)", method, endpoint, r.StatusCode, want, data)
	}
	return data
}

func TestAuthoredSkillHostSaveReadActivateAndOriginalSession(t *testing.T) {
	h, s, b := authoredFixture(t)
	before, _ := os.ReadFile(s.Path())
	definition, _ := h.mgr.studio.get(s.studioID)
	beforeAgent, _ := json.Marshal(definition)
	loop, hub, browser := s.agent, s.hub, s.browser
	if !slices.Contains(s.agent.NativeToolNames(), tools.SkillSaveName) {
		t.Fatal("optional native Skill save not registered")
	}
	tool := &tools.SkillSave{Backend: b}
	in := authoredRequest("create:one.1", "report-recipe", "FIRST VERSION")
	raw, _ := json.Marshal(in)
	result, err := tool.Execute(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	var saved tools.SkillSaveResult
	data, _ := json.Marshal(result.Details)
	if json.Unmarshal(data, &saved) != nil || !saved.Saved || !saved.Enabled || saved.Pending || saved.BehaviorTested {
		t.Fatalf("save receipt: %s", data)
	}
	if s.agent != loop || s.hub != hub || s.browser != browser {
		t.Fatal("save recreated conversation machinery")
	}
	after, _ := os.ReadFile(s.Path())
	definition, _ = h.mgr.studio.get(s.studioID)
	afterAgent, _ := json.Marshal(definition)
	if !bytes.Equal(before, after) || !bytes.Equal(beforeAgent, afterAgent) {
		t.Fatal("save changed history or shared Agent definition")
	}
	if got := sessionSkillNamesHTTP(t, h, s.ID); !slices.Contains(got, "report-recipe") {
		t.Fatalf("completion list=%v", got)
	}
	if !slices.Contains(s.agent.NativeToolNames(), tools.SkillReadName) {
		t.Fatal("host reader absent")
	}
	read := &tools.RuntimeSkillRead{Backend: b}
	value, err := read.Execute(context.Background(), json.RawMessage(`{"skill":"report-recipe","path":"references/example.sql"}`))
	if err != nil || !strings.Contains(value.Text, "synthetic-only") {
		t.Fatalf("reference read=%+v %v", value, err)
	}
	for _, sk := range h.mgr.Skills() {
		if sk.Name == "report-recipe" {
			t.Fatal("private Skill leaked into global catalog")
		}
	}
	search, err := b.Search(context.Background(), tools.SkillSearchRequest{Query: "report", Limit: 10})
	if err != nil || len(search.Skills) != 1 || !search.Skills[0].Editable {
		t.Fatalf("search=%+v %v", search, err)
	}
	if _, err := b.Read(context.Background(), tools.SkillReadRequest{Skill: saved.Skill.ID, Path: "../../private"}); err == nil {
		t.Fatal("read accepted traversal")
	}
	if _, err := b.Read(context.Background(), tools.SkillReadRequest{Skill: "unknown"}); err == nil {
		t.Fatal("unknown Skill readable")
	}
	store, _ := h.mgr.authoredSkillStore(nil, s.studioID)
	same, err := store.Read(saved.Skill.ID, "")
	if err != nil || same.Version != 1 {
		t.Fatal("save not durable")
	}
	other := createStudioAgent(t, h, "Other author", "", "ready")
	otherSID := createStudioConversationBody(t, h, other.ID, "{}").ID
	if slices.Contains(sessionSkillNamesHTTP(t, h, otherSID), "report-recipe") {
		t.Fatal("other Agent received private Skill")
	}
}

func TestAuthoredSkillHTTPVersionEditCASExportAndRetry(t *testing.T) {
	h, s, b := authoredFixture(t)
	ctx := context.Background()
	saved, err := b.Save(ctx, authoredRequest("initial", "versioned-report", "FIRST VERSION"))
	if err != nil {
		t.Fatal(err)
	}
	base := "/api/apps/" + s.studioID + "/authored-skills/" + saved.Skill.ID
	var doc map[string]any
	_ = json.Unmarshal(authoredHTTP(t, h, "GET", base+"/content", nil, 200), &doc)
	if doc["revision"] != saved.Skill.Revision || doc["editable"] != true {
		t.Fatalf("document=%v", doc)
	}
	newText := strings.ReplaceAll(doc["content"].(string), "FIRST VERSION", "HUMAN EDIT")
	edit := map[string]any{"content": newText, "expected_revision": saved.Skill.Revision}
	var edited map[string]any
	_ = json.Unmarshal(authoredHTTP(t, h, "PUT", base+"/content", edit, 200), &edited)
	if edited["revision"] == saved.Skill.Revision || edited["version"] != float64(2) {
		t.Fatal("edit did not create a new version")
	}
	current, err := b.Read(ctx, tools.SkillReadRequest{Skill: saved.Skill.ID})
	if err != nil || !strings.Contains(current.Content, "HUMAN EDIT") {
		t.Fatal("idle model view stayed stale")
	}
	// A later reference-only update must not turn a lost editor response into
	// another edit or overwrite those newer references on retry.
	store, _ := h.mgr.authoredSkillStore(nil, s.studioID)
	latest, _ := store.Read(saved.Skill.ID, "")
	latest.Files[1].Content = "SELECT 'NEWER REFERENCE';\n"
	third, err := b.Save(ctx, skillauthor.SaveRequest{RequestID: "reference-change", SkillID: latest.ID, ExpectedRevision: latest.Revision, Files: latest.Files})
	if err != nil {
		t.Fatal(err)
	}
	retry := authoredHTTP(t, h, "PUT", base+"/content", edit, 200)
	if !bytes.Contains(retry, []byte(edited["revision"].(string))) {
		t.Fatal("identical retry did not return original receipt")
	}
	final, _ := store.Read(latest.ID, "")
	if final.Revision != third.Skill.Revision || final.Version != 3 || !strings.Contains(final.Files[1].Content, "NEWER") {
		t.Fatal("retry overwrote newer version")
	}
	bad := map[string]any{"content": strings.ReplaceAll(newText, "HUMAN EDIT", "STALE"), "expected_revision": saved.Skill.Revision}
	authoredHTTP(t, h, "PUT", base+"/content", bad, 409)
	var old map[string]any
	_ = json.Unmarshal(authoredHTTP(t, h, "GET", base+"/content?revision="+saved.Skill.Revision, nil, 200), &old)
	if old["editable"] != false || !strings.Contains(old["content"].(string), "FIRST VERSION") {
		t.Fatal("immutable history changed")
	}
	authoredHTTP(t, h, "GET", base+"/versions", nil, 200)
	files := authoredHTTP(t, h, "GET", base+"/files?path=references&revision="+saved.Skill.Revision, nil, 200)
	if !bytes.Contains(files, []byte("example.sql")) {
		t.Fatal("reference tree missing")
	}
	archive := authoredHTTP(t, h, "GET", base+"/export?revision="+saved.Skill.Revision, nil, 200)
	z, err := zip.NewReader(bytes.NewReader(archive), int64(len(archive)))
	if err != nil || len(z.File) != 2 {
		t.Fatalf("export=%v", err)
	}
	for _, file := range z.File {
		if !strings.HasPrefix(file.Name, "versioned-report/") {
			t.Fatal("export escaped Skill root")
		}
	}
	other := createStudioAgent(t, h, "Separate agent", "", "ready")
	authoredHTTP(t, h, "GET", "/api/apps/"+other.ID+"/authored-skills/"+saved.Skill.ID+"/content", nil, 404)
	if got := sessionSkillNamesHTTP(t, h, s.ID); !slices.Contains(got, "versioned-report") {
		t.Fatal("selection lost after editor")
	}
}

func TestAuthoredSkillBusyRunPinsReferenceThenAppliesVersionAndDisable(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	h := newHarness(t, func(int) (llm.Response, error) { close(entered); <-release; return textTurn("done"), nil })
	a := postStudioAgent(t, h, authoredCreatorAgentBody(t, h, "Busy author"), 201)
	sid := createStudioConversationBody(t, h, a.ID, "{}").ID
	s := h.session(sid)
	b := h.mgr.skillAuthorBackend(s)
	first, err := b.Save(context.Background(), authoredRequest("first", "pinned-report", "OLD BODY"))
	if err != nil {
		t.Fatal(err)
	}
	stream := h.start(sid, "perform synthetic work")
	defer stream.close()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("provider did not start")
	}
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	in := authoredRequest("update", "pinned-report", "NEW BODY")
	in.SkillID = first.Skill.ID
	in.ExpectedRevision = first.Skill.Revision
	in.Files[1].Content = "SELECT 'NEW REFERENCE';\n"
	second, err := b.Save(context.Background(), in)
	if err != nil || !second.Pending {
		t.Fatalf("busy save=%+v %v", second, err)
	}
	pinned, err := b.Read(context.Background(), tools.SkillReadRequest{Skill: first.Skill.ID, Path: "references/example.sql"})
	if err != nil || strings.Contains(pinned.Content, "NEW REFERENCE") {
		t.Fatal("active run switched reference bytes")
	}
	explicit, err := b.Read(context.Background(), tools.SkillReadRequest{Skill: first.Skill.ID, Path: "references/example.sql", Revision: second.Skill.Revision})
	if err != nil || !strings.Contains(explicit.Content, "NEW REFERENCE") {
		t.Fatal("authoring cannot explicitly inspect latest revision")
	}
	close(release)
	stream.wait(t, EvRunEnd)
	s.wait()
	var newDoc tools.SkillReadResult
	deadline := time.Now().Add(3 * time.Second)
	for {
		newDoc, err = b.Read(context.Background(), tools.SkillReadRequest{Skill: first.Skill.ID})
		if err != nil || strings.Contains(newDoc.Content, "NEW BODY") || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err != nil || !strings.Contains(newDoc.Content, "NEW BODY") {
		t.Fatal("idle did not activate new version")
	}
	disabled := false
	in.RequestID = "disable"
	in.ExpectedRevision = second.Skill.Revision
	in.Activate = &disabled
	out, err := b.Save(context.Background(), in)
	if err != nil || out.Enabled || out.Pending {
		t.Fatalf("disable=%+v %v", out, err)
	}
	if slices.Contains(sessionSkillNamesHTTP(t, h, sid), "pinned-report") {
		t.Fatal("disabled Skill remained in completion")
	}
	store, _ := h.mgr.authoredSkillStore(nil, a.ID)
	history, _ := store.Versions(first.Skill.ID)
	if len(history) != 3 {
		t.Fatal("version history was not preserved")
	}
}

func TestAuthoredSkillAgentLoopReceiptAndSubsequentInvocation(t *testing.T) {
	in := authoredRequest("agent:save.1", "loop-report", "Follow this verified reporting procedure.")
	raw, _ := json.Marshal(in)
	h, capture := newStudioHarness(t, []llm.Response{toolTurn(tools.SkillSaveName, string(raw)), textTurn("saved"), textTurn("used")})
	a := postStudioAgent(t, h, authoredCreatorAgentBody(t, h, "Loop author"), 201)
	sid := createStudioConversationBody(t, h, a.ID, "{}").ID
	stream := h.stream(sid, "")
	defer stream.close()
	stream.next(t)
	h.post("/api/sessions/"+sid+"/messages", `{"prompt":"保存验证过的流程为 Skill"}`, 202)
	end := stream.wait(t, EvToolEnd)
	if end.IsError || end.Name != tools.SkillSaveName {
		t.Fatalf("tool=%+v", end)
	}
	var receipt tools.SkillSaveResult
	data, _ := json.Marshal(end.Details)
	_ = json.Unmarshal(data, &receipt)
	if !receipt.Saved || !receipt.Pending || receipt.Skill.Version != 1 || receipt.AgentID != a.ID || receipt.BehaviorTested {
		t.Fatalf("receipt=%s", data)
	}
	stream.wait(t, EvRunEnd)
	h.session(sid).wait()
	studioAsk(t, h, sid, "/skill:loop-report execute a synthetic report")
	systems, _, histories, _ := capture.snapshot()
	if len(histories) != 3 || !strings.Contains(systems[2], "loop-report") {
		t.Fatal("next run did not know created Skill")
	}
	last := histories[2][len(histories[2])-1]
	if !strings.Contains(studioMessageText(last), "verified reporting procedure") {
		t.Fatal("slash invocation did not expand stored content")
	}
	if !slices.Contains(capture.toolSchemas()[2], tools.SkillSaveName) {
		t.Fatal("Skill update removed its native management tool")
	}
}

func TestAuthoredSkillPreservesLegacyNoManifestAndSurvivesRecreation(t *testing.T) {
	sk := writeManifestSkill(t, t.TempDir(), "existing-package", "Existing package", "existing.echo")
	h := newHarness(t, func(int) (llm.Response, error) { return textTurn("done"), nil }, func(c *Config) { c.Skills = []skills.Skill{sk} })
	a := postStudioAgent(t, h, authoredCreatorAgentBody(t, h, "Legacy author"), 201)
	sid := createStudioConversationBody(t, h, a.ID, "{}").ID
	s := h.session(sid)
	b := h.mgr.skillAuthorBackend(s)
	created, err := b.Save(context.Background(), authoredRequest("legacy", "legacy-report", "BODY"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range s.mcpReserved {
		if tool.Name() == "existing_echo" {
			t.Fatal("private instruction creation granted global manifest executor")
		}
	}
	// Reconstruct through the same persisted binding and transcript, rather than
	// depending on the original in-memory session's closures.
	store := s.store
	binding, _ := h.mgr.studio.binding(sid)
	fresh := h.mgr.newSession(store, s.cfg, nil, s.cwd, llm.Usage{}, llm.Usage{}, nil)
	t.Cleanup(func() { fresh.bgCancel() })
	fresh.mu.Lock()
	names := []string{}
	for _, entry := range fresh.invocationSkills {
		names = append(names, entry.Name)
	}
	fresh.mu.Unlock()
	if !slices.Contains(names, "legacy-report") {
		t.Fatal("restored session lost private Skill")
	}
	for _, tool := range fresh.mcpReserved {
		if tool.Name() == "existing_echo" {
			t.Fatal("restoration granted manifest executor")
		}
	}
	if !reflect.DeepEqual(binding.Agent, a) {
		t.Fatal("shared definition changed")
	}
	if created.Skill.ID == "" {
		t.Fatal("missing stable identity")
	}
}

func TestAuthoredSkillOwnerIsolationAndPolicy(t *testing.T) {
	for _, mode := range []Mode{ModeAuto, ModeStrict, ModeStandard} {
		policy := NewPolicy()
		policy.mode = mode
		for _, name := range []string{tools.SkillSearchName, tools.SkillReadName, tools.SkillSaveName} {
			_, automatic := policy.Decide(agent.GateRequest{ToolName: name})
			if automatic != (name != tools.SkillSaveName || mode != ModeStrict) {
				t.Fatalf("mode %s tool %s auto=%v", mode, name, automatic)
			}
		}
	}
	h := newMultiuserHarness(t, func(int) (llm.Response, error) { return textTurn("done"), nil })
	// Admin establishes a shared Agent; each account still authors into its own
	// independent store and cannot preview another owner's ID by guessing it.
	a, err := h.mgr.studio.put("", studioInput{Name: "Shared author", Description: "Shared", Instructions: "Synthetic only", Status: "ready", Model: "k3"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	alice := identity.LocalScope("alice")
	bob := identity.LocalScope("bob")
	store, err := h.mgr.authoredSkillStore(&alice, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := store.Save(context.Background(), authoredRequest("alice-only", "private-report", "ALICE ONLY"))
	if err != nil {
		t.Fatal(err)
	}
	other, _ := h.mgr.authoredSkillStore(&bob, a.ID)
	if _, err := other.Read(saved.Skill.ID, ""); err == nil {
		t.Fatal("cross-owner store lookup succeeded")
	}
	base := "/api/apps/" + a.ID + "/authored-skills/" + saved.Skill.ID
	for _, tail := range []string{"/content", "/files", "/versions", "/export"} {
		r := h.doAs(bobToken, "GET", base+tail, "")
		r.Body.Close()
		if r.StatusCode != 404 {
			t.Fatalf("Bob%s status %d", tail, r.StatusCode)
		}
	}
	r := h.doAs(aliceToken, "GET", base+"/content?path="+url.QueryEscape("SKILL.md"), "")
	defer r.Body.Close()
	data, _ := io.ReadAll(r.Body)
	if r.StatusCode != 200 || !bytes.Contains(data, []byte("ALICE ONLY")) {
		t.Fatalf("Alice %d %s", r.StatusCode, data)
	}
}
