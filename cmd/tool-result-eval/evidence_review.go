package main

import "github.com/yosukeno/pogo/agent"

// Diagnostics are independent of acceptance. A checkpoint or an accepted
// rewrite never replaces the existing factual, actual-delivery or scope tests.
func recordEvidenceStatus(report *runReport, event agent.Event, log *safeLog, stage string) {
	if event.Kind != agent.EventMessage && event.Kind != agent.EventAgentEnd {
		return
	}
	if event.EvidenceCoverage != nil {
		status := *event.EvidenceCoverage
		report.EvidenceCoverage = &status
	}
	if event.QuoteCheck != nil {
		status := *event.QuoteCheck
		status.Paths = append([]string(nil), status.Paths...)
		report.QuoteCheck = &status
	} else if event.Kind == agent.EventAgentEnd {
		report.QuoteCheck = nil
	}
	if event.QuoteRepair != nil {
		status := *event.QuoteRepair
		report.QuoteRepair = &status
		// The terminal event repeats the final state, not another model call.
		if event.Kind == agent.EventMessage {
			log.emit(map[string]any{"type": "quote_repair", "run_id": report.ID, "stage": stage, "status": status})
		}
	}
	if event.Kind == agent.EventAgentEnd && (report.EvidenceCoverage != nil || report.QuoteCheck != nil) {
		log.emit(map[string]any{"type": "evidence_review", "run_id": report.ID, "stage": stage,
			"coverage": report.EvidenceCoverage, "quotes": report.QuoteCheck})
	}
}
