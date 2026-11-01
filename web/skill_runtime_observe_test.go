package web

// Runtime skill observation, conversational and administrator channels: a
// runtime contract tool executed inside an agent run records one
// execute/runtime event as the internal caller through the pogo entrypoint,
// and the administrator direct-call endpoint records the same shape without a
// run. The executor is the in-process recording stand-in, the package the
// shared ZIP fixture — the same arrangement as mcp_runtime_test.go.

import (
	"testing"

	"github.com/yosukeno/pogo/internal/testfs"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/skillobserve"
	"github.com/yosukeno/pogo/skillruntime"
)

// runtimeSkillObserveFixture installs the packaged fixture into a studio
// harness behind the recording executor and opens a conversation bound to it.
func runtimeSkillObserveFixture(t *testing.T, turns []llm.Response) (*harness, *skillObserveRecorder, studioConversation, string) {
	t.Helper()
	service, err := skillruntime.NewManagementService(testfs.SealedTempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	executor := newWebRecordingExecutor()
	executor.register("org.test.mcp", "check", "test.replace.check", webContractJSON("test.replace.check"))
	service.Executor = executor
	h, _ := newStudioHarness(t, turns, func(c *Config) { c.SkillRuntime = service })
	rec := &skillObserveRecorder{}
	h.mgr.SetSkillRecorder(rec)
	var installed map[string]any
	runtimeSkillResponse(t, postSkillZip(t, h, webRuntimePackageZip(t, "org.test.mcp", "1.0.0")), 201, &installed)
	ids, _ := installed["skill_ids"].([]any)
	skillID, _ := ids[0].(string)
	if skillID == "" {
		t.Fatalf("install response carries no skill id: %v", installed)
	}
	body := studioAgentBody("Runtime Skill", "Use the bound capability.", "ready")
	body["skills"] = []string{skillID}
	var definition studioAgent
	runtimeSkillResponse(t, h.do("POST", "/api/manage/agents", studioBody(body)), 201, &definition)
	conversation := createStudioConversationBody(t, h, definition.ID, "")
	if sess := h.session(conversation.ID); sess == nil || sess.runtimeSkill == nil {
		t.Fatal("Skill session not constructed")
	}
	return h, rec, conversation, skillID
}

func runtimeObserveEvents(rec *skillObserveRecorder) []skillobserve.Event {
	out := []skillobserve.Event{}
	for _, e := range rec.finals() {
		if e.Mechanism == "runtime" {
			out = append(out, e)
		}
	}
	return out
}

func TestRuntimeSkillObserveSessionAndAdminCall(t *testing.T) {
	turns := []llm.Response{textTurn("placeholder"), textTurn("done")}
	h, rec, conversation, skillID := runtimeSkillObserveFixture(t, turns)
	sess := h.session(conversation.ID)
	descriptors := sess.runtimeSkill.Descriptors()
	if len(descriptors) != 1 {
		t.Fatalf("descriptors = %+v", descriptors)
	}
	d := descriptors[0]
	if d.SkillID != skillID || d.SkillName != "replace-fixture" {
		t.Fatalf("descriptor attribution = %+v, want %s / replace-fixture", d, skillID)
	}
	// The event's skill_id is the identity hash of the bundle projection —
	// the rs_ id figures as the projection's path, never verbatim.
	identity := skillobserve.Identify("replace-fixture", "bundle", "skill-runtime://"+skillID+"/SKILL.md")

	// The conversational channel: the scripted model picks the runtime tool.
	turns[0] = toolTurn(d.Name, `{"query":"x"}`)
	sess.policy.Set(ModeAuto, 0)
	studioAsk(t, h, conversation.ID, "check x")

	events := runtimeObserveEvents(rec)
	if len(events) != 1 {
		t.Fatalf("runtime events after run = %+v, want 1", events)
	}
	e := events[0]
	if e.Stage != "execute" || e.Status != "ok" || e.ErrorKind != "" {
		t.Fatalf("event = %+v", e)
	}
	if e.Caller != "internal" || e.Entrypoint != "pogo" {
		t.Fatalf("source = %s/%s, want internal/pogo", e.Caller, e.Entrypoint)
	}
	if e.SkillID != identity.ID || e.SkillName != "replace-fixture" || e.SkillSource != "bundle" {
		t.Fatalf("identity = %q/%q/%q, want %q hash", e.SkillID, e.SkillName, e.SkillSource, identity.ID)
	}
	if e.Tool != "test.replace.check" || e.WireTool != d.Name {
		t.Fatalf("tool = %q/%q", e.Tool, e.WireTool)
	}
	if e.SessionID != conversation.ID || e.RunID == "" || e.TraceID == "" {
		t.Fatalf("correlation = %+v", e)
	}
	if e.FinishedAt < e.StartedAt || e.DurationMs < 0 {
		t.Fatalf("times = %+v", e)
	}
	// The bundle is not part of the discovery snapshot yet — no catalog event
	// may carry its identity (that stage is follow-up work).
	for _, other := range rec.finals() {
		if other.Stage == "discovery" && other.SkillSource == "bundle" {
			t.Fatalf("bundle discovery recorded ahead of its stage: %+v", other)
		}
	}

	// The administrator direct-call channel: same event shape, no run.
	var out skillruntime.CallResult
	runtimeSkillResponse(t, h.do("POST", "/api/sessions/"+conversation.ID+"/skill-calls",
		studioBody(map[string]any{"capability": "test.replace.check", "input": map[string]string{"query": "y"}})), 200, &out)

	events = runtimeObserveEvents(rec)
	if len(events) != 2 {
		t.Fatalf("runtime events after admin call = %+v, want 2", events)
	}
	e = events[1]
	if e.Stage != "execute" || e.Status != "ok" || e.Caller != "internal" || e.Entrypoint != "pogo" {
		t.Fatalf("admin event = %+v", e)
	}
	if e.SkillID != identity.ID || e.SkillName != "replace-fixture" || e.SkillSource != "bundle" {
		t.Fatalf("admin identity = %+v", e)
	}
	if e.Tool != "test.replace.check" || e.WireTool != d.Name {
		t.Fatalf("admin tool = %q/%q", e.Tool, e.WireTool)
	}
	if e.SessionID != conversation.ID || e.RunID != "" {
		t.Fatalf("admin correlation = %+v, want the session and no invented run", e)
	}

	// An unbound capability resolves no identity and records nothing.
	runtimeSkillResponse(t, h.do("POST", "/api/sessions/"+conversation.ID+"/skill-calls",
		studioBody(map[string]any{"capability": "test.unknown", "input": map[string]string{}})), 400, nil)
	if got := runtimeObserveEvents(rec); len(got) != 2 {
		t.Fatalf("unknown capability recorded: %+v", got)
	}
}
