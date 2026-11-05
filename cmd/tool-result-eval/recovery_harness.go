package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/yosukeno/pogo/agent"
	"github.com/yosukeno/pogo/config"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/session"
	"github.com/yosukeno/pogo/tools"
)

const recoveryTokenBudget = 240000

type recoveryMessages struct {
	Messages        int    `json:"messages"`
	Bytes           int    `json:"bytes"`
	SHA256          string `json:"sha256"`
	EstimatedTokens int64  `json:"estimated_tokens"`
}

type recoveryStageReport struct {
	Name             string                           `json:"name"`
	TaskSHA256       string                           `json:"task_sha256,omitempty"`
	WallMS           int64                            `json:"wall_ms"`
	EndReason        string                           `json:"end_reason"`
	ErrorClass       string                           `json:"error_class,omitempty"`
	Turns            int                              `json:"turns"`
	Requests         []requestReport                  `json:"requests"`
	Usage            usageReport                      `json:"usage"`
	ToolCalls        []toolCallReport                 `json:"tool_calls"`
	ReplyBytes       int                              `json:"reply_bytes"`
	ReplySHA256      string                           `json:"reply_sha256"`
	CitationRepair   *agent.CitationRepairStatus      `json:"citation_repair,omitempty"`
	EvidenceCoverage *agent.EvidenceCoverageStatus    `json:"evidence_coverage,omitempty"`
	QuoteCheck       *agent.EvidenceQuoteCheck        `json:"quote_check,omitempty"`
	QuoteRepair      *agent.EvidenceQuoteRepairStatus `json:"quote_repair,omitempty"`
}

type recoveryBoundaryReport struct {
	Before                        recoveryMessages      `json:"before"`
	After                         recoveryMessages      `json:"after"`
	Persisted                     recoveryMessages      `json:"persisted"`
	Reopened                      recoveryMessages      `json:"reopened"`
	PersistedRoundTrip            bool                  `json:"persisted_round_trip"`
	SessionFileSHA256             string                `json:"session_file_sha256,omitempty"`
	SessionFileBytes              int                   `json:"session_file_bytes,omitempty"`
	StableScopeSHA256             string                `json:"stable_scope_sha256"`
	RegistryRebuilt               bool                  `json:"registry_rebuilt"`
	AgentRebuilt                  bool                  `json:"agent_rebuilt"`
	ClientRebuilt                 bool                  `json:"client_rebuilt"`
	ModelChanged                  bool                  `json:"model_changed"`
	ModelBefore                   string                `json:"model_before"`
	ModelAfter                    string                `json:"model_after"`
	OriginalStampInActiveMessages bool                  `json:"original_stamp_in_active_messages"`
	CompactCalled                 bool                  `json:"compact_called"`
	CompactCommitted              bool                  `json:"compact_committed"`
	CompactKeepTokens             int64                 `json:"compact_keep_tokens"`
	CompactBeforeTokens           int64                 `json:"compact_before_tokens,omitempty"`
	CompactAfterTokens            int64                 `json:"compact_after_tokens,omitempty"`
	Summary                       *recoverySummaryProbe `json:"summary,omitempty"`
	UsageBefore                   llm.Usage             `json:"cumulative_agent_usage_before"`
	UsageAfter                    llm.Usage             `json:"cumulative_agent_usage_after"`
}

type recoveryRunReport struct {
	ID               string                 `json:"id"`
	Scenario         string                 `json:"scenario"`
	Phase            string                 `json:"phase"`
	BudgetMode       string                 `json:"budget_mode"`
	Repeat           int                    `json:"repeat"`
	Model            string                 `json:"model"`
	Provider         string                 `json:"provider"`
	FixtureSHA256    string                 `json:"fixture_sha256"`
	SystemSHA256     string                 `json:"system_sha256"`
	EarlyTaskSHA256  string                 `json:"early_task_sha256"`
	LateTaskSHA256   string                 `json:"late_task_sha256"`
	WallMS           int64                  `json:"wall_ms"`
	FailureStage     string                 `json:"failure_stage,omitempty"`
	ErrorClass       string                 `json:"error_class,omitempty"`
	OriginalResultID string                 `json:"original_result_id,omitempty"`
	Stages           []recoveryStageReport  `json:"stages"`
	Boundary         recoveryBoundaryReport `json:"boundary"`
	EarlyEvidence    recoveryEarlyEvidence  `json:"early_evidence"`
	Source           recoverySourceCheck    `json:"source_check"`
	Late             runReport              `json:"late"`
	Usage            usageReport            `json:"usage"`
	AgentUsage       llm.Usage              `json:"cumulative_agent_usage_final"`
	MCPCalls         int64                  `json:"mcp_original_call_attempts"`
	FileToolCalls    int                    `json:"file_tool_calls"`
	Passed           bool                   `json:"passed"`
}

func validRecoveryPhase(phase string) bool {
	switch phase {
	case "", "all", "control", "compact", "persist", "compact-persist", "reconnect", "switch":
		return true
	}
	return false
}

func recoveryPhases(opt options) []string {
	if opt.RecoveryPhase != "" && opt.RecoveryPhase != "all" {
		return []string{opt.RecoveryPhase}
	}
	phases := []string{"control", "compact", "persist", "compact-persist", "reconnect"}
	if opt.RecoverySwitchModel != "" {
		phases = append(phases, "switch")
	}
	return phases
}

func recoverySnapshot(messages []llm.Message, client llm.Client) recoveryMessages {
	raw, _ := json.Marshal(messages)
	return recoveryMessages{Messages: len(messages), Bytes: len(raw), SHA256: digest(raw), EstimatedTokens: llm.EstimateTokensFor(client, messages)}
}

func recoveryClient(resolved config.Resolved) llm.Client {
	return llm.New(llm.Options{BaseURL: resolved.BaseURL, APIKey: resolved.APIKey, Model: resolved.Model, Headers: resolved.Headers, ExtraBody: resolved.ExtraBody, Compatibility: resolved.Compatibility, MaxTokens: resolved.MaxTokens, MaxRetries: -1, Timeout: 90 * time.Second})
}

func recoveryBudget(opt options, resolved config.Resolved, mode string) agent.ToolOutputBudgetConfig {
	return agent.ToolOutputBudgetConfig{ContextWindow: resolved.ContextWindow, MaxOutputTokens: resolved.MaxTokens, PerResultTokens: opt.PerResult, BatchTokens: opt.Batch, Disabled: mode == "off"}
}

func recoveryAgent(measured *measuredClient, registry *tools.Registry, workdir, system string, budget agent.ToolOutputBudgetConfig) *agent.Agent {
	return agent.New(agent.Config{Client: measured, Registry: registry, SystemPrompt: system, MaxTurns: 12, TokenBudget: recoveryTokenBudget, TimeBudget: 180 * time.Second, Cwd: workdir, Citations: true, ToolExecution: tools.Sequential, ToolOutputBudget: budget})
}

// A phase never supplies a result ID to a user task. These helpers inspect host
// receipts solely for acceptance checks, independently of the model's choices.
func recoveryOriginal(messages []llm.Message, toolName string) (string, *agent.Source) {
	calls := map[string]string{}
	for _, message := range messages {
		for _, block := range message.Content {
			if block.Type == llm.BlockToolUse {
				calls[block.ID] = block.Name
			}
			if block.Type == llm.BlockToolResult && calls[block.ToolUseID] == toolName && block.ResultID != "" && !block.IsError {
				var source agent.Source
				if json.Unmarshal(block.Source, &source) == nil {
					return block.ResultID, &source
				}
			}
		}
	}
	return "", nil
}

func recoverySourceInMessages(messages []llm.Message, label string) *agent.Source {
	for _, message := range messages {
		for _, block := range message.Content {
			var source agent.Source
			if json.Unmarshal(block.Source, &source) != nil {
				continue
			}
			if source.Label == label && source.SavedResult == nil {
				return &source
			}
			if source.SavedResult != nil && source.SavedResult.Origin != nil && source.SavedResult.Origin.Label == label {
				copy := *source.SavedResult.Origin
				return &copy
			}
		}
	}
	return nil
}

func recoveryUsageStats(usage llm.Usage) *session.UsageStats {
	return &session.UsageStats{Input: usage.Input, Output: usage.Output, CacheRead: usage.CacheRead, Reasoning: usage.Reasoning}
}

func recoveryUsageDelta(before, after llm.Usage) llm.Usage {
	return llm.Usage{Input: after.Input - before.Input, Output: after.Output - before.Output, CacheRead: after.CacheRead - before.CacheRead, Reasoning: after.Reasoning - before.Reasoning}
}

func runRecoveryStage(ctx context.Context, current *agent.Agent, measured *measuredClient, name, prompt string, log *safeLog) (runReport, []agent.Source) {
	ctx, cancel := context.WithTimeout(ctx, 180*time.Second)
	defer cancel()
	report := runReport{ID: measured.runID + "/" + name, ToolCounts: map[string]int{}, ToolCalls: []toolCallReport{}}
	measured.mu.Lock()
	requestStart := len(measured.requests)
	measured.stage = name
	measured.mu.Unlock()
	positions := map[string]int{}
	var sources []agent.Source
	for event := range current.Run(ctx, prompt) {
		recordEvidenceStatus(&report, event, log, name)
		switch event.Kind {
		case agent.EventTurnStart:
			report.Turns = event.Turn
			if event.ToolOutputBudget != nil {
				report.Budget = *event.ToolOutputBudget
				if report.Budget.ReducedResults > report.MaximumReducedResults {
					report.MaximumReducedResults = report.Budget.ReducedResults
				}
			}
			log.emit(map[string]any{"type": "turn_start", "run_id": measured.runID, "stage": name, "turn": event.Turn, "budget": event.ToolOutputBudget})
		case agent.EventToolStart:
			call := toolCallReport{ID: event.ToolCallID, Name: event.ToolName, ArgsSHA256: digest([]byte(event.ToolArgs))}
			if len(event.ToolArgs) <= 8192 && json.Valid([]byte(event.ToolArgs)) {
				call.Args = json.RawMessage(event.ToolArgs)
			} else {
				call.ArgsOmitted = true
			}
			positions[call.ID] = len(report.ToolCalls)
			report.ToolCalls = append(report.ToolCalls, call)
			report.ToolCounts[call.Name]++
			if call.Name == "read" || call.Name == "ls" || call.Name == "find" || call.Name == "grep" {
				report.FileToolCalls++
			}
			log.emit(map[string]any{"type": "tool_start", "run_id": measured.runID, "stage": name, "call": call})
		case agent.EventToolEnd:
			index, exists := positions[event.ToolCallID]
			if !exists {
				index = len(report.ToolCalls)
				report.ToolCalls = append(report.ToolCalls, toolCallReport{ID: event.ToolCallID, Name: event.ToolName})
				if event.ToolName == "read" || event.ToolName == "ls" || event.ToolName == "find" || event.ToolName == "grep" {
					report.FileToolCalls++
				}
			}
			call := &report.ToolCalls[index]
			call.IsError, call.OutputBytes, call.OutputSHA256 = event.IsError, len(event.ToolOutput), digest([]byte(event.ToolOutput))
			if event.Source != nil {
				call.SourceLabel, call.SourceType = event.Source.Label, event.Source.Type
				if !event.IsError && event.ToolName == tools.ToolResultReadName {
					sources = append(sources, *event.Source)
				}
			}
			if details, ok := event.ToolDetails.(tools.ToolResultReadDetails); ok {
				call.ReadDetails = details
				if details.View == "query" && !event.IsError {
					call.Query = summarizeQuery(event.ToolOutput)
				}
			}
			log.emit(map[string]any{"type": "tool_end", "run_id": measured.runID, "stage": name, "call": call})
		case agent.EventMessage:
			if event.CitationRepair != nil {
				status := *event.CitationRepair
				report.CitationRepair = &status
				log.emit(map[string]any{"type": "citation_repair", "run_id": measured.runID, "stage": name, "status": status})
			}
			if text := event.Message.Text(); text != "" {
				report.FinalAnswer = bounded(text, 64*1024)
			}
		case agent.EventAgentEnd:
			report.EndReason, report.ErrorClass = string(event.EndReason), errorClass(event.Err)
		}
	}
	measured.mu.Lock()
	report.Requests = append([]requestReport(nil), measured.requests[requestStart:]...)
	measured.mu.Unlock()
	report.Usage = totalUsage(report.Requests)
	report.AllRequestsUsageKnown = report.Usage.Known
	if len(report.Requests) > 0 {
		report.SchemaSHA256 = report.Requests[0].SchemaSHA256
	}
	return report, sources
}

func runRecoveryPhase(parent context.Context, opt options, resolved config.Resolved, phase, mode string, repeat int, log *safeLog) (report recoveryRunReport) {
	f := opt.recovery
	if f == nil {
		value := makeRecoveryFixture()
		f = &value
	}
	system := f.SystemPrompt + "\n\n" + agent.CitationSection()
	report = recoveryRunReport{ID: fmt.Sprintf("recovery-%s-%s-%02d", phase, mode, repeat), Scenario: "recovery", Phase: phase, BudgetMode: mode, Repeat: repeat, Model: resolved.Model, Provider: resolved.Provider, FixtureSHA256: f.SHA256, SystemSHA256: digest([]byte(system)), EarlyTaskSHA256: digest([]byte(f.EarlyTask)), LateTaskSHA256: digest([]byte(f.LateTask)), Stages: []recoveryStageReport{}}
	started := time.Now()
	defer func() { report.WallMS = time.Since(started).Milliseconds() }()
	ctx, cancel := context.WithTimeout(parent, 900*time.Second)
	defer cancel()
	private, err := os.MkdirTemp("", "pogo-result-recovery-")
	if err != nil {
		report.ErrorClass = "private_workspace_unavailable"
		return
	}
	defer os.RemoveAll(private)
	private, err = filepath.EvalSymlinks(private)
	if err != nil {
		report.ErrorClass = "private_workspace_canonicalization_failed"
		return
	}
	workdir := filepath.Join(private, "work")
	if os.Mkdir(workdir, 0700) != nil {
		report.ErrorClass = "private_workspace_unavailable"
		return
	}
	store, err := session.Create(filepath.Join(private, "sessions"), workdir, resolved.Model)
	if err != nil {
		report.ErrorClass = "session_create_failed"
		return
	}
	local, err := (evalScenario{logs: &f.Logs}).newMCP(ctx)
	if err != nil {
		report.ErrorClass = "local_mcp_setup_failed"
		return
	}
	defer local.close()
	defer func() { report.MCPCalls = local.attempts.Load() }()
	empty := []string{}
	registryOptions := tools.Options{Cwd: workdir, ReadOnly: true, Enabled: &empty, MCP: []tools.Tool{local.wrapper}, SpillDir: filepath.Join(private, "spill"), ResultScope: "cli:" + store.Path()}
	registry := tools.New(registryOptions)
	budget := recoveryBudget(opt, resolved, mode)
	measured := &measuredClient{inner: recoveryClient(resolved), log: log, runID: report.ID, knownAll: true, logs: &f.Logs, generation: 1, recoveryResolved: &resolved, recoveryBudget: &budget}
	current := recoveryAgent(measured, registry, workdir, system, recoveryBudget(opt, resolved, mode))
	defer func() { measured.mu.Lock(); report.Usage = totalUsage(measured.requests); measured.mu.Unlock() }()
	labels := map[string]bool{}
	var earlyCalls []toolCallReport
	var earlyRequests []requestReport
	log.emit(map[string]any{"type": "recovery_phase_start", "run_id": report.ID, "phase": phase, "model": resolved.Model, "fixture_sha256": f.SHA256, "system_sha256": report.SystemSHA256, "early_task_sha256": report.EarlyTaskSHA256, "late_task_sha256": report.LateTaskSHA256})
	prompts := append([]string{f.EarlyTask}, f.BackgroundTurns...)
	for index, prompt := range prompts {
		name := "early-fetch"
		if index > 0 {
			name = fmt.Sprintf("background-%d", index)
		}
		beforeMessages, beforeUsage := len(current.Messages()), current.Usage()
		stageStart := time.Now()
		run, _ := runRecoveryStage(ctx, current, measured, name, prompt, log)
		report.Stages = append(report.Stages, recoveryStageReport{Name: name, TaskSHA256: digest([]byte(prompt)), WallMS: time.Since(stageStart).Milliseconds(), EndReason: run.EndReason, ErrorClass: run.ErrorClass, Turns: run.Turns, Requests: run.Requests, Usage: run.Usage, ToolCalls: run.ToolCalls, ReplyBytes: len(run.FinalAnswer), ReplySHA256: digest([]byte(run.FinalAnswer)), CitationRepair: run.CitationRepair, EvidenceCoverage: run.EvidenceCoverage, QuoteCheck: run.QuoteCheck, QuoteRepair: run.QuoteRepair})
		earlyCalls = append(earlyCalls, run.ToolCalls...)
		earlyRequests = append(earlyRequests, run.Requests...)
		report.FileToolCalls += run.FileToolCalls
		for _, call := range run.ToolCalls {
			if call.SourceLabel != "" {
				labels[call.SourceLabel] = true
			}
		}
		if err := store.AppendAll(current.Messages()[beforeMessages:]); err != nil {
			report.FailureStage = name
			report.ErrorClass = "session_append_failed"
			return
		}
		if err := store.AppendStats(session.Stats{Usage: recoveryUsageStats(recoveryUsageDelta(beforeUsage, current.Usage()))}); err != nil {
			report.FailureStage = name
			report.ErrorClass = "session_usage_append_failed"
			return
		}
		if run.EndReason != string(agent.EndCompleted) || run.ErrorClass != "" {
			report.FailureStage = name
			report.ErrorClass = run.ErrorClass
			if report.ErrorClass == "" {
				report.ErrorClass = "warmup_did_not_complete"
			}
			return
		}
	}
	originalID, originalSource := recoveryOriginal(current.Messages(), local.wrapper.Name())
	report.OriginalResultID = originalID
	report.EarlyEvidence = checkRecoveryEarlyDelivery(earlyCalls, earlyRequests, originalID, *f)
	if originalID == "" || originalSource == nil || local.attempts.Load() != 1 || report.FileToolCalls != 0 || !report.EarlyEvidence.Passed {
		report.FailureStage = "early-validation"
		report.ErrorClass = "invalid_early_observation_or_leaked_middle_evidence"
		return
	}
	report.Boundary.Before = recoverySnapshot(current.Messages(), measured)
	report.Boundary.UsageBefore = current.Usage()
	report.Boundary.StableScopeSHA256 = digest([]byte(registryOptions.ResultScope))
	report.Boundary.ModelBefore, report.Boundary.ModelAfter = resolved.Model, resolved.Model
	if phase == "compact" || phase == "compact-persist" {
		measured.stage = "compact"
		requestStart := len(measured.requests)
		stageStart := time.Now()
		report.Boundary.CompactCalled = true
		compact, compactErr := current.CompactWithCommit(ctx, 0, func(messages []llm.Message) error {
			if err := store.ForkWithMessages("", messages); err != nil {
				return err
			}
			report.Boundary.CompactCommitted = true
			return nil
		})
		requests := append([]requestReport(nil), measured.requests[requestStart:]...)
		report.Stages = append(report.Stages, recoveryStageReport{Name: "compact", WallMS: time.Since(stageStart).Milliseconds(), EndReason: string(agent.EndCompleted), ErrorClass: errorClass(compactErr), Requests: requests, Usage: totalUsage(requests), ReplyBytes: len(compact.Summary), ReplySHA256: digest([]byte(compact.Summary))})
		probe := probeRecoverySummary(compact.Summary, *f)
		report.Boundary.Summary = &probe
		report.Boundary.CompactBeforeTokens, report.Boundary.CompactAfterTokens = compact.BeforeTokens, compact.AfterTokens
		if compactErr != nil {
			report.FailureStage = "compact"
			report.ErrorClass = errorClass(compactErr)
			return
		}
		// UsageTotals deliberately includes abandoned branches. The original
		// warmup stats remain charged, so append only this real summary's cost.
		if err := store.AppendStats(session.Stats{Usage: recoveryUsageStats(compact.Usage)}); err != nil {
			report.FailureStage = "compact"
			report.ErrorClass = "session_usage_append_failed"
			return
		}
		if probe.RootCauseLeaked {
			report.FailureStage = "compact"
			report.ErrorClass = "summary_contains_unknown_middle_answer"
			return
		}
	}
	if phase == "persist" || phase == "compact-persist" {
		report.Boundary.Persisted = recoverySnapshot(store.Messages(""), measured)
		sessionBytes, readErr := os.ReadFile(store.Path())
		if readErr != nil {
			report.FailureStage = "persist"
			report.ErrorClass = "session_file_read_failed"
			return
		}
		report.Boundary.SessionFileBytes, report.Boundary.SessionFileSHA256 = len(sessionBytes), digest(sessionBytes)
		reopened, openErr := session.Open(store.Path())
		if openErr != nil {
			report.FailureStage = "persist"
			report.ErrorClass = "session_reopen_failed"
			return
		}
		report.Boundary.Reopened = recoverySnapshot(reopened.Messages(""), measured)
		report.Boundary.PersistedRoundTrip = report.Boundary.Persisted.SHA256 == report.Boundary.Reopened.SHA256
		if !report.Boundary.PersistedRoundTrip {
			report.FailureStage = "persist"
			report.ErrorClass = "session_round_trip_changed_messages"
			return
		}
		registry = tools.New(registryOptions)
		current = recoveryAgent(measured, registry, workdir, system, recoveryBudget(opt, resolved, mode))
		current.SetMessages(reopened.Messages(""))
		usage, delegated := reopened.UsageTotals()
		current.SetUsage(usage, delegated)
		store = reopened
		report.Boundary.RegistryRebuilt, report.Boundary.AgentRebuilt = true, true
	}
	lateResolved := resolved
	if phase == "reconnect" || phase == "switch" {
		if phase == "switch" {
			if opt.switchResolved == nil || opt.switchResolved.Model == resolved.Model {
				report.FailureStage = "switch"
				report.ErrorClass = "different_switch_model_unavailable"
				return
			}
			lateResolved = *opt.switchResolved
		}
		measured.inner = recoveryClient(lateResolved)
		measured.generation++
		budget = recoveryBudget(opt, lateResolved, mode)
		measured.recoveryResolved, measured.recoveryBudget = &lateResolved, &budget
		current.SetClient(measured)
		current.SetToolOutputBudget(recoveryBudget(opt, lateResolved, mode))
		report.Boundary.ClientRebuilt = true
		report.Boundary.ModelAfter = lateResolved.Model
		report.Boundary.ModelChanged = lateResolved.Model != resolved.Model
	}
	report.Boundary.After = recoverySnapshot(current.Messages(), measured)
	report.Boundary.UsageAfter = current.Usage()
	report.Boundary.OriginalStampInActiveMessages = recoverySourceInMessages(current.Messages(), originalSource.Label) != nil
	beforeMessages, beforeUsage := len(current.Messages()), current.Usage()
	lateStart := time.Now()
	late, lateSources := runRecoveryStage(ctx, current, measured, "late-analysis", f.LateTask, log)
	late.Model, late.Provider, late.Scenario = lateResolved.Model, lateResolved.Provider, "recovery"
	late.ContextWindow, late.MaxOutputTokens = lateResolved.ContextWindow, lateResolved.MaxTokens
	late.WallMS = time.Since(lateStart).Milliseconds()
	late.MCPCalls = local.attempts.Load()
	report.FileToolCalls += late.FileToolCalls
	for _, call := range late.ToolCalls {
		if call.SourceLabel != "" {
			labels[call.SourceLabel] = true
		}
	}
	if err := store.AppendAll(current.Messages()[beforeMessages:]); err != nil {
		report.ErrorClass = "late_session_append_failed"
	}
	if err := store.AppendStats(session.Stats{Usage: recoveryUsageStats(recoveryUsageDelta(beforeUsage, current.Usage()))}); err != nil {
		report.ErrorClass = "late_usage_append_failed"
	}
	recoveryCtx, recoveryCancel := context.WithTimeout(parent, hostRecoveryTimeout)
	late.Recovery = recoverLogText(recoveryCtx, registry, originalID, f.Logs)
	recoveryCancel()
	(evalScenario{logs: &f.Logs}).finishLogs(&late, originalID, labels)
	trace := checkRecoveryDelivery(late.ToolCalls, late.Requests, originalID, *f)
	late.LogEvidence = &trace
	late.Passed = late.Passed && trace.Passed
	applyCoverageCheck(parent, opt, &late, originalID, f.Logs, log)
	report.Late = late
	report.AgentUsage = current.Usage()
	report.Stages = append(report.Stages, recoveryStageReport{Name: "late-analysis", TaskSHA256: digest([]byte(f.LateTask)), WallMS: late.WallMS, EndReason: late.EndReason, ErrorClass: late.ErrorClass, Turns: late.Turns, Requests: late.Requests, Usage: late.Usage, ToolCalls: late.ToolCalls, ReplyBytes: len(late.FinalAnswer), ReplySHA256: digest([]byte(late.FinalAnswer)), CitationRepair: late.CitationRepair, EvidenceCoverage: late.EvidenceCoverage, QuoteCheck: late.QuoteCheck, QuoteRepair: late.QuoteRepair})
	report.Source = checkRecoverySource(originalSource, recoverySourceInMessages(current.Messages(), originalSource.Label), lateSources, originalID)
	report.Passed = late.Passed && report.Source.Passed && report.EarlyEvidence.Passed && report.FileToolCalls == 0 && local.attempts.Load() == 1 && report.ErrorClass == ""
	if !report.Passed {
		report.FailureStage = "late-analysis"
	}
	log.emit(map[string]any{"type": "recovery_phase_end", "run_id": report.ID, "passed": report.Passed, "late_evidence": trace, "source": report.Source, "mcp_calls": local.attempts.Load(), "boundary": report.Boundary})
	return
}

func runRecoveryCLI(ctx context.Context, opt options, resolved config.Resolved, log *safeLog, secrets []string) int {
	f := makeRecoveryFixture()
	opt.recovery = &f
	runs := []recoveryRunReport{}
	passed := true
	summary := func(status string) map[string]any {
		return map[string]any{"version": 1, "scenario": "recovery", "coverage_check": opt.CoverageCheck, "semantic_grader": coverageJudgeMetadata(opt), "status": status, "passed": passed, "created_at": time.Now().UTC().Format(time.RFC3339), "fixture_sha256": f.SHA256, "dataset_text_bytes": len(f.Logs.Text), "dataset_text_sha256": f.Logs.SHA256, "expected": f.Logs.Expected, "runs": runs, "limits": map[string]any{"phase_timeout_seconds": 900, "request_timeout_seconds": 90, "per_run_timeout_seconds": 180, "host_recovery_timeout_seconds": int(hostRecoveryTimeout / time.Second), "max_turns_per_run": 12, "cumulative_phase_soft_token_budget": recoveryTokenBudget, "client_max_retries": 0}, "notes": []string{"Each phase independently fetches one observation, performs three real background exchanges, then applies only its named lifecycle boundary before the identical late task.", "Compaction uses the real model and CompactWithCommit keep=0 with session.ForkWithMessages; persist rebuilds Registry and Agent from session.Open with unchanged store scope.", "The host never adds a result ID, byte offset, source stamp or answer to the late user task. Late evidence must bind to newly executed native result-reader calls.", "reconnect constructs a fresh client for the same model; only explicit switch uses a different configured model.", "Phase token limits are soft: usage includes every completed request, including a final request that crosses the cap; usage is never reset between rounds.", "No raw fixture/oracle/report/config is placed in agent roots; any file-tool call fails acceptance. Session artifacts remain in private temporary host storage.", "Reports contain summary hashes and bounded evidence metadata, not summary/reasoning text, credentials or multi-megabyte payloads."}}
	}
	for repeat := 1; repeat <= opt.Repeats; repeat++ {
		modes := []string{opt.Mode}
		if opt.Mode == "both" {
			modes = []string{"auto", "off"}
			if repeat%2 == 0 {
				modes = []string{"off", "auto"}
			}
		}
		for _, phase := range recoveryPhases(opt) {
			for _, mode := range modes {
				if ctx.Err() != nil {
					passed = false
					break
				}
				report := runRecoveryPhase(ctx, opt, resolved, phase, mode, repeat, log)
				runs = append(runs, report)
				passed = passed && report.Passed
				if writeSummary(opt.Output, secrets, summary("running")) != nil {
					fmt.Fprintln(os.Stderr, "recovery intermediate summary could not be written")
					return 2
				}
				fmt.Fprintf(os.Stderr, "%s: passed=%t facts=%t late_evidence=%t source=%t mcp=%d files=%d\n", report.ID, report.Passed, report.Late.NumericCorrect, report.Late.LogEvidence != nil && report.Late.LogEvidence.Passed, report.Source.Passed, report.MCPCalls, report.FileToolCalls)
			}
		}
	}
	if writeSummary(opt.Output, secrets, summary("finished")) != nil || log.err != nil {
		fmt.Fprintln(os.Stderr, "recovery report write failed")
		return 2
	}
	if !passed {
		return 1
	}
	return 0
}
