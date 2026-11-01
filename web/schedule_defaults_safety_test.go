package web

// Independent HTTP/runtime regressions for explicit schedule defaults. Each
// harness uses temporary files and a scripted model; no production task runs.
import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/schedule"
)

func TestScheduleDefaultsSafetyLegacyReadAndEditNeverUpgrade(t *testing.T) {
	for _, cap := range []int{0, 3} {
		t.Run(map[int]string{0: "follow", 3: "finite"}[cap], func(t *testing.T) {
			dir := scheduleEnv(t)
			h := newMultiuserHarness(t, scriptedTurns(textTurn("legacy result")))
			a := scheduleSafetyAgent(t, h, "Unchanged legacy contract")
			sid := h.studioConversationAs(aliceToken, a.ID)
			originalSession := h.session(sid)
			st := schedule.NewStore(dir)
			old := schedule.Job{ID: "pre-modes", AgentID: a.ID, Owner: originalSession.owner, Workdir: originalSession.cwd, Prompt: "old task", Kind: schedule.KindEvery, EverySec: 3600, Timezone: "Asia/Shanghai", MaxTurns: cap, Enabled: true, CreatedAt: time.Now()}
			if err := st.Add(old); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(filepath.Join(dir, "jobs.json"))
			if err != nil {
				t.Fatal(err)
			}
			scheduleSafetyReply(t, h, aliceToken, "GET", "/api/schedule/jobs/pre-modes", nil, 200)
			after, _ := os.ReadFile(filepath.Join(dir, "jobs.json"))
			if !bytes.Equal(before, after) {
				t.Fatal("reading an old definition rewrote its seal/defaults")
			}
			for revision := int64(1); revision <= 2; revision++ {
				body := scheduleSafetyBody(a.ID, "", "")
				body["revision"], body["max_turns"] = revision, cap
				body["title"] = "metadata edit only"
				if revision == 2 {
					body["turn_limit_mode"], body["approval_mode"] = nil, nil
				}
				scheduleSafetyReply(t, h, aliceToken, "PATCH", "/api/schedule/jobs/pre-modes", body, 200)
				saved, err := st.Find(old.ID)
				if err != nil || saved.TurnLimitMode != "" || saved.ApprovalMode != "" || saved.MaxTurns != cap || saved.DefHash != schedule.DefinitionHash(saved) {
					t.Fatalf("metadata PATCH upgraded old authority/cap: %+v %v", saved, err)
				}
			}
			if _, err := st.RunOnce(context.Background(), old.ID, schedule.TriggerManual, nil, h.mgr.scheduleJobRunner); err != nil {
				t.Fatal(err)
			}
			saved, err := st.Find(old.ID)
			if err != nil || saved.LastRunStatus != schedule.StatusOK || saved.SessionID == "" {
				t.Fatalf("legacy execution failed: %+v %v", saved, err)
			}
			if h.session(saved.SessionID).policy.State().Mode != string(ModeStandard) || originalSession.policy.State().Mode != string(ModeStandard) {
				t.Fatal("old scheduled or original interactive session gained auto approval")
			}
		})
	}
}

func TestScheduleDefaultsSafetyOmittedAndNullPatchPreserveExplicitContract(t *testing.T) {
	dir := scheduleEnv(t)
	h := newMultiuserHarness(t, scriptedTurns(textTurn("unused")))
	a := scheduleSafetyAgent(t, h, "Explicit default task")
	sid := h.studioConversationAs(aliceToken, a.ID)
	body := scheduleSafetyBody(a.ID, sid, "new-default")
	scheduleSafetyReply(t, h, aliceToken, "POST", "/api/schedule/jobs", body, 201)
	st := schedule.NewStore(dir)
	for revision := int64(1); revision <= 2; revision++ {
		body["revision"] = revision
		body["title"] = "Preserve explicit authority"
		if revision == 2 {
			body["turn_limit_mode"], body["approval_mode"] = nil, nil
		}
		scheduleSafetyReply(t, h, aliceToken, "PATCH", "/api/schedule/jobs/new-default", body, 200)
		saved, err := st.Find("new-default")
		if err != nil || saved.TurnLimitMode != "unlimited" || saved.ApprovalMode != "auto" || saved.MaxTurns != 0 || saved.DefHash != schedule.DefinitionHash(saved) {
			t.Fatalf("omitted/null PATCH changed explicit contract: %+v %v", saved, err)
		}
	}
	before, _ := os.ReadFile(filepath.Join(dir, "jobs.json"))
	body["approval_mode"], body["turn_limit_mode"], body["max_turns"] = "strict", "limited", 25
	scheduleSafetyReply(t, h, aliceToken, "PATCH", "/api/schedule/jobs/new-default", body, 409)
	after, _ := os.ReadFile(filepath.Join(dir, "jobs.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("stale authority edit modified the persisted definition")
	}
	for _, operation := range []struct{ method, suffix string }{{"GET", ""}, {"PATCH", ""}, {"POST", "/run-now"}, {"GET", "/runs"}} {
		scheduleSafetyReply(t, h, bobToken, operation.method, "/api/schedule/jobs/new-default"+operation.suffix, body, 404)
	}
}

func TestScheduleDefaultsSafetyAutoDoesNotWidenPinnedCapabilitiesOrOtherSession(t *testing.T) {
	dir := scheduleEnv(t)
	h, capture := newStudioHarness(t, []llm.Response{textTurn("bounded capabilities")})
	a := postStudioAgent(t, h, studioAgentCapsBody("Read-only selection", "ready", map[string]any{"models": []string{"k3"}, "tools": []string{"read"}, "skills": []string{}, "mcp_servers": []string{}}), 201)
	source := startStudioConversation(t, h, a.ID)
	if _, err := h.session(source).Start("ordinary conversation", nil, ""); err != nil {
		t.Fatal(err)
	}
	h.session(source).wait()
	body := map[string]any{"agent_id": a.ID, "source_session_id": source, "name": "auto-selection", "prompt": "Read only", "timezone": "Asia/Shanghai", "kind": "every", "every_sec": 3600}
	agentScheduleResponse(t, h.do("POST", "/api/schedule/jobs", studioBody(body)), 201)
	st := schedule.NewStore(dir)
	if _, err := st.RunOnce(context.Background(), "auto-selection", schedule.TriggerManual, nil, h.mgr.scheduleJobRunner); err != nil {
		t.Fatal(err)
	}
	saved, err := st.Find("auto-selection")
	if err != nil || saved.LastRunStatus != schedule.StatusOK || saved.SessionID == "" {
		t.Fatalf("new default did not run: %+v %v", saved, err)
	}
	if h.session(saved.SessionID).policy.State().Mode != string(ModeAuto) || h.session(source).policy.State().Mode != string(ModeStandard) {
		t.Fatal("scheduled auto approval leaked to the existing source conversation")
	}
	schemas := capture.toolSchemas()
	if len(schemas) != 2 || !slices.Equal(schemas[0], schemas[1]) || !slices.Contains(schemas[1], "read") {
		t.Fatalf("auto approval widened pinned tool/skill/MCP selection: %v", schemas)
	}
	for _, name := range []string{"bash", "write", "edit", "browser", "office"} {
		if slices.Contains(schemas[1], name) {
			t.Fatalf("scheduled auto policy acquired excluded tool %q", name)
		}
	}
}

func TestScheduleDefaultsSafetyInvalidAuthorityCombinationsNeverPersist(t *testing.T) {
	dir := scheduleEnv(t)
	h := newMultiuserHarness(t, scriptedTurns(textTurn("unused")))
	a := scheduleSafetyAgent(t, h, "Invalid modes")
	sid := h.studioConversationAs(aliceToken, a.ID)
	for _, options := range []map[string]any{
		{"turn_limit_mode": "unlimited", "max_turns": 3},
		{"turn_limit_mode": "limited", "max_turns": 0},
		{"turn_limit_mode": "legacy", "max_turns": 51},
		{"turn_limit_mode": "infinite"},
		{"approval_mode": "full"},
		{"approval_mode": true},
	} {
		body := scheduleSafetyBody(a.ID, sid, "rejected")
		for key, value := range options {
			body[key] = value
		}
		scheduleSafetyReply(t, h, aliceToken, "POST", "/api/schedule/jobs", body, 400)
	}
	jobs, err := schedule.NewStore(dir).Load()
	if err != nil || len(jobs) != 0 || len(h.rec.systems()) != 0 || len(h.mgr.SchedRunning()) != 0 {
		t.Fatalf("invalid authority persisted or executed: jobs=%v err=%v", jobs, err)
	}
}
