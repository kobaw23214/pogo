package agent

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

// ToolOutputBudgetStats describes the text view sent to a model, not bytes
// removed from the transcript and not a provider tokenizer measurement.
type ToolOutputBudgetStats struct {
	Estimator              string `json:"estimator"`
	Batches                int    `json:"batches"`
	ReducedResults         int    `json:"reduced_results"`
	OriginalTokens         int64  `json:"original_tokens"`
	ViewTokens             int64  `json:"view_tokens"`
	FreedTokens            int64  `json:"freed_tokens"`
	BudgetOverruns         int    `json:"budget_overruns,omitempty"`
	UnrecoverableOverruns  int    `json:"unrecoverable_overruns,omitempty"`
	LastBatchKnown         bool   `json:"last_batch_known"`
	LastBatchTarget        int64  `json:"last_batch_target"`
	LastBatchFloor         int64  `json:"last_batch_required"`
	LastBatchViewTokens    int64  `json:"last_batch_view_tokens"`
	LastBatchOverrunTokens int64  `json:"last_batch_overrun_tokens,omitempty"`
	IdentityFloorOverrun   bool   `json:"identity_floor_overrun,omitempty"`
	LastHeadroom           int64  `json:"last_headroom"`
}

type toolResultPosition struct{ message, block int }

type cachedToolResultView struct {
	originalText    string
	originalID      string
	callID          string
	text            string
	resultID        string
	budget          int64
	recoverable     bool
	delivery        *tools.ModelResultDelivery
	deliveryBudget  int64
	initialDelivery *tools.ResultDeliveryRule
}

// Recovery guards describe only observations actually delivered during this run.
// Review and cacheToolResults both run on the loop goroutine; worker goroutines
// never access these maps. They are not restored from a transcript.
type toolReadRecoveryState struct {
	blocked  map[string]undeliveredToolRead
	executed map[string]json.RawMessage
	sequence uint64
}

type undeliveredToolRead struct {
	budget      int64
	sequence    uint64
	nextRequest json.RawMessage
}

func estimatedToolText(text string) int64 {
	return int64(len(text))/llm.BytesPerToken + boolTokenRemainder(len(text)%llm.BytesPerToken != 0)
}

func boolTokenRemainder(hasRemainder bool) int64 {
	if hasRemainder {
		return 1
	}
	return 0
}

// SetToolOutputBudget is called between runs with the new model's actual
// context and output limits. Keeping smaller cached views avoids unnecessary
// prompt-prefix churn when a later model has more available space.
func (a *Agent) SetToolOutputBudget(cfg ToolOutputBudgetConfig) {
	a.toolOutputBudget = cfg
	a.resetToolReadRecovery()
	if cfg.Disabled {
		a.toolResultViews = nil
	}
	a.lastPromptEstimate = 0
	a.lastInput, a.measuredThrough = 0, 0
	a.lastToolBudgetPlan = toolResultBudgetPlan{Headroom: -1}
	a.lastToolBudgetMessage = -1
}

func (a *Agent) activeContextEditConfig() ContextEditConfig {
	cfg := a.contextEdit
	if cfg.Trigger <= 0 && len(a.cleared) > 0 {
		// A forced overflow recovery remains frozen even when automatic
		// clearing is disabled. A late request must not resurrect its payloads.
		cfg.Trigger = 1<<63 - 1
	}
	return cfg
}

func (a *Agent) resultViewAt(position toolResultPosition, original llm.Block) (cachedToolResultView, bool) {
	view, ok := a.toolResultViews[position]
	return view, ok && view.originalText == original.Text && view.originalID == original.ResultID && view.callID == original.ToolUseID
}

// cachedToolPrompt is copy-on-write. UI details, line/body offsets, images and
// persisted Text retain their original meaning; only this request's view uses
// the bounded text and host-created recovery reference.
func (a *Agent) cachedToolPrompt(messages []llm.Message) []llm.Message {
	if a.toolOutputBudget.Disabled || len(a.toolResultViews) == 0 {
		return messages
	}
	var out []llm.Message
	for i, message := range messages {
		copied := false
		for j, block := range message.Content {
			if block.Type != llm.BlockToolResult {
				continue
			}
			view, ok := a.resultViewAt(toolResultPosition{i, j}, block)
			if !ok || view.text == block.Text && view.resultID == block.ResultID {
				continue
			}
			if out == nil {
				out = append([]llm.Message(nil), messages...)
			}
			if !copied {
				out[i].Content = append([]llm.Block(nil), message.Content...)
				copied = true
			}
			out[i].Content[j].Text, out[i].Content[j].ResultID = view.text, view.resultID
		}
	}
	if out == nil {
		return messages
	}
	return out
}

func toolCitationPrefix(block llm.Block) (string, string) {
	var source Source
	if len(block.Source) == 0 || json.Unmarshal(block.Source, &source) != nil {
		return "", block.Text
	}
	if _, ok := sourceLabelNumber(source.Label); !ok || !strings.HasPrefix(block.Text, "[cite:"+source.Label+" |") {
		return "", block.Text
	}
	end := strings.IndexByte(block.Text, '\n')
	if end < 0 {
		return "", block.Text
	}
	return block.Text[:end+1], block.Text[end+1:]
}

// cacheToolBatch runs only on the agent loop goroutine, after every action has
// settled. Allocation is by call order, never by parallel completion order.
func (a *Agent) cacheToolBatch(ctx context.Context, calls, results []llm.Block, messageIndex int, priorTokens int64) {
	positions := make([]int, len(results))
	for i := range positions {
		positions[i] = i
	}
	a.cacheToolResults(ctx, calls, results, positions, messageIndex, priorTokens)
}

func (a *Agent) cacheToolResults(ctx context.Context, calls, results []llm.Block, positions []int, messageIndex int, priorTokens int64) {
	if a.toolOutputBudget.Disabled || len(results) == 0 {
		return
	}
	if a.toolResultViews == nil {
		a.toolResultViews = make(map[toolResultPosition]cachedToolResultView)
	}
	costs, failures := make([]int64, len(results)), make([]bool, len(results))
	for i, result := range results {
		position := toolResultPosition{messageIndex, positions[i]}
		if _, cached := a.resultViewAt(position, result); !cached && a.registry != nil && result.ResultID != "" && i < len(calls) {
			prefix, body := toolCitationPrefix(result)
			view := a.registry.InitialResultView(ctx, tools.ModelResultBudgetRequest{
				Tool: calls[i].Name, Text: body, ResultID: result.ResultID,
				IsError: result.IsError, Details: result.Details, Args: calls[i].Input,
			})
			if view.Reduced {
				a.toolResultViews[position] = cachedToolResultView{
					originalText: result.Text, originalID: result.ResultID, callID: result.ToolUseID,
					text: prefix + view.Text, resultID: view.ResultID, recoverable: view.Recoverable,
					initialDelivery: copyInitialResultDelivery(view.InitialDelivery),
				}
			}
		}
		costs[i], failures[i] = estimatedToolText(result.Text), result.IsError
		if cached, ok := a.resultViewAt(toolResultPosition{messageIndex, positions[i]}, result); ok {
			costs[i] = estimatedToolText(cached.text)
		}
	}
	plan := planToolResultBudget(a.toolOutputBudget, max(int64(0), priorTokens), costs, failures)
	if priorTokens >= 0 {
		a.lastToolBudgetPlan = plan
		a.lastToolBudgetMessage = messageIndex
	}
	for i, result := range results {
		position := toolResultPosition{messageIndex, positions[i]}
		name, args := "tool", json.RawMessage(nil)
		if i < len(calls) {
			name, args = calls[i].Name, calls[i].Input
		}
		actualArgs, actualRead := a.takeToolReadExecution(result.ToolUseID, priorTokens >= 0)
		if actualRead {
			args = actualArgs
		}
		observe := func(delivery *tools.ModelResultDelivery, budget int64) {
			if actualRead && !result.IsError {
				a.observeToolReadDelivery(actualArgs, delivery, budget)
			}
		}
		old, cached := a.resultViewAt(position, result)
		if cached && estimatedToolText(old.text) <= plan.PerResult[i] {
			observe(old.delivery, old.deliveryBudget)
			a.rememberBudgetResult(old.resultID, result)
			continue // never expand an established observation view
		}
		if !cached && estimatedToolText(result.Text) <= plan.PerResult[i] {
			// The planner already charged the complete stamped text. Rounding
			// the header and body separately can cost one extra token and turn
			// an already-fitting small JSON result into a shape-only preview.
			// Keep the exact result when no reduction was requested.
			delete(a.toolResultViews, position)
			if actualRead && !result.IsError {
				_, body := toolCitationPrefix(result)
				view := a.registry.BudgetResultView(ctx, tools.ModelResultBudgetRequest{
					Tool: name, Text: body, ResultID: result.ResultID,
					Details: result.Details, Args: args, Tokens: estimatedToolText(body),
				})
				observe(view.Delivery, plan.PerResult[i])
			}
			a.rememberBudgetResult(result.ResultID, result)
			continue
		}
		prefix, body := toolCitationPrefix(result)
		budget := max(int64(0), plan.PerResult[i]-estimatedToolText(prefix))
		view := a.registry.BudgetResultView(ctx, tools.ModelResultBudgetRequest{
			Tool: name, Text: body, ResultID: result.ResultID, IsError: result.IsError,
			Details: result.Details, Args: args, Tokens: budget,
			InitialDelivery: old.initialDelivery,
		})
		text := prefix + view.Text
		delivery, deliveryBudget := view.Delivery, plan.PerResult[i]
		initialDelivery := view.InitialDelivery
		ceiling := result.Text
		ceilingID := result.ResultID
		if cached {
			ceiling, ceilingID = old.text, old.resultID
		}
		if estimatedToolText(text) >= estimatedToolText(ceiling) {
			// Tiny allocations and long citation headers can cost more in
			// management metadata than the original body. Keep the smaller,
			// recoverable view and report any remaining budget overrun.
			text, view.ResultID = ceiling, ceilingID
			// The discarded view's delivery flags do not describe the text we
			// selected. Reuse old flags only with that exact old cached text.
			delivery = nil
			if cached {
				view.Recoverable = old.recoverable
				delivery, deliveryBudget = old.delivery, old.deliveryBudget
				initialDelivery = old.initialDelivery
			}
		}
		a.toolResultViews[position] = cachedToolResultView{
			originalText: result.Text, originalID: result.ResultID, callID: result.ToolUseID,
			text: text, resultID: view.ResultID, budget: plan.PerResult[i], recoverable: view.Recoverable,
			delivery: copyToolResultDelivery(delivery), deliveryBudget: deliveryBudget,
			initialDelivery: copyInitialResultDelivery(initialDelivery),
		}
		observe(delivery, deliveryBudget)
		a.rememberBudgetResult(view.ResultID, result)
	}
}

func copyInitialResultDelivery(rule *tools.ResultDeliveryRule) *tools.ResultDeliveryRule {
	if rule == nil {
		return nil
	}
	copy := *rule
	copy.JSONPointers = append([]string(nil), rule.JSONPointers...)
	copy.Keywords = append([]string(nil), rule.Keywords...)
	return &copy
}

func copyToolResultDelivery(delivery *tools.ModelResultDelivery) *tools.ModelResultDelivery {
	if delivery == nil {
		return nil
	}
	copy := *delivery
	copy.NextRequest = append(json.RawMessage(nil), delivery.NextRequest...)
	return &copy
}

func (a *Agent) resetToolReadRecovery() { a.toolReadRecovery = toolReadRecoveryState{} }

func (a *Agent) rememberToolReadExecution(callID string, args json.RawMessage) {
	if a.toolOutputBudget.Disabled {
		return
	}
	if _, valid := tools.ToolResultRequestKey(args); !valid {
		return
	}
	if a.toolReadRecovery.executed == nil {
		a.toolReadRecovery.executed = make(map[string]json.RawMessage)
	}
	a.toolReadRecovery.executed[callID] = append(json.RawMessage(nil), args...)
}

func (a *Agent) takeToolReadExecution(callID string, live bool) (json.RawMessage, bool) {
	if !live {
		return nil, false
	}
	args, found := a.toolReadRecovery.executed[callID]
	delete(a.toolReadRecovery.executed, callID)
	return args, found
}

func (a *Agent) observeToolReadDelivery(args json.RawMessage, delivery *tools.ModelResultDelivery, budget int64) {
	key, valid := tools.ToolResultRequestKey(args)
	if !valid {
		return
	}
	if delivery == nil || delivery.RequestKey != key || delivery.Delivered || !delivery.Undeliverable {
		// Uncertain or usable deliveries must not leave a stale execution guard.
		delete(a.toolReadRecovery.blocked, key)
		return
	}
	if a.toolReadRecovery.blocked == nil {
		a.toolReadRecovery.blocked = make(map[string]undeliveredToolRead)
	}
	const maximumRecoveryGuards = 128
	if _, exists := a.toolReadRecovery.blocked[key]; !exists && len(a.toolReadRecovery.blocked) >= maximumRecoveryGuards {
		var oldestKey string
		var oldest uint64
		for candidate, failure := range a.toolReadRecovery.blocked {
			if oldestKey == "" || failure.sequence < oldest {
				oldestKey, oldest = candidate, failure.sequence
			}
		}
		delete(a.toolReadRecovery.blocked, oldestKey)
	}
	a.toolReadRecovery.sequence++
	var next json.RawMessage
	if len(delivery.NextRequest) <= 8192 {
		if nextKey, valid := tools.ToolResultRequestKey(delivery.NextRequest); valid && nextKey != key {
			next = append(json.RawMessage(nil), delivery.NextRequest...)
		}
	}
	a.toolReadRecovery.blocked[key] = undeliveredToolRead{budget: max(int64(0), budget), sequence: a.toolReadRecovery.sequence, nextRequest: next}
}

func (a *Agent) repeatedToolReadReceipt(args json.RawMessage) (string, bool) {
	if a.toolOutputBudget.Disabled {
		return "", false
	}
	key, valid := tools.ToolResultRequestKey(args)
	if !valid {
		return "", false
	}
	failure, blocked := a.toolReadRecovery.blocked[key]
	if !blocked {
		return "", false
	}
	// Actual parallel-batch allocations can be smaller than this upper bound.
	// If more capacity might now be available, allow a fresh immutable read.
	plan := planToolResultBudget(a.toolOutputBudget, a.promptTokens(), []int64{toolBudgetMaxInt64}, nil)
	if len(plan.PerResult) == 0 || plan.PerResult[0] > failure.budget {
		return "", false
	}
	envelope := map[string]any{
		"request_executed":       false,
		"repeated_undeliverable": true,
		"page_displayed":         false,
		"reason":                 "This saved-result selection already delivered no requested data at this model budget. The original action was not repeated.",
	}
	if len(failure.nextRequest) > 0 {
		envelope["next_request"] = failure.nextRequest
	} else {
		envelope["read_more"] = "Select fewer fields or a deeper pointer, or retry when more model space is available. Repeating this unchanged request cannot deliver the missing data."
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return "", false
	}
	if len(encoded) > 2048 {
		delete(envelope, "next_request")
		envelope["read_more"] = "Use a deeper pointer or narrower selection, or retry when more model space is available. The unchanged selection remains undisplayed."
		encoded, _ = json.Marshal(envelope)
	}
	return string(encoded), true
}

// ensureToolHistoryBudgets restores request views after resume and tightens
// them after a model switch. It never edits persisted Text or replays a tool.
// Historical batches use model-sized caps; the live batch additionally uses
// its actual pre-batch headroom when it settles.
func (a *Agent) ensureToolHistoryBudgets(ctx context.Context) {
	a.refreshResultReferenceLeases(ctx)
	if a.toolOutputBudget.Disabled {
		return
	}
	callByID := make(map[string]llm.Block)
	alive := make(map[toolResultPosition]bool)
	for i, message := range a.messages {
		for _, block := range message.Content {
			if block.Type == llm.BlockToolUse {
				callByID[block.ID] = block
			}
		}
		var calls, results []llm.Block
		var positions []int
		for j, block := range message.Content {
			if block.Type != llm.BlockToolResult {
				continue
			}
			calls = append(calls, callByID[block.ToolUseID])
			results = append(results, block)
			positions = append(positions, j)
			alive[toolResultPosition{i, j}] = true
		}
		if len(results) == 0 {
			continue
		}
		// Explicit block positions also handle imported messages interleaving
		// ordinary user text and results. -1 marks a historical reconstruction,
		// not a new live batch with a measured pre-batch headroom.
		a.cacheToolResults(ctx, calls, results, positions, i, -1)
	}
	for position := range a.toolResultViews {
		if !alive[position] {
			delete(a.toolResultViews, position)
		}
	}
}

func (a *Agent) toolBudgetStats() ToolOutputBudgetStats {
	stat := ToolOutputBudgetStats{Estimator: "ceil(UTF-8 text bytes / 4); images and protocol overhead are separate",
		LastBatchKnown:  a.lastToolBudgetMessage >= 0,
		LastBatchTarget: a.lastToolBudgetPlan.TargetTokens, LastBatchFloor: a.lastToolBudgetPlan.RequiredTokens, LastHeadroom: a.lastToolBudgetPlan.Headroom,
		IdentityFloorOverrun: a.lastToolBudgetMessage >= 0 && a.lastToolBudgetPlan.RequiredTokens > a.lastToolBudgetPlan.TargetTokens}
	for i, message := range a.messages {
		batch := false
		for j, block := range message.Content {
			if block.Type != llm.BlockToolResult {
				continue
			}
			batch = true
			original := estimatedToolText(block.Text)
			viewTokens := original
			if view, ok := a.resultViewAt(toolResultPosition{i, j}, block); ok {
				viewTokens = estimatedToolText(view.text)
				if view.text != block.Text {
					stat.ReducedResults++
				}
				if viewTokens > view.budget {
					stat.BudgetOverruns++
					if !view.recoverable {
						stat.UnrecoverableOverruns++
					}
				}
			}
			stat.OriginalTokens += original
			stat.ViewTokens += viewTokens
			if i == a.lastToolBudgetMessage {
				stat.LastBatchViewTokens += viewTokens
			}
		}
		if batch {
			stat.Batches++
		}
	}
	stat.FreedTokens = max(int64(0), stat.OriginalTokens-stat.ViewTokens)
	if stat.LastBatchKnown {
		stat.LastBatchOverrunTokens = max(int64(0), stat.LastBatchViewTokens-stat.LastBatchTarget)
	}
	return stat
}
