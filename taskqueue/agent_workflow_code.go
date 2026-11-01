package taskqueue

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	af "github.com/yosukeno/pogo/agentworkflow"
)

func awValidNodeTest(r af.Run) bool {
	for _, step := range r.Definition.Steps {
		if step.ID == r.NodeTest.StepID {
			return step.Kind == "code" && step.Code != nil && af.ValidateCodeInputs(step.Code, r.NodeTest.Inputs) == nil
		}
	}
	return false
}

// New receipts are validated at the durable boundary as well as by the runner.
// An ordinary Agent receipt can never masquerade as a code execution result.
func awValidCodeReceipt(step af.Step, st af.StepRun) bool {
	if step.Kind == "iteration" || step.Kind == "loop" {
		return awValidRepeatReceipt(step, st)
	}
	if st.Repeat != nil {
		return false
	}
	if step.Kind != "code" {
		if st.CodeExecution != nil || len(st.CodeInputs) != 0 || len(st.Attempts) > 0 || st.FallbackUsed {
			return false
		}
		return len(st.Output) == 0 || step.Kind == "join" && len(st.Output) <= af.MaxCodeDataBytes && json.Valid(st.Output)
	}
	if step.Code == nil || st.SessionID != "" || st.ActiveRunID != "" || st.Artifact != "" || st.ArtifactSHA256 != "" || st.Feedback != "" {
		return false
	}
	if (st.Status == af.Queued || st.Status == af.Skipped) && (len(st.CodeInputs) != 0 || st.CodeExecution != nil || len(st.Output) != 0) {
		return false
	}
	if len(st.CodeInputs) != 0 && af.ValidateCodeInputs(step.Code, st.CodeInputs) != nil {
		return false
	}
	if st.Status == af.Completed && (len(st.CodeInputs) == 0 || st.CodeExecution == nil || af.ValidateCodeOutput(step.Code, st.Output) != nil) {
		return false
	}
	if st.Status != af.Completed && len(st.Output) != 0 {
		return false
	}
	if !awValidCodeAttempts(step, st) {
		return false
	}
	if receipt := st.CodeExecution; receipt != nil {
		sum := sha256.Sum256([]byte(step.Code.Source))
		if receipt.SourceSHA256 != hex.EncodeToString(sum[:]) || receipt.Language != step.Code.Language ||
			!validText(receipt.Stdout, 16<<10, false) || !validText(receipt.Stderr, 16<<10, false) ||
			!validText(receipt.Runtime, 256, false) || !validText(receipt.ErrorCode, 256, false) || receipt.DurationMS < 0 {
			return false
		}
		if st.Status == af.Completed && !st.FallbackUsed && (receipt.TimedOut || receipt.ErrorCode != "" || receipt.ExitCode == nil || *receipt.ExitCode != 0) {
			return false
		}
	}
	return true
}
