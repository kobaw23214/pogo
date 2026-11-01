package taskqueue

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	af "github.com/yosukeno/pogo/agentworkflow"
)

func TestAgentWorkflowCodeCancellationWinsCompletionRace(t *testing.T) {
	for _, mode := range []string{"sequence", "graph", "node_test"} {
		t.Run(mode, func(t *testing.T) {
			s, _ := testStore(t)
			step := af.Step{ID: "calculate", Kind: "code", Code: &af.CodeSpec{Language: "javascript", Source: "function main(inputs) { return {value:1}; }", Inputs: map[string]af.CodeBinding{}, Outputs: map[string]af.ValueSchema{"value": {Type: "integer"}}}}
			request := af.EditRequest{Action: "create", RequestID: "create-code", Name: "取消竞态", Steps: []af.Step{step}}
			if mode == "graph" {
				request.Graph = &af.Graph{Version: 1, Edges: []af.Edge{{Source: "__input", Target: "calculate"}, {Source: "calculate", Target: "__output"}}}
			}
			d, _, err := s.AWEdit("alice", "research", request)
			if err != nil {
				t.Fatal(err)
			}
			r := awRunFixture(d, "code-race")
			r.Inputs = map[string]string{}
			if mode == "node_test" {
				r.NodeTest = &af.NodeTest{StepID: "calculate", Inputs: json.RawMessage(`{}`)}
			}
			r, q, _, err := s.AWAdmit(r, "start")
			if err != nil {
				t.Fatal(err)
			}
			q, err = s.Claim(q.ID, 10)
			if err != nil {
				t.Fatal(err)
			}
			before := r.Steps[0]
			running := before
			running.Status = af.Running
			running.StartedAt = now()
			running.CodeInputs = json.RawMessage(`{}`)
			if mode == "sequence" {
				r.Status = af.Running
				r.Steps[0] = running
				r, err = s.AWUpdate(r, r.Revision, q.ID, q.Revision)
			} else {
				r, err = s.AWGraphSaveStep(r.OwnerID, r.AgentID, d.ID, r.ID, q.ID, q.Revision, before, running)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err = s.AWCancel(r.OwnerID, r.AgentID, d.ID, r.ID); err != nil {
				t.Fatal(err)
			}
			// Simulate successful execution already in flight when cancellation
			// was persisted, before its context signal reaches the worker.
			completed := r.Steps[0]
			completed.Status = af.Completed
			completed.FinishedAt = now()
			completed.Output = json.RawMessage(`{"value":1}`)
			digest := sha256.Sum256([]byte(step.Code.Source))
			exit := 0
			completed.CodeExecution = &af.CodeExecution{Language: "javascript", SourceSHA256: hex.EncodeToString(digest[:]), ExitCode: &exit}
			if mode == "sequence" {
				r.Status = af.Completed
				r.CurrentStep = 1
				r.Steps[0] = completed
				r, err = s.AWUpdate(r, r.Revision, q.ID, q.Revision)
			} else {
				r, err = s.AWGraphSaveStep(r.OwnerID, r.AgentID, d.ID, r.ID, q.ID, q.Revision, r.Steps[0], completed)
				if err == nil {
					r, err = s.AWGraphSettle(r.OwnerID, r.AgentID, d.ID, r.ID, q.ID, q.Revision, r.Revision, af.Completed, "")
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if r.Status != af.Cancelled || !r.CancelRequested || r.Steps[0].Status != af.Completed || string(r.Steps[0].Output) != `{"value":1}` {
				t.Fatal("cancellation lost or actual receipt discarded", r)
			}
		})
	}
}
