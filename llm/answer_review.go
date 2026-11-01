package llm

// AnswerReviewStatus is host-only progress metadata for an optional answer
// rewrite. It is persisted for the UI, never sent as part of a model request.
type AnswerReviewStatus struct {
	Kind       string `json:"kind"`
	State      string `json:"state"`
	Turn       int    `json:"turn"`
	StartedAt  int64  `json:"started_at"`
	FinishedAt int64  `json:"finished_at,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
	Reason     string `json:"reason,omitempty"`
	MessageID  string `json:"message_id,omitempty"`
}

func (s *AnswerReviewStatus) Clone() *AnswerReviewStatus {
	if s == nil {
		return nil
	}
	copy := *s
	return &copy
}
