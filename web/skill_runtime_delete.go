package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/yosukeno/pogo/skillruntime"
	"github.com/yosukeno/pogo/skillruntime/core"
)

func (s *Server) handleRuntimeSkillDelete(w http.ResponseWriter, r *http.Request) {
	p, ok := s.runtimeSkills(w)
	if !ok {
		return
	}
	digest := r.PathValue("digest")
	if !core.IsDigest(digest) {
		writeErr(w, 400, errors.New("Skill 版本标识无效"))
		return
	}
	// Definitions and session bindings cannot appear between the reference
	// check and removal. Other package versions remain independently usable.
	s.mgr.studio.runtimeSkillEdits.Lock()
	defer s.mgr.studio.runtimeSkillEdits.Unlock()
	var installed *skillruntime.Installed
	for _, entry := range p.List() {
		if entry.Ref.PackageDigest == digest {
			copy := entry
			installed = &copy
			break
		}
	}
	if installed == nil {
		writeErr(w, 404, skillruntime.ErrPackageNotFound)
		return
	}
	// force is the dynamic-unload escape hatch: references stop blocking the
	// delete, and every session that pinned this version keeps its transcript
	// but loses the Skill. Only calls literally in flight still block, inside
	// Service.Delete.
	if r.URL.Query().Get("force") != "1" {
		if err := s.mgr.checkRuntimeSkillDelete(*installed); err != nil {
			writeErr(w, 409, err)
			return
		}
	}
	result, err := p.Delete(digest)
	switch {
	case errors.Is(err, skillruntime.ErrPackageNotFound):
		writeErr(w, 404, err)
	case errors.Is(err, skillruntime.ErrPackageBusy):
		writeErr(w, 409, err)
	case err != nil:
		writeErr(w, 500, err)
	default:
		// The catalog lost a version: resync the MCP serve layer's runtime tools.
		s.mgr.fireRuntimeSkillChange()
		writeJSON(w, 200, result)
	}
}

func (m *Manager) checkRuntimeSkillDelete(installed skillruntime.Installed) error {
	ref := installed.Ref
	id := skillruntime.PackageSkillID(ref)
	references := func(raw json.RawMessage) (bool, error) {
		if len(raw) == 0 {
			return false, nil
		}
		var value struct {
			Packages []core.PackageRef `json:"packages"`
		}
		if err := json.Unmarshal(raw, &value); err != nil {
			return false, errors.New("无法核验已有Skill 绑定，请先修复配置")
		}
		for _, runtimeSkill := range value.Packages {
			if runtimeSkill.PackageDigest == ref.PackageDigest {
				return true, nil
			}
		}
		return false, nil
	}
	agentUses := func(a studioAgent) (bool, error) {
		bound, err := references(a.SkillBinding)
		if err != nil || bound {
			return bound, err
		}
		if a.Skills != nil {
			for _, selected := range *a.Skills {
				if selected == id {
					return true, nil
				}
			}
		}
		return false, nil
	}
	m.studio.mu.RLock()
	defer m.studio.mu.RUnlock()
	var agentNames, sessionIDs, orphanIDs []string
	for _, a := range m.studio.data.Agents {
		if a.Deleted {
			continue
		}
		used, err := agentUses(a)
		if err != nil {
			return err
		}
		if used {
			agentNames = append(agentNames, "「"+a.Name+"」")
		}
	}
	for id, binding := range m.studio.data.Bindings {
		// Deleted transcripts cannot be resumed; their registry tombstones
		// do not permanently prevent reclaiming an unused package version.
		if _, err := os.Stat(m.transcriptPath(id)); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		effective := binding.effectiveSkillBinding()
		used, err := agentUses(effective.Agent)
		if err != nil {
			return err
		}
		locked, err := references(effective.SkillLock)
		if err != nil {
			return err
		}
		if used || locked {
			// A session whose agent is deleted shows up in no list — name it
			// as the invisible blocker it is, or the count looks like a bug.
			if agent, ok := m.studio.data.Agents[binding.Agent.ID]; !ok || agent.Deleted {
				orphanIDs = append(orphanIDs, shortSessionID(id))
			} else {
				sessionIDs = append(sessionIDs, shortSessionID(id))
			}
		}
	}
	if len(agentNames)+len(sessionIDs)+len(orphanIDs) > 0 {
		return fmt.Errorf("此 Skill 版本仍被引用：%s。可强制删除：引用它的 Agent/会话会保留，但不再能使用此 Skill。", strings.Join(referenceSummary(agentNames, sessionIDs, orphanIDs), "；"))
	}
	return nil
}

// shortSessionID keeps error messages readable: the timestamp prefix is what
// a user matches against their session list.
func shortSessionID(id string) string {
	if len(id) > 20 {
		return id[:20] + "…"
	}
	return id
}

// referenceSummary caps each list at a few entries so one popular Skill does
// not turn the 409 into a wall of ids.
func referenceSummary(agents, sessions, orphans []string) []string {
	const max = 4
	cap := func(list []string) string {
		if len(list) > max {
			return strings.Join(list[:max], "、") + " 等"
		}
		return strings.Join(list, "、")
	}
	var parts []string
	if len(agents) > 0 {
		parts = append(parts, fmt.Sprintf("%d 个 Agent（%s）", len(agents), cap(agents)))
	}
	if len(sessions) > 0 {
		parts = append(parts, fmt.Sprintf("%d 个会话（%s）", len(sessions), cap(sessions)))
	}
	if len(orphans) > 0 {
		parts = append(parts, fmt.Sprintf("%d 个不可见的历史会话（%s，其 Agent 已删除）", len(orphans), cap(orphans)))
	}
	return parts
}
