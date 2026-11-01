package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

// Document resource limits from SPEC §3.
const (
	// MaxDocumentBytes is the maximum size of one protocol document (1 MiB).
	MaxDocumentBytes = 1 << 20
	// MaxJSONDepth is the maximum nesting depth of one protocol document.
	MaxJSONDepth = 32
)

// decodeRawJSON strictly decodes one JSON document into generic values:
//   - rejects documents over MaxDocumentBytes or deeper than MaxJSONDepth;
//   - rejects duplicate object keys (which encoding/json silently accepts);
//   - rejects non-finite numbers (e.g. 1e999);
//   - rejects trailing garbage after the top-level value;
//   - preserves numbers as json.Number for exact validation.
func decodeRawJSON(data []byte) (any, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("empty document")
	}
	if len(data) > MaxDocumentBytes {
		return nil, fmt.Errorf("document too large: %d bytes > %d", len(data), MaxDocumentBytes)
	}
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("document is not valid UTF-8")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	v, err := decodeValue(dec, 1)
	if err != nil {
		return nil, err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, fmt.Errorf("trailing data after top-level JSON value")
		}
		return nil, fmt.Errorf("trailing data after top-level JSON value: %v", err)
	}
	return convertNumbers(v), nil
}

// convertNumbers rewrites json.Number leaves to int64 (when integral and in
// range) or float64 so that downstream schema validation sees real numeric
// types. Finiteness was already enforced by decodeValue.
func convertNumbers(v any) any {
	switch t := v.(type) {
	case json.Number:
		s := t.String()
		if !strings.ContainsAny(s, ".eE") {
			if i, err := strconv.ParseInt(s, 10, 64); err == nil {
				return i
			}
		}
		f, _ := strconv.ParseFloat(s, 64) // already validated finite
		return f
	case map[string]any:
		for k, sub := range t {
			t[k] = convertNumbers(sub)
		}
		return t
	case []any:
		for i, sub := range t {
			t[i] = convertNumbers(sub)
		}
		return t
	default:
		return v
	}
}

func decodeValue(dec *json.Decoder, depth int) (any, error) {
	if depth > MaxJSONDepth {
		return nil, fmt.Errorf("document deeper than %d levels", MaxJSONDepth)
	}
	tok, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("invalid JSON: %v", err)
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			obj := make(map[string]any, 8)
			for dec.More() {
				ktok, err := dec.Token()
				if err != nil {
					return nil, fmt.Errorf("invalid JSON object key: %v", err)
				}
				key, ok := ktok.(string)
				if !ok {
					return nil, fmt.Errorf("invalid JSON object key type")
				}
				if _, dup := obj[key]; dup {
					return nil, fmt.Errorf("duplicate JSON key %q", key)
				}
				val, err := decodeValue(dec, depth+1)
				if err != nil {
					return nil, err
				}
				obj[key] = val
			}
			if _, err := dec.Token(); err != nil { // closing '}'
				return nil, fmt.Errorf("invalid JSON: %v", err)
			}
			return obj, nil
		case '[':
			arr := []any{}
			for dec.More() {
				val, err := decodeValue(dec, depth+1)
				if err != nil {
					return nil, err
				}
				arr = append(arr, val)
			}
			if _, err := dec.Token(); err != nil { // closing ']'
				return nil, fmt.Errorf("invalid JSON: %v", err)
			}
			return arr, nil
		default:
			return nil, fmt.Errorf("unexpected JSON delimiter %q", string(t))
		}
	case json.Number:
		f, err := strconv.ParseFloat(t.String(), 64)
		if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
			return nil, fmt.Errorf("non-finite or invalid number %q", t.String())
		}
		return t, nil
	case string, bool, nil:
		return t, nil
	default:
		return nil, fmt.Errorf("unexpected JSON token of type %T", tok)
	}
}

// decodeTyped decodes data into out with unknown-field rejection. The caller
// must have run decodeRawJSON first so duplicates, size and depth are
// already enforced.
func decodeTyped(data []byte, out any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("typed decode failed: %v", err)
	}
	return nil
}
