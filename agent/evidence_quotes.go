package agent

import (
	"encoding/json"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const evidenceQuoteMaxItems = 128

// EvidenceQuoteCheck concerns literal reproduction only. It does not establish
// that a quotation supports the surrounding claim or identifies the right row.
// It is host metadata, never an instruction or a source stamp.
type EvidenceQuoteCheck struct {
	OptedIn    bool     `json:"opted_in"`
	Passed     bool     `json:"passed"`
	Verifiable bool     `json:"verifiable"`
	Reason     string   `json:"reason,omitempty"`
	Paths      []string `json:"paths"`
}

type evidenceQuoteAnswer struct {
	object *citationJSONObject
	quotes []string
}

// The visible citation contract, not the incidental name of an arbitrary JSON
// string, declares this narrow schema to contain verbatim evidence excerpts.
func EvidenceQuoteContract() string {
	return "For a standalone JSON answer declaring sources as a string array and evidence as an array of objects with quote string fields, each evidence[].quote is a verbatim excerpt, not a paraphrase. " +
		"Copy its exact characters from saved-result body text actually delivered by successful native tool_result reads. Do not silently change words, whitespace, punctuation or Unicode spelling. " +
		"Ordinary description fields and other JSON schemas are not quotations. Source headers, directories, previews, omitted-page receipts and scan totals are not the missing source body. " +
		"A literal quotation check verifies reproduction only; its relevance and attribution must still be supported."
}

func parseEvidenceQuoteAnswer(answer string) (*evidenceQuoteAnswer, string) {
	object := parseCitationJSONObject(answer)
	if object == nil {
		return nil, ""
	}
	raw, declared := object.fields["evidence"]
	if !declared || len(strings.TrimSpace(string(raw))) == 0 || strings.TrimSpace(string(raw))[0] != '[' {
		return nil, ""
	}
	var items []map[string]json.RawMessage
	if json.Unmarshal(raw, &items) != nil || len(items) == 0 {
		return nil, ""
	}
	parsed := &evidenceQuoteAnswer{object: object, quotes: make([]string, 0, min(len(items), evidenceQuoteMaxItems))}
	for _, item := range items {
		value, found := item["quote"]
		trimmed := strings.TrimSpace(string(value))
		var quote string
		if !found || len(trimmed) == 0 || trimmed[0] != '"' || json.Unmarshal(value, &quote) != nil {
			return nil, ""
		}
		if len(parsed.quotes) < evidenceQuoteMaxItems {
			parsed.quotes = append(parsed.quotes, quote)
		}
	}
	if len(items) > evidenceQuoteMaxItems {
		return parsed, "quote_contract_exceeds_limit"
	}
	if !utf8.ValidString(answer) {
		return parsed, "invalid_quote_contract"
	}
	// Duplicate nested keys could otherwise disappear during comparison of the
	// non-quote values. Ambiguous or excessively deep JSON is not repaired.
	for _, value := range object.fields {
		decoder := json.NewDecoder(strings.NewReader(string(value)))
		decoder.UseNumber()
		if !evidenceQuoteUniqueValue(decoder, 0) {
			return parsed, "invalid_quote_contract"
		}
	}
	return parsed, ""
}

func evidenceQuoteUniqueValue(decoder *json.Decoder, depth int) bool {
	if depth > 32 {
		return false
	}
	token, err := decoder.Token()
	if err != nil {
		return false
	}
	delim, container := token.(json.Delim)
	if !container {
		return true
	}
	if delim == '{' {
		seen := map[string]bool{}
		for decoder.More() {
			token, err = decoder.Token()
			key, ok := token.(string)
			if err != nil || !ok || seen[key] || !evidenceQuoteUniqueValue(decoder, depth+1) {
				return false
			}
			seen[key] = true
		}
		token, err = decoder.Token()
		return err == nil && token == json.Delim('}')
	}
	if delim == '[' {
		for decoder.More() {
			if !evidenceQuoteUniqueValue(decoder, depth+1) {
				return false
			}
		}
		token, err = decoder.Token()
		return err == nil && token == json.Delim(']')
	}
	return false
}

type evidenceQuoteRange struct {
	start, end int
	text       string
}

// Merge only observed adjacent or byte-consistent overlapping ranges in the
// SAME result, representation and selected string. A store lookup, count or
// another source cannot supply an unobserved character.
func evidenceQuoteTexts(ledger *evidenceLedger) ([]string, bool) {
	if ledger == nil {
		return nil, false
	}
	texts := []string{}
	groups := map[string][]evidenceQuoteRange{}
	complete := !ledger.Incomplete()
	for _, fragment := range ledger.Fragments() {
		if !sourceResultID(fragment.ResultID) || !utf8.ValidString(fragment.Text) || fragment.Text == "" ||
			(fragment.Domain != "stored_text" && fragment.Domain != "json" && fragment.Domain != "native") {
			complete = false
			continue
		}
		if !fragment.HasRange {
			if fragment.CompleteValue {
				texts = append(texts, fragment.Text)
			} else {
				complete = false
			}
			continue
		}
		if fragment.Start < 0 || fragment.End < fragment.Start || fragment.End-fragment.Start != len(fragment.Text) {
			complete = false
			continue
		}
		key := fragment.ResultID + "\x00" + fragment.Domain + "\x00" + fragment.Pointer
		groups[key] = append(groups[key], evidenceQuoteRange{fragment.Start, fragment.End, fragment.Text})
	}
	for _, intervals := range groups {
		sort.Slice(intervals, func(i, j int) bool {
			if intervals[i].start == intervals[j].start {
				return intervals[i].end < intervals[j].end
			}
			return intervals[i].start < intervals[j].start
		})
		merged := []evidenceQuoteRange{}
		valid := true
		for _, interval := range intervals {
			if len(merged) == 0 || interval.start > merged[len(merged)-1].end {
				merged = append(merged, interval)
				continue
			}
			last := &merged[len(merged)-1]
			overlap := min(last.end, interval.end) - interval.start
			if overlap > 0 && last.text[interval.start-last.start:interval.start-last.start+overlap] != interval.text[:overlap] {
				valid = false
				break
			}
			if interval.end > last.end {
				last.text += interval.text[last.end-interval.start:]
				last.end = interval.end
			}
		}
		if !valid {
			complete = false
			continue
		}
		for _, interval := range merged {
			texts = append(texts, interval.text)
		}
	}
	return texts, complete
}

func CheckEvidenceQuotes(answer string, ledger *evidenceLedger) EvidenceQuoteCheck {
	check := EvidenceQuoteCheck{Paths: []string{}}
	parsed, problem := parseEvidenceQuoteAnswer(answer)
	if parsed == nil {
		return check
	}
	check.OptedIn = true
	if problem != "" {
		check.Reason = problem
		return check
	}
	texts, complete := evidenceQuoteTexts(ledger)
	for index, quote := range parsed.quotes {
		found := false
		if strings.TrimSpace(quote) != "" {
			for _, text := range texts {
				if strings.Contains(text, quote) {
					found = true
					break
				}
			}
		}
		if !found {
			check.Paths = append(check.Paths, "/evidence/"+strconv.Itoa(index)+"/quote")
		}
	}
	if len(check.Paths) == 0 {
		check.Passed, check.Verifiable = true, true
	} else if len(texts) == 0 {
		check.Reason = "no_delivered_native_body"
	} else if !complete {
		check.Reason = "delivery_ledger_incomplete"
	} else {
		check.Verifiable = true
		check.Reason = "quotes_not_verbatim_in_delivered_text"
	}
	return check
}

func EvidenceQuoteRepairNotice(check EvidenceQuoteCheck) string {
	if !check.OptedIn || check.Passed || !check.Verifiable {
		return ""
	}
	paths, _ := json.Marshal(check.Paths)
	return "[pogo] verbatim quotation check: the explicitly declared evidence quote string(s) at JSON paths " + string(paths) +
		" do not occur character-for-character in saved-result body text actually delivered by successful native reads in this task. " +
		"Return the complete same JSON answer in its original raw/fenced format. Change only evidence[].quote strings to exact excerpts from the existing delivered source text; keep every other field, array order, type, business value and source citation unchanged. " +
		"Do not paraphrase, normalize punctuation, invent missing text, or use a preview/scan count as the body. Do not call tools or fetch anything again. " +
		"If the existing evidence cannot verify a quote, do not guess a replacement or claim that verification succeeded."
}

func evidenceQuoteProtectedValues(parsed *evidenceQuoteAnswer) (map[string]any, bool) {
	values := make(map[string]any, len(parsed.object.fields))
	for key, raw := range parsed.object.fields {
		decoder := json.NewDecoder(strings.NewReader(string(raw)))
		decoder.UseNumber()
		var value any
		if decoder.Decode(&value) != nil {
			return nil, false
		}
		if key == "evidence" {
			items, ok := value.([]any)
			if !ok {
				return nil, false
			}
			for _, item := range items {
				entry, ok := item.(map[string]any)
				if !ok {
					return nil, false
				}
				delete(entry, "quote")
			}
		}
		values[key] = value
	}
	return values, true
}

func EvidenceQuoteRewriteFailure(original, replacement string, ledger *evidenceLedger) string {
	before, problem := parseEvidenceQuoteAnswer(original)
	if before == nil {
		return "quote_contract_not_declared"
	}
	if problem != "" {
		return problem
	}
	after, problem := parseEvidenceQuoteAnswer(replacement)
	if after == nil || problem != "" {
		return "quote_rewrite_contract_changed"
	}
	if before.object.fence != after.object.fence {
		return "quote_rewrite_format_changed"
	}
	left, leftOK := evidenceQuoteProtectedValues(before)
	right, rightOK := evidenceQuoteProtectedValues(after)
	if !leftOK || !rightOK || !reflect.DeepEqual(left, right) {
		return "quote_rewrite_changed_nonquote_values"
	}
	checked := CheckEvidenceQuotes(replacement, ledger)
	if !checked.Verifiable {
		return "quote_rewrite_unverifiable"
	}
	if !checked.Passed {
		return "quote_rewrite_still_not_verbatim"
	}
	return ""
}
