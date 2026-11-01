package web

import (
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/information"
	"github.com/yosukeno/pogo/inputref"
	"github.com/yosukeno/pogo/tools"
)

// InformationStore derives authority only from authenticated owner and the
// manager's trusted workspace. Agent IDs and caller filesystem paths have no
// part in this namespace: sibling and delegated Agents share the same view.
func (m *Manager) InformationStore(owner *identity.Scope) (*information.Store, error) {
	workspace := m.cfg.Cwd
	if owner != nil {
		if !owner.Valid() {
			return nil, information.ErrInvalid
		}
		workspace = m.UserRoot(*owner)
	}
	if m.Isolation() && owner == nil {
		return nil, information.ErrInvalid
	}
	scope, err := information.Namespace(owner, workspace)
	if err != nil {
		return nil, err
	}
	return information.Open(filepath.Join(m.cfg.SessionDir, "information", scope), scope)
}
func (s *Server) informationStore(w http.ResponseWriter, r *http.Request) (*information.Store, *identity.Scope, bool) {
	w.Header().Set("Cache-Control", "no-store")
	var owner *identity.Scope
	if scope, ok := s.ownerScope(r); ok {
		owner = &scope
	} else if s.multiuser() {
		writeErr(w, 401, errors.New("请先登录"))
		return nil, nil, false
	}
	store, err := s.mgr.InformationStore(owner)
	if err != nil {
		writeErr(w, 500, errors.New("信息库不可用"))
		return nil, nil, false
	}
	return store, owner, true
}
func (s *Server) informationRoutes() {
	s.mux.HandleFunc("GET /api/information", s.handleInformationList)
	s.mux.HandleFunc("GET /api/information/office-views", s.handleInformationOfficeViews)
	s.mux.HandleFunc("GET /api/information/entries/{id}", s.handleInformationGet)
	s.mux.HandleFunc("POST /api/information/entries", s.handleInformationCreate)
	s.mux.HandleFunc("PATCH /api/information/entries/{id}", s.handleInformationUpdate)
	s.mux.HandleFunc("DELETE /api/information/entries/{id}", s.handleInformationDelete)
	s.mux.HandleFunc("POST /api/information/files", s.handleInformationFile)
	s.mux.HandleFunc("POST /api/information/assets", s.handleInformationAsset)
	s.mux.HandleFunc("GET /api/information/entries/{id}/content", s.handleInformationContent)
	s.mux.HandleFunc("GET /api/information/entries/{id}/office-preview", s.handleInformationOfficePreview)
	s.mux.HandleFunc("POST /api/information/entries/{id}/office-view", s.handleInformationOfficeView)
	s.mux.HandleFunc("GET /api/information/entries/{id}/assets/content", s.handleInformationNestedContent)
	s.mux.HandleFunc("POST /api/information/import-shortcut", s.handleInformationImportShortcut)
	s.mux.HandleFunc("POST /api/sessions/{sid}/information/resolve", s.handleInformationResolve)
}
func informationError(w http.ResponseWriter, store *information.Store, err error) {
	status, code, msg := 400, "INFORMATION_INVALID", "信息库资料无效，请检查名称、内容及目录"
	var maxBody *http.MaxBytesError
	switch {
	case errors.Is(err, information.ErrTooLarge), errors.As(err, &maxBody):
		status, code, msg = 413, "INFORMATION_TOO_LARGE", "文件过大，信息库单个文件最多支持 512 MiB"
	case errors.Is(err, information.ErrConflict):
		status, code, msg = 409, "INFORMATION_CONFLICT", "信息库已在其他窗口更新，请重新加载后保存"
	case errors.Is(err, information.ErrNameConflict):
		status, code, msg = 409, "INFORMATION_NAME_CONFLICT", "当前文件夹已存在同名条目"
	case errors.Is(err, information.ErrNotFound), errors.Is(err, os.ErrNotExist):
		status, code, msg = 404, "INFORMATION_NOT_FOUND", "资料不存在或不可访问"
	case errors.Is(err, information.ErrNotEmpty):
		status, code, msg = 409, "INFORMATION_NOT_EMPTY", "文件夹非空，请确认是否删除全部内容"
	}
	body := map[string]any{"code": code, "error": msg}
	if status == 409 {
		if v, e := store.List(); e == nil {
			body["current"] = v
		}
	}
	writeJSON(w, status, body)
}
func informationBody(w http.ResponseWriter, r *http.Request, v any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 2<<20))
	d.DisallowUnknownFields()
	var extra any
	if d.Decode(v) != nil || d.Decode(&extra) != io.EOF {
		writeErr(w, 400, errors.New("信息库请求格式无效"))
		return false
	}
	return true
}
func informationMutation(w http.ResponseWriter, v information.TreeView, e *information.Entry, status int) {
	writeJSON(w, status, struct {
		information.TreeView
		Entry *information.Entry `json:"entry,omitempty"`
	}{v, e})
}
func (s *Server) handleInformationList(w http.ResponseWriter, r *http.Request) {
	store, _, ok := s.informationStore(w, r)
	if !ok {
		return
	}
	v, err := store.List()
	if err != nil {
		informationError(w, store, err)
		return
	}
	writeJSON(w, 200, v)
}
func informationVersion(r *http.Request) (int64, error) {
	if len(r.URL.Query()["version"]) > 1 {
		return 0, information.ErrInvalid
	}
	raw := r.URL.Query().Get("version")
	if raw == "" {
		return 0, nil
	}
	n, e := strconv.ParseInt(raw, 10, 64)
	if e != nil || n < 1 || n > information.MaxRevision {
		return 0, information.ErrInvalid
	}
	return n, nil
}
func (s *Server) handleInformationGet(w http.ResponseWriter, r *http.Request) {
	store, _, ok := s.informationStore(w, r)
	if !ok {
		return
	}
	v, err := informationVersion(r)
	if err != nil {
		informationError(w, store, err)
		return
	}
	entry, err := store.Get(r.PathValue("id"), v)
	if err != nil {
		informationError(w, store, err)
		return
	}
	writeJSON(w, 200, entry)
}
func (s *Server) handleInformationCreate(w http.ResponseWriter, r *http.Request) {
	store, _, ok := s.informationStore(w, r)
	if !ok {
		return
	}
	var input struct {
		Revision *int64                `json:"revision"`
		ParentID string                `json:"parent_id"`
		Kind     string                `json:"kind"`
		Name     string                `json:"name"`
		Shortcut *information.Shortcut `json:"shortcut,omitempty"`
	}
	if !informationBody(w, r, &input) {
		return
	}
	if input.Revision == nil || input.Kind == "file" {
		informationError(w, store, information.ErrInvalid)
		return
	}
	v, e, err := store.Create(*input.Revision, information.CreateInput{ParentID: input.ParentID, Kind: input.Kind, Name: input.Name, Shortcut: input.Shortcut})
	if err != nil {
		informationError(w, store, err)
		return
	}
	informationMutation(w, v, e, 201)
}
func (s *Server) handleInformationUpdate(w http.ResponseWriter, r *http.Request) {
	store, _, ok := s.informationStore(w, r)
	if !ok {
		return
	}
	var input struct {
		Revision *int64                `json:"revision"`
		Name     *string               `json:"name,omitempty"`
		ParentID *string               `json:"parent_id,omitempty"`
		Shortcut *information.Shortcut `json:"shortcut,omitempty"`
	}
	if !informationBody(w, r, &input) {
		return
	}
	if input.Revision == nil || input.Name == nil && input.ParentID == nil && input.Shortcut == nil {
		informationError(w, store, information.ErrInvalid)
		return
	}
	v, e, err := store.Update(*input.Revision, r.PathValue("id"), information.UpdateInput{Name: input.Name, ParentID: input.ParentID, Shortcut: input.Shortcut})
	if err != nil {
		informationError(w, store, err)
		return
	}
	informationMutation(w, v, e, 200)
}
func (s *Server) handleInformationDelete(w http.ResponseWriter, r *http.Request) {
	store, _, ok := s.informationStore(w, r)
	if !ok {
		return
	}
	revision, err := strconv.ParseInt(r.URL.Query().Get("revision"), 10, 64)
	if err != nil || len(r.URL.Query()["revision"]) != 1 || len(r.URL.Query()["recursive"]) > 1 {
		informationError(w, store, information.ErrInvalid)
		return
	}
	recursive := r.URL.Query().Get("recursive")
	if recursive != "" && recursive != "true" && recursive != "false" {
		informationError(w, store, information.ErrInvalid)
		return
	}
	v, e, err := store.Delete(revision, r.PathValue("id"), recursive == "true")
	if err != nil {
		informationError(w, store, err)
		return
	}
	informationMutation(w, v, e, 200)
}
func informationMultipart(w http.ResponseWriter, r *http.Request, store *information.Store) (information.Asset, string, int64, error) {
	r.Body = http.MaxBytesReader(w, r.Body, information.MaxAssetBytes+(1<<20))
	reader, err := r.MultipartReader()
	if err != nil {
		return information.Asset{}, "", 0, information.ErrInvalid
	}
	var asset information.Asset
	parent := ""
	var revision int64
	seen := map[string]bool{}
	for count := 0; ; count++ {
		p, e := reader.NextPart()
		if e == io.EOF {
			break
		}
		if e != nil || count >= 8 {
			return information.Asset{}, "", 0, information.ErrInvalid
		}
		name := p.FormName()
		if seen[name] {
			p.Close()
			return information.Asset{}, "", 0, information.ErrInvalid
		}
		seen[name] = true
		switch name {
		case "file":
			if p.FileName() == "" {
				p.Close()
				return information.Asset{}, "", 0, information.ErrInvalid
			}
			asset, err = store.PutAsset(p.FileName(), p.Header.Get("Content-Type"), p, information.MaxAssetBytes)
		case "parent_id", "revision":
			b, e := io.ReadAll(io.LimitReader(p, 2049))
			if e != nil || len(b) > 2048 {
				p.Close()
				return information.Asset{}, "", 0, information.ErrInvalid
			}
			if name == "parent_id" {
				parent = string(b)
			} else {
				revision, err = strconv.ParseInt(string(b), 10, 64)
			}
		default:
			err = information.ErrInvalid
		}
		p.Close()
		if err != nil {
			return information.Asset{}, "", 0, err
		}
	}
	if !seen["file"] {
		return information.Asset{}, "", 0, information.ErrInvalid
	}
	if r.URL.Path == "/api/information/files" && !seen["revision"] {
		return information.Asset{}, "", 0, information.ErrInvalid
	}
	return asset, parent, revision, nil
}
func informationAttachment(asset information.Asset, p string) AttachmentMeta {
	ext := strings.ToLower(path.Ext(asset.Name))
	kind := fileKindByExt(ext)
	if strings.HasPrefix(asset.Mime, "image/") {
		kind = "image"
	}
	return AttachmentMeta{Path: p, Name: asset.Name, Mime: asset.Mime, Size: asset.Size, Kind: kind, Created: time.Now().UTC()}
}
func (s *Server) handleInformationFile(w http.ResponseWriter, r *http.Request) {
	store, _, ok := s.informationStore(w, r)
	if !ok {
		return
	}
	a, parent, revision, err := informationMultipart(w, r, store)
	if err != nil {
		informationError(w, store, err)
		return
	}
	v, e, err := store.Create(revision, information.CreateInput{ParentID: parent, Kind: "file", Name: a.Name, File: &a})
	if err != nil {
		informationError(w, store, err)
		return
	}
	informationMutation(w, v, e, 201)
}
func (s *Server) handleInformationAsset(w http.ResponseWriter, r *http.Request) {
	store, _, ok := s.informationStore(w, r)
	if !ok {
		return
	}
	a, _, _, err := informationMultipart(w, r, store)
	if err != nil {
		informationError(w, store, err)
		return
	}
	writeJSON(w, 201, map[string]any{"part": inputref.Part{Type: "file", Path: a.Asset, AsFile: true}, "attachment": informationAttachment(a, a.Asset)})
}
func (s *Server) handleInformationContent(w http.ResponseWriter, r *http.Request) {
	store, _, ok := s.informationStore(w, r)
	if !ok {
		return
	}
	v, err := informationVersion(r)
	if err != nil {
		informationError(w, store, err)
		return
	}
	e, err := store.Get(r.PathValue("id"), v)
	if err != nil {
		informationError(w, store, err)
		return
	}
	if e.File == nil {
		informationError(w, store, information.ErrInvalid)
		return
	}
	f, a, err := store.AssetRead(e.File.Asset)
	if err != nil {
		informationError(w, store, err)
		return
	}
	defer f.Close()
	serveInformationAsset(w, r, e, a, f)
}
func (s *Server) handleInformationNestedContent(w http.ResponseWriter, r *http.Request) {
	store, _, ok := s.informationStore(w, r)
	if !ok {
		return
	}
	version, err := informationVersion(r)
	handle := r.URL.Query().Get("asset")
	if err != nil || version < 1 || len(r.URL.Query()["asset"]) != 1 || handle == "" {
		informationError(w, store, information.ErrInvalid)
		return
	}
	entry, err := store.Get(r.PathValue("id"), version)
	if err != nil {
		informationError(w, store, err)
		return
	}
	member := entry.File != nil && entry.File.Asset == handle
	if entry.Shortcut != nil {
		for _, part := range inputref.Flatten(entry.Shortcut.Parts) {
			if part.Type == "file" && part.Path == handle {
				member = true
				break
			}
		}
	}
	if !member {
		informationError(w, store, information.ErrNotFound)
		return
	}
	f, a, err := store.AssetRead(handle)
	if err != nil {
		informationError(w, store, err)
		return
	}
	defer f.Close()
	serveInformationAsset(w, r, entry, a, f)
}
func serveInformationAsset(w http.ResponseWriter, r *http.Request, e information.Entry, a information.Asset, f *os.File) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox; default-src 'none'; style-src 'unsafe-inline'")
	disposition := "attachment"
	safeInline := strings.HasPrefix(a.Mime, "image/") && !strings.Contains(a.Mime, "svg") || a.Mime == "application/pdf" || strings.HasPrefix(a.Mime, "text/plain")
	if r.URL.Query().Get("download") != "1" && safeInline {
		disposition = "inline"
	}
	w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": a.Name}))
	w.Header().Set("Content-Type", a.Mime)
	http.ServeContent(w, r, a.Name, e.UpdatedAt, f)
}
func (s *Server) handleInformationImportShortcut(w http.ResponseWriter, r *http.Request) {
	store, owner, ok := s.informationStore(w, r)
	if !ok {
		return
	}
	var in struct {
		Revision      *int64 `json:"revision"`
		ParentID      string `json:"parent_id"`
		SourceAgentID string `json:"source_agent_id"`
		ShortcutID    string `json:"shortcut_id"`
	}
	if !informationBody(w, r, &in) {
		return
	}
	if in.Revision == nil {
		informationError(w, store, information.ErrInvalid)
		return
	}
	if in.SourceAgentID != "" {
		a, visible := s.mgr.studio.get(in.SourceAgentID)
		if !validAgentDirName(in.SourceAgentID) || !visible || a.Status != "ready" && !s.adminRequest(r) {
			writeErr(w, 404, errors.New("Agent 不存在或不可访问"))
			return
		}
	}
	view, err := s.mgr.userShortcuts(owner, in.SourceAgentID)
	if err != nil {
		informationError(w, store, err)
		return
	}
	var shortcut *userShortcut
	starters, _ := s.starterShortcutProjection(in.SourceAgentID)
	fromStarter := false
	for index, q := range append(view.Shortcuts, starters...) {
		if q.ID == in.ShortcutID {
			copy := q
			shortcut = &copy
			fromStarter = index >= len(view.Shortcuts)
			break
		}
	}
	if shortcut == nil {
		informationError(w, store, information.ErrNotFound)
		return
	}
	parts := inputref.Clone(shortcut.Parts)
	mapped := map[string]string{}
	var assets *os.Root
	var publicAssets *os.Root
	defer func() {
		if assets != nil {
			assets.Close()
		}
		if publicAssets != nil {
			publicAssets.Close()
		}
	}()
	for _, p := range inputPartPointers(parts) {
		if p.Type != "file" {
			continue
		}
		if replacement, ok := mapped[p.Path]; ok {
			p.Path = replacement
			continue
		}
		var assetRoot *os.Root
		if fromStarter && strings.HasPrefix(p.Path, "starter-assets/") {
			if publicAssets == nil {
				publicAssets, err = os.OpenRoot(s.starterAssetNamespaceForAgent(in.SourceAgentID))
				if err != nil {
					informationError(w, store, err)
					return
				}
			}
			assetRoot = publicAssets
		} else if assets == nil {
			namespace, e := s.mgr.shortcutAssetNamespace(owner, in.SourceAgentID)
			if e != nil {
				informationError(w, store, e)
				return
			}
			assets, e = os.OpenRoot(namespace)
			if e != nil {
				informationError(w, store, e)
				return
			}
		}
		if assetRoot == nil {
			assetRoot = assets
		}
		file, meta, e := readStarterAsset(assetRoot, p.Path)
		if e != nil {
			informationError(w, store, e)
			return
		}
		a, e := store.PutAsset(meta.Name, "", file, information.MaxAssetBytes)
		file.Close()
		if e != nil {
			informationError(w, store, e)
			return
		}
		if a.SHA256 != meta.SHA256 {
			informationError(w, store, information.ErrInvalid)
			return
		}
		mapped[p.Path] = a.Asset
		p.Path = a.Asset
	}
	q := &information.Shortcut{ID: shortcut.ID, Title: shortcut.Title, Content: shortcut.Content, Icon: shortcut.Icon, Language: shortcut.Language, InputLayout: shortcut.InputLayout, Parts: parts}
	v, e, err := store.Create(*in.Revision, information.CreateInput{ParentID: in.ParentID, Kind: "shortcut", Name: q.Title, Shortcut: q})
	if err != nil {
		informationError(w, store, err)
		return
	}
	informationMutation(w, v, e, 201)
}
func (s *Server) handleInformationResolve(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.session(w, r)
	if !ok {
		return
	}
	if s.rejectBackgroundTaskWrite(w, sess.ID) {
		return
	}
	store, _, ok := s.informationStore(w, r)
	if !ok {
		return
	}
	trusted, err := s.mgr.InformationStore(sess.owner)
	if err != nil || trusted.ScopeID() != store.ScopeID() {
		writeErr(w, 404, errors.New("信息库不属于当前会话"))
		return
	}
	var in struct {
		ID      string `json:"id"`
		Version int64  `json:"version"`
	}
	if !informationBody(w, r, &in) {
		return
	}
	if in.Version < 1 {
		informationError(w, store, information.ErrInvalid)
		return
	}
	entry, err := store.Get(in.ID, in.Version)
	if err != nil {
		informationError(w, store, err)
		return
	}
	parts := []inputref.Part{}
	layout := "top"
	if entry.File != nil {
		parts = append(parts, inputref.Part{Type: "file", Path: entry.File.Asset, AsFile: true})
	} else if q := entry.Shortcut; q != nil {
		parts = append(parts, inputref.Part{Type: "shortcut", ShortcutID: entry.ID + "_v" + strconv.FormatInt(entry.Version, 10), Title: q.Title, Text: q.Content, Icon: q.Icon, Language: q.Language, Parts: inputref.Clone(q.Parts)})
		if q.InputLayout != "" {
			layout = q.InputLayout
		}
	} else {
		informationError(w, store, information.ErrInvalid)
		return
	}
	attachments := []AttachmentMeta{}
	mapped := map[string]string{}
	fileParts := []*inputref.Part{}
	for _, part := range inputPartPointers(parts) {
		if part.Type == "file" {
			fileParts = append(fileParts, part)
		}
	}
	if len(fileParts) > 0 {
		err = tools.WithFileMutationLock(filepath.Join(sess.cwd, "uploads", "information"), func() error {
			// These witnesses are read from the authenticated session's own stored
			// messages/ledger. Unattributed old directories are never cache candidates.
			refs, referencedWorkspace := sessionUploadRefs(sess.Path())
			before, beforeErr := os.Stat(referencedWorkspace)
			current, currentErr := os.Stat(sess.cwd)
			if beforeErr != nil || currentErr != nil || !os.SameFile(before, current) {
				refs = nil
			}
			legacy := append(refs, sessionUploadLedger(sess.cwd, sess.ID)...)
			batch, e := store.BeginMaterialization(sess.cwd, legacy...)
			if e != nil {
				return e
			}
			defer batch.Rollback()
			for _, part := range fileParts {
				if p, ok := mapped[part.Path]; ok {
					part.Path = p
					continue
				}
				result, e := batch.MaterializeAsset(part.Path)
				if e != nil {
					return e
				}
				mapped[part.Path] = result.Path
				part.Path = result.Path
				attachments = append(attachments, informationAttachment(result.Asset, result.Path))
			}
			return batch.Commit()
		})
	}
	if err != nil {
		informationError(w, store, err)
		return
	}
	writeJSON(w, 200, map[string]any{"parts": parts, "input_layout": layout, "attachments": attachments, "entry": entry})
}
