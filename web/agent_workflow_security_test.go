package web

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	af "github.com/yosukeno/pogo/agentworkflow"
	"github.com/yosukeno/pogo/config"
	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/llm"
)

func awSecurityConfirmFlow(t *testing.T, h *muHarness, aid string) af.Definition {
	t.Helper()
	return awTestValue[af.Definition](t, awTestCall(t, h, aliceToken, aid, "edit", map[string]any{
		"action": "create", "request_id": "security-flow", "name": "独立资料边界测试",
		"steps": []af.Step{{ID: "review", Kind: "confirm", Prompt: "请本人检查冻结资料"}},
	}, 200), "workflow")
}

func TestAgentWorkflowRejectsAncestorSymlinksAcrossUsers(t *testing.T) {
	for _, container := range []string{"agent-directory", "run-container"} {
		t.Run(container, func(t *testing.T) {
			h := newMultiuserHarness(t, func(int) (llm.Response, error) {
				t.Fatal("rejected input called model")
				return textTurn("unused"), nil
			})
			a := awTestAgent(t, h, "文件边界助手")
			d := awSecurityConfirmFlow(t, h, a.ID)
			aliceRoot := h.mgr.UserRoot(identity.LocalScope("alice"))
			bobRoot := h.mgr.UserRoot(identity.LocalScope("bob"))
			foreign := filepath.Join(bobRoot, "private-materials")
			if e := os.MkdirAll(foreign, 0700); e != nil {
				t.Fatal(e)
			}
			secret := filepath.Join(foreign, "secret.md")
			if e := os.WriteFile(secret, []byte("BOB-PRIVATE-MATERIAL"), 0600); e != nil {
				t.Fatal(e)
			}
			agentDir := filepath.Join(aliceRoot, "agents", a.ID)
			link := agentDir
			if container == "run-container" {
				if e := os.MkdirAll(agentDir, 0700); e != nil {
					t.Fatal(e)
				}
				link = filepath.Join(agentDir, "workflow-runs")
			} else if e := os.MkdirAll(filepath.Dir(agentDir), 0700); e != nil {
				t.Fatal(e)
			}
			if e := os.Symlink(foreign, link); e != nil {
				t.Fatal(e)
			}
			body := map[string]any{"action": "trial", "workflow_id": d.ID, "request_id": "reject-symlink", "expected_revision": d.Revision, "expected_hash": d.DefinitionHash}
			if container == "agent-directory" {
				body["files"] = []string{"secret.md"}
			}
			awTestCall(t, h, aliceToken, a.ID, "run", body, 400)
			rows, e := os.ReadDir(foreign)
			if e != nil || len(rows) != 1 || rows[0].Name() != "secret.md" {
				t.Fatalf("foreign data was written/deleted: %v %v", rows, e)
			}
			b, e := os.ReadFile(secret)
			if e != nil || string(b) != "BOB-PRIVATE-MATERIAL" {
				t.Fatal("foreign source changed", e)
			}
			runs := awTestValue[[]af.Run](t, awTestCall(t, h, aliceToken, a.ID, "read", map[string]any{"action": "runs", "workflow_id": d.ID}, 200), "runs")
			if len(runs) != 0 {
				t.Fatal("escaped workspace was admitted")
			}
		})
	}
}

func TestAgentWorkflowInputsFreezeAndTamperedRetryRejected(t *testing.T) {
	h := newMultiuserHarness(t, func(int) (llm.Response, error) {
		t.Fatal("confirmation-only flow called model")
		return textTurn("unused"), nil
	})
	a := awTestAgent(t, h, "冻结资料助手")
	source := h.studioConversationAs(aliceToken, a.ID)
	d := awSecurityConfirmFlow(t, h, a.ID)
	data := []byte("QA-FROZEN-INPUT original material")
	path := filepath.Join(h.session(source).cwd, "source.md")
	if e := os.WriteFile(path, data, 0600); e != nil {
		t.Fatal(e)
	}
	accepted := awTestValue[af.Run](t, awTestCall(t, h, aliceToken, a.ID, "run", map[string]any{"action": "trial", "workflow_id": d.ID, "request_id": "freeze", "expected_revision": d.Revision, "expected_hash": d.DefinitionHash, "source_session_id": source, "files": []string{"source.md"}}, 200), "run")
	awTestRun(t, h, aliceToken, a.ID, d.ID, accepted.ID, af.WaitingConfirmation)
	owner := identity.LocalScope("alice")
	private, e := taskRunsDB(t, h.mgr).AWRun(sessionResultOwnerKey(&owner), a.ID, d.ID, accepted.ID)
	if e != nil {
		t.Fatal(e)
	}
	if len(private.Files) != 1 {
		t.Fatal("input snapshot missing")
	}
	if e = os.WriteFile(path, []byte("NEW LIVE SOURCE"), 0600); e != nil {
		t.Fatal(e)
	}
	frozen, e := h.mgr.awReadOwned(&owner, filepath.Join(private.Workspace, private.Files[0].Path), 4<<20)
	sum := sha256.Sum256(frozen)
	if e != nil || string(frozen) != string(data) || hex.EncodeToString(sum[:]) != private.Files[0].SHA256 {
		t.Fatal("accepted input changed with live file", e)
	}
	awTestCall(t, h, aliceToken, a.ID, "run", map[string]any{"action": "cancel", "workflow_id": d.ID, "run_id": private.ID, "request_id": "stop"}, 200)
	if e = os.WriteFile(filepath.Join(private.Workspace, private.Files[0].Path), []byte("TAMPERED SNAPSHOT"), 0600); e != nil {
		t.Fatal(e)
	}
	awTestCall(t, h, aliceToken, a.ID, "run", map[string]any{"action": "retry", "workflow_id": d.ID, "run_id": private.ID, "request_id": "tampered-retry"}, 400)
	runs := awTestValue[[]af.Run](t, awTestCall(t, h, aliceToken, a.ID, "read", map[string]any{"action": "runs", "workflow_id": d.ID}, 200), "runs")
	if len(runs) != 1 {
		t.Fatal("tampered input minted a retry")
	}
}

func TestAgentWorkflowArtifactSnapshotIndependentOfEditableWorkspace(t *testing.T) {
	model := &awReportClient{}
	h := newMultiuserHarness(t, nil, func(c *Config) { c.NewClient = func(config.Resolved, func(llm.RetryInfo)) llm.Client { return model } })
	a := awTestAgent(t, h, "产物历史助手", "write")
	source := h.studioConversationAs(aliceToken, a.ID)
	if e := os.WriteFile(filepath.Join(h.session(source).cwd, "source.md"), []byte("QA-SOURCE-731：合成资料预算1000元。"), 0600); e != nil {
		t.Fatal(e)
	}
	d := awTestValue[af.Definition](t, awTestCall(t, h, aliceToken, a.ID, "edit", map[string]any{"action": "create", "request_id": "artifact-flow", "name": "产物快照测试", "steps": []af.Step{{ID: "collect", Kind: "agent", Task: "阅读所选材料，保存要点和来源。", OutputFile: "notes.md"}, {ID: "review", Kind: "confirm", Prompt: "确认 {{ steps.collect.summary }}"}}}, 200), "workflow")
	accepted := awTestValue[af.Run](t, awTestCall(t, h, aliceToken, a.ID, "run", map[string]any{"action": "trial", "workflow_id": d.ID, "request_id": "artifact-trial", "expected_revision": d.Revision, "expected_hash": d.DefinitionHash, "source_session_id": source, "files": []string{"source.md"}}, 200), "run")
	waiting := awTestRun(t, h, aliceToken, a.ID, d.ID, accepted.ID, af.WaitingConfirmation)
	if !af.ValidHash(waiting.Steps[0].ArtifactSHA256) {
		t.Fatal("artifact did not retain digest")
	}
	owner := identity.LocalScope("alice")
	private, e := taskRunsDB(t, h.mgr).AWRun(sessionResultOwnerKey(&owner), a.ID, d.ID, accepted.ID)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(private.Workspace, "notes.md"), []byte("EDITED WORKING FILE"), 0600); e != nil {
		t.Fatal(e)
	}
	query := map[string]any{"action": "artifact", "workflow_id": d.ID, "run_id": private.ID, "step_id": "collect"}
	artifact := awTestCall(t, h, aliceToken, a.ID, "read", query, 200)
	text := awTestValue[string](t, artifact, "text")
	if !strings.Contains(text, "QA-SOURCE-731") || strings.Contains(text, "EDITED WORKING FILE") {
		t.Fatal("historical output follows mutable workspace", text)
	}
	if e = os.WriteFile(filepath.Join(h.mgr.cfg.SessionDir, h.mgr.awArtifactPath(private.ID, "collect")), []byte("CORRUPTED PRIVATE SNAPSHOT"), 0600); e != nil {
		t.Fatal(e)
	}
	awTestCall(t, h, aliceToken, a.ID, "read", query, 400)
}
