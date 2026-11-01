package web

import (
	"errors"
	"io"
	"os"
	"path/filepath"

	af "github.com/yosukeno/pogo/agentworkflow"
	"github.com/yosukeno/pogo/identity"
)

// Resolve from the authenticated user's root, never by opening a mutable
// workspace path as a fresh root. os.Root prevents ancestor-symlink escapes.
func (m *Manager) awRoot(owner *identity.Scope) (*os.Root, error) {
	base := m.cfg.Cwd
	if owner != nil {
		base = m.UserRoot(*owner)
	}
	return os.OpenRoot(base)
}
func (m *Manager) awRelative(owner *identity.Scope, absolute string) (string, error) {
	base := m.cfg.Cwd
	if owner != nil {
		base = m.UserRoot(*owner)
	}
	rel, e := filepath.Rel(base, absolute)
	rel = filepath.ToSlash(rel)
	if e != nil || !af.SafePath(rel) {
		return "", errors.New("工作流文件不属于当前用户目录")
	}
	return rel, nil
}
func awReadRoot(root *os.Root, path string, limit int64) ([]byte, error) {
	if !af.SafePath(filepath.ToSlash(path)) {
		return nil, errors.New("文件路径无效")
	}
	f, e := root.Open(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	st, e := f.Stat()
	if e != nil || !st.Mode().IsRegular() || st.Size() > limit {
		return nil, errors.New("文件须为大小受限的普通文件")
	}
	b, e := io.ReadAll(io.LimitReader(f, limit+1))
	if int64(len(b)) > limit {
		return nil, errors.New("文件过大")
	}
	return b, e
}
func (m *Manager) awReadOwned(owner *identity.Scope, absolute string, limit int64) ([]byte, error) {
	rel, e := m.awRelative(owner, absolute)
	if e != nil {
		return nil, e
	}
	root, e := m.awRoot(owner)
	if e != nil {
		return nil, e
	}
	defer root.Close()
	return awReadRoot(root, rel, limit)
}
func (m *Manager) awWriteOwned(owner *identity.Scope, absolute string, data []byte) error {
	rel, e := m.awRelative(owner, absolute)
	if e != nil {
		return e
	}
	root, e := m.awRoot(owner)
	if e != nil {
		return e
	}
	defer root.Close()
	return awWriteRoot(root, rel, data)
}
func awWriteRoot(root *os.Root, rel string, data []byte) error {
	if !af.SafePath(filepath.ToSlash(rel)) {
		return errors.New("写入路径无效")
	}
	if e := root.MkdirAll(filepath.Dir(rel), 0700); e != nil {
		return e
	}
	f, e := root.OpenFile(rel, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	if _, e = f.Write(data); e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e != nil {
		return e
	}
	return closeErr
}
func (m *Manager) awRemoveWorkspace(owner *identity.Scope, absolute string) {
	rel, e := m.awRelative(owner, absolute)
	if e != nil {
		return
	}
	root, e := m.awRoot(owner)
	if e == nil {
		defer root.Close()
		_ = root.RemoveAll(rel)
	}
}
func (m *Manager) awArtifactPath(rid, step string) string {
	return filepath.Join("agent-workflow-artifacts", rid, step)
}
func (m *Manager) awSaveArtifact(rid, step string, data []byte) error {
	if !validID(rid) || !af.ValidID(step) {
		return errors.New("产物身份无效")
	}
	root, e := os.OpenRoot(m.cfg.SessionDir)
	if e != nil {
		return e
	}
	defer root.Close()
	return awWriteRoot(root, m.awArtifactPath(rid, step), data)
}
func (m *Manager) awReadArtifact(rid, step string) ([]byte, error) {
	if !validID(rid) || !af.ValidID(step) {
		return nil, errors.New("产物身份无效")
	}
	root, e := os.OpenRoot(m.cfg.SessionDir)
	if e != nil {
		return nil, e
	}
	defer root.Close()
	return awReadRoot(root, m.awArtifactPath(rid, step), 2<<20)
}
