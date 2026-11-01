package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	af "github.com/yosukeno/pogo/agentworkflow"
	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/inputref"
	"github.com/yosukeno/pogo/session"
	"github.com/yosukeno/pogo/skillruntime"
	"github.com/yosukeno/pogo/skillruntime/core"
	"github.com/yosukeno/pogo/skills"
)

const awResourceLimit = 16 << 20

type awSkillResource struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Version     string `json:"version"`
	Available   bool   `json:"available"`
	Reason      string `json:"reason,omitempty"`
}
type awShortcutResource struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Source    string `json:"source"`
	Version   string `json:"version"`
	Available bool   `json:"available"`
	Reason    string `json:"reason,omitempty"`
}
type awResourceFileInfo struct {
	Path string `json:"path"`
	Name string `json:"name"`
	Size int64  `json:"size"`
}
type awResourceCatalog struct {
	Skills          []awSkillResource    `json:"skills"`
	Shortcuts       []awShortcutResource `json:"shortcuts"`
	Files           []awResourceFileInfo `json:"files"`
	FilesTruncated  bool                 `json:"files_truncated"`
	FileLimit       int                  `json:"file_limit"`
	SourceSessionID string               `json:"source_session_id,omitempty"`
	FilesReason     string               `json:"files_reason,omitempty"`
}

func awResourceHash(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func awSkillResourceReason(sk skills.Skill) string {
	if sk.Source == "bundle-unavailable" {
		return "运行时Skill授权、安装状态或能力绑定已不可用"
	}
	switch sk.Name {
	case "agent-workflows", "background-tasks", "workflow-authoring":
		return "内部工作流节点不开放工作流管理、后台调度或平台工作流能力"
	}
	if sk.BrowserTask {
		return "浏览器任务需要交互式会话授权，暂不能绑定到内部工作流节点"
	}
	if sk.DisableModelInvocation {
		return "该Skill要求用户显式交互调用，不能自动在节点执行"
	}
	return ""
}
func (m *Manager) awResourceSkills(owner *identity.Scope, a studioAgent) (map[string]skills.Skill, error) {
	list, _ := m.studioSelection(a)
	if list == nil {
		list = m.Skills()
	}
	var authored []skills.Skill
	if _, statErr := os.Lstat(filepath.Join(m.cfg.SessionDir, "authored-skills")); statErr == nil {
		var err error
		authored, err = m.authoredSkills(owner, a.ID)
		if err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(statErr) {
		return nil, statErr
	}
	list = appendAuthoredSkills(list, authored)
	out := map[string]skills.Skill{}
	for _, sk := range list {
		if agentConversationSkill(sk) {
			if id := skillIDOf(sk); id != "" {
				out[id] = sk
			}
		}
	}
	selected := map[string]bool{}
	if a.Skills != nil {
		for _, id := range *a.Skills {
			if strings.HasPrefix(id, "rs_") {
				selected[id] = true
			}
		}
	}
	if m.cfg.SkillRuntime != nil && len(a.SkillBinding) > 0 && m.cfg.SkillRuntime.ValidateBinding(a.SkillBinding) == nil {
		var binding core.Binding
		if json.Unmarshal(a.SkillBinding, &binding) == nil {
			for _, ref := range binding.Packages {
				selected[skillruntime.PackageSkillID(ref)] = true
			}
		}
	}
	if m.cfg.SkillRuntime != nil {
		bindingReady := len(a.SkillBinding) > 0 && m.cfg.SkillRuntime.ValidateBinding(a.SkillBinding) == nil
		for _, installed := range m.cfg.SkillRuntime.Catalog() {
			id := skillruntime.PackageSkillID(installed.Ref)
			if !selected[id] {
				continue
			}
			delete(selected, id)
			read := func(relative string) ([]byte, bool, error) {
				if installed.Revoked || len(a.SkillBinding) == 0 || m.cfg.SkillRuntime.ValidateBinding(a.SkillBinding) != nil {
					return nil, false, errors.New("运行时Skill授权或绑定已不可用")
				}
				return m.cfg.SkillRuntime.SkillDocument(id, relative)
			}
			name := installed.Skill
			if name == "" {
				name = installed.Name
			}
			source := "bundle"
			if installed.Revoked || !bindingReady {
				source = "bundle-unavailable"
			}
			out[id] = skills.Skill{Name: name, Description: installed.Name, Revision: installed.Ref.Version + ":" + installed.Ref.PackageDigest, Source: source, Path: "skill-runtime://" + id + "/SKILL.md", ReadDocument: read}
		}
	}
	for id := range selected {
		out[id] = skills.Skill{Name: id, Source: "bundle-unavailable", Path: "skill-runtime://" + id + "/SKILL.md", ReadDocument: func(string) ([]byte, bool, error) {
			return nil, false, errors.New("运行时Skill已删除或执行环境不可用")
		}}
	}
	return out, nil
}
func awReadResourceSkill(sk skills.Skill) ([]byte, error) {
	data, truncated, err := skills.ReadDocument(sk, "SKILL.md")
	if err != nil || truncated || len(data) > 256<<10 || !utf8.Valid(data) {
		return nil, errors.New("Skill主文档不存在、不可读或超过256KiB")
	}
	return data, nil
}
func (m *Manager) awResourceShortcuts(ctx context.Context, owner *identity.Scope, a studioAgent) (map[string]shortcutToolReference, error) {
	scope, err := shortcutScopeID(owner, a.ID)
	if err != nil {
		return nil, err
	}
	b := &shortcutToolBackend{m: m, s: &Session{}, owner: owner, agentID: a.ID, scopeID: scope}
	out := map[string]shortcutToolReference{}
	for _, source := range []string{"shortcut", "quick_card"} {
		refs, e := b.sourceReferences(ctx, source, "")
		if e != nil {
			return nil, e
		}
		for _, ref := range refs {
			if ref.summary.Kind != "folder" {
				out[source+":"+ref.summary.ID] = ref
			}
		}
	}
	return out, nil
}
func (s *Server) agentWorkflowResources(ctx context.Context, owner *identity.Scope, a studioAgent, source string) (awResourceCatalog, error) {
	out := awResourceCatalog{Skills: []awSkillResource{}, Shortcuts: []awShortcutResource{}, Files: []awResourceFileInfo{}, FileLimit: 500, SourceSessionID: source}
	if a.ID == "" || a.Status != "ready" {
		return out, ErrNotFound
	}
	known, err := s.mgr.awResourceSkills(owner, a)
	if err != nil {
		return out, err
	}
	for id, sk := range known {
		row := awSkillResource{ID: id, Name: sk.Name, Description: sk.Description, Version: sk.Revision, Available: true}
		row.Reason = awSkillResourceReason(sk)
		data, err := awReadResourceSkill(sk)
		if err != nil {
			row.Reason = err.Error()
		} else if row.Version == "" {
			row.Version = awResourceHash(data)
		}
		row.Available = row.Reason == ""
		out.Skills = append(out.Skills, row)
	}
	refs, err := s.mgr.awResourceShortcuts(ctx, owner, a)
	if err != nil {
		return out, err
	}
	for id, ref := range refs {
		row := awShortcutResource{ID: id, Title: ref.summary.Title, Source: ref.summary.Source, Version: ref.summary.Version, Available: true}
		if _, e := s.mgr.awCompileShortcut(ctx, owner, a, known, ref, nil); e != nil {
			row.Available = false
			row.Reason = e.Error()
		}
		out.Shortcuts = append(out.Shortcuts, row)
	}
	sort.Slice(out.Skills, func(i, j int) bool { return out.Skills[i].Name < out.Skills[j].Name })
	sort.Slice(out.Shortcuts, func(i, j int) bool { return out.Shortcuts[i].Title < out.Shortcuts[j].Title })
	dir, err := s.awResourceSourceDir(owner, a.ID, source)
	if err != nil {
		return out, err
	}
	trusted, err := s.mgr.awRoot(owner)
	if err != nil {
		return out, err
	}
	defer trusted.Close()
	relative, err := s.mgr.awRelative(owner, dir)
	if err != nil {
		return out, err
	}
	root, err := trusted.OpenRoot(relative)
	if os.IsNotExist(err) {
		out.FilesReason = "当前Agent工作目录尚未创建"
		return out, nil
	}
	if err != nil {
		return out, err
	}
	defer root.Close()
	seen := 0
	err = fs.WalkDir(root.FS(), ".", func(p string, e fs.DirEntry, walkErr error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if walkErr != nil {
			return walkErr
		}
		seen++
		if seen > 5000 {
			out.FilesTruncated = true
			return fs.SkipAll
		}
		if p == "." {
			return nil
		}
		if e.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if e.IsDir() {
			if strings.HasPrefix(e.Name(), ".") || e.Name() == "workflow-runs" || e.Name() == "_resources" || strings.Count(p, "/") >= 8 {
				return fs.SkipDir
			}
			return nil
		}
		if !af.SafePath(p) {
			return nil
		}
		info, e2 := e.Info()
		if e2 != nil {
			return e2
		}
		if !info.Mode().IsRegular() || info.Size() > 4<<20 {
			return nil
		}
		if len(out.Files) == out.FileLimit {
			out.FilesTruncated = true
			return fs.SkipAll
		}
		out.Files = append(out.Files, awResourceFileInfo{Path: p, Name: e.Name(), Size: info.Size()})
		return nil
	})
	return out, err
}
func (s *Server) awResourceSourceDir(owner *identity.Scope, aid, source string) (string, error) {
	base := s.mgr.cfg.Cwd
	if owner != nil {
		base = s.mgr.UserRoot(*owner)
	}
	if source == "" {
		return filepath.Join(base, "agents", aid), nil
	}
	if !s.mgr.backgroundTaskSessionOwned(source, aid, sessionResultOwnerKey(owner)) || s.mgr.agentWorkflowMarker(source) != nil {
		return "", ErrNotFound
	}
	sess, err := s.mgr.Get(source)
	if err != nil {
		return "", err
	}
	return sess.cwd, nil
}

// Resolve file handles through their existing host namespace. No supplied path
// can select another owner, Agent, source workspace or raw filesystem root.
func (m *Manager) awShortcutResourceFile(ctx context.Context, owner *identity.Scope, aid string, ref shortcutToolReference, p string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var namespace string
	if strings.HasPrefix(p, "shortcut-assets/") {
		var err error
		namespace, err = m.shortcutAssetNamespace(owner, aid)
		if err != nil {
			return nil, err
		}
	} else if ref.origin == "quick_card" && strings.HasPrefix(p, "starter-assets/") {
		sum := sha256.Sum256([]byte("agent:" + aid))
		namespace = filepath.Join(m.cfg.SessionDir, ".starter-assets", hex.EncodeToString(sum[:]))
	} else {
		return nil, errors.New("快捷方式文件不是当前用户和Agent授权的已保存附件，请重新保存附件")
	}
	root, err := os.OpenRoot(namespace)
	if err != nil {
		return nil, errShortcutAssetInvalid
	}
	defer root.Close()
	f, record, err := readStarterAsset(root, p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if record.Size > 4<<20 {
		return nil, errors.New("快捷方式附件超过4MiB")
	}
	data, err := io.ReadAll(io.LimitReader(f, (4<<20)+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) != record.Size || awResourceHash(data) != record.SHA256 {
		return nil, errors.New("快捷方式附件内容已改变")
	}
	return data, nil
}

// emit stores node-local files and returns their paths. nil is read-only
// preflight: supported references and authorization are still fully checked.
type awResourceEmit func(kind, name string, data []byte) (string, error)

func (m *Manager) awCompileShortcut(ctx context.Context, owner *identity.Scope, a studioAgent, known map[string]skills.Skill, ref shortcutToolReference, emit awResourceEmit) (string, error) {
	var text strings.Builder
	var visit func([]inputref.Part, int) error
	visit = func(parts []inputref.Part, depth int) error {
		if depth > 16 {
			return errors.New("快捷方式嵌套超过16层")
		}
		for _, p := range parts {
			if err := ctx.Err(); err != nil {
				return err
			}
			switch p.Type {
			case "text":
				text.WriteString(p.Text)
			case "shortcut":
				text.WriteString("\n" + p.Title + "\n")
				if len(p.Parts) > 0 {
					if err := visit(p.Parts, depth+1); err != nil {
						return err
					}
				} else {
					text.WriteString(p.Text)
				}
			case "skill":
				var sk skills.Skill
				matchedID := ""
				found := false
				for id, v := range known {
					if p.Name == id || p.Name == v.Name {
						sk = v
						matchedID = id
						found = true
						break
					}
				}
				if !found {
					return fmt.Errorf("快捷方式引用的Skill %s 未由所属Agent授权", p.Name)
				}
				if reason := awSkillResourceReason(sk); reason != "" {
					return errors.New(reason)
				}
				data, err := awReadResourceSkill(sk)
				if err != nil {
					return err
				}
				if emit != nil {
					if _, err = emit("skill", matchedID, data); err != nil {
						return err
					}
				}
				text.WriteString("\n已绑定Skill指南：" + sk.Name + "\n")
			case "file":
				data, err := m.awShortcutResourceFile(ctx, owner, a.ID, ref, p.Path)
				if err != nil {
					return err
				}
				dest := "[授权附件]"
				if emit != nil {
					dest, err = emit("attachment", path.Base(p.Path), data)
					if err != nil {
						return err
					}
				}
				text.WriteString("\n附件：" + dest + "\n")
			default:
				return fmt.Errorf("快捷方式含暂不支持的引用类型 %s，请改为文本、Skill或已保存文件", p.Type)
			}
			if text.Len() > 256<<10 {
				return errors.New("快捷方式文本超过256KiB")
			}
		}
		return nil
	}
	if err := visit(ref.parts, 0); err != nil {
		return "", err
	}
	return text.String(), nil
}

func (s *Server) freezeAgentWorkflowResources(ctx context.Context, owner *identity.Scope, a studioAgent, run *af.Run) error {
	has := false
	for _, step := range af.ResourceSteps(run.Definition) {
		has = has || len(step.Skills)+len(step.Shortcuts) > 0
	}
	if !has {
		return nil
	}
	if err := s.validateAgentWorkflowResources(ctx, owner, a, run.Definition); err != nil {
		return err
	}
	known, err := s.mgr.awResourceSkills(owner, a)
	if err != nil {
		return err
	}
	refs, err := s.mgr.awResourceShortcuts(ctx, owner, a)
	if err != nil {
		return err
	}
	resources := &af.RunResources{Version: 1, Nodes: map[string]af.NodeResources{}}
	total := 0
	for _, step := range af.ResourceSteps(run.Definition) {
		if len(step.Skills)+len(step.Shortcuts) == 0 {
			continue
		}
		node := af.NodeResources{}
		added := map[string]bool{}
		inline := 0
		var emit awResourceEmit
		emit = func(kind, name string, data []byte) (string, error) {
			if kind == "skill" && added[name] {
				for _, sk := range node.Skills {
					if sk.ID == name {
						return sk.Path, nil
					}
				}
			}
			if kind == "skill" || kind == "shortcut" {
				inline += len(data)
				if inline > 256<<10 {
					return "", errors.New("每节点Skill与快捷方式内联正文合计不能超过256KiB")
				}
			}
			total += len(data)
			if total > awResourceLimit {
				return "", errors.New("工作流绑定资源总计超过16MiB")
			}
			if len(node.Files) >= 128 {
				return "", errors.New("节点资源文件超过128份")
			}
			base := filepath.Base(name)
			if kind == "skill" {
				base = "SKILL.md"
			}
			if kind == "shortcut" {
				base = "shortcut.md"
			}
			dest := fmt.Sprintf("resources/%s/%s/%03d-%s", step.ID, kind, len(node.Files)+1, base)
			absolute := filepath.Join(run.Workspace, "_resources", step.ID, filepath.FromSlash(dest))
			if err := s.mgr.awWriteOwned(owner, absolute, data); err != nil {
				return "", err
			}
			hash := awResourceHash(data)
			node.Files = append(node.Files, af.ResourceFile{Path: dest, Name: base, Size: int64(len(data)), SHA256: hash})
			if kind == "skill" {
				sk, ok := known[name]
				if !ok {
					return "", errors.New("节点Skill不在所属Agent授权集合内")
				}
				version := sk.Revision
				if version == "" {
					version = hash
				}
				node.Skills = append(node.Skills, af.ResourceDocument{ID: name, Name: sk.Name, Version: version, Path: dest, SHA256: hash})
				added[name] = true
			}
			return dest, nil
		}
		for _, id := range step.Skills {
			sk, ok := known[id]
			if !ok {
				return fmt.Errorf("节点 %s 绑定的Skill已撤销或不存在", step.ID)
			}
			if reason := awSkillResourceReason(sk); reason != "" {
				return errors.New(reason)
			}
			data, e := awReadResourceSkill(sk)
			if e != nil {
				return e
			}
			if _, e = emit("skill", id, data); e != nil {
				return e
			}
		}
		for _, id := range step.Shortcuts {
			ref, ok := refs[id]
			if !ok {
				return fmt.Errorf("节点 %s 绑定的快捷方式已撤销或不存在", step.ID)
			}
			text, e := s.mgr.awCompileShortcut(ctx, owner, a, known, ref, emit)
			if e != nil {
				return e
			}
			dest, e := emit("shortcut", ref.summary.Title, []byte(text))
			if e != nil {
				return e
			}
			node.Shortcuts = append(node.Shortcuts, af.ResourceShortcut{ID: id, Title: ref.summary.Title, Version: ref.summary.Version, Path: dest, SHA256: awResourceHash([]byte(text))})
		}
		resources.Nodes[step.ID] = node
	}
	if !af.ValidRunResources(run.Definition, resources) {
		return errors.New("节点冻结资源结构无效")
	}
	run.Resources = resources
	return nil
}

func (m *Manager) checkAgentWorkflowResources(ctx context.Context, owner *identity.Scope, a studioAgent, run af.Run) error {
	if run.Resources == nil {
		return nil
	}
	if !af.ValidRunResources(run.Definition, run.Resources) {
		return errors.New("运行资源快照无效")
	}
	known, err := m.awResourceSkills(owner, a)
	if err != nil {
		return err
	}
	refs, err := m.awResourceShortcuts(ctx, owner, a)
	if err != nil {
		return err
	}
	documentHashes := map[string]string{}
	for _, node := range run.Resources.Nodes {
		for _, sk := range node.Skills {
			current, ok := known[sk.ID]
			if !ok || awSkillResourceReason(current) != "" {
				return fmt.Errorf("节点Skill %s 的授权已撤销", sk.Name)
			}
			hash, checked := documentHashes[sk.ID]
			if !checked {
				data, readErr := awReadResourceSkill(current)
				if readErr != nil {
					return fmt.Errorf("节点Skill %s 的主文档已不可用，请重新试跑", sk.Name)
				}
				hash = awResourceHash(data)
				documentHashes[sk.ID] = hash
			}
			if hash != sk.SHA256 {
				return fmt.Errorf("节点Skill %s 的主文档已变化，请重新试跑；不会将新版指南混入本次运行", sk.Name)
			}
		}
		for _, sc := range node.Shortcuts {
			if _, ok := refs[sc.ID]; !ok {
				return fmt.Errorf("节点快捷方式 %s 已撤销或不存在", sc.Title)
			}
		}
	}
	return nil
}

func (m *Manager) agentWorkflowResourcePrompt(owner *identity.Scope, run af.Run, stepID, cwd string, materialize bool) (string, error) {
	if run.Resources == nil {
		return "", nil
	}
	if !af.ValidRunResources(run.Definition, run.Resources) {
		return "", errors.New("运行资源快照无效")
	}
	node, ok := run.Resources.Nodes[stepID]
	if !ok {
		return "", nil
	}
	contents := map[string][]byte{}
	for _, f := range node.Files {
		data, err := m.awReadOwned(owner, filepath.Join(run.Workspace, "_resources", stepID, filepath.FromSlash(f.Path)), 4<<20)
		if err != nil || int64(len(data)) != f.Size || awResourceHash(data) != f.SHA256 {
			return "", errors.New("节点冻结资源已改变或不可用")
		}
		contents[f.Path] = data
		if materialize {
			if err = m.awWriteOwned(owner, filepath.Join(cwd, filepath.FromSlash(f.Path)), data); err != nil {
				return "", err
			}
		}
	}
	var out strings.Builder
	out.WriteString("\n本节点显式绑定的冻结资源（不授予额外工具权限；普通Skill仅冻结主文档，包内其它文件仍使用会话能力锁）：\n")
	for _, sk := range node.Skills {
		fmt.Fprintf(&out, "\n<bound_skill name=%q version=%q path=%q>\n%s\n</bound_skill>\n", sk.Name, sk.Version, sk.Path, contents[sk.Path])
	}
	for _, sc := range node.Shortcuts {
		fmt.Fprintf(&out, "\n<bound_shortcut title=%q version=%q path=%q>\n%s\n</bound_shortcut>\n", sc.Title, sc.Version, sc.Path, contents[sc.Path])
	}
	return out.String(), nil
}

func (s *Server) copyAgentWorkflowRetryResources(owner *identity.Scope, run *af.Run, old af.Run) error {
	if old.Resources == nil {
		return nil
	}
	if !af.ValidRunResources(old.Definition, old.Resources) {
		return errors.New("历史资源快照无效")
	}
	for stepID, node := range old.Resources.Nodes {
		for _, f := range node.Files {
			data, err := s.mgr.awReadOwned(owner, filepath.Join(old.Workspace, "_resources", stepID, filepath.FromSlash(f.Path)), 4<<20)
			if err != nil || int64(len(data)) != f.Size || awResourceHash(data) != f.SHA256 {
				return errors.New("历史资源已改变，不能无提示重试")
			}
			if err = s.mgr.awWriteOwned(owner, filepath.Join(run.Workspace, "_resources", stepID, filepath.FromSlash(f.Path)), data); err != nil {
				return err
			}
		}
	}
	run.Resources = old.Resources
	return nil
}

func (s *Server) validateAgentWorkflowResources(ctx context.Context, owner *identity.Scope, a studioAgent, d af.Definition) error {
	needed := false
	for _, step := range af.ResourceSteps(d) {
		needed = needed || len(step.Skills)+len(step.Shortcuts) > 0
	}
	if !needed {
		return nil
	}
	known, err := s.mgr.awResourceSkills(owner, a)
	if err != nil {
		return err
	}
	refs, err := s.mgr.awResourceShortcuts(ctx, owner, a)
	if err != nil {
		return err
	}
	for _, step := range af.ResourceSteps(d) {
		inline := 0
		seen := map[string]bool{}
		paths := map[string]string{}
		fileIndex := 0
		checkBudget := func(kind, id string, data []byte) (string, error) {
			if kind == "skill" && seen[id] {
				return paths[id], nil
			}
			seen[id] = true
			if kind != "attachment" {
				inline += len(data)
			}
			if inline > 256<<10 {
				return "", errors.New("每节点Skill与快捷方式内联正文合计不能超过256KiB")
			}
			fileIndex++
			name := filepath.Base(id)
			if kind == "skill" {
				name = "SKILL.md"
			}
			if kind == "shortcut" {
				name = "shortcut.md"
			}
			dest := fmt.Sprintf("resources/%s/%s/%03d-%s", step.ID, kind, fileIndex, name)
			paths[id] = dest
			return dest, nil
		}
		for _, id := range step.Skills {
			sk, ok := known[id]
			if !ok {
				return fmt.Errorf("节点 %s 的Skill不在所属Agent授权集合内", step.ID)
			}
			if reason := awSkillResourceReason(sk); reason != "" {
				return errors.New(reason)
			}
			data, readErr := awReadResourceSkill(sk)
			if readErr != nil {
				return readErr
			}
			if _, err = checkBudget("skill", id, data); err != nil {
				return err
			}
		}
		for _, id := range step.Shortcuts {
			ref, ok := refs[id]
			if !ok {
				return fmt.Errorf("节点 %s 的快捷方式已撤销或不存在", step.ID)
			}
			text, compileErr := s.mgr.awCompileShortcut(ctx, owner, a, known, ref, checkBudget)
			if compileErr != nil {
				return compileErr
			}
			if _, err = checkBudget("shortcut", id, []byte(text)); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *Manager) awResourceSequenceSession(run af.Run, snap awExecutionSnapshot, stepID string) (*Session, error) {
	dir := filepath.Join(run.Workspace, "nodes", stepID)
	relative, err := m.awRelative(snap.Owner, dir)
	if err != nil {
		return nil, err
	}
	root, err := m.awRoot(snap.Owner)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if err = root.MkdirAll(relative, 0700); err != nil {
		return nil, err
	}
	for _, f := range run.Files {
		data, e := m.awReadOwned(snap.Owner, filepath.Join(run.Workspace, filepath.FromSlash(f.Path)), 4<<20)
		if e != nil || awResourceHash(data) != f.SHA256 {
			return nil, errors.New("冻结输入材料不可用")
		}
		if e = m.awWriteOwned(snap.Owner, filepath.Join(dir, filepath.FromSlash(f.Path)), data); e != nil {
			return nil, e
		}
	}
	for _, st := range run.Steps {
		if st.Status != af.Completed || st.Artifact == "" {
			continue
		}
		data, e := m.awReadArtifact(run.ID, st.StepID)
		if e != nil || awResourceHash(data) != st.ArtifactSHA256 {
			return nil, errors.New("前序产物快照不可用")
		}
		if e = m.awWriteOwned(snap.Owner, filepath.Join(dir, filepath.FromSlash(st.Artifact)), data); e != nil {
			return nil, e
		}
	}
	allowed := map[string]bool{}
	for _, step := range run.Definition.Steps {
		if step.ID == stepID {
			break
		}
		allowed[step.ID] = true
	}
	if err = m.awMaterializeRepeatArtifacts(snap.Owner, run, allowed, dir); err != nil {
		return nil, err
	}
	if _, err = m.agentWorkflowResourcePrompt(snap.Owner, run, stepID, dir, true); err != nil {
		return nil, err
	}
	marker := session.AgentWorkflowIdentity{OwnerID: run.OwnerID, AgentID: run.AgentID, WorkflowID: run.WorkflowID, RunID: run.ID, SourceSessionID: run.SourceSessionID}
	sess, err := m.createStudioExecutionConversationWithWorkflow(snap.Agent, snap.Owner, false, nil, nil, &marker, relative)
	if err == nil {
		sess.SetPolicy(awWorkflowToolApprovalMode(run.Definition), 0)
	}
	return sess, err
}
