package web

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"slices"

	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/skillauthor"
	"github.com/yosukeno/pogo/skills"
)

func (s *Server) authoredSkillRoutes() {
	base := "/api/apps/{aid}/authored-skills"
	s.mux.HandleFunc("GET "+base, s.handleAuthoredSkills)
	s.mux.HandleFunc("GET "+base+"/{id}/content", s.handleAuthoredSkillContent)
	s.mux.HandleFunc("PUT "+base+"/{id}/content", s.handleAuthoredSkillEdit)
	s.mux.HandleFunc("GET "+base+"/{id}/files", s.handleAuthoredSkillFiles)
	s.mux.HandleFunc("GET "+base+"/{id}/versions", s.handleAuthoredSkillVersions)
	s.mux.HandleFunc("GET "+base+"/{id}/export", s.handleAuthoredSkillExport)
}

func (s *Server) authoredSkillOwner(w http.ResponseWriter, r *http.Request) (*identity.Scope, *skillauthor.Store, bool) {
	var owner *identity.Scope
	if scope, ok := s.ownerScope(r); ok {
		owner = &scope
	} else if s.multiuser() {
		writeErr(w, 401, errors.New("请先登录"))
		return nil, nil, false
	}
	aid := r.PathValue("aid")
	a, ok := s.mgr.studio.get(aid)
	if !validAgentDirName(aid) || !ok || a.Status != "ready" && !s.adminRequest(r) {
		writeErr(w, 404, ErrNotFound)
		return nil, nil, false
	}
	store, err := s.mgr.authoredSkillStore(owner, aid)
	if err != nil {
		writeErr(w, 503, errors.New("Skill 存储不可用"))
		return nil, nil, false
	}
	w.Header().Set("Cache-Control", "no-store")
	return owner, store, true
}

func (s *Server) handleAuthoredSkills(w http.ResponseWriter, r *http.Request) {
	_, store, ok := s.authoredSkillOwner(w, r)
	if !ok {
		return
	}
	entries, err := store.List()
	if err != nil {
		status, err := authoredSkillError(err)
		writeErr(w, status, err)
		return
	}
	rows := make([]any, 0, len(entries))
	for _, entry := range entries {
		rows = append(rows, authoredSummary(entry))
	}
	writeJSON(w, 200, map[string]any{"skills": rows})
}

func readAuthoredRequest(w http.ResponseWriter, r *http.Request, store *skillauthor.Store) (skillauthor.Snapshot, bool) {
	entry, err := store.Read(r.PathValue("id"), r.URL.Query().Get("revision"))
	if err != nil {
		status, err := authoredSkillError(err)
		writeErr(w, status, err)
		return entry, false
	}
	return entry, true
}

func writeAuthoredDocument(w http.ResponseWriter, entry skillauthor.Snapshot, relative string, historical bool) {
	if relative == "" {
		relative = "SKILL.md"
	}
	content, err := authoredReadResult(entry, relative)
	if err != nil {
		status, err := authoredSkillError(err)
		if errors.Is(err, skills.ErrDocumentPath) {
			status = 400
		}
		writeErr(w, status, err)
		return
	}
	language, mediaType := skills.DocumentFormat(content.Path)
	editable := !historical && content.Path == "SKILL.md"
	reason := ""
	if historical {
		reason = "历史版本只读，可从此版本恢复为新版本"
	} else if !editable {
		reason = "参考文档只读；可让 Agent 使用 skill_save 保存完整的新版本"
	}
	writeJSON(w, 200, map[string]any{"name": entry.Name, "source": "authored", "path": "skill-authored://" + entry.ID + "/SKILL.md",
		"relative_path": content.Path, "language": language, "media_type": mediaType, "content": content.Content,
		"truncated": false, "editable": editable, "revision": entry.Revision, "version": entry.Version, "edit_disabled_reason": reason})
}

func (s *Server) handleAuthoredSkillContent(w http.ResponseWriter, r *http.Request) {
	_, store, ok := s.authoredSkillOwner(w, r)
	if !ok {
		return
	}
	entry, ok := readAuthoredRequest(w, r, store)
	if !ok {
		return
	}
	// Explicitly selecting a version is a historical preview even if it is
	// currently latest; edits always start from a newly loaded current view.
	writeAuthoredDocument(w, entry, r.URL.Query().Get("path"), r.URL.Query().Get("revision") != "")
}

func (s *Server) handleAuthoredSkillFiles(w http.ResponseWriter, r *http.Request) {
	_, store, ok := s.authoredSkillOwner(w, r)
	if !ok {
		return
	}
	entry, ok := readAuthoredRequest(w, r, store)
	if !ok {
		return
	}
	files, err := authoredFileList(entry, r.URL.Query().Get("path"))
	if err != nil {
		status, err := authoredSkillError(err)
		if errors.Is(err, skills.ErrDocumentPath) {
			status = 400
		}
		writeErr(w, status, err)
		return
	}
	writeJSON(w, 200, files)
}

func (s *Server) handleAuthoredSkillVersions(w http.ResponseWriter, r *http.Request) {
	_, store, ok := s.authoredSkillOwner(w, r)
	if !ok {
		return
	}
	entries, err := store.Versions(r.PathValue("id"))
	if err != nil {
		status, err := authoredSkillError(err)
		writeErr(w, status, err)
		return
	}
	rows := make([]any, 0, len(entries))
	for _, entry := range entries {
		rows = append(rows, map[string]any{"revision": entry.Revision, "version": entry.Version, "created_at": entry.UpdatedAt, "enabled": entry.Enabled})
	}
	current := ""
	if len(entries) > 0 {
		current = entries[len(entries)-1].Revision
	}
	slices.Reverse(rows)
	writeJSON(w, 200, map[string]any{"versions": rows, "current_revision": current})
}

func (s *Server) handleAuthoredSkillEdit(w http.ResponseWriter, r *http.Request) {
	owner, store, ok := s.authoredSkillOwner(w, r)
	if !ok {
		return
	}
	if r.URL.Query().Get("revision") != "" {
		writeErr(w, 400, errors.New("历史预览不能直接编辑"))
		return
	}
	var input struct {
		Content          *string `json:"content"`
		ExpectedRevision string  `json:"expected_revision"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || input.Content == nil || !skillDocumentRevisionRE.MatchString(input.ExpectedRevision) {
		writeErr(w, 400, errors.New("需要完整 content 和有效 expected_revision"))
		return
	}
	if decoder.Decode(new(any)) != io.EOF {
		writeErr(w, 400, errors.New("只能提交一个 JSON 对象"))
		return
	}
	entry, err := store.Read(r.PathValue("id"), "")
	if err != nil {
		status, err := authoredSkillError(err)
		writeErr(w, status, err)
		return
	}
	base, err := store.Read(entry.ID, input.ExpectedRevision)
	if err != nil {
		if errors.Is(err, skillauthor.ErrNotFound) {
			writeErr(w, 409, skillauthor.ErrConflict)
		} else {
			status, err := authoredSkillError(err)
			writeErr(w, status, err)
		}
		return
	}
	// Preserve every reference byte. A stale revision cannot overwrite a
	// concurrent Agent edit, and every edit becomes another complete snapshot.
	files := slices.Clone(base.Files)
	for i := range files {
		if files[i].Path == "SKILL.md" {
			files[i].Content = *input.Content
		}
	}
	request := skillauthor.SaveRequest{RequestID: authoredRequestDigest(entry.ID, input.ExpectedRevision, *input.Content), SkillID: entry.ID, ExpectedRevision: input.ExpectedRevision, Files: files}
	name, _, err := skillauthor.ValidateFiles(files)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	s.mgr.studio.runtimeSkillEdits.Lock()
	err = s.mgr.checkAuthoredSkillName(name)
	var saved skillauthor.SaveResult
	if err == nil {
		saved, err = store.Save(r.Context(), request)
	}
	s.mgr.studio.runtimeSkillEdits.Unlock()
	if err != nil {
		status, err := authoredSkillError(err)
		writeErr(w, status, err)
		return
	}
	s.mgr.syncAuthoredSkillSessions(owner, r.PathValue("aid"))
	writeAuthoredDocument(w, saved.Skill, "SKILL.md", false)
}

func (s *Server) handleAuthoredSkillExport(w http.ResponseWriter, r *http.Request) {
	_, store, ok := s.authoredSkillOwner(w, r)
	if !ok {
		return
	}
	entry, ok := readAuthoredRequest(w, r, store)
	if !ok {
		return
	}
	var data bytes.Buffer
	archive := zip.NewWriter(&data)
	for _, file := range entry.Files {
		out, err := archive.Create(entry.Name + "/" + file.Path)
		if err != nil {
			writeErr(w, 500, errors.New("Skill 导出失败"))
			return
		}
		if _, err = io.WriteString(out, file.Content); err != nil {
			writeErr(w, 500, errors.New("Skill 导出失败"))
			return
		}
	}
	if err := archive.Close(); err != nil {
		writeErr(w, 500, errors.New("Skill 导出失败"))
		return
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": entry.Name + ".zip"}))
	_, _ = w.Write(data.Bytes())
}
