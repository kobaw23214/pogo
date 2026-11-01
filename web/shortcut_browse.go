package web

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"

	"github.com/yosukeno/pogo/shortcuts"
)

// List is a directory view, not Search with an empty filter. Private root
// children and read-only source directories share a view, never a write store.
func (b *shortcutToolBackend) List(ctx context.Context, in shortcuts.ListRequest) (shortcuts.ListResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := shortcuts.ListResult{ParentID: in.ParentID, Breadcrumbs: []shortcuts.Folder{}, Entries: []shortcuts.ReferenceSummary{}}
	if err := b.access(ctx, false); err != nil {
		return out, err
	}
	if in.Offset < 0 || in.Offset > 10000 || in.Limit < 0 || in.Limit > 100 {
		return out, errors.New("invalid shortcut directory offset or limit")
	}
	view, err := b.m.userShortcuts(b.owner, b.agentID)
	if err != nil {
		return out, err
	}
	out.Revision = view.Revision
	var all []shortcutToolReference
	switch {
	case in.ParentID == shortcuts.InformationFolderID || strings.HasPrefix(in.ParentID, shortcuts.InformationFolderID+"/"):
		out.ReadOnly = true
		all, out.Breadcrumbs, err = b.informationDirectory(ctx, in.ParentID)
	case in.ParentID == shortcuts.QuickCardsFolderID:
		out.ReadOnly = true
		out.Breadcrumbs = []shortcuts.Folder{{ID: shortcuts.QuickCardsFolderID, Name: "快捷卡片", ReadOnly: true}}
		all, err = b.sourceReferences(ctx, "quick_card", "")
		for i := range all {
			all[i].summary.ID = shortcuts.QuickCardsFolderID + "/" + all[i].summary.ID
			all[i].summary.ParentID = shortcuts.QuickCardsFolderID
			all[i].summary.ReadOnly = true
		}
	case in.ParentID == "" || shortcutFolderID.MatchString(in.ParentID):
		out.Breadcrumbs, err = privateShortcutBreadcrumbs(view.Folders, in.ParentID)
		if err != nil {
			break
		}
		version := strconv.FormatInt(view.Revision, 10)
		if in.ParentID == "" {
			all = append(all,
				shortcutToolReference{summary: shortcuts.ReferenceSummary{Source: "information", Kind: "folder", ID: shortcuts.InformationFolderID, Title: "信息库", ReadOnly: true}},
				shortcutToolReference{summary: shortcuts.ReferenceSummary{Source: "quick_card", Kind: "folder", ID: shortcuts.QuickCardsFolderID, Title: "快捷卡片", ReadOnly: true}},
			)
		}
		for _, folder := range view.Folders {
			if folder.ParentID == in.ParentID {
				all = append(all, shortcutToolReference{summary: shortcuts.ReferenceSummary{Source: "shortcut", Kind: "folder", ID: folder.ID, Title: folder.Name, ParentID: folder.ParentID, Version: version}})
			}
		}
		for _, entry := range view.Shortcuts {
			if entry.ParentID == in.ParentID {
				all = append(all, b.issue("shortcut", entry.ID, entry.Title, entry.ParentID, entry.Icon, version, "shortcut", shortcutToolParts(entry)))
			}
		}
	default:
		err = errors.New("unknown shortcut directory; browse the root for current host-issued directory IDs")
	}
	if err != nil {
		return out, err
	}
	sort.SliceStable(all, func(i, j int) bool {
		a, c := all[i].summary, all[j].summary
		if (a.Kind == "folder") != (c.Kind == "folder") {
			return a.Kind == "folder"
		}
		if a.Title != c.Title {
			return a.Title < c.Title
		}
		return a.ID < c.ID
	})
	out.Total, out.Offset = len(all), min(in.Offset, len(all))
	limit := in.Limit
	if limit == 0 {
		limit = 30
	}
	end := min(out.Offset+limit, len(all))
	for _, ref := range all[out.Offset:end] {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		if entry := ref.informationEntry; entry != nil {
			store, e := b.m.InformationStore(b.owner)
			if e != nil {
				return out, e
			}
			full, e := store.Get(entry.ID, entry.Version)
			if e != nil {
				return out, e
			}
			original := ref.summary
			ref = b.issue("information", entry.ID, entry.Name, original.ParentID, "", strconv.FormatInt(entry.Version, 10), "information", informationShortcutToolParts(full))
			ref.summary.ID, ref.summary.ReadOnly = original.ID, true
		}
		if ref.summary.RefID != "" {
			if len(b.refs) >= 4096 {
				if _, exists := b.refs[ref.summary.RefID]; !exists {
					return out, errors.New("reference handle limit reached; start a new conversation")
				}
			}
			b.refs[ref.summary.RefID] = ref
		}
		out.Entries = append(out.Entries, ref.summary)
	}
	if end < len(all) {
		out.NextOffset = &end
	}
	return out, nil
}

func privateShortcutBreadcrumbs(folders []shortcutFolder, id string) ([]shortcuts.Folder, error) {
	byID := make(map[string]shortcutFolder, len(folders))
	for _, folder := range folders {
		byID[folder.ID] = folder
	}
	path, seen := []shortcuts.Folder{}, map[string]bool{}
	for id != "" {
		folder, exists := byID[id]
		if !exists || seen[id] || len(path) >= shortcutsMaxFolderDepth {
			return nil, errors.New("private folder is unavailable; browse the root again")
		}
		seen[id] = true
		path = append(path, shortcuts.Folder{ID: folder.ID, Name: folder.Name, ParentID: folder.ParentID})
		id = folder.ParentID
	}
	for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
		path[i], path[j] = path[j], path[i]
	}
	return path, nil
}

func (b *shortcutToolBackend) informationDirectory(ctx context.Context, parentID string) ([]shortcutToolReference, []shortcuts.Folder, error) {
	all, err := b.sourceReferences(ctx, "information", "")
	if err != nil {
		return nil, nil, err
	}
	index := make(map[string]shortcuts.ReferenceSummary)
	for _, ref := range all {
		if ref.summary.Kind == "folder" {
			index[ref.summary.ID] = ref.summary
		}
	}
	root := shortcuts.Folder{ID: shortcuts.InformationFolderID, Name: "信息库", ReadOnly: true}
	rawParent := strings.TrimPrefix(parentID, shortcuts.InformationFolderID)
	if rawParent != "" {
		rawParent = strings.TrimPrefix(rawParent, "/")
		if !shortcutFolderID.MatchString(rawParent) {
			return nil, nil, errors.New("invalid information directory ID")
		}
	}
	path, seen := []shortcuts.Folder{}, map[string]bool{}
	for id := rawParent; id != ""; {
		folder, exists := index[id]
		if !exists || seen[id] || len(path) >= 64 {
			return nil, nil, errors.New("information directory is unavailable")
		}
		seen[id] = true
		parent := shortcuts.InformationFolderID
		if folder.ParentID != "" {
			parent += "/" + folder.ParentID
		}
		path = append(path, shortcuts.Folder{ID: shortcuts.InformationFolderID + "/" + id, Name: folder.Title, ParentID: parent, ReadOnly: true})
		id = folder.ParentID
	}
	for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
		path[i], path[j] = path[j], path[i]
	}
	path = append([]shortcuts.Folder{root}, path...)
	children := []shortcutToolReference{}
	for _, ref := range all {
		if ref.summary.ParentID != rawParent {
			continue
		}
		ref.summary.ID = shortcuts.InformationFolderID + "/" + ref.summary.ID
		ref.summary.ParentID, ref.summary.ReadOnly = parentID, true
		children = append(children, ref)
	}
	return children, path, nil
}
