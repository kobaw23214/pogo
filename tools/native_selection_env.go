package tools

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

func nativeSelectionChildEnv(env []string, enabled *[]string) []string {
	out := make([]string, 0, len(env)+1)
	for _, item := range env {
		key, _, _ := strings.Cut(item, "=")
		if !strings.EqualFold(key, EnvSubagentNativeTools) {
			out = append(out, item)
		}
	}
	if enabled != nil {
		value := *enabled
		if value == nil {
			value = []string{}
		}
		raw, _ := json.Marshal(value)
		out = append(out, EnvSubagentNativeTools+"="+string(raw))
	}
	return out
}

// NativeSelectionFromEnvironment is consumed only by spawned children. Missing
// preserves legacy CLI behavior; malformed restrictions fail closed.
func NativeSelectionFromEnvironment() (*[]string, error) {
	raw, ok := os.LookupEnv(EnvSubagentNativeTools)
	if !ok {
		return nil, nil
	}
	var names []string
	if json.Unmarshal([]byte(raw), &names) != nil || names == nil {
		return nil, fmt.Errorf("invalid inherited native tool selection")
	}
	known := BuiltinNameSet()
	for _, name := range names {
		if !known[name] {
			return nil, fmt.Errorf("invalid inherited native tool %q", name)
		}
	}
	return &names, nil
}
