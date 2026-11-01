// Package evidencebounds derives bounded literal/line relations solely from
// caller-verified bytes delivered to a model. It never reads result storage.
package evidencebounds

type Domain struct {
	ResultID string `json:"result_id"`
	Kind     string `json:"kind"`
	Pointer  string `json:"pointer,omitempty"`
}

type Fragment struct {
	ID                   string
	Domain               Domain
	Start, End           int
	Text                 string
	StartsLine, EndsLine bool
}

type Hit struct {
	Ordinal, Start, End int
}

// Each Search may be one actual page. Analyze unions pages with the same
// domain and literal, retaining every input ID as an alias of the selector.
type Search struct {
	ID           string
	Domain       Domain
	Pattern      string
	ScanComplete bool
	Total        int
	Hits         []Hit
}

// The relation asks whether left-selector matches not returned by that selector
// can share a complete delivered line with the right literal. "verified_disjoint"
// is a saved-text literal relation, never a verdict about business properties.
type RelationRequest struct{ LeftID, RightID string }

type Input struct {
	Fragments  []Fragment
	Searches   []Search
	Relations  []RelationRequest
	Incomplete bool // Caller ledger caps/invalid observations must not be hidden.
}

type Witness struct {
	Start          int      `json:"start"`
	End            int      `json:"end"`
	LineStart      int      `json:"line_start"`
	LineEnd        int      `json:"line_end"`
	LineSHA256     string   `json:"line_sha256"`
	FragmentIDs    []string `json:"fragment_ids"`
	GlobalHitKnown bool     `json:"global_hit_known"`
	Ordinal        *int     `json:"ordinal,omitempty"`
	OrdinalLower   *int     `json:"ordinal_lower,omitempty"`
	OrdinalUpper   *int     `json:"ordinal_upper_exclusive,omitempty"`
}

type Coverage struct {
	SearchID                 string    `json:"search_id"`
	IDs                      []string  `json:"search_ids"`
	Domain                   Domain    `json:"domain"`
	Pattern                  string    `json:"pattern"`
	Total                    int       `json:"total_matches"`
	NativeDelivered          int       `json:"selector_delivered_matches"`
	NativeCompleteBody       int       `json:"selector_complete_body_matches"`
	KnownCompleteBody        int       `json:"known_complete_body_matches"`
	OtherBodyCount           int       `json:"other_body_matches"`
	OtherBodyCountLowerBound bool      `json:"other_body_count_lower_bound,omitempty"`
	OtherBody                []Witness `json:"other_body,omitempty"`
	NativeComplete           bool      `json:"selector_complete"`
	CompleteBody             bool      `json:"all_matching_body_known"`
	Incomplete               bool      `json:"incomplete,omitempty"`
	Reasons                  []string  `json:"reasons,omitempty"`
}

type RelationWitness struct {
	Left  Witness `json:"left"`
	Right Witness `json:"right"`
}

type Relation struct {
	ID                string            `json:"id"`
	Domain            Domain            `json:"domain"`
	LeftSearchID      string            `json:"left_search_id"`
	RightSearchID     string            `json:"right_search_id"`
	LeftPattern       string            `json:"left_pattern"`
	RightPattern      string            `json:"right_pattern"`
	LeftIDs           []string          `json:"left_search_ids"`
	RightIDs          []string          `json:"right_search_ids"`
	Kind              string            `json:"kind"`
	Witnesses         []RelationWitness `json:"witnesses,omitempty"`
	LeftScanComplete  bool              `json:"left_scan_complete"`
	RightScanComplete bool              `json:"right_scan_complete"`
	SourceScope       string            `json:"source_scope"`
	Incomplete        bool              `json:"incomplete,omitempty"`
	Reasons           []string          `json:"reasons,omitempty"`
}

type Report struct {
	Coverage   []Coverage `json:"coverage"`
	Relations  []Relation `json:"relations"`
	Incomplete bool       `json:"incomplete,omitempty"`
	Reasons    []string   `json:"reasons,omitempty"`
}

const (
	MaxFragments   = 1024
	MaxBytes       = 512 << 10
	MaxSearches    = 512
	MaxSelectors   = 128
	MaxHits        = 32768
	MaxOccurrences = 32768
	MaxRelations   = 128
	MaxWitnesses   = 8
	MaxLines       = 65536
	MaxLineRefs    = 32
)
