package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/yosukeno/pogo/mcpobserve"
	"github.com/yosukeno/pogo/skillobserve"
	"github.com/yosukeno/pogo/skillruntime"
	"github.com/yosukeno/pogo/tools"
)

func (m *Manager) validateRuntimeSkillDefinition(in *studioInput) error {
	in.skillBinding = nil
	selection := skillruntime.Selection{SkillIDs: []string{}}
	if in.Skills != nil {
		for _, id := range *in.Skills {
			if strings.HasPrefix(id, "rs_") {
				selection.SkillIDs = append(selection.SkillIDs, id)
			}
		}
	}
	if selection.Empty() {
		return nil
	}
	if m.cfg.SkillRuntime == nil {
		return errors.New("Skill 工具运行环境尚未启用")
	}
	plan, err := m.cfg.SkillRuntime.PlanSelection(selection)
	if err != nil {
		return err
	}
	if in.Status == "ready" {
		if !plan.Ready {
			return fmt.Errorf("Agent 尚未就绪：%s", plan.Issues[0].Message)
		}
		if err := m.cfg.SkillRuntime.ValidateBinding(plan.Binding); err != nil {
			return err
		}
	}
	in.skillBinding = plan.Binding
	return nil
}

func (s *Server) runtimeSkillRoutes() {
	s.mux.HandleFunc("GET /api/manage/skills", s.admin(s.handleManageSkills))
	s.mux.HandleFunc("GET /api/manage/skills/{id}/content", s.admin(s.handleManageSkillContent))
	s.mux.HandleFunc("PUT /api/manage/skills/{id}/content", s.admin(s.handleManageSkillSaveContent))
	s.mux.HandleFunc("GET /api/manage/skills/{id}/files", s.admin(s.handleManageSkillFiles))
	s.mux.HandleFunc("DELETE /api/manage/skills/{id}", s.admin(s.handleManageSkillDelete))
	s.mux.HandleFunc("POST /api/manage/skills/{id}/disable", s.admin(s.handleManageSkillDisable))
	s.mux.HandleFunc("POST /api/manage/skill-selection", s.admin(s.handleSkillSelection))
	s.mux.HandleFunc("POST /api/sessions/{sid}/skill-files", s.handleRuntimeSkillUpload)
	s.mux.HandleFunc("GET /api/sessions/{sid}/skill-capabilities", s.handleRuntimeSkillCapabilities)
	s.mux.HandleFunc("GET /api/sessions/{sid}/skill-artifacts/{artifactId}", s.handleRuntimeSkillArtifact)
	// Only administrator acceptance tooling may bypass the conversational gate.
	s.mux.HandleFunc("POST /api/sessions/{sid}/skill-calls", s.admin(s.handleRuntimeSkillCall))
}

func (s *Server) handleSkillSelection(w http.ResponseWriter, r *http.Request) {
	p, ok := s.runtimeSkills(w)
	if !ok {
		return
	}
	var input skillruntime.Selection
	if err := decodeBody(r, &input); err != nil {
		writeErr(w, 400, err)
		return
	}
	plan, err := p.PlanSelection(input)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, plan)
}
func (s *Server) runtimeSkills(w http.ResponseWriter) (*skillruntime.Service, bool) {
	if s.mgr.cfg.SkillRuntime == nil {
		writeErr(w, 404, errors.New("Skill prototype is disabled"))
		return nil, false
	}
	return s.mgr.cfg.SkillRuntime, true
}
func (s *Session) runtimeSkillSnapshot() *skillruntime.BoundSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runtimeSkill
}

func (s *Server) runtimeSkillSession(w http.ResponseWriter, r *http.Request) (*Session, bool) {
	sess, ok := s.session(w, r)
	if !ok {
		return nil, false
	}
	if sess.runtimeSkillSnapshot() == nil {
		writeErr(w, 409, errors.New("Skill session is unavailable; inspect its pinned configuration"))
		return nil, false
	}
	return sess, true
}
func (s *Server) handleRuntimeSkillUpload(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.runtimeSkillSession(w, r)
	if !ok {
		return
	}
	if s.rejectBackgroundTaskWrite(w, sess.ID) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 32<<20)
	data, err := io.ReadAll(r.Body)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	runtime := sess.runtimeSkillSnapshot()
	if runtime == nil {
		writeErr(w, 409, errors.New("Skill session is unavailable"))
		return
	}
	file, err := runtime.Upload(data)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 201, file)
}
func (s *Server) handleRuntimeSkillCapabilities(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.runtimeSkillSession(w, r)
	if !ok {
		return
	}
	runtime := sess.runtimeSkillSnapshot()
	if runtime == nil {
		writeErr(w, 409, errors.New("Skill session is unavailable"))
		return
	}
	writeJSON(w, 200, map[string]any{"capabilities": runtime.Descriptors(), "lock": runtime.Lock})
}
func (s *Server) handleRuntimeSkillCall(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.runtimeSkillSession(w, r)
	if !ok {
		return
	}
	if s.rejectBackgroundTaskWrite(w, sess.ID) {
		return
	}
	var input struct {
		Capability string          `json:"capability"`
		Input      json.RawMessage `json:"input"`
	}
	if err := decodeBody(r, &input); err != nil {
		writeErr(w, 400, err)
		return
	}
	callContext, releaseCall, err := (&mcpLayer{mgr: s.mgr, agentID: sess.studioID}).reserveAgentCall(r.Context(), sess)
	if err != nil {
		writeErr(w, 409, err)
		return
	}
	defer releaseCall()
	// The administrator's direct call observes like the conversational path:
	// internal caller through the pogo entrypoint, identity resolved from the
	// capability's owning bundle — never from the request's words. An unbound
	// capability resolves no identity and records nothing.
	md := mcpobserve.Metadata{SessionID: sess.ID, Entrypoint: "pogo", AgentID: sess.studioID, AgentName: sess.studioName}
	sess.mu.Lock()
	if sess.owner != nil {
		md.OwnerID = sess.owner.SubjectID
	}
	sess.mu.Unlock()
	runtime := sess.runtimeSkillSnapshot()
	if runtime == nil {
		writeErr(w, 409, errors.New("Skill session is unavailable"))
		return
	}
	var identity skillobserve.Identity
	wireTool := ""
	for _, d := range runtime.Descriptors() {
		if d.ID == input.Capability {
			identity = runtimeSkillIdentity(d.SkillID, d.SkillName)
			wireTool = d.Name
			break
		}
	}
	ctx := mcpobserve.WithMetadata(callContext, md)
	ctx = skillobserve.WithRecorder(ctx, s.mgr.SkillRecorder())
	ctx = skillobserve.WithSource(ctx, "internal", "pogo")
	ctx, span := skillobserve.Start(ctx, identity, "execute", "runtime", input.Capability, wireTool)
	defer span.End("interrupted", "unknown_outcome")
	out, err := runtime.Call(ctx, input.Capability, input.Input)
	if err != nil {
		span.End("error", "execution_error")
		writeErr(w, 400, err)
		return
	}
	span.End("ok", "")
	writeJSON(w, 200, out)
}

// RuntimeSkill capabilities compose with the Agent's selected Skill/MCP snapshot.
// Their private store and executor are never added to ordinary tool read roots.
func (m *Manager) runtimeSkillTools(binding studioBinding, sessionID string) (*skillruntime.BoundSession, []tools.Tool, string) {
	if len(binding.Agent.SkillBinding) == 0 && len(binding.SkillLock) == 0 {
		return nil, nil, ""
	}
	var bound *skillruntime.BoundSession
	var err error
	if m.cfg.SkillRuntime == nil {
		err = errors.New("Skill executor is not configured")
	} else {
		bound, err = m.cfg.SkillRuntime.Session(binding.Agent.SkillBinding, binding.SkillLock, sessionID)
	}
	if err != nil {
		return nil, nil, "\nSkill capabilities are unavailable: " + err.Error() + ". Report this to the user; do not claim execution or use shell to bypass the Skill binding."
	}
	return runtimeSkillProjection(bound)
}

func runtimeSkillProjection(bound *skillruntime.BoundSession) (*skillruntime.BoundSession, []tools.Tool, string) {
	prompt := "\n\n# Skill capabilities\nUse the bound tools below for Skill operations. Their runtime and assets are private to the executor. File arguments use this conversation's opaque fileId values (included with uploaded attachments), never workspace paths. Ordinary tools cannot access the Skill store; do not fall back to shell to bypass a denied Skill operation.\n"
	list := []tools.Tool{&tools.RuntimeSkillRead{Session: bound}}
	prompt += "Use skill_read with the Skill name and relative path to load bundled reference documents. skill-runtime:// locations are opaque identifiers, not filesystem paths.\n"
	for _, descriptor := range bound.Descriptors() {
		list = append(list, &tools.RuntimeTool{Session: bound, Descriptor: descriptor, Identity: runtimeSkillIdentity(descriptor.SkillID, descriptor.SkillName)})
		prompt += fmt.Sprintf("\nCapability %s is tool %s.", descriptor.ID, descriptor.Name)
	}
	return bound, list, prompt + bound.SkillText()
}

func privateRuntimeSkillStore(store, workspace string) bool {
	for _, pair := range [][2]string{{store, workspace}, {workspace, store}} {
		rel, err := filepath.Rel(pair[0], pair[1])
		if err != nil {
			return false
		}
		if rel == "." || !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != ".." {
			return false
		}
	}
	return true
}
func (m *Manager) prepareRuntimeSkillAgent(ctx context.Context, a studioAgent) error {
	if len(a.SkillBinding) == 0 {
		return nil
	}
	if m.cfg.SkillRuntime == nil {
		return errors.New("Skill prototype is disabled")
	}
	return m.cfg.SkillRuntime.Prepare(ctx, a.SkillBinding)
}
