package taskqueue

import (
	"bytes"
	"encoding/json"

	af "github.com/yosukeno/pogo/agentworkflow"
)

func awEqualJSON(a, b json.RawMessage) bool {
	var x, y bytes.Buffer
	return json.Compact(&x, a) == nil && json.Compact(&y, b) == nil && bytes.Equal(x.Bytes(), y.Bytes())
}

func awValidCodeAttempts(step af.Step, st af.StepRun) bool {
	if af.ValidateCodePolicy(step.Code, step.Policy) != nil {
		return false
	}
	if len(st.Attempts) == 0 {
		return !st.FallbackUsed && (step.Policy == nil || st.Status != af.Completed)
	}
	maximum := 1
	if step.Policy != nil {
		maximum += step.Policy.MaxRetries
	}
	if len(st.Attempts) > maximum {
		return false
	}
	var last *af.CodeExecution
	for i, attempt := range st.Attempts {
		if attempt.Number != i+1 || attempt.StartedAt <= 0 || attempt.StartedAt < st.StartedAt || attempt.FinishedAt != 0 && attempt.FinishedAt < attempt.StartedAt || !validText(attempt.Error, 8<<10, false) || len(attempt.Output) > af.MaxCodeDataBytes {
			return false
		}
		if i > 0 && (st.Attempts[i-1].FinishedAt == 0 || st.Attempts[i-1].Error == "") {
			return false
		}
		if attempt.FinishedAt == 0 {
			if i != len(st.Attempts)-1 || attempt.Execution != nil || len(attempt.Output) > 0 || attempt.Error != "" || st.Status == af.Completed {
				return false
			}
			continue
		}
		if attempt.Execution == nil || len(attempt.Output) > 0 && !json.Valid(attempt.Output) {
			return false
		}
		partial := af.StepRun{StepID: step.ID, Status: af.Running, CodeExecution: attempt.Execution}
		// Validate individual execution metadata without recursing through attempts.
		plain := step
		plain.Policy = nil
		if !awValidCodeReceiptWithoutAttempts(plain, partial) {
			return false
		}
		if attempt.Error == "" && (attempt.Execution.ErrorCode != "" || af.ValidateCodeOutput(step.Code, attempt.Output) != nil) {
			return false
		}
		last = attempt.Execution
	}
	if last != nil {
		x, _ := json.Marshal(last)
		y, _ := json.Marshal(st.CodeExecution)
		if !bytes.Equal(x, y) {
			return false
		}
	}
	if st.FallbackUsed {
		if st.Status != af.Completed || step.Policy == nil || step.Policy.OnFailure != "fallback" || last == nil || !af.RecoverableCodeExecution(last) || !awEqualJSON(st.Output, step.Policy.Fallback) {
			return false
		}
	}
	return true
}

// The metadata rules are shared with ordinary receipts while avoiding a
// recursive attempt -> receipt -> attempt validation cycle.
func awValidCodeReceiptWithoutAttempts(step af.Step, st af.StepRun) bool {
	if st.CodeExecution == nil {
		return false
	}
	copy := st
	copy.Attempts = nil
	copy.Status = af.Running
	step.Policy = nil
	return awValidCodeReceipt(step, copy)
}

func awFrozenAttemptPrefix(before, after []af.CodeAttempt) bool {
	if len(after) < len(before) {
		return false
	}
	for i, old := range before {
		next := after[i]
		if old.Number != next.Number || old.StartedAt != next.StartedAt {
			return false
		}
		if old.FinishedAt != 0 {
			a, _ := json.Marshal(old)
			b, _ := json.Marshal(next)
			if !bytes.Equal(a, b) {
				return false
			}
		}
	}
	return true
}

func awValidRepeatReceipt(step af.Step, st af.StepRun) bool {
	if st.CodeExecution != nil || len(st.CodeInputs) > 0 || len(st.Attempts) > 0 || st.FallbackUsed || st.SessionID != "" || st.ActiveRunID != "" || st.Artifact != "" || st.ArtifactSHA256 != "" || st.Feedback != "" {
		return false
	}
	if st.Repeat == nil {
		return st.Status != af.Completed && len(st.Output) == 0
	}
	r := st.Repeat
	raw, err := json.Marshal(r)
	if err != nil || len(raw) > af.MaxRepeatReceiptBytes || r.Kind != step.Kind || r.Total < 0 || r.Total > af.MaxRepeatItems || len(r.Items) > r.Total || (st.Status == af.Queued || st.Status == af.Skipped) {
		return false
	}
	var body *af.Step
	if step.Kind == "iteration" && step.Iteration != nil {
		body = step.Iteration.Body
		if r.LimitReached || len(r.State) > 0 {
			return false
		}
	}
	if step.Kind == "loop" && step.Loop != nil {
		body = step.Loop.Body
		if r.Total != step.Loop.MaxIterations || len(r.State) > af.MaxRepeatItemBytes || len(r.State) > 0 && !json.Valid(r.State) {
			return false
		}
	}
	if body == nil {
		return false
	}
	allTerminal := len(r.Items) == r.Total
	for i, item := range r.Items {
		if item.Index != i || len(item.Input) == 0 || len(item.Input) > af.MaxRepeatItemBytes || !json.Valid(item.Input) || len(item.Output) > af.MaxCodeDataBytes || len(item.Output) > 0 && !json.Valid(item.Output) || !validText(item.Summary, 8<<10, false) || !validText(item.Error, 8<<10, false) || !validText(item.SessionID, 256, false) || !validText(item.ActiveRunID, 256, false) || item.StartedAt < 0 || item.FinishedAt < 0 || item.StartedAt > 0 && item.FinishedAt > 0 && item.FinishedAt < item.StartedAt {
			return false
		}
		switch item.Status {
		case af.Queued, af.Running, af.Completed, af.Failed, af.Cancelled, af.Interrupted, af.Skipped:
		default:
			return false
		}
		if item.Status == af.Queued || item.Status == af.Running {
			allTerminal = false
		}
		if item.Status == af.Completed && (item.StartedAt == 0 || item.FinishedAt < item.StartedAt || len(item.Output) == 0) {
			return false
		}
		if body.Kind == "code" {
			receipt := af.StepRun{StepID: step.ID, Status: item.Status, StartedAt: item.StartedAt, FinishedAt: item.FinishedAt, Output: item.Output, CodeInputs: item.CodeInputs, CodeExecution: item.CodeExecution, Attempts: item.Attempts, FallbackUsed: item.FallbackUsed}
			if item.SessionID != "" || item.ActiveRunID != "" || item.Artifact != "" || item.ArtifactSHA256 != "" || !awValidCodeReceipt(*body, receipt) {
				return false
			}
		} else {
			if len(item.CodeInputs) > 0 || item.CodeExecution != nil || len(item.Attempts) > 0 || item.FallbackUsed || item.ActiveRunID != "" && item.SessionID == "" {
				return false
			}
			if item.Artifact != "" && (item.Artifact != body.OutputFile || !af.SafePath(item.Artifact) || !af.ValidHash(item.ArtifactSHA256)) || item.Artifact == "" && item.ArtifactSHA256 != "" {
				return false
			}
			if item.Status == af.Completed && (item.SessionID == "" || item.ActiveRunID == "" || body.OutputFile != "" && item.Artifact == "") {
				return false
			}
		}
	}
	if st.Status == af.Completed {
		if len(st.Output) == 0 || len(st.Output) > af.MaxCodeDataBytes || !json.Valid(st.Output) {
			return false
		}
		if step.Kind == "iteration" {
			if !allTerminal {
				return false
			}
			for _, item := range r.Items {
				if item.Status != af.Completed && (step.Iteration.OnItemError != "continue" || item.Status != af.Failed) {
					return false
				}
			}
		} else {
			if len(r.Items) == 0 || r.LimitReached || r.Items[len(r.Items)-1].Status != af.Completed {
				return false
			}
			ok, e := af.EvaluateLoopUntil(step.Loop, r.State, len(r.Items)-1)
			if e != nil || !ok {
				return false
			}
		}
	}
	return st.Status == af.Completed || len(st.Output) == 0
}

func awValidReceiptTransition(run af.Run, index int, before, after af.StepRun) bool {
	if !awFrozenAttemptPrefix(before.Attempts, after.Attempts) {
		return false
	}
	if before.Repeat != nil {
		if after.Repeat == nil || before.Repeat.Kind != after.Repeat.Kind || before.Repeat.Total != after.Repeat.Total || len(after.Repeat.Items) < len(before.Repeat.Items) {
			return false
		}
		for i, old := range before.Repeat.Items {
			next := after.Repeat.Items[i]
			if old.Index != next.Index || !awEqualJSON(old.Input, next.Input) || old.SessionID != "" && old.SessionID != next.SessionID || old.ActiveRunID != "" && old.ActiveRunID != next.ActiveRunID || old.StartedAt != 0 && old.StartedAt != next.StartedAt || !awFrozenAttemptPrefix(old.Attempts, next.Attempts) {
				return false
			}
			if old.Status == af.Completed || old.Status == af.Failed || old.Status == af.Cancelled || old.Status == af.Interrupted || old.Status == af.Skipped {
				a, _ := json.Marshal(old)
				b, _ := json.Marshal(next)
				if !bytes.Equal(a, b) {
					return false
				}
			}
		}
	}
	if after.Repeat != nil {
		step := run.Definition.Steps[index]
		if step.Kind == "iteration" {
			items, err := af.ResolveIterationItems(run, step.ID)
			if err != nil || len(items) != after.Repeat.Total {
				return false
			}
			for _, item := range after.Repeat.Items {
				if item.Index >= len(items) || !awEqualJSON(items[item.Index], item.Input) {
					return false
				}
			}
		} else if step.Kind == "loop" {
			state, err := af.ResolveLoopInitial(run, step.ID)
			if err != nil {
				return false
			}
			for _, item := range after.Repeat.Items {
				if !awEqualJSON(state, item.Input) {
					return false
				}
				if item.Status == af.Completed {
					state = item.Output
				}
			}
			if !awEqualJSON(state, after.Repeat.State) {
				return false
			}
		}
	}
	return true
}

func awStopRepeatItems(st *af.StepRun, status, message string) {
	if st.Repeat == nil {
		return
	}
	for i := range st.Repeat.Items {
		item := &st.Repeat.Items[i]
		if item.Status == af.Running {
			item.Status, item.Summary, item.Error, item.FinishedAt = status, message, message, now()
		}
		if item.Status == af.Queued {
			item.Status, item.FinishedAt = af.Skipped, now()
		}
	}
}
