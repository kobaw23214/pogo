package web

// Authored instruction Skills are owner-and-Agent private data. They never
// enter Manager.Skills, the global upload directory, ordinary file roots, or a
// runtime package's executable capability catalog.
import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/skillauthor"
	"github.com/yosukeno/pogo/skills"
	"github.com/yosukeno/pogo/tools"
)

func (m *Manager) authoredSkillStore(owner *identity.Scope, aid string) (*skillauthor.Store, error) {
	if aid == "" || !validAgentDirName(aid) || m.cfg.SessionDir == "" || m.Isolation() && owner == nil {
		return nil, errors.New("当前 Agent 的 Skill 存储不可用")
	}
	scope, err := shortcutScopeID(owner, aid)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256([]byte("pogo-authored-skills-v1:" + scope))
	return skillauthor.New(filepath.Join(m.cfg.SessionDir, "authored-skills"), hex.EncodeToString(digest[:]))
}

func authoredSkillProjection(snapshot skillauthor.Snapshot) skills.Skill {
	// Store snapshots are detached copies. Capturing the complete version makes
	// an active run's document reads stable when another window saves an edit.
	read := func(relative string) ([]byte, bool, error) {
		clean, err := skills.DocumentPath(relative)
		if err != nil {
			return nil, false, err
		}
		for _, file := range snapshot.Files {
			if file.Path == clean {
				return []byte(file.Content), false, nil
			}
		}
		return nil, false, skills.ErrDocumentUnavailable
	}
	return skills.Skill{Name: snapshot.Name, Description: snapshot.Description,
		Source: "authored", Path: "skill-authored://" + snapshot.ID + "/SKILL.md", Revision: snapshot.Revision,
		ReadContent: func() ([]byte, error) { data, _, err := read("SKILL.md"); return data, err }, ReadDocument: read}
}

func authoredProjectionID(sk skills.Skill) string {
	if sk.Source != "authored" || !strings.HasPrefix(sk.Path, "skill-authored://") {
		return ""
	}
	return strings.TrimSuffix(strings.TrimPrefix(sk.Path, "skill-authored://"), "/SKILL.md")
}

func (m *Manager) authoredSkills(owner *identity.Scope, aid string) ([]skills.Skill, error) {
	if aid == "" {
		return nil, nil
	}
	store, err := m.authoredSkillStore(owner, aid)
	if err != nil {
		return nil, err
	}
	entries, err := store.List()
	if err != nil {
		return nil, err
	}
	list := []skills.Skill{}
	for _, entry := range entries {
		if entry.Enabled {
			list = append(list, authoredSkillProjection(entry))
		}
	}
	return list, nil
}

// An explicit Studio selection remains explicit. Private instruction Skills
// are an additional owner-local grant; they never modify the shared Agent
// definition or make its other tools/Skills available.
func withAuthoredSelection(a studioAgent, authored []skills.Skill) studioAgent {
	if a.Skills == nil {
		return a
	}
	ids := []string{}
	for _, id := range *a.Skills {
		if !strings.HasPrefix(id, "as_") {
			ids = append(ids, id)
		}
	}
	for _, sk := range authored {
		ids = append(ids, skillIDOf(sk))
	}
	a.Skills = &ids
	return a
}

func appendAuthoredSkills(base, authored []skills.Skill) []skills.Skill {
	list := slices.Clone(base)
	names := map[string]bool{}
	for _, sk := range list {
		names[sk.Name] = true
	}
	for _, sk := range authored {
		if !names[sk.Name] {
			list = append(list, sk)
			names[sk.Name] = true
		}
	}
	return list
}

type skillAuthorBackend struct {
	m                  *Manager
	s                  *Session
	owner              *identity.Scope
	agentID, workspace string
}

func (m *Manager) skillAuthorBackend(s *Session) *skillAuthorBackend {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.studioID == "" {
		return nil
	}
	b := &skillAuthorBackend{m: m, s: s, agentID: s.studioID, workspace: s.cwd}
	if s.owner != nil {
		copy := *s.owner
		b.owner = &copy
	}
	return b
}

func (b *skillAuthorBackend) access(ctx context.Context) (*skillauthor.Store, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	b.s.mu.Lock()
	sameOwner := b.owner == nil && b.s.owner == nil || b.owner != nil && b.s.owner != nil && *b.owner == *b.s.owner
	valid := sameOwner && b.agentID == b.s.studioID && b.workspace == b.s.cwd && !b.s.closing
	b.s.mu.Unlock()
	a, exists := b.m.studio.get(b.agentID)
	if !valid || !exists || a.Status != "ready" {
		return nil, errors.New("当前 Agent 会话已改变或不可用")
	}
	return b.m.authoredSkillStore(b.owner, b.agentID)
}

func authoredSummary(snapshot skillauthor.Snapshot) tools.SkillSummary {
	return tools.SkillSummary{ID: snapshot.ID, Name: snapshot.Name, Description: snapshot.Description,
		Revision: snapshot.Revision, Version: snapshot.Version, Editable: true, Enabled: snapshot.Enabled, Source: "authored"}
}

func (b *skillAuthorBackend) selected() []skills.Skill {
	b.s.mu.Lock()
	defer b.s.mu.Unlock()
	return slices.Clone(b.s.invocationSkills)
}

func (b *skillAuthorBackend) Search(ctx context.Context, in tools.SkillSearchRequest) (tools.SkillSearchResult, error) {
	store, err := b.access(ctx)
	if err != nil {
		return tools.SkillSearchResult{}, err
	}
	entries, err := store.List()
	if err != nil {
		return tools.SkillSearchResult{}, err
	}
	all := map[string]tools.SkillSummary{}
	for _, sk := range b.selected() {
		id := skillIDOf(sk)
		all[id] = tools.SkillSummary{ID: id, Name: sk.Name, Description: sk.Description, Enabled: true, Source: sk.Source}
	}
	for _, entry := range entries {
		all[entry.ID] = authoredSummary(entry)
	}
	query := strings.ToLower(strings.TrimSpace(in.Query))
	rows := []tools.SkillSummary{}
	for _, row := range all {
		if query == "" || strings.Contains(strings.ToLower(row.Name+" "+row.Description), query) {
			rows = append(rows, row)
		}
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Name < rows[j].Name })
	if in.Limit == 0 {
		in.Limit = 30
	}
	if in.Offset < 0 || in.Offset > 10000 || in.Limit < 1 || in.Limit > 100 || len(in.Query) > 1024 {
		return tools.SkillSearchResult{}, errors.New("Skill 搜索参数无效")
	}
	start := min(in.Offset, len(rows))
	end := min(start+in.Limit, len(rows))
	return tools.SkillSearchResult{Skills: rows[start:end], Offset: in.Offset, Total: len(rows), HasMore: end < len(rows)}, nil
}

func authoredReadResult(entry skillauthor.Snapshot, relative string) (tools.SkillReadResult, error) {
	if relative == "" {
		relative = "SKILL.md"
	}
	clean, err := skills.DocumentPath(relative)
	if err != nil {
		return tools.SkillReadResult{}, err
	}
	result := tools.SkillReadResult{Skill: authoredSummary(entry), Path: clean, Revision: entry.Revision, Files: []tools.SkillFileInfo{}}
	found := false
	for _, file := range entry.Files {
		result.Files = append(result.Files, tools.SkillFileInfo{Path: file.Path, SizeBytes: len(file.Content)})
		if file.Path == clean {
			result.Content = file.Content
			found = true
		}
	}
	if !found {
		return result, skillauthor.ErrNotFound
	}
	return result, nil
}

func (b *skillAuthorBackend) Read(ctx context.Context, in tools.SkillReadRequest) (tools.SkillReadResult, error) {
	store, err := b.access(ctx)
	if err != nil {
		return tools.SkillReadResult{}, err
	}
	if in.Path == "" {
		in.Path = "SKILL.md"
	}
	clean, err := skills.DocumentPath(in.Path)
	if err != nil {
		return tools.SkillReadResult{}, err
	}
	// A normal activation reads the version captured for the current run.
	// Editing starts from skill_search's explicit current revision instead.
	if in.Revision == "" {
		for _, sk := range b.selected() {
			if sk.Name != in.Skill && skillIDOf(sk) != in.Skill {
				continue
			}
			if sk.Source == "authored" {
				entry, err := store.Read(skillIDOf(sk), sk.Revision)
				if err != nil {
					return tools.SkillReadResult{}, err
				}
				return authoredReadResult(entry, clean)
			}
			data, truncated, err := skills.ReadDocument(sk, clean)
			if err != nil {
				return tools.SkillReadResult{}, err
			}
			if truncated {
				return tools.SkillReadResult{}, errors.New("Skill 文档超过读取上限")
			}
			return tools.SkillReadResult{Skill: tools.SkillSummary{ID: skillIDOf(sk), Name: sk.Name, Description: sk.Description, Enabled: true, Source: sk.Source}, Content: string(data), Path: clean, Files: []tools.SkillFileInfo{}}, nil
		}
	}
	entries, err := store.List()
	if err != nil {
		return tools.SkillReadResult{}, err
	}
	for _, entry := range entries {
		if entry.ID == in.Skill || entry.Name == in.Skill {
			if in.Revision != "" {
				entry, err = store.Read(entry.ID, in.Revision)
				if err != nil {
					return tools.SkillReadResult{}, err
				}
			}
			return authoredReadResult(entry, clean)
		}
	}
	return tools.SkillReadResult{}, skillauthor.ErrNotFound
}

func (m *Manager) checkAuthoredSkillName(name string) error {
	for _, sk := range m.Skills() {
		if sk.Name == name || slices.Contains(sk.Aliases, name) {
			return errors.New("此名称已由已安装的 Skill 使用，请为新 Skill 选择其他名称")
		}
	}
	if m.cfg.SkillRuntime != nil {
		for _, entry := range m.cfg.SkillRuntime.Catalog() {
			if entry.Skill == name {
				return errors.New("此名称已由运行时 Skill 使用")
			}
		}
	}
	return nil
}

func (m *Manager) syncAuthoredSkillSessions(owner *identity.Scope, aid string) {
	m.mu.Lock()
	list := []*Session{}
	for _, s := range m.sessions {
		if s.studioID == aid && (owner == nil && s.owner == nil || owner != nil && s.owner != nil && *owner == *s.owner) {
			list = append(list, s)
		}
	}
	m.mu.Unlock()
	for _, s := range list {
		_, _ = s.SyncStudioSkillPolicy()
	}
}

func (b *skillAuthorBackend) Save(ctx context.Context, in skillauthor.SaveRequest) (tools.SkillSaveResult, error) {
	a, ok := b.m.studio.get(b.agentID)
	if !ok || !b.m.studioNativeSelected(a, tools.SkillSaveName) {
		return tools.SkillSaveResult{}, errors.New("Skill 创建能力已停用，请在 Agent 配置中启用 skill-creator")
	}
	store, err := b.access(ctx)
	if err != nil {
		return tools.SkillSaveResult{}, err
	}
	name, _, err := skillauthor.ValidateFiles(in.Files)
	if err != nil {
		return tools.SkillSaveResult{}, err
	}
	b.m.studio.runtimeSkillEdits.Lock()
	err = b.m.checkAuthoredSkillName(name)
	var saved skillauthor.SaveResult
	if err == nil {
		saved, err = store.Save(ctx, in)
	}
	b.m.studio.runtimeSkillEdits.Unlock()
	if err != nil {
		return tools.SkillSaveResult{}, err
	}
	b.m.syncAuthoredSkillSessions(b.owner, b.agentID)
	b.s.mu.Lock()
	pending := false
	matched := false
	for _, sk := range b.s.invocationSkills {
		if skillIDOf(sk) == saved.Skill.ID {
			matched = sk.Revision == saved.Skill.Revision
			break
		}
	}
	pending = saved.Skill.Enabled && !matched
	if !saved.Skill.Enabled {
		for _, sk := range b.s.invocationSkills {
			if skillIDOf(sk) == saved.Skill.ID {
				pending = true
			}
		}
	}
	b.s.mu.Unlock()
	return tools.SkillSaveResult{Type: "skill_authoring", Action: "save", Saved: true, AgentID: b.agentID,
		Skill: authoredSummary(saved.Skill), RequestID: saved.RequestID, Reused: saved.Reused, Enabled: saved.Skill.Enabled, Pending: pending, BehaviorTested: false}, nil
}

// File trees contain relative metadata only. The same immutable version backs
// root content, nested documents and exports, including historical previews.
func authoredFileList(entry skillauthor.Snapshot, relative string) (skills.FileList, error) {
	result := skills.FileList{RelativePath: relative, Entries: []skills.FileEntry{}, Limits: skills.FileListLimits{MaxEntries: skills.FileListMaxEntries, MaxDepth: skills.FileListMaxDepth}}
	if relative != "" {
		clean, err := skills.DocumentPath(relative)
		if err != nil {
			return result, err
		}
		relative = clean
		result.RelativePath = clean
	}
	prefix := ""
	if relative != "" {
		prefix = relative + "/"
	}
	seen := map[string]bool{}
	for _, file := range entry.Files {
		if !strings.HasPrefix(file.Path, prefix) {
			continue
		}
		rest := strings.TrimPrefix(file.Path, prefix)
		component, _, directory := strings.Cut(rest, "/")
		if seen[component] {
			continue
		}
		seen[component] = true
		row := skills.FileEntry{Name: component, Path: path.Join(relative, component)}
		if directory {
			row.Kind = "directory"
			row.Expandable = true
		} else {
			row.Kind = "file"
			row.Previewable = true
			row.SizeBytes = int64(len(file.Content))
			row.Language, row.MediaType = skills.DocumentFormat(file.Path)
		}
		result.Entries = append(result.Entries, row)
	}
	if relative != "" && len(result.Entries) == 0 {
		return result, skillauthor.ErrNotFound
	}
	sort.Slice(result.Entries, func(i, j int) bool {
		a, b := result.Entries[i], result.Entries[j]
		if a.Kind != b.Kind {
			return a.Kind == "directory"
		}
		return a.Name < b.Name
	})
	return result, nil
}

func authoredSkillError(err error) (int, error) {
	switch {
	case errors.Is(err, skillauthor.ErrNotFound):
		return 404, err
	case errors.Is(err, skillauthor.ErrConflict):
		return 409, errors.New("Skill 已被其他窗口或 Agent 修改，请重新加载后再保存；当前编辑内容仍可保留")
	case errors.Is(err, skillauthor.ErrNameConflict):
		return 409, errors.New("当前 Agent 已有同名 Skill，请读取后更新，或使用其他名称")
	case errors.Is(err, skillauthor.ErrRequestConflict):
		return 409, errors.New("此请求编号已用于其他内容，请保留当前内容并使用新的请求编号")
	case errors.Is(err, skillauthor.ErrLimit):
		return 413, err
	case errors.Is(err, skillauthor.ErrInvalid):
		return 400, err
	default:
		return 500, errors.New("Skill 存储暂不可用；原内容保持不变，请稍后重试")
	}
}

func authoredRequestDigest(id, revision, content string) string {
	data, _ := json.Marshal([]string{id, revision, content})
	digest := sha256.Sum256(data)
	return "editor-" + hex.EncodeToString(digest[:])
}
