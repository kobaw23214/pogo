package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/session"
)

func waitInboxState(t *testing.T, s *Session, check func(Snapshot) bool) Snapshot {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		snap := s.Hub().Snapshot()
		if check(snap) {
			return snap
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("inbox did not settle: %+v", s.Hub().Snapshot())
	return Snapshot{}
}

func inboxRequest(t *testing.T, h *harness, sid string, args map[string]any, want int) InboxItem {
	t.Helper()
	body, _ := json.Marshal(args)
	resp := h.do(http.MethodPost, "/api/sessions/"+sid+"/control", string(body))
	defer resp.Body.Close()
	if resp.StatusCode != want {
		t.Fatalf("status=%d want=%d for %s", resp.StatusCode, want, body)
	}
	if want != http.StatusOK {
		return InboxItem{}
	}
	var out struct {
		Accepted bool      `json:"accepted"`
		Item     InboxItem `json:"item"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if !out.Accepted {
		t.Fatal("missing accepted receipt")
	}
	return out.Item
}

func TestInboxQueuesDurablyEditsAndWithdrawsWithoutEnteringModel(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	h := newHarness(t, func(n int) (llm.Response, error) {
		if n == 1 {
			<-release
		}
		return textTurn("done"), nil
	})
	sid := h.createSession()
	stream := h.start(sid, "first")
	defer stream.close()
	s := h.session(sid)
	item := inboxRequest(t, h, sid, map[string]any{"action": "inbox_send", "id": "q1", "prompt": "later", "mode": "queued"}, 200)
	if item.Status != "queued" || item.Revision != 1 {
		t.Fatalf("receipt=%+v", item)
	}
	var saved inboxFile
	data, err := os.ReadFile(inboxPath(s.Path()))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved.Items) != 1 || saved.Items[0].Text != "later" {
		t.Fatalf("not durable: %s", data)
	}
	if len(s.Hub().Snapshot().Inbox) != 1 {
		t.Fatal("snapshot omitted pending message")
	}
	replay, _, cancel := s.Hub().Subscribe(0)
	defer cancel()
	found := false
	for _, e := range replay {
		if e.Type == EvInboxUpdated && e.Item.ID == "q1" {
			found = true
		}
	}
	if !found {
		t.Fatal("missing receipt SSE")
	}
	dup := inboxRequest(t, h, sid, map[string]any{"action": "inbox_send", "id": "q1", "prompt": "later", "mode": "queued"}, 200)
	if dup.Revision != 1 {
		t.Fatal("duplicate mutated receipt")
	}
	inboxRequest(t, h, sid, map[string]any{"action": "inbox_send", "id": "q1", "prompt": "different", "mode": "queued"}, 409)
	item = inboxRequest(t, h, sid, map[string]any{"action": "inbox_edit", "id": "q1", "revision": 1, "prompt": "revised"}, 200)
	inboxRequest(t, h, sid, map[string]any{"action": "inbox_edit", "id": "q1", "revision": 1, "prompt": "stale"}, 409)
	dup = inboxRequest(t, h, sid, map[string]any{"action": "inbox_send", "id": "q1", "prompt": "later", "mode": "queued"}, 200)
	if dup.Text != "revised" {
		t.Fatal("retry overwrote edited text")
	}
	item = inboxRequest(t, h, sid, map[string]any{"action": "inbox_withdraw", "id": "q1", "revision": item.Revision}, 200)
	if item.Status != "withdrawn" {
		t.Fatalf("withdraw=%+v", item)
	}
	if item.Text != "" {
		t.Fatal("withdrawn receipt retained full message body")
	}
	dup = inboxRequest(t, h, sid, map[string]any{"action": "inbox_send", "id": "q1", "prompt": "later", "mode": "queued"}, 200)
	if dup.Status != "withdrawn" || dup.Text != "" {
		t.Fatal("retry revived withdrawn input")
	}
	once.Do(func() { close(release) })
	stream.wait(t, EvRunEnd)
	if h.calls() != 1 {
		t.Fatalf("withdrawn message executed: %d calls", h.calls())
	}
	for _, m := range s.Hub().Snapshot().Messages {
		if m.InboxID != "" {
			t.Fatal("queued text reached model transcript")
		}
	}
}

func TestInboxSteerDeliversInsideSameRunAndKeepsReceiptOnReplay(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	h := newHarness(t, func(n int) (llm.Response, error) {
		if n == 1 {
			<-release
		}
		return textTurn("answer"), nil
	})
	sid := h.createSession()
	stream := h.start(sid, "original work")
	defer stream.close()
	s := h.session(sid)
	run := s.Hub().Snapshot().Run.RunID
	item, err := s.ReceiveInput("steer-1", "补充问题，原任务继续", "queued", run)
	if err != nil {
		t.Fatal(err)
	}
	item = inboxRequest(t, h, sid, map[string]any{"action": "inbox_steer", "id": item.ID, "revision": item.Revision, "expected_run_id": run}, 200)
	if item.Status != "steering" || item.RunID != run {
		t.Fatalf("receipt=%+v", item)
	}
	inboxRequest(t, h, sid, map[string]any{"action": "inbox_withdraw", "id": item.ID, "revision": item.Revision}, 409)
	if h.calls() > 1 {
		t.Fatal("steering interrupted current generation")
	}
	once.Do(func() { close(release) })
	stream.wait(t, EvRunEnd)
	snap := waitInboxState(t, s, func(sn Snapshot) bool { return len(sn.Inbox) == 1 && sn.Inbox[0].Status == "delivered" })
	if h.calls() != 2 {
		t.Fatalf("calls=%d", h.calls())
	}
	var received Message
	for _, m := range snap.Messages {
		if m.InboxID == item.ID {
			received = m
		}
	}
	if !received.Steering || received.RunID != run || received.ID == "" || snap.Inbox[0].MessageID != received.ID {
		t.Fatalf("lost provenance: %+v %+v", received, snap.Inbox)
	}
	// Replaying the original accepted request after delivery does not start work.
	dup, err := s.ReceiveInput(item.ID, "补充问题，原任务继续", "queued", run)
	if err != nil || dup.Status != "delivered" || h.calls() != 2 {
		t.Fatalf("duplicate delivery: %+v %v", dup, err)
	}
	if dup.Text != "" {
		t.Fatal("delivered receipt duplicated transcript body")
	}
	store, err := session.Open(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, m := range store.Messages("") {
		if m.InboxID == item.ID {
			found = true
			if !m.Steering || m.RunID != run {
				t.Fatal("missing persisted provenance")
			}
		}
	}
	if !found {
		t.Fatal("delivery not persisted")
	}
}

func TestInboxSuccessfulCompletionStartsQueuedInputsOnceInOrder(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	h := newHarness(t, func(n int) (llm.Response, error) {
		if n == 1 {
			<-release
		}
		return textTurn(fmt.Sprint(n)), nil
	})
	sid := h.createSession()
	stream := h.start(sid, "first")
	defer stream.close()
	s := h.session(sid)
	for _, id := range []string{"second", "third"} {
		if _, err := s.ReceiveInput(id, id, "queued", ""); err != nil {
			t.Fatal(err)
		}
	}
	once.Do(func() { close(release) })
	snap := waitInboxState(t, s, func(sn Snapshot) bool {
		return len(sn.Inbox) == 2 && sn.Inbox[0].Status == "delivered" && sn.Inbox[1].Status == "delivered" && !sn.Run.Active
	})
	if h.calls() != 3 {
		t.Fatalf("calls=%d want3", h.calls())
	}
	var ids []string
	var runs []string
	for _, m := range snap.Messages {
		if m.InboxID != "" {
			ids = append(ids, m.InboxID)
			runs = append(runs, m.RunID)
			if m.Steering {
				t.Fatal("new queued run incorrectly marked steering")
			}
		}
	}
	if fmt.Sprint(ids) != "[second third]" || runs[0] == runs[1] {
		t.Fatalf("order/runs: %v %v", ids, runs)
	}
}

func TestInboxConcurrentIdleSendsPreserveReceiptOrder(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	h := newHarness(t, func(n int) (llm.Response, error) {
		if n == 1 {
			<-release
		}
		return textTurn("done"), nil
	})
	s := h.session(h.createSession())
	const total = 12
	var wg sync.WaitGroup
	errors := make(chan error, total)
	for i := 0; i < total; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := fmt.Sprintf("message-%d", i)
			_, err := s.ReceiveInput(id, id, "queued", "")
			errors <- err
		}(i)
	}
	wg.Wait()
	for i := 0; i < total; i++ {
		if err := <-errors; err != nil {
			t.Fatal(err)
		}
	}
	var accepted []string
	for _, item := range s.Hub().Snapshot().Inbox {
		accepted = append(accepted, item.ID)
	}
	once.Do(func() { close(release) })
	snap := waitInboxState(t, s, func(sn Snapshot) bool {
		if sn.Run.Active || len(sn.Inbox) != total {
			return false
		}
		for _, item := range sn.Inbox {
			if item.Status != "delivered" {
				return false
			}
		}
		return true
	})
	var delivered []string
	for _, m := range snap.Messages {
		if m.InboxID != "" {
			delivered = append(delivered, m.InboxID)
		}
	}
	if h.calls() != total || fmt.Sprint(accepted) != fmt.Sprint(delivered) {
		t.Fatalf("receipt order %v, delivery order %v, calls %d", accepted, delivered, h.calls())
	}
}

func TestInboxCancelDuringIdleStartReservationKeepsMessages(t *testing.T) {
	s := bareInboxSession(t)
	if err := s.loadInboxLocked(); err != nil {
		t.Fatal(err)
	}
	s.inbox = []InboxItem{{ID: "first", Text: "first", Mode: "queued", Status: "steering", Revision: 2}, {ID: "next", Text: "next", Mode: "queued", Status: "queued", Revision: 1}}
	s.inboxStarting = "first"
	if !s.Cancel() {
		t.Fatal("reserved start could not be stopped")
	}
	if s.Active() {
		t.Fatal("cancel left a reserved start")
	}
	for _, item := range s.Hub().Snapshot().Inbox {
		if item.Status != "queued" || item.Mode != "steer" {
			t.Fatalf("cancel lost receipt: %+v", item)
		}
	}
}

func TestInboxCancelledOrFailedRunDoesNotDrainQueue(t *testing.T) {
	for _, kind := range []string{"cancel", "failure"} {
		t.Run(kind, func(t *testing.T) {
			release := make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(release) })
			h := newHarness(t, func(n int) (llm.Response, error) {
				if n == 1 {
					<-release
				}
				if kind == "failure" {
					return llm.Response{}, errors.New("fixture failure")
				}
				return textTurn("done"), nil
			})
			sid := h.createSession()
			stream := h.start(sid, "first")
			defer stream.close()
			s := h.session(sid)
			if _, err := s.ReceiveInput("later", "later", "queued", ""); err != nil {
				t.Fatal(err)
			}
			if kind == "cancel" {
				s.Cancel()
			}
			once.Do(func() { close(release) })
			stream.wait(t, EvRunEnd)
			snap := waitInboxState(t, s, func(sn Snapshot) bool { return len(sn.Inbox) == 1 && sn.Inbox[0].Mode == "steer" })
			if snap.Inbox[0].Status != "queued" || snap.Inbox[0].Note == "" || h.calls() != 1 {
				t.Fatalf("queue executed after %s: %+v calls=%d", kind, snap.Inbox, h.calls())
			}
		})
	}
}

func TestInboxCancellationBeforeBoundaryKeepsSteeringPending(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	h := newHarness(t, func(n int) (llm.Response, error) {
		if n == 1 {
			close(entered)
			<-release
		}
		return textTurn("done"), nil
	})
	sid := h.createSession()
	stream := h.start(sid, "original")
	defer stream.close()
	s := h.session(sid)
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("provider did not start")
	}
	if _, err := s.ReceiveInput("cancelled-steer", "not read yet", "steer", ""); err != nil {
		t.Fatal(err)
	}
	s.Cancel()
	once.Do(func() { close(release) })
	stream.wait(t, EvRunEnd)
	snap := waitInboxState(t, s, func(sn Snapshot) bool { return len(sn.Inbox) == 1 && sn.Inbox[0].Status == "queued" })
	if snap.Inbox[0].Mode != "steer" || h.calls() > 1 {
		t.Fatalf("cancelled input consumed: %+v calls=%d", snap.Inbox, h.calls())
	}
	for _, m := range snap.Messages {
		if m.InboxID == "cancelled-steer" {
			t.Fatal("unread input entered cancelled transcript")
		}
	}
}

func bareInboxSession(t *testing.T) *Session {
	t.Helper()
	store, err := session.Create(t.TempDir(), t.TempDir(), "test")
	if err != nil {
		t.Fatal(err)
	}
	// ReceiveInput also checks the session's live Skill catalog. Keep this
	// persistence-only fixture attached to an empty manager, as real sessions are.
	return &Session{store: store, hub: NewHub(), mgr: &Manager{}}
}

func TestInboxRecoveryUsesTranscriptIdentityAndNeverAutoplays(t *testing.T) {
	s := bareInboxSession(t)
	committed := llm.UserText("committed")
	committed.InboxID = "done"
	committed.RunID = "r1"
	committed.Steering = true
	if err := s.store.Append(committed); err != nil {
		t.Fatal(err)
	}
	s.inboxLoaded = true
	s.inboxHashes = map[string]string{"done": inboxDigest("committed", "steer"), "pending": inboxDigest("later", "queued")}
	s.inbox = []InboxItem{{ID: "done", Text: "committed", Mode: "steer", Status: "steering", Revision: 2}, {ID: "pending", Text: "later", Mode: "queued", Status: "queued", Revision: 1}}
	if err := s.saveInboxLocked(); err != nil {
		t.Fatal(err)
	}
	s.inboxLoaded = false
	s.inbox = nil
	s.hub.Seed(s.store.TimedMessages(""))
	if err := s.loadInboxLocked(); err != nil {
		t.Fatal(err)
	}
	if s.inbox[0].Status != "delivered" || s.inbox[0].MessageID == "" || s.inbox[1].Status != "queued" || s.inbox[1].Mode != "steer" {
		t.Fatalf("recovery=%+v", s.inbox)
	}
	if s.active != nil {
		t.Fatal("recovery started a run")
	}
	if len(s.hub.Snapshot().Inbox) != 2 {
		t.Fatal("recovered receipt missing from snapshot")
	}
}

func TestInboxTerminalBodiesCompactWithoutLosingIdempotency(t *testing.T) {
	s := bareInboxSession(t)
	s.inboxLoaded = true
	s.inboxHashes = map[string]string{}
	text := strings.Repeat("x", maxInboxText)
	// Older versions retained bodies on terminal receipts. Reading them must
	// compact those bodies while preserving the idempotency key and status.
	for i := 0; i < 100; i++ {
		id := fmt.Sprintf("completed-%d", i)
		s.inbox = append(s.inbox, InboxItem{ID: id, Text: text, Mode: "queued", Status: "delivered", Revision: 4})
		s.inboxHashes[id] = inboxDigest(text, "queued")
	}
	if err := s.saveInboxLocked(); err != nil {
		t.Fatal(err)
	}
	s.inboxLoaded = false
	if err := s.loadInboxLocked(); err != nil {
		t.Fatal(err)
	}
	for _, item := range s.inbox {
		if item.Text != "" {
			t.Fatal("terminal body retained after reload")
		}
	}
	if err := s.saveInboxLocked(); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(inboxPath(s.store.Path()))
	if err != nil {
		t.Fatal(err)
	}
	if st.Size() > 100<<10 {
		t.Fatalf("terminal receipts remained bloated: %d", st.Size())
	}
	item, err := s.ReceiveInput("completed-42", text, "queued", "")
	if err != nil || item.Status != "delivered" || item.Text != "" {
		t.Fatalf("retry identity lost: %+v %v", item, err)
	}
}

func TestInboxEndRaceReturnsUndeliveredInputToVisibleManualQueue(t *testing.T) {
	h := newHarness(t, scriptedTurns(textTurn("done")))
	s := h.session(h.createSession())
	// Represent the final boundary after the loop atomically closed steering but
	// before Session.finish releases the run slot.
	s.mu.Lock()
	s.active = &activeRun{id: "closing"}
	s.mu.Unlock()
	item, err := s.ReceiveInput("late", "late", "steer", "closing")
	if err != nil || item.Status != "queued" || item.Note == "" {
		t.Fatalf("late message disappeared: %+v %v", item, err)
	}
	s.mu.Lock()
	s.active = nil
	s.mu.Unlock()
	if h.calls() != 0 {
		t.Fatal("end-race receipt was executed")
	}
}

func TestInboxPersistenceFailureDoesNotAcknowledgeOrPublish(t *testing.T) {
	s := bareInboxSession(t)
	if err := s.loadInboxLocked(); err != nil {
		t.Fatal(err)
	}
	// A directory at the sidecar destination makes the atomic rename fail, without
	// relying on permission behavior that changes under root.
	if err := os.Mkdir(inboxPath(s.store.Path()), 0700); err != nil {
		t.Fatal(err)
	}
	_, err := s.ReceiveInput("not-saved", "hello", "queued", "")
	if err == nil || len(s.inbox) != 0 || len(s.hub.Snapshot().Inbox) != 0 {
		t.Fatalf("failed durable write was acknowledged: %v %+v", err, s.inbox)
	}
}

func TestInboxReceiptFailureStopsBeforeSteeredModelCallAndRecoversWithoutReplay(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	h := newHarness(t, func(n int) (llm.Response, error) {
		if n == 1 {
			<-release
		}
		return textTurn("done"), nil
	})
	sid := h.createSession()
	stream := h.start(sid, "first")
	defer stream.close()
	s := h.session(sid)
	if _, err := s.ReceiveInput("persist-boundary", "new instruction", "steer", ""); err != nil {
		t.Fatal(err)
	}
	path := inboxPath(s.Path())
	backup := path + ".fixture-backup"
	if err := os.Rename(path, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	needsRestore := true
	defer func() {
		if needsRestore {
			_ = os.Remove(path)
			_ = os.Rename(backup, path)
		}
	}()
	once.Do(func() { close(release) })
	end := stream.wait(t, EvRunEnd)
	if end.EndReason != "aborted" || h.calls() != 1 {
		t.Fatalf("executed without receipt commit: %+v calls=%d", end, h.calls())
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(backup, path); err != nil {
		t.Fatal(err)
	}
	needsRestore = false
	// The durable transcript marker wins over the stale "steering" receipt.
	store, err := session.Open(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	reloaded := &Session{store: store, hub: NewHub()}
	reloaded.hub.Seed(store.TimedMessages(""))
	if err := reloaded.loadInboxLocked(); err != nil {
		t.Fatal(err)
	}
	if len(reloaded.inbox) != 1 || reloaded.inbox[0].Status != "delivered" || reloaded.active != nil {
		t.Fatalf("receipt replayed or lost on restore: %+v", reloaded.inbox)
	}
}

func TestInboxIsolationAndInvalidInput(t *testing.T) {
	a, b := bareInboxSession(t), bareInboxSession(t)
	// Simulate a preparing run; no model or worker process is needed for receipts.
	a.active = &activeRun{id: "a", preparing: true}
	b.active = &activeRun{id: "b", preparing: true}
	if _, err := a.ReceiveInput("same", "private A", "queued", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := b.ReceiveInput("same", "private B", "queued", ""); err != nil {
		t.Fatal(err)
	}
	if a.inbox[0].Text == b.inbox[0].Text || filepath.Dir(a.store.Path()) == filepath.Dir(b.store.Path()) {
		t.Fatal("session receipts not isolated")
	}
	for _, in := range [][2]string{{"../foreign", "hello"}, {"valid", "/auto"}, {"valid", "/skill:browser-use"}, {"valid", "  "}} {
		if _, err := a.ReceiveInput(in[0], in[1], "queued", ""); !errors.Is(err, errInboxInvalid) {
			t.Fatalf("invalid accepted: %v %v", in, err)
		}
	}
	if _, err := a.ReceiveInput("path", "/Users/me/project needs a review", "queued", ""); err != nil {
		t.Fatal("ordinary paths must remain text", err)
	}
}
