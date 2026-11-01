package web

// Independent scheduled-Agent boundary tests. All state lives in the existing
// temporary HTTP harness; no real clock, model or operating-system job runs.
import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/inputref"
	"github.com/yosukeno/pogo/schedule"
)

func scheduleSafetyReply(t *testing.T, h *muHarness, token, method, path string, body any, want int) map[string]any {
	t.Helper()
	raw := ""
	if body != nil {
		b, e := json.Marshal(body)
		if e != nil {
			t.Fatal(e)
		}
		raw = string(b)
	}
	res := h.doAs(token, method, path, raw)
	defer res.Body.Close()
	b, e := io.ReadAll(res.Body)
	if e != nil {
		t.Fatal(e)
	}
	if res.StatusCode != want {
		t.Fatalf("%s %s got%d want%d: %s", method, path, res.StatusCode, want, b)
	}
	out := map[string]any{}
	if len(b) > 0 {
		if e = json.Unmarshal(b, &out); e != nil {
			t.Fatal(e)
		}
	}
	return out
}
func scheduleSafetyAgent(t *testing.T, h *muHarness, name string) studioAgent {
	t.Helper()
	a, e := h.mgr.studio.put("", studioInput{Name: name, Instructions: "schedule safety only", Model: "k3", Status: "ready"}, 0)
	if e != nil {
		t.Fatal(e)
	}
	return a
}
func scheduleSafetyBody(agent, source, name string) map[string]any {
	return map[string]any{"agent_id": agent, "source_session_id": source, "name": name, "title": "Private safety task", "prompt": "data only", "timezone": "Asia/Shanghai", "kind": "every", "every_sec": 3600}
}

func TestScheduleBindingSafetyForeignOwnerCannotObserveOrMutateAnyEndpoint(t *testing.T) {
	dir := scheduleEnv(t)
	h := newMultiuserHarness(t, scriptedTurns(textTurn("unused")))
	h.armUsers(map[string]userRecord{"alice": {Username: "alice", Token: aliceToken}, "bob": {Username: "bob", Token: bobToken, Admin: true}})
	a := scheduleSafetyAgent(t, h, "Private plans")
	source := h.studioConversationAs(aliceToken, a.ID)
	body := scheduleSafetyBody(a.ID, source, "alice-only")
	created := scheduleSafetyReply(t, h, aliceToken, "POST", "/api/schedule/jobs", body, 201)
	job := created["job"].(map[string]any)
	if _, exists := job["owner"]; exists {
		t.Fatal("server authorization owner leaked into client-controlled job view")
	}
	before, e := os.ReadFile(filepath.Join(dir, "jobs.json"))
	if e != nil {
		t.Fatal(e)
	}
	for _, query := range []string{"", "?agent_id=" + a.ID, "?scope=agent"} {
		v := scheduleSafetyReply(t, h, bobToken, "GET", "/api/schedule/jobs"+query, nil, 200)
		if len(v["jobs"].([]any)) != 0 {
			t.Fatal("admin account saw another owner's scheduled Agent task")
		}
	}
	for _, op := range []struct{ method, suffix string }{{"GET", ""}, {"PATCH", ""}, {"POST", "/enable"}, {"POST", "/disable"}, {"DELETE", ""}, {"POST", "/run-now"}, {"GET", "/runs"}} {
		scheduleSafetyReply(t, h, bobToken, op.method, "/api/schedule/jobs/alice-only"+op.suffix, map[string]any{}, 404)
	}
	after, e := os.ReadFile(filepath.Join(dir, "jobs.json"))
	if e != nil || !bytes.Equal(before, after) {
		t.Fatal("foreign endpoint attempts changed persistent jobs", e)
	}
	own := scheduleSafetyReply(t, h, aliceToken, "GET", "/api/schedule/jobs/alice-only", nil, 200)
	if own["job"].(map[string]any)["agent_id"] != a.ID {
		t.Fatal("owner lost its unchanged binding")
	}
	if len(h.rec.systems()) != 0 || len(h.mgr.SchedRunning()) != 0 {
		t.Fatal("rejected request started runtime/model work")
	}
}

func TestScheduleBindingSafetyRejectsForgedSourceAndForeignImmutableAsset(t *testing.T) {
	dir := scheduleEnv(t)
	h := newMultiuserHarness(t, scriptedTurns(textTurn("unused")))
	a := scheduleSafetyAgent(t, h, "One")
	other := scheduleSafetyAgent(t, h, "Two")
	own := h.studioConversationAs(aliceToken, a.ID)
	bob := h.studioConversationAs(bobToken, a.ID)
	wrongAgent := h.studioConversationAs(aliceToken, other.ID)
	ordinary := h.createAs(aliceToken)
	for _, source := range []string{bob, wrongAgent, ordinary, "missing-session"} {
		scheduleSafetyReply(t, h, aliceToken, "POST", "/api/schedule/jobs", scheduleSafetyBody(a.ID, source, "forged-source"), 404)
	}
	forged := scheduleSafetyBody(a.ID, own, "forged-owner")
	forged["owner"] = map[string]string{"subject_id": "bob"}
	scheduleSafetyReply(t, h, aliceToken, "POST", "/api/schedule/jobs", forged, 400)
	foreignStore, e := h.mgr.InformationStore(h.session(bob).owner)
	if e != nil {
		t.Fatal(e)
	}
	asset, e := foreignStore.PutAsset("private.txt", "", strings.NewReader("BOB_PRIVATE_SCHEDULE_BYTES"), 1024)
	if e != nil {
		t.Fatal(e)
	}
	forged = scheduleSafetyBody(a.ID, own, "foreign-asset")
	forged["parts"] = []inputref.Part{{Type: "file", Path: asset.Asset, AsFile: true}}
	v := scheduleSafetyReply(t, h, aliceToken, "POST", "/api/schedule/jobs", forged, 400)
	encoded, _ := json.Marshal(v)
	if bytes.Contains(encoded, []byte("BOB_PRIVATE_SCHEDULE_BYTES")) {
		t.Fatal("foreign asset content leaked in refusal")
	}
	jobs, e := schedule.NewStore(dir).Load()
	if e != nil || len(jobs) != 0 {
		t.Fatal("rejected source/owner/asset persisted a task", e)
	}
}

func TestScheduleBindingSafetyRejectsInternalSymlinkAttachments(t *testing.T) {
	dir := scheduleEnv(t)
	h := newMultiuserHarness(t, scriptedTurns(textTurn("unused")))
	a := scheduleSafetyAgent(t, h, "File safety")
	sid := h.studioConversationAs(aliceToken, a.ID)
	cwd := h.session(sid).cwd
	if e := os.MkdirAll(filepath.Join(cwd, "uploads", "real"), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(cwd, "uploads", "real", "source.txt"), []byte("pristine source"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink("real/source.txt", filepath.Join(cwd, "uploads", "linked.txt")); e != nil {
		t.Skipf("symlinks unavailable: %v", e)
	}
	if e := os.Symlink("real", filepath.Join(cwd, "uploads", "alias")); e != nil {
		t.Fatal(e)
	}
	for _, p := range []string{"uploads/linked.txt", "uploads/alias/source.txt"} {
		b := scheduleSafetyBody(a.ID, sid, "symlink-task")
		b["parts"] = []inputref.Part{{Type: "file", Path: p}}
		scheduleSafetyReply(t, h, aliceToken, "POST", "/api/schedule/jobs", b, 400)
	}
	jobs, e := schedule.NewStore(dir).Load()
	if e != nil || len(jobs) != 0 {
		t.Fatal("symlink attachment created a job", e)
	}
	valid := scheduleSafetyBody(a.ID, sid, "ordinary-file")
	valid["parts"] = []inputref.Part{{Type: "file", Path: "uploads/real/source.txt"}}
	scheduleSafetyReply(t, h, aliceToken, "POST", "/api/schedule/jobs", valid, 201)
}

func TestScheduleBindingSafetyRemovedAccountAndRunningEditsFailClosed(t *testing.T) {
	dir := scheduleEnv(t)
	h := newMultiuserHarness(t, scriptedTurns(textTurn("must not run")))
	a := scheduleSafetyAgent(t, h, "Runtime owner")
	sid := h.studioConversationAs(aliceToken, a.ID)
	body := scheduleSafetyBody(a.ID, sid, "revoked-owner")
	scheduleSafetyReply(t, h, aliceToken, "POST", "/api/schedule/jobs", body, 201)
	if !h.mgr.SchedStart("revoked-owner") {
		t.Fatal("could not stage in-flight marker")
	}
	body["revision"] = 1
	scheduleSafetyReply(t, h, aliceToken, "PATCH", "/api/schedule/jobs/revoked-owner", body, 409)
	scheduleSafetyReply(t, h, aliceToken, "DELETE", "/api/schedule/jobs/revoked-owner", nil, 409)
	h.mgr.SchedDone("revoked-owner")
	h.armUsers(map[string]userRecord{"bob": {Username: "bob", Token: bobToken, Admin: true}})
	result, e := schedule.NewStore(dir).RunOnce(context.Background(), "revoked-owner", schedule.TriggerScheduled, nil, h.mgr.scheduleJobRunner)
	if e != nil || result.Status != schedule.StatusError {
		t.Fatal("removed owner was not recorded as a failed run", result, e)
	}
	after, e := schedule.NewStore(dir).Find("revoked-owner")
	if e != nil || after.SessionID != "" || after.LastError == "" || len(h.rec.systems()) != 0 {
		t.Fatal("revoked owner reached Agent session/model", after, e)
	}
}

func TestScheduleBindingSafetyDeletedIDCannotTransferPrivateRunHistory(t *testing.T) {
	dir := scheduleEnv(t)
	h := newMultiuserHarness(t, scriptedTurns(textTurn("unused")))
	a := scheduleSafetyAgent(t, h, "Historical owner")
	aliceSource := h.studioConversationAs(aliceToken, a.ID)
	bobSource := h.studioConversationAs(bobToken, a.ID)
	first := scheduleSafetyReply(t, h, aliceToken, "POST", "/api/schedule/jobs", scheduleSafetyBody(a.ID, aliceSource, "same-history-id"), 201)
	oldID := first["job"].(map[string]any)["id"].(string)
	st := schedule.NewStore(dir)
	if err := st.AppendRun(oldID, schedule.RunRecord{Status: schedule.StatusOK, Summary: "ALICE_PRIVATE_SUMMARY", SessionID: "alice-private-session"}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(st.OutLogPath(oldID)), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(st.OutLogPath(oldID), []byte("ALICE_PRIVATE_LOG\n"), 0600); err != nil {
		t.Fatal(err)
	}
	scheduleSafetyReply(t, h, aliceToken, "DELETE", "/api/schedule/jobs/"+oldID, nil, 200)
	body, _ := json.Marshal(scheduleSafetyBody(a.ID, bobSource, "same-history-id"))
	response := h.doAs(bobToken, "POST", "/api/schedule/jobs", string(body))
	raw, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode == 409 {
		return
	}
	if response.StatusCode != 201 {
		t.Fatalf("expected historical ID refusal or new identity, got %d: %s", response.StatusCode, raw)
	}
	var created struct {
		Job schedule.Job `json:"job"`
	}
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatal(err)
	}
	view := scheduleSafetyReply(t, h, bobToken, "GET", "/api/schedule/jobs/"+created.Job.ID+"/runs", nil, 200)
	encoded, _ := json.Marshal(view)
	if created.Job.ID == oldID || bytes.Contains(encoded, []byte("ALICE_PRIVATE")) {
		t.Fatalf("deleted task ID transferred prior owner's run/log history: new=%q old=%q view=%s", created.Job.ID, oldID, encoded)
	}
}
