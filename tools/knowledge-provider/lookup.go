package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// attckLookup answers malware.attck.lookup over the packaged snapshot:
// substring match across id / English name / Chinese name / aliases,
// case-insensitive on both sides, optional kind filter, bounded results.

type attckEntry struct {
	ID          string   `json:"id"`
	Kind        string   `json:"kind"`
	Name        string   `json:"name"`
	ZhName      string   `json:"zhName,omitempty"`
	Tactics     []string `json:"tactics,omitempty"`
	Aliases     []string `json:"aliases,omitempty"`
	Platform    string   `json:"platform,omitempty"`
	Description string   `json:"description"`
}

type attckSnapshot struct {
	Note    string       `json:"note"`
	Entries []attckEntry `json:"entries"`
}

type attckLookupInput struct {
	Query string `json:"query"`
	Kind  string `json:"kind,omitempty"`
	Limit int    `json:"limit,omitempty"`
}

type attckLookupOutput struct {
	Count   int          `json:"count"`
	Entries []attckEntry `json:"entries"`
	Note    string       `json:"note,omitempty"`
}

func attckLookup(path string, rawInput json.RawMessage) (json.RawMessage, error) {
	var in attckLookupInput
	if len(rawInput) > 0 {
		if err := json.Unmarshal(rawInput, &in); err != nil {
			return nil, &toolError{code: "INVALID_INPUT", message: err.Error()}
		}
	}
	q := strings.TrimSpace(in.Query)
	if q == "" {
		return nil, &toolError{code: "INVALID_INPUT", message: "query is required"}
	}
	switch in.Kind {
	case "", "technique", "group", "malware":
	default:
		return nil, &toolError{code: "INVALID_INPUT", message: fmt.Sprintf("kind must be technique, group or malware, got %q", in.Kind)}
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 5
	}
	if limit > 20 {
		limit = 20
	}
	snap, err := loadAttck(path)
	if err != nil {
		return nil, err
	}
	needle := strings.ToLower(q)
	var hits []attckEntry
	for _, e := range snap.Entries {
		if in.Kind != "" && e.Kind != in.Kind {
			continue
		}
		if !entryMatches(e, needle) {
			continue
		}
		hits = append(hits, e)
	}
	// Deterministic order: exact id first, then exact names, then the rest by id.
	sort.SliceStable(hits, func(i, j int) bool { return rank(hits[i], needle) < rank(hits[j], needle) })
	if len(hits) > limit {
		hits = hits[:limit]
	}
	out := attckLookupOutput{Count: len(hits), Entries: hits}
	if out.Entries == nil {
		out.Entries = []attckEntry{}
	}
	// Zero hits is an honest empty answer, distinct from a failed lookup.
	raw, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

func loadAttck(path string) (*attckSnapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, &toolError{code: "ASSET_MISSING", message: err.Error()}
	}
	var snap attckSnapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, &toolError{code: "ASSET_INVALID", message: err.Error()}
	}
	return &snap, nil
}

func entryMatches(e attckEntry, lowerNeedle string) bool {
	if strings.Contains(strings.ToLower(e.ID), lowerNeedle) ||
		strings.Contains(strings.ToLower(e.Name), lowerNeedle) ||
		strings.Contains(strings.ToLower(e.ZhName), lowerNeedle) {
		return true
	}
	for _, a := range e.Aliases {
		if strings.Contains(strings.ToLower(a), lowerNeedle) {
			return true
		}
	}
	for _, t := range e.Tactics {
		if strings.Contains(strings.ToLower(t), lowerNeedle) {
			return true
		}
	}
	return false
}

func rank(e attckEntry, lowerNeedle string) int {
	if strings.EqualFold(e.ID, lowerNeedle) {
		return 0
	}
	if strings.EqualFold(e.Name, lowerNeedle) || strings.EqualFold(e.ZhName, lowerNeedle) {
		return 1
	}
	if strings.Contains(strings.ToLower(e.ID), lowerNeedle) {
		return 2
	}
	return 3
}
