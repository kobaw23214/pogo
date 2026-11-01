package shortcuts

// Management uses stable host IDs and whole-library CAS. An empty ParentID in
// List and Move means the private root, unlike Search's unfiltered empty value.
const (
	InformationFolderID = "@information"
	QuickCardsFolderID  = "@quick-cards"
)

type Folder struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	ParentID string `json:"parent_id,omitempty"`
	ReadOnly bool   `json:"read_only,omitempty"`
}

type ListRequest struct {
	ParentID string `json:"parent_id,omitempty"`
	Offset   int    `json:"offset,omitempty"`
	Limit    int    `json:"limit,omitempty"`
}

type ListResult struct {
	ParentID    string             `json:"parent_id"`
	Revision    int64              `json:"revision"`
	ReadOnly    bool               `json:"read_only"`
	Breadcrumbs []Folder           `json:"breadcrumbs"`
	Entries     []ReferenceSummary `json:"entries"`
	Total       int                `json:"total"`
	Offset      int                `json:"offset"`
	NextOffset  *int               `json:"next_offset,omitempty"`
}

type MutationRequest struct {
	RequestID        string `json:"request_id"`
	ExpectedRevision int64  `json:"expected_revision"`
}

type FolderCreateRequest struct {
	MutationRequest
	Name     string `json:"name"`
	ParentID string `json:"parent_id,omitempty"`
}

// A metadata-only edit leaves the ordered snapshot byte-for-byte intact.
// A Graph replaces the card's complete ordered content; metadata fields and
// Graph are mutually exclusive so an ambiguous partial overwrite is rejected.
type UpdateRequest struct {
	MutationRequest
	ID       string  `json:"id"`
	Title    *string `json:"title,omitempty"`
	Icon     *string `json:"icon,omitempty"`
	Language *string `json:"language,omitempty"`
	Graph    *Graph  `json:"graph,omitempty"`
}

type FolderRenameRequest struct {
	MutationRequest
	ID   string `json:"id"`
	Name string `json:"name"`
}

type MoveRequest struct {
	MutationRequest
	IDs      []string `json:"ids"`
	ParentID string   `json:"parent_id"`
}

type DeleteRequest struct {
	MutationRequest
	IDs       []string `json:"ids"`
	Recursive bool     `json:"recursive,omitempty"`
}

// Preview is the default. Apply must replay the exact host-issued plan and
// revision. Only entirely equal cards in the same private folder are grouped.
type MergeRequest struct {
	MutationRequest
	Apply  bool   `json:"apply,omitempty"`
	PlanID string `json:"plan_id,omitempty"`
}

type Change struct {
	Action       string `json:"action"`
	Kind         string `json:"kind"`
	ID           string `json:"id"`
	Title        string `json:"title"`
	FromParentID string `json:"from_parent_id,omitempty"`
	ToParentID   string `json:"to_parent_id,omitempty"`
}

type MergeGroup struct {
	KeepID       string   `json:"keep_id"`
	DuplicateIDs []string `json:"duplicate_ids"`
	Title        string   `json:"title"`
	ParentID     string   `json:"parent_id,omitempty"`
}

type MergePlan struct {
	ID       string       `json:"id"`
	Revision int64        `json:"revision"`
	Groups   []MergeGroup `json:"groups"`
	Removals int          `json:"removals"`
}

type MutationResult struct {
	AgentID         string     `json:"agent_id"`
	Action          string     `json:"action"`
	Saved           bool       `json:"saved"`
	Changed         bool       `json:"changed"`
	Revision        int64      `json:"revision"`
	AppliedRevision int64      `json:"applied_revision,omitempty"`
	RequestID       string     `json:"request_id,omitempty"`
	Reused          bool       `json:"reused,omitempty"`
	Shortcut        *Entry     `json:"shortcut,omitempty"`
	Folder          *Folder    `json:"folder,omitempty"`
	Changes         []Change   `json:"changes"`
	Plan            *MergePlan `json:"plan,omitempty"`
}
