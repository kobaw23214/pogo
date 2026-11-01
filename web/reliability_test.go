package web

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/session"
	"github.com/yosukeno/pogo/tools"
)

func obstructTranscript(t *testing.T, path string) func() {
	t.Helper()
	backup := path + ".saved"
	if err := os.Rename(path, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(backup, path); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func seedReliabilityConversation(t *testing.T, s *Session) []llm.Message {
	t.Helper()
	ms := []llm.Message{llm.UserText("keep the original constraints"), textTurn(strings.Repeat("original evidence ", 800)).Message}
	s.agent.SetMessages(ms)
	if err := s.store.AppendAll(ms); err != nil {
		t.Fatal(err)
	}
	s.persisted = len(ms)
	s.hub.Reset(s.store.TimedMessages(""), llm.Usage{})
	return ms
}

func TestPersistFailureKeepsMessageAndUsageCursorsRetryable(t *testing.T) {
	h := newHarness(t, scriptedTurns(textTurn("done")))
	s := h.session(h.createSession())
	ms := []llm.Message{llm.UserText("unsaved"), textTurn("answer").Message}
	s.agent.SetMessages(ms)
	s.agent.SetUsage(llm.Usage{Input: 120, Output: 8}, llm.Usage{})
	restore := obstructTranscript(t, s.Path())
	defer restore()
	s.finish()
	if s.persisted != 0 || s.recorded.Usage.Input != 0 {
		t.Fatalf("failed write advanced cursor: %d %+v", s.persisted, s.recorded)
	}
	if _, err := s.Start("must wait for persistence", nil, ""); err == nil {
		t.Fatal("new run bypassed unsaved prefix")
	}
	if h.calls() != 0 {
		t.Fatal("failed persistence still invoked model")
	}
	restore()
	s.finish()
	s.finish()
	reopened, err := session.Open(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reopened.Messages(""), ms) {
		t.Fatal("retry lost or duplicated messages")
	}
	u, _ := reopened.UsageTotals()
	if u.Input != 120 || u.Output != 8 {
		t.Fatalf("retry lost or duplicated usage: %+v", u)
	}
	// Isolate a later stats failure from message persistence.
	s.agent.SetUsage(llm.Usage{Input: 150, Output: 10}, llm.Usage{})
	restoreStats := obstructTranscript(t, s.Path())
	defer restoreStats()
	s.finish()
	if s.recorded.Usage.Input != 120 {
		t.Fatal("failed stats write advanced usage cursor")
	}
	restoreStats()
	s.finish()
	u, _ = s.store.UsageTotals()
	if u.Input != 150 || u.Output != 10 {
		t.Fatalf("stats retry mismatch: %+v", u)
	}
}

func TestCompactReservesSessionUntilAllViewsCommit(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	h := newHarness(t, func(int) (llm.Response, error) { close(entered); <-release; return textTurn("a complete summary"), nil })
	s := h.session(h.createSession())
	seedReliabilityConversation(t, s)
	result := make(chan error, 1)
	go func() { _, err := s.Compact(context.Background()); result <- err }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("compact did not start")
	}
	if !s.Active() {
		t.Fatal("compaction not reserved")
	}
	if _, err := s.Start("racing prompt", nil, ""); !errors.Is(err, ErrRunActive) {
		t.Fatalf("Start during compact: %v", err)
	}
	if _, err := s.SetModel("k3"); !errors.Is(err, ErrRunActive) {
		t.Fatalf("SetModel during compact: %v", err)
	}
	if _, err := s.Compact(context.Background()); !errors.Is(err, ErrRunActive) {
		t.Fatalf("second compact: %v", err)
	}
	if err := h.mgr.Delete(s.ID); !errors.Is(err, ErrRunActive) {
		t.Fatalf("Delete during compact: %v", err)
	}
	if len(s.hub.Snapshot().Messages) != 2 {
		t.Fatal("snapshot changed before commit")
	}
	releaseOnce.Do(func() { close(release) })
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if s.Active() || len(s.agent.Messages()) != 1 || len(s.store.Messages("")) != 1 || len(s.hub.Snapshot().Messages) != 1 {
		t.Fatal("compaction views did not settle together")
	}
}

func TestCompactWriteFailureKeepsOriginalConversationEverywhere(t *testing.T) {
	h := newHarness(t, scriptedTurns(textTurn("complete summary")))
	s := h.session(h.createSession())
	before := seedReliabilityConversation(t, s)
	head := s.store.Head()
	restore := obstructTranscript(t, s.Path())
	defer restore()
	if _, err := s.Compact(context.Background()); err == nil {
		t.Fatal("expected write failure")
	}
	if s.store.Head() != head || !reflect.DeepEqual(s.agent.Messages(), before) || !reflect.DeepEqual(s.store.Messages(""), before) || len(s.hub.Snapshot().Messages) != 2 {
		t.Fatal("failed summary replaced one of the views")
	}
	if s.Active() {
		t.Fatal("failed compact kept its reservation")
	}
	restore()
	reopened, err := session.Open(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reopened.Messages(""), before) {
		t.Fatal("failed compaction changed reopened history")
	}
}

func TestDeleteClosesBackgroundLifetimeAndRejectsLateWrites(t *testing.T) {
	h := newHarness(t, scriptedTurns(textTurn("must not run")))
	s := h.session(h.createSession())
	path := s.Path()
	s.queueBgNotification("queued completion")
	if err := h.mgr.Delete(s.ID); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(s.sessCtx.Err(), context.Canceled) || !s.bgOff || s.bgWake != nil || len(s.bgInbox) != 0 {
		t.Fatal("delete left background lifetime alive")
	}
	s.onBgComplete(tools.BgTask{ID: "late"}, tools.BgResult{Text: "late result", Details: tools.SubagentDetails{InputTok: 100, OutTok: 8}})
	s.queueBgNotification("another late result")
	s.wakeBg()
	if _, err := s.Start("late HTTP handler", nil, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("closed session accepted Start: %v", err)
	}
	name := "late rename"
	if err := s.SetInfo(&name, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("closed session accepted rename: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("late completion recreated transcript: %v", err)
	}
	if h.calls() != 0 {
		t.Fatal("late completion invoked model")
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); s.Close() }()
	}
	wg.Wait()
}

func TestEvictionKeepsAnUnsavedSessionLoaded(t *testing.T) {
	h := newHarness(t, scriptedTurns(textTurn("done")))
	s := h.session(h.createSession())
	s.agent.SetMessages([]llm.Message{llm.UserText("only in memory")})
	restore := obstructTranscript(t, s.Path())
	defer restore()
	h.mgr.evictAll()
	if h.mgr.sessions[s.ID] != s || s.closing {
		t.Fatal("eviction discarded unsaved conversation")
	}
	restore()
	h.mgr.evictAll()
	if _, ok := h.mgr.sessions[s.ID]; ok {
		t.Fatal("healthy idle session did not evict")
	}
	reopened, err := session.Open(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	if len(reopened.Messages("")) != 1 {
		t.Fatal("eviction lost recovered write")
	}
}
