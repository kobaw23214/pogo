package shortcuts

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/yosukeno/pogo/inputref"
	"github.com/yosukeno/pogo/skills"
)

var localID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,160}$`)

// ValidationError is a repairable draft/reference problem. No storage operation
// is performed by this package, including when validation fails late.
type ValidationError struct{ Code, Message string }

func (e *ValidationError) Error() string { return e.Code + ": " + e.Message }
func invalid(code, format string, args ...any) error {
	return &ValidationError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// ValidateCreateRequest checks model-owned fields before a host resolves or
// stages any source assets. Parent existence and RequestID idempotency are host
// checks because they are scoped to the authenticated owner and Agent.
func ValidateCreateRequest(r CreateRequest) error {
	if !localID.MatchString(r.RequestID) {
		return invalid("INVALID_REQUEST_ID", "request_id must contain 1-160 letters, digits, underscores or hyphens")
	}
	if r.ParentID != "" && !localID.MatchString(r.ParentID) {
		return invalid("INVALID_PARENT_ID", "parent_id must identify a private folder")
	}
	return ValidateGraph(r.Graph)
}

// ValidateGraph rejects malformed/unreachable/cyclic graphs before invoking a
// source resolver. Memoized heights make checking a shared DAG linear.
func ValidateGraph(g Graph) error { _, _, err := graph(g); return err }

func graph(g Graph) (map[string]Node, []string, error) {
	if !localID.MatchString(g.RootID) || len(g.Nodes) == 0 || len(g.Nodes) > MaxNodes {
		return nil, nil, invalid("INVALID_GRAPH", "root_id and 1-%d nodes are required", MaxNodes)
	}
	nodes := make(map[string]Node, len(g.Nodes))
	refs := map[string]bool{}
	count := 0
	for _, n := range g.Nodes {
		if !localID.MatchString(n.LocalID) {
			return nil, nil, invalid("INVALID_LOCAL_ID", "invalid local_id %q", n.LocalID)
		}
		if _, ok := nodes[n.LocalID]; ok {
			return nil, nil, invalid("DUPLICATE_NODE", "duplicate local_id %q", n.LocalID)
		}
		n.Title = strings.TrimSpace(n.Title)
		probe := inputref.Part{Type: "shortcut", ShortcutID: "sc_probe", Title: n.Title, Text: "valid", Icon: n.Icon, Language: n.Language}
		if len(n.Parts) == 0 || !skills.ValidInputShortcut(probe) {
			return nil, nil, invalid("INVALID_NODE", "invalid title, icon, language or empty parts for %q", n.LocalID)
		}
		for _, p := range n.Parts {
			count++
			if count > MaxNodes {
				return nil, nil, invalid("BUDGET_NODES", "flat input exceeds %d atoms", MaxNodes)
			}
			switch p.Kind {
			case "text":
				if p.RefID != "" || p.NodeID != "" || !utf8.ValidString(p.Text) {
					return nil, nil, invalid("INVALID_PART", "text accepts only text")
				}
			case "ref":
				if !localID.MatchString(p.RefID) || p.Text != "" || p.NodeID != "" {
					return nil, nil, invalid("INVALID_PART", "ref requires only a host-issued ref_id")
				}
				refs[p.RefID] = true
			case "node":
				if !localID.MatchString(p.NodeID) || p.Text != "" || p.RefID != "" {
					return nil, nil, invalid("INVALID_PART", "node requires only node_id")
				}
			default:
				return nil, nil, invalid("INVALID_PART", "unsupported kind %q", p.Kind)
			}
		}
		nodes[n.LocalID] = n
	}
	if _, ok := nodes[g.RootID]; !ok {
		return nil, nil, invalid("MISSING_ROOT", "root_id does not identify a node")
	}
	state := map[string]uint8{}
	height := map[string]int{}
	var visit func(string) (int, error)
	visit = func(id string) (int, error) {
		n, ok := nodes[id]
		if !ok {
			return 0, invalid("MISSING_NODE", "unknown node_id %q", id)
		}
		if state[id] == 1 {
			return 0, invalid("CYCLE", "node %q refers to an ancestor", id)
		}
		if state[id] == 2 {
			return height[id], nil
		}
		state[id] = 1
		h := 1
		for _, p := range n.Parts {
			if p.Kind == "node" {
				d, err := visit(p.NodeID)
				if err != nil {
					return 0, err
				}
				h = max(h, d+1)
			}
		}
		if h > MaxDepth {
			return 0, invalid("BUDGET_DEPTH", "shortcut nesting exceeds %d levels including the root", MaxDepth)
		}
		state[id] = 2
		height[id] = h
		return h, nil
	}
	if _, err := visit(g.RootID); err != nil {
		return nil, nil, err
	}
	if len(state) != len(nodes) {
		return nil, nil, invalid("UNREACHABLE_NODE", "every node must be reachable from root_id")
	}
	ids := make([]string, 0, len(refs))
	for id := range refs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return nodes, ids, nil
}

// SnapshotSHA256 hashes a frozen tree independently of struct property order.
// Host resolvers may omit SHA256 and let Compile derive it from verified bytes.
func SnapshotSHA256(parts []inputref.Part) string {
	raw, _ := json.Marshal(parts)
	var canonical any
	_ = json.Unmarshal(raw, &canonical)
	raw, _ = json.Marshal(canonical)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func partBytes(p inputref.Part) int {
	size := len(p.Type) + len(p.Text) + len(p.Path) + len(p.Name) + len(p.Favicon) + len(p.ShortcutID) + len(p.Title) + len(p.Icon) + len(p.Language)
	if o := p.Office; o != nil {
		size += len(o.Path) + len(o.Title) + len(o.Editor) + len(o.Text) + len(o.Sheet) + len(o.Range) + len(o.CanvasVersion) + len(o.CapturedAt) + len(o.SourceSession)
		if l := o.Locator; l != nil {
			size += len(l.Kind) + len(l.Exact) + len(l.Prefix) + len(l.Suffix)
		}
		if i := o.Information; i != nil {
			size += len(i.EntryID) + len(i.SnapshotSHA256) + 16
		}
	}
	return size
}

func addStats(a, b Stats) (Stats, error) {
	a.Nodes += b.Nodes
	a.Bytes += b.Bytes
	a.Depth = max(a.Depth, b.Depth)
	if a.Nodes > MaxNodes {
		return Stats{}, invalid("BUDGET_NODES", "expanded tree exceeds %d nodes", MaxNodes)
	}
	if a.Bytes > MaxBytes {
		return Stats{}, invalid("BUDGET_BYTES", "expanded tree exceeds %d KiB including frozen metadata and summaries", MaxBytes>>10)
	}
	if a.Depth > MaxDepth {
		return Stats{}, invalid("BUDGET_DEPTH", "expanded tree exceeds %d shortcut levels", MaxDepth)
	}
	return a, nil
}

// Fallback is a bounded display summary. It never copies nested snapshot bodies;
// the model/history serializer reads Parts once instead of appending this summary.
func Fallback(parts []inputref.Part) string {
	var b strings.Builder
	for _, p := range parts {
		switch p.Type {
		case "text":
			b.WriteString(p.Text)
		case "shortcut":
			fmt.Fprintf(&b, "〔资料：%s〕", p.Title)
		case "element":
			b.WriteString("〔网页引用〕")
		case "file":
			fmt.Fprintf(&b, "〔文件：%s〕", path.Base(p.Path))
		case "skill":
			fmt.Fprintf(&b, "〔技能：%s〕", p.Name)
		case "office":
			if p.Office != nil {
				fmt.Fprintf(&b, "〔Office：%s〕", p.Office.Title)
			}
		}
		// Nothing after the first 256 runes can affect the bounded summary. Limit
		// intermediate allocation even for a maximum-size text atom.
		if utf8.RuneCountInString(b.String()) > 256 {
			break
		}
	}
	text := []rune(b.String())
	if len(text) > 256 {
		text = append(text[:255], '…')
	}
	return string(text)
}

type compiler struct {
	nodes     map[string]Node
	refs      map[string]Reference
	summaries map[string]string
	memo      map[string]Stats
	ids       map[string]string
}

func referenceStats(parts []inputref.Part) (Stats, error) {
	var out Stats
	for _, p := range parts {
		next := Stats{Nodes: 1, Bytes: partBytes(p)}
		if p.Type == "shortcut" {
			next.Depth = 1
			if p.Parts != nil {
				children, err := referenceStats(p.Parts)
				if err != nil {
					return Stats{}, err
				}
				next.Nodes += children.Nodes
				next.Bytes += children.Bytes
				next.Depth += children.Depth
			}
		}
		var err error
		out, err = addStats(out, next)
		if err != nil {
			return Stats{}, err
		}
	}
	return out, nil
}

// measure computes each unique subtree once, but charges each occurrence when
// a parent repeats it. Huge shared DAGs are rejected before tree materialization.
func (c *compiler) measure(id string) (Stats, error) {
	if m, ok := c.memo[id]; ok {
		return m, nil
	}
	n := c.nodes[id]
	placeholders := make([]inputref.Part, 0, len(n.Parts))
	m := Stats{Nodes: 1, Bytes: partBytes(inputref.Part{Type: "shortcut", ShortcutID: "sc_" + strings.Repeat("0", 32), Title: n.Title, Icon: n.Icon, Language: n.Language}), Depth: 1}
	for _, p := range n.Parts {
		var child Stats
		switch p.Kind {
		case "text":
			v := inputref.Part{Type: "text", Text: p.Text}
			placeholders = append(placeholders, v)
			child = Stats{Nodes: 1, Bytes: partBytes(v)}
		case "ref":
			ref := c.refs[p.RefID]
			placeholders = append(placeholders, ref.Parts...)
			var err error
			child, err = referenceStats(ref.Parts)
			if err != nil {
				return Stats{}, err
			}
		case "node":
			var err error
			child, err = c.measure(p.NodeID)
			if err != nil {
				return Stats{}, err
			}
			placeholders = append(placeholders, inputref.Part{Type: "shortcut", Title: c.nodes[p.NodeID].Title})
		}
		child.Depth++
		var err error
		m, err = addStats(m, child)
		if err != nil {
			return Stats{}, err
		}
	}
	summary := Fallback(placeholders)
	m.Bytes += len(summary)
	if m.Bytes > MaxBytes {
		return Stats{}, invalid("BUDGET_BYTES", "expanded tree exceeds %d KiB including summaries", MaxBytes>>10)
	}
	c.summaries[id] = summary
	c.memo[id] = m
	return m, nil
}

func (c *compiler) node(id string) inputref.Part {
	n := c.nodes[id]
	root := inputref.Part{Type: "shortcut", ShortcutID: c.ids[id], Title: n.Title, Icon: n.Icon, Language: n.Language, Text: c.summaries[id], Parts: make([]inputref.Part, 0, len(n.Parts))}
	for _, p := range n.Parts {
		switch p.Kind {
		case "text":
			root.Parts = append(root.Parts, inputref.Part{Type: "text", Text: p.Text})
		case "ref":
			root.Parts = append(root.Parts, inputref.Clone(c.refs[p.RefID].Parts)...)
		case "node":
			root.Parts = append(root.Parts, c.node(p.NodeID))
		}
	}
	return root
}

func newID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "sc_" + hex.EncodeToString(raw[:]), nil
}

// Compile turns one graph into a complete immutable root snapshot. Resolver is
// called only after full graph validation and must authenticate each handle.
// File imports/transactions are the host's responsibility; Compile never writes.
// The complete expanded budget is checked before allocating expanded children.
func Compile(ctx context.Context, scopeID string, g Graph, resolve Resolver) (Compiled, error) {
	var out Compiled
	if err := ctx.Err(); err != nil {
		return out, err
	}
	nodes, refIDs, err := graph(g)
	if err != nil {
		return out, err
	}
	if scopeID == "" {
		return out, invalid("MISSING_HOST_SCOPE", "authenticated shortcut scope is unavailable")
	}
	refs := make(map[string]Reference, len(refIDs))
	existing := map[string]bool{}
	for _, id := range refIDs {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		if resolve == nil {
			return out, invalid("UNKNOWN_REFERENCE", "reference %q is unavailable", id)
		}
		ref, err := resolve(ctx, id)
		if err != nil {
			return out, fmt.Errorf("reference %q: %w", id, err)
		}
		if ref.ScopeID != scopeID {
			return out, invalid("REFERENCE_SCOPE", "reference %q belongs to another scope", id)
		}
		ref.Parts = inputref.Clone(ref.Parts)
		if msg := skills.ValidateInputParts(ref.Parts, "inline", "shortcut"); msg != "" {
			return out, invalid("INVALID_REFERENCE", "reference %q: %s", id, msg)
		}
		digest := SnapshotSHA256(ref.Parts)
		if ref.SHA256 != "" && ref.SHA256 != digest {
			return out, invalid("REFERENCE_DIGEST", "reference %q changed", id)
		}
		ref.SHA256 = digest
		refs[id] = ref
		for _, p := range inputref.Flatten(ref.Parts) {
			if p.Type == "shortcut" {
				existing[p.ShortcutID] = true
			}
		}
	}
	c := compiler{nodes: nodes, refs: refs, summaries: map[string]string{}, memo: map[string]Stats{}, ids: map[string]string{}}
	stats, err := c.measure(g.RootID)
	if err != nil {
		return out, err
	}
	for id := range nodes {
		for {
			generated, err := newID()
			if err != nil {
				return out, fmt.Errorf("generate shortcut identifier: %w", err)
			}
			if existing[generated] {
				continue
			}
			c.ids[id] = generated
			existing[generated] = true
			break
		}
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	root := c.node(g.RootID)
	if msg := skills.ValidateInputParts([]inputref.Part{root}, "inline", "shortcut"); msg != "" {
		return out, invalid("INVALID_RESULT", "%s", msg)
	}
	out = Compiled{Root: Entry{ID: root.ShortcutID, Title: root.Title, Content: root.Text, Icon: root.Icon, Language: root.Language, Parts: root.Parts, InputLayout: "inline"}, IDs: c.ids, Stats: stats, References: refs}
	return out, nil
}
