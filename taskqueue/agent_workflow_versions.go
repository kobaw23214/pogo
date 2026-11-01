package taskqueue

// The original edit receipts already retain each saved definition. History is
// a read-only projection of those receipts, activation snapshots and actual
// Run definitions; opening history never backfills or rewrites them.
import (
	"encoding/json"
	"fmt"
	"sort"

	af "github.com/yosukeno/pogo/agentworkflow"
)

const awVersionPageSize = 100

type awRetainedVersion struct {
	definition af.Definition
	stepCount  int
	sources    map[string]bool
	priority   int
}

func awSnapshotMatches(d, current af.Definition) bool {
	return d.ID == current.ID && d.OwnerID == current.OwnerID && d.AgentID == current.AgentID &&
		d.Revision > 0 && d.Revision <= current.Revision && d.DefinitionHash == af.Hash(d)
}

func awVersionCurrent(q queryer, owner, agent, id string) (af.Definition, error) {
	d, err := awGet(q, owner, agent, id)
	if err != nil {
		return d, err
	}
	if d.ID != id || d.OwnerID != owner || d.AgentID != agent {
		return af.Definition{}, ErrConflict
	}
	return d, nil
}

func awVersionSummary(v awRetainedVersion, current af.Definition, enabled *af.Definition) af.VersionSummary {
	d := v.definition
	sources := []string{}
	for _, source := range []string{"draft", "saved", "enabled", "run"} {
		if v.sources[source] {
			sources = append(sources, source)
		}
	}
	return af.VersionSummary{VersionID: d.DefinitionHash, Revision: d.Revision, Name: d.Name, UpdatedAt: d.UpdatedAt,
		StepCount: v.stepCount, IsCurrentDraft: d.Revision == current.Revision && d.DefinitionHash == current.DefinitionHash,
		IsEnabled: enabled != nil && d.Revision == enabled.Revision && d.DefinitionHash == enabled.DefinitionHash, Sources: sources}
}

func awEnabledSnapshot(q queryer, current af.Definition) (*af.Definition, error) {
	if current.EnabledVersion == "" {
		return nil, nil
	}
	d, err := awDecodeDefinition(q.QueryRow(`SELECT definition FROM agent_workflow_versions WHERE workflow_id=? AND version_id=?`, current.ID, current.EnabledVersion))
	if err != nil {
		return nil, err
	}
	if !awSnapshotMatches(d, current) || d.DefinitionHash != current.EnabledVersion {
		return nil, ErrConflict
	}
	return &d, nil
}

func awRetainedVersions(q queryer, current af.Definition, before, exact int) (map[int]awRetainedVersion, error) {
	versions := map[int]awRetainedVersion{}
	add := func(raw []byte, source string, priority int) error {
		var d af.Definition
		if json.Unmarshal(raw, &d) != nil || !awSnapshotMatches(d, current) {
			return fmt.Errorf("%w: invalid retained workflow definition", ErrConflict)
		}
		if d.Archived || before > 0 && d.Revision >= before || exact > 0 && d.Revision != exact {
			return nil
		}
		stepCount := len(d.Steps)
		if exact == 0 {
			// A page keeps metadata only, rather than retaining up to 100 large
			// 200-node graphs. Each original body is validated and then released.
			d.Steps, d.Inputs, d.Graph, d.Description = nil, nil, nil, ""
		}
		old, exists := versions[d.Revision]
		if exists && old.definition.DefinitionHash != d.DefinitionHash {
			return fmt.Errorf("%w: ambiguous workflow revision", ErrConflict)
		}
		if !exists {
			old = awRetainedVersion{definition: d, stepCount: stepCount, sources: map[string]bool{}, priority: priority}
		}
		old.sources[source] = true
		// An immutable save receipt is the original save metadata, while the
		// current row can have a later activation timestamp at the same revision.
		if priority > old.priority {
			old.definition = d
			old.stepCount = stepCount
			old.priority = priority
		}
		versions[d.Revision] = old
		return nil
	}
	raw, _ := json.Marshal(current)
	if err := add(raw, "draft", 0); err != nil {
		return nil, err
	}
	queries := []struct {
		sql      string
		args     []any
		source   string
		priority int
	}{
		{`SELECT CASE WHEN json_type(CAST(response AS TEXT),'$.workflow')='object' THEN json_extract(CAST(response AS TEXT),'$.workflow') ELSE CAST(response AS TEXT) END FROM agent_workflow_edits WHERE owner_id=? AND agent_id=?`, []any{current.OwnerID, current.AgentID}, "saved", 3},
		{`SELECT CAST(definition AS TEXT) FROM agent_workflow_versions WHERE workflow_id=?`, []any{current.ID}, "enabled", 2},
		{`SELECT json_extract(CAST(record AS TEXT),'$.definition') FROM agent_workflow_runs WHERE owner_id=? AND agent_id=? AND workflow_id=?`, []any{current.OwnerID, current.AgentID, current.ID}, "run", 1},
	}
	for _, query := range queries {
		sql := `WITH snapshots(raw) AS (` + query.sql + `)
SELECT MIN(raw) FROM snapshots WHERE json_extract(raw,'$.id')=? AND json_extract(raw,'$.revision')<=?
AND COALESCE(json_extract(raw,'$.archived'),0)=0
AND (?=0 OR json_extract(raw,'$.revision')<?) AND (?=0 OR json_extract(raw,'$.revision')=?)
GROUP BY json_extract(raw,'$.revision'),json_extract(raw,'$.definition_hash')
ORDER BY json_extract(raw,'$.revision') DESC LIMIT 101`
		args := append(query.args, current.ID, current.Revision, before, before, exact, exact)
		rows, err := q.Query(sql, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var value []byte
			if err = rows.Scan(&value); err == nil {
				err = add(value, query.source, query.priority)
			}
			if err != nil {
				rows.Close()
				return nil, err
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return versions, nil
}

func (s *Store) AWVersions(owner, agent, id string, before int) (af.VersionPage, error) {
	page := af.VersionPage{Versions: []af.VersionSummary{}}
	if awScope(owner, agent) != nil || before < 0 {
		return page, ErrConflict
	}
	current, err := awVersionCurrent(s.db, owner, agent, id)
	if err != nil {
		return page, err
	}
	enabled, err := awEnabledSnapshot(s.db, current)
	if err != nil {
		return page, err
	}
	if enabled != nil {
		summary := awVersionSummary(awRetainedVersion{definition: *enabled, stepCount: len(enabled.Steps), sources: map[string]bool{"enabled": true}}, current, enabled)
		page.EnabledVersion = &summary
	}
	versions, err := awRetainedVersions(s.db, current, before, 0)
	if err != nil {
		return page, err
	}
	revisions := make([]int, 0, len(versions))
	for revision := range versions {
		revisions = append(revisions, revision)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(revisions)))
	page.HasMore = len(revisions) > awVersionPageSize
	if page.HasMore {
		revisions = revisions[:awVersionPageSize]
	}
	for _, revision := range revisions {
		page.Versions = append(page.Versions, awVersionSummary(versions[revision], current, enabled))
	}
	if page.HasMore {
		page.NextBeforeRevision = revisions[len(revisions)-1]
	}
	return page, nil
}

func awExactDefinitionVersion(q queryer, current af.Definition, hash string, revision int) (af.Definition, awRetainedVersion, error) {
	if !af.ValidHash(hash) || revision < 1 || revision > current.Revision {
		return af.Definition{}, awRetainedVersion{}, ErrNotFound
	}
	versions, err := awRetainedVersions(q, current, 0, revision)
	if err != nil {
		return af.Definition{}, awRetainedVersion{}, err
	}
	v, ok := versions[revision]
	if !ok || v.definition.DefinitionHash != hash {
		return af.Definition{}, awRetainedVersion{}, ErrNotFound
	}
	return v.definition, v, nil
}

func (s *Store) AWDefinitionVersion(owner, agent, id, hash string, revision int) (af.Definition, af.VersionSummary, error) {
	if err := awScope(owner, agent); err != nil {
		return af.Definition{}, af.VersionSummary{}, err
	}
	current, err := awVersionCurrent(s.db, owner, agent, id)
	if err != nil {
		return af.Definition{}, af.VersionSummary{}, err
	}
	d, v, err := awExactDefinitionVersion(s.db, current, hash, revision)
	if err != nil {
		return af.Definition{}, af.VersionSummary{}, err
	}
	enabled, err := awEnabledSnapshot(s.db, current)
	if err != nil {
		return af.Definition{}, af.VersionSummary{}, err
	}
	return d, awVersionSummary(v, current, enabled), nil
}
