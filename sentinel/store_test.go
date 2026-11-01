package sentinel

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func testStore(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}
func testSpec(t *testing.T) Spec {
	t.Helper()
	root := t.TempDir()
	return Spec{OwnerID: "tenant-a", AgentID: "agent-a", Workspace: root, ScriptDir: root, Command: []string{"python3", "watch.py"}, Title: "Generic observer", Task: "Investigate relevant changes", Mode: "poll", IntervalSeconds: 60, TimeoutSeconds: 30, MaxTurns: 20, Enabled: true}
}
func createWatch(t *testing.T, s *Store) Watch {
	t.Helper()
	w, err := s.Create(testSpec(t))
	if err != nil {
		t.Fatal(err)
	}
	return w
}
func oneFrame(id, key string, n int) Frame {
	return Frame{V: 1, ID: id, Events: []InputEvent{{Key: key, Summary: "changed", Data: json.RawMessage(fmt.Sprintf(`{"n":%d}`, n))}}, Cursor: json.RawMessage(fmt.Sprintf(`{"n":%d}`, n)), State: json.RawMessage(fmt.Sprintf(`{"n":%d}`, n))}
}
func acceptFrame(t *testing.T, s *Store, w Watch, f Frame) {
	t.Helper()
	if err := s.Accept(w.Spec.ID, w.Spec.Generation, f); err != nil {
		t.Fatal(err)
	}
}
func claim(t *testing.T, s *Store, id string, n int) *Batch {
	t.Helper()
	b, err := s.ClaimBatch(id, n)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestPersistentDatabaseRequiredAndVerified(t *testing.T) {
	for _, path := range []string{"", ":memory:"} {
		if s, err := Open(path); err == nil {
			s.Close()
			t.Fatalf("accepted volatile path %q", path)
		}
	}
	s, _ := testStore(t)
	var mode string
	var syncMode int
	if err := s.db.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow("PRAGMA synchronous").Scan(&syncMode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" || syncMode != 2 {
		t.Fatalf("durability mode=%s sync=%d", mode, syncMode)
	}
}
func TestReceiptReplayConflictAndAtomicState(t *testing.T) {
	s, _ := testStore(t)
	w := createWatch(t, s)
	f := oneFrame("f1", "event-1", 1)
	acceptFrame(t, s, w, f)
	acceptFrame(t, s, w, f)
	got, err := s.Get(w.Spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Pending != 1 || string(got.Cursor) != `{"n":1}` {
		t.Fatalf("duplicate changed state %+v", got)
	}
	f.State = json.RawMessage(`{"n":99}`)
	if err := s.Accept(w.Spec.ID, w.Spec.Generation, f); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflict=%v", err)
	}
	got, _ = s.Get(w.Spec.ID)
	if string(got.State) != `{"n":1}` {
		t.Fatal("conflicting receipt changed committed state")
	}
	f.ID = "f2"
	f.Events = nil
	acceptFrame(t, s, w, f)
	got, _ = s.Get(w.Spec.ID)
	if got.Pending != 1 || string(got.State) != `{"n":99}` {
		t.Fatal("quiet state frame incorrectly emitted event or lost state")
	}
	f.ID = "f3"
	f.Events = []InputEvent{{Key: "event-1", Summary: "changed", Data: json.RawMessage(`{"n":1}`)}}
	acceptFrame(t, s, w, f)
	got, _ = s.Get(w.Spec.ID)
	if got.Pending != 1 {
		t.Fatal("event dedup is not independent of frame identity")
	}
}
func TestEventKeyConflictDoesNotAdvanceCursor(t *testing.T) {
	s, _ := testStore(t)
	w := createWatch(t, s)
	acceptFrame(t, s, w, oneFrame("f1", "same", 1))
	if err := s.Accept(w.Spec.ID, w.Spec.Generation, oneFrame("f2", "same", 2)); !errors.Is(err, ErrConflict) {
		t.Fatalf("conflict=%v", err)
	}
	got, _ := s.Get(w.Spec.ID)
	if string(got.Cursor) != `{"n":1}` {
		t.Fatal("conflicting event advanced cursor")
	}
}

func TestFrameConflictDiagnosticsKeepReceiptImmutable(t *testing.T) {
	for _, changed := range []string{"events", "state", "cursor"} {
		t.Run(changed, func(t *testing.T) {
			s, _ := testStore(t)
			w := createWatch(t, s)
			f := oneFrame("source-id", "event-1", 1)
			acceptFrame(t, s, w, f)
			switch changed {
			case "events":
				// A later quiet poll must not reuse the eventful poll's frame ID.
				f.Events = nil
			case "state":
				f.State = json.RawMessage(`{"private_state":"do-not-print"}`)
			case "cursor":
				f.Cursor = json.RawMessage(`{"private_cursor":"do-not-print"}`)
			}
			err := s.Accept(w.Spec.ID, w.Spec.Generation, f)
			if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), `frame ID sha256:bfd4cfebf820612b was reused with different events, state or cursor`) || !strings.Contains(err.Error(), "use a new frame ID") {
				t.Fatalf("frame conflict diagnostic=%v", err)
			}
			if strings.Contains(err.Error(), "do-not-print") || strings.Contains(err.Error(), "event-1") || strings.Contains(err.Error(), "source-id") {
				t.Fatalf("frame conflict diagnostic exposed payload: %v", err)
			}
			got, err := s.Get(w.Spec.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Pending != 1 || string(got.State) != `{"n":1}` || string(got.Cursor) != `{"n":1}` {
				t.Fatalf("conflicting frame changed committed data: %+v", got)
			}
		})
	}
}

func TestEventConflictDiagnosticsKeepEventAndCursorImmutable(t *testing.T) {
	for _, changed := range []string{"summary", "data"} {
		t.Run(changed, func(t *testing.T) {
			s, _ := testStore(t)
			w := createWatch(t, s)
			acceptFrame(t, s, w, oneFrame("first-frame", "source-version-1", 1))
			f := oneFrame("second-frame", "source-version-1", 1)
			f.State, f.Cursor = json.RawMessage(`{"n":99}`), json.RawMessage(`{"n":99}`)
			switch changed {
			case "summary":
				f.Events[0].Summary = "private-summary-do-not-print"
			case "data":
				f.Events[0].Data = json.RawMessage(`{"private_data":"do-not-print"}`)
			}
			err := s.Accept(w.Spec.ID, w.Spec.Generation, f)
			if !errors.Is(err, ErrConflict) || !strings.Contains(err.Error(), `event key sha256:174f486152d81b29 was reused with different summary or data`) {
				t.Fatalf("event conflict diagnostic=%v", err)
			}
			if strings.Contains(err.Error(), "do-not-print") || strings.Contains(err.Error(), "second-frame") || strings.Contains(err.Error(), "source-version-1") {
				t.Fatalf("event conflict diagnostic exposed payload: %v", err)
			}
			got, err := s.Get(w.Spec.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.Pending != 1 || string(got.State) != `{"n":1}` || string(got.Cursor) != `{"n":1}` {
				t.Fatalf("conflicting event changed committed data: %+v", got)
			}
			b := claim(t, s, w.Spec.ID, 1)
			if b == nil || len(b.Events) != 1 || b.Events[0].Summary != "changed" || string(b.Events[0].Data) != `{"n":1}` {
				t.Fatalf("conflicting event changed the stored event: %+v", b)
			}
		})
	}
}

func TestConflictDiagnosticsFingerprintCollectorIdentities(t *testing.T) {
	for _, kind := range []string{"frame", "event"} {
		t.Run(kind, func(t *testing.T) {
			s, _ := testStore(t)
			w := createWatch(t, s)
			identity := "SYNTHETIC_CANARY_SECRET\n\t" + strings.Repeat("x", 180) + "hidden-suffix"
			f := oneFrame("first-frame", "event-1", 1)
			if kind == "frame" {
				f.ID = identity
			} else {
				f.Events[0].Key = identity
			}
			acceptFrame(t, s, w, f)
			if kind == "frame" {
				f.Events = nil
			} else {
				f.ID = "second-frame"
				f.Events[0].Summary = "different"
			}
			err := s.Accept(w.Spec.ID, w.Spec.Generation, f)
			if !errors.Is(err, ErrConflict) {
				t.Fatalf("conflict=%v", err)
			}
			message := err.Error()
			if strings.ContainsAny(message, "\n\t") || strings.Contains(message, "SYNTHETIC_CANARY_SECRET") || strings.Contains(message, "hidden-suffix") || strings.Contains(message, strings.Repeat("x", 16)) || !strings.Contains(message, "sha256:f2fd9d964b2df1a3") || len(message) > 512 {
				t.Fatalf("collector identity was not replaced by a safe fingerprint: %q", message)
			}
			if again := s.Accept(w.Spec.ID, w.Spec.Generation, f); !errors.Is(again, ErrConflict) || again.Error() != message {
				t.Fatalf("collector identity fingerprint was unstable: %v", again)
			}
			var stored string
			query := "SELECT frame_id FROM sentinel_frames WHERE watch_id=?"
			if kind == "event" {
				query = "SELECT event_key FROM sentinel_events WHERE watch_id=?"
			}
			if err := s.db.QueryRow(query, w.Spec.ID).Scan(&stored); err != nil {
				t.Fatal(err)
			}
			if stored != identity {
				t.Fatal("diagnostic fingerprint changed the persisted collector identity")
			}
		})
	}
}

func TestPauseAndResumeFenceRetiredCollectorsKeepEvents(t *testing.T) {
	s, _ := testStore(t)
	w := createWatch(t, s)
	acceptFrame(t, s, w, oneFrame("f1", "e1", 1))
	paused, err := s.SetEnabled(w.Spec.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Accept(w.Spec.ID, w.Spec.Generation, oneFrame("f2", "e2", 2)); !errors.Is(err, ErrStaleGeneration) {
		t.Fatalf("old generation=%v", err)
	}
	if err = s.Accept(w.Spec.ID, paused.Spec.Generation, oneFrame("f2", "e2", 2)); !errors.Is(err, ErrDisabled) {
		t.Fatalf("paused collector=%v", err)
	}
	if claim(t, s, w.Spec.ID, 32) != nil {
		t.Fatal("paused watch dispatched")
	}
	resumed, err := s.SetEnabled(w.Spec.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.Pending != 1 || resumed.Spec.Generation <= paused.Spec.Generation {
		t.Fatal("resume lost events or did not fence")
	}
	b := claim(t, s, w.Spec.ID, 32)
	if b == nil || len(b.Events) != 1 {
		t.Fatal("resume did not deliver retained event")
	}
}
func TestConcurrentReceiptIsOneLogicalObservation(t *testing.T) {
	s, _ := testStore(t)
	w := createWatch(t, s)
	f := oneFrame("f1", "e1", 1)
	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); errs <- s.Accept(w.Spec.ID, w.Spec.Generation, f) }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	got, _ := s.Get(w.Spec.ID)
	if got.Pending != 1 {
		t.Fatalf("pending=%d", got.Pending)
	}
	var count int
	s.db.QueryRow("SELECT count(*) FROM sentinel_frames").Scan(&count)
	if count != 1 {
		t.Fatalf("receipts=%d", count)
	}
}
func TestQueueFullRollsBackAllEventsAndCursor(t *testing.T) {
	s, _ := testStore(t)
	w := createWatch(t, s)
	for n := 0; n < MaxPendingEvents-1; {
		f := Frame{V: 1, ID: fmt.Sprint(n), Cursor: json.RawMessage(`{"old":true}`)}
		for len(f.Events) < 64 && n < MaxPendingEvents-1 {
			f.Events = append(f.Events, InputEvent{Key: fmt.Sprint(n), Summary: "changed"})
			n++
		}
		acceptFrame(t, s, w, f)
	}
	f := Frame{V: 1, ID: "overflow", Cursor: json.RawMessage(`{"wrong":true}`), Events: []InputEvent{{Key: "would-fit", Summary: "first"}, {Key: "over-limit", Summary: "second"}}}
	if err := s.Accept(w.Spec.ID, w.Spec.Generation, f); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("overflow=%v", err)
	}
	got, _ := s.Get(w.Spec.ID)
	if got.Pending != MaxPendingEvents-1 || string(got.Cursor) != `{"old":true}` {
		t.Fatal("queue rejection partially committed")
	}
	var n int
	s.db.QueryRow("SELECT count(*) FROM sentinel_frames WHERE frame_id='overflow'").Scan(&n)
	if n != 0 {
		t.Fatal("rejected frame was acknowledged")
	}
}
func TestBatchMembershipFrozenWhileAgentBusy(t *testing.T) {
	s, _ := testStore(t)
	w := createWatch(t, s)
	acceptFrame(t, s, w, oneFrame("f1", "e1", 1))
	b := claim(t, s, w.Spec.ID, 32)
	if b == nil || b.Attempt != 1 {
		t.Fatal("missing first attempt")
	}
	if err := s.SetBatchRun(b.ID, "session-1", "run-1", "running"); err != nil {
		t.Fatal(err)
	}
	acceptFrame(t, s, w, oneFrame("f2", "e2", 2))
	if claim(t, s, w.Spec.ID, 32) != nil {
		t.Fatal("overlapping Agent execution admitted")
	}
	got, _ := s.GetBatch(b.ID)
	if len(got.Events) != 1 || got.Events[0].Key != "e1" {
		t.Fatal("in-flight batch changed")
	}
	if err := s.SettleBatch(b.ID, "report", "done", 0); err != nil {
		t.Fatal(err)
	}
	next := claim(t, s, w.Spec.ID, 32)
	if next == nil || next.ID == b.ID || len(next.Events) != 1 || next.Events[0].Key != "e2" {
		t.Fatal("events during busy execution lost")
	}
}
func TestDeferredBatchKeepsIdentityButStartsNewAttempt(t *testing.T) {
	s, _ := testStore(t)
	w := createWatch(t, s)
	acceptFrame(t, s, w, oneFrame("f1", "e1", 1))
	b := claim(t, s, w.Spec.ID, 32)
	if err := s.SetBatchRun(b.ID, "session-1", "run-1", "running"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Hour).UnixMilli()
	if err := s.SettleBatch(b.ID, "defer", "later", deadline); err != nil {
		t.Fatal(err)
	}
	if err := s.SettleBatch(b.ID, "defer", "later", deadline); err != nil {
		t.Fatal("decision replay", err)
	}
	if claim(t, s, w.Spec.ID, 32) != nil {
		t.Fatal("defer ignored deadline")
	}
	if _, err := s.db.Exec("UPDATE sentinel_batches SET not_before=0 WHERE id=?", b.ID); err != nil {
		t.Fatal(err)
	}
	next := claim(t, s, w.Spec.ID, 32)
	if next == nil || next.ID != b.ID || next.Attempt != 2 || next.RunID != "" || len(next.Events) != 1 {
		t.Fatalf("deferred identity=%+v", next)
	}
}
func TestRestartPreservesQueueAndRequiresExplicitRetry(t *testing.T) {
	s, path := testStore(t)
	w := createWatch(t, s)
	acceptFrame(t, s, w, oneFrame("f1", "e1", 1))
	b := claim(t, s, w.Spec.ID, 32)
	if err := s.SetBatchRun(b.ID, "session-1", "run-1", "running"); err != nil {
		t.Fatal(err)
	}
	acceptFrame(t, s, w, oneFrame("f2", "e2", 2))
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	still, _ := s2.GetBatch(b.ID)
	if still.Status != "running" {
		t.Fatal("Open unexpectedly took ownership of live work")
	}
	if err = s2.Recover(); err != nil {
		t.Fatal(err)
	}
	got, err := s2.Get(w.Spec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Pending != 2 || got.Spec.Generation <= w.Spec.Generation {
		t.Fatal("recovery lost queue or collector fence")
	}
	interrupted, _ := s2.GetBatch(b.ID)
	if interrupted.Status != "interrupted" || interrupted.RunID != "run-1" {
		t.Fatal("unknown execution not retained")
	}
	if claim(t, s2, w.Spec.ID, 32) != nil {
		t.Fatal("recovery silently repeated external work")
	}
	if err = s2.RetryBatch(b.ID); err != nil {
		t.Fatal(err)
	}
	retried := claim(t, s2, w.Spec.ID, 32)
	if retried == nil || retried.ID != b.ID || retried.Attempt != 2 || retried.RunID != "" {
		t.Fatal("explicit retry did not preserve event identity/new attempt")
	}
}
func TestDecisionsAreImmutableAndRemovalDoesNotHideActiveWork(t *testing.T) {
	s, _ := testStore(t)
	w := createWatch(t, s)
	acceptFrame(t, s, w, oneFrame("f1", "e1", 1))
	b := claim(t, s, w.Spec.ID, 32)
	if err := s.Remove(w.Spec.ID); !errors.Is(err, ErrBatchActive) {
		t.Fatalf("active removal=%v", err)
	}
	if err := s.SettleBatch(b.ID, "report", "report text", 0); err != nil {
		t.Fatal(err)
	}
	if err := s.SettleBatch(b.ID, "report", "report text", 0); err != nil {
		t.Fatal(err)
	}
	if err := s.SettleBatch(b.ID, "ignore", "", 0); !errors.Is(err, ErrConflict) {
		t.Fatal("completed result overwritten", err)
	}
	if err := s.Remove(w.Spec.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(w.Spec.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("removed watch=%v", err)
	}
	history, err := s.ListBatches(w.Spec.ID, 10)
	if err != nil || len(history) != 1 || history[0].Summary != "report text" {
		t.Fatal("removal erased audit", err)
	}
	list, err := s.List("tenant-a", "")
	if err != nil || len(list) != 0 {
		t.Fatal("removed watch listed", err)
	}
}
func TestTenantListAndProtocolValidation(t *testing.T) {
	s, _ := testStore(t)
	w := createWatch(t, s)
	other := testSpec(t)
	other.OwnerID = "tenant-b"
	if _, err := s.Create(other); err != nil {
		t.Fatal(err)
	}
	list, err := s.List("tenant-a", "")
	if err != nil || len(list) != 1 || list[0].Spec.ID != w.Spec.ID {
		t.Fatal("cross-tenant list", err)
	}
	f := oneFrame("f1", "e1", 1)
	f.State = json.RawMessage(`not json`)
	if err := ValidateFrame(f); err == nil {
		t.Fatal("invalid JSON accepted")
	}
	f = oneFrame("f1", "e1", 1)
	f.V = 2
	if err := ValidateFrame(f); err == nil {
		t.Fatal("unsupported protocol accepted")
	}
	f = oneFrame("f1", "e1", 1)
	wire, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	json.Unmarshal(wire, &fields)
	if fields["id"] == nil || fields["frame_id"] != nil {
		t.Fatalf("wrong public protocol %s", wire)
	}
}

func TestBusyAdmissionRequeuesSameAttemptOnlyBeforeRun(t *testing.T) {
	s, _ := testStore(t)
	w := createWatch(t, s)
	acceptFrame(t, s, w, oneFrame("f1", "e1", 1))
	b := claim(t, s, w.Spec.ID, 32)
	if err := s.SetBatchRun(b.ID, "session-1", "", "retry_pending"); err != nil {
		t.Fatal(err)
	}
	next := claim(t, s, w.Spec.ID, 32)
	if next == nil || next.ID != b.ID || next.Attempt != 1 {
		t.Fatal("busy admission changed logical wake")
	}
	if err := s.SetBatchRun(b.ID, "session-1", "run-1", "running"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetBatchRun(b.ID, "session-1", "", "retry_pending"); !errors.Is(err, ErrConflict) {
		t.Fatal("execution with possible effects silently requeued", err)
	}
}
func TestSupervisorListAllAndTenantListsStaySeparate(t *testing.T) {
	s, _ := testStore(t)
	a := testSpec(t)
	a.OwnerID = "tenant-a"
	b := testSpec(t)
	b.OwnerID = "tenant-b"
	if _, err := s.Create(a); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(b); err != nil {
		t.Fatal(err)
	}
	all, err := s.ListAll()
	if err != nil || len(all) != 2 {
		t.Fatal("supervisor cannot see watches", err)
	}
	none, err := s.List("", "")
	if err != nil || len(none) != 0 {
		t.Fatal("empty owner key became wildcard", err)
	}
}
func TestExpiredDeferReceiptCanRecoverAndResume(t *testing.T) {
	s, _ := testStore(t)
	w := createWatch(t, s)
	acceptFrame(t, s, w, oneFrame("f1", "e1", 1))
	b := claim(t, s, w.Spec.ID, 32)
	past := time.Now().Add(-time.Hour).UnixMilli()
	if err := s.SettleBatch(b.ID, "defer", "restored decision", past); err != nil {
		t.Fatal(err)
	}
	if err := s.SettleBatch(b.ID, "defer", "restored decision", past); err != nil {
		t.Fatal("idempotent replay after deadline", err)
	}
	next := claim(t, s, w.Spec.ID, 32)
	if next == nil || next.Attempt != 2 {
		t.Fatal("restored expired defer did not become eligible")
	}
}

func TestRetiredCollectorCannotOverwriteNewRuntimeStatus(t *testing.T) {
	s, _ := testStore(t)
	w := createWatch(t, s)
	resumed, err := s.SetEnabled(w.Spec.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.SetRuntimeGeneration(w.Spec.ID, resumed.Spec.Generation, "watching", "", 0, 0); err != nil {
		t.Fatal(err)
	}
	if err = s.SetRuntimeGeneration(w.Spec.ID, w.Spec.Generation, "error", "late callback", 0, 5); !errors.Is(err, ErrStaleGeneration) {
		t.Fatal("retired status accepted", err)
	}
	got, _ := s.Get(w.Spec.ID)
	if got.Status != "watching" || got.Error != "" {
		t.Fatal("retired callback replaced new collector")
	}
}
func TestLongRuntimeErrorIsVisibleAndBounded(t *testing.T) {
	s, _ := testStore(t)
	w := createWatch(t, s)
	message := string(make([]byte, 8192))
	if err := s.SetRuntimeGeneration(w.Spec.ID, w.Spec.Generation, "error", message, 0, 1); err != nil {
		t.Fatal(err)
	}
	got, _ := s.Get(w.Spec.ID)
	if got.Status != "error" || len(got.Error) != 4096 {
		t.Fatal("large error silently left a healthy runtime state")
	}
}

func TestBatchInputBudgetKeepsLargePayloadsForLater(t *testing.T) {
	s, _ := testStore(t)
	w := createWatch(t, s)
	for i := 0; i < 3; i++ {
		data, _ := json.Marshal(map[string]string{"payload": strings.Repeat("x", 28<<10)})
		f := Frame{V: 1, ID: fmt.Sprint(i), Events: []InputEvent{{Key: fmt.Sprint(i), Summary: "changed", Data: data}}}
		acceptFrame(t, s, w, f)
	}
	b := claim(t, s, w.Spec.ID, MaxBatchEvents)
	if b == nil || len(b.Events) != 2 {
		t.Fatalf("bounded batch=%+v", b)
	}
	if err := s.SettleBatch(b.ID, "ignore", "", 0); err != nil {
		t.Fatal(err)
	}
	next := claim(t, s, w.Spec.ID, MaxBatchEvents)
	if next == nil || len(next.Events) != 1 {
		t.Fatal("deferred payload lost")
	}
}
