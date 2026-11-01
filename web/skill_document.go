package web

import (
	"errors"
	"net/http"

	"github.com/yosukeno/pogo/skills"
)

func writeSkillDocument(w http.ResponseWriter, skill skills.Skill, relative string, edit ...skillDocumentEditMetadata) {
	relative, err := skills.DocumentPath(relative)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	data, truncated, err := skills.ReadDocument(skill, relative)
	if err != nil {
		status := http.StatusNotFound
		if errors.Is(err, skills.ErrDocumentPath) {
			status = http.StatusBadRequest
		} else if errors.Is(err, skills.ErrDocumentNotText) {
			status = http.StatusUnsupportedMediaType
		}
		writeErr(w, status, err)
		return
	}
	writeSkillDocumentBytes(w, skill, relative, data, truncated, edit...)
}

func writeSkillDocumentBytes(w http.ResponseWriter, skill skills.Skill, relative string, data []byte, truncated bool, edit ...skillDocumentEditMetadata) {
	language, mediaType := skills.DocumentFormat(relative)
	w.Header().Set("Cache-Control", "no-store")
	metadata := skillDocumentEditMetadata{}
	if len(edit) > 0 {
		metadata = edit[0]
	}
	if truncated {
		metadata = skillDocumentEditMetadata{Reason: "文档超过预览长度，无法在线编辑"}
	}
	revision := ""
	if !truncated {
		revision = skillDocumentRevision(data)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"name": skill.Name, "path": skill.Path, "source": skill.Source,
		"relative_path": relative, "language": language, "media_type": mediaType,
		"content": string(data), "truncated": truncated,
		"editable": metadata.Editable, "revision": revision, "edit_disabled_reason": metadata.Reason,
	})
}
