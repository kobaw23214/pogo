package tools

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	// These are trusted process-local inheritance facts, never tool arguments.
	envFileAccessRoot = "POGO_SUBAGENT_FILE_ACCESS_ROOT"
	envFileAccessFull = "POGO_SUBAGENT_FILE_ACCESS_FULL"
)

func fileAccessSnapshot(access *FileAccess, cwd string) (string, bool) {
	if access == nil || access.FullAccess == nil || !filepath.IsAbs(access.Root) {
		return "", false
	}
	root := canonical(access.Root)
	if filepath.Dir(root) == root || !within(canonical(cwd), root) {
		return "", false
	}
	return root, access.FullAccess()
}

func fileAccessChildEnv(env []string, root string, full bool) []string {
	out := make([]string, 0, len(env)+2)
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		if !strings.EqualFold(key, envFileAccessRoot) && !strings.EqualFold(key, envFileAccessFull) {
			out = append(out, entry)
		}
	}
	if root != "" {
		out = append(out, envFileAccessRoot+"="+root, envFileAccessFull+"="+strconv.FormatBool(full))
	}
	return out
}

func (g *FDGate) initFileAccess() {
	depth, _ := strconv.Atoi(os.Getenv(EnvSubagentDepth))
	root := os.Getenv(envFileAccessRoot)
	if depth < 1 || !filepath.IsAbs(root) || filepath.Dir(filepath.Clean(root)) == filepath.Clean(root) {
		return
	}
	g.fileAccess = &FileAccess{Root: filepath.Clean(root), FullAccess: g.fullAccess.Load}
	g.fullAccess.Store(os.Getenv(envFileAccessFull) == "true")
}

// FileAccess shares this particular child's latest parent verdict. There is no
// global mode: concurrent children of different sessions have separate channels.
func (g *FDGate) FileAccess() *FileAccess {
	return g.fileAccess
}

func (g *FDGate) updateFileAccess(verdict *gateVerdict) {
	if g.fileAccess != nil {
		g.fullAccess.Store(verdict != nil && verdict.Allow && verdict.FullAccess && verdict.FileAccessRoot == g.fileAccess.Root)
	}
}
