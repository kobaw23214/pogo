package main

import (
	"encoding/json"
	"slices"

	"github.com/yosukeno/pogo/agent"
	"github.com/yosukeno/pogo/tools"
)

type recoveryEarlyEvidence struct {
	Passed                  bool             `json:"passed"`
	DeliveredMiddleEvidence []string         `json:"delivered_middle_evidence"`
	Delivery                logEvidenceTrace `json:"delivery"`
}

func checkRecoveryEarlyDelivery(calls []toolCallReport, requests []requestReport, originalID string, f recoveryFixture) recoveryEarlyEvidence {
	checked := recoveryEarlyEvidence{DeliveredMiddleEvidence: []string{}, Delivery: checkLogDelivery(calls, requests, originalID, f.Logs)}
	for _, key := range checked.Delivery.DeliveredEvidence {
		if key != "final_failure" {
			checked.DeliveredMiddleEvidence = append(checked.DeliveredMiddleEvidence, key)
		}
	}
	// Even a short successful early read could expose the root code without
	// containing the entire oracle line. Check any verified byte overlap, then
	// reuse the normal call/source binding checks for that smaller requirement.
	for _, request := range requests {
		for _, view := range request.ToolViews {
			if view.Log == nil {
				continue
			}
			var intervals [][2]int
			if view.Log.Kind == "text_range" {
				intervals = append(intervals, [2]int{view.Log.Start, view.Log.End})
			}
			for _, part := range view.Log.Ranges {
				intervals = append(intervals, [2]int{part.Start, part.End})
			}
			for _, evidence := range f.Logs.RequiredEvidence {
				if evidence.Key == "final_failure" || slices.Contains(checked.DeliveredMiddleEvidence, evidence.Key) {
					continue
				}
				for _, interval := range intervals {
					start, end := max(interval[0], evidence.Start), min(interval[1], evidence.End)
					if start >= end {
						continue
					}
					probe := f.Logs
					probe.RequiredEvidence = []logEvidenceRequirement{{Key: evidence.Key, Start: start, End: end, Line: f.Logs.Text[start:end]}}
					if checkLogDelivery(calls, requests, originalID, probe).Passed {
						checked.DeliveredMiddleEvidence = append(checked.DeliveredMiddleEvidence, evidence.Key)
						break
					}
				}
			}
		}
	}
	checked.Passed = originalID != "" && len(checked.DeliveredMiddleEvidence) == 0
	return checked
}

// calls must contain only calls begun after the lifecycle boundary. Historical
// tool blocks can remain in late requests, but an early call ID cannot establish
// that the model recovered the saved observation during the late task.
func checkRecoveryDelivery(calls []toolCallReport, lateRequests []requestReport, originalID string, f recoveryFixture) logEvidenceTrace {
	lateReads := make([]toolCallReport, 0, len(calls))
	for _, call := range calls {
		if call.Name == tools.ToolResultReadName {
			lateReads = append(lateReads, call)
		}
	}
	return checkLogDelivery(lateReads, lateRequests, originalID, f.Logs)
}

type recoverySourceStamp struct {
	Label   string `json:"label"`
	Type    string `json:"type"`
	Locator string `json:"locator"`
	CallID  string `json:"call_id"`
	Hash    string `json:"hash"`
	Time    int64  `json:"ts"`
}

type recoverySourceCheck struct {
	Passed                bool                  `json:"passed"`
	OriginalBefore        *recoverySourceStamp  `json:"original_before,omitempty"`
	OriginalAfter         *recoverySourceStamp  `json:"original_after,omitempty"`
	OriginalStampRetained bool                  `json:"original_stamp_retained"`
	LateRawSources        int                   `json:"late_raw_sources"`
	LateDerivedSources    int                   `json:"late_derived_sources"`
	ParentStamps          []recoverySourceStamp `json:"parent_stamps"`
	Violations            []string              `json:"violations"`
}

func recoveryStamp(source *agent.Source) *recoverySourceStamp {
	if source == nil {
		return nil
	}
	return &recoverySourceStamp{Label: source.Label, Type: source.Type, Locator: source.Locator, CallID: source.CallID, Hash: source.Hash, Time: source.Time}
}

func sameRecoveryStamp(a, b *agent.Source) bool {
	if a == nil || b == nil {
		return false
	}
	return *recoveryStamp(a) == *recoveryStamp(b)
}

func checkRecoverySource(before, after *agent.Source, lateSources []agent.Source, originalID string) recoverySourceCheck {
	check := recoverySourceCheck{OriginalBefore: recoveryStamp(before), OriginalAfter: recoveryStamp(after), ParentStamps: []recoverySourceStamp{}, Violations: []string{}}
	if before == nil || before.Label == "" || before.Hash == "" || before.Time <= 0 || before.Type != agent.SourceMCP || before.SavedResult != nil || !validRecoveryResultID(originalID) {
		check.Violations = append(check.Violations, "original observation stamp is unavailable or not an original MCP source")
		return check
	}
	check.OriginalStampRetained = sameRecoveryStamp(before, after) && after.SavedResult == nil
	if !check.OriginalStampRetained {
		check.Violations = append(check.Violations, "restored original source changed or disappeared")
	}
	for _, source := range lateSources {
		if source.SavedResult == nil {
			if sameRecoveryStamp(before, &source) {
				check.LateRawSources++
			} else {
				check.Violations = append(check.Violations, "late raw read refreshed or changed the original source stamp")
			}
			continue
		}
		ref := source.SavedResult
		if ref.ResultID != originalID || ref.OriginAmbiguous || ref.Origin == nil || ref.Origin.SavedResult != nil || !sameRecoveryStamp(before, ref.Origin) ||
			source.Label == before.Label || source.Type != agent.SourceTool || source.Locator != "tool://tool-result/"+originalID || source.Time < before.Time {
			check.Violations = append(check.Violations, "late derived source is not bound to the retained original observation")
			continue
		}
		if ref.View == "search" || ref.View == "query" {
			var args struct {
				ResultID string `json:"result_id"`
				View     string `json:"view"`
			}
			if json.Unmarshal(ref.Query, &args) != nil || args.ResultID != originalID || args.View != ref.View {
				check.Violations = append(check.Violations, "derived source lost its native saved-result request")
				continue
			}
		}
		check.LateDerivedSources++
		check.ParentStamps = append(check.ParentStamps, *recoveryStamp(ref.Origin))
	}
	if len(lateSources) == 0 {
		check.Violations = append(check.Violations, "no successful late read source was captured")
	}
	check.Passed = check.OriginalStampRetained && len(check.Violations) == 0 && check.LateRawSources+check.LateDerivedSources > 0
	return check
}

func validRecoveryResultID(id string) bool {
	if len(id) != 65 || id[0] != 'R' {
		return false
	}
	for _, c := range id[1:] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
