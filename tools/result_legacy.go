package tools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"

	"github.com/yosukeno/pogo/llm"
)

// LegacyResultPaths extracts host-persisted spill references, never paths from
// model-visible prose. New scopes may retain access to these exact old files
// without restoring the former shared-directory read grant.
func LegacyResultPaths(messages []llm.Message) []string {
	var paths []string
	for _, m := range messages {
		for _, b := range m.Content {
			if b.Type != llm.BlockToolResult || len(b.Details) == 0 {
				continue
			}
			var d struct {
				FullOutputPath string `json:"full_output_path"`
			}
			if json.Unmarshal(b.Details, &d) == nil && d.FullOutputPath != "" {
				paths = append(paths, d.FullOutputPath)
			}
		}
	}
	return paths
}

var legacyResultName = regexp.MustCompile(`^[A-Za-z0-9_-]+-[a-f0-9]{12}\.log$`)

func legacyResultReadRoots(base string, paths []string) []string {
	abs, err := filepath.Abs(base)
	if err != nil {
		return nil
	}
	var roots []string
	for _, p := range paths {
		path, err := filepath.Abs(p)
		if err != nil || filepath.Dir(path) != abs || !legacyResultName.MatchString(filepath.Base(path)) {
			continue
		}
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		// Do not let a historical path acquire a new target through a symlink.
		if canonical(path) == path {
			roots = append(roots, path)
		}
	}
	return CanonicalRoots(roots)
}
