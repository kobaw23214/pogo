package web

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/taskqueue"
	"github.com/yosukeno/pogo/workflow"
)

type workflowCallbackReceipt struct {
	ID        string
	Payload   []byte
	Timestamp string
	Signature string
}

func enableCallbackFixture(t *testing.T, h *muHarness, wf Workflow, p workflowPublication, destination string) (workflowPublication, string) {
	t.Helper()
	p.Callback = &workflowPublicationCallback{Enabled: true, URL: destination}
	response := embedJSON(t, h, h.token, "PUT", "/api/manage/workflows/"+wf.ID+"/publications/"+p.ID, p, 200)
	signing := embedString(t, response, "callback_secret")
	if err := json.Unmarshal(mustMarshalCallback(t, response), &p); err != nil {
		t.Fatal(err)
	}
	return p, signing
}
func mustMarshalCallback(t *testing.T, value any) []byte {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func waitWorkflowCallback(t *testing.T, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("callback did not reach expected durable state")
}
func callbackDeliveryList(t *testing.T, h *muHarness, p workflowPublication) []taskqueue.WorkflowCallbackDelivery {
	t.Helper()
	db, err := h.mgr.taskRuns.database(false)
	if err != nil || db == nil {
		t.Fatalf("db: %v", err)
	}
	out, err := db.ListWorkflowCallbackDeliveries(p.ID, 100)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
func TestWorkflowCallbackHTTPRetriesSignatureWithoutOpenPage(t *testing.T) {
	var key atomic.Value
	key.Store("")
	var mu sync.Mutex
	received := []workflowCallbackReceipt{}
	failures := atomic.Int32{}
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 64<<10))
		id := r.Header.Get("X-Pogo-Event-Id")
		timestamp := r.Header.Get("X-Pogo-Timestamp")
		signature := r.Header.Get("X-Pogo-Signature")
		mac := hmac.New(sha256.New, []byte(key.Load().(string)))
		_, _ = io.WriteString(mac, id+"."+timestamp+".")
		_, _ = mac.Write(body)
		if signature != "sha256="+hex.EncodeToString(mac.Sum(nil)) {
			t.Error("invalid callback signature")
		}
		epoch, err := strconv.ParseInt(timestamp, 10, 64)
		if err != nil || time.Since(time.Unix(epoch, 0)) > time.Minute {
			t.Error("invalid callback timestamp")
		}
		mu.Lock()
		received = append(received, workflowCallbackReceipt{id, bytes.Clone(body), timestamp, signature})
		mu.Unlock()
		if failures.Add(1) == 1 {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(204)
	}))
	defer receiver.Close()
	h, wf, p, clientSecret := workflowEmbedFixture(t)
	p, signing := enableCallbackFixture(t, h, wf, p, receiver.URL+"/callback")
	key.Store(signing)
	token := embedSession(t, h, p, clientSecret, "employee-a")
	start := embedJSON(t, h, token, "POST", embedPath(p, "runs"), map[string]any{"inputs": map[string]string{"case_id": "CASE-42"}, "request_key": "without-open-page"}, 201)
	runID := embedString(t, start, "id")
	rec := awaitWorkflowService(t, h.mgr, runID, func(r workflow.RunRecord) bool { return r.Nodes["review"].Status == workflow.NodeWaitingHuman })
	if rec.Origin == nil || rec.Origin.BusinessID != "business-42" {
		t.Fatalf("origin=%+v", rec.Origin)
	}
	embedJSON(t, h, token, "POST", embedPath(p, "runs/"+runID+"/nodes/review/resume"), map[string]any{"action": "approve", "fields": map[string]string{"note": "this opinion must not be sent"}}, 200)
	waitWorkflowCallback(t, func() bool {
		rows := callbackDeliveryList(t, h, p)
		terminal := false
		for _, d := range rows {
			if d.EventType == "run.completed" {
				terminal = d.Status == "delivered"
			}
		}
		return terminal
	})
	rows := callbackDeliveryList(t, h, p)
	if len(rows) < 2 {
		t.Fatalf("missing callback state rows: %+v", rows)
	}
	mu.Lock()
	receipts := append([]workflowCallbackReceipt(nil), received...)
	mu.Unlock()
	if len(receipts) < 3 || receipts[0].ID != receipts[1].ID || !bytes.Equal(receipts[0].Payload, receipts[1].Payload) {
		t.Fatalf("retry identity/body changed: %+v", receipts)
	}
	for _, v := range receipts {
		if bytes.Contains(v.Payload, []byte("this opinion")) || bytes.Contains(v.Payload, []byte("CASE-42")) {
			t.Fatal("private approval or inputs sent")
		}
	}
	listed := embedJSON(t, h, h.token, "GET", "/api/manage/workflows/"+wf.ID+"/publications/"+p.ID+"/deliveries", nil, 200)
	if bytes.Contains(mustMarshalCallback(t, listed), []byte(signing)) {
		t.Fatal("signing secret exposed in delivery list")
	}
	embedJSON(t, h, aliceToken, "GET", "/api/manage/workflows/"+wf.ID+"/publications/"+p.ID+"/deliveries", nil, 403)
	// Work exists without any browser/SSE observer, with the same business Run.
	final, _ := h.mgr.wfruns.snapshot(runID)
	if final.Status != workflow.RunCompleted {
		t.Fatalf("status=%s", final.Status)
	}
}

func TestWorkflowCallbackRecoveryPausedTargetChangeAndReplay(t *testing.T) {
	receiver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer receiver.Close()
	h, wf, p, _ := workflowEmbedFixture(t)
	p, _ = enableCallbackFixture(t, h, wf, p, receiver.URL+"/first")
	// Stop the automatic worker to model a process gone between durable run.json
	// and callback insertion. Its replacement reconciles the same disk state.
	h.psrv.stopWorkflowCallbacks()
	privatePublication, _ := h.psrv.workflowEmbed.publications.get(p.ID)
	target := workflowPublicationCallbackTarget(privatePublication)
	def, err := h.mgr.workflows.publishedDefinition(wf.ID)
	if err != nil {
		t.Fatal(err)
	}
	owner := identityScopeCallback("alice")
	meta, err := h.mgr.admitWorkflowWithOptions(def, &owner, "publication", p.ID, "reconcile-only", "", workflowAdmissionOptions{Inputs: map[string]string{"case_id": "recover"}, PublicationID: p.ID, BusinessID: "business-42", Callback: target})
	if err != nil {
		t.Fatal(err)
	}
	rec := awaitWorkflowService(t, h.mgr, meta.ID, func(r workflow.RunRecord) bool { return r.Nodes["review"].Status == workflow.NodeWaitingHuman })
	rows := callbackDeliveryList(t, h, p)
	if len(rows) != 1 || rows[0].EventType != "run.queued" {
		t.Fatalf("stopped worker recorded transition: %+v", rows)
	}
	replacement := newWorkflowCallbackRuntime(h.psrv)
	defer replacement.cancel()
	db := taskRunsDB(t, h.mgr)
	if err = replacement.reconcile(db); err != nil {
		t.Fatal(err)
	}
	rows = callbackDeliveryList(t, h, p)
	if len(rows) != 2 {
		t.Fatalf("reconcile did not recover waiting transition %+v", rows)
	}
	// Disable is a durable pause; it does not discard accepted events.
	stored, _ := h.psrv.workflowEmbed.publications.get(p.ID)
	stored.Callback.Enabled = false
	if err = h.psrv.workflowEmbed.publications.save(stored, stored.Revision); err != nil {
		t.Fatal(err)
	}
	replacement.pump()
	rows = callbackDeliveryList(t, h, p)
	var queued taskqueue.WorkflowCallbackDelivery
	for _, d := range rows {
		if d.EventType == "run.queued" {
			queued = d
		}
	}
	if queued.Status != "paused" || queued.Attempts != 0 {
		t.Fatalf("not paused: %+v", queued)
	}
	stored, _ = h.psrv.workflowEmbed.publications.get(p.ID)
	stored.Callback.Enabled = true
	stored.Callback.URL = receiver.URL + "/second"
	if err = h.psrv.workflowEmbed.publications.save(stored, stored.Revision); err != nil {
		t.Fatal(err)
	}
	replacement.now = func() time.Time { return time.Now().Add(2 * time.Second) }
	replacement.pump()
	blocked, _ := db.WorkflowCallbackDelivery(queued.ID)
	if blocked.Status != "blocked" || blocked.Attempts != 0 {
		t.Fatalf("old target silently replaced: %+v", blocked)
	}
	body := map[string]bool{"use_current_target": false}
	path := "/api/manage/workflows/" + wf.ID + "/publications/" + p.ID + "/deliveries/" + queued.ID + "/retry"
	embedJSON(t, h, h.token, "POST", path, body, 409)
	embedJSON(t, h, h.token, "POST", path, map[string]bool{"use_current_target": true}, 200)
	replacement.pump()
	delivered, _ := db.WorkflowCallbackDelivery(queued.ID)
	if delivered.Status != "delivered" || delivered.TargetURL != stored.Callback.URL || !bytes.Equal(delivered.Payload, queued.Payload) {
		t.Fatalf("manual replay=%+v", delivered)
	}
	// A run admitted before callback enrollment stays unchanged after enabling.
	old, err := h.mgr.admitWorkflowWithOptions(def, &owner, "manual", wf.ID, "old-no-callback", "", workflowAdmissionOptions{Inputs: map[string]string{"case_id": "old"}})
	if err != nil {
		t.Fatal(err)
	}
	oldRecord := awaitWorkflowService(t, h.mgr, old.ID, func(r workflow.RunRecord) bool { return r.Nodes["review"].Status == workflow.NodeWaitingHuman })
	if err = replacement.record(oldRecord); err != nil {
		t.Fatal(err)
	}
	subs, err := db.ListWorkflowCallbackSubscriptions()
	if err != nil || len(subs) != 1 {
		t.Fatalf("history enrolled %+v %v", subs, err)
	}
	// File/outbox recovery reads the already-persisted record, preserving Run ID.
	raw, err := os.ReadFile(filepath.Join(h.mgr.wfruns.runDir(rec.ID), "run.json"))
	if err != nil || !bytes.Contains(raw, []byte(meta.ID)) {
		t.Fatalf("record gone: %v", err)
	}
}
func identityScopeCallback(username string) identity.Scope { return identity.LocalScope(username) }

func TestWorkflowCallbackURLValidationSecretPersistenceAndRedirect(t *testing.T) {
	for _, bad := range []string{"http://business.example/cb", "https://user:pw@example.com/cb", "https://example.com/cb?token=secret", "https://169.254.169.254/latest", "https://[fe80::1]/cb", "https://224.0.0.1/cb", "https://0.0.0.0/cb", "https://example.com/#fragment"} {
		if err := validateWorkflowCallbackURL(bad, true); err == nil {
			t.Fatalf("unsafe url accepted %q", bad)
		}
	}
	for _, good := range []string{"https://business.example/callback", "http://127.0.0.1:5199/callback", "http://[::1]:5199/callback"} {
		if err := validateWorkflowCallbackURL(good, true); err != nil {
			t.Fatalf("rejected %q: %v", good, err)
		}
	}
	var followed atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { followed.Add(1); w.WriteHeader(204) }))
	defer destination.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, 302) }))
	defer redirect.Close()
	client := newWorkflowCallbackHTTPClient()
	defer client.CloseIdleConnections()
	response, err := client.Post(redirect.URL, "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 302 || followed.Load() != 0 {
		t.Fatal("callback redirect followed")
	}
	h, wf, p, _ := workflowEmbedFixture(t)
	p, key := enableCallbackFixture(t, h, wf, p, destination.URL)
	reopened, err := openWorkflowPublications(h.mgr.cfg.SessionDir)
	if err != nil {
		t.Fatal(err)
	}
	stored, ok := reopened.get(p.ID)
	if !ok || stored.SigningSecret != key {
		t.Fatal("callback signing key did not persist")
	}
	jsonList := embedJSON(t, h, h.token, "GET", "/api/manage/workflows/"+wf.ID+"/publications", nil, 200)
	if bytes.Contains(mustMarshalCallback(t, jsonList), []byte(key)) {
		t.Fatal("callback key leaked")
	}
	rotation := embedJSON(t, h, h.token, "POST", "/api/manage/workflows/"+wf.ID+"/publications/"+p.ID+"/rotate-callback-secret", map[string]any{}, 200)
	rotated := embedString(t, rotation, "callback_secret")
	if rotated == "" || rotated == key {
		t.Fatal("callback secret rotation failed")
	}
}
