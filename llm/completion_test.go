package llm

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestStreamRequiresExplicitCompletion(t *testing.T) {
	text := "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n"
	tool := "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c1\",\"function\":{\"name\":\"write\",\"arguments\":\"{}\"}}]}}]}\n\n"
	finish := func(reason string) string {
		return fmt.Sprintf("data: {\"choices\":[{\"delta\":{},\"finish_reason\":%q}]}\n\n", reason)
	}
	for _, tc := range []struct {
		name, stream string
		want         StopReason
		bad          bool
	}{
		{"empty EOF", "", "", true},
		{"text EOF", text, "", true},
		{"tool EOF", tool, "", true},
		{"unknown reason", text + finish("unexpected") + "data: [DONE]\n\n", "", true},
		{"corrupt frame", text + "data: {broken}\n\n" + finish("stop"), "", true},
		{"filtered tool", tool + finish("content_filter"), "", true},
		{"finish without DONE", text + finish("stop"), StopEndTurn, false},
		{"DONE without finish", text + "data: [DONE]\n\n", StopEndTurn, false},
		{"tool DONE", tool + "data: [DONE]\n\n", StopToolUse, false},
		{"truncated", tool + finish("length"), StopMaxTokens, false},
		{"filtered text", text + finish("content_filter"), "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := (&OpenAIClient{}).readStream(context.Background(), strings.NewReader(tc.stream), nil, time.Now())
			if tc.bad {
				if err == nil || len(out.Message.Content) != 0 {
					t.Fatalf("unsafe completion: response=%+v err=%v", out, err)
				}
			} else if err != nil || out.StopReason != tc.want {
				t.Fatalf("stop=%s err=%v, want %s", out.StopReason, err, tc.want)
			}
		})
	}
}

func TestCleanEOFDoesNotReplayVisibleOutput(t *testing.T) {
	attempts := 0
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n")
	})
	var text strings.Builder
	out, err := c.Stream(context.Background(), "", nil, nil, func(d Delta) { text.WriteString(d.Text) })
	if !errors.Is(err, ErrIncompleteStream) || attempts != 1 || text.String() != "partial" || len(out.Message.Content) != 0 {
		t.Fatalf("attempts=%d text=%q response=%+v err=%v", attempts, text.String(), out, err)
	}
}
