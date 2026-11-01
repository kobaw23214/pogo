package web

// Shortcut mutations change stable private records only. Reference snapshots,
// source files and built-in projections are never traversed for writes.
import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/yosukeno/pogo/inputref"
	"github.com/yosukeno/pogo/shortcuts"
	"github.com/yosukeno/pogo/tools"
)

type shortcutManagementPlan struct {
	Plan    shortcuts.MergePlan
	Expires time.Time
	Digest  string
}

func shortcutManagementID(id string) bool { return shortcutFolderID.MatchString(id) }
func shortcutManagementName(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" || !utf8.ValidString(name) || utf8.RuneCountInString(name) > 120 || strings.ContainsFunc(name, unicode.IsControl) {
		return "", errors.New("目录名称需要为 1–120 个有效字符")
	}
	return name, nil
}
func shortcutMutationDigest(action string, payload any) string {
	return shortcutToolDigest(struct {
		Action  string
		Payload any
	}{action, payload})
}
func shortcutMutationBase(in shortcuts.MutationRequest) error {
	if !shortcutToolRequestID.MatchString(in.RequestID) || in.ExpectedRevision < 0 || in.ExpectedRevision >= shortcutsMaxRevision {
		return errors.New("需要有效 request_id 和 expected_revision")
	}
	return nil
}
func shortcutPrivateIDs(ids []string) error {
	if len(ids) == 0 || len(ids) > shortcutsMaxCount+shortcutsMaxFolders {
		return errors.New("需要 1–512 个私有条目 ID")
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if !shortcutManagementID(id) || seen[id] {
			return errors.New("条目必须是互不重复的私有 ID；预设来源只读")
		}
		seen[id] = true
	}
	return nil
}
func shortcutDocPath(b *shortcutToolBackend) (string, error) {
	return shortcutPath(b.m.cfg.SessionDir, b.owner, b.agentID)
}
func shortcutFindEntry(doc *shortcutDocument, id string) int {
	for i, e := range doc.Shortcuts {
		if e.ID == id {
			return i
		}
	}
	return -1
}
func shortcutFindFolder(doc *shortcutDocument, id string) int {
	for i, f := range doc.Folders {
		if f.ID == id {
			return i
		}
	}
	return -1
}
func shortcutCheckParent(doc *shortcutDocument, parent string) error {
	if parent == "" {
		return nil
	}
	if !shortcutManagementID(parent) || shortcutFindFolder(doc, parent) < 0 {
		return errors.New("目标必须是当前 Agent 的私有目录；信息库和快捷卡片来源只读")
	}
	return nil
}
func shortcutResultTarget(out *shortcuts.MutationResult, doc shortcutDocument, target string) {
	if i := shortcutFindEntry(&doc, target); i >= 0 {
		entry := shortcutEntry(doc.Shortcuts[i])
		entry.Parts = inputref.Clone(entry.Parts)
		out.Shortcut = &entry
	}
	if i := shortcutFindFolder(&doc, target); i >= 0 {
		f := doc.Folders[i]
		out.Folder = &shortcuts.Folder{ID: f.ID, Name: f.Name, ParentID: f.ParentID}
	}
}
func (b *shortcutToolBackend) managementReplayLocked(doc shortcutDocument, action string, in shortcuts.MutationRequest, digest string) (shortcuts.MutationResult, bool, error) {
	out := shortcuts.MutationResult{AgentID: b.agentID, Action: action, Revision: doc.Revision, RequestID: in.RequestID, Changes: []shortcuts.Change{}}
	if old, ok := doc.ToolCreates[shortcutToolDigest(in.RequestID)]; ok {
		if old.Digest != digest {
			return out, true, errors.New("request_id 已用于不同操作或内容，请使用新的 request_id")
		}
		out.Saved = true
		out.Reused = true
		out.AppliedRevision = old.Revision
		shortcutResultTarget(&out, doc, old.ShortcutID)
		return out, true, nil
	}
	if doc.Revision != in.ExpectedRevision {
		return out, false, errShortcutsConflict
	}
	return out, false, nil
}
func (b *shortcutToolBackend) managementPreflight(action string, in shortcuts.MutationRequest, payload any) (shortcuts.MutationResult, bool, error) {
	p, err := shortcutDocPath(b)
	if err != nil {
		return shortcuts.MutationResult{}, false, err
	}
	shortcutsMu.Lock()
	defer shortcutsMu.Unlock()
	doc, err := readShortcutDocument(p, b.scopeID, b.agentID)
	if err != nil {
		return shortcuts.MutationResult{}, false, err
	}
	return b.managementReplayLocked(doc, action, in, shortcutMutationDigest(action, payload))
}

// Every first successful request commits its acknowledgement with the mutation
// atomically, including no-ops. A replay only acknowledges that earlier commit.
func (b *shortcutToolBackend) managementMutate(ctx context.Context, action string, in shortcuts.MutationRequest, payload any, change func(*shortcutDocument) ([]shortcuts.Change, string, error)) (shortcuts.MutationResult, error) {
	var out shortcuts.MutationResult
	if err := ctx.Err(); err != nil {
		return out, err
	}
	p, err := shortcutDocPath(b)
	if err != nil {
		return out, err
	}
	shortcutsMu.Lock()
	defer shortcutsMu.Unlock()
	doc, err := readShortcutDocument(p, b.scopeID, b.agentID)
	if err != nil {
		return out, err
	}
	digest := shortcutMutationDigest(action, payload)
	if out, replayed, e := b.managementReplayLocked(doc, action, in, digest); e != nil || replayed {
		return out, e
	}
	if doc.Revision >= shortcutsMaxRevision || len(doc.ToolCreates) >= shortcutToolMaxReceipts {
		return out, errors.New("快捷方式版本或请求记录已达到存储上限")
	}
	beforeEntries := slices.Clone(doc.Shortcuts)
	beforeFolders := slices.Clone(doc.Folders)
	changes, target, err := change(&doc)
	if err != nil {
		return out, err
	}
	if _, err = validateUserShortcuts(doc.Shortcuts); err != nil {
		return out, err
	}
	if _, err = validateShortcutFolders(doc.Folders, doc.Shortcuts); err != nil {
		return out, err
	}
	changed := !reflect.DeepEqual(beforeEntries, doc.Shortcuts) || !reflect.DeepEqual(beforeFolders, doc.Folders)
	doc.Revision++
	if doc.ToolCreates == nil {
		doc.ToolCreates = map[string]shortcutCreateReceipt{}
	}
	if target == "" {
		target = "op_" + digest
	}
	doc.ToolCreates[shortcutToolDigest(in.RequestID)] = shortcutCreateReceipt{Digest: digest, Revision: doc.Revision, ShortcutID: target}
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	if err = encoder.Encode(doc); err != nil || encoded.Len() > shortcutsMaxBody {
		return out, errors.New("快捷方式整理超过存储上限；原版本已保留")
	}
	if err = ctx.Err(); err != nil {
		return out, err
	}
	if err = b.access(ctx, true); err != nil {
		return out, err
	}
	if err = writeWorkflowFileAtomic(p, encoded.Bytes()); err != nil {
		return out, errors.New("快捷方式整理保存失败；原版本已保留")
	}
	if changes == nil {
		changes = []shortcuts.Change{}
	}
	out = shortcuts.MutationResult{AgentID: b.agentID, Action: action, Saved: true, Changed: changed, Revision: doc.Revision, AppliedRevision: doc.Revision, RequestID: in.RequestID, Changes: changes}
	shortcutResultTarget(&out, doc, target)
	return out, nil
}
func (b *shortcutToolBackend) managementEvent(out shortcuts.MutationResult) {
	if out.Saved && !out.Reused && b.s.hub != nil {
		id := ""
		if out.Shortcut != nil {
			id = out.Shortcut.ID
		}
		b.s.hub.Publish(Event{Type: EvShortcutsChanged, ShortcutAgentID: b.agentID, ShortcutRevision: out.Revision, ShortcutID: id})
	}
}

func (b *shortcutToolBackend) FolderCreate(ctx context.Context, in shortcuts.FolderCreateRequest) (shortcuts.MutationResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.access(ctx, true); err != nil {
		return shortcuts.MutationResult{}, err
	}
	if err := shortcutMutationBase(in.MutationRequest); err != nil {
		return shortcuts.MutationResult{}, err
	}
	name, err := shortcutManagementName(in.Name)
	if err != nil {
		return shortcuts.MutationResult{}, err
	}
	if in.ParentID != "" && !shortcutManagementID(in.ParentID) {
		return shortcuts.MutationResult{}, errors.New("预设来源不是可写目录")
	}
	out, err := b.managementMutate(ctx, "folder_create", in.MutationRequest, in, func(doc *shortcutDocument) ([]shortcuts.Change, string, error) {
		if err := shortcutCheckParent(doc, in.ParentID); err != nil {
			return nil, "", err
		}
		for _, folder := range doc.Folders {
			if folder.ParentID == in.ParentID && folder.Name == name {
				return nil, "", errors.New("同目录已有同名文件夹")
			}
		}
		id := "sf_" + shortcutToolDigest([]string{b.scopeID, in.RequestID})[:32]
		if shortcutFindEntry(doc, id) >= 0 || shortcutFindFolder(doc, id) >= 0 {
			return nil, "", errors.New("目录 ID 已存在，请重新创建")
		}
		doc.Folders = append(doc.Folders, shortcutFolder{ID: id, Name: name, ParentID: in.ParentID})
		return []shortcuts.Change{{Action: "folder_create", Kind: "folder", ID: id, Title: name, ToParentID: in.ParentID}}, id, nil
	})
	if err == nil {
		b.managementEvent(out)
	}
	return out, err
}
func (b *shortcutToolBackend) FolderRename(ctx context.Context, in shortcuts.FolderRenameRequest) (shortcuts.MutationResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.access(ctx, true); err != nil {
		return shortcuts.MutationResult{}, err
	}
	if err := shortcutMutationBase(in.MutationRequest); err != nil {
		return shortcuts.MutationResult{}, err
	}
	if !shortcutManagementID(in.ID) {
		return shortcuts.MutationResult{}, errors.New("预设来源只读")
	}
	name, err := shortcutManagementName(in.Name)
	if err != nil {
		return shortcuts.MutationResult{}, err
	}
	out, err := b.managementMutate(ctx, "folder_rename", in.MutationRequest, in, func(doc *shortcutDocument) ([]shortcuts.Change, string, error) {
		i := shortcutFindFolder(doc, in.ID)
		if i < 0 {
			return nil, "", errors.New("私有目录不存在")
		}
		old := doc.Folders[i]
		for _, f := range doc.Folders {
			if f.ID != in.ID && f.ParentID == old.ParentID && f.Name == name {
				return nil, "", errors.New("同目录已有同名文件夹")
			}
		}
		doc.Folders[i].Name = name
		if old.Name == name {
			return nil, in.ID, nil
		}
		return []shortcuts.Change{{Action: "folder_rename", Kind: "folder", ID: in.ID, Title: name, FromParentID: old.ParentID, ToParentID: old.ParentID}}, in.ID, nil
	})
	if err == nil {
		b.managementEvent(out)
	}
	return out, err
}

func (b *shortcutToolBackend) Update(ctx context.Context, in shortcuts.UpdateRequest) (shortcuts.MutationResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.access(ctx, true); err != nil {
		return shortcuts.MutationResult{}, err
	}
	if err := shortcutMutationBase(in.MutationRequest); err != nil {
		return shortcuts.MutationResult{}, err
	}
	if !shortcutManagementID(in.ID) {
		return shortcuts.MutationResult{}, errors.New("只能修改私有快捷方式")
	}
	if in.Graph != nil && (in.Title != nil || in.Icon != nil || in.Language != nil) || in.Graph == nil && in.Title == nil && in.Icon == nil && in.Language == nil {
		return shortcuts.MutationResult{}, errors.New("需要元数据修改或完整 graph，二者不能混用")
	}
	if in.Graph == nil {
		out, err := b.managementMutate(ctx, "update", in.MutationRequest, in, func(doc *shortcutDocument) ([]shortcuts.Change, string, error) {
			i := shortcutFindEntry(doc, in.ID)
			if i < 0 {
				return nil, "", errors.New("私有快捷方式不存在")
			}
			old := doc.Shortcuts[i]
			next := old
			if in.Title != nil {
				next.Title = strings.TrimSpace(*in.Title)
			}
			if in.Icon != nil {
				next.Icon = *in.Icon
			}
			if in.Language != nil {
				next.Language = *in.Language
			}
			doc.Shortcuts[i] = next
			if reflect.DeepEqual(old, next) {
				return nil, in.ID, nil
			}
			return []shortcuts.Change{{Action: "update", Kind: "shortcut", ID: next.ID, Title: next.Title, FromParentID: next.ParentID, ToParentID: next.ParentID}}, in.ID, nil
		})
		if err == nil {
			b.managementEvent(out)
		}
		return out, err
	}
	if err := shortcuts.ValidateGraph(*in.Graph); err != nil {
		return shortcuts.MutationResult{}, err
	}
	if replay, exists, err := b.managementPreflight("update", in.MutationRequest, in); err != nil || exists {
		return replay, err
	}
	namespace, err := b.m.shortcutAssetNamespace(b.owner, b.agentID)
	if err != nil {
		return shortcuts.MutationResult{}, err
	}
	var out shortcuts.MutationResult
	err = tools.WithFileMutationLock(namespace, func() error {
		batch := shortcutToolAssetBatch{b: b, plans: map[string]*shortcutToolAssetPlan{}}
		defer batch.close()
		compiled, err := shortcuts.Compile(ctx, b.scopeID, *in.Graph, func(ctx context.Context, id string) (shortcuts.Reference, error) {
			ref, ok := b.refs[id]
			if !ok {
				return shortcuts.Reference{}, errors.New("引用已失效，请先读取当前 Agent 的资料")
			}
			parts, err := batch.prepare(ctx, ref)
			if err != nil {
				return shortcuts.Reference{}, err
			}
			return shortcuts.Reference{ScopeID: b.scopeID, Version: ref.summary.Version, Parts: parts, SHA256: shortcuts.SnapshotSHA256(parts)}, nil
		})
		if err != nil {
			return err
		}
		for _, part := range flatInputParts(compiled.Root.Parts) {
			if part.Type == "shortcut" && part.ShortcutID == in.ID {
				return errors.New("更新不能把目标卡片自身的快照再次嵌入；请使用 shortcut_query 的 read 操作返回的 graph 展开后编辑，或另存为新卡片")
			}
		}
		if err = b.s.checkInputSkillAccess(compiled.Root.Parts); err != nil {
			return err
		}
		if err = b.access(ctx, true); err != nil {
			return err
		}
		if err = batch.stage(ctx); err != nil {
			return err
		}
		out, err = b.managementMutate(ctx, "update", in.MutationRequest, in, func(doc *shortcutDocument) ([]shortcuts.Change, string, error) {
			i := shortcutFindEntry(doc, in.ID)
			if i < 0 {
				return nil, "", errors.New("私有快捷方式不存在")
			}
			old := doc.Shortcuts[i]
			root := compiled.Root
			next := userShortcut{ID: old.ID, ParentID: old.ParentID, Title: root.Title, Content: root.Content, Icon: root.Icon, Language: root.Language, Parts: inputref.Clone(root.Parts), InputLayout: "inline"}
			doc.Shortcuts[i] = next
			if reflect.DeepEqual(old, next) {
				return nil, old.ID, nil
			}
			return []shortcuts.Change{{Action: "update", Kind: "shortcut", ID: old.ID, Title: root.Title, FromParentID: old.ParentID, ToParentID: old.ParentID}}, old.ID, nil
		})
		batch.committed = err == nil && !out.Reused
		return err
	})
	if err == nil {
		b.managementEvent(out)
	}
	return out, err
}

func (b *shortcutToolBackend) Move(ctx context.Context, in shortcuts.MoveRequest) (shortcuts.MutationResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.access(ctx, true); err != nil {
		return shortcuts.MutationResult{}, err
	}
	if err := shortcutMutationBase(in.MutationRequest); err != nil {
		return shortcuts.MutationResult{}, err
	}
	if err := shortcutPrivateIDs(in.IDs); err != nil {
		return shortcuts.MutationResult{}, err
	}
	if in.ParentID != "" && !shortcutManagementID(in.ParentID) {
		return shortcuts.MutationResult{}, errors.New("预设来源不能作为移动目标")
	}
	out, err := b.managementMutate(ctx, "move", in.MutationRequest, in, func(doc *shortcutDocument) ([]shortcuts.Change, string, error) {
		if err := shortcutCheckParent(doc, in.ParentID); err != nil {
			return nil, "", err
		}
		selected := map[string]bool{}
		parents := map[string]string{}
		for _, f := range doc.Folders {
			parents[f.ID] = f.ParentID
		}
		for _, id := range in.IDs {
			if shortcutFindEntry(doc, id) < 0 && shortcutFindFolder(doc, id) < 0 {
				return nil, "", errors.New("一个或多个私有条目不存在；未移动任何条目")
			}
			selected[id] = true
		}
		for _, id := range in.IDs {
			if shortcutFindFolder(doc, id) < 0 {
				continue
			}
			for parent := in.ParentID; parent != ""; parent = parents[parent] {
				if parent == id {
					return nil, "", errors.New("不能把目录移动到自身或其后代目录")
				}
			}
		}
		for _, id := range in.IDs {
			i := shortcutFindFolder(doc, id)
			if i < 0 || doc.Folders[i].ParentID == in.ParentID {
				continue
			}
			for _, other := range doc.Folders {
				if other.ID != id && other.Name == doc.Folders[i].Name && (other.ParentID == in.ParentID || selected[other.ID]) {
					return nil, "", errors.New("目标目录已有同名文件夹；请先改名，不会覆盖或自动重命名")
				}
			}
		}
		for _, id := range in.IDs {
			parent := parents[id]
			if i := shortcutFindEntry(doc, id); i >= 0 {
				parent = doc.Shortcuts[i].ParentID
			}
			for parent != "" {
				if selected[parent] {
					return nil, "", errors.New("不能同时移动祖先目录及其内部条目，请只选择祖先目录")
				}
				parent = parents[parent]
			}
		}
		changes := []shortcuts.Change{}
		for _, id := range in.IDs {
			if i := shortcutFindEntry(doc, id); i >= 0 {
				e := &doc.Shortcuts[i]
				if e.ParentID != in.ParentID {
					changes = append(changes, shortcuts.Change{Action: "move", Kind: "shortcut", ID: id, Title: e.Title, FromParentID: e.ParentID, ToParentID: in.ParentID})
					e.ParentID = in.ParentID
				}
			} else {
				i := shortcutFindFolder(doc, id)
				f := &doc.Folders[i]
				if f.ParentID != in.ParentID {
					changes = append(changes, shortcuts.Change{Action: "move", Kind: "folder", ID: id, Title: f.Name, FromParentID: f.ParentID, ToParentID: in.ParentID})
					f.ParentID = in.ParentID
				}
			}
		}
		return changes, "", nil
	})
	if err == nil {
		b.managementEvent(out)
	}
	return out, err
}

func (b *shortcutToolBackend) Delete(ctx context.Context, in shortcuts.DeleteRequest) (shortcuts.MutationResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.access(ctx, true); err != nil {
		return shortcuts.MutationResult{}, err
	}
	if err := shortcutMutationBase(in.MutationRequest); err != nil {
		return shortcuts.MutationResult{}, err
	}
	if err := shortcutPrivateIDs(in.IDs); err != nil {
		return shortcuts.MutationResult{}, err
	}
	out, err := b.managementMutate(ctx, "delete", in.MutationRequest, in, func(doc *shortcutDocument) ([]shortcuts.Change, string, error) {
		removed := map[string]bool{}
		for _, id := range in.IDs {
			if shortcutFindEntry(doc, id) < 0 && shortcutFindFolder(doc, id) < 0 {
				return nil, "", errors.New("一个或多个私有条目不存在；未删除任何条目")
			}
			removed[id] = true
		}
		if !in.Recursive {
			for _, f := range doc.Folders {
				if removed[f.ParentID] {
					return nil, "", errors.New("目录非空；需要显式 recursive=true 才能删除后代")
				}
			}
			for _, e := range doc.Shortcuts {
				if removed[e.ParentID] {
					return nil, "", errors.New("目录非空；需要显式 recursive=true 才能删除后代")
				}
			}
		} else {
			for pass := 0; pass < shortcutsMaxFolderDepth; pass++ {
				for _, f := range doc.Folders {
					if removed[f.ParentID] {
						removed[f.ID] = true
					}
				}
			}
			for _, e := range doc.Shortcuts {
				if removed[e.ParentID] {
					removed[e.ID] = true
				}
			}
		}
		entries := []userShortcut{}
		folders := []shortcutFolder{}
		changes := []shortcuts.Change{}
		for _, e := range doc.Shortcuts {
			if removed[e.ID] {
				changes = append(changes, shortcuts.Change{Action: "delete", Kind: "shortcut", ID: e.ID, Title: e.Title, FromParentID: e.ParentID})
			} else {
				entries = append(entries, e)
			}
		}
		for _, f := range doc.Folders {
			if removed[f.ID] {
				changes = append(changes, shortcuts.Change{Action: "delete", Kind: "folder", ID: f.ID, Title: f.Name, FromParentID: f.ParentID})
			} else {
				folders = append(folders, f)
			}
		}
		doc.Shortcuts = entries
		doc.Folders = folders
		return changes, "", nil
	})
	if err == nil {
		b.managementEvent(out)
	}
	return out, err
}

func shortcutExactMergePlan(doc shortcutDocument, scope, session string) shortcuts.MergePlan {
	groups := map[string][]userShortcut{}
	for _, e := range doc.Shortcuts {
		key := shortcutToolDigest(struct {
			Parent, Title, Icon, Language, Content, Layout string
			Parts                                          []inputref.Part
		}{e.ParentID, e.Title, e.Icon, e.Language, e.Content, e.InputLayout, e.Parts})
		groups[key] = append(groups[key], e)
	}
	plan := shortcuts.MergePlan{Revision: doc.Revision, Groups: []shortcuts.MergeGroup{}}
	for _, entries := range groups {
		if len(entries) < 2 {
			continue
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].ID < entries[j].ID })
		group := shortcuts.MergeGroup{KeepID: entries[0].ID, Title: entries[0].Title, ParentID: entries[0].ParentID, DuplicateIDs: []string{}}
		for _, e := range entries[1:] {
			group.DuplicateIDs = append(group.DuplicateIDs, e.ID)
		}
		plan.Removals += len(group.DuplicateIDs)
		plan.Groups = append(plan.Groups, group)
	}
	sort.Slice(plan.Groups, func(i, j int) bool { return plan.Groups[i].KeepID < plan.Groups[j].KeepID })
	plan.ID = "smp_" + shortcutToolDigest(struct {
		Scope, Session string
		Revision       int64
		Entries        []userShortcut
		Groups         []shortcuts.MergeGroup
	}{scope, session, doc.Revision, doc.Shortcuts, plan.Groups})
	return plan
}
func cloneShortcutMergePlan(in shortcuts.MergePlan) shortcuts.MergePlan {
	out := in
	out.Groups = append([]shortcuts.MergeGroup{}, in.Groups...)
	for i := range out.Groups {
		out.Groups[i].DuplicateIDs = append([]string{}, in.Groups[i].DuplicateIDs...)
	}
	return out
}

func (b *shortcutToolBackend) Merge(ctx context.Context, in shortcuts.MergeRequest) (shortcuts.MutationResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if err := b.access(ctx, in.Apply); err != nil {
		return shortcuts.MutationResult{}, err
	}
	if !in.Apply {
		if in.PlanID != "" || in.ExpectedRevision < 0 {
			return shortcuts.MutationResult{}, errors.New("预览不接受 plan_id，版本不能为负数")
		}
		p, err := shortcutDocPath(b)
		if err != nil {
			return shortcuts.MutationResult{}, err
		}
		shortcutsMu.Lock()
		doc, err := readShortcutDocument(p, b.scopeID, b.agentID)
		shortcutsMu.Unlock()
		if err != nil {
			return shortcuts.MutationResult{}, err
		}
		if in.ExpectedRevision != 0 && in.ExpectedRevision != doc.Revision {
			return shortcuts.MutationResult{}, errShortcutsConflict
		}
		plan := shortcutExactMergePlan(doc, b.scopeID, b.s.ID)
		now := time.Now()
		for id, cached := range b.managementPlans {
			if cached.Expires.Before(now) {
				delete(b.managementPlans, id)
			}
		}
		if len(b.managementPlans) >= 256 {
			b.managementPlans = map[string]shortcutManagementPlan{}
		}
		if b.managementPlans == nil {
			b.managementPlans = map[string]shortcutManagementPlan{}
		}
		b.managementPlans[plan.ID] = shortcutManagementPlan{Plan: cloneShortcutMergePlan(plan), Expires: now.Add(10 * time.Minute), Digest: shortcutToolDigest(plan)}
		return shortcuts.MutationResult{AgentID: b.agentID, Action: "merge", Revision: doc.Revision, Changes: []shortcuts.Change{}, Plan: &plan}, nil
	}
	if err := shortcutMutationBase(in.MutationRequest); err != nil {
		return shortcuts.MutationResult{}, err
	}
	if in.PlanID == "" {
		return shortcuts.MutationResult{}, errors.New("需要先预览并提供宿主生成的 plan_id")
	}
	// A durable replay must remain valid after the ephemeral preview expires.
	if replay, exists, err := b.managementPreflight("merge", in.MutationRequest, in); err != nil || exists {
		return replay, err
	}
	cached, ok := b.managementPlans[in.PlanID]
	if !ok || cached.Expires.Before(time.Now()) {
		return shortcuts.MutationResult{}, errors.New("合并计划不存在或已过期，请重新预览")
	}
	out, err := b.managementMutate(ctx, "merge", in.MutationRequest, in, func(doc *shortcutDocument) ([]shortcuts.Change, string, error) {
		plan := shortcutExactMergePlan(*doc, b.scopeID, b.s.ID)
		if plan.ID != in.PlanID || plan.Revision != in.ExpectedRevision || shortcutToolDigest(plan) != cached.Digest {
			return nil, "", errShortcutsConflict
		}
		removed := map[string]bool{}
		for _, group := range plan.Groups {
			for _, id := range group.DuplicateIDs {
				removed[id] = true
			}
		}
		entries := []userShortcut{}
		changes := []shortcuts.Change{}
		for _, e := range doc.Shortcuts {
			if removed[e.ID] {
				changes = append(changes, shortcuts.Change{Action: "delete", Kind: "shortcut", ID: e.ID, Title: e.Title, FromParentID: e.ParentID})
			} else {
				entries = append(entries, e)
			}
		}
		doc.Shortcuts = entries
		return changes, "", nil
	})
	if err == nil {
		plan := cloneShortcutMergePlan(cached.Plan)
		out.Plan = &plan
		b.managementEvent(out)
	}
	return out, err
}
