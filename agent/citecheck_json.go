package agent

import (
	"bytes"
	"encoding/json"
	"io"
	"reflect"
	"strings"
)

// CitationRepairStatus is host-only acceptance metadata. A repair may improve
// citation placement; it never certifies that a source supports a claim.
type CitationRepairStatus struct {
	Attempted bool   `json:"attempted"`
	Accepted  bool   `json:"accepted"`
	Reason    string `json:"reason,omitempty"`
}

type citationJSONObject struct {
	fields  map[string]json.RawMessage
	sources []string
	fence   string
}

// Only an explicit sources string-array opts a machine-readable answer into
// citation repair. Other JSON schemas and code examples remain untouched.
func parseCitationJSONObject(answer string) *citationJSONObject {
	text := strings.TrimSpace(answer)
	if len(text) == 0 || len(text) > 1<<20 {
		return nil
	}
	object := &citationJSONObject{}
	if strings.HasPrefix(text, "```") || strings.HasPrefix(text, "~~~") {
		newline := strings.IndexByte(text, '\n')
		if newline < 0 {
			return nil
		}
		opener := strings.TrimSpace(text[:newline])
		marker := opener[:3]
		language := strings.TrimSpace(opener[3:])
		if language != "" && !strings.EqualFold(language, "json") || !strings.HasSuffix(text, marker) {
			return nil
		}
		object.fence = opener
		text = strings.TrimSpace(text[newline+1 : len(text)-len(marker)])
	}
	decoder := json.NewDecoder(strings.NewReader(text))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil
	}
	object.fields = make(map[string]json.RawMessage)
	for decoder.More() {
		token, err = decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return nil
		}
		if _, duplicate := object.fields[key]; duplicate {
			return nil
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return nil
		}
		object.fields[key] = value
	}
	if token, err = decoder.Token(); err != nil || token != json.Delim('}') {
		return nil
	}
	if _, err = decoder.Token(); err != io.EOF {
		return nil
	}
	value, declared := object.fields["sources"]
	value = bytes.TrimSpace(value)
	if !declared || len(value) == 0 || value[0] != '[' {
		return nil
	}
	var elements []json.RawMessage
	if json.Unmarshal(value, &elements) != nil {
		return nil
	}
	for _, element := range elements {
		element = bytes.TrimSpace(element)
		var source string
		if len(element) == 0 || element[0] != '"' || json.Unmarshal(element, &source) != nil {
			return nil
		}
		object.sources = append(object.sources, source)
	}
	return object
}

func citationCheckedText(answer string) string {
	if object := parseCitationJSONObject(answer); object != nil {
		return strings.Join(object.sources, "\n")
	}
	if json.Valid([]byte(strings.TrimSpace(answer))) {
		// A different machine-readable schema may contain citation syntax as
		// literal data. It has not opted into rewriting its values or structure.
		return ""
	}
	return stripCodeSpans(answer)
}

func hasRegisteredCitation(text string, store *SourceStore) bool {
	for _, match := range citeAnchorRe.FindAllStringSubmatch(text, -1) {
		if _, ok := store.Get(match[2]); ok {
			return true
		}
	}
	return false
}

// Protect every non-citation value, not a fixture-specific list of "core"
// keys. We cannot safely distinguish substantive data from arbitrary prose
// fields. A changed provenance explanation therefore remains an explicit
// rejected repair rather than silently replacing facts or inventing citations.
func structuredCitationRewriteFailure(original, replacement string, store *SourceStore) string {
	before := parseCitationJSONObject(original)
	if before == nil {
		return ""
	}
	after := parseCitationJSONObject(replacement)
	if after == nil || before.fence != after.fence {
		return "structured_format_changed"
	}
	if len(before.fields) != len(after.fields) {
		return "structured_answer_changed"
	}
	for key, value := range before.fields {
		other, exists := after.fields[key]
		if !exists {
			return "structured_answer_changed"
		}
		if key == "sources" {
			continue
		}
		decode := func(raw json.RawMessage) (any, error) {
			decoder := json.NewDecoder(bytes.NewReader(raw))
			decoder.UseNumber()
			var result any
			err := decoder.Decode(&result)
			return result, err
		}
		left, leftErr := decode(value)
		right, rightErr := decode(other)
		if leftErr != nil || rightErr != nil || !reflect.DeepEqual(left, right) {
			return "structured_answer_changed"
		}
	}
	text := strings.Join(after.sources, "\n")
	if !hasRegisteredCitation(text, store) || len(ValidateCitations(replacement, store)) != 0 || len(FindPageMarkers(replacement)) != 0 {
		return "structured_sources_missing_or_invalid"
	}
	return ""
}

func structuredCitationNotice(answer string, labels []string) string {
	if parseCitationJSONObject(answer) == nil {
		return ""
	}
	if len(labels) > 24 {
		labels = labels[len(labels)-24:]
	}
	available := ""
	if len(labels) > 0 {
		available = " The current task read material with actual usable source headers: " + strings.Join(labels, ", ") + "."
	}
	return " This answer declares a JSON sources array." + available +
		" Return the same JSON object and keep its current raw/fenced format, key set, types, factual values and original quotations. " +
		"Use the declared sources array for actual supporting [1](#cite:Sx) anchors; do not add Markdown prose outside the JSON or append anchors to quoted evidence. " +
		"Do not remove all sources to evade an invalid-label correction. A visible [cite:...] header is a usable citation label; do not replace the answer with an unsupported claim that no labels were provided. " +
		"Correct a mistaken source-availability explanation only if that can be done without changing substantive data. If it cannot, preserve the original data rather than inventing evidence."
}
