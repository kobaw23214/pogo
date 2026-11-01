package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

func TestIncompleteStreamNeverExecutesTools(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c1\",\"function\":{\"name\":\"write\",\"arguments\":\"{}\"}}]}}]}\n\n")
	}))
	defer srv.Close()
	tool := &fakeTool{name: "write"}
	reg := tools.NewRegistry(tool)
	a := New(Config{Client: llm.New(llm.Options{BaseURL: srv.URL, Model: "fake", MaxRetries: -1}), Registry: reg})
	var failed bool
	for event := range a.Run(context.Background(), "write") {
		if errors.Is(event.Err, llm.ErrIncompleteStream) {
			failed = true
		}
	}
	if !failed || len(tool.callArgs()) != 0 {
		t.Fatalf("failed=%v calls=%v", failed, tool.callArgs())
	}
	for _, msg := range a.Messages() {
		if len(msg.ToolCalls()) > 0 {
			t.Fatal("incomplete tool call committed to history")
		}
	}
}

func TestCompactRequiresCompleteSummaryAndChargesRejectedUsage(t *testing.T) {
	for _, reason := range []llm.StopReason{llm.StopMaxTokens, llm.StopAborted, llm.StopToolUse, ""} {
		t.Run(string(reason), func(t *testing.T) {
			c := &fakeClient{responses: []llm.Response{{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockText, Text: "partial summary"}}}, StopReason: reason, Usage: llm.Usage{Input: 100, Output: 10}}}}
			a := New(Config{Client: c, Registry: emptyRegistry()})
			a.SetMessages([]llm.Message{llm.UserText("keep all constraints"), llm.UserText(strings.Repeat("history ", 1000))})
			before := a.Messages()
			committed := false
			res, err := a.CompactWithCommit(context.Background(), 0, func([]llm.Message) error { committed = true; return nil })
			if !errors.Is(err, ErrIncompleteSummary) || committed || !reflect.DeepEqual(before, a.Messages()) || a.Usage().Input != 100 || res.Usage.Input != 100 {
				t.Fatalf("err=%v committed=%v usage=%+v result=%+v", err, committed, a.Usage(), res)
			}
		})
	}
	// An empty normal response consumed its reported usage too.
	a := compacting(t, &summariser{usage: llm.Usage{Input: 17}})
	if _, err := a.Compact(context.Background()); !errors.Is(err, ErrEmptySummary) || a.Usage().Input != 17 {
		t.Fatalf("err=%v usage=%+v", err, a.Usage())
	}
}

func TestCompactCommitsStorageBeforeReplacingMemory(t *testing.T) {
	for _, fail := range []bool{true, false} {
		a := compacting(t, &summariser{reply: "complete summary"})
		before := a.Messages()
		storageErr := errors.New("storage failed")
		called := false
		_, err := a.CompactWithCommit(context.Background(), 0, func(candidate []llm.Message) error {
			called = true
			if !reflect.DeepEqual(before, a.Messages()) {
				t.Fatal("memory replaced before storage committed")
			}
			if len(candidate) != 1 {
				t.Fatalf("candidate=%+v", candidate)
			}
			if fail {
				return storageErr
			}
			return nil
		})
		if !called {
			t.Fatal("commit hook not called")
		}
		if fail {
			if !errors.Is(err, storageErr) || !reflect.DeepEqual(before, a.Messages()) {
				t.Fatalf("failed commit changed history: %v", err)
			}
		} else if err != nil || len(a.Messages()) != 1 {
			t.Fatalf("commit failed: %v", err)
		}
	}
}
