package session

import "github.com/yosukeno/pogo/llm"

// Compaction is a durable model-context checkpoint on the visible transcript.
// Messages is the replacement prompt, not a user-visible message.
type Compaction struct {
	Summary      string        `json:"summary"`
	Instructions string        `json:"instructions,omitempty"`
	Before       int           `json:"before"`
	BeforeTokens int64         `json:"before_tokens"`
	AfterTokens  int64         `json:"after_tokens"`
	StartedAt    int64         `json:"started_at,omitempty"`
	FinishedAt   int64         `json:"finished_at,omitempty"`
	Messages     []llm.Message `json:"messages"`
}

// CompactionView is the UI-safe form of a checkpoint, anchored after a visible
// message count so its position survives snapshot reconstruction.
type CompactionView struct {
	ID           string `json:"id"`
	After        int    `json:"after"`
	Time         int64  `json:"ts"`
	Summary      string `json:"summary"`
	Instructions string `json:"instructions,omitempty"`
	Before       int    `json:"before"`
	BeforeTokens int64  `json:"before_tokens"`
	AfterTokens  int64  `json:"after_tokens"`
	StartedAt    int64  `json:"started_at,omitempty"`
	FinishedAt   int64  `json:"finished_at,omitempty"`
}

// AppendCompaction commits a checkpoint without forking or hiding any real messages.
func (s *Store) AppendCompaction(c Compaction) (CompactionView, error) {
	if err := s.append(Record{Type: "compaction", Compaction: &c}); err != nil {
		return CompactionView{}, err
	}
	record := s.records[len(s.records)-1]
	after := 0
	for _, r := range s.chain("") {
		if r.Type == "message" && r.Message != nil && visible(r.Message) {
			after++
		}
	}
	return compactionView(record, after), nil
}

// Compactions returns UI-safe checkpoints in transcript order.
func (s *Store) Compactions(leaf string) []CompactionView {
	var out []CompactionView
	after := 0
	for _, r := range s.chain(leaf) {
		if r.Type == "message" && r.Message != nil && visible(r.Message) {
			after++
		}
		if r.Type == "compaction" && r.Compaction != nil {
			out = append(out, compactionView(r, after))
		}
	}
	return out
}

func compactionView(r Record, after int) CompactionView {
	c := r.Compaction
	return CompactionView{
		ID: r.ID, After: after, Time: r.Time,
		Summary: c.Summary, Instructions: c.Instructions,
		Before: c.Before, BeforeTokens: c.BeforeTokens, AfterTokens: c.AfterTokens,
		StartedAt: c.StartedAt, FinishedAt: c.FinishedAt,
	}
}

// PromptTimedMessages reconstructs model context from the latest checkpoint and
// subsequent real messages. With no checkpoint it is exactly TimedMessages.
func (s *Store) PromptTimedMessages(leaf string) []Timed {
	records := s.chain(leaf)
	start := 0
	var checkpoint *Compaction
	var checkpointTime int64
	for i, r := range records {
		if r.Type == "compaction" && r.Compaction != nil && len(r.Compaction.Messages) > 0 {
			checkpoint, checkpointTime, start = r.Compaction, r.Time, i+1
		}
	}
	var raw []Timed
	if checkpoint != nil {
		for _, m := range checkpoint.Messages {
			raw = append(raw, Timed{Message: m, Time: checkpointTime})
		}
	}
	for _, r := range records[start:] {
		if r.Type == "message" && r.Message != nil {
			raw = append(raw, Timed{Message: *r.Message, Time: r.Time})
		}
	}
	out, _ := recoverToolResults(raw)
	return out
}
