package web

// The model receives opaque handles. Source identities, owner, Agent, workspace
// and file roots are host facts pinned by this session, never tool arguments.
import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/information"
	"github.com/yosukeno/pogo/inputref"
	"github.com/yosukeno/pogo/shortcuts"
	"github.com/yosukeno/pogo/skills"
	"github.com/yosukeno/pogo/tools"
)

type shortcutToolReference struct {
	summary          shortcuts.ReferenceSummary
	parts            []inputref.Part
	origin           string
	searchText       string
	fileWitnesses    map[string]starterAssetRecord
	informationEntry *information.Entry
}

type shortcutToolBackend struct {
	m                           *Manager
	s                           *Session
	owner                       *identity.Scope
	agentID, scopeID, workspace string
	mu                          sync.Mutex
	refs                        map[string]shortcutToolReference
	managementPlans             map[string]shortcutManagementPlan
}

func (m *Manager) shortcutToolBackend(s *Session) *shortcutToolBackend {
	b := &shortcutToolBackend{m: m, s: s, refs: map[string]shortcutToolReference{}}
	s.mu.Lock()
	b.agentID, b.workspace = s.studioID, s.cwd
	if s.owner != nil {
		copy := *s.owner
		b.owner = &copy
	}
	s.mu.Unlock()
	if b.agentID == "" {
		return nil
	}
	b.scopeID, _ = shortcutScopeID(b.owner, b.agentID)
	return b
}

func (b *shortcutToolBackend) access(ctx context.Context, create bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.s.mu.Lock()
	ownerEqual := b.owner == nil && b.s.owner == nil || b.owner != nil && b.s.owner != nil && *b.owner == *b.s.owner
	bound := ownerEqual && b.agentID == b.s.studioID && b.workspace == b.s.cwd && !b.s.closing
	b.s.mu.Unlock()
	if !bound || b.agentID == "" || b.scopeID == "" || b.m.Isolation() && b.owner == nil {
		return errors.New("shortcut session scope changed or unavailable")
	}
	if b.agentID != "" {
		a, ok := b.m.studio.get(b.agentID)
		if !ok || a.Status != "ready" {
			return errors.New("Agent no longer available")
		}
	}
	return nil
}

func shortcutToolParts(entry userShortcut) []inputref.Part {
	parts := inputref.Clone(entry.Parts)
	if len(parts) == 0 {
		parts = []inputref.Part{{Type: "text", Text: entry.Content}}
	}
	return []inputref.Part{{Type: "shortcut", ShortcutID: entry.ID, Title: entry.Title, Text: entry.Content, Icon: entry.Icon, Language: entry.Language, Parts: parts}}
}

func (b *shortcutToolBackend) issue(source, id, title, parent, icon, version, origin string, parts []inputref.Part) shortcutToolReference {
	kind := "text"
	if len(parts) == 1 {
		kind = parts[0].Type
	}
	handle := "sr_" + shortcutToolDigest(struct {
		Scope, Source, ID, Version string
		Parts                      []inputref.Part
	}{b.scopeID, source, id, version, parts})
	searchText := shortcutToolSearchText(parts)
	snippet := searchText
	runes := []rune(snippet)
	if len(runes) > 180 {
		snippet = string(runes[:180]) + "…"
	}
	return shortcutToolReference{summary: shortcuts.ReferenceSummary{RefID: handle, Source: source, ID: id, Title: title, ParentID: parent, Icon: icon, Version: version, Kind: kind, Snippet: snippet, ReadOnly: source != "shortcut"}, parts: inputref.Clone(parts), origin: origin, searchText: searchText}
}

func shortcutToolSearchText(parts []inputref.Part) string {
	var out strings.Builder
	var visit func([]inputref.Part)
	visit = func(parts []inputref.Part) {
		for _, p := range parts {
			switch p.Type {
			case "text", "element":
				out.WriteString(p.Text)
			case "file":
				out.WriteString(path.Base(p.Path))
			case "skill":
				out.WriteString(p.Name)
			case "mcp":
				out.WriteString(skills.MCPInputText(p.Server, p.Name))
			case "office":
				if p.Office != nil {
					out.WriteString(p.Office.Title + " " + p.Office.Text)
				}
			case "shortcut":
				out.WriteString(p.Title + " ")
				if p.Parts != nil {
					visit(p.Parts)
				} else {
					out.WriteString(p.Text)
				}
			}
			out.WriteByte('\n')
		}
	}
	visit(parts)
	return strings.TrimSpace(out.String())
}

func (b *shortcutToolBackend) sourceReferences(ctx context.Context, source, query string) ([]shortcutToolReference, error) {
	include := func(name string) bool { return source == "" || source == "all" || source == name }
	var all []shortcutToolReference
	if include("shortcut") {
		view, err := b.m.userShortcuts(b.owner, b.agentID)
		if err != nil {
			return nil, err
		}
		for _, folder := range view.Folders {
			all = append(all, shortcutToolReference{summary: shortcuts.ReferenceSummary{Source: "shortcut", Kind: "folder", ID: folder.ID, Title: folder.Name, ParentID: folder.ParentID}})
		}
		for _, entry := range view.Shortcuts {
			all = append(all, b.issue("shortcut", entry.ID, entry.Title, entry.ParentID, entry.Icon, strconv.FormatInt(view.Revision, 10), "shortcut", shortcutToolParts(entry)))
		}
	}
	if include("quick_card") && b.agentID != "" {
		a, ok := b.m.studio.get(b.agentID)
		if ok {
			seen := map[string]int{}
			for _, entry := range a.Starters {
				title := strings.TrimSpace(entry.Title)
				if title == "" {
					title = strings.TrimSpace(entry.Prompt)
				}
				if title == "" && len(entry.Parts) == 0 {
					continue
				}
				card := skills.StarterCard{Title: title, Label: entry.Label, Prompt: entry.Prompt, Icon: entry.Icon, Parts: inputref.Clone(entry.Parts), InputLayout: entry.InputLayout}
				key := starterShortcutID(b.agentID, card, 0)
				n := seen[key]
				seen[key]++
				x := userShortcut{ID: starterShortcutID(b.agentID, card, n), Title: string([]rune(title)[:min(len([]rune(title)), 120)]), Content: card.Prompt, Icon: card.Icon, Parts: card.Parts, InputLayout: card.InputLayout}
				all = append(all, b.issue("quick_card", x.ID, x.Title, "", x.Icon, shortcutToolDigest(card), "quick_card", shortcutToolParts(x)))
			}
		}
	}
	if include("information") {
		store, err := b.m.InformationStore(b.owner)
		if err != nil {
			return nil, err
		}
		info, err := store.List()
		if err != nil {
			return nil, err
		}
		contentHits := map[string]string{}
		if query != "" && len(query) <= 512 {
			hits, searchErr := store.Search(query, 100)
			if searchErr != nil {
				return nil, searchErr
			}
			for _, hit := range hits {
				contentHits[hit.Entry.ID] = hit.Snippet
			}
		}
		for _, entry := range info.Entries {
			if err = ctx.Err(); err != nil {
				return nil, err
			}
			if entry.Kind == "folder" {
				all = append(all, shortcutToolReference{summary: shortcuts.ReferenceSummary{Source: "information", Kind: "folder", ID: entry.ID, Title: entry.Name, ParentID: entry.ParentID, ReadOnly: true}})
				continue
			}
			snippet, hit := contentHits[entry.ID]
			if query != "" && !strings.Contains(strings.ToLower(entry.Name), strings.ToLower(query)) && !hit {
				continue
			}
			// Defer loading full recursive content until this item is actually in
			// the returned page. Content search shares the library's bounded scan.
			searchText := entry.Name + "\n" + snippet
			if hit {
				searchText += "\n" + query
			}
			copy := entry
			version := strconv.FormatInt(entry.Version, 10)
			all = append(all, shortcutToolReference{summary: shortcuts.ReferenceSummary{RefID: "sr_" + shortcutToolDigest(struct{ Scope, ID, Version string }{b.scopeID, entry.ID, version}), Source: "information", ID: entry.ID, Title: entry.Name, ParentID: entry.ParentID, Version: version, Kind: entry.Kind, Snippet: snippet, ReadOnly: true}, origin: "information", searchText: searchText, informationEntry: &copy})
		}
	}
	if b.s.hub != nil && (include("message") || include("browser") || include("office") || include("file") || include("skill")) {
		messages := b.s.hub.Snapshot().Messages
		for i := len(messages) - 1; i >= 0; i-- {
			if messages[i].Role != "user" {
				continue
			}
			parts := shortcutToolMessageParts(messages[i])
			for n, part := range parts {
				source := "message"
				title := fmt.Sprintf("当前消息引用 %d", n+1)
				switch part.Type {
				case "element":
					source = "browser"
					title = "网页元素引用"
				case "office":
					source = "office"
					title = part.Office.Title + " " + part.Office.Range
				case "file":
					source = "file"
					title = path.Base(part.Path)
				case "skill":
					source = "skill"
					title = part.Name
				case "mcp":
					source = "mcp"
					title = part.Server + "/" + part.Name
				case "shortcut":
					title = part.Title
				}
				all = append(all, b.issue(source, messages[i].ID+":"+strconv.Itoa(n), title, "", "", shortcutToolDigest(part), "message", []inputref.Part{part}))
			}
			break
		}
	}
	if include("skill") {
		available := b.m.Skills()
		b.s.mu.Lock()
		if b.agentID != "" {
			available = append([]skills.Skill{}, b.s.invocationSkills...)
		}
		b.s.mu.Unlock()
		for _, sk := range available {
			if sk.BrowserTask && !b.m.browserAllowed(b.owner) {
				continue
			}
			all = append(all, b.issue("skill", sk.Name, sk.Name, "", "", shortcutToolDigest(struct{ Name, Path string }{sk.Name, sk.Path}), "skill", []inputref.Part{{Type: "skill", Name: sk.Name}}))
		}
	}
	return all, nil
}

func informationShortcutToolParts(entry information.Entry) []inputref.Part {
	if entry.File != nil {
		return []inputref.Part{{Type: "file", Path: entry.File.Asset, AsFile: true}}
	}
	if q := entry.Shortcut; q != nil {
		return shortcutToolParts(userShortcut{ID: entry.ID + "_v" + strconv.FormatInt(entry.Version, 10), Title: q.Title, Content: q.Content, Icon: q.Icon, Language: q.Language, Parts: q.Parts, InputLayout: q.InputLayout})
	}
	return nil
}

func shortcutToolMessageParts(message Message) []inputref.Part {
	var parts []inputref.Part
	for i := 0; i < len(message.Content); i++ {
		block := message.Content[i]
		switch {
		case block.InputShortcut != nil:
			q := block.InputShortcut
			parts = append(parts, inputref.Part{Type: "shortcut", ShortcutID: q.ID, Title: q.Title, Text: q.Text, Icon: q.Icon, Language: q.Language, Parts: inputref.Clone(q.Parts)})
			i += min(max(q.BlockCount, 0), len(message.Content)-i-1)
		case block.InputOffice != nil:
			parts = append(parts, inputref.Clone([]inputref.Part{{Type: "office", Office: block.InputOffice}})...)
		case block.InputElement:
			parts = append(parts, inputref.Part{Type: "element", Text: block.Text, Favicon: block.InputElementFavicon})
		case block.InputMCP != nil:
			parts = append(parts, inputref.Part{Type: "mcp", Server: block.InputMCP.Server, Name: block.InputMCP.Name})
		case block.InputSkill != "":
			parts = append(parts, inputref.Part{Type: "skill", Name: block.InputSkill})
		case block.FilePath != "":
			parts = append(parts, inputref.Part{Type: "file", Path: block.FilePath, AsFile: true})
		case block.ImageRef != "":
			parts = append(parts, inputref.Part{Type: "file", Path: block.ImageRef, AsFile: true})
		case block.Text != "":
			parts = append(parts, inputref.Part{Type: "text", Text: block.Text})
		}
	}
	return parts
}

func (b *shortcutToolBackend) Search(ctx context.Context, in shortcuts.SearchRequest) (shortcuts.SearchResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out shortcuts.SearchResult
	out.Entries = []shortcuts.ReferenceSummary{}
	if err := b.access(ctx, false); err != nil {
		return out, err
	}
	valid := map[string]bool{"": true, "all": true, "shortcut": true, "quick_card": true, "information": true, "message": true, "browser": true, "office": true, "file": true, "skill": true}
	if !valid[in.Source] || in.Offset < 0 || in.Limit < 0 || in.Limit > 100 || len(in.Query) > 1024 || in.ParentID != "" && in.Source != "shortcut" {
		return out, errors.New("invalid shortcut search; parent_id requires source=shortcut; limit must be 1..100")
	}
	all, err := b.sourceReferences(ctx, in.Source, strings.TrimSpace(in.Query))
	if err != nil {
		return out, err
	}
	query := strings.ToLower(strings.TrimSpace(in.Query))
	var matches []shortcutToolReference
	for _, ref := range all {
		if in.Source != "" && in.Source != "all" && ref.summary.Source != in.Source && !(in.Source == "message" && ref.origin == "message") {
			continue
		}
		if in.ParentID != "" && ref.summary.ParentID != in.ParentID {
			continue
		}
		if query != "" && !strings.Contains(strings.ToLower(ref.summary.Title+"\n"+ref.searchText), query) {
			continue
		}
		matches = append(matches, ref)
	}
	sort.SliceStable(matches, func(i, j int) bool {
		a, c := matches[i].summary, matches[j].summary
		if a.Source != c.Source {
			return a.Source < c.Source
		}
		if a.Title != c.Title {
			return a.Title < c.Title
		}
		return a.RefID < c.RefID
	})
	out.Total = len(matches)
	out.Offset = min(in.Offset, len(matches))
	limit := in.Limit
	if limit == 0 {
		limit = 20
	}
	end := min(out.Offset+limit, len(matches))
	for _, ref := range matches[out.Offset:end] {
		if ref.summary.RefID != "" {
			if entry := ref.informationEntry; entry != nil {
				store, e := b.m.InformationStore(b.owner)
				if e != nil {
					return out, e
				}
				full, e := store.Get(entry.ID, entry.Version)
				if e != nil {
					return out, e
				}
				ref = b.issue("information", entry.ID, entry.Name, entry.ParentID, "", strconv.FormatInt(entry.Version, 10), "information", informationShortcutToolParts(full))
			}
			ref, err = b.freezeMessageFiles(ctx, ref)
			if err != nil {
				return out, err
			}
			if len(b.refs) >= 4096 {
				if _, ok := b.refs[ref.summary.RefID]; !ok {
					return out, errors.New("reference handle limit reached; start a new conversation")
				}
			}
			b.refs[ref.summary.RefID] = ref
		}
		out.Entries = append(out.Entries, ref.summary)
	}
	if end < len(matches) {
		out.NextOffset = &end
	}
	return out, nil
}

func (b *shortcutToolBackend) Read(ctx context.Context, in shortcuts.ReadRequest) (shortcuts.ReadResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out shortcuts.ReadResult
	if err := b.access(ctx, false); err != nil {
		return out, err
	}
	ref, ok := b.refs[in.RefID]
	if !ok {
		return out, errors.New("unknown reference handle; search this Agent's sources first")
	}
	out.Reference = ref.summary
	out.Parts = inputref.Clone(ref.parts)
	if len(ref.parts) == 1 && ref.parts[0].Type == "shortcut" {
		if len(b.refs)+len(flatInputParts(ref.parts)) > 4096 {
			return out, errors.New("reference handle limit reached; start a new conversation before copying this tree")
		}
		q := ref.parts[0]
		graph := shortcuts.Graph{RootID: "root"}
		var visit func(string, string, string, string, []inputref.Part) string
		visit = func(id, title, icon, language string, parts []inputref.Part) string {
			index := len(graph.Nodes)
			graph.Nodes = append(graph.Nodes, shortcuts.Node{LocalID: id, Title: title, Icon: icon, Language: language, Parts: []shortcuts.Part{}})
			for n, part := range parts {
				if part.Type == "text" {
					graph.Nodes[index].Parts = append(graph.Nodes[index].Parts, shortcuts.Part{Kind: "text", Text: part.Text})
				} else if part.Type == "shortcut" {
					childID := id + "_" + strconv.Itoa(n)
					childParts := part.Parts
					if len(childParts) == 0 {
						childParts = []inputref.Part{{Type: "text", Text: part.Text}}
					}
					visit(childID, part.Title, part.Icon, part.Language, childParts)
					graph.Nodes[index].Parts = append(graph.Nodes[index].Parts, shortcuts.Part{Kind: "node", NodeID: childID})
				} else {
					leaf := b.issue(ref.summary.Source, ref.summary.ID+":"+id+":"+strconv.Itoa(n), ref.summary.Title, "", "", ref.summary.Version, ref.origin, []inputref.Part{part})
					leaf.fileWitnesses = ref.fileWitnesses
					b.refs[leaf.summary.RefID] = leaf
					graph.Nodes[index].Parts = append(graph.Nodes[index].Parts, shortcuts.Part{Kind: "ref", RefID: leaf.summary.RefID})
				}
			}
			return id
		}
		parts := q.Parts
		if len(parts) == 0 {
			parts = []inputref.Part{{Type: "text", Text: q.Text}}
		}
		visit("root", q.Title, q.Icon, q.Language, parts)
		out.Graph = &graph
	}
	return out, nil
}

func (b *shortcutToolBackend) Create(ctx context.Context, in shortcuts.CreateRequest) (shortcuts.CreateResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out shortcuts.CreateResult
	if err := b.access(ctx, true); err != nil {
		return out, err
	}
	if err := shortcuts.ValidateCreateRequest(in); err != nil {
		return out, err
	}
	digest := shortcutToolDigest(in)
	if old, exists, err := b.m.toolShortcutReceipt(b.owner, b.agentID, in.RequestID, digest); err != nil || exists {
		if err == nil && old.Shortcut.ParentID != in.ParentID {
			return out, errors.New("the existing shortcut was moved; its current location and content were preserved; use a new request_id for a separate copy")
		}
		old.AgentID = b.agentID
		return old, err
	}
	if in.ParentID != "" {
		view, err := b.m.userShortcuts(b.owner, b.agentID)
		if err != nil {
			return out, err
		}
		found := false
		for _, folder := range view.Folders {
			found = found || folder.ID == in.ParentID
		}
		if !found {
			return out, errors.New("parent_id is not an existing private folder of this Agent")
		}
	}
	namespace, err := b.m.shortcutAssetNamespace(b.owner, b.agentID)
	if err != nil {
		return out, err
	}
	err = tools.WithFileMutationLock(namespace, func() error { var e error; out, e = b.createWithAssets(ctx, in, digest); return e })
	return out, err
}

func (b *shortcutToolBackend) createWithAssets(ctx context.Context, in shortcuts.CreateRequest, digest string) (shortcuts.CreateResult, error) {
	var out shortcuts.CreateResult
	batch := shortcutToolAssetBatch{b: b, plans: map[string]*shortcutToolAssetPlan{}}
	defer batch.close()
	compiled, err := shortcuts.Compile(ctx, b.scopeID, in.Graph, func(ctx context.Context, id string) (shortcuts.Reference, error) {
		ref, ok := b.refs[id]
		if !ok {
			return shortcuts.Reference{}, errors.New("unknown reference handle; search this Agent's sources first")
		}
		parts, err := batch.prepare(ctx, ref)
		if err != nil {
			return shortcuts.Reference{}, err
		}
		return shortcuts.Reference{ScopeID: b.scopeID, Version: ref.summary.Version, Parts: parts, SHA256: shortcuts.SnapshotSHA256(parts)}, nil
	})
	if err != nil {
		return out, err
	}
	if err = b.s.checkInputSkillAccess(compiled.Root.Parts); err != nil {
		return out, err
	}
	if err = b.access(ctx, true); err != nil {
		return out, err
	}
	if err = batch.stage(ctx); err != nil {
		return out, err
	}
	if err = b.access(ctx, true); err != nil {
		return out, err
	}
	x := compiled.Root
	entry := userShortcut{ID: x.ID, Title: x.Title, Content: x.Content, Icon: x.Icon, Language: x.Language, Parts: inputref.Clone(x.Parts), InputLayout: "inline", ParentID: in.ParentID}
	out, err = b.m.appendToolShortcut(b.owner, b.agentID, in.RequestID, digest, entry)
	if err != nil {
		return out, err
	}
	// A racing identical request may have committed in a sibling session. Its
	// receipt wins, and this attempt's unused staged assets are rolled back.
	batch.committed = !out.Reused
	out.AgentID = b.agentID
	if b.s.hub != nil {
		b.s.hub.Publish(Event{Type: EvShortcutsChanged, ShortcutAgentID: b.agentID, ShortcutRevision: out.Revision, ShortcutID: out.Shortcut.ID})
	}
	return out, nil
}
