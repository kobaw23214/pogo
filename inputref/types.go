// Package inputref contains immutable, user-authored reference snapshots.
// It carries display data only, never grants access to a source or capability.
package inputref

type Part struct {
	Type       string          `json:"type"`
	Text       string          `json:"text,omitempty"`
	Name       string          `json:"name,omitempty"`
	Server     string          `json:"server,omitempty"`
	Path       string          `json:"path,omitempty"`
	AsFile     bool            `json:"as_file,omitempty"`
	Favicon    string          `json:"favicon,omitempty"`
	ShortcutID string          `json:"shortcut_id,omitempty"`
	Title      string          `json:"title,omitempty"`
	Icon       string          `json:"icon,omitempty"`
	Language   string          `json:"language,omitempty"`
	Parts      []Part          `json:"parts,omitempty"`
	Office     *OfficeSnapshot `json:"office,omitempty"`
}

// MCPReference identifies a user-selected method, never a capability grant or
// an executable call. The live session resolves this pair independently.
type MCPReference struct {
	Server string `json:"server"`
	Name   string `json:"name"`
}

type OfficeLocator struct {
	Kind   string `json:"kind"`
	Exact  string `json:"exact"`
	Prefix string `json:"prefix,omitempty"`
	Suffix string `json:"suffix,omitempty"`
}

type OfficeSnapshot struct {
	Path          string `json:"path"`
	Title         string `json:"title"`
	Editor        string `json:"editor"`
	Text          string `json:"text"`
	Sheet         string `json:"sheet,omitempty"`
	Range         string `json:"range,omitempty"`
	CanvasVersion string `json:"canvas_version"`
	DirtyEpoch    uint64 `json:"dirty_epoch,omitempty"`
	Dirty         bool   `json:"dirty,omitempty"`
	CapturedAt    string `json:"captured_at,omitempty"`
	// SourceSession is the Pogo conversation at capture time, never an access token.
	SourceSession string         `json:"source_session,omitempty"`
	Locator       *OfficeLocator `json:"locator,omitempty"`
	// Information marks a selection made from an immutable saved library file,
	// not from a live editing canvas. It grants no workspace or source-session access.
	Information *InformationOrigin `json:"information,omitempty"`
}

type InformationOrigin struct {
	EntryID        string `json:"entry_id"`
	Version        int64  `json:"version"`
	SnapshotSHA256 string `json:"snapshot_sha256"`
}

// Clone detaches all nested slices and pointers from a caller's draft.
func Clone(parts []Part) []Part {
	if parts == nil {
		return nil
	}
	out := make([]Part, len(parts))
	for i, p := range parts {
		out[i] = p
		out[i].Parts = Clone(p.Parts)
		if p.Office != nil {
			o := *p.Office
			if o.Locator != nil {
				l := *o.Locator
				o.Locator = &l
			}
			if o.Information != nil {
				origin := *o.Information
				o.Information = &origin
			}
			out[i].Office = &o
		}
	}
	return out
}

// Flatten returns preorder nodes, including container nodes. Callers validate
// budgets first; shared order is also the index used for explicit Skill atoms.
func Flatten(parts []Part) []Part {
	var out []Part
	var walk func([]Part)
	walk = func(ps []Part) {
		for _, p := range ps {
			out = append(out, p)
			walk(p.Parts)
		}
	}
	walk(parts)
	return out
}
