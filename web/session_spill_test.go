package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/catalog"
	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/session"
	"github.com/yosukeno/pogo/tools"
)

func sessionSpillTestDir(base, scope string) string {
	sum := sha256.Sum256([]byte(scope))
	return filepath.Join(base, "scopes", hex.EncodeToString(sum[:]))
}

// Use the actual output store and explicit retention, rather than only planting
// a similarly named directory. Deleting a session overrides both its read lease
// and its keep-until metadata, while ordinary expiry still honors those marks.
func seedSessionSpillTest(t *testing.T, base, scope string) string {
	t.Helper()
	r := tools.New(tools.Options{Cwd: t.TempDir(), SpillDir: base, ResultScope: scope})
	result := r.PrepareResult(context.Background(), "mcp_fixture", tools.Result{Text: strings.Repeat("private observation\n", 4000)}, nil)
	if result.ResultID == "" {
		t.Fatal("fixture did not save an actual result")
	}
	reader, ok := r.Get("tool_result")
	if !ok {
		t.Fatal("missing protected result reader")
	}
	args, _ := json.Marshal(map[string]any{"result_id": result.ResultID, "view": "retain", "days": 30})
	if _, err := reader.Execute(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	dir := sessionSpillTestDir(base, scope)
	for _, name := range []string{"bash-output.log", "subagent-answer.log", "browser.log"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(name), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func requireSessionSpillExists(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected preserved Spill path %q: %v", path, err)
	}
}

func TestDeleteSessionRemovesOnlyItsWebSpillScope(t *testing.T) {
	base := t.TempDir()
	t.Setenv("POGO_SPILL_DIR", base)
	h := newHarness(t, scriptedTurns(textTurn("unused")))
	id := h.createSession()
	live := h.session(id)
	scope := "web:single-user:" + id
	if got := sessionResultScope(id, nil); got != scope {
		t.Fatalf("single-user scope changed: %q", got)
	}
	owned := seedSessionSpillTest(t, base, scope)
	other := seedSessionSpillTest(t, base, "web:single-user:other-session")
	child := seedSessionSpillTest(t, base, "cli:"+filepath.Join(h.mgr.cfg.SessionDir, session.SubagentDirName, "independent-child.jsonl"))
	legacy := filepath.Join(base, "bash-legacy.log")
	if err := os.WriteFile(legacy, []byte("unattributed legacy data"), 0600); err != nil {
		t.Fatal(err)
	}
	response := h.do(http.MethodDelete, "/api/sessions/"+id, "")
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("delete: %d %s", response.StatusCode, body)
	}
	if _, err := os.Stat(owned); !os.IsNotExist(err) {
		t.Fatalf("deleted scope survived: %v", err)
	}
	for _, path := range []string{other, child, legacy} {
		requireSessionSpillExists(t, path)
	}
	if !errors.Is(live.sessCtx.Err(), context.Canceled) || !live.bgOff {
		t.Fatal("Spill deletion preceded session/background shutdown")
	}
	live.onBgComplete(tools.BgTask{ID: "late"}, tools.BgResult{Text: "late completion"})
	if _, err := os.Stat(owned); !os.IsNotExist(err) {
		t.Fatalf("late completion recreated deleted Spill: %v", err)
	}
}

func TestDeleteColdOwnedSessionSpillUsesCatalogAndPreservesOtherOwners(t *testing.T) {
	base := t.TempDir()
	t.Setenv("POGO_SPILL_DIR", base)
	h := newMultiuserHarness(t, scriptedTurns(textTurn("unused")))
	id := h.createAs(aliceToken)
	alice, bob := identity.LocalScope("alice"), identity.LocalScope("bob")
	key := fmt.Sprintf("%q/%q/%q/%q", alice.AppID, alice.TenantID, alice.SubjectID, alice.ProjectID)
	if got := sessionResultScope(id, &alice); got != "web:"+key+":"+id {
		t.Fatalf("owned scope changed: %q", got)
	}
	owned := seedSessionSpillTest(t, base, "web:"+key+":"+id)
	foreign := seedSessionSpillTest(t, base, sessionResultScope(id, &bob))
	h.mgr.evictAll()
	if h.mgr.sessions[id] != nil {
		t.Fatal("fixture is not cold")
	}
	denied := h.doAs(bobToken, http.MethodDelete, "/api/sessions/"+id, "")
	denied.Body.Close()
	if denied.StatusCode != http.StatusNotFound {
		t.Fatalf("foreign deletion: %d", denied.StatusCode)
	}
	requireSessionSpillExists(t, owned)
	response := h.doAs(aliceToken, http.MethodDelete, "/api/sessions/"+id, "")
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("cold owned deletion: %d %s", response.StatusCode, body)
	}
	if _, err := os.Stat(owned); !os.IsNotExist(err) {
		t.Fatalf("cold scope survived: %v", err)
	}
	requireSessionSpillExists(t, foreign)
	if h.mgr.sessions[id] != nil {
		t.Fatal("cold deletion constructed a live Agent")
	}
	if _, err := h.mgr.cfg.Catalog.Get(context.Background(), alice, id); !errors.Is(err, catalog.ErrNotFound) {
		t.Fatalf("deleted catalog row remained visible: %v", err)
	}
}

func TestDeleteSessionSpillGuardsPreserveData(t *testing.T) {
	for _, guard := range []string{"active", "compacting", "office"} {
		t.Run(guard, func(t *testing.T) {
			base := t.TempDir()
			t.Setenv("POGO_SPILL_DIR", base)
			h := newHarness(t, scriptedTurns(textTurn("unused")))
			live := h.session(h.createSession())
			dir := seedSessionSpillTest(t, base, sessionResultScope(live.ID, nil))
			switch guard {
			case "active":
				live.active = &activeRun{}
			case "compacting":
				live.compacting = true
			case "office":
				h.mgr.officeBeforeDelete = func(string) error { return errors.New("unsaved Office work") }
			}
			err := h.mgr.Delete(live.ID)
			live.active, live.compacting = nil, false
			if err == nil || (guard != "office" && !errors.Is(err, ErrRunActive)) {
				t.Fatalf("guard did not stop deletion: %v", err)
			}
			requireSessionSpillExists(t, dir)
			requireSessionSpillExists(t, live.Path())
			if live.closing || live.sessCtx.Err() != nil {
				t.Fatal("rejected deletion closed the live session")
			}
		})
	}
}

func TestDeleteSessionSpillOwnershipFailureDoesNotDowngradeOrClose(t *testing.T) {
	base := t.TempDir()
	t.Setenv("POGO_SPILL_DIR", base)
	h := newMultiuserHarness(t, scriptedTurns(textTurn("unused")))
	id := h.createAs(aliceToken)
	live := h.session(id)
	owner := *live.owner
	dir := seedSessionSpillTest(t, base, sessionResultScope(id, &owner))
	wrong := identity.LocalScope("bob")
	live.owner = &wrong
	err := h.mgr.Delete(id)
	live.owner = &owner
	if err == nil || !strings.Contains(err.Error(), "owner") {
		t.Fatalf("owner mismatch was accepted: %v", err)
	}
	requireSessionSpillExists(t, dir)
	if live.closing || live.sessCtx.Err() != nil {
		t.Fatal("failed owner resolution closed the session")
	}
	coldID := writeTranscript(t, h.mgr.cfg.SessionDir, "unregistered transcript")
	unknown := seedSessionSpillTest(t, base, "web:single-user:"+coldID)
	if err := h.mgr.Delete(coldID); err == nil {
		t.Fatal("missing catalog owner downgraded to single-user")
	}
	requireSessionSpillExists(t, unknown)
	requireSessionSpillExists(t, h.mgr.transcriptPath(coldID))
}

func TestDeleteSessionSpillPartialFailureKeepsTranscriptAndOwnerForColdRetry(t *testing.T) {
	base := t.TempDir()
	t.Setenv("POGO_SPILL_DIR", base)
	h := newMultiuserHarness(t, scriptedTurns(textTurn("unused")))
	id := h.createAs(aliceToken)
	live := h.session(id)
	owner := *live.owner
	dir := seedSessionSpillTest(t, base, sessionResultScope(id, &owner))
	partial := filepath.Join(dir, "bash-output.log")
	h.mgr.spillScopeCleanup = func(gotBase, gotScope string) error {
		if gotBase != base || gotScope != sessionResultScope(id, &owner) {
			return errors.New("cleanup received an unrelated scope")
		}
		if live.sessCtx.Err() == nil || !live.bgOff {
			return errors.New("cleanup ran before managed workers were drained")
		}
		if err := os.Remove(partial); err != nil {
			return err
		}
		return errors.New("injected partial cleanup failure")
	}
	response := h.doAs(aliceToken, http.MethodDelete, "/api/sessions/"+id, "")
	body, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if response.StatusCode != http.StatusInternalServerError || !strings.Contains(string(body), "会话记录尚未删除") {
		t.Fatalf("partial failure was hidden: %d %s", response.StatusCode, body)
	}
	requireSessionSpillExists(t, live.Path())
	requireSessionSpillExists(t, filepath.Join(dir, "results"))
	if _, err := os.Stat(partial); !os.IsNotExist(err) {
		t.Fatalf("fixture did not partially remove data: %v", err)
	}
	if _, err := h.mgr.cfg.Catalog.Get(context.Background(), owner, id); err != nil {
		t.Fatalf("partial cleanup tombstoned the owner: %v", err)
	}
	if h.mgr.sessions[id] != nil {
		t.Fatal("failed cleanup kept a closed live registry cached")
	}
	h.mgr.spillScopeCleanup = nil
	retry := h.doAs(aliceToken, http.MethodDelete, "/api/sessions/"+id, "")
	retry.Body.Close()
	if retry.StatusCode != http.StatusNoContent {
		t.Fatalf("cold retry failed: %d", retry.StatusCode)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("cold retry retained Spill files: %v", err)
	}
	if _, err := os.Stat(live.Path()); !os.IsNotExist(err) {
		t.Fatalf("cold retry retained transcript: %v", err)
	}
}
