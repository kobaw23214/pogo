package web

// These operations are explicit user actions, not model filesystem tools. They
// operate through a descriptor-root and never follow a symlink selected in the
// tree. The same hierarchical mutation queue fences host and agent saves.
import (
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/yosukeno/pogo/tools"
)

type fileMutationRequest struct {
	Path      string `json:"path"`
	Name      string `json:"name,omitempty"`
	Recursive bool   `json:"recursive,omitempty"`
	MtimeMS   int64  `json:"mtime_ms"`
	Size      int64  `json:"size"`
}

// checkedPanelMutationPath deliberately does NOT Resolve the final target:
// Resolve follows links and would turn "delete link" into "delete its target".
func (s *Server) checkedPanelMutationPath(r *http.Request, path string) (root, rel, absolute string, err error) {
	root, err = tools.Resolve(s.requestRoot(r), ".")
	if err != nil {
		return
	}
	path = s.panelRelativePath(r, path)
	if filepath.IsAbs(path) {
		rel, err = filepath.Rel(root, path)
	} else {
		rel = filepath.Clean(path)
	}
	if err != nil || path == "" || rel == "." || rel == ".." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || strings.ContainsRune(rel, 0) {
		err = officeFail(400, "PATH_REJECTED", "不能操作工作区根目录或工作区外的路径。")
		return
	}
	absolute = filepath.Join(root, rel)
	// A CLI deployment may place its private session directory inside cwd.
	// Library assets, editor journals and session metadata remain owned by
	// their APIs even if a file-tree listing happens to expose that ancestor.
	if s.mgr.cfg.SessionDir != "" {
		private, e := filepath.Abs(s.mgr.cfg.SessionDir)
		if e == nil {
			if resolved, e := filepath.EvalSymlinks(private); e == nil {
				private = resolved
			}
			within := func(parent, child string) bool {
				rel, e := filepath.Rel(parent, child)
				return e == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)) && !filepath.IsAbs(rel)
			}
			if within(private, absolute) || within(absolute, private) {
				err = officeFail(403, "PRIVATE_STORAGE_PROTECTED", "这是应用管理的资料和会话存储，请在对应的信息库或设置页面中操作。")
				return
			}
		}
	}
	return
}

func panelMutationSource(root *os.Root, rel string) (os.FileInfo, error) {
	parts := strings.Split(rel, string(os.PathSeparator))
	for i := range parts {
		info, err := root.Lstat(filepath.Join(parts[:i+1]...))
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, officeFail(400, "SYMLINK_REJECTED", "此操作不支持符号链接，请选择工作区内的实际文件或文件夹。")
		}
		if i == len(parts)-1 {
			if !info.IsDir() && !info.Mode().IsRegular() {
				return nil, officeFail(400, "FILE_TYPE_REJECTED", "只能操作普通文件或文件夹。")
			}
			return info, nil
		}
		if !info.IsDir() {
			return nil, fs.ErrNotExist
		}
	}
	return nil, fs.ErrInvalid
}

// A spelling check alone is not a storage boundary on case-insensitive volumes.
// Run this after descriptor-root validation, comparing actual directories both
// ways: the selected entry may contain SessionDir, or may be inside it under a
// differently cased name. No recursive private-file scan or content read is
// needed, and none of the user's paths are followed outside the checked root.
func (s *Server) panelPrivateStorageIdentityAllowed(root *os.Root, rel string, source os.FileInfo) error {
	if s.mgr.cfg.SessionDir == "" {
		return nil
	}
	private, err := filepath.Abs(s.mgr.cfg.SessionDir)
	if err != nil {
		return officeFail(403, "PRIVATE_STORAGE_PROTECTED", "资料和会话存储身份尚未确认，暂不能操作此项目。")
	}
	protected := func() error {
		return officeFail(403, "PRIVATE_STORAGE_PROTECTED", "这是应用管理的资料和会话存储，请在对应的信息库或设置页面中操作。")
	}
	// Covers a selected ancestor of the private root even if its final
	// descendants have not been created yet.
	for current := private; ; current = filepath.Dir(current) {
		if info, e := os.Stat(current); e == nil && os.SameFile(source, info) {
			return protected()
		}
		if filepath.Dir(current) == current {
			break
		}
	}
	privateInfo, err := os.Stat(private)
	if err != nil {
		return nil
	}
	for current := rel; ; current = filepath.Dir(current) {
		info, e := root.Lstat(current)
		if e != nil {
			return officeFail(409, "FILE_CHANGED", "文件身份已变化，未执行操作。")
		}
		if os.SameFile(privateInfo, info) {
			return protected()
		}
		if current == "." {
			break
		}
	}
	return nil
}

func panelMutationError(w http.ResponseWriter, err error) {
	var typed *officeError
	switch {
	case errors.As(err, &typed):
		writeJSON(w, typed.status, map[string]any{"error": typed.message, "code": typed.code})
	case errors.Is(err, fs.ErrNotExist):
		writeJSON(w, 404, map[string]any{"error": "文件或文件夹已不存在，请刷新后重试。", "code": "FILE_NOT_FOUND"})
	case errors.Is(err, fs.ErrExist):
		writeJSON(w, 409, map[string]any{"error": "此文件夹中已有同名项目，请使用其他名称。", "code": "NAME_EXISTS"})
	default:
		writeJSON(w, 500, map[string]any{"error": "文件操作失败，请刷新后检查当前状态。", "code": "FILE_OPERATION_FAILED"})
	}
}

func (s *Server) handleFileRename(w http.ResponseWriter, r *http.Request) {
	s.handleFileMutation(w, r, true)
}
func (s *Server) handleFileDelete(w http.ResponseWriter, r *http.Request) {
	s.handleFileMutation(w, r, false)
}

func (s *Server) handleFileMutation(w http.ResponseWriter, r *http.Request, rename bool) {
	var body fileMutationRequest
	if err := decodeBody(r, &body); err != nil {
		writeErr(w, 400, err)
		return
	}
	if rename && (strings.TrimSpace(body.Name) == "" || body.Name == "." || body.Name == ".." || len(body.Name) > 255 || strings.ContainsAny(body.Name, "/\\\x00") || strings.ContainsAny(body.Name, "\r\n")) {
		panelMutationError(w, officeFail(400, "INVALID_NAME", "请输入一个有效名称，不能包含路径分隔符。"))
		return
	}
	rootPath, rel, absolute, err := s.checkedPanelMutationPath(r, body.Path)
	if err != nil {
		panelMutationError(w, err)
		return
	}
	var directory bool
	newPanelPath := ""
	err = tools.WithFileMutationLock(filepath.Dir(absolute), func() error {
		// Lock order is path -> Office registry -> document, matching export and
		// takeover. Hold the registry through publication so an editor cannot
		// open a child between the occupancy check and the directory mutation.
		if s.office != nil {
			s.office.mu.Lock()
			defer s.office.mu.Unlock()
		}
		root, e := os.OpenRoot(rootPath)
		if e != nil {
			return e
		}
		defer root.Close()
		info, e := panelMutationSource(root, rel)
		if e != nil {
			return e
		}
		if e := s.panelPrivateStorageIdentityAllowed(root, rel, info); e != nil {
			return e
		}
		if s.office != nil {
			if e := s.office.panelMutationAllowedLocked(absolute, info); e != nil {
				return e
			}
		}
		if info.ModTime().UnixMilli() != body.MtimeMS || info.Size() != body.Size {
			return officeFail(409, "FILE_CHANGED", "此项目在你打开菜单后发生了变化，请刷新后重试。")
		}
		directory = info.IsDir()
		if !rename && directory && !body.Recursive {
			return officeFail(400, "RECURSIVE_CONFIRMATION_REQUIRED", "删除文件夹会删除其中所有内容，请确认后重试。")
		}
		parent, e := root.OpenRoot(filepath.Dir(rel))
		if e != nil {
			return e
		}
		defer parent.Close()
		// Recheck the exact inode through the pinned parent descriptor.
		current, e := parent.Lstat(filepath.Base(rel))
		if e != nil {
			return e
		}
		if !os.SameFile(info, current) {
			return officeFail(409, "FILE_CHANGED", "文件身份已变化，未执行操作。")
		}
		if rename {
			if body.Name == filepath.Base(rel) {
				return officeFail(400, "SAME_NAME", "新名称与原名称相同。")
			}
			dir, e := parent.Open(".")
			if e != nil {
				return e
			}
			defer dir.Close()
			if e = panelRenameNoReplace(dir, filepath.Base(rel), body.Name); e != nil {
				return e
			}
			_ = dir.Sync()
			newPanelPath = filepath.ToSlash(filepath.Join(filepath.Dir(body.Path), body.Name))
		} else {
			// Root.RemoveAll does not follow symlinks introduced inside a tree;
			// it also pins traversal beneath this parent if another process races.
			if directory {
				e = parent.RemoveAll(filepath.Base(rel))
			} else {
				e = parent.Remove(filepath.Base(rel))
			}
			if e != nil {
				return e
			}
			if dir, e := parent.Open("."); e == nil {
				_ = dir.Sync()
				dir.Close()
			}
		}
		return nil
	})
	if err != nil {
		panelMutationError(w, err)
		return
	}
	kind := "delete"
	if rename {
		kind = "rename"
	}
	writeJSON(w, 200, map[string]any{"ok": true, "kind": kind, "path": filepath.ToSlash(body.Path), "new_path": newPanelPath, "is_directory": directory})
}

func (o *officeService) panelMutationAllowedLocked(absolute string, source os.FileInfo) error {
	if o.closed || o.editorJournalError {
		return officeFail(409, "OFFICE_STATE_UNCONFIRMED", "Office 编辑状态尚未确认，请先处理文档占用再操作文件。")
	}
	within := func(path string) bool {
		if path == absolute || strings.HasPrefix(path, absolute+string(os.PathSeparator)) {
			return true
		}
		// Text paths are insufficient on a case-insensitive filesystem or when
		// another in-workspace name is a hard link to an occupied document.
		// Compare the actual inode and, for directories, each trusted ancestor.
		for current := path; ; current = filepath.Dir(current) {
			if info, err := os.Stat(current); err == nil && os.SameFile(source, info) {
				return true
			}
			if !source.IsDir() || filepath.Dir(current) == current {
				return false
			}
		}
	}
	for path := range o.batchLeases {
		if within(path) {
			return officeFail(409, "OFFICE_FILE_IN_USE", "此文件或文件夹中的文档正在批处理，请完成后再重命名或删除。")
		}
	}
	for path, id := range o.active {
		if !within(path) {
			continue
		}
		d := o.sessions[id]
		if d == nil {
			return officeFail(409, "OFFICE_STATE_UNCONFIRMED", "此文档的编辑占用尚未确认，请先在文档页面处理占用。")
		}
		d.mu.Lock()
		busy := !d.closed || d.dirtyEpoch > d.persistedEpoch || d.blocked || d.saving || d.exportBusy || d.batchLease != ""
		d.mu.Unlock()
		if busy {
			return officeFail(409, "OFFICE_FILE_IN_USE", "此文件或文件夹中的文档仍有编辑占用，请先保存并关闭；失联占用请在文档页面处理后重试。")
		}
	}
	return nil
}
