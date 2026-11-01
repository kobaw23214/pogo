package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"unicode/utf8"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

const (
	sourceCheckpointVersion    = 1
	sourceCheckpointMaxBytes   = 8 << 20
	sourceCheckpointMaxEntries = 100000
)

// This is host bookkeeping, not model evidence or a storage capability. A
// binding still has to pass the protected reader's session-scoped Store.Load.
type sourceCheckpoint struct {
	Version  int                       `json:"version"`
	Seq      int                       `json:"seq"`
	Sources  []Source                  `json:"sources"`
	Bindings []sourceCheckpointBinding `json:"bindings"`
	Reserved []string                  `json:"reserved"`
}

type sourceCheckpointBinding struct {
	ResultID    string `json:"result_id"`
	State       string `json:"state"` // source, none (explicit non-evidence), ambiguous
	SourceLabel string `json:"source_label,omitempty"`
}

type sourceStoreSnapshot struct {
	seq      int
	byLabel  map[string]*Source
	all      []*Source
	reserved map[string]struct{}
	bindings map[string]resultSourceBinding
}

func (s *SourceStore) sourceSnapshot() sourceStoreSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	snapshot := sourceStoreSnapshot{seq: s.seq, byLabel: make(map[string]*Source, len(s.byLabel)), all: append([]*Source(nil), s.all...), reserved: make(map[string]struct{}, len(s.reserved)), bindings: make(map[string]resultSourceBinding, len(s.resultSources))}
	for label, source := range s.byLabel {
		snapshot.byLabel[label] = source
	}
	for label := range s.reserved {
		snapshot.reserved[label] = struct{}{}
	}
	for id, binding := range s.resultSources {
		snapshot.bindings[id] = binding
	}
	return snapshot
}

// Budget preparation during summarise can add bindings, but does not register
// sources or spend labels. Undo just those additions on a failed compaction.
// Never erase sources added by an unsupported concurrent writer.
func (s *SourceStore) restoreFailedCompactBindings(before sourceStoreSnapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.seq != before.seq || len(s.byLabel) != len(before.byLabel) || len(s.reserved) != len(before.reserved) {
		return
	}
	for label, source := range before.byLabel {
		if s.byLabel[label] != source {
			return
		}
	}
	for label := range before.reserved {
		if _, found := s.reserved[label]; !found {
			return
		}
	}
	s.resultSources = before.bindings
}

func checkpointFromSnapshot(snapshot sourceStoreSnapshot) sourceCheckpoint {
	checkpoint := sourceCheckpoint{Version: sourceCheckpointVersion, Seq: snapshot.seq, Sources: []Source{}, Bindings: []sourceCheckpointBinding{}, Reserved: []string{}}
	for _, source := range snapshot.byLabel {
		if source != nil {
			checkpoint.Sources = append(checkpoint.Sources, *source)
		}
	}
	for id, binding := range snapshot.bindings {
		entry := sourceCheckpointBinding{ResultID: id, State: "none"}
		if binding.ambiguous {
			entry.State = "ambiguous"
		} else if binding.source != nil {
			entry.State, entry.SourceLabel = "source", binding.source.Label
		}
		checkpoint.Bindings = append(checkpoint.Bindings, entry)
	}
	for label := range snapshot.reserved {
		checkpoint.Reserved = append(checkpoint.Reserved, label)
	}
	sort.Slice(checkpoint.Sources, func(i, j int) bool {
		a, _ := sourceLabelNumber(checkpoint.Sources[i].Label)
		b, _ := sourceLabelNumber(checkpoint.Sources[j].Label)
		return a < b
	})
	sort.Slice(checkpoint.Bindings, func(i, j int) bool { return checkpoint.Bindings[i].ResultID < checkpoint.Bindings[j].ResultID })
	sort.Strings(checkpoint.Reserved)
	return checkpoint
}

func sourceCheckpointSameIdentity(a, b *Source) bool {
	if a == nil || b == nil {
		return a == b
	}
	if a.Label != b.Label || a.Type != b.Type || a.Locator != b.Locator || a.CallID != b.CallID || a.Hash != b.Hash || a.Time != b.Time {
		return false
	}
	left, right := a.SavedResult, b.SavedResult
	if left == nil || right == nil {
		return left == right
	}
	if left.ResultID != right.ResultID || left.View != right.View || left.Pointer != right.Pointer || left.OriginAmbiguous != right.OriginAmbiguous {
		return false
	}
	// Cosmetic title/excerpt changes in duplicate historic stamps must not
	// replace their first reachable record, nor count as a different artifact.
	leftQuery, leftErr := json.Marshal(left.Query)
	rightQuery, rightErr := json.Marshal(right.Query)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftQuery, rightQuery) && sourceCheckpointSameIdentity(left.Origin, right.Origin)
}

// The candidate is detached from live metadata. View-only IDs are host-created
// too, but only a cache entry still matched to its actual block may contribute.
func (a *Agent) compactSourceCheckpoint(summary string) (json.RawMessage, error) {
	if a.sources == nil {
		return nil, nil
	}
	snapshot := a.sources.sourceSnapshot()
	for position := range a.toolResultViews {
		if position.message < 0 || position.message >= len(a.messages) {
			continue
		}
		message := a.messages[position.message]
		if position.block < 0 || position.block >= len(message.Content) {
			continue
		}
		block := message.Content[position.block]
		view, matched := a.resultViewAt(position, block)
		if !matched || block.Type != llm.BlockToolResult || !sourceResultID(view.resultID) {
			continue
		}
		var source *Source
		if len(block.Source) > 0 {
			var saved Source
			if json.Unmarshal(block.Source, &saved) != nil {
				continue
			}
			known := snapshot.byLabel[saved.Label]
			if known == nil || known.CallID != saved.CallID || known.Hash != saved.Hash {
				continue
			}
			source = known
		}
		binding := resultSourceBinding{source: source}
		if previous, exists := snapshot.bindings[view.resultID]; exists {
			if previous.ambiguous || !sameResultSource(previous.source, source) {
				binding = resultSourceBinding{ambiguous: true}
			} else {
				binding = previous
			}
		}
		snapshot.bindings[view.resultID] = binding
	}
	// A summary can quote an unknown anchor. Reserving it does not make it
	// evidence; it prevents a future source from accidentally giving it meaning.
	reserved := &SourceStore{reserved: snapshot.reserved}
	reserved.reserveCitations(summary)
	checkpoint := checkpointFromSnapshot(snapshot)
	if len(checkpoint.Sources) > sourceCheckpointMaxEntries || len(checkpoint.Bindings) > sourceCheckpointMaxEntries || len(checkpoint.Reserved) > sourceCheckpointMaxEntries {
		return nil, errors.New("source checkpoint exceeds entry limit; conversation unchanged")
	}
	// Bound the aggregate before encoding a possibly large source registry into
	// another complete buffer. Individual sources are ordinary small metadata.
	size := 0
	for _, source := range checkpoint.Sources {
		encoded, err := json.Marshal(source)
		if err != nil {
			return nil, errors.New("source checkpoint could not be encoded; conversation unchanged")
		}
		size += len(encoded)
		if size > sourceCheckpointMaxBytes {
			return nil, errors.New("source checkpoint exceeds 8 MiB; conversation unchanged")
		}
	}
	for _, binding := range checkpoint.Bindings {
		size += len(binding.ResultID) + len(binding.State) + len(binding.SourceLabel) + 64
		if size > sourceCheckpointMaxBytes {
			return nil, errors.New("source checkpoint exceeds 8 MiB; conversation unchanged")
		}
	}
	for _, label := range checkpoint.Reserved {
		size += len(label) + 4
		if size > sourceCheckpointMaxBytes {
			return nil, errors.New("source checkpoint exceeds 8 MiB; conversation unchanged")
		}
	}
	raw, err := json.Marshal(checkpoint)
	if err != nil {
		return nil, errors.New("source checkpoint could not be encoded; conversation unchanged")
	}
	if _, err = decodeSourceCheckpoint(raw); err != nil {
		return nil, fmt.Errorf("source checkpoint could not be retained; conversation unchanged: %w", err)
	}
	return raw, nil
}

// Reject duplicate keys at every nesting level before decoding typed metadata.
// Raw Query JSON remains data; this walk never interprets or executes it.
func sourceCheckpointJSONValue(decoder *json.Decoder, depth int) error {
	if depth > 64 {
		return errors.New("source checkpoint nesting exceeds limit")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, compound := token.(json.Delim)
	if !compound {
		return nil
	}
	switch delimiter {
	case '{':
		keys := map[string]bool{}
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || keys[name] {
				return errors.New("source checkpoint contains duplicate keys")
			}
			keys[name] = true
			if err := sourceCheckpointJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for decoder.More() {
			if err := sourceCheckpointJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
	default:
		return errors.New("invalid source checkpoint JSON")
	}
	_, err = decoder.Token()
	return err
}

func decodeSourceCheckpoint(raw json.RawMessage) (sourceCheckpoint, error) {
	var checkpoint sourceCheckpoint
	if len(raw) == 0 || len(raw) > sourceCheckpointMaxBytes || !utf8.Valid(raw) {
		return checkpoint, errors.New("source checkpoint is empty, invalid UTF-8 or exceeds 8 MiB")
	}
	shape := json.NewDecoder(bytes.NewReader(raw))
	if err := sourceCheckpointJSONValue(shape, 0); err != nil {
		return checkpoint, err
	}
	if _, err := shape.Token(); err != io.EOF {
		return checkpoint, errors.New("source checkpoint must be one JSON object")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&checkpoint); err != nil {
		return checkpoint, errors.New("invalid source checkpoint fields")
	}
	if checkpoint.Version != sourceCheckpointVersion || checkpoint.Seq < 0 || checkpoint.Seq == int(^uint(0)>>1) {
		return checkpoint, errors.New("unsupported source checkpoint version or sequence")
	}
	if len(checkpoint.Sources) > sourceCheckpointMaxEntries || len(checkpoint.Bindings) > sourceCheckpointMaxEntries || len(checkpoint.Reserved) > sourceCheckpointMaxEntries {
		return checkpoint, errors.New("source checkpoint exceeds entry limit")
	}
	sources := make(map[string]*Source, len(checkpoint.Sources))
	for index := range checkpoint.Sources {
		source := &checkpoint.Sources[index]
		n, valid := sourceLabelNumber(source.Label)
		if !valid || n > checkpoint.Seq || source.Type == "" || source.Locator == "" || source.CallID == "" || source.Hash == "" || source.Time <= 0 || sources[source.Label] != nil {
			return checkpoint, errors.New("source checkpoint contains invalid or duplicate source identity")
		}
		if ref := source.SavedResult; ref != nil {
			if !sourceResultID(ref.ResultID) || ref.View == "" || ref.OriginAmbiguous && ref.Origin != nil || ref.Origin != nil && ref.Origin.SavedResult != nil || len(ref.Query) > 0 && !json.Valid(ref.Query) {
				return checkpoint, errors.New("source checkpoint contains an invalid saved-result parent")
			}
		}
		sources[source.Label] = source
	}
	for _, source := range checkpoint.Sources {
		if ref := source.SavedResult; ref != nil && ref.Origin != nil {
			original := sources[ref.Origin.Label]
			if original == nil || !sourceCheckpointSameIdentity(original, ref.Origin) {
				return checkpoint, errors.New("source checkpoint parent identity is missing or inconsistent")
			}
		}
	}
	bindings := make(map[string]bool, len(checkpoint.Bindings))
	for _, binding := range checkpoint.Bindings {
		if !sourceResultID(binding.ResultID) || bindings[binding.ResultID] {
			return checkpoint, errors.New("source checkpoint contains invalid or duplicate result binding")
		}
		bindings[binding.ResultID] = true
		switch binding.State {
		case "source":
			if sources[binding.SourceLabel] == nil {
				return checkpoint, errors.New("source checkpoint result has no source identity")
			}
		case "none", "ambiguous":
			if binding.SourceLabel != "" {
				return checkpoint, errors.New("source checkpoint non-evidence binding has a source")
			}
		default:
			return checkpoint, errors.New("unknown source checkpoint binding state")
		}
	}
	reserved := make(map[string]bool, len(checkpoint.Reserved))
	for _, label := range checkpoint.Reserved {
		if _, valid := sourceLabelNumber(label); !valid || reserved[label] {
			return checkpoint, errors.New("source checkpoint contains invalid reserved label")
		}
		reserved[label] = true
	}
	return checkpoint, nil
}

func (s *SourceStore) invalidateCheckpointLabelLocked(label string, byLabel map[string]*Source, blocked map[string]bool) {
	blocked[label] = true
	delete(byLabel, label)
	if s.reserved == nil {
		s.reserved = make(map[string]struct{})
	}
	s.reserved[label] = struct{}{}
	for id, binding := range s.resultSources {
		if binding.source != nil && binding.source.Label == label {
			s.resultSources[id] = resultSourceBinding{ambiguous: true}
		}
	}
	for name, source := range byLabel {
		if ref := source.SavedResult; ref != nil && ref.Origin != nil && ref.Origin.Label == label {
			copy, reference := *source, *ref
			reference.Origin = nil
			reference.OriginAmbiguous = true
			copy.SavedResult = &reference
			byLabel[name] = &copy
		}
	}
}

func (s *SourceStore) ambiguousCheckpointBlockLocked(toolName string, block llm.Block, source *Source) {
	mark := func(id string) {
		if sourceResultID(id) {
			s.resultSources[id] = resultSourceBinding{ambiguous: true}
		}
	}
	mark(block.ResultID)
	if source != nil && source.SavedResult != nil {
		mark(source.SavedResult.ResultID)
	}
	if toolName == tools.ToolResultReadName && !block.IsError {
		var read tools.ToolResultReadDetails
		if json.Unmarshal(block.Details, &read) == nil {
			mark(read.ResultID)
		}
	}
}

func (s *SourceStore) observeSourceCheckpointLocked(raw json.RawMessage, byLabel map[string]*Source, blocked map[string]bool) {
	checkpoint, err := decodeSourceCheckpoint(raw)
	if err != nil {
		return
	}
	conflict := false
	for index := range checkpoint.Sources {
		source := &checkpoint.Sources[index]
		if blocked[source.Label] {
			conflict = true
			continue
		}
		if existing := byLabel[source.Label]; existing != nil && !sourceCheckpointSameIdentity(existing, source) {
			s.invalidateCheckpointLabelLocked(source.Label, byLabel, blocked)
			conflict = true
		}
	}
	if conflict {
		return
	}
	if checkpoint.Seq > s.seq {
		s.seq = checkpoint.Seq
	}
	if s.reserved == nil {
		s.reserved = make(map[string]struct{})
	}
	for _, label := range checkpoint.Reserved {
		s.reserved[label] = struct{}{}
	}
	for index := range checkpoint.Sources {
		source := &checkpoint.Sources[index]
		if byLabel[source.Label] == nil {
			byLabel[source.Label] = source
		}
	}
	for _, binding := range checkpoint.Bindings {
		if binding.State == "ambiguous" {
			s.resultSources[binding.ResultID] = resultSourceBinding{ambiguous: true}
			continue
		}
		s.bindResultLocked(binding.ResultID, byLabel[binding.SourceLabel])
	}
}
