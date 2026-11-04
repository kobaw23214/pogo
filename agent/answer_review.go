package agent

import (
	"strings"
	"time"

	"github.com/yosukeno/pogo/llm"
)

const DefaultAnswerReviewTimeout = 5 * time.Minute

type AnswerReviewStatus = llm.AnswerReviewStatus

// Keep the actual reasoning received during an optional rewrite even when its
// proposed answer is rejected. The previous answer's reasoning already lives
// in the preceding message; replaying it would mislabel it as this attempt.
func preserveAnswerReviewThinking(message, attempted llm.Message, streamed string, fallback bool, duration time.Duration) llm.Message {
	var final strings.Builder
	var thinkingMS int64
	for _, block := range attempted.Content {
		if block.Type == llm.BlockThinking {
			final.WriteString(block.Text)
			thinkingMS += block.DurationMS
		}
	}
	thought := final.String()
	if streamed != "" && !strings.Contains(thought, streamed) {
		if thought == "" || strings.HasPrefix(streamed, thought) {
			thought = streamed
		} else {
			thought = streamed + "\n" + thought
		}
	}
	if !fallback && thought == "" {
		return message
	}
	blocks := make([]llm.Block, 0, len(message.Content)+1)
	if thought != "" {
		if thinkingMS == 0 {
			thinkingMS = duration.Milliseconds()
		}
		blocks = append(blocks, llm.Block{Type: llm.BlockThinking, Text: thought, DurationMS: thinkingMS})
	}
	for _, block := range message.Content {
		if block.Type != llm.BlockThinking {
			blocks = append(blocks, block)
		}
	}
	message.Content = blocks
	return message
}
