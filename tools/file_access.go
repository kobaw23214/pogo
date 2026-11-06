package tools

import (
	"errors"
	"path/filepath"
)

// FileAccess is trusted host state for the session's ordinary file tools.
// Root is the authenticated owner's workspace, not a model-supplied path.
// FullAccess is consulted once per tool execution, so existing tools follow
// policy changes without replacing their objects or changing relative paths.
type FileAccess struct {
	Root       string
	FullAccess func() bool
}

func fileAccessRoots(cwd string, roots []string, access *FileAccess) ([]string, error) {
	if access == nil || access.FullAccess == nil || !access.FullAccess() {
		return roots, nil
	}
	if access.Root == "" || !filepath.IsAbs(access.Root) {
		return nil, errors.New("full access requires an absolute account workspace")
	}
	root := canonical(access.Root)
	if filepath.Dir(root) == root || !within(canonical(cwd), root) {
		return nil, errors.New("full access workspace must contain this conversation and cannot be the filesystem root")
	}
	// Never append into a shared Roots backing array: the same session can run
	// parallel reads while another request changes its approval policy.
	return append(append([]string(nil), roots...), root), nil
}
