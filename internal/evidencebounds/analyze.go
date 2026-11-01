package evidencebounds

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"
	"unicode/utf8"
)

type group struct {
	key, pattern string
	domain       Domain
	ids          []string
	total        int
	scan         bool
	hits         map[int]Hit
	byStart      map[int]Hit
	bad          []string
	coverage     Coverage
	known        map[int]Witness
}

type line struct {
	start, end int
	text       string
	refs       []string
}

type domainLines struct {
	lines []line
	bad   []string
}

func hash(text string) string {
	h := sha256.Sum256([]byte(text))
	return hex.EncodeToString(h[:])
}

// SearchKey is identity only. It confers no ability to read the named result.
func SearchKey(domain Domain, pattern string) string {
	raw, _ := json.Marshal(struct {
		Domain  Domain
		Pattern string
	}{domain, pattern})
	return hash(string(raw))
}

func addReason(reasons *[]string, reason string) {
	for _, r := range *reasons {
		if r == reason {
			return
		}
	}
	*reasons = append(*reasons, reason)
}

func supported(d Domain) bool { return d.ResultID != "" && d.Kind == "stored_text" && d.Pointer == "" }
func literalOK(p string) bool {
	return p != "" && len(p) <= 1024 && utf8.ValidString(p) && !strings.ContainsAny(p, "\r\n")
}

// A border permits overlapping literal occurrences. Without an observed global
// scan anchor, a local fragment cannot determine the scanner's non-overlap phase.
func selfOverlap(pattern string) bool {
	pi := make([]int, len(pattern))
	for i := 1; i < len(pattern); i++ {
		j := pi[i-1]
		for j > 0 && pattern[i] != pattern[j] {
			j = pi[j-1]
		}
		if pattern[i] == pattern[j] {
			j++
		}
		pi[i] = j
	}
	return len(pi) > 0 && pi[len(pi)-1] > 0
}

// Analyze does not accept storage, network, UI or fixture access. Its limits
// bound both work and receipts; unsupported or conflicting evidence stays unknown.
func Analyze(in Input) Report {
	out := Report{Coverage: []Coverage{}, Relations: []Relation{}, Incomplete: in.Incomplete}
	if in.Incomplete {
		addReason(&out.Reasons, "caller_ledger_incomplete")
	}
	groups := map[string]*group{}
	aliases := map[string]*group{}
	var order []*group
	hitCount := 0
	for i, s := range in.Searches {
		if i >= MaxSearches {
			addReason(&out.Reasons, "search_cap")
			break
		}
		key := SearchKey(s.Domain, s.Pattern)
		g := groups[key]
		if g == nil {
			if len(order) >= MaxSelectors {
				addReason(&out.Reasons, "selector_cap")
				break
			}
			g = &group{key: key, pattern: s.Pattern, domain: s.Domain, total: s.Total, hits: map[int]Hit{}, byStart: map[int]Hit{}, known: map[int]Witness{}}
			groups[key] = g
			order = append(order, g)
		}
		if !supported(s.Domain) || !literalOK(s.Pattern) || s.Total < 0 || s.ID == "" || len(s.ID) > 256 {
			addReason(&g.bad, "unsupported_or_invalid_search")
		}
		if s.Total != g.total {
			addReason(&g.bad, "conflicting_search_total")
		}
		g.scan = g.scan || s.ScanComplete
		if prior := aliases[s.ID]; prior != nil && prior != g {
			addReason(&g.bad, "conflicting_search_alias")
			addReason(&prior.bad, "conflicting_search_alias")
		} else {
			aliases[s.ID] = g
		}
		present := false
		for _, id := range g.ids {
			present = present || id == s.ID
		}
		if !present {
			g.ids = append(g.ids, s.ID)
		}
		for _, h := range s.Hits {
			if h.Ordinal < 0 || h.Ordinal >= s.Total || h.Start < 0 || h.End < 0 || h.End < h.Start || h.End-h.Start != len(s.Pattern) {
				addReason(&g.bad, "invalid_native_hit")
				continue
			}
			if old, ok := g.hits[h.Ordinal]; ok {
				if old != h {
					addReason(&g.bad, "conflicting_native_ordinal")
				}
				continue
			}
			if old, ok := g.byStart[h.Start]; ok && old != h {
				addReason(&g.bad, "conflicting_native_position")
				continue
			}
			if hitCount >= MaxHits {
				addReason(&g.bad, "native_hit_cap")
				continue
			}
			g.hits[h.Ordinal], g.byStart[h.Start] = h, h
			hitCount++
		}
	}
	fragments := map[Domain][]Fragment{}
	domainBad := map[Domain][]string{}
	fragmentIDs := map[string]Fragment{}
	bytes := 0
	for i, f := range in.Fragments {
		if i >= MaxFragments || bytes+len(f.Text) > MaxBytes {
			addReason(&out.Reasons, "body_cap")
			addReasonPtr(domainBad, f.Domain, "body_cap")
			break
		}
		if f.ID == "" || len(f.ID) > 256 {
			addReasonPtr(domainBad, f.Domain, "invalid_fragment")
			continue
		}
		if prior, exists := fragmentIDs[f.ID]; exists {
			if prior != f {
				addReasonPtr(domainBad, f.Domain, "conflicting_fragment_id")
				addReasonPtr(domainBad, prior.Domain, "conflicting_fragment_id")
			}
			continue
		}
		fragmentIDs[f.ID] = f
		if !supported(f.Domain) {
			continue
		}
		if f.Start < 0 || f.End < f.Start || f.End-f.Start != len(f.Text) || !utf8.ValidString(f.Text) {
			addReasonPtr(domainBad, f.Domain, "invalid_fragment")
			continue
		}
		bytes += len(f.Text)
		fragments[f.Domain] = append(fragments[f.Domain], f)
	}
	domains := map[Domain]domainLines{}
	for _, g := range order {
		if _, ok := domains[g.domain]; !ok {
			lines, reason := mergeLines(fragments[g.domain])
			bad := append([]string(nil), domainBad[g.domain]...)
			if reason != "" {
				addReason(&bad, reason)
				if reason == "conflicting_fragment_overlap" {
					lines = nil
				}
			}
			domains[g.domain] = domainLines{lines, bad}
		}
	}
	occurrences := 0
	globalIncomplete := in.Incomplete || len(out.Reasons) > 0
	for _, g := range order {
		g.analyze(domains[g.domain], &occurrences)
		if globalIncomplete {
			g.coverage.Incomplete, g.coverage.OtherBodyCountLowerBound = true, true
			g.coverage.NativeComplete, g.coverage.CompleteBody = false, false
			addReason(&g.coverage.Reasons, "input_ledger_incomplete")
		}
		out.Coverage = append(out.Coverage, g.coverage)
		out.Incomplete = out.Incomplete || g.coverage.Incomplete
	}
	seenRelations := map[string]bool{}
	for i, request := range in.Relations {
		if i >= MaxRelations {
			addReason(&out.Reasons, "relation_cap")
			break
		}
		left, right := aliases[request.LeftID], aliases[request.RightID]
		if left == nil {
			left = groups[request.LeftID]
		}
		if right == nil {
			right = groups[request.RightID]
		}
		relation := compare(left, right, in.Incomplete || len(out.Reasons) > 0)
		if seenRelations[relation.ID] {
			continue
		}
		seenRelations[relation.ID] = true
		out.Relations = append(out.Relations, relation)
		out.Incomplete = out.Incomplete || relation.Incomplete
	}
	out.Incomplete = out.Incomplete || len(out.Reasons) > 0
	return out
}

func addReasonPtr(all map[Domain][]string, d Domain, reason string) {
	list := all[d]
	addReason(&list, reason)
	all[d] = list
}

func (g *group) analyze(body domainLines, occurrences *int) {
	sort.Strings(g.ids)
	c := Coverage{SearchID: g.key, IDs: append([]string(nil), g.ids...), Domain: g.domain, Pattern: g.pattern, Total: g.total, NativeDelivered: len(g.hits)}
	for _, r := range body.bad {
		addReason(&g.bad, r)
	}
	hits := make([]Hit, 0, len(g.hits))
	for _, hit := range g.hits {
		hits = append(hits, hit)
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].Ordinal < hits[j].Ordinal })
	for i, hit := range hits {
		if i > 0 && hits[i-1].End > hit.Start {
			addReason(&g.bad, "native_order_conflict")
		}
	}
	if !fatalReasons(g.bad) {
		for _, hit := range hits {
			if l, ok := containing(body.lines, hit.Start, hit.End); ok {
				if l.text[hit.Start-l.start:hit.End-l.start] != g.pattern {
					addReason(&g.bad, "native_hit_body_conflict")
					break
				}
				w := witness(l, hit.Start, hit.End)
				ordinal := hit.Ordinal
				w.Ordinal = &ordinal
				g.known[hit.Start] = w
			}
		}
	}
	if !fatalReasons(g.bad) && literalOK(g.pattern) && !selfOverlap(g.pattern) {
		stop := false
		for _, l := range body.lines {
			for at := 0; at <= len(l.text)-len(g.pattern); {
				found := strings.Index(l.text[at:], g.pattern)
				if found < 0 {
					break
				}
				start := l.start + at + found
				at += found + len(g.pattern)
				if _, exists := g.known[start]; exists {
					continue
				}
				if *occurrences >= MaxOccurrences {
					addReason(&g.bad, "literal_occurrence_cap")
					stop = true
					break
				}
				(*occurrences)++
				w := witness(l, start, start+len(g.pattern))
				lo, hi := 0, g.total
				index := sort.Search(len(hits), func(i int) bool { return hits[i].Start >= start })
				if index > 0 {
					lo = hits[index-1].Ordinal + 1
				}
				if index < len(hits) {
					hi = hits[index].Ordinal
				}
				if lo >= hi || g.scan && len(g.known) >= g.total {
					addReason(&g.bad, "literal_count_or_position_conflict")
					stop = true
					break
				}
				w.OrdinalLower, w.OrdinalUpper = &lo, &hi
				g.known[start] = w
			}
			if stop {
				break
			}
		}
	} else if !fatalReasons(g.bad) && literalOK(g.pattern) && len(g.hits) < g.total {
		addReason(&g.bad, "self_overlapping_literal_phase_unknown")
	}
	if len(g.bad) > 0 {
		// Conservative observations may still be useful to callers, but none of
		// these partial/capped results may establish a complete-set relation.
		c.Incomplete, c.OtherBodyCountLowerBound, c.Reasons = true, true, append([]string(nil), g.bad...)
		if fatalReasons(g.bad) {
			g.known = map[int]Witness{}
		}
	}
	starts := make([]int, 0, len(g.known))
	for p := range g.known {
		starts = append(starts, p)
	}
	sort.Ints(starts)
	for _, start := range starts {
		w := g.known[start]
		if _, native := g.byStart[start]; native {
			c.NativeCompleteBody++
		} else {
			c.OtherBodyCount++
			if len(c.OtherBody) < MaxWitnesses {
				c.OtherBody = append(c.OtherBody, w)
			}
		}
	}
	c.KnownCompleteBody = len(g.known)
	c.NativeComplete = g.scan && len(g.hits) == g.total && !c.Incomplete
	c.CompleteBody = g.scan && len(g.known) == g.total && !c.Incomplete
	g.coverage = c
}

func fatalReasons(reasons []string) bool {
	for _, reason := range reasons {
		switch reason {
		case "body_cap", "literal_occurrence_cap", "self_overlapping_literal_phase_unknown", "line_cap", "line_reference_cap":
			// The bytes already retained are still genuine witnesses, but not
			// a complete-set proof.
		default:
			return true
		}
	}
	return false
}

func containing(lines []line, start, end int) (line, bool) {
	i := sort.Search(len(lines), func(i int) bool { return lines[i].end >= end })
	if i < len(lines) && lines[i].start <= start && end <= lines[i].end {
		return lines[i], true
	}
	return line{}, false
}

func witness(l line, start, end int) Witness {
	return Witness{Start: start, End: end, LineStart: l.start, LineEnd: l.end, LineSHA256: hash(l.text), FragmentIDs: append([]string(nil), l.refs...), GlobalHitKnown: true}
}
