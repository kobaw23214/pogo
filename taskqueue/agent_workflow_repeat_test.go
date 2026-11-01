package taskqueue

// These tests construct simulated execution metadata to exercise the durable
// storage boundary. No script/model/process is started or claimed as tested.
import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	af "github.com/yosukeno/pogo/agentworkflow"
)

func repeatStoreCopy[T any](value T) T {
	raw, _ := json.Marshal(value)
	var copy T
	_ = json.Unmarshal(raw, &copy)
	return copy
}

func repeatStoreMetadata(spec *af.CodeSpec, errorCode string, exit *int) *af.CodeExecution {
	digest := sha256.Sum256([]byte(spec.Source))
	return &af.CodeExecution{Language: spec.Language, Runtime: "store-fixture-metadata-only", SourceSHA256: hex.EncodeToString(digest[:]), ErrorCode: errorCode, ExitCode: exit}
}

func repeatStoreStart(t *testing.T, s *Store, mode string, step af.Step) (af.Run, Run) {
	t.Helper()
	request := af.EditRequest{Action: "create", RequestID: "create-boundary", Name: "逐项存储边界", Steps: []af.Step{step}}
	if mode == "graph" {
		request.Graph = &af.Graph{Version: 1, Edges: []af.Edge{{Source: "__input", Target: step.ID}, {Source: step.ID, Target: "__output"}}}
	}
	d, _, err := s.AWEdit("alice", "research", request)
	if err != nil {
		t.Fatal(err)
	}
	r := awRunFixture(d, "repeat-store-boundary")
	r.Inputs = map[string]string{}
	r, q, _, err := s.AWAdmit(r, "start-boundary")
	if err != nil {
		t.Fatal(err)
	}
	q, err = s.Claim(q.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	after := r.Steps[0]
	after.Status, after.StartedAt = af.Running, now()
	if step.Kind == "code" {
		after.CodeInputs = json.RawMessage(`{}`)
	}
	r, err = repeatStoreSave(s, mode, r, q, after)
	if err != nil {
		t.Fatal("start fixture", err)
	}
	return r, q
}

func repeatStoreSave(s *Store, mode string, r af.Run, q Run, after af.StepRun) (af.Run, error) {
	if mode == "graph" {
		return s.AWGraphSaveStep(r.OwnerID, r.AgentID, r.WorkflowID, r.ID, q.ID, q.Revision, r.Steps[0], after)
	}
	r = repeatStoreCopy(r)
	r.Status = af.Running
	r.Steps[0] = after
	return s.AWUpdate(r, r.Revision, q.ID, q.Revision)
}

func TestAgentWorkflowRepeatStoreFallbackRequiresExecutedRecoverableFailure(t *testing.T) {
	for _, mode := range []string{"sequence", "graph"} {
		t.Run(mode, func(t *testing.T) {
			s, _ := testStore(t)
			step := af.Step{ID: "calculate", Kind: "code", Code: &af.CodeSpec{Language: "python", Source: "def main(inputs):\n    raise ValueError('store fixture')\n", Inputs: map[string]af.CodeBinding{}, Outputs: map[string]af.ValueSchema{"value": {Type: "integer"}}}, Policy: &af.CodePolicy{OnFailure: "fallback", Fallback: json.RawMessage(`{"value":0}`)}}
			r, q := repeatStoreStart(t, s, mode, step)
			exit := 0
			makeFallback := func(errorCode string, exitCode *int) af.StepRun {
				st := repeatStoreCopy(r.Steps[0])
				st.Status, st.FinishedAt = af.Completed, now()
				st.Output = json.RawMessage(`{"value":0}`)
				st.FallbackUsed = true
				st.CodeExecution = repeatStoreMetadata(step.Code, errorCode, exitCode)
				st.Attempts = []af.CodeAttempt{{Number: 1, StartedAt: st.StartedAt, FinishedAt: st.FinishedAt, Execution: repeatStoreCopy(st.CodeExecution), Error: "simulated execution failure"}}
				return st
			}
			for _, bad := range []af.StepRun{makeFallback("execution_failed", nil), makeFallback("execution_failed", &exit), makeFallback("code_error", nil)} {
				_, err := repeatStoreSave(s, mode, r, q, bad)
				awExpect(t, err, ErrConflict)
			}
			mismatched := makeFallback("code_error", &exit)
			mismatched.Output = json.RawMessage(`{"value":9}`)
			_, err := repeatStoreSave(s, mode, r, q, mismatched)
			awExpect(t, err, ErrConflict)
			accepted, err := repeatStoreSave(s, mode, r, q, makeFallback("code_error", &exit))
			if err != nil {
				t.Fatal("actual recoverable failure with declared fallback rejected", err)
			}
			if !accepted.Steps[0].FallbackUsed || accepted.Steps[0].CodeExecution.ErrorCode != "code_error" || string(accepted.Steps[0].Output) != `{"value":0}` {
				t.Fatal("fallback hid actual failed execution", accepted.Steps[0])
			}
		})
	}
}

func repeatStoreInFlight(t *testing.T, s *Store, mode string) (af.Run, Run) {
	t.Helper()
	body := &af.Step{Kind: "code", Code: &af.CodeSpec{Language: "python", Source: "def main(inputs):\n    if inputs['item'] == 2: raise ValueError('store fixture')\n    return {'value': inputs['item']}\n", Inputs: map[string]af.CodeBinding{"item": {Type: "integer", Source: "iteration.item"}}, Outputs: map[string]af.ValueSchema{"value": {Type: "integer"}}}, Policy: &af.CodePolicy{MaxRetries: 1}}
	step := af.Step{ID: "batch", Kind: "iteration", Iteration: &af.IterationSpec{Items: af.CodeBinding{Type: "array", Value: json.RawMessage(`[1,2,3]`)}, Body: body, Concurrency: 2, MaxItems: 3}}
	r, q := repeatStoreStart(t, s, mode, step)
	st := repeatStoreCopy(r.Steps[0])
	st.Repeat = &af.RepeatRun{Kind: "iteration", Total: 3, Items: []af.RepeatItem{{Index: 0, Status: af.Queued, Input: json.RawMessage(`1`)}, {Index: 1, Status: af.Queued, Input: json.RawMessage(`2`)}, {Index: 2, Status: af.Queued, Input: json.RawMessage(`3`)}}}
	var err error
	r, err = repeatStoreSave(s, mode, r, q, st)
	if err != nil {
		t.Fatal("initial items", err)
	}
	st = repeatStoreCopy(r.Steps[0])
	first := &st.Repeat.Items[0]
	first.Status, first.StartedAt = af.Running, now()
	first.CodeInputs = json.RawMessage(`{"item":1}`)
	first.Attempts = []af.CodeAttempt{{Number: 1, StartedAt: first.StartedAt}}
	r, err = repeatStoreSave(s, mode, r, q, st)
	if err != nil {
		t.Fatal("first running", err)
	}
	st = repeatStoreCopy(r.Steps[0])
	first = &st.Repeat.Items[0]
	first.Status, first.FinishedAt = af.Completed, now()
	first.Output = json.RawMessage(`{"value":1}`)
	zero := 0
	first.CodeExecution = repeatStoreMetadata(body.Code, "", &zero)
	first.Attempts[0].FinishedAt = first.FinishedAt
	first.Attempts[0].Execution = repeatStoreCopy(first.CodeExecution)
	first.Attempts[0].Output = append(json.RawMessage(nil), first.Output...)
	second := &st.Repeat.Items[1]
	second.Status, second.StartedAt = af.Running, now()
	second.CodeInputs = json.RawMessage(`{"item":2}`)
	second.CodeExecution = repeatStoreMetadata(body.Code, "code_error", &zero)
	second.Attempts = []af.CodeAttempt{{Number: 1, StartedAt: second.StartedAt, FinishedAt: now(), Execution: repeatStoreCopy(second.CodeExecution), Error: "simulated first attempt failure"}, {Number: 2, StartedAt: now()}}
	r, err = repeatStoreSave(s, mode, r, q, st)
	if err != nil {
		t.Fatal("mixed complete/running/queued items", err)
	}
	return r, q
}

func TestAgentWorkflowRepeatStoreRestartFencesItemsWithoutRewritingAttempts(t *testing.T) {
	for _, mode := range []string{"sequence", "graph"} {
		t.Run(mode, func(t *testing.T) {
			s, _ := testStore(t)
			r, q := repeatStoreInFlight(t, s, mode)
			completed, _ := json.Marshal(r.Steps[0].Repeat.Items[0])
			attempts, _ := json.Marshal(r.Steps[0].Repeat.Items[1].Attempts)
			if err := s.Recover(); err != nil {
				t.Fatal(err)
			}
			if err := s.AWRecover(); err != nil {
				t.Fatal(err)
			}
			recovered, err := s.AWRun(r.OwnerID, r.AgentID, r.WorkflowID, r.ID)
			if err != nil {
				t.Fatal(err)
			}
			if recovered.Status != af.Interrupted || recovered.Steps[0].Status != af.Interrupted {
				t.Fatal("outer run not fenced", recovered.Status)
			}
			items := recovered.Steps[0].Repeat.Items
			if items[1].Status != af.Interrupted || items[1].FinishedAt == 0 || items[2].Status != af.Skipped || items[2].StartedAt != 0 {
				t.Fatal("nested item statuses remain active", items)
			}
			gotCompleted, _ := json.Marshal(items[0])
			gotAttempts, _ := json.Marshal(items[1].Attempts)
			if !bytes.Equal(completed, gotCompleted) || !bytes.Equal(attempts, gotAttempts) {
				t.Fatal("restart rewrote completed item or actual attempt history")
			}
			if items[1].Attempts[1].FinishedAt != 0 || items[1].Attempts[1].Execution != nil {
				t.Fatal("restart fabricated a terminal execution receipt")
			}
			claim, err := s.Get(q.ID)
			if err != nil || claim.Status != Interrupted || claim.Revision <= q.Revision {
				t.Fatal("queue claim was not fenced", claim, err)
			}
			_, err = repeatStoreSave(s, mode, r, q, r.Steps[0])
			awExpect(t, err, ErrConflict)
		})
	}
}

func TestAgentWorkflowRepeatStoreRejectsHistoricalAndInputRewrites(t *testing.T) {
	for _, mode := range []string{"sequence", "graph"} {
		t.Run(mode, func(t *testing.T) {
			s, _ := testStore(t)
			r, q := repeatStoreInFlight(t, s, mode)
			mutations := []func(*af.StepRun){
				func(st *af.StepRun) { st.Repeat.Items[0].Summary = "rewrite completed item" },
				func(st *af.StepRun) {
					item := &st.Repeat.Items[1]
					item.Attempts[0].Execution.Stdout = "rewrite finished attempt"
					item.CodeExecution.Stdout = "rewrite finished attempt"
				},
				func(st *af.StepRun) { st.Repeat.Items[1].Input = json.RawMessage(`99`) },
				func(st *af.StepRun) { st.Repeat.Items[1].Attempts[1].StartedAt++ },
			}
			for i, change := range mutations {
				st := repeatStoreCopy(r.Steps[0])
				change(&st)
				if _, err := repeatStoreSave(s, mode, r, q, st); err != ErrConflict {
					t.Fatalf("mutation %d accepted or wrong failure: %v", i, err)
				}
			}
			unchanged, err := s.AWRun(r.OwnerID, r.AgentID, r.WorkflowID, r.ID)
			if err != nil {
				t.Fatal(err)
			}
			old, _ := json.Marshal(r.Steps[0])
			current, _ := json.Marshal(unchanged.Steps[0])
			if !bytes.Equal(old, current) || unchanged.Revision != r.Revision {
				t.Fatal("rejected write changed actual history")
			}
		})
	}
}
