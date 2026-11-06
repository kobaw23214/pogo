package web

// Per-Agent capability binding: the Skill/MCP/model selections a Studio
// definition carries, the admin catalog the picker is built from, and the
// session-bound projections the runtime enforces. The selections live on the
// definition (and therefore on every pinned conversation binding); this file
// resolves them against the live catalogs — create/update time (reject the
// unknown), session construction and run refresh (fail closed on the
// deleted), and the read endpoints (report the effective set).

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"sort"
	"strings"

	"github.com/yosukeno/pogo/mcpclient"
	"github.com/yosukeno/pogo/skillruntime"
	"github.com/yosukeno/pogo/skills"
	"github.com/yosukeno/pogo/tools"
)

// capabilitySkill is one entry of the admin Skill catalog. The ID is the
// observation identity's opaque hash: deterministic for a stable name+path,
// meaningless as a filesystem fact, and never a path itself.
type capabilitySkill struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Aliases     []string `json:"aliases,omitempty"`
	Description string   `json:"description"`
	Hidden      bool     `json:"hidden"`
	ToolCount   int      `json:"tool_count"`
	// Builtin identifies trusted system origin. BuiltinRequired separately
	// identifies always-present guidance; native-linked Skills are selectable
	// because checking them controls executable capability grants.
	Builtin           bool     `json:"builtin"`
	BuiltinRequired   bool     `json:"builtin_required"`
	NativeTools       []string `json:"native_tools,omitempty"`
	LegacyNativeTools []string `json:"legacy_native_tools,omitempty"`
	// BrowserTask marks the arm-the-gate kind (browser-use): invoking the
	// skill opens the browser gate for that run. Surfaced so the picker and
	// the library card can say "增强内置" rather than leaving the checkbox's
	// meaning to guesswork.
	BrowserTask bool   `json:"browser_task,omitempty"`
	Source      string `json:"source"`
	State       string `json:"state"`
	Removable   bool   `json:"removable"`
}

// capabilityMCPServer is one entry of the admin MCP catalog: the
// configuration's name and shape plus the pool's live state. Credentials,
// headers and env values never leave mcp.json.
type capabilityMCPServer struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Transport string `json:"transport"`
	Enabled   bool   `json:"enabled"`
	Status    string `json:"status"`
	ToolCount int    `json:"tool_count"`
}

// skillIDOf is the catalog identity a selection references. Empty when the
// skill has no usable identity (no path): such a skill is invisible to the
// picker rather than selectable-but-unresolvable.
func skillIDOf(sk skills.Skill) string {
	if id := authoredProjectionID(sk); id != "" {
		return id
	}
	return skillIdentityOf(sk).ID
}

// skillCatalogIDs indexes the manager's loaded skills by catalog ID.
func (m *Manager) skillCatalogIDs() map[string]skills.Skill {
	list := m.loadedSkills()
	out := make(map[string]skills.Skill, len(list))
	for _, sk := range list {
		if id := skillIDOf(sk); id != "" {
			out[id] = sk
		}
	}
	return out
}

// resolveSkillIDs maps a pinned selection onto the live catalog. An ID whose
// skill was deleted or renamed since the definition was saved is dropped —
// fail closed: the conversation simply lacks that capability, and nothing
// widens it back to the global set.
func (m *Manager) resolveSkillIDs(ids []string) []skills.Skill {
	known := m.skillCatalogIDs()
	out := make([]skills.Skill, 0, len(ids))
	for _, id := range ids {
		if sk, ok := known[id]; ok && agentConversationSkill(sk) {
			out = append(out, sk)
		}
	}
	return out
}

// studioSkillNames records what a conversation was actually given. Legacy
// definitions inherit the global set; explicit selections record exactly
// what resolved at creation (a dropped ID leaves its absence visible).
func (m *Manager) studioSkillNames(a studioAgent) []string {
	if a.Skills == nil {
		return m.SkillNames()
	}
	return skills.Names(m.resolveSkillIDs(*a.Skills))
}

// mcpServerConfigs reads the configured MCP servers this manager connects
// to. The manager knows only its store path; a manager without one has no
// MCP layer and therefore an empty catalog.
func (m *Manager) mcpServerConfigs() map[string]*mcpclient.ServerConfig {
	configured, _ := m.loadMCPServerConfigs()
	return configured
}

func (m *Manager) loadMCPServerConfigs() (map[string]*mcpclient.ServerConfig, error) {
	if m.cfg.MCPClientStore == "" {
		return map[string]*mcpclient.ServerConfig{}, nil
	}
	cfg, err := mcpclient.Load(m.cfg.MCPClientStore)
	if err != nil {
		return nil, err
	}
	if cfg.Servers == nil {
		return map[string]*mcpclient.ServerConfig{}, nil
	}
	return cfg.Servers, nil
}

// validateStudioRefs is the studio store's reference hook: the shape pass in
// validateStudioInput has already normalized the arrays; here every
// reference must exist in the live catalogs. A disabled server is rejected —
// selecting it would connect nothing while reading as if it would — but a
// configured server that is merely down is fine: the point of the selection
// is to be ready before the service is.
func (m *Manager) validateStudioRefs(in *studioInput) error {
	if err := m.validateRuntimeSkillDefinition(in); err != nil {
		return err
	}
	if in.Skills != nil {
		known := m.skillCatalogIDs()
		if m.cfg.SkillRuntime != nil {
			for _, sk := range m.cfg.SkillRuntime.SelectionCatalog().Skills {
				if sk.State != "revoked" && sk.State != "unavailable" {
					known[sk.ID] = skills.Skill{Name: sk.SkillID}
				}
			}
		}
		seenNames := map[string]bool{}
		retired := m.retiredPlatformSkillIDs()
		for _, id := range *in.Skills {
			if retired[id] {
				continue
			} // Compatibility only; never registered for execution.
			sk, ok := known[id]
			if !ok {
				return fmt.Errorf("未知的 Skill：%s", id)
			}
			if seenNames[sk.Name] {
				return fmt.Errorf("Skill 名称重复：%s，请只选择一个版本", sk.Name)
			}
			seenNames[sk.Name] = true
		}
	}
	if in.MCPServers != nil && len(*in.MCPServers) > 0 {
		configured := m.mcpServerConfigs()
		for _, name := range *in.MCPServers {
			sc, ok := configured[name]
			if !ok {
				return fmt.Errorf("未知的 MCP 服务器：%s", name)
			}
			if !sc.IsEnabled() {
				return fmt.Errorf("MCP 服务器 %s 已停用，请先启用后再选择", name)
			}
		}
	}
	if in.Tools != nil {
		known := tools.BuiltinNameSet()
		for _, name := range *in.Tools {
			if !known[name] {
				return fmt.Errorf("未知的内置工具：%s", name)
			}
		}
	}
	return nil
}

// studioSelection resolves a pinned binding into the session-facing views.
// A nil skills result means legacy global inheritance; a non-nil (possibly
// empty) slice is the resolved subset. A nil mcpOnly means the whole pool;
// a non-nil (possibly empty) map is the selected server set.
func (m *Manager) studioSelection(a studioAgent) (subset []skills.Skill, mcpOnly map[string]bool) {
	if a.Skills != nil {
		subset = m.resolveSkillIDs(*a.Skills)
		if subset == nil {
			subset = []skills.Skill{}
		}
	}
	if a.MCPServers != nil {
		mcpOnly = map[string]bool{}
		for _, name := range *a.MCPServers {
			mcpOnly[name] = true
		}
	}
	return subset, mcpOnly
}

// unregisterSynthTools keeps every skill surface a legacy Studio definition
// has always meant — prompt sections, read roots, /skill:name, observation —
// but withholds manifest-tool registration: schema tokens are paid only by
// conversations whose Agent explicitly chose the skill. The definition's
// editor is the place to make that choice. Ordinary sessions are not routed
// here — with no selection mechanism of their own, the global set stays
// their registration. The catalog itself is untouched either way: /api/tools
// and MCP tools/list still show everything declared.
func (m *Manager) unregisterSynthTools(v skillViews) skillViews {
	out := v
	out.synthTools = nil
	out.synthRO = nil
	return out
}

// filterSkillViews projects the manager's consistent skill view down to a
// selected subset. Every derived surface moves together — prompt section,
// readable roots, child -skill flags, synthesized manifest tools and the
// observation snapshot — so a conversation can never see half a selection.
// Catalog-derived entries carry their skill's name, which is the join key
// back to the subset.
func (m *Manager) filterSkillViews(v skillViews, subset []skills.Skill) skillViews {
	subset = agentConversationSkills(subset)
	names := make(map[string]bool, len(subset))
	for _, sk := range subset {
		names[sk.Name] = true
	}
	out := skillViews{
		section:   skills.PromptSection(subset),
		uiSection: uiCapabilitiesSection(subset),
		readRoots: append(append([]string{}, skills.Roots(subset)...), m.cfg.ReadRoots...),
	}
	for _, sk := range subset {
		if sk.Source == "authored" {
			continue
		}
		if p := sk.Dir; p != "" {
			out.pathFlags = append(out.pathFlags, p)
		} else if p := sk.Path; p != "" {
			out.pathFlags = append(out.pathFlags, p)
		}
	}
	keep := map[string]bool{}
	for _, info := range v.synthCatalog {
		if names[info.Skill] {
			out.synthCatalog = append(out.synthCatalog, info)
			keep[info.WireName] = true
		}
	}
	for _, t := range v.synthTools {
		if keep[t.Name()] {
			out.synthTools = append(out.synthTools, t)
		}
	}
	for _, name := range v.synthRO {
		if keep[name] {
			out.synthRO = append(out.synthRO, name)
		}
	}
	// The observation snapshot is recomputed from the subset rather than
	// filtered by name so the identities and read targets are built exactly
	// as rebuildSkillViews builds them — one computation, no second spelling.
	for _, sk := range subset {
		if id := skillIdentityOf(sk); id.ID != "" {
			out.snapshot = append(out.snapshot, skillSnapshotEntry{
				identity: id, path: canonicalSkillPath(sk.Path), hidden: sk.DisableModelInvocation,
			})
		}
	}
	return out
}

// mcpCatalog assembles the admin MCP picker entries: every configured server
// with its live pool state. A server the pool has not connected yet reports
// its keeper state; a disabled one never connects and says so.
func (m *Manager) mcpCatalog() ([]capabilityMCPServer, error) {
	configured, err := m.loadMCPServerConfigs()
	if err != nil {
		return nil, err
	}
	statusByName := map[string]mcpclient.Status{}
	if m.mcpPool != nil {
		if _, _, statuses := m.mcpPool.Snapshot(); statuses != nil {
			for _, st := range statuses {
				statusByName[st.Name] = st
			}
		}
	}
	names := make([]string, 0, len(configured))
	for name := range configured {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]capabilityMCPServer, 0, len(names))
	for _, name := range names {
		sc := configured[name]
		transport := sc.Transport
		if sc.Command != "" {
			transport = "stdio"
		} else if transport == "" {
			transport = "streamable"
		}
		entry := capabilityMCPServer{
			ID: name, Name: name, Transport: transport, Enabled: sc.IsEnabled(),
			Status: "unknown",
		}
		if !entry.Enabled {
			entry.Status = "disabled"
		} else if st, ok := statusByName[name]; ok {
			entry.Status = st.State
			entry.ToolCount = st.Tools
		}
		out = append(out, entry)
	}
	return out, nil
}

// handleCapabilities serves the admin picker catalog. Both arrays are always
// present (never null): an empty deployment is a valid answer, and the UI
// should not need a null branch per section.
func (s *Server) handleCapabilities(w http.ResponseWriter, r *http.Request) {
	mcpEntries, err := s.mgr.mcpCatalog()
	if err != nil {
		writeErr(w, http.StatusServiceUnavailable, fmt.Errorf("无法读取 MCP 目录：%w", err))
		return
	}
	skillEntries, runtimeSkillCatalog := s.mgr.skillCapabilities()
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"skills":      skillEntries,
		"mcp_servers": mcpEntries,
		// The native half of the capability model: what the Agent editor's
		// built-in column is built from. Static per binary, so it comes from
		// the tools package's own catalog rather than a manager view.
		"builtin_tools":              tools.BuiltinCatalog(),
		"runtime_tools":              runtimeSkillCatalog.Tools,
		"skill_execution_configured": runtimeSkillCatalog.ExecutionConfigured,
	})
}

func (m *Manager) skillCapabilities() ([]any, skillruntime.SelectionCatalog) {
	toolCounts := map[string]int{}
	for _, info := range m.SynthCatalog() {
		toolCounts[info.Skill]++
	}
	skillEntries := []any{}
	for _, sk := range m.Skills() {
		id := skillIDOf(sk)
		if id == "" {
			continue
		}
		skillEntries = append(skillEntries, capabilitySkill{
			ID: id, Name: sk.Name, Aliases: sk.Aliases, Description: sk.Description,
			Hidden: sk.DisableModelInvocation, ToolCount: toolCounts[sk.Name],
			Builtin: sk.Source == "embedded", BuiltinRequired: sk.Source == "embedded" && !trustedNativeSkill(sk), NativeTools: nativeToolsForSkill(sk), LegacyNativeTools: legacyNativeToolsForSkill(sk), BrowserTask: sk.BrowserTask, Source: sk.Source, State: "available", Removable: m.removableSkill(sk),
		})
	}
	runtimeSkillCatalog := skillruntime.SelectionCatalog{Skills: []skillruntime.SkillEntry{}, Tools: []skillruntime.ToolEntry{}}
	if m.cfg.SkillRuntime != nil {
		runtimeSkillCatalog = m.cfg.SkillRuntime.SelectionCatalog()
		for _, entry := range runtimeSkillCatalog.Skills {
			skillEntries = append(skillEntries, entry)
		}
	}
	return skillEntries, runtimeSkillCatalog
}

// handleStudioSessionCapabilities reports effective model/Skill/native/MCP
// policies. Legacy definitions (arrays omitted)
// resolve against the live global catalog, so a pre-selection conversation
// keeps answering "everything you have" instead of retroactively reading as
// "nothing".
func (s *Server) handleStudioSessionCapabilities(w http.ResponseWriter, r *http.Request) {
	sid, aid := r.PathValue("sid"), r.PathValue("aid")
	b, ok := s.mgr.studio.binding(sid)
	if !ok || b.Agent.ID != aid || !s.ownsSession(r, sid) {
		writeErr(w, http.StatusNotFound, ErrNotFound)
		return
	}
	// Loading a dormant conversation reconciles its idle policies. Read the
	// binding afterwards, under the same lock as the effective runtime, so an
	// old creation snapshot cannot overwrite the just-applied selection.
	sess, err := s.mgr.Get(sid)
	if err != nil {
		writeErr(w, http.StatusNotFound, ErrNotFound)
		return
	}
	sess.mu.Lock()
	desired := b.Agent
	if current, target, err := s.mgr.studio.modelPolicyState(sid); err == nil {
		b, desired = current, target
	}
	privateSkills := slices.Clone(sess.invocationSkills)
	sess.mu.Unlock()
	b = b.effectiveSkillBinding()
	skillIDs := []string{}
	if b.Agent.Skills != nil {
		skillIDs = append(skillIDs, (*b.Agent.Skills)...)
	} else {
		for id := range s.mgr.skillCatalogIDs() {
			skillIDs = append(skillIDs, id)
		}
		sort.Strings(skillIDs)
	}
	knownSkills := s.mgr.skillCatalogIDs()
	retiredSkills := s.mgr.retiredPlatformSkillIDs()
	skillIDs = slices.DeleteFunc(skillIDs, func(id string) bool {
		sk, ok := knownSkills[id]
		return retiredSkills[id] || ok && !agentConversationSkill(sk)
	})
	inheritedMCP := []string{}
	if b.effectiveMCPServers() == nil {
		for name, sc := range s.mgr.mcpServerConfigs() {
			if sc.IsEnabled() {
				inheritedMCP = append(inheritedMCP, name)
			}
		}
	}
	mcpNames := mcpSelectionNames(b.effectiveMCPServers(), inheritedMCP)
	toolState, toolSync := s.mgr.effectiveStudioTools(sid, b)
	// Private instruction Skills live outside the shared Agent definition.
	// Report the captured effective set, including the old version while busy.
	var authored []skills.Skill
	for _, sk := range privateSkills {
		if sk.Source == "authored" {
			authored = append(authored, sk)
			if !slices.Contains(skillIDs, skillIDOf(sk)) {
				skillIDs = append(skillIDs, skillIDOf(sk))
			}
		}
	}
	toolNames := slices.Clone(toolState.Tools)
	if !s.browserAllowedRequest(r) {
		toolNames = slices.DeleteFunc(toolNames, func(name string) bool { return name == "browser" })
		browserSkills := map[string]bool{}
		for _, sk := range s.mgr.Skills() {
			if sk.BrowserTask {
				browserSkills[skillIDOf(sk)] = true
			}
		}
		skillIDs = slices.DeleteFunc(skillIDs, func(id string) bool { return browserSkills[id] })
	}
	w.Header().Set("Cache-Control", "no-store")
	model, modelList, modelRevision := s.mgr.effectiveStudioModel(sid, b)
	writeJSON(w, http.StatusOK, map[string]any{
		"model_sync":              true,
		"model_revision":          modelRevision,
		"models":                  modelList,
		"model":                   model,
		"skills":                  skillIDs,
		"skills_revision":         b.skillRevision(),
		"skills_desired_revision": desired.Revision,
		"skills_pending":          skillPolicyDiffers(b, withAuthoredSelection(desired, authored)),
		"mcp_servers":             mcpNames,
		"mcp_sync":                b.runtime() == studioWebRuntime,
		"mcp_revision":            b.mcpRevision(),
		"mcp_desired_revision":    desired.Revision,
		"mcp_pending":             b.runtime() == studioWebRuntime && mcpPolicyDiffers(b, desired),
		"tools":                   toolNames,
		"tools_sync":              toolSync,
		"tools_revision":          toolState.Revision,
		"tools_pending":           toolState.Deferred,
		"tools_desired_revision":  toolState.DesiredRevision,
	})
}

// errCapabilitySession is the read endpoints' closed answer: the session
// exists but the caller may not see it, so it looks like nothing at all.
var errCapabilitySession = errors.New("session not found")

// sessionSkillSet resolves the skills a conversation may see: the pinned
// Studio selection after any committed idle Skill update (legacy bindings inherit the
// live global set), the global set for an ordinary one. The second return
// says the session exists and is visible to this request.
func (s *Server) sessionSkillSet(r *http.Request, sid string) ([]skills.Skill, error) {
	if !validID(sid) || !s.ownsSession(r, sid) {
		return nil, errCapabilitySession
	}
	if b, bound := s.mgr.studio.binding(sid); bound {
		var liveRuntime *skillruntime.BoundSession
		var privateSkills []skills.Skill
		if sess, err := s.mgr.Get(sid); err == nil {
			sess.mu.Lock()
			if current, ok := s.mgr.studio.binding(sid); ok {
				b = current
			}
			liveRuntime = sess.runtimeSkill
			for _, sk := range sess.invocationSkills {
				if sk.Source == "authored" {
					privateSkills = append(privateSkills, sk)
				}
			}
			sess.mu.Unlock()
		}
		b = b.effectiveSkillBinding()
		var list []skills.Skill
		if b.Agent.Skills != nil {
			list = s.mgr.resolveSkillIDs(*b.Agent.Skills)
			if list == nil {
				list = []skills.Skill{}
			}
		} else {
			list = append(list, s.mgr.Skills()...)
		}
		list = appendAuthoredSkills(list, privateSkills)
		if len(b.SkillLock) > 0 && s.mgr.cfg.SkillRuntime != nil {
			// The live conversation already holds a validated BoundSession;
			// reuse it rather than minting a fresh one. Session() re-verifies
			// the whole installed package tree (Store.Load re-hashes every
			// file), which is seconds per call with multi-GB packages — far
			// too slow to pay on every dock-panel open. The in-memory copy
			// was validated when the conversation bound, and execution
			// re-checks revocation per call — but a package that was deleted
			// (forced or replaced) since must not stay visible, so only a
			// Live() copy serves the fast path.
			if liveRuntime != nil && liveRuntime.Live() {
				list = append(list, liveRuntime.Skills()...)
				return list, nil
			}
			runtime, err := s.mgr.cfg.SkillRuntime.Session(b.Agent.SkillBinding, b.SkillLock, sid)
			if err == nil {
				list = append(list, runtime.Skills()...)
			} else {
				// The pinned package can legitimately disappear — a forced
				// delete or a replace. The conversation loses those skills,
				// not its whole capability view.
				if s.opts.Logger != nil {
					s.opts.Logger.Printf("session %s runtime skills unavailable: %v", sid, err)
				}
			}
		}
		return list, nil
	}
	// An ordinary session has no selection of its own; it still must exist —
	// a made-up id gets the same closed answer as someone else's.
	if _, err := os.Stat(s.mgr.transcriptPath(sid)); err != nil {
		return nil, errCapabilitySession
	}
	return s.mgr.Skills(), nil
}

// sessionSynthCatalog is the same selection applied to the manifest tool
// catalog: a conversation's /api/tools view holds exactly what its skills
// declare. The MCP half of that response is pool projections, narrowed by
// sessionMCPOnly instead.
func (s *Server) sessionSynthCatalog(list []skills.Skill) []tools.SynthInfo {
	names := make(map[string]bool, len(list))
	for _, sk := range list {
		names[sk.Name] = true
	}
	out := []tools.SynthInfo{}
	for _, info := range s.mgr.SynthCatalog() {
		if names[info.Skill] {
			out = append(out, info)
		}
	}
	if s.mgr.cfg.SkillRuntime != nil {
		for _, sk := range list {
			if sk.Source != "bundle" || !strings.HasPrefix(sk.Path, "skill-runtime://") {
				continue
			}
			id := strings.TrimSuffix(strings.TrimPrefix(sk.Path, "skill-runtime://"), "/SKILL.md")
			for _, descriptor := range s.mgr.cfg.SkillRuntime.SkillTools(id) {
				out = append(out, tools.SynthInfo{Name: descriptor.Name, WireName: descriptor.Name, Title: descriptor.ID, Description: descriptor.Description, InputSchema: descriptor.InputSchema, ReadOnly: descriptor.ReadOnly, Skill: sk.Name, Registered: true, Renderer: "json"})
			}
		}
	}
	return out
}

// sessionMCPOnly is the MCP half of a conversation's /api/tools view: the
// effective Studio selection (nil map meaning the whole pool, a possibly-empty
// non-nil map meaning exactly those servers), the whole pool for an ordinary
// session. The visibility rule matches sessionSkillSet's.
func (s *Server) sessionMCPOnly(r *http.Request, sid string) (map[string]bool, error) {
	if !validID(sid) || !s.ownsSession(r, sid) {
		return nil, errCapabilitySession
	}
	if b, bound := s.mgr.studio.binding(sid); bound {
		return mcpSelectionOnly(b.effectiveMCPServers()), nil
	}
	// An ordinary session has no selection of its own; it still must exist —
	// a made-up id gets the same closed answer as someone else's.
	if _, err := os.Stat(s.mgr.transcriptPath(sid)); err != nil {
		return nil, errCapabilitySession
	}
	return nil, nil
}
