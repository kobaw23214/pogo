package tools

import "fmt"

var optionalNativeNames = []string{ShortcutManageName, ShortcutDeleteName, ShortcutMergeName, SkillSaveName, WatchName, WatchTestName, WatchDecideName, BackgroundTaskName, AgentWorkflowReadName, AgentWorkflowEditName, AgentWorkflowEnableName, AgentWorkflowRunName, "write", "edit", "bash", ShellTasksName, ShellOutputName, ShellStopName, "browser"}

// HostNativeTool also finds a temporarily withheld host capability so model
// metadata can stay current without registering it or granting its execution.
func (r *Registry) HostNativeTool(name string) (Tool, bool) {
	if tool, ok := r.nativeTools[name]; ok {
		return tool, true
	}
	return r.Get(name)
}

// nativeToolInstances keeps host-issued instances, including capabilities that
// were initially withheld. Merely constructing these tools performs no action.
func nativeToolInstances(o Options, writeRoots []string, spill string, r *Registry) map[string]Tool {
	available := map[string]Tool{}
	for _, tool := range NewAgentWorkflowTools(o.AgentWorkflows) {
		if !o.ReadOnly || tool.(ReadOnlyCapable).IsReadOnly() {
			available[tool.Name()] = tool
		}
	}
	if !o.ReadOnly {
		if o.BackgroundTasks != nil {
			available[BackgroundTaskName] = &BackgroundTask{Backend: o.BackgroundTasks}
		}
		if o.Watches != nil {
			for _, tool := range NewWatchTools(o.Watches) {
				available[tool.Name()] = tool
			}
		}
		available["write"] = &Write{Cwd: o.Cwd, Roots: writeRoots, Journal: o.Journal, ProtectionManifest: o.CommandProtectionManifest}
		available["edit"] = &Edit{Cwd: o.Cwd, Roots: writeRoots, Journal: o.Journal, ProtectionManifest: o.CommandProtectionManifest}
		available["bash"] = &Bash{Cwd: o.Cwd, Guard: o.Guard, SpillDir: spill, Scope: o.Scope, ProtectionManifest: o.CommandProtectionManifest, Jobs: o.ShellJobs}
		for _, tool := range shellJobTools(o.ShellJobs) {
			available[tool.Name()] = tool
		}
		if shortcutBackendAvailable(o.Shortcuts) {
			available[ShortcutCreateName] = &ShortcutCreate{Backend: o.Shortcuts}
		}
		if management := shortcutManagementFor(o); management != nil {
			for _, tool := range managementMutationTools(management) {
				available[tool.Name()] = tool
			}
		}
		if shortcutBackendAvailable(o.Shortcuts) || shortcutManagementFor(o) != nil {
			available[ShortcutManageName] = &ShortcutManage{Backend: o.Shortcuts, Management: shortcutManagementFor(o)}
		}
		if skillAuthorBackendAvailable(o.SkillAuthor) {
			available[SkillSaveName] = &SkillSave{Backend: o.SkillAuthor}
		}
	}
	if o.Browser != nil {
		mode := o.BrowserImageMode
		if mode == BrowserImageNone && o.Images != nil {
			mode = BrowserImageHelper
		}
		available["browser"] = &Browser{Link: o.Browser, SpillDir: spill, ImageMode: mode}
	}
	for name, tool := range available {
		// Keep the unfiltered host group as a capability template so a later
		// selection update can add or remove actions without rebuilding stores.
		if name != ShortcutManageName && o.keeps(name) {
			for _, existing := range r.order {
				if existing.Name() == name {
					available[name] = existing
					break
				}
			}
			if available[name] != tool {
				continue
			}
		}
		if o.Information != nil && o.Information.Store != nil {
			tool = protectInformationFileTool(tool, o.Cwd, o.Information.Store.Root())
		}
		available[name] = tool
	}
	return available
}

// WithNativeSelection replaces only optional host built-ins. Stateful domain,
// coordination and MCP tools keep their objects; no selected name can invent a
// capability for which this registry has no host-issued implementation.
func (r *Registry) WithNativeSelection(enabled *[]string, childBrowser BrowserLink) (*Registry, func(), error) {
	if r.nativeTools == nil {
		return nil, nil, fmt.Errorf("native tool updates unavailable for this conversation")
	}
	opts := Options{Enabled: enabled, SentinelEvent: r.sentinelEvent}
	base := make([]Tool, 0, len(r.order))
	taken := map[string]bool{}
	start := 0
	var child *Subagent
	for i, tool := range r.order {
		if i == r.mcpStart {
			start = len(base)
		}
		if native, ok := r.nativeTools[tool.Name()]; ok {
			_, grouped := tool.(*ShortcutManage)
			if native == tool || grouped && tool.Name() == ShortcutManageName {
				continue
			}
		}
		base = append(base, tool)
		taken[tool.Name()] = true
		if v, ok := tool.(*Subagent); ok {
			child = v
		}
	}
	if r.mcpStart >= len(r.order) {
		start = len(base)
	}
	insert := []Tool{}
	for _, name := range optionalNativeNames {
		if !opts.keeps(name) {
			continue
		}
		tool, ok := r.nativeTools[name]
		if !ok {
			continue
		}
		if manage, grouped := tool.(*ShortcutManage); grouped {
			tool = manage.withSelection(enabled)
			if len(tool.(*ShortcutManage).actionTools()) == 0 {
				continue
			}
		}
		if taken[name] {
			return nil, nil, fmt.Errorf("native tool conflicts with existing tool %q", name)
		}
		insert = append(insert, tool)
	}
	all := append(append(append([]Tool(nil), base[:start]...), insert...), base[start:]...)
	next := NewRegistry(all...)
	next.mcpStart, next.readOnly, next.spillDir = start+len(insert), r.readOnly, r.spillDir
	next.results, next.scopedOutputRoot, next.nativeTools = r.results, r.scopedOutputRoot, r.nativeTools
	next.sentinelEvent = r.sentinelEvent
	next.resultDelivery = CloneResultDeliveryPolicy(r.resultDelivery)
	apply := func() {
		if child != nil {
			child.Enabled = cloneNativeSelection(enabled)
			child.Browser = nil
			if opts.keeps("browser") {
				child.Browser = childBrowser
			}
		}
	}
	return next, apply, nil
}

func cloneNativeSelection(value *[]string) *[]string {
	if value == nil {
		return nil
	}
	copy := append([]string{}, (*value)...)
	return &copy
}
