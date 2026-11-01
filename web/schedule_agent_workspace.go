package web

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/yosukeno/pogo/identity"
)

// scheduleWorkspace resolves only the server's own canonical spelling and the
// trusted owner suffix. User-controlled workspace/ancestor symlinks are never
// treated as a new account root, including before a default directory exists.
func (m *Manager) scheduleWorkspace(owner *identity.Scope, target string, allowMissing bool) (base, absolute string, err error) {
	server, err := filepath.EvalSymlinks(m.Cwd())
	if err != nil {
		return "", "", err
	}
	base = server
	if owner != nil {
		if !owner.Valid() {
			return "", "", errors.New("任务账号无效")
		}
		suffix, ok := m.UserRootRel(*owner)
		if !ok {
			return "", "", errors.New("任务账号目录无效")
		}
		base = filepath.Join(server, filepath.FromSlash(suffix))
	}
	if target == "" {
		target = base
	}
	// Existing sessions store the original server path spelling (e.g. macOS
	// /var versus /private/var). Translate that exact trusted prefix only.
	if rel, e := filepath.Rel(m.Cwd(), target); e == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		target = filepath.Join(server, rel)
	}
	rel, err := filepath.Rel(base, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", "", errors.New("任务工作区不属于该账号")
	}
	absolute = filepath.Join(base, rel)
	all, err := filepath.Rel(server, absolute)
	if err != nil {
		return "", "", err
	}
	root, err := os.OpenRoot(server)
	if err != nil {
		return "", "", err
	}
	defer root.Close()
	current := ""
	for _, component := range strings.Split(all, string(filepath.Separator)) {
		if component == "." || component == "" {
			continue
		}
		current = filepath.Join(current, component)
		st, e := root.Lstat(current)
		if os.IsNotExist(e) && allowMissing {
			return base, absolute, nil
		}
		if e != nil {
			return "", "", e
		}
		if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			return "", "", errors.New("任务工作区或父目录含符号链接，无法安全使用")
		}
	}
	return base, absolute, nil
}
