package evidencebounds

import "sort"

func compare(left, right *group, inputIncomplete bool) Relation {
	r := Relation{Kind: "unknown", SourceScope: "saved_text_only; upstream completeness unknown"}
	if left == nil || right == nil {
		r.ID, r.Incomplete, r.Reasons = hash("missing_relation_selector"), true, []string{"relation_selector_unavailable"}
		return r
	}
	r.ID = hash("remainder_line_literal:" + left.key + ":" + right.key)
	r.Domain, r.LeftSearchID, r.RightSearchID = left.domain, left.key, right.key
	r.LeftPattern, r.RightPattern = left.pattern, right.pattern
	r.LeftIDs, r.RightIDs = append([]string(nil), left.ids...), append([]string(nil), right.ids...)
	r.LeftScanComplete, r.RightScanComplete = left.scan, right.scan
	if left.domain != right.domain || !supported(left.domain) || !literalOK(left.pattern) || !literalOK(right.pattern) {
		r.Incomplete, r.Reasons = true, []string{"relation_domain_or_literal_unsupported"}
		return r
	}
	if !left.scan || fatalReasons(left.bad) || fatalReasons(right.bad) {
		r.Incomplete, r.Reasons = true, []string{"relation_input_incomplete"}
		return r
	}
	// Cooccurrence means the SAME complete line, not merely two literals inside
	// a multi-line context. A single actual witness refutes the exclusion.
	starts := make([]int, 0, len(left.known))
	for start := range left.known {
		starts = append(starts, start)
	}
	sort.Ints(starts)
	leftLines := map[int][]Witness{}
	rightStarts := make([]int, 0, len(right.known))
	rightLines := map[int]Witness{}
	for start, w := range right.known {
		rightStarts = append(rightStarts, start)
		if prior, ok := rightLines[w.LineStart]; !ok || w.Start < prior.Start {
			rightLines[w.LineStart] = w
		}
	}
	sort.Ints(rightStarts)
	for _, start := range starts {
		lw := left.known[start]
		leftLines[lw.LineStart] = append(leftLines[lw.LineStart], lw)
		if _, selected := left.byStart[start]; selected {
			continue
		}
		if rw, ok := rightLines[lw.LineStart]; ok {
			if lw.LineEnd == rw.LineEnd {
				r.Kind = "counterexample"
				if len(r.Witnesses) < MaxWitnesses {
					r.Witnesses = append(r.Witnesses, RelationWitness{lw, rw})
				}
			}
		}
	}
	if r.Kind == "counterexample" {
		if inputIncomplete || left.coverage.Incomplete || right.coverage.Incomplete {
			r.Kind, r.Incomplete, r.Reasons = "unknown", true, []string{"partial_relation_input; actual_cooccurrence_witness_retained"}
		}
		return r
	}
	if inputIncomplete || left.coverage.Incomplete || right.coverage.Incomplete {
		r.Incomplete, r.Reasons = true, []string{"relation_input_incomplete"}
		return r
	}
	// Exclusion requires the entire right selector, not only its scanned count.
	// All reported global right hits must be actual complete-line witnesses.
	if !right.scan || len(right.hits) != right.total || right.coverage.NativeCompleteBody != right.total {
		r.Incomplete, r.Reasons = true, []string{"right_selector_not_completely_observed"}
		return r
	}
	if right.total == 0 {
		// A zero-count receipt is not delivered body. Keep this first contract
		// conservative rather than silently inventing a text-based witness.
		r.Incomplete, r.Reasons = true, []string{"right_selector_has_no_body_witness"}
		return r
	}
	// Local occurrences of self-overlapping left literals have an unknown global
	// non-overlap phase; they cannot establish a missing occurrence or exclusion.
	if selfOverlap(left.pattern) {
		r.Incomplete, r.Reasons = true, []string{"left_literal_phase_unknown"}
		return r
	}
	seenLines := map[int]bool{}
	for _, rightStart := range rightStarts {
		rw := right.known[rightStart]
		if rw.Ordinal == nil || seenLines[rw.LineStart] {
			continue
		}
		seenLines[rw.LineStart] = true
		lineHasLeft := false
		for _, lw := range leftLines[rw.LineStart] {
			if lw.LineEnd != rw.LineEnd {
				continue
			}
			lineHasLeft = true
			if _, selected := left.byStart[lw.Start]; !selected {
				// This is normally caught above; do not convert an incomplete
				// observation into a disjoint verdict if internal bounds change.
				r.Incomplete, r.Reasons = true, []string{"left_remainder_has_right_literal"}
				return r
			}
			if len(r.Witnesses) < MaxWitnesses {
				r.Witnesses = append(r.Witnesses, RelationWitness{lw, rw})
			}
		}
		if !lineHasLeft {
			// The complete right line contains no left literal. Its bytes and
			// exact hash remain referenced by the right witness.
			if len(r.Witnesses) < MaxWitnesses {
				r.Witnesses = append(r.Witnesses, RelationWitness{Right: rw})
			}
		}
	}
	r.Kind = "verified_disjoint"
	return r
}
