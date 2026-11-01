package web

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yosukeno/pogo/config"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/skills"
	"github.com/yosukeno/pogo/tools"
)

func officeLifecycleHarness(t *testing.T) (*harness, *officeService, string) {
	t.Helper()
	sk := writeTestSkill(t, t.TempDir(), "docs", "Office", "Read before changing a document.")
	h := newHarness(t, scriptedTurns(textTurn("done")), func(c *Config) { c.Skills = []skills.Skill{sk} })
	fixture, _, _ := officeTestService(t)
	o := &officeService{cfg: fixture.cfg, sessions: map[string]*officeDocumentSession{}, active: map[string]string{}, manager: h.mgr}
	h.mgr.officeForSession = o.link
	h.mgr.officeBeforeDelete = o.beforeDeleteConversation
	h.mgr.officeRevokeSession = o.revokeConversation
	h.mgr.officeSessionActive = o.conversationActive
	t.Cleanup(o.close)
	file := filepath.Join(h.mgr.Cwd(), "lifecycle.docx")
	if err := os.WriteFile(file, officeTestPackage(t, "manual text preserved"), 0600); err != nil {
		t.Fatal(err)
	}
	return h, o, file
}

func officeLifecycleLink(t *testing.T, s *Session) *officeLink {
	t.Helper()
	for _, tool := range s.mcpReserved {
		if office, ok := tool.(*tools.Office); ok {
			return office.Link.(*officeLink)
		}
	}
	t.Fatal("session has no scoped Office tool")
	return nil
}

func officeLifecycleSignal(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("session lifecycle did not progress")
	}
}

func officeLifecycleRelease(release chan struct{}) {
	select {
	case <-release:
	default:
		close(release)
	}
}

func TestOfficeSessionColdLoadCoalesces32Requests(t *testing.T) {
	h, o, _ := officeLifecycleHarness(t)
	old, err := h.mgr.Create("", "")
	if err != nil {
		t.Fatal(err)
	}
	h.mgr.evictAll()
	oldLink := officeLifecycleLink(t, old)
	_, err = oldLink.Execute(context.Background(), json.RawMessage(`{"action":"list"}`))
	officeExpectError(t, err, "CONVERSATION_CLOSED")
	newClient := h.mgr.cfg.NewClient
	entered, release := make(chan struct{}), make(chan struct{})
	defer officeLifecycleRelease(release)
	var constructs atomic.Int32
	h.mgr.cfg.NewClient = func(c config.Resolved, notice func(llm.RetryInfo)) llm.Client {
		if constructs.Add(1) == 1 {
			close(entered)
		}
		<-release
		return newClient(c, notice)
	}
	var requests sync.WaitGroup
	results := make(chan *Session, 32)
	failures := make(chan error, 32)
	for index := 0; index < 32; index++ {
		requests.Add(1)
		go func() {
			defer requests.Done()
			s, err := h.mgr.Get(old.ID)
			if err != nil {
				failures <- err
				return
			}
			results <- s
		}()
	}
	officeLifecycleSignal(t, entered)
	close(release)
	requests.Wait()
	close(results)
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	if constructs.Load() != 1 {
		t.Fatalf("built %d agents for one cold conversation", constructs.Load())
	}
	var live *Session
	for s := range results {
		if live == nil {
			live = s
		}
		if s != live || s == old {
			t.Fatal("cold requests did not share one new instance")
		}
	}
	link := officeLifecycleLink(t, live)
	if _, err := link.Execute(context.Background(), json.RawMessage(`{"action":"list"}`)); err != nil {
		t.Fatal("cold loading revoked the surviving Office role", err)
	}
	if _, err := link.Execute(context.Background(), json.RawMessage(`{"action":"open","path":"lifecycle.docx"}`)); err != nil {
		t.Fatal("surviving role cannot open a document", err)
	}
	if !o.conversationActive(live.ID) {
		t.Fatal("opened document did not retain its conversation")
	}
}

func TestOfficeUnpublishedCleanupPreservesWinnerAndTranscript(t *testing.T) {
	h, o, _ := officeLifecycleHarness(t)
	winner, err := h.mgr.Create("", "")
	if err != nil {
		t.Fatal(err)
	}
	link := officeLifecycleLink(t, winner)
	if _, err := link.Execute(context.Background(), json.RawMessage(`{"action":"open","path":"lifecycle.docx"}`)); err != nil {
		t.Fatal(err)
	}
	private, err := h.mgr.loadSession(winner.ID)
	if err != nil {
		t.Fatal(err)
	}
	private.agent.SetMessages([]llm.Message{llm.UserText("stale private state must not replace the shared transcript")})
	before, err := os.ReadFile(winner.Path())
	if err != nil {
		t.Fatal(err)
	}
	private.closeUnpublished()
	private.closeUnpublished()
	after, err := os.ReadFile(winner.Path())
	if err != nil || string(before) != string(after) {
		t.Fatal("discarded constructor wrote the shared transcript", err)
	}
	if !private.bgOff || private.sessCtx.Err() == nil {
		t.Fatal("discarded constructor retained private background resources")
	}
	if _, err := link.Execute(context.Background(), json.RawMessage(`{"action":"list"}`)); err != nil || !o.conversationActive(winner.ID) {
		t.Fatal("discarded constructor revoked the registered instance", err)
	}
}

func TestOfficeEvictionWaitsForOldGenerationBeforeReload(t *testing.T) {
	h, o, _ := officeLifecycleHarness(t)
	old, err := h.mgr.Create("", "")
	if err != nil {
		t.Fatal(err)
	}
	other, err := h.mgr.Create("", "")
	if err != nil {
		t.Fatal(err)
	}
	h.mgr.mu.Lock()
	delete(h.mgr.sessions, other.ID)
	h.mgr.mu.Unlock()
	other.Close()
	entered, release := make(chan struct{}), make(chan struct{})
	defer officeLifecycleRelease(release)
	var blocked atomic.Bool
	h.mgr.officeRevokeSession = func(id string) {
		if id == old.ID && blocked.CompareAndSwap(false, true) {
			close(entered)
			<-release
		}
		o.revokeConversation(id)
	}
	evicted := make(chan struct{})
	go func() { h.mgr.evictAll(); close(evicted) }()
	officeLifecycleSignal(t, entered)
	loaded := make(chan struct{})
	var next *Session
	var loadErr error
	go func() { next, loadErr = h.mgr.Get(old.ID); close(loaded) }()
	unrelated := make(chan struct{})
	var unrelatedErr error
	go func() { _, unrelatedErr = h.mgr.Get(other.ID); close(unrelated) }()
	officeLifecycleSignal(t, unrelated)
	if unrelatedErr != nil {
		t.Fatal("an old generation's cleanup blocked a different conversation", unrelatedErr)
	}
	select {
	case <-loaded:
		t.Fatal("new generation published before old Office cleanup")
	default:
	}
	close(release)
	officeLifecycleSignal(t, evicted)
	officeLifecycleSignal(t, loaded)
	if loadErr != nil || next == old {
		t.Fatal("conversation did not reload after its old instance drained", loadErr)
	}
	if _, err := officeLifecycleLink(t, next).Execute(context.Background(), json.RawMessage(`{"action":"list"}`)); err != nil {
		t.Fatal("old cleanup revoked the reloaded Office generation", err)
	}
}

func TestOfficeDirtyDocumentPreventsEvictionAndDelete(t *testing.T) {
	h, o, _ := officeLifecycleHarness(t)
	s, err := h.mgr.Create("", "")
	if err != nil {
		t.Fatal(err)
	}
	link := officeLifecycleLink(t, s)
	if _, err := link.Execute(context.Background(), json.RawMessage(`{"action":"open","path":"lifecycle.docx"}`)); err != nil {
		t.Fatal(err)
	}
	o.mu.Lock()
	d := o.sessions[o.active[filepath.Join(s.cwd, "lifecycle.docx")]]
	o.mu.Unlock()
	d.mu.Lock()
	d.dirtyEpoch = 1
	d.mu.Unlock()
	h.mgr.evictAll()
	h.mgr.mu.Lock()
	live := h.mgr.sessions[s.ID]
	h.mgr.mu.Unlock()
	if live != s {
		t.Fatal("human dirty document's conversation was evicted")
	}
	officeExpectError(t, h.mgr.Delete(s.ID), "UNSAVED_DOCUMENT")
	if _, err := link.Execute(context.Background(), json.RawMessage(`{"action":"list"}`)); err != nil {
		t.Fatal("refused deletion revoked its live Office role", err)
	}
	d.mu.Lock()
	d.dirtyEpoch = 0
	d.mu.Unlock()
	if err := h.mgr.Delete(s.ID); err != nil {
		t.Fatal(err)
	}
	_, err = link.Execute(context.Background(), json.RawMessage(`{"action":"list"}`))
	officeExpectError(t, err, "CONVERSATION_CLOSED")
	if !d.view().Closed {
		t.Fatal("real deletion left a live document")
	}
	if _, err := h.mgr.Get(s.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("deleted conversation was resurrected", err)
	}
}

func TestOfficeDeleteAndShutdownDrainColdLoad(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		t.Run(map[bool]string{false: "delete", true: "shutdown"}[shutdown], func(t *testing.T) {
			h, o, _ := officeLifecycleHarness(t)
			s, err := h.mgr.Create("", "")
			if err != nil {
				t.Fatal(err)
			}
			h.mgr.evictAll()
			newClient := h.mgr.cfg.NewClient
			entered, release := make(chan struct{}), make(chan struct{})
			defer officeLifecycleRelease(release)
			h.mgr.cfg.NewClient = func(c config.Resolved, notice func(llm.RetryInfo)) llm.Client {
				close(entered)
				<-release
				return newClient(c, notice)
			}
			loaded := make(chan struct{})
			var loadErr error
			go func() { _, loadErr = h.mgr.Get(s.ID); close(loaded) }()
			officeLifecycleSignal(t, entered)
			ended := make(chan struct{})
			var endErr error
			go func() {
				if shutdown {
					h.mgr.Close()
				} else {
					endErr = h.mgr.Delete(s.ID)
				}
				close(ended)
			}()
			deadline := time.Now().Add(3 * time.Second)
			for {
				h.mgr.mu.Lock()
				reserved := h.mgr.closed || h.mgr.deleting[s.ID]
				h.mgr.mu.Unlock()
				if reserved {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("close/delete did not reserve the in-flight conversation")
				}
				time.Sleep(time.Millisecond)
			}
			close(release)
			officeLifecycleSignal(t, loaded)
			officeLifecycleSignal(t, ended)
			if loadErr == nil || endErr != nil {
				t.Fatalf("constructor escaped close/delete: load=%v end=%v", loadErr, endErr)
			}
			h.mgr.mu.Lock()
			live, pending := h.mgr.sessions[s.ID], h.mgr.sessionLoads[s.ID]
			h.mgr.mu.Unlock()
			o.mu.Lock()
			role := o.roles[s.ID]
			o.mu.Unlock()
			if live != nil || pending != nil || role != nil {
				t.Fatal("close/delete retained a pending constructor or active Office role")
			}
		})
	}
}
