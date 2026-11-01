package web

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/skills"
	"github.com/yosukeno/pogo/tools"
)

func bindingTestSkills(t *testing.T) []skills.Skill {
	t.Helper()
	dirs, err := skills.MaterializeEmbedded(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	list, diagnostics := skills.Load(skills.Options{Embedded: dirs, UserDir: t.TempDir()})
	if len(diagnostics) > 0 {
		t.Fatalf("embedded fixture diagnostics: %+v", diagnostics)
	}
	return list
}
func bindingSkillID(t *testing.T, list []skills.Skill, name string) string {
	t.Helper()
	for _, sk := range list {
		if sk.Name == name && sk.Source == "embedded" {
			return skillIDOf(sk)
		}
	}
	t.Fatalf("missing trusted %s", name)
	return ""
}
func bindingStrings(values ...string) *[]string { out := append([]string{}, values...); return &out }

func TestStudioSkillToolBindingTrustedSelectionAndNoStandaloneGrant(t *testing.T) {
	list := bindingTestSkills(t)
	m := &Manager{cfg: Config{Skills: list}}
	id := bindingSkillID(t, list, "agent-workflows")
	in := studioInput{Skills: bindingStrings(id), Tools: bindingStrings("write", tools.BackgroundTaskName), SkillToolBindings: []string{"agent-workflows"}}
	if err := m.normalizeStudioSkillTools(&in, nil); err != nil {
		t.Fatal(err)
	}
	if len(in.SkillToolBindings) != 4 || !tools.BuiltinSelected(in.Tools, "write") || tools.BuiltinSelected(in.Tools, tools.BackgroundTaskName) {
		t.Fatalf("bad normalized selection: %+v", in)
	}
	for _, name := range tools.SkillBindingTools("agent-workflows") {
		if !tools.BuiltinSelected(in.Tools, name) {
			t.Fatalf("missing associated %s", name)
		}
	}
	// A same-name externally supplied Skill and an explicit native name cannot
	// impersonate the host's trusted binding.
	external := list[slices.IndexFunc(list, func(sk skills.Skill) bool { return sk.Name == "agent-workflows" })]
	external.Source = "user"
	m.cfg.Skills = []skills.Skill{external}
	bad := studioInput{Skills: bindingStrings(skillIDOf(external)), Tools: bindingStrings(tools.AgentWorkflowEditName)}
	if err := m.normalizeStudioSkillTools(&bad, nil); err != nil {
		t.Fatal(err)
	}
	if tools.BuiltinSelected(bad.Tools, tools.AgentWorkflowEditName) {
		t.Fatal("external Skill minted native grant")
	}
	// Revoking/replacing a trusted catalog entry also closes an already-saved
	// managed grant on restore and at host dispatch.
	a := studioAgent{studioInput: in}
	if m.studioNativeSelected(a, tools.AgentWorkflowEditName) {
		t.Fatal("revoked trusted Skill retained execution rights")
	}
}

func TestStudioSkillToolBindingLegacyPartialAndMonotoneAdoption(t *testing.T) {
	list := bindingTestSkills(t)
	m := &Manager{cfg: Config{Skills: list}}
	id := bindingSkillID(t, list, "agent-workflows")
	old := studioAgent{studioInput: studioInput{Name: "legacy", Skills: bindingStrings(id), Tools: bindingStrings("write", tools.AgentWorkflowReadName)}}
	before, _ := json.Marshal(old)
	unchanged := old.studioInput
	unchanged.Name = "renamed"
	if err := m.normalizeStudioSkillTools(&unchanged, &old); err != nil {
		t.Fatal(err)
	}
	if len(unchanged.SkillToolBindings) != 0 || !slices.Equal(*unchanged.Tools, *old.Tools) {
		t.Fatalf("unrelated edit expanded legacy grants: %+v", unchanged)
	}
	direct := old.studioInput
	direct.Tools = bindingStrings("write", tools.AgentWorkflowEditName)
	if err := m.normalizeStudioSkillTools(&direct, &old); err == nil {
		t.Fatal("standalone legacy native change accepted")
	}
	adopt := old.studioInput
	adopt.SkillToolBindings = []string{"agent-workflows"}
	if err := m.normalizeStudioSkillTools(&adopt, &old); err != nil {
		t.Fatal(err)
	}
	if !tools.BuiltinSelected(adopt.Tools, tools.AgentWorkflowRunName) {
		t.Fatal("explicit association was not granted")
	}
	managed := studioAgent{studioInput: adopt}
	disabled := adopt
	disabled.Skills = bindingStrings()
	disabled.SkillToolBindings = nil
	if err := m.normalizeStudioSkillTools(&disabled, &managed); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(disabled.SkillToolBindings, "agent-workflows") || tools.BuiltinSelected(disabled.Tools, tools.AgentWorkflowReadName) || !tools.BuiltinSelected(disabled.Tools, "write") {
		t.Fatalf("managed association was downgraded or unrelated tool lost: %+v", disabled)
	}
	after, _ := json.Marshal(old)
	if string(before) != string(after) {
		t.Fatal("normalization mutated original legacy snapshot")
	}
}

func TestStudioSkillToolBindingBackgroundLegacyAliasesAreNotExpanded(t *testing.T) {
	list := bindingTestSkills(t)
	m := &Manager{cfg: Config{Skills: list}}
	id := bindingSkillID(t, list, "background-tasks")
	old := studioAgent{studioInput: studioInput{Skills: bindingStrings(id), Tools: bindingStrings(tools.WatchTestName)}}
	noChange := old.studioInput
	if err := m.normalizeStudioSkillTools(&noChange, &old); err != nil {
		t.Fatal(err)
	}
	if tools.BuiltinSelected(noChange.Tools, tools.BackgroundTaskName) {
		t.Fatal("legacy script check became full scheduler authority")
	}
	adopted := old.studioInput
	adopted.SkillToolBindings = []string{"background-tasks"}
	if err := m.normalizeStudioSkillTools(&adopted, &old); err != nil {
		t.Fatal(err)
	}
	if !tools.BuiltinSelected(adopted.Tools, tools.BackgroundTaskName) || tools.BuiltinSelected(adopted.Tools, tools.WatchTestName) || tools.BuiltinSelected(adopted.Tools, tools.WatchName) {
		t.Fatal("adoption did not consolidate the background entry")
	}
}

func TestStudioSkillToolBindingRuntimeSyncAndBackendRevocation(t *testing.T) {
	list := bindingTestSkills(t)
	h := newHarness(t, func(int) (llm.Response, error) {
		t.Fatal("binding check must not invoke model")
		return llm.Response{}, nil
	}, func(c *Config) {
		c.Skills = list
		var err error
		c.Cwd, err = filepath.EvalSymlinks(c.Cwd)
		if err != nil {
			t.Fatal(err)
		}
		c.SessionDir, err = filepath.EvalSymlinks(c.SessionDir)
		if err != nil {
			t.Fatal(err)
		}
	})
	id := bindingSkillID(t, list, "agent-workflows")
	a := postStudioAgent(t, h, studioAgentCapsBody("Linked workflow", "ready", map[string]any{"skills": []string{id}, "tools": []string{"write"}, "skill_tool_bindings": []string{"agent-workflows"}}), http.StatusCreated)
	if len(a.SkillToolBindings) != 4 {
		t.Fatalf("new definition lacks mandatory association rules: %+v", a.SkillToolBindings)
	}
	sid := startStudioConversation(t, h, a.ID)
	session := h.session(sid)
	if !slices.Contains(session.agent.NativeToolNames(), tools.AgentWorkflowEditName) {
		t.Fatal("associated workflow tool was not registered")
	}
	backend := h.mgr.agentWorkflowToolBackend(session)
	if _, err := backend.AgentWorkflowCall(context.Background(), "read", json.RawMessage(`{"action":"capabilities"}`)); err != nil {
		t.Fatal(err)
	}
	changed := a.studioInput
	changed.Skills = bindingStrings()
	changed.SkillToolBindings = nil // simulate stale client omitting the new field
	saved, err := h.mgr.studio.put(a.ID, changed, a.Revision)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(saved.SkillToolBindings, "agent-workflows") {
		t.Fatal("stale update erased binding rule")
	}
	if _, err := backend.AgentWorkflowCall(context.Background(), "read", json.RawMessage(`{"action":"capabilities"}`)); err == nil {
		t.Fatal("retained backend ignored revocation")
	}
	if _, err := session.SyncStudioSkillPolicy(); err != nil {
		t.Fatal(err)
	}
	if _, err := session.SyncStudioToolPolicy(); err != nil {
		t.Fatal(err)
	}
	for _, name := range session.agent.NativeToolNames() {
		if strings.HasPrefix(name, "agent_workflow_") {
			t.Fatalf("live registry retained revoked %s", name)
		}
	}
	b, ok := h.mgr.studio.binding(sid)
	if !ok || b.ToolPolicy == nil || !slices.Contains(b.ToolPolicy.SkillToolBindings, "agent-workflows") {
		t.Fatal("effective association did not persist with native policy")
	}
	reopened, err := openStudioStore(h.mgr.cfg.SessionDir)
	if err != nil {
		t.Fatal(err)
	}
	restored, ok := reopened.binding(sid)
	if !ok || tools.BuiltinSelected(h.mgr.studioNativeSelection(restored.effectiveNativeAgent()), tools.AgentWorkflowEditName) {
		t.Fatal("restored binding regained revoked native tool")
	}
	if h.calls() != 0 {
		t.Fatal("unexpected model call")
	}
}

func TestStudioSkillToolBindingLegacyInheritedPickerSaveRetainsHiddenGrants(t *testing.T) {
	list := bindingTestSkills(t)
	m := &Manager{cfg: Config{Skills: list}}
	old := studioAgent{studioInput: studioInput{Name: "legacy inherited"}}
	selectedSkills := []string{}
	for _, sk := range m.Skills() {
		selectedSkills = append(selectedSkills, skillIDOf(sk))
	}
	selectedTools := []string{}
	for _, entry := range tools.BuiltinCatalog() {
		if entry.Name != "write" {
			selectedTools = append(selectedTools, entry.Name)
		}
	}
	in := studioInput{Name: "rename and disable general write", Skills: &selectedSkills, Tools: &selectedTools}
	if err := m.normalizeStudioSkillTools(&in, &old); err != nil {
		t.Fatal(err)
	}
	if len(in.SkillToolBindings) != 0 || tools.BuiltinSelected(in.Tools, "write") || !tools.BuiltinSelected(in.Tools, tools.WatchName) || !tools.BuiltinSelected(in.Tools, tools.WatchTestName) {
		t.Fatalf("legacy picker save narrowed/expanded a bound group: %+v", in)
	}
}

func TestStudioSkillToolBindingCapabilityMetadataSeparatesSystemAndSelectable(t *testing.T) {
	list := bindingTestSkills(t)
	m := &Manager{cfg: Config{Skills: list}}
	entries, _ := m.skillCapabilities()
	found := map[string]bool{}
	for _, raw := range entries {
		entry, ok := raw.(capabilitySkill)
		if !ok {
			continue
		}
		if slices.Contains(tools.SkillBindingNames(), entry.Name) {
			found[entry.Name] = true
			if !entry.Builtin || entry.BuiltinRequired || !slices.Equal(entry.NativeTools, tools.SkillBindingTools(entry.Name)) {
				t.Fatalf("incorrect selectable binding metadata: %+v", entry)
			}
		} else if entry.Builtin && !entry.BuiltinRequired {
			t.Fatalf("unrelated system Skill lost required flag: %+v", entry)
		}
	}
	if len(found) != 4 {
		t.Fatalf("missing binding groups: %+v", found)
	}
}

func TestStudioSkillToolBindingStaleClientCannotTurnGuidanceIntoExtraRights(t *testing.T) {
	list := bindingTestSkills(t)
	m := &Manager{cfg: Config{Skills: list}}
	id := bindingSkillID(t, list, "agent-workflows")
	for _, existing := range []bool{false, true} {
		t.Run(map[bool]string{false: "new definition", true: "legacy Skill toggle"}[existing], func(t *testing.T) {
			var previous *studioAgent
			if existing {
				previous = &studioAgent{studioInput: studioInput{Skills: bindingStrings(), Tools: bindingStrings("write", tools.AgentWorkflowReadName)}}
			}
			in := studioInput{Skills: bindingStrings(id), Tools: bindingStrings("write", tools.AgentWorkflowReadName)}
			before, _ := json.Marshal(in)
			if err := m.normalizeStudioSkillTools(&in, previous); err == nil {
				t.Fatal("old client expanded an auto-selected guide into execute/enable rights")
			}
			if tools.BuiltinSelected(in.Tools, tools.AgentWorkflowRunName) {
				t.Fatal("rejection mutated requested native grants")
			}
			acknowledged := studioInput{Skills: bindingStrings(id), Tools: bindingStrings("write", tools.AgentWorkflowReadName), SkillToolBindings: []string{"agent-workflows"}}
			if err := m.normalizeStudioSkillTools(&acknowledged, previous); err != nil {
				t.Fatal(err)
			}
			if !tools.BuiltinSelected(acknowledged.Tools, tools.AgentWorkflowRunName) {
				t.Fatal("explicit association did not enable its documented tools")
			}
			managed := studioAgent{studioInput: acknowledged}
			stale := studioInput{Skills: bindingStrings(id), Tools: bindingStrings("write", tools.AgentWorkflowReadName)}
			if err := m.normalizeStudioSkillTools(&stale, &managed); err == nil {
				t.Fatal("managed group silently restored a tool explicitly disabled by a stale client")
			}
			matched := acknowledged
			matched.SkillToolBindings = nil
			if err := m.normalizeStudioSkillTools(&matched, &managed); err != nil {
				t.Fatalf("unchanged existing grant failed without new field: %v", err)
			}
			if !slices.Contains(matched.SkillToolBindings, "agent-workflows") {
				t.Fatal("stale matched update downgraded managed rule")
			}
			var original studioInput
			if err := json.Unmarshal(before, &original); err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(*original.Tools, *in.Tools) {
				t.Fatal("rejected adoption changed source tools")
			}
		})
	}
}
