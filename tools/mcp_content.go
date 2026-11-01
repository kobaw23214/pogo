package tools

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"unicode/utf8"
)

// maxMCPJSONNumberExponent bounds the decimal exponent accepted by the
// semantic JSON comparison. Exponents beyond this are valid JSON, but
// comparing them with arbitrary-precision arithmetic can turn a small hostile
// token into a huge allocation. Uncertain numbers make the comparison fail,
// which preserves both model-visible copies.
const maxMCPJSONNumberExponent = 10000

// containsIdenticalJSONValue reports whether any complete text block is one
// valid JSON value with the same semantic content as the marshalled
// structuredContent. Text is untrusted server output: on any malformed or
// unsupported value this returns false rather than guessing.
func containsIdenticalJSONValue(textBlocks []string, structured []byte) bool {
	if len(textBlocks) == 0 || len(structured) == 0 {
		return false
	}
	want, err := decodeSingleJSONValue(structured)
	if err != nil {
		return false
	}
	for _, text := range textBlocks {
		got, err := decodeSingleJSONValue([]byte(text))
		if err == nil && jsonValuesEqual(got, want) {
			return true
		}
	}
	return false
}

// decodeSingleJSONValue decodes exactly one JSON value and rejects trailing
// data. Concatenated blocks therefore cannot manufacture a match.
func decodeSingleJSONValue(data []byte) (any, error) {
	if !utf8.Valid(data) {
		return nil, io.ErrUnexpectedEOF
	}
	if duplicate, err := jsonHasDuplicateObjectKeys(data); err != nil || duplicate {
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return nil, err
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			err = io.ErrUnexpectedEOF
		}
		return nil, err
	}
	return value, nil
}

// jsonHasDuplicateObjectKeys rejects the one object shape for which
// encoding/json defines behavior but JSON interoperability does not. The last
// duplicate would otherwise look semantically identical while the model-visible
// text still carries more members than structuredContent.
func jsonHasDuplicateObjectKeys(data []byte) (bool, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	return scanJSONForDuplicateKeys(dec, 0)
}

// Scan one token stream rather than repeatedly decoding nested RawMessages.
// Extremely deep values are kept in both representations, not recursively
// expanded for an optional presentation optimization.
func scanJSONForDuplicateKeys(dec *json.Decoder, depth int) (bool, error) {
	if depth > 256 {
		return false, io.ErrUnexpectedEOF
	}
	open, err := dec.Token()
	if err != nil {
		return false, err
	}
	delim, ok := open.(json.Delim)
	if !ok || (delim != '{' && delim != '[') {
		return false, nil
	}
	if delim == '[' {
		for dec.More() {
			duplicate, err := scanJSONForDuplicateKeys(dec, depth+1)
			if err != nil || duplicate {
				return duplicate, err
			}
		}
		_, err := dec.Token()
		return false, err
	}

	seen := make(map[string]struct{})
	for dec.More() {
		keyToken, err := dec.Token()
		if err != nil {
			return false, err
		}
		key, ok := keyToken.(string)
		if !ok || strings.ContainsRune(key, utf8.RuneError) {
			return false, io.ErrUnexpectedEOF
		}
		if _, exists := seen[key]; exists {
			return true, nil
		}
		seen[key] = struct{}{}
		duplicate, err := scanJSONForDuplicateKeys(dec, depth+1)
		if err != nil || duplicate {
			return duplicate, err
		}
	}
	_, err = dec.Token()
	return false, err
}

// jsonValuesEqual compares values decoded with UseNumber. Object key order and
// insignificant whitespace are already invisible to the decoder; arrays keep
// their order, and numbers use an exact decimal comparison.
func jsonValuesEqual(got, want any) bool {
	switch gotValue := got.(type) {
	case nil:
		return want == nil
	case bool:
		wantValue, ok := want.(bool)
		return ok && gotValue == wantValue
	case string:
		wantValue, ok := want.(string)
		// encoding/json replaces malformed surrogate escapes with RuneError.
		// Keep both on that ambiguity, even for a literal replacement character.
		return ok && !strings.ContainsRune(gotValue, utf8.RuneError) && gotValue == wantValue
	case json.Number:
		wantValue, ok := want.(json.Number)
		return ok && jsonNumbersEqual(gotValue, wantValue)
	case []any:
		wantValue, ok := want.([]any)
		if !ok || len(gotValue) != len(wantValue) {
			return false
		}
		for i := range gotValue {
			if !jsonValuesEqual(gotValue[i], wantValue[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		wantValue, ok := want.(map[string]any)
		if !ok || len(gotValue) != len(wantValue) {
			return false
		}
		for key, gotChild := range gotValue {
			wantChild, ok := wantValue[key]
			if !ok || !jsonValuesEqual(gotChild, wantChild) {
				return false
			}
		}
		return true
	default:
		return false
	}
}

// mcpJSONNumber is a normalized decimal: sign*digits*10^scale. It deliberately
// does not use float64 (which would conflate adjacent integers) or big.Rat
// (which would expand a hostile exponent).
type mcpJSONNumber struct {
	negative bool
	zero     bool
	digits   string
	scale    int
}

func jsonNumbersEqual(got, want json.Number) bool {
	gotNumber, ok := parseMCPJSONNumber(got)
	if !ok {
		return false
	}
	wantNumber, ok := parseMCPJSONNumber(want)
	if !ok {
		return false
	}
	if gotNumber.zero || wantNumber.zero {
		return gotNumber.zero && wantNumber.zero
	}
	return gotNumber.negative == wantNumber.negative &&
		gotNumber.scale == wantNumber.scale &&
		gotNumber.digits == wantNumber.digits
}

func parseMCPJSONNumber(value json.Number) (mcpJSONNumber, bool) {
	raw := string(value)
	number := mcpJSONNumber{}
	if raw == "" {
		return number, false
	}
	if raw[0] == '-' {
		number.negative = true
		raw = raw[1:]
	}

	mantissa := raw
	exponent := 0
	if index := strings.IndexAny(raw, "eE"); index >= 0 {
		mantissa = raw[:index]
		parsed, ok := parseBoundedJSONExponent(raw[index+1:])
		if !ok {
			return mcpJSONNumber{}, false
		}
		exponent = parsed
	}

	integer := mantissa
	fraction := ""
	if index := strings.IndexByte(mantissa, '.'); index >= 0 {
		integer = mantissa[:index]
		fraction = mantissa[index+1:]
	}
	digits := integer + fraction
	digits = strings.TrimLeft(digits, "0")
	if digits == "" {
		number.zero = true
		return number, true
	}
	trimmed := strings.TrimRight(digits, "0")
	number.scale = exponent - len(fraction) + len(digits) - len(trimmed)
	number.digits = trimmed
	return number, number.scale >= -maxMCPJSONNumberExponent &&
		number.scale <= maxMCPJSONNumberExponent
}

// parseBoundedJSONExponent accepts only exponents whose value fits the
// comparison bound. More than eight exponent digits is itself an obscure
// representation; rejecting it early also bounds work on hostile input.
func parseBoundedJSONExponent(raw string) (int, bool) {
	if raw == "" {
		return 0, false
	}
	negative := false
	switch raw[0] {
	case '+':
		raw = raw[1:]
	case '-':
		negative = true
		raw = raw[1:]
	}
	if len(raw) == 0 || len(raw) > 8 {
		return 0, false
	}
	value := 0
	for index := 0; index < len(raw); index++ {
		digit := raw[index]
		if digit < '0' || digit > '9' {
			return 0, false
		}
		value = value*10 + int(digit-'0')
	}
	if negative {
		value = -value
	}
	return value, value >= -maxMCPJSONNumberExponent && value <= maxMCPJSONNumberExponent
}
