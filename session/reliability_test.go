package session

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/llm"
)

func reliabilityPair() []llm.Message {
	return []llm.Message{
		{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockToolUse, ID: "call", Name: "read"}}},
		{Role: llm.RoleUser, Content: []llm.Block{{Type: llm.BlockToolResult, ToolUseID: "call", Text: "saved evidence"}}},
	}
}

func TestAppendBatchRollsBackPartialAndAmbiguousWrites(t *testing.T) {
	t.Setenv("POGO_SESSION_INDEX", "off")
	for _, mode := range []string{"partial", "complete-then-error"} {
		t.Run(mode, func(t *testing.T) {
			s, err := Create(t.TempDir(), "offline", "offline")
			if err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(s.Path())
			head := s.Head()
			s.writeFile = func(f *os.File, p []byte) error {
				if mode == "partial" {
					p = p[:bytes.IndexByte(p, '\n')+1]
				}
				if _, err := f.Write(p); err != nil {
					t.Fatal(err)
				}
				return errors.New("injected disk failure")
			}
			if err := s.AppendAll(reliabilityPair()); err == nil {
				t.Fatal("expected injected failure")
			}
			after, _ := os.ReadFile(s.Path())
			if !bytes.Equal(before, after) || s.Head() != head || len(s.Messages("")) != 0 {
				t.Fatal("failed append changed committed state")
			}
			s.writeFile = nil
			if err := s.AppendAll(reliabilityPair()); err != nil {
				t.Fatal(err)
			}
			reopened, err := Open(s.Path())
			if err != nil {
				t.Fatal(err)
			}
			if got := reopened.Messages(""); !reflect.DeepEqual(got, reliabilityPair()) {
				t.Fatalf("retry did not commit exactly once: %#v", got)
			}
		})
	}
}

func TestCrashAtEveryBatchByteKeepsOnlyCompleteToolTurns(t *testing.T) {
	t.Setenv("POGO_SESSION_INDEX", "off")
	s, err := Create(t.TempDir(), "offline", "offline")
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(s.Path())
	if err := s.AppendAll(reliabilityPair()); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(s.Path())
	payload := after[len(before):]
	crashPath := filepath.Join(t.TempDir(), "crash.jsonl")
	for cut := 0; cut <= len(payload); cut++ {
		if err := os.WriteFile(crashPath, append(append([]byte{}, before...), payload[:cut]...), 0o600); err != nil {
			t.Fatal(err)
		}
		reopened, err := Open(crashPath)
		if err != nil {
			t.Fatalf("cut %d: %v", cut, err)
		}
		got := reopened.Messages("")
		if len(got) != 0 && !reflect.DeepEqual(got, reliabilityPair()) {
			t.Fatalf("cut %d exposed a partial batch: %#v", cut, got)
		}
	}
}

func TestFailedForkKeepsOldHeadAndSummaryBatchIsAtomic(t *testing.T) {
	t.Setenv("POGO_SESSION_INDEX", "off")
	s, err := Create(t.TempDir(), "offline", "offline")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Append(llm.UserText("original")); err != nil {
		t.Fatal(err)
	}
	head := s.Head()
	s.writeFile = func(f *os.File, p []byte) error {
		_, _ = f.Write(p[:bytes.IndexByte(p, '\n')+1])
		return errors.New("fail")
	}
	if err := s.Fork(""); err == nil {
		t.Fatal("expected failure")
	}
	if s.Head() != head {
		t.Fatal("failed fork moved the head")
	}
	if err := s.ForkWithMessages("", []llm.Message{llm.UserText("summary")}); err == nil {
		t.Fatal("expected failure")
	}
	reopened, err := Open(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.Messages(""); len(got) != 1 || got[0].Text() != "original" {
		t.Fatal("failed replacement abandoned the original")
	}
}

func TestLegacyUnpairedToolCallGetsExplicitUnknownOutcome(t *testing.T) {
	t.Setenv("POGO_SESSION_INDEX", "off")
	s, err := Create(t.TempDir(), "offline", "offline")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Append(reliabilityPair()[0]); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(s.Path())
	reopened, err := Open(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	got := reopened.Messages("")
	if len(got) != 2 || !got[1].Content[0].IsError || got[1].Content[0].ToolUseID != "call" || !strings.Contains(got[1].Content[0].Text, "outcome is unknown") {
		t.Fatalf("unsafe recovery: %#v", got)
	}
	if len(reopened.Diagnostics()) == 0 {
		t.Fatal("recovery was silent")
	}
	after, _ := os.ReadFile(s.Path())
	if !bytes.Equal(before, after) {
		t.Fatal("read-only recovery rewrote evidence")
	}
}

func TestMalformedBatchDoesNotSwallowFollowingLegacyRecords(t *testing.T) {
	for _, bad := range []Record{
		{ID: "bad", BatchSize: 2},
		{ID: "bad", BatchID: "b", BatchIndex: -1, BatchSize: 2},
		{ID: "bad", BatchID: "b", BatchIndex: 0, BatchSize: 99},
	} {
		records := []Record{{ID: "before"}, bad, {ID: "after"}}
		var diags []string
		got, dropped := committedRecords(records, &diags)
		if len(got) != 2 || got[0].ID != "before" || got[1].ID != "after" || dropped != 1 || len(diags) == 0 {
			t.Fatalf("malformed batch swallowed healthy records: %#v %d %v", got, dropped, diags)
		}
	}
}

func TestUncertainRollbackRequiresReopenBeforeAnyRetry(t *testing.T) {
	t.Setenv("POGO_SESSION_INDEX", "off")
	s, err := Create(t.TempDir(), "offline", "offline")
	if err != nil {
		t.Fatal(err)
	}
	path := s.Path()
	s.writeFile = func(f *os.File, p []byte) error {
		if _, err := f.Write(p); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(path, path+".held"); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		return errors.New("injected uncertain write")
	}
	if err := s.AppendAll(reliabilityPair()); err == nil || !strings.Contains(err.Error(), "reopen session") {
		t.Fatalf("rollback uncertainty not surfaced: %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".held", path); err != nil {
		t.Fatal(err)
	}
	s.writeFile = nil
	if err := s.AppendAll(reliabilityPair()); err == nil {
		t.Fatal("unsafe retry allowed after uncertain rollback")
	}
	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reopened.Messages(""), reliabilityPair()) {
		t.Fatal("reopen did not reconcile the complete write")
	}
}
