package llm

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestFailedModelStreamPreservesObservedProgressAndUsage(t *testing.T) {
	var attempts atomic.Int32
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"思考\"}}]}\n\n")
		w.(http.Flusher).Flush()
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"局部\"}}],\"usage\":{\"prompt_tokens\":17,\"completion_tokens\":3}}\n\n")
		w.(http.Flusher).Flush()
		<-release
	}))
	defer server.Close()
	defer close(release)
	client := New(Options{BaseURL: server.URL, Model: "glm-5.3", MaxRetries: 3, Timeout: 75 * time.Millisecond})
	out, err := client.Stream(WithRequestPurpose(context.Background(), PurposeCompaction), "same system", []Message{UserText("same history")}, nil, nil)
	if err == nil || attempts.Load() != 1 {
		t.Fatalf("a dirty failed call must not retry: err=%v attempts=%d", err, attempts.Load())
	}
	if len(out.Message.Content) != 0 {
		t.Fatal("partial thinking or text was committed as a message")
	}
	if out.Usage.Input != 17 || out.Usage.Output != 3 {
		t.Fatalf("already observed provider usage was lost: %+v", out.Usage)
	}
	if !out.Timing.HeadersReceived || out.Timing.TTFB <= 0 || out.Timing.TTFT <= 0 || out.Timing.FirstText <= 0 || out.Timing.LastContent < out.Timing.FirstText || out.Timing.Total <= 0 {
		t.Fatalf("failed-call timing lost: %+v", out.Timing)
	}
	if out.Timing.ThinkingBytes != int64(len("思考")) || out.Timing.TextBytes != int64(len("局部")) {
		t.Fatalf("received UTF-8 bytes were lost or treated as tokens: %+v", out.Timing)
	}
	if out.RequestProfile == nil || !out.RequestProfile.Applied || out.RequestProfile.ReasoningEffort != "low" {
		t.Fatalf("failed-call request profile was lost: %+v", out.RequestProfile)
	}
}

func TestHeaderTimeoutIsNotReportedAsReceivedHeaders(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-release }))
	defer server.Close()
	defer close(release)
	client := New(Options{BaseURL: server.URL, Model: "m", MaxRetries: -1, Timeout: 30 * time.Millisecond})
	out, err := client.Stream(context.Background(), "", []Message{UserText("hi")}, nil, nil)
	if err == nil || out.Timing.Total <= 0 || out.Timing.HeadersReceived || out.Timing.TTFB != 0 || out.Timing.TTFT != 0 || out.Timing.FirstText != 0 {
		t.Fatalf("no-response failure misreported: err=%v timing=%+v", err, out.Timing)
	}
	if out.Usage != (Usage{}) || len(out.Message.Content) != 0 {
		t.Fatal("no-response failure fabricated usage or output")
	}
}

func TestCancelledModelStreamRetainsObservedUsageWithoutPartialMessage(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	client := &OpenAIClient{}
	stream := "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"hidden\"}}],\"usage\":{\"prompt_tokens\":9,\"completion_tokens\":2}}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\"never parsed\"}}]}\n\n"
	out, err := client.readStream(ctx, strings.NewReader(stream), func(Delta) { cancel() }, time.Now())
	if err != nil || out.StopReason != StopAborted || len(out.Message.Content) != 0 || out.Usage.Input != 9 || out.Usage.Output != 2 {
		t.Fatalf("cancelled response lost observations: out=%+v err=%v", out, err)
	}
	if out.Timing.ThinkingBytes != int64(len("hidden")) || out.Timing.TextBytes != 0 || out.Timing.FirstText != 0 || out.Timing.TTFT <= 0 {
		t.Fatalf("cancellation observations include undelivered content: %+v", out.Timing)
	}
}
