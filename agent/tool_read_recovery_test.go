package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

func recoveryQueryForTest(id string, offset int) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"result_id":%q,"view":"query","pointer":"/rows","offset":%d}`, id, offset))
}

func recoveryFailureForTest(t *testing.T, a *Agent, args, next json.RawMessage, budget int64) {
	t.Helper()
	key, valid := tools.ToolResultRequestKey(args)
	if !valid {
		t.Fatalf("invalid test request: %s", args)
	}
	a.observeToolReadDelivery(args, &tools.ModelResultDelivery{RequestKey: key, Undeliverable: true, StatisticsDelivered: true, NextRequest: next}, budget)
}

func TestToolReadRecoveryGuardNormalizesSemanticRequestsAndAllowsProgress(t *testing.T) {
	registry, _ := budgetRegistryForTest(t)
	a := New(Config{Client: &fakeClient{}, Registry: registry, ToolOutputBudget: ToolOutputBudgetConfig{PerResultTokens: 384, BatchTokens: 2048}})
	id := savedResultIDForTest("3")
	base := json.RawMessage(`{"result_id":"` + id + `","view":"query","pointer":"/rows"}`)
	next := recoveryQueryForTest(id, 20)
	recoveryFailureForTest(t, a, base, next, 384)
	equivalent := json.RawMessage(`{"select":[],"where":[],"offset":0,"limit":20,"include_rows":true,"pointer":"/rows","view":"query","result_id":"` + id + `"}`)
	for _, request := range []json.RawMessage{base, equivalent} {
		receipt, blocked := a.repeatedToolReadReceipt(request)
		var decoded struct {
			Executed bool            `json:"request_executed"`
			Repeated bool            `json:"repeated_undeliverable"`
			Next     json.RawMessage `json:"next_request"`
		}
		if !blocked || len(receipt) > 2048 || json.Unmarshal([]byte(receipt), &decoded) != nil || decoded.Executed || !decoded.Repeated || string(decoded.Next) != string(next) {
			t.Fatalf("equivalent request bypassed the guard or lost bounded continuation: %s / %v", receipt, blocked)
		}
	}
	for _, request := range []json.RawMessage{
		next,
		json.RawMessage(`{"result_id":"` + id + `","view":"query","pointer":"/rows","select":["/id"]}`),
		json.RawMessage(`{"result_id":"` + id + `","view":"query","pointer":"/other"}`),
		json.RawMessage(`{"result_id":"` + id + `","view":"query","pointer":"/rows","where":[{"pointer":"/status","op":"eq","value":"active"}]}`),
	} {
		if _, blocked := a.repeatedToolReadReceipt(request); blocked {
			t.Fatalf("a narrower/new selection was blocked: %s", request)
		}
	}
	// Budget changes are normally made between runs (and reset the guard).
	// Directly vary the current cap here to isolate its comparison contract.
	a.toolOutputBudget.PerResultTokens = 256
	if _, blocked := a.repeatedToolReadReceipt(base); !blocked {
		t.Fatal("a lower model budget bypassed the repeated-undeliverable guard")
	}
	a.toolOutputBudget.PerResultTokens = 512
	if _, blocked := a.repeatedToolReadReceipt(base); blocked {
		t.Fatal("genuinely greater model capacity was not allowed to retry")
	}
}

func TestToolReadRecoveryGuardUsesPostApprovalArgumentsAndNativeTool(t *testing.T) {
	registry, _ := budgetRegistryForTest(t)
	a := New(Config{Client: &fakeClient{}, Registry: registry, ToolOutputBudget: ToolOutputBudgetConfig{PerResultTokens: 384, BatchTokens: 2048}})
	id := savedResultIDForTest("4")
	blockedArgs, changedArgs := recoveryQueryForTest(id, 0), recoveryQueryForTest(id, 20)
	recoveryFailureForTest(t, a, blockedArgs, changedArgs, 384)
	call := llm.Block{Type: llm.BlockToolUse, ID: "reviewed", Name: tools.ToolResultReadName, Input: blockedArgs}
	a.gate = gateFunc(func(GateRequest) GateDecision { return GateDecision{Allow: true, Args: changedArgs} })
	rv := a.review(context.Background(), call, 1, llm.StopToolUse)
	if rv.settled || string(rv.args) != string(changedArgs) {
		t.Fatal("guard checked the pre-approval request instead of the actual narrowed request")
	}
	actual, exists := a.takeToolReadExecution(call.ID, true)
	if !exists || string(actual) != string(changedArgs) {
		t.Fatal("delivery accounting did not capture the arguments that will execute")
	}
	a.gate = gateFunc(func(GateRequest) GateDecision { return GateDecision{Allow: true, Args: blockedArgs} })
	call.Input = changedArgs
	rv = a.review(context.Background(), call, 1, llm.StopToolUse)
	if !rv.settled || !rv.recoverySuppressed || !rv.isError || !strings.Contains(rv.output, `"request_executed":false`) {
		t.Fatal("a rewrite to a known-undeliverable selection bypassed the guard")
	}
	a.gate = nil
	a.registry = tools.NewRegistry(&fakeTool{name: tools.ToolResultReadName})
	if rv := a.review(context.Background(), call, 1, llm.StopToolUse); rv.settled {
		t.Fatal("same-named non-native tools were intercepted by the saved-result guard")
	}
}

func TestToolReadRecoveryGuardDoesNotRetainUncertainOrDeliveredFailures(t *testing.T) {
	registry, _ := budgetRegistryForTest(t)
	a := New(Config{Client: &fakeClient{}, Registry: registry, ToolOutputBudget: ToolOutputBudgetConfig{PerResultTokens: 384, BatchTokens: 2048}})
	args := recoveryQueryForTest(savedResultIDForTest("5"), 0)
	key, _ := tools.ToolResultRequestKey(args)
	for _, delivery := range []*tools.ModelResultDelivery{
		nil,
		{RequestKey: "not the request key", Undeliverable: true},
		{RequestKey: key, Delivered: true},
		{RequestKey: key, Delivered: true, StatisticsDelivered: true},
	} {
		recoveryFailureForTest(t, a, args, nil, 384)
		a.observeToolReadDelivery(args, delivery, 384)
		if _, blocked := a.repeatedToolReadReceipt(args); blocked {
			t.Fatalf("usable/uncertain delivery kept a stale execution guard: %+v", delivery)
		}
	}
	// Exact old cached text without matching delivery metadata cannot justify a
	// new guard, even when a newly generated discarded view would be omitted.
	recoveryFailureForTest(t, a, args, nil, 384)
	result := llm.Block{Type: llm.BlockToolResult, ToolUseID: "old-result", Text: strings.Repeat("original ", 1000)}
	a.toolResultViews = map[toolResultPosition]cachedToolResultView{{0, 0}: {
		originalText: result.Text, callID: result.ToolUseID, text: "old retained text", budget: 384,
	}}
	a.rememberToolReadExecution(result.ToolUseID, args)
	a.cacheToolBatch(context.Background(), []llm.Block{{Name: tools.ToolResultReadName, Input: args}}, []llm.Block{result}, 0, 0)
	if _, blocked := a.repeatedToolReadReceipt(args); blocked {
		t.Fatal("old cached text with unknown delivery status falsely blocked a read")
	}
	// Historical reconstruction must not turn an earlier run's omissions into
	// active per-run execution guards.
	a.rememberToolReadExecution("pending", args)
	if _, consumed := a.takeToolReadExecution("pending", false); consumed {
		t.Fatal("history reconstruction consumed a live request")
	}
	if _, consumed := a.takeToolReadExecution("pending", true); !consumed {
		t.Fatal("historical reconstruction erased pending live arguments")
	}
}

func TestToolReadRecoveryGuardIgnoresDiscardedOmissionMetadata(t *testing.T) {
	ctx := context.Background()
	registry, _ := budgetRegistryForTest(t)
	prepared := registry.PrepareResult(ctx, "fixture", tools.Result{Text: `{"v":42}`}, nil)
	args := json.RawMessage(`{"result_id":"` + prepared.ResultID + `","view":"json","pointer":"/v"}`)
	// This short trusted scalar body already contains its useful value. A long
	// mandatory citation leaves zero renderer space; the larger omission receipt
	// must lose to the original text, including for recovery-guard accounting.
	body := `{"result_id":"` + prepared.ResultID + `","view":"json","pointer":"/v","value":42}`
	source := &Source{Label: "S1", CallID: "scalar-read", Type: SourceTool, Title: "fixture"}
	prefix := "[cite:S1 | tool_result | " + strings.Repeat("title", 140) + "]\n"
	result := llm.Block{Type: llm.BlockToolResult, ToolUseID: source.CallID, Text: prefix + body, Source: marshalSource(source)}
	a := New(Config{Client: &fakeClient{}, Registry: registry, ToolOutputBudget: ToolOutputBudgetConfig{PerResultTokens: 128, BatchTokens: 128}})
	recoveryFailureForTest(t, a, args, nil, 128)
	a.rememberToolReadExecution(result.ToolUseID, args)
	a.cacheToolBatch(ctx, []llm.Block{{Name: tools.ToolResultReadName, Input: args}}, []llm.Block{result}, 0, 0)
	a.messages = []llm.Message{{Role: llm.RoleUser, Content: []llm.Block{result}}}
	if view := a.cachedToolPrompt(a.messages)[0].Content[0]; view.Text != result.Text {
		t.Fatalf("fixture did not retain the original scalar after the soft ceiling: %s", view.Text)
	}
	if _, blocked := a.repeatedToolReadReceipt(args); blocked {
		t.Fatal("discarded omission metadata blocked a scalar that the model actually received")
	}
}

func TestToolReadRecoveryGuardResetsAtLifecycleBoundaries(t *testing.T) {
	for _, boundary := range []string{"messages", "model", "budget", "run", "compact"} {
		t.Run(boundary, func(t *testing.T) {
			registry, _ := budgetRegistryForTest(t)
			a := New(Config{Client: &fakeClient{}, Registry: registry, ToolOutputBudget: ToolOutputBudgetConfig{PerResultTokens: 384, BatchTokens: 2048}})
			args := recoveryQueryForTest(savedResultIDForTest("6"), 0)
			recoveryFailureForTest(t, a, args, nil, 384)
			switch boundary {
			case "messages":
				a.SetMessages(nil)
			case "model":
				a.SetClient(&fakeClient{})
			case "budget":
				a.SetToolOutputBudget(a.toolOutputBudget)
			case "run":
				drain(a.Run(context.Background(), "new human request"))
			case "compact":
				a.client = &summariser{reply: "A short summary retaining the recovery constraints."}
				a.messages = []llm.Message{llm.UserText("Inspect the saved data"), {Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockText, Text: strings.Repeat("past observations ", 1500)}}}}
				if _, err := a.Compact(context.Background()); err != nil {
					t.Fatalf("real compaction did not commit: %v", err)
				}
			}
			if len(a.toolReadRecovery.blocked) != 0 || len(a.toolReadRecovery.executed) != 0 {
				t.Fatal("lifecycle boundary retained stale recovery state")
			}
			if _, blocked := a.repeatedToolReadReceipt(args); blocked {
				t.Fatal("a new history/run/model was blocked by an older omission")
			}
		})
	}
}

func TestToolReadRecoveryFailedCompactionCommitRetainsGuard(t *testing.T) {
	registry, _ := budgetRegistryForTest(t)
	a := New(Config{Client: &summariser{reply: "A short summary."}, Registry: registry, ToolOutputBudget: ToolOutputBudgetConfig{PerResultTokens: 384, BatchTokens: 2048}})
	a.messages = []llm.Message{llm.UserText("Inspect the saved data"), {Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockText, Text: strings.Repeat("past observations ", 1500)}}}}
	args := recoveryQueryForTest(savedResultIDForTest("8"), 0)
	recoveryFailureForTest(t, a, args, nil, 384)
	before, _ := json.Marshal(a.Messages())
	commitErr := errors.New("test durable commit failed")
	_, err := a.CompactWithCommit(context.Background(), 0, func([]llm.Message) error { return commitErr })
	if !errors.Is(err, commitErr) {
		t.Fatalf("compaction did not report its failed commit: %v", err)
	}
	after, _ := json.Marshal(a.Messages())
	if string(before) != string(after) || len(a.toolReadRecovery.blocked) != 1 {
		t.Fatal("failed durable compaction changed history or erased recovery protection")
	}
	if _, blocked := a.repeatedToolReadReceipt(args); !blocked {
		t.Fatal("failed durable compaction dropped its unchanged read guard")
	}
}

func TestToolReadRecoveryGuardBoundsStateAndReceipt(t *testing.T) {
	registry, _ := budgetRegistryForTest(t)
	a := New(Config{Client: &fakeClient{}, Registry: registry, ToolOutputBudget: ToolOutputBudgetConfig{PerResultTokens: 384, BatchTokens: 2048}})
	id := savedResultIDForTest("7")
	for offset := 0; offset < 150; offset++ {
		recoveryFailureForTest(t, a, recoveryQueryForTest(id, offset), nil, 384)
	}
	if len(a.toolReadRecovery.blocked) != 128 {
		t.Fatalf("recovery guard state grew beyond its bounded window: %d", len(a.toolReadRecovery.blocked))
	}
	if _, blocked := a.repeatedToolReadReceipt(recoveryQueryForTest(id, 0)); blocked {
		t.Fatal("oldest guard was not evicted")
	}
	args := recoveryQueryForTest(id, 149)
	if receipt, blocked := a.repeatedToolReadReceipt(args); !blocked || len(receipt) > 2048 || !json.Valid([]byte(receipt)) {
		t.Fatal("most recent guard lost its bounded, valid receipt")
	}
}

func TestToolReadRecoveryLoopSkipsRepeatedScanAndDoesNotCreateEvidence(t *testing.T) {
	ctx := context.Background()
	registry, options := budgetRegistryForTest(t)
	body := `{"rows":[{"payload":"` + strings.Repeat("x", 8000) + `"}]}`
	prepared := registry.PrepareResult(ctx, "fixture", tools.Result{Text: body}, nil)
	first := json.RawMessage(`{"result_id":"` + prepared.ResultID + `","view":"json","pointer":"/rows","limit":1}`)
	second := json.RawMessage(`{"offset":0,"pointer":"/rows","view":"json","result_id":"` + prepared.ResultID + `","limit":1}`)
	response := func(id string, args json.RawMessage) llm.Response {
		return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockToolUse, ID: id, Name: tools.ToolResultReadName, Input: args}}}, StopReason: llm.StopToolUse}
	}
	client := &fakeClient{responses: []llm.Response{response("first-read", first), response("duplicate-read", second)}}
	var deletionErr error
	deleted := 0
	client.onStream = func(request int) {
		if request != 2 {
			return
		}
		// Delivery accounting has completed before this model request. Remove
		// the immutable record to prove that the following duplicate is stopped
		// before execution, rather than doing another failing filesystem read.
		deletionErr = filepath.WalkDir(options.SpillDir, func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !entry.IsDir() && strings.HasPrefix(entry.Name(), "result-") && strings.HasSuffix(entry.Name(), ".log") {
				deleted++
				return os.Remove(path)
			}
			return nil
		})
	}
	a := New(Config{Client: client, Registry: registry, Citations: true, ToolOutputBudget: ToolOutputBudgetConfig{PerResultTokens: 128, BatchTokens: 128}})
	events := drain(a.Run(ctx, "Read the selected page"))
	if deletionErr != nil || deleted == 0 {
		t.Fatalf("record deletion probe failed: %v, deleted %d", deletionErr, deleted)
	}
	var firstResult, duplicate llm.Block
	for _, message := range a.Messages() {
		for _, result := range message.Content {
			if result.Type == llm.BlockToolResult && result.ToolUseID == "first-read" {
				firstResult = result
			}
			if result.Type == llm.BlockToolResult && result.ToolUseID == "duplicate-read" {
				duplicate = result
			}
		}
	}
	if !strings.Contains(firstResult.Text, strings.Repeat("x", 100)) || !duplicate.IsError || len(duplicate.Source) != 0 || duplicate.ResultID != "" || !strings.Contains(duplicate.Text, `"request_executed":false`) {
		t.Fatalf("original page changed or repeated scan was not intercepted: original=%d bytes duplicate=%+v", len(firstResult.Text), duplicate)
	}
	for _, event := range events {
		if event.Kind == EventToolEnd && event.ToolCallID == duplicate.ToolUseID && (event.Source != nil || event.ToolOutput != duplicate.Text) {
			t.Fatal("synthetic guard receipt became a fresh source or changed the UI body")
		}
	}
}
