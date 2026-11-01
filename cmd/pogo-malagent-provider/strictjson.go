package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"unicode/utf8"
)

// Resource limits mirror the host's pogo.skill/v1 document profile so the
// provider rejects malformed frames with the same strictness the host applies.
const (
	maxFrameBytes = 1 << 20
	maxJSONDepth  = 32
)

// decodeStrict decodes exactly one JSON document into generic values and
// rejects duplicate object keys, excessive depth, non-finite numbers,
// non-UTF-8 bytes and trailing garbage — none of which encoding/json's
// default decoder rejects on its own.
func decodeStrict(data []byte) (any, error) {
	if len(data) == 0 {
		return nil, errors.New("empty document")
	}
	if len(data) > maxFrameBytes {
		return nil, fmt.Errorf("document too large: %d bytes > %d", len(data), maxFrameBytes)
	}
	if !utf8.Valid(data) {
		return nil, errors.New("document is not valid UTF-8")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	v, err := decodeStrictValue(dec, 1)
	if err != nil {
		return nil, err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("trailing data after top-level JSON value")
	}
	return v, nil
}

func decodeStrictValue(dec *json.Decoder, depth int) (any, error) {
	if depth > maxJSONDepth {
		return nil, fmt.Errorf("document deeper than %d levels", maxJSONDepth)
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
					return nil, errors.New("invalid JSON object key type")
				}
				if _, dup := obj[key]; dup {
					return nil, fmt.Errorf("duplicate JSON key %q", key)
				}
				val, err := decodeStrictValue(dec, depth+1)
				if err != nil {
					return nil, err
				}
				obj[key] = val
			}
			if _, err := dec.Token(); err != nil {
				return nil, fmt.Errorf("invalid JSON: %v", err)
			}
			return obj, nil
		case '[':
			arr := []any{}
			for dec.More() {
				val, err := decodeStrictValue(dec, depth+1)
				if err != nil {
					return nil, err
				}
				arr = append(arr, val)
			}
			if _, err := dec.Token(); err != nil {
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

// decodeTypedStrict first runs the strict generic pass (duplicate keys, depth,
// trailing data) and then decodes into out rejecting unknown fields. Numbers
// are already proven finite by the generic pass.
func decodeTypedStrict(data []byte, out any) error {
	if _, err := decodeStrict(data); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	// UseNumber keeps passthrough map values bit-exact when re-encoded.
	dec.UseNumber()
	dec.DisallowUnknownFields()
	if err := dec.Decode(out); err != nil {
		return fmt.Errorf("typed decode failed: %v", err)
	}
	return nil
}
