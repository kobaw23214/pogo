package tools

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// ArgsValidator lets a tool take over validation because it accepts argument
// shapes its own schema does not describe.
//
// This is an opt-out on purpose. Leniency is sometimes worth having — see Edit,
// which accepts a flat single-edit form models keep sending — but it should be a
// visible decision in the tool that wants it, not a hole in the check everyone
// else relies on.
type ArgsValidator interface {
	ValidateArgs(args json.RawMessage) error
}

// ValidateArgs checks a call's arguments against the tool's schema before
// anything runs.
//
// It exists because the alternative is what the model used to receive: Go's own
// unmarshal error, e.g. "json: cannot unmarshal number into Go struct field
// readArgs.path of type string". That names Go internals the model cannot act on,
// and says nothing at all when a required field is simply absent — the argument
// just arrives as a zero value and the tool fails later for a reason that looks
// unrelated.
//
// Unknown fields are deliberately tolerated: models add harmless extras, and
// refusing those would cost a turn for nothing. They are only mentioned when one
// of them looks like a misspelling of a field that is missing.
func ValidateArgs(t Tool, args json.RawMessage) error {
	if v, ok := t.(ArgsValidator); ok {
		return v.ValidateArgs(args)
	}

	// Use reflection-based schema if available, otherwise fall back to hand-written
	schema := t.InputSchema()
	if sp, ok := t.(SchemaProvider); ok {
		schema = sp.Schema()
	}
	return validateAgainst(schema, args, "")
}

func validateAgainst(schema map[string]any, raw json.RawMessage, prefix string) error {
	fields, err := objectFields(raw)
	if err != nil {
		return err
	}

	props, _ := schema["properties"].(map[string]any)
	var problems []string

	for _, name := range requiredNames(schema) {
		v, ok := fields[name]
		if ok && jsonKind(v) != "null" {
			continue
		}
		problems = append(problems, missingFieldProblem(prefix+name, name, fields, props))
	}

	// Sorted so a call with two bad fields reports them the same way every time.
	// A message that reorders itself between identical calls reads like two
	// different problems.
	for _, name := range sortedKeys(fields) {
		spec, ok := props[name].(map[string]any)
		if !ok {
			continue // unknown field: tolerated, see the doc comment
		}
		want, _ := spec["type"].(string)
		got := jsonKind(fields[name])
		if typeMatches(want, fields[name]) {
			continue
		}
		problems = append(problems, fmt.Sprintf("field %q must be %s, got %s", prefix+name, article(want), got))
	}

	// Enum values, checked here rather than left to each tool. A field with a closed
	// set of values is the case where naming the alternatives costs nothing and
	// guessing costs a turn, and the model has usually sent something close —
	// "readonly" for "explore" — which a bare "invalid value" would not help with.
	for _, name := range sortedKeys(fields) {
		spec, ok := props[name].(map[string]any)
		if !ok {
			continue
		}
		allowed := enumValues(spec)
		if len(allowed) == 0 || jsonKind(fields[name]) != "string" {
			continue
		}
		var got string
		if json.Unmarshal(fields[name], &got) != nil {
			continue
		}
		if !contains(allowed, got) {
			problems = append(problems, fmt.Sprintf("field %q must be one of %s, got %q",
				prefix+name, strings.Join(quoteAll(allowed), ", "), got))
		}
	}

	if len(problems) > 0 {
		return fmt.Errorf("%s. Expected: %s", strings.Join(problems, "; "), describeFields(schema))
	}

	// Only recurse once the shallow shape is sound, so the first message the model
	// reads is about the outer object rather than about elements of an array that
	// should not have been there in the first place.
	for _, name := range sortedKeys(fields) {
		spec, ok := props[name].(map[string]any)
		if !ok {
			continue
		}
		items, ok := spec["items"].(map[string]any)
		if !ok || jsonKind(fields[name]) != "array" {
			continue
		}
		var elems []json.RawMessage
		if json.Unmarshal(fields[name], &elems) != nil {
			continue
		}
		want, _ := items["type"].(string)
		if want == "object" {
			for i, el := range elems {
				if err := validateAgainst(items, el, fmt.Sprintf("%s%s[%d].", prefix, name, i)); err != nil {
					return err
				}
			}
			continue
		}
		// Scalar elements get a type (and enum) check against items, named by
		// position. Recursing instead would march every element back through
		// objectFields and demand it be an object: read's paths[] is an array of
		// strings, and it died on exactly that — with a message blaming the whole
		// arguments, so the model spent a turn re-guessing JSON that had been
		// well-formed all along. An items without a type means "anything":
		// nothing to check, and nothing to demand.
		for i, el := range elems {
			display := fmt.Sprintf("%s%s[%d]", prefix, name, i)
			got := jsonKind(el)
			if want != "" && !typeMatches(want, el) {
				return fmt.Errorf("field %q must be %s, got %s", display, article(want), got)
			}
			if allowed := enumValues(items); len(allowed) > 0 && got == "string" {
				var s string
				if json.Unmarshal(el, &s) == nil && !contains(allowed, s) {
					return fmt.Errorf("field %q must be one of %s, got %q",
						display, strings.Join(quoteAll(allowed), ", "), s)
				}
			}
		}
	}
	return nil
}

// objectFields decodes the argument object. Absent or empty arguments are an
// empty object rather than an error: a tool with no required fields is legitimately
// called with nothing, and the required check below reports the rest.
func objectFields(raw json.RawMessage) (map[string]json.RawMessage, error) {
	if len(strings.TrimSpace(string(raw))) == 0 {
		return map[string]json.RawMessage{}, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, fmt.Errorf("arguments must be a JSON object, got %s", jsonKind(raw))
	}
	return fields, nil
}

// missingFieldProblem names the absent field and, when one of the supplied fields
// looks like a misspelling of it, says so.
//
// That hint is the difference between one wasted turn and none: a model that sent
// file_path needs to be told which name this tool uses, not merely that something
// is missing.
func missingFieldProblem(display, name string, fields map[string]json.RawMessage, props map[string]any) string {
	var unknown []string
	for _, got := range sortedKeys(fields) {
		if _, known := props[got]; known {
			continue
		}
		if looksLike(got, name) {
			return fmt.Sprintf("missing required field %q (received %q, which is not a field of this tool)", display, got)
		}
		unknown = append(unknown, got)
	}
	if len(unknown) > 0 {
		// No near-match, but something unrecognised did arrive — "cmd" for
		// "command" is a real case that no cheap similarity test catches. Naming
		// what was received makes the mistake visible instead of leaving the model
		// to wonder whether its key was ignored or misread.
		return fmt.Sprintf("missing required field %q (unrecognised field(s) received: %s)",
			display, strings.Join(unknown, ", "))
	}
	return fmt.Sprintf("missing required field %q", display)
}

// looksLike reports whether a supplied name is plausibly a variant of a wanted
// one: case, separators, and common prefixes like file_path for path.
func looksLike(got, want string) bool {
	g, w := fold(got), fold(want)
	if g == w {
		return true
	}
	// Guard on length so that a one-letter field does not match everything.
	return len(w) >= 3 && (strings.Contains(g, w) || strings.Contains(w, g))
}

func fold(s string) string {
	return strings.NewReplacer("_", "", "-", "", " ", "").Replace(strings.ToLower(s))
}

// describeFields renders the tool's fields so the model can correct itself from
// the error alone, without another round trip to re-read the schema.
func describeFields(schema map[string]any) string {
	props, _ := schema["properties"].(map[string]any)
	if len(props) == 0 {
		return "no fields"
	}
	required := map[string]bool{}
	for _, n := range requiredNames(schema) {
		required[n] = true
	}
	out := make([]string, 0, len(props))
	for _, name := range sortedKeys(props) {
		spec, _ := props[name].(map[string]any)
		typ, _ := spec["type"].(string)
		if typ == "" {
			typ = "any"
		}
		if required[name] {
			typ += ", required"
		}
		out = append(out, fmt.Sprintf("%s (%s)", name, typ))
	}
	return strings.Join(out, ", ")
}

// requiredNames reads the required list, tolerating both the []string the
// hand-written schemas use and the []any a schema that made a JSON round trip
// would produce.
func requiredNames(schema map[string]any) []string {
	switch v := schema["required"].(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, e := range v {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// enumValues reads an enum list, tolerating both the []string a hand-written
// schema uses and the []any one that made a JSON round trip produces — the same
// two shapes requiredNames has to handle, for the same reason.
func enumValues(spec map[string]any) []string {
	switch v := spec["enum"].(type) {
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, e := range v {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

func contains(list []string, s string) bool {
	for _, e := range list {
		if e == s {
			return true
		}
	}
	return false
}

func quoteAll(list []string) []string {
	out := make([]string, len(list))
	for i, s := range list {
		out[i] = fmt.Sprintf("%q", s)
	}
	return out
}

// jsonKind names the JSON type of a raw value using JSON Schema's vocabulary, so
// the message the model reads uses the same words as the schema it was given.
func jsonKind(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if s == "" {
		return "null"
	}
	switch s[0] {
	case '"':
		return "string"
	case '{':
		return "object"
	case '[':
		return "array"
	case 't', 'f':
		return "boolean"
	case 'n':
		return "null"
	default:
		return "number"
	}
}

// typeMatches reports whether a value satisfies the type its schema declares.
// Name equality is the rule for every kind but one: JSON has a single number
// kind, so a schema asking for the "integer" subset can never be answered by
// comparing names — an MCP server declaring current and size as integers,
// sent {"current":1,"size":1}, was refused with "must be a integer, got
// number" before the tool could run. jsonKind keeps calling every JSON number
// "number" (the string mismatch messages already quote that word), so the
// integer question goes to the literal itself.
func typeMatches(want string, raw json.RawMessage) bool {
	got := jsonKind(raw)
	if want == "" || got == "null" || want == got {
		return true
	}
	if want == "integer" && got == "number" {
		return isIntegerLiteral(raw)
	}
	return false
}

// isIntegerLiteral reports whether a JSON number is mathematically whole — no
// fractional part survives its decimal point and exponent — by reading the
// decimal literal, never a parsed value.
//
// float64 is the trap: 9007199254740993 and 1.0000000000000001 both land on
// numbers float64 can represent and would be called whole whether they are or
// not. math/big is exact but spends memory proportional to the exponent, and
// an exponent is caller-controlled input, so even a huge exponent must be
// handled in bounded space. Digit counting
// answers exactly: with f fraction digits and exponent e the value is the
// digit run times 10^(e-f), which is whole when e >= f, or when the digits
// the exponent would leave past the point are all zeros anyway (1.0, 1.20e1,
// 1000e-3); and an all-zero run is zero, whole at any exponent.
func isIntegerLiteral(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	i := 0
	if i < len(s) && s[i] == '-' {
		i++
	}
	intFrom := i
	for i < len(s) && isDigit(s[i]) {
		i++
	}
	intRun := s[intFrom:i]
	if len(intRun) > 1 && intRun[0] == '0' {
		return false // JSON forbids leading zeros in the integer part.
	}

	fracRun := ""
	if i < len(s) && s[i] == '.' {
		i++
		fracFrom := i
		for i < len(s) && isDigit(s[i]) {
			i++
		}
		fracRun = s[fracFrom:i]
		if fracRun == "" {
			return false // "1." is not a number; decoded JSON never sends it
		}
	}

	expDigits := ""
	expNegative := false
	if i < len(s) && (s[i] == 'e' || s[i] == 'E') {
		i++
		if i < len(s) && (s[i] == '+' || s[i] == '-') {
			expNegative = s[i] == '-'
			i++
		}
		expFrom := i
		for i < len(s) && isDigit(s[i]) {
			i++
		}
		if expFrom == i {
			return false // "1e" likewise
		}
		expDigits = strings.TrimLeft(s[expFrom:i], "0")
	}
	if intRun == "" || i != len(s) {
		return false // "-", ".5", trailing junk: nothing to vouch for
	}

	if allZeroDigits(intRun) && allZeroDigits(fracRun) {
		return true // zero, whole at any exponent: 0, -0, 0.000e-999...
	}
	// Trailing zeros of both digit runs read as one: the fraction's count,
	// plus the integer part's when the fraction is zeros all the way through,
	// so nothing nonzero separates the runs.
	zeros := trailingZeroDigits(fracRun)
	if allZeroDigits(fracRun) {
		zeros += trailingZeroDigits(intRun)
	}
	return exponentAtLeast(expDigits, expNegative, int64(len(fracRun))-zeros)
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// allZeroDigits reports whether a digit run is all zeros. An empty run is,
// vacuously — which is what the arithmetic in isIntegerLiteral wants.
func allZeroDigits(s string) bool {
	return strings.Trim(s, "0") == ""
}

func trailingZeroDigits(s string) int64 {
	var n int64
	for i := len(s) - 1; i >= 0 && s[i] == '0'; i-- {
		n++
	}
	return n
}

// Compare an arbitrarily long exponent with a threshold bounded by the
// mantissa's length. No exponent conversion, multiplication or big allocation.
func exponentAtLeast(digits string, negative bool, threshold int64) bool {
	if digits == "" {
		return threshold <= 0
	}
	if negative && threshold >= 0 {
		return false
	}
	if !negative && threshold <= 0 {
		return true
	}
	if threshold < 0 {
		threshold = -threshold // Cannot be MinInt64: it is a difference of lengths.
	}
	want := strconv.FormatInt(threshold, 10)
	comparison := len(digits) - len(want)
	if comparison == 0 {
		comparison = strings.Compare(digits, want)
	}
	if negative {
		return comparison <= 0
	}
	return comparison >= 0
}

func article(typ string) string {
	if strings.HasPrefix(typ, "a") || strings.HasPrefix(typ, "o") {
		return "an " + typ
	}
	return "a " + typ
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
