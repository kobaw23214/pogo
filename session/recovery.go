package session

import (
	"fmt"

	"github.com/yosukeno/pogo/llm"
)

// committedRecords filters incomplete logical appends before selecting the head
// or repairing parent links. Batch fields are optional for old JSONL files.
func committedRecords(records []Record, diags *[]string) ([]Record, int) {
	out := make([]Record, 0, len(records))
	dropped := 0
	for i := 0; i < len(records); {
		r := records[i]
		if r.BatchID == "" && r.BatchSize == 0 && r.BatchIndex == 0 {
			out = append(out, r)
			i++
			continue
		}
		end := i + 1
		for r.BatchID != "" && end < len(records) && records[end].BatchID == r.BatchID {
			end++
		}
		valid := r.BatchID != "" && r.BatchIndex == 0 && r.BatchSize == end-i && r.BatchSize > 1
		for j := i; valid && j < end; j++ {
			member := records[j]
			valid = member.BatchSize == r.BatchSize && member.BatchIndex == j-i
			if j > i {
				valid = valid && member.ParentID == records[j-1].ID
			}
		}
		if valid {
			out = append(out, records[i:end]...)
		} else {
			dropped += end - i
		}
		i = end
	}
	if dropped > 0 && diags != nil {
		*diags = append(*diags, fmt.Sprintf("ignored %d record(s) from incomplete append batches; the previous committed history was retained", dropped))
	}
	return out, dropped
}

// recoverToolResults makes a legacy or damaged transcript resumable without
// guessing whether an interrupted tool ran. Only missing results are supplied;
// real results and their evidence are retained. The original file is untouched.
func recoverToolResults(messages []Timed) ([]Timed, int) {
	out := make([]Timed, 0, len(messages))
	var pending []llm.Block
	var pendingTime int64
	recovered := 0
	flush := func() {
		if len(pending) == 0 {
			return
		}
		blocks := make([]llm.Block, 0, len(pending))
		for _, call := range pending {
			blocks = append(blocks, llm.Block{Type: llm.BlockToolResult, ToolUseID: call.ID, IsError: true,
				Text: "[pogo] This tool call was interrupted before its result was saved. Its outcome is unknown. The tool has not been rerun; check the current state before retrying any action."})
		}
		out = append(out, Timed{Message: llm.Message{Role: llm.RoleUser, Content: blocks}, Time: pendingTime})
		recovered += len(pending)
		pending = nil
	}
	for _, m := range messages {
		isResults := m.Message.Role == llm.RoleUser && len(m.Message.Content) > 0
		for _, b := range m.Message.Content {
			if b.Type != llm.BlockToolResult {
				isResults = false
			}
		}
		if !isResults {
			flush()
		}
		out = append(out, m)
		if isResults {
			for _, b := range m.Message.Content {
				for i := 0; i < len(pending); i++ {
					if pending[i].ID == b.ToolUseID {
						pending = append(pending[:i], pending[i+1:]...)
						i--
					}
				}
			}
		}
		if calls := m.Message.ToolCalls(); len(calls) > 0 {
			pending, pendingTime = calls, m.Time
		}
	}
	flush()
	return out, recovered
}
