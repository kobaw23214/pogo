package llm

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A provider's extra headers must ride on every request — a vendor gateway's
// SSE switch (X-DashScope-SSE being the case that created the field) is
// load-bearing — and a collision with one of the standard headers resolves in
// the provider's favour, because that override was configured on purpose.
func TestProviderHeadersRideEveryRequestAndWinCollisions(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		w.Header().Set("Content-Type", "text/event-stream")
		flushFrame(w, `data: {"choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":null}]}`+"\n\n")
		flushFrame(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		flushFrame(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)

	c := New(Options{
		BaseURL: srv.URL, APIKey: "test", Model: "m",
		Headers: map[string]string{"X-DashScope-SSE": "enable", "Accept": "application/json"},
	})
	if _, err := c.Stream(context.Background(), "", []Message{UserText("hi")}, nil, nil); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if got == nil {
		t.Fatal("no request reached the server")
	}
	if v := got.Get("X-DashScope-SSE"); v != "enable" {
		t.Errorf("X-DashScope-SSE = %q; the provider's header did not ride the request", v)
	}
	if v := got.Get("Accept"); v != "application/json" {
		t.Errorf("Accept = %q; a provider header lost a collision with the standard one", v)
	}
	if v := got.Get("Authorization"); v != "Bearer test" {
		t.Errorf("Authorization = %q; want the untouched standard credential header", v)
	}
}

// extra_body folds over the wire request: keys the gateway demands ride along
// (`parameters` being the case that created the field), the standard fields
// survive byte-for-byte, and a collision resolves in the custom key's favour —
// the same rule headers follow.
func TestExtraBodyMergesOverTheWireRequest(t *testing.T) {
	var body []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		flushFrame(w, `data: {"choices":[{"index":0,"delta":{"content":"ok"},"finish_reason":null}]}`+"\n\n")
		flushFrame(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
		flushFrame(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)

	c := New(Options{
		BaseURL: srv.URL, APIKey: "test", Model: "m", MaxTokens: 42,
		ExtraBody: map[string]any{
			"parameters": map[string]any{},
			"top_k":      5,
			"max_tokens": 99, // a deliberate override of a standard field
		},
	})
	if _, err := c.Stream(context.Background(), "", []Message{UserText("hi")}, nil, nil); err != nil {
		t.Fatalf("Stream: %v", err)
	}
	if body == nil {
		t.Fatal("no request reached the server")
	}
	var sent map[string]any
	if err := json.Unmarshal(body, &sent); err != nil {
		t.Fatalf("merged body is not JSON: %v (%s)", err, body)
	}
	if _, ok := sent["parameters"].(map[string]any); !ok {
		t.Errorf("parameters = %v; the gateway's required key did not ride the body", sent["parameters"])
	}
	if sent["top_k"] != float64(5) {
		t.Errorf("top_k = %v; want 5", sent["top_k"])
	}
	if sent["max_tokens"] != float64(99) {
		t.Errorf("max_tokens = %v; a custom key lost its collision with the standard one", sent["max_tokens"])
	}
	if sent["model"] != "m" || sent["stream"] != true {
		t.Errorf("standard fields damaged by the merge: model=%v stream=%v", sent["model"], sent["stream"])
	}
	so, ok := sent["stream_options"].(map[string]any)
	if !ok || so["include_usage"] != true {
		t.Errorf("stream_options = %v; nested standard values did not survive verbatim", sent["stream_options"])
	}
}

func TestDefaultRequestTimeoutIsThirtyMinutes(t *testing.T) {
	c := New(Options{})
	if c.http.Timeout != 30*time.Minute {
		t.Errorf("default timeout = %v, want 30m", c.http.Timeout)
	}
	custom := New(Options{Timeout: 45 * time.Second})
	if custom.http.Timeout != 45*time.Second {
		t.Errorf("custom timeout = %v, want 45s", custom.http.Timeout)
	}
}

// Argument fragments must stream out as they arrive, attributed to their call:
// that is what lets a UI preview a large write instead of sitting silent until
// the whole call has been generated.
func TestToolCallArgumentDeltasStreamWithTheirCallID(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flushFrame(w, `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"write","arguments":"{\"pa"}}]},"finish_reason":null}]}`+"\n\n")
		flushFrame(w, `data: {"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"th\":\"a.go\"}"}}]},"finish_reason":null}]}`+"\n\n")
		flushFrame(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n")
		flushFrame(w, "data: [DONE]\n\n")
	})

	var deltas []Delta
	resp, err := c.Stream(context.Background(), "sys", []Message{UserText("hi")}, nil, func(d Delta) {
		deltas = append(deltas, d)
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	var starts, fragments int
	for _, d := range deltas {
		switch d.Kind {
		case DeltaToolCallStart:
			starts++
			if d.ToolID != "c1" || d.ToolName != "write" {
				t.Errorf("start delta = %+v", d)
			}
		case DeltaToolCallArgs:
			fragments++
			if d.ToolID != "c1" {
				t.Errorf("fragment lost its call id: %+v", d)
			}
			if d.Text == "" {
				t.Error("an empty fragment was emitted")
			}
		}
	}
	if starts != 1 || fragments != 2 {
		t.Errorf("deltas = %d starts, %d fragments; want 1 and 2", starts, fragments)
	}

	// The settled message is unchanged: fragments are a preview, not a second
	// channel for the arguments.
	calls := resp.Message.ToolCalls()
	if len(calls) != 1 || string(calls[0].Input) != `{"path":"a.go"}` {
		t.Errorf("settled tool calls = %+v", calls)
	}
}

// The thinking block carries how long it streamed, wall-clock: the work log and
// the turn card both read DurationMS off it, and a field that silently stayed
// zero would cost them the "思考过程 · 2.3s" label with no error anywhere.
func TestThinkingDurationIsMeasuredAndSettled(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flushFrame(w, `data: {"choices":[{"index":0,"delta":{"reasoning_content":"先看"},"finish_reason":null}]}`+"\n\n")
		// A real gap, so the measured interval cannot round down to zero.
		time.Sleep(10 * time.Millisecond)
		flushFrame(w, `data: {"choices":[{"index":0,"delta":{"reasoning_content":"文件"},"finish_reason":null}]}`+"\n\n")
		flushFrame(w, `data: {"choices":[{"index":0,"delta":{"content":"答案"},"finish_reason":"stop"}]}`+"\n\n")
		flushFrame(w, "data: [DONE]\n\n")
	})

	resp, err := c.Stream(context.Background(), "sys", []Message{UserText("hi")}, nil, nil)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	var thinking *Block
	for i := range resp.Message.Content {
		if resp.Message.Content[i].Type == BlockThinking {
			thinking = &resp.Message.Content[i]
		}
	}
	if thinking == nil {
		t.Fatalf("no thinking block in %+v", resp.Message.Content)
	}
	if thinking.DurationMS < 5 {
		t.Errorf("DurationMS = %d, want at least the slept 10ms (minus slack)", thinking.DurationMS)
	}
	// The text block must not pick up the thinking interval.
	for _, b := range resp.Message.Content {
		if b.Type == BlockText && b.DurationMS != 0 {
			t.Errorf("text block carries DurationMS = %d", b.DurationMS)
		}
	}
}

// The seam between the HTTP layer and the classifier: a provider's 400 body has to
// arrive at the caller as an *APIError with the status, type and message intact, or
// the loop cannot tell an overflow from a malformed request and the recovery never
// runs. The body here is the shape Kimi actually returns, captured from a live
// probe against pogo's own endpoint.
func TestAnOverflowResponseArrivesAsAClassifiableError(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error",` +
			`"message":"Invalid request: Your request exceeded model token limit: 262144 (requested: 400011)"}}`))
	})
	_, err := c.Stream(context.Background(), "sys", []Message{UserText("hi")}, nil, nil)
	if err == nil {
		t.Fatal("a 400 returned no error")
	}

	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error is %T, not *APIError: %v", err, err)
	}
	if apiErr.Status != http.StatusBadRequest {
		t.Errorf("Status = %d, want 400; the classifier restricts itself to 4xx", apiErr.Status)
	}
	if apiErr.Type != "invalid_request_error" {
		t.Errorf("Type = %q, want the provider's own classification", apiErr.Type)
	}
	if !strings.Contains(apiErr.Message, "exceeded model token limit") {
		t.Errorf("Message lost the provider's explanation: %q", apiErr.Message)
	}
	if !IsContextOverflow(err) {
		t.Error("the overflow was not classifiable end to end, so the loop cannot recover from it")
	}
}

// And a 400 that is not an overflow must not become one, all the way through the
// same path: responding to a malformed request by discarding history would destroy
// context to work around a bug in the request.
func TestAMalformedRequestIsNotMistakenForAnOverflow(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"type":"invalid_request_error",` +
			`"message":"Request format error, missing required parameter"}}`))
	})
	_, err := c.Stream(context.Background(), "sys", []Message{UserText("hi")}, nil, nil)
	if err == nil {
		t.Fatal("a 400 returned no error")
	}
	if IsContextOverflow(err) {
		t.Errorf("a malformed request was classified as an overflow: %v", err)
	}
	// It still has to be readable: this is what a user sees.
	if !strings.Contains(err.Error(), "missing required parameter") {
		t.Errorf("the provider's explanation was lost: %v", err)
	}
}
