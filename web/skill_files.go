package web

import (
	"errors"
	"net/http"

	"github.com/yosukeno/pogo/skills"
)

func (s *Server) handleManageSkillFiles(w http.ResponseWriter, r *http.Request) {
	id, relative := r.PathValue("id"), r.URL.Query().Get("path")
	var files skills.FileList
	var err error
	if _, ok := s.packagedSkill(id); ok {
		files, err = s.mgr.cfg.SkillRuntime.SkillFiles(id, relative)
	} else {
		skill, ok := s.mgr.skillCatalogIDs()[id]
		if !ok || skill.Source == "embedded" {
			writeErr(w, http.StatusNotFound, ErrNotFound)
			return
		}
		files, err = skills.ListFiles(skill, relative)
	}
	if err != nil {
		status := http.StatusNotFound
		if errors.Is(err, skills.ErrDocumentPath) {
			status = http.StatusBadRequest
		}
		writeErr(w, status, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, files)
}
