package llm

import (
	"encoding/json"
	"fmt"
)

// ToolInputJSONError describes syntax only. Model-generated arguments are not
// repaired: guessing at a slash can change a command, regex or saved document.
func ToolInputJSONError(input json.RawMessage) string {
	if len(input) == 0 || json.Valid(input) {
		return ""
	}
	var raw json.RawMessage
	if err := json.Unmarshal(input, &raw); err != nil {
		if syntax, ok := err.(*json.SyntaxError); ok {
			return fmt.Sprintf("%s (byte %d)", syntax.Error(), syntax.Offset)
		}
		return err.Error()
	}
	return "invalid JSON"
}

// MarshalJSON preserves invalid argument text without embedding invalid JSON
// in a message, event, snapshot or transcript. The original Block is untouched
// so execution still validates the exact bytes the model supplied.
func (b Block) MarshalJSON() ([]byte, error) {
	type storedBlock Block
	stored := storedBlock(b)
	if issue := ToolInputJSONError(b.Input); issue != "" {
		quoted, err := json.Marshal(string(b.Input))
		if err != nil {
			return nil, err
		}
		stored.Input = quoted
		stored.InputInvalid, stored.InputError = true, issue
	}
	return json.Marshal(stored)
}

// UnmarshalJSON accepts existing transcripts unchanged and restores argument
// bytes only when the new explicit failure marker accompanies a JSON string.
// A malformed call remains malformed after reload; it never becomes runnable
// merely because the transcript's surrounding JSON is now safe to serialize.
func (b *Block) UnmarshalJSON(data []byte) error {
	type storedBlock Block
	var stored storedBlock
	if err := json.Unmarshal(data, &stored); err != nil {
		return err
	}
	if stored.InputInvalid {
		var raw string
		if json.Unmarshal(stored.Input, &raw) == nil {
			stored.Input = json.RawMessage(raw)
		}
	}
	*b = Block(stored)
	return nil
}
