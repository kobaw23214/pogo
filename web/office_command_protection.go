package web

// Managed document paths are host facts, not a pattern search in shell source.
// The private manifest is refreshed from registered Office documents and used
// when each ordinary command starts. It does not retroactively change an
// already-running process's mount namespace or affect a user's local terminal.

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/tools"
)

type officeCommandProtectionState struct {
	manifest     string
	paths        map[string]bool
	privateRoots []string
	blocked      bool
}

func (o *officeService) protectionStorageBase() string {
	if o.manager != nil && o.manager.cfg.SessionDir != "" {
		return o.manager.cfg.SessionDir
	}
	if o.batchDir != "" {
		return filepath.Dir(o.batchDir)
	}
	return ""
}

func (o *officeService) unavailableProtectionManifest(cwd string, owner *identity.Scope) string {
	encoded, _ := json.Marshal([]any{cwd, owner})
	base := o.protectionStorageBase()
	if !filepath.IsAbs(base) {
		base = filepath.Join(os.TempDir(), "pogo-document-protection-unavailable")
	}
	// This path is intentionally never written with a valid manifest. Returning
	// it together with an error keeps a non-error-returning session constructor
	// fail-closed instead of silently binding an empty protection setting.
	return filepath.Join(base, "office-command-protection", officeHash(encoded), "manifest.unavailable.json")
}

func officeProtectionWithin(root, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && !filepath.IsAbs(rel)
}

func (o *officeService) protectionStateLocked(cwd string, owner *identity.Scope) (*officeCommandProtectionState, error) {
	if !filepath.IsAbs(cwd) {
		return nil, officeFail(503, "DOCUMENT_PROTECTION_UNAVAILABLE", "文档工作区身份不可用。")
	}
	cwd = filepath.Clean(cwd)
	encoded, _ := json.Marshal([]any{cwd, owner})
	key := officeHash(encoded)
	if state := o.commandProtection[key]; state != nil {
		return state, nil
	}
	base := o.protectionStorageBase()
	if !filepath.IsAbs(base) {
		return nil, officeFail(503, "DOCUMENT_PROTECTION_UNAVAILABLE", "当前服务未配置私有文档保护目录。")
	}
	dir := filepath.Join(base, "office-command-protection", key)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	real, err := filepath.EvalSymlinks(dir)
	if err != nil || filepath.Clean(real) != dir {
		return nil, officeFail(503, "DOCUMENT_PROTECTION_UNAVAILABLE", "私有文档保护目录身份不正确。")
	}
	state := &officeCommandProtectionState{manifest: filepath.Join(dir, "manifest.json"), paths: map[string]bool{}, privateRoots: []string{filepath.Clean(base)}}
	f, err := os.Open(state.manifest)
	if err == nil {
		defer f.Close()
		info, statErr := f.Stat()
		if statErr != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
			return nil, officeFail(503, "DOCUMENT_PROTECTION_UNAVAILABLE", "文档保护记录不正确。")
		}
		var manifest tools.CommandProtectionManifest
		decoder := json.NewDecoder(io.LimitReader(f, (1<<20)+1))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&manifest) != nil || manifest.Schema != 1 || decoder.Decode(new(any)) != io.EOF {
			return nil, officeFail(503, "DOCUMENT_PROTECTION_UNAVAILABLE", "文档保护记录不正确。")
		}
		for _, path := range manifest.Paths {
			if !filepath.IsAbs(path) || !officeProtectionWithin(cwd, path) {
				return nil, officeFail(503, "DOCUMENT_PROTECTION_UNAVAILABLE", "文档保护记录与当前工作区不一致。")
			}
			state.paths[filepath.Clean(path)] = true
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err := saveOfficeCommandProtection(state); err != nil {
		return nil, err
	}
	if o.commandProtection == nil {
		o.commandProtection = map[string]*officeCommandProtectionState{}
	}
	o.commandProtection[key] = state
	return state, nil
}

func saveOfficeCommandProtection(state *officeCommandProtectionState) error {
	paths := make([]string, 0, len(state.paths))
	for path := range state.paths {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	data, err := json.Marshal(tools.CommandProtectionManifest{Schema: 1, Paths: paths, PrivateRoots: state.privateRoots})
	if err != nil {
		return err
	}
	if len(data) > 1<<20 {
		return officeFail(503, "DOCUMENT_PROTECTION_UNAVAILABLE", "当前文档保护登记已达到容量限制。")
	}
	f, err := os.CreateTemp(filepath.Dir(state.manifest), ".manifest-")
	if err != nil {
		return err
	}
	temp := f.Name()
	defer os.Remove(temp)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(temp, state.manifest); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(state.manifest))
	if err != nil {
		return err
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return err
	}
	state.blocked = false
	tools.RestoreCommandProtectionManifest(state.manifest)
	return nil
}

// commandProtectionManifest is called outside o.mu when binding a Web session.
// It merges only already-known Office registry entries, never a workspace walk.
func (o *officeService) commandProtectionManifest(cwd string, owner *identity.Scope) (string, error) {
	var active []string
	o.mu.Lock()
	for absolute, id := range o.active {
		if d := o.sessions[id]; d != nil && d.cwd == cwd && officeSameOwner(d.owner, owner) {
			active = append(active, absolute)
		}
	}
	o.mu.Unlock()
	o.commandProtectionMu.Lock()
	defer o.commandProtectionMu.Unlock()
	state, err := o.protectionStateLocked(cwd, owner)
	if err != nil {
		return o.unavailableProtectionManifest(cwd, owner), err
	}
	changed := false
	for _, path := range active {
		if !state.paths[path] {
			state.paths[path], changed = true, true
		}
	}
	if changed || state.blocked {
		if err := saveOfficeCommandProtection(state); err != nil {
			state.blocked = true
			_ = tools.InvalidateCommandProtectionManifest(state.manifest)
			return o.unavailableProtectionManifest(cwd, owner), err
		}
	}
	return state.manifest, nil
}

func (o *officeService) registerCommandProtectedDocument(l *officeLink, absolute string) error {
	if l == nil || l.service != o {
		return officeFail(403, "DOCUMENT_PROTECTION_UNAVAILABLE", "文档操作身份不正确。")
	}
	return o.registerCommandProtectedPath(l.cwd, l.owner, absolute)
}

// Safe to call while createDocumentLocked holds o.mu: this method takes only
// the separate protection mutex and never calls back into the Office registry.
func (o *officeService) registerCommandProtectedSession(d *officeDocumentSession) error {
	if d == nil {
		return officeFail(503, "DOCUMENT_PROTECTION_UNAVAILABLE", "文档身份不可用。")
	}
	return o.registerCommandProtectedPath(d.cwd, d.owner, d.absolute)
}

func (o *officeService) registerCommandProtectedPath(cwd string, owner *identity.Scope, absolute string) (registrationErr error) {
	defer func() {
		if registrationErr != nil {
			o.invalidateCommandProtection(cwd, owner)
		}
	}()
	absolute = filepath.Clean(absolute)
	if !filepath.IsAbs(absolute) || !officeProtectionWithin(cwd, absolute) {
		return officeFail(400, "PATH_REJECTED", "文档不在当前会话工作区。")
	}
	ext := strings.ToLower(filepath.Ext(absolute))
	if ext != ".docx" && ext != ".xlsx" {
		return nil
	}
	real, err := filepath.EvalSymlinks(absolute)
	if err != nil || filepath.Clean(real) != absolute {
		return officeFail(400, "PATH_REJECTED", "托管文档必须使用真实规范路径，不能通过符号链接登记。")
	}
	info, err := os.Lstat(absolute)
	if err != nil || !info.Mode().IsRegular() {
		return officeFail(400, "PATH_REJECTED", "只能登记存在的普通 Word 或 Excel 文件。")
	}
	o.commandProtectionMu.Lock()
	defer o.commandProtectionMu.Unlock()
	state, err := o.protectionStateLocked(cwd, owner)
	if err != nil {
		return err
	}
	if state.paths[absolute] && !state.blocked {
		return nil
	}
	state.paths[absolute] = true
	if err := saveOfficeCommandProtection(state); err != nil {
		// Retain the new authoritative path in memory. The old on-disk list
		// must not remain a usable permission snapshot after publication fails.
		state.blocked = true
		return err
	}
	return nil
}

func (o *officeService) invalidateCommandProtection(cwd string, owner *identity.Scope) {
	encoded, _ := json.Marshal([]any{filepath.Clean(cwd), owner})
	key := officeHash(encoded)
	o.commandProtectionMu.Lock()
	defer o.commandProtectionMu.Unlock()
	if state := o.commandProtection[key]; state != nil {
		state.blocked = true
		_ = tools.InvalidateCommandProtectionManifest(state.manifest)
		return
	}
	if base := o.protectionStorageBase(); filepath.IsAbs(base) {
		_ = tools.InvalidateCommandProtectionManifest(filepath.Join(base, "office-command-protection", key, "manifest.json"))
	}
}
