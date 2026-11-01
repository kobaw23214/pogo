package main

import (
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const semanticJudgeVersion = "pogo-coverage-semantic-v9"
const semanticJudgeModel = "glm-5.3"

const (
	semanticCoverageMaxBytes    = 16 << 10
	semanticJudgeInputMaxBytes  = 512 << 10
	semanticJudgeOutputMaxBytes = 64 << 10
	semanticJudgeMaxUnits       = 64
	semanticJudgeMaxAssertions  = 128
)

// The only answer supplied here is the existing final answer's coverage field,
// projected as {"coverage":...}. No oracle, full fixture, source checkpoint or
// tool/UI output can be added to this independently judged input.
type semanticCoverageInput struct {
	Answer string                 `json:"answer"`
	Ledger SemanticDeliveryLedger `json:"delivery_ledger"`
}

type semanticUnit struct {
	ID       string                     `json:"id"`
	Selector string                     `json:"selector"`
	Text     string                     `json:"text"`
	Anchors  []semanticQuantifierAnchor `json:"quantifier_anchors"`
}

type semanticQuantifierAnchor struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

type semanticUnitReview struct {
	UnitID       string   `json:"unit_id"`
	Complete     bool     `json:"complete"`
	AssertionIDs []string `json:"assertion_ids"`
}

type semanticAnchorReview struct {
	AnchorID    string `json:"anchor_id"`
	AssertionID string `json:"assertion_id"`
	// An anchor is an audit aid, never a keyword-based scope verdict. Negated
	// or expressly uncertain universals do not assert that every row was read.
	Interpretation string `json:"interpretation"` // affirmative, particular_extent, negated, conditional, scope_metadata, count_metadata, non_quantifier
}

type semanticAssertion struct {
	ID     string `json:"id"`
	UnitID string `json:"unit_id"`
	Quote  string `json:"quote"`
	// These optional annotations are accepted for old report/scripted-client
	// compatibility. The original quote and host unit, not a grader's English
	// paraphrase, define the assertion being audited.
	Claim        string `json:"claim,omitempty"`
	Selector     string `json:"selector,omitempty"`
	EvidenceKind string `json:"evidence_kind"` // scope_metadata, body_property, count
	Quantifier   string `json:"quantifier"`    // all, displayed_subset, count, none, unknown
	// Evidence IDs already belong to one host-validated result. Older graders
	// may echo that ID; an omitted echo binds to the ledger without changing
	// the grader's quote, classification or verdict.
	ResultID                   string             `json:"result_id,omitempty"`
	Pattern                    string             `json:"pattern"`
	Count                      *int               `json:"count"`
	EvidenceIDs                []string           `json:"evidence_ids"`
	Verdict                    string             `json:"verdict"` // supported, unsupported, unknown
	ReasonCode                 string             `json:"reason_code"`
	Domain                     *SemanticDomainRef `json:"domain"`
	RelationIDs                []string           `json:"relation_ids"`
	RelationPredicate          string             `json:"relation_predicate,omitempty"`
	RelationRightSearchPageIDs []string           `json:"relation_right_search_page_ids"`
}

type semanticDecision struct {
	Scope             string                 `json:"scope"` // saved_only, subset_only, remote_full, unknown
	AllClaimsReviewed bool                   `json:"all_claims_reviewed"`
	Reviews           []semanticUnitReview   `json:"reviews"`
	AnchorReviews     []semanticAnchorReview `json:"anchor_reviews"`
	Assertions        []semanticAssertion    `json:"assertions"`
}

type semanticAssertionGuard struct {
	AssertionID string   `json:"assertion_id"`
	Passed      bool     `json:"passed"`
	Reasons     []string `json:"reasons"`
}

// Each attempt is an independent, tool-free grader call. Only the final text's
// hash and typed decision are retained; provider reasoning is never recorded.
// A repair cannot erase the first failure or its measured cost.
type semanticAttemptReport struct {
	Phase            string                   `json:"phase"`
	SystemSHA256     string                   `json:"system_sha256"`
	InputSHA256      string                   `json:"input_sha256"`
	OutputSHA256     string                   `json:"output_sha256"`
	OutputBytes      int                      `json:"output_bytes"`
	WallMS           int64                    `json:"wall_ms"`
	Usage            usageReport              `json:"usage"`
	StreamCalls      int                      `json:"stream_calls"`
	StopReason       string                   `json:"stop_reason,omitempty"`
	ErrorClass       string                   `json:"error_class,omitempty"`
	Decision         *semanticDecision        `json:"decision,omitempty"`
	AssertionGuards  []semanticAssertionGuard `json:"assertion_guards"`
	ValidationErrors []string                 `json:"validation_errors"`
	Passed           bool                     `json:"passed"`
}

type semanticReport struct {
	Version          string                       `json:"version"`
	JudgeModel       string                       `json:"judge_model"`
	SystemSHA256     string                       `json:"system_sha256"`
	InputSHA256      string                       `json:"input_sha256,omitempty"`
	AnswerSHA256     string                       `json:"answer_sha256"`
	LedgerSHA256     string                       `json:"ledger_sha256,omitempty"`
	OutputSHA256     string                       `json:"output_sha256,omitempty"`
	OutputBytes      int                          `json:"output_bytes"`
	WallMS           int64                        `json:"wall_ms"`
	Usage            usageReport                  `json:"usage"`
	StreamCalls      int                          `json:"stream_calls"`
	StopReason       string                       `json:"stop_reason,omitempty"`
	ErrorClass       string                       `json:"error_class,omitempty"`
	Inventory        []semanticUnit               `json:"inventory"`
	Decision         *semanticDecision            `json:"decision,omitempty"`
	AssertionGuards  []semanticAssertionGuard     `json:"assertion_guards"`
	ValidationErrors []string                     `json:"validation_errors"`
	Passed           bool                         `json:"passed"`
	Attempts         []semanticAttemptReport      `json:"attempts"`
	RepairReason     string                       `json:"repair_reason,omitempty"`
	RepairSkipped    string                       `json:"repair_skipped,omitempty"`
	LiteralBounds    *SemanticLiteralBoundsReport `json:"literal_bounds,omitempty"`
}

// Extent anchors audit whether a particular row was completely delivered; they
// do not on their own assert cardinality over a selector. The independent judge
// still supplies that classification, and the actual complete-row gate applies.
var semanticParticularExtentAnchorRe = regexp.MustCompile(`(?i)^(?:full|complete|completely|完整)$`)

var semanticQuantifierRe = regexp.MustCompile(`(?i)\b(?:all|every|each|entire|entirety|full|complete|completely|exhaustive|exhaustively|total)\b|全部|所有|每一|每个|每条|全量|完整|均|无遗漏|总共|总计`)

// These are narrow misclassification tripwires, not a general semantic judge
// or a phrase-based acceptance rule. In particular, merely mentioning actual
// reading, recovery or a scoped subset does not trigger a body-universal claim.
var semanticPositiveBodyUniversalRe = regexp.MustCompile(`(?i)\b(?:all|every|each|they|those|these)\b.{0,100}\b(?:recovered|resolved|irrelevant|unrelated|benign|healthy|normal)\b|\b(?:all|every|each)\b.{0,80}\b(?:were|are|have\s+been|has\s+been)\s+(?:(?:all|fully|completely|already)\s+)?(?:read|reviewed|verified|checked)\b|(?:均|都|全部|所有).{0,16}(?:恢复|无关|正常|成功|读完|读过|已读|已阅|已核对|已检查|已验证)|(?:已经|已).{0,4}全部.{0,12}(?:读完|读取|核对|检查|恢复)`)
var semanticEpistemicLimitRe = regexp.MustCompile(`(?i)cannot|can't|unable\s+to|not\s+enough|does\s+not\s+prove|doesn't\s+prove|do\s+not\s+infer|no\s+claim|not\s+establish|not\s+guaranteed|cannot\s+conclude|不能|无法|不可推断|不代表|不足以|未证实|不保证|无从|不应推断|不能断言|不能认定`)
var semanticExplicitSubsetRe = regexp.MustCompile(`(?i)\b(?:displayed|shown|visible|subset)\b|\bdelivered\s+(?:rows|records|items|matches|fragments|lines)\b|this\s+page|these\s+fragments|first\s+[0-9]+|展示|显示|可见|本页|当前页|这些片段|已实际读到|已实际读取|实际读取的|实际读到的|所读片段|已交付的|实际交付的|已送达的|前[0-9一二三四五六七八九十]+条`)
var semanticScanRe = regexp.MustCompile(`(?i)\bscan(?:ned|ning)?\b|扫描`)

func semanticJSONBody(text string) (string, error) {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "```") {
		first := strings.IndexByte(text, '\n')
		if first < 0 || !strings.HasSuffix(text, "```") {
			return "", errors.New("invalid JSON fence")
		}
		marker := strings.ToLower(strings.TrimSpace(text[:first]))
		if marker != "```" && marker != "```json" {
			return "", errors.New("invalid JSON fence language")
		}
		text = strings.TrimSpace(text[first+1 : len(text)-3])
	}
	return text, nil
}

func semanticCoverageInventory(answer string) ([]semanticUnit, error) {
	if len(answer) > semanticCoverageMaxBytes || !utf8.ValidString(answer) {
		return nil, errors.New("invalid or oversized coverage input")
	}
	body, err := semanticJSONBody(answer)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(strings.NewReader(body))
	open, err := decoder.Token()
	if err != nil || open != json.Delim('{') {
		return nil, errors.New("coverage input must be an object")
	}
	var coverage string
	seen := false
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil || key != "coverage" || seen {
			return nil, errors.New("coverage input must contain only one coverage field")
		}
		seen = true
		if err := decoder.Decode(&coverage); err != nil {
			return nil, errors.New("coverage must be text")
		}
	}
	close, err := decoder.Token()
	if err != nil || close != json.Delim('}') || !seen {
		return nil, errors.New("invalid coverage object")
	}
	if _, err := decoder.Token(); err != io.EOF {
		return nil, errors.New("coverage input has trailing data")
	}
	if strings.TrimSpace(coverage) == "" {
		return nil, errors.New("coverage is empty")
	}
	clauses := semanticCoverageClauses(coverage)
	if len(clauses) == 0 || len(clauses) > semanticJudgeMaxUnits {
		return nil, errors.New("coverage clause count exceeds bound")
	}
	units := make([]semanticUnit, 0, len(clauses))
	anchorCount := 0
	for index, clause := range clauses {
		id := semanticSequenceID("unit", index+1)
		unit := semanticUnit{ID: id, Selector: "/coverage", Text: clause, Anchors: []semanticQuantifierAnchor{}}
		for anchorIndex, location := range semanticQuantifierRe.FindAllStringIndex(clause, -1) {
			anchorCount++
			if anchorCount > semanticJudgeMaxAssertions {
				return nil, errors.New("coverage quantifier audit count exceeds bound")
			}
			unit.Anchors = append(unit.Anchors, semanticQuantifierAnchor{ID: semanticSequenceID(id+"_anchor", anchorIndex+1), Text: clause[location[0]:location[1]]})
		}
		units = append(units, unit)
	}
	return units, nil
}

func semanticSequenceID(prefix string, index int) string {
	// These IDs are host-side inventory keys, unrelated to source S labels.
	const digits = "0123456789"
	return prefix + "_" + string([]byte{digits[index/100%10], digits[index/10%10], digits[index%10]})
}

func semanticCoverageClauses(coverage string) []string {
	var out []string
	start := 0
	for position, r := range coverage {
		// A comma can connect a preceding domain limitation to its following
		// proposition. Retain that original contiguous span instead of forcing
		// the grader to quote a detached pronoun without its bounded antecedent.
		separator := strings.ContainsRune("；;。！？!?\n\r", r)
		if r == '.' {
			next := position + 1
			separator = next == len(coverage)
			if next < len(coverage) {
				following, _ := utf8.DecodeRuneInString(coverage[next:])
				separator = unicode.IsSpace(following)
			}
		}
		if !separator {
			continue
		}
		if clause := strings.TrimSpace(coverage[start:position]); clause != "" {
			out = append(out, clause)
		}
		start = position + utf8.RuneLen(r)
	}
	if clause := strings.TrimSpace(coverage[start:]); clause != "" {
		out = append(out, clause)
	}
	return out
}
