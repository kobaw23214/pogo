package web

import (
	"bytes"
	"errors"
	"mime"
	"net/http"
	"time"
)

func (s *Server) handleRuntimeSkillArtifact(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.runtimeSkillSession(w, r)
	if !ok {
		return
	}
	runtime := sess.runtimeSkillSnapshot()
	if runtime == nil {
		writeErr(w, 409, errors.New("Skill session is unavailable"))
		return
	}
	content, err := runtime.ReadArtifact(r.PathValue("artifactId"))
	if err != nil {
		writeErr(w, http.StatusNotFound, errors.New("artifact is unavailable for this session"))
		return
	}
	mediaType, _, err := mime.ParseMediaType(content.Artifact.MediaType)
	if err != nil {
		mediaType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", mediaType)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": content.Artifact.Name}))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "private, no-store")
	http.ServeContent(w, r, content.Artifact.Name, time.Time{}, bytes.NewReader(content.Data))
}
