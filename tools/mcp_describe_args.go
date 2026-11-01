package tools

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Discovery has two meanings: an empty tool selects an index; a named tool
// selects detail. Silently dropping a misspelled optional selector changes the
// operation, so this helper deliberately opts out of generic lenient decoding.
func (d *MCPDescribe) ValidateArgs(raw json.RawMessage) error {
	_, err := d.decodeArgs(raw)
	return err
}

func (d *MCPDescribe) decodeArgs(raw json.RawMessage) (mcpDescribeArgs, error) {
	var args mcpDescribeArgs
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = json.RawMessage(`{}`)
	}
	if len(raw) > 16<<10 {
		return args, errors.New("mcp_describe arguments exceed 16 KiB; use a short filter or exact tool name")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	token, err := dec.Token()
	if err != nil || token != json.Delim('{') {
		return args, errors.New("arguments must be a JSON object; use {} for an index or {\"tool\":\"EXACT_NAME\"} for detail")
	}
	props := d.InputSchema()["properties"].(map[string]any)
	fields := map[string]json.RawMessage{}
	for dec.More() {
		token, err = dec.Token()
		if err != nil {
			return args, errors.New("invalid argument object")
		}
		key, ok := token.(string)
		if !ok {
			return args, errors.New("argument names must be strings")
		}
		if key == "tool_name" {
			return args, errors.New("unknown parameter \"tool_name\"; use \"tool\" for exact detail, or \"filter\" to search the index. No tool was inspected or activated")
		}
		if _, known := props[key]; !known {
			return args, fmt.Errorf("unknown parameter %q; expected: %s", key, describeFields(d.InputSchema()))
		}
		if _, duplicate := fields[key]; duplicate {
			return args, fmt.Errorf("duplicate parameter %q", key)
		}
		var value json.RawMessage
		if err := dec.Decode(&value); err != nil {
			return args, fmt.Errorf("invalid value for parameter %q", key)
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return args, fmt.Errorf("parameter %q cannot be null; omit unused optional parameters", key)
		}
		fields[key] = value
	}
	if token, err = dec.Token(); err != nil || token != json.Delim('}') {
		return args, errors.New("incomplete argument object")
	}
	if _, err = dec.Token(); err != io.EOF {
		return args, errors.New("arguments must contain exactly one JSON object")
	}
	// No fallback: unknown fields and duplicate selectors have already failed.
	if err := json.Unmarshal(raw, &args); err != nil {
		return args, fmt.Errorf("incorrect parameter type; expected: %s", describeFields(d.InputSchema()))
	}
	if _, supplied := fields["tool"]; supplied && strings.TrimSpace(args.Tool) == "" {
		return args, errors.New("tool must be a non-empty exact name; omit tool to list or search the index")
	}
	// "index" is the default answer shape's own name: a caller spelling it out
	// is not guessing wrong. The strictness this decoder exists for targets
	// lost selectors (tool_name), not synonyms of the default; anything else
	// still fails so the message can correct a real wrong guess.
	if args.Mode != "" && args.Mode != "index" && args.Mode != "groups" {
		return args, errors.New(`mode must be "index" or "groups", or omitted for the default index; use tool for detail, filter to narrow the index`)
	}
	if args.Tool != "" {
		for _, key := range []string{"filter", "group", "role", "mode", "offset", "limit"} {
			if _, supplied := fields[key]; supplied {
				return args, fmt.Errorf("parameter %q only applies to index/groups, not tool detail; detail accepts tool and optional server", key)
			}
		}
	}
	if args.Mode == "groups" && len(fields) != 1 {
		return args, errors.New("mode groups returns the complete group overview; omit other parameters or omit mode to filter the index")
	}
	return args, nil
}

var _ ArgsValidator = (*MCPDescribe)(nil)
