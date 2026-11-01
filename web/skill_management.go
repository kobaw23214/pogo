package web

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/yosukeno/pogo/skillruntime"
	"github.com/yosukeno/pogo/skills"
)

func (s *Server) handleManageSkills(w http.ResponseWriter, r *http.Request) {
	entries, catalog := s.mgr.skillCapabilities()
	var execution *skillruntime.ExecutionStatus
	if service := s.mgr.cfg.SkillRuntime; service != nil {
		status := service.ExecutionStatus()
		execution = &status
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"skills": entries, "tools": catalog.Tools, "execution": execution, "max_upload_bytes": skillUploadMaxBytes()})
}

func (m *Manager) uploadedSkillRoot() string {
	dir := m.SkillOptions().UserDir
	if dir == "" {
		dir = skills.UserDir()
	}
	return dir
}

func (m *Manager) removableSkill(skill skills.Skill) bool {
	if skill.Source != "user" || skill.Dir == "" {
		return false
	}
	root, err := filepath.Abs(m.uploadedSkillRoot())
	if err != nil {
		return false
	}
	dir, err := filepath.Abs(skill.Dir)
	if err != nil || filepath.Dir(dir) != root {
		return false
	}
	return skillNameRE.MatchString(filepath.Base(dir))
}

func (s *Server) packagedSkill(id string) (*skillruntime.Installed, bool) {
	service := s.mgr.cfg.SkillRuntime
	if service == nil {
		return nil, false
	}
	for _, installed := range service.Catalog() {
		if skillruntime.PackageSkillID(installed.Ref) == id {
			return &installed, true
		}
	}
	return nil, false
}

func (s *Server) handleManageSkillContent(w http.ResponseWriter, r *http.Request) {
	s.mgr.studio.runtimeSkillEdits.RLock()
	defer s.mgr.studio.runtimeSkillEdits.RUnlock()
	id := r.PathValue("id")
	if installed, ok := s.packagedSkill(id); ok {
		writeSkillDocument(w, skills.Skill{
			Name:   installed.Skill,
			Source: "bundle",
			ReadDocument: func(relative string) ([]byte, bool, error) {
				return s.mgr.cfg.SkillRuntime.SkillStaticDocument(id, relative)
			},
		}, r.URL.Query().Get("path"), skillDocumentEditMetadata{Reason: "运行时 Skill 包由摘要固定，请重新导入新版本，不能直接改写"})
		return
	}
	skill, ok := s.mgr.skillCatalogIDs()[id]
	if !ok {
		writeErr(w, 404, ErrNotFound)
		return
	}
	relative, err := skills.DocumentPath(r.URL.Query().Get("path"))
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeSkillDocument(w, skill, relative, s.mgr.skillDocumentEditMetadata(skill, relative))
}

func (s *Server) handleManageSkillDisable(w http.ResponseWriter, r *http.Request) {
	installed, ok := s.packagedSkill(r.PathValue("id"))
	if !ok {
		writeErr(w, 400, errors.New("该 Skill 由系统或目录配置管理，无法在此停用"))
		return
	}
	if err := s.mgr.cfg.SkillRuntime.Revoke(installed.Ref.PackageDigest); err != nil {
		writeErr(w, 409, err)
		return
	}
	// Revocation drops the package's runtime tools from the MCP serve layer.
	s.mgr.fireRuntimeSkillChange()
	writeJSON(w, 200, map[string]bool{"disabled": true})
}

func (s *Server) handleManageSkillDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if installed, ok := s.packagedSkill(id); ok {
		r.SetPathValue("digest", installed.Ref.PackageDigest)
		s.handleRuntimeSkillDelete(w, r)
		return
	}
	// Definition writes and removals share the same reference lock as bundled
	// Skills. No Agent can acquire a new reference between this check and delete.
	m := s.mgr
	m.studio.runtimeSkillEdits.Lock()
	defer m.studio.runtimeSkillEdits.Unlock()
	skill, ok := m.skillCatalogIDs()[id]
	if !ok {
		writeErr(w, 404, ErrNotFound)
		return
	}
	if !m.removableSkill(skill) {
		writeErr(w, 409, errors.New("系统内置或目录挂载的 Skill 不能从此处删除"))
		return
	}
	// force skips the reference check: agents/sessions that pinned this skill
	// keep their transcripts and simply lose it (dynamic unload).
	if r.URL.Query().Get("force") != "1" {
		if err := m.checkOrdinarySkillDelete(id); err != nil {
			writeErr(w, 409, err)
			return
		}
	}
	root, err := os.OpenRoot(m.uploadedSkillRoot())
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	defer root.Close()
	if err := root.RemoveAll(filepath.Base(skill.Dir)); err != nil {
		writeErr(w, 500, err)
		return
	}
	loaded, _ := skills.Load(m.SkillOptions())
	m.SetSkills(loaded)
	writeJSON(w, 200, map[string]bool{"deleted": true, "cleanup_pending": false})
}

func (m *Manager) checkOrdinarySkillDelete(id string) error {
	uses := func(a studioAgent) bool {
		if a.Skills == nil {
			return true
		}
		for _, selected := range *a.Skills {
			if selected == id {
				return true
			}
		}
		return false
	}
	m.studio.mu.RLock()
	defer m.studio.mu.RUnlock()
	var agentNames, sessionIDs, orphanIDs []string
	for _, a := range m.studio.data.Agents {
		if !a.Deleted && uses(a) {
			agentNames = append(agentNames, "「"+a.Name+"」")
		}
	}
	for sid, binding := range m.studio.data.Bindings {
		if _, err := os.Stat(m.transcriptPath(sid)); os.IsNotExist(err) {
			continue
		} else if err != nil {
			return err
		}
		if !uses(binding.effectiveSkillBinding().Agent) {
			continue
		}
		if agent, ok := m.studio.data.Agents[binding.Agent.ID]; !ok || agent.Deleted {
			orphanIDs = append(orphanIDs, shortSessionID(sid))
		} else {
			sessionIDs = append(sessionIDs, shortSessionID(sid))
		}
	}
	if len(agentNames)+len(sessionIDs)+len(orphanIDs) > 0 {
		return fmt.Errorf("此 Skill 仍被引用：%s。可强制删除：引用它的 Agent/会话会保留，但不再能使用此 Skill。", strings.Join(referenceSummary(agentNames, sessionIDs, orphanIDs), "；"))
	}
	return nil
}
