package skillruntime

// The Service-level call path must emit pogo.tool/1 frames the schema and the
// providers accept. Executor-level tests hand-build their own requests, so a
// drift in Service's construction code (an invoke request once shipped with
// type "call") sailed past every test and only surfaced as provider-side
// UNSUPPORTED_OPERATION errors in production. These tests drive the real
// Service path end to end with a recording executor and assert on the frames
// it actually builds, plus the executor-boundary schema gate itself.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
)

// recordingExecutor captures every Execution the Service constructs and
// answers with the minimal schema-conformant response for each request type,
// like a provider binary would.
type recordingExecutor struct {
	operation string
	contract  ContractIdentity

	mu     sync.Mutex
	events []Execution
}

func (r *recordingExecutor) Identity() string { return "local-bwrap-v1:" + strings.Repeat("ab", 32) }

func (r *recordingExecutor) Execute(ctx context.Context, in Execution) ([]byte, error) {
	out, err := r.ExecuteDetailed(ctx, in)
	return out.Output, err
}

func (r *recordingExecutor) ExecuteDetailed(_ context.Context, in Execution) (DetailedResult, error) {
	r.mu.Lock()
	r.events = append(r.events, in)
	r.mu.Unlock()
	frame := map[string]any{"protocolVersion": "pogo.tool/1", "callId": in.Request.CallID}
	switch in.Request.Type {
	case ProcessRequestDescribe:
		frame["type"] = ProcessRequestDescribe
		frame["operations"] = []any{map[string]any{"operation": r.operation, "contract": r.contract}}
	case ProcessRequestInvoke:
		frame["type"] = "result"
		frame["output"] = map[string]any{"ok": true}
		frame["artifacts"] = []any{}
	default:
		frame["type"] = "error"
		frame["error"] = map[string]string{"code": "UNSUPPORTED_OPERATION", "message": fmt.Sprintf("unknown request type %q", in.Request.Type)}
	}
	raw, err := json.Marshal(frame)
	if err != nil {
		return DetailedResult{}, err
	}
	return DetailedResult{Output: raw}, nil
}

func TestServiceCallPathEmitsInvokeFrames(t *testing.T) {
	s := newStubService(t)
	installed, refs := installSkillFixture(t, s)
	rec := &recordingExecutor{operation: "inspect", contract: ContractIdentity{ID: "test.file.inspect", Version: "1.0.0", Digest: refs["inspect"].Digest}}
	s.Executor = rec

	plan, err := s.PlanSelection(Selection{SkillIDs: []string{PackageSkillID(installed.Ref)}})
	if err != nil || !plan.Ready {
		t.Fatalf("fixture selection not ready: %+v %v", plan, err)
	}
	if err := s.Prepare(context.Background(), plan.Binding); err != nil {
		t.Fatalf("describe handshake failed: %v", err)
	}
	lock, err := s.MakeLock(plan.Binding, "sess-wire")
	if err != nil {
		t.Fatal(err)
	}
	bound, err := s.Session(plan.Binding, lock, "sess-wire")
	if err != nil {
		t.Fatal(err)
	}
	uploaded, err := bound.Upload([]byte("payload"))
	if err != nil {
		t.Fatal(err)
	}
	input, err := json.Marshal(map[string]string{"fileId": uploaded.FileID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bound.Call(context.Background(), "test.file.inspect", input); err != nil {
		t.Fatalf("service-level call failed: %v", err)
	}

	events := rec.snapshot()
	if len(events) < 2 {
		t.Fatalf("expected describe + invoke executions, got %d", len(events))
	}
	for _, e := range events {
		if e.Request.ProtocolVersion != "pogo.tool/1" {
			t.Fatalf("protocol version = %q", e.Request.ProtocolVersion)
		}
	}
	invoke := events[len(events)-1].Request
	if invoke.Type != ProcessRequestInvoke {
		t.Fatalf("invoke request type = %q, want %q (schema enum, provider gate)", invoke.Type, ProcessRequestInvoke)
	}
	if invoke.Operation != "inspect" || invoke.Contract == nil || invoke.Contract.Digest != refs["inspect"].Digest {
		t.Fatalf("invoke identity drifted: op=%+v contract=%+v", invoke.Operation, invoke.Contract)
	}
	if !strings.HasPrefix(invoke.CallID, "call:") {
		t.Fatalf("call id = %q", invoke.CallID)
	}
}

func TestMarshalOutboundRequestGatesTypeDrift(t *testing.T) {
	identity := ContractIdentity{ID: "test.file.inspect", Version: "1.0.0", Digest: "sha256:" + strings.Repeat("a", 64)}
	frame := func(typ string) ProcessRequest {
		return ProcessRequest{
			ProtocolVersion: "pogo.tool/1",
			CallID:          "call:wire",
			Type:            typ,
			Operation:       "inspect",
			Contract:        &identity,
			Input:           json.RawMessage(`{"fileId":"file:test"}`),
			Context: &ProcessContext{
				Files: []ProcessFile{{InputPointer: "/fileId", FileID: "file:test", Path: "/input/0.bin", Digest: "sha256:" + strings.Repeat("b", 64)}},
				Assets: []ProcessAsset{{AssetID: "samples", Path: "/assets/samples", Digest: "sha256:" + strings.Repeat("c", 64)}},
			},
		}
	}
	if _, err := marshalOutboundRequest(func() *ProcessRequest { r := frame(ProcessRequestInvoke); return &r }()); err != nil {
		t.Fatalf("conformant invoke frame rejected: %v", err)
	}
	if _, err := marshalOutboundRequest(&ProcessRequest{ProtocolVersion: "pogo.tool/1", CallID: "call:wire", Type: ProcessRequestDescribe}); err != nil {
		t.Fatalf("conformant describe frame rejected: %v", err)
	}
	drifted := frame("call")
	if _, err := marshalOutboundRequest(&drifted); err == nil || !strings.Contains(err.Error(), "pogo.tool/1") {
		t.Fatalf("type drift must fail the schema gate: %v", err)
	}
}

func (r *recordingExecutor) snapshot() []Execution {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Execution(nil), r.events...)
}
