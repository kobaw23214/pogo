package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestOfficeActivityTracksRealDispatchAndVerifiedWordReceipt(t *testing.T) {
	service, document, _ := officeTestService(t)
	instance := officeTestReady(t, service, document)
	const id = "activity_word_replace_001"
	args := json.RawMessage(`{"search":"待评审","replacement":"评审中","expectedText":"人工输入保留\n状态：待评审\n"}`)
	done := make(chan error, 1)
	go func() {
		_, err := service.command(context.Background(), document, id, "word.replace", args)
		done <- err
	}()
	officeTestQueued(t, document, id)
	queued := document.view().Activity
	if len(queued.Items) != 1 || queued.Items[0].Status != "queued" || queued.Items[0].Actor != officeActivityAgent || queued.Items[0].Verified {
		t.Fatal("queued action claimed editor work")
	}
	officeTestDispatch(t, service, document, instance, id)
	running := document.view().Activity
	if running.Version <= queued.Version || running.Items[0].Status != "running" || running.Items[0].StartedAt == "" {
		t.Fatal("dispatch did not begin activity")
	}
	response := officeTestBridge(t, service, document, "POST", "results", map[string]any{"instanceId": instance, "operationId": id, "ok": true,
		"result": map[string]any{"before": map[string]any{"kind": "word", "text": "人工输入保留\n状态：待评审\n"}, "after": map[string]any{"kind": "word", "text": "人工输入保留\n状态：评审中\n"}}}, "")
	if response.Code != http.StatusOK || <-done != nil {
		t.Fatal("native receipt was not accepted")
	}
	finished := document.view().Activity
	item := finished.Items[0]
	if finished.Version <= running.Version || item.Status != "succeeded" || !item.Verified || item.ChangeCount == nil || *item.ChangeCount != 1 || item.FinishedAt == "" || len(item.Changes) != 1 || item.Changes[0].Before != "待评审" || item.Changes[0].After != "评审中" {
		t.Fatalf("wrong completed receipt: %#v", item)
	}
	encoded, _ := json.Marshal(finished)
	if strings.Contains(string(encoded), "人工输入保留") {
		t.Fatal("activity exposed the full Word document")
	}
	_, err := service.command(context.Background(), document, id, "word.replace", args)
	if err != nil || document.view().Activity.Version != finished.Version {
		t.Fatal("idempotent replay created a second activity/write")
	}
}

func TestOfficeActivityNativeDiffRequiresMatchingBeforeAndAfter(t *testing.T) {
	for _, test := range []struct{ before, after string }{
		{"not the expected target", "new"}, {"target", "new plus unexpected text"},
	} {
		item := officeActivity{Target: officeActivityTarget{Kind: "word"}}
		op := &officeOperation{name: "word.replace", args: json.RawMessage(`{"search":"target","replacement":"new","expectedText":"target","expectedCount":1}`)}
		result, _ := json.Marshal(map[string]any{"before": map[string]any{"kind": "word", "text": test.before}, "after": map[string]any{"kind": "word", "text": test.after}})
		officeActivityMutation(&item, op, result)
		if item.Verified || item.ChangeCount != nil || len(item.Changes) != 0 {
			t.Fatal("unmatched native receipt became a claimed edit")
		}
	}
}

func TestOfficeActivityCellReceiptsAreBoundedAndReadScopeIsNarrow(t *testing.T) {
	service, document, _ := officeTestService(t)
	document.editor = "cell"
	instance := officeTestReady(t, service, document)
	const id = "activity_sheet_apply_001"
	old, updated := make([]any, 10), make([]any, 10)
	for i := range old {
		old[i], updated[i] = i, strings.Repeat("中", 300)
	}
	args, _ := json.Marshal(map[string]any{"sheet": "明细", "range": "C2:L2", "expectedInputs": [][]any{old}, "inputs": [][]any{updated}})
	done := make(chan error, 1)
	go func() {
		_, err := service.command(context.Background(), document, id, "sheet.apply", args)
		done <- err
	}()
	officeTestQueued(t, document, id)
	officeTestDispatch(t, service, document, instance, id)
	response := officeTestBridge(t, service, document, "POST", "results", map[string]any{"instanceId": instance, "operationId": id, "ok": true,
		"result": map[string]any{"before": map[string]any{"kind": "cell", "sheet": "明细", "range": "C2:L2", "inputs": [][]any{old}}, "after": map[string]any{"kind": "cell", "sheet": "明细", "range": "C2:L2", "inputs": [][]any{updated}}}}, "")
	if response.Code != 200 || <-done != nil {
		t.Fatal("native cell receipt failed")
	}
	item := document.view().Activity.Items[0]
	if !item.Verified || item.ChangeCount == nil || *item.ChangeCount != 10 || len(item.Changes) != 6 || !item.Truncated || item.Changes[0].Address != "C2" || item.Changes[5].Address != "H2" || len([]rune(item.Changes[0].After)) != 161 {
		t.Fatalf("unbounded/incorrect cell changes: %#v", item)
	}
	readResult, _ := json.Marshal(map[string]any{"kind": "cell", "sheet": "明细", "range": "C2:L2", "inputs": [][]any{updated}})
	verification := officeActivity{Target: item.Target, verificationOf: id, verificationEpoch: item.dirtyEpoch}
	document.mu.Lock()
	if !document.verifyActivityReadLocked(&verification, readResult) {
		t.Fatal("same native inputs did not verify the exact changed scope")
	}
	wrongRead, _ := json.Marshal(map[string]any{"kind": "cell", "sheet": "明细", "range": "C2:L2", "inputs": [][]any{old}})
	if document.verifyActivityReadLocked(&verification, wrongRead) {
		t.Fatal("mismatched later inputs claimed verification")
	}
	document.mu.Unlock()
	document.mu.Lock()
	read := &officeOperation{id: "read_matching_scope", name: "sheet.read", args: json.RawMessage(`{"sheet":"明细","range":"C2:L2"}`)}
	document.addActivityLocked(read, officeActivityAgent)
	if document.activity[0].Phase != "verifying" {
		t.Fatal("post-write read did not preserve its exact scope")
	}
	other := &officeOperation{id: "read_other_scope", name: "sheet.read", args: json.RawMessage(`{"sheet":"汇总","range":"C2:L2"}`)}
	document.addActivityLocked(other, officeActivityAgent)
	if document.activity[0].Phase != "reading" {
		t.Fatal("different worksheet claimed write verification")
	}
	document.dirtyEpoch++
	document.addActivityLocked(&officeOperation{id: "read_after_manual_input", name: "sheet.read", args: read.args}, officeActivityAgent)
	if document.activity[0].Phase != "reading" {
		t.Fatal("manual input epoch was ignored")
	}
	document.mu.Unlock()
}

func TestOfficeActivityLateReadRechecksLiveVerificationAssociation(t *testing.T) {
	for _, scenario := range []string{"unchanged", "human-state-before-read-receipt", "history-queued-after-read"} {
		t.Run(scenario, func(t *testing.T) {
			service, document, _ := officeTestService(t)
			document.editor = "cell"
			instance := officeTestReady(t, service, document)
			const writeID, readID = "verification_write_001", "verification_read_001"
			writeDone := make(chan error, 1)
			go func() {
				_, err := service.command(context.Background(), document, writeID, "sheet.apply", json.RawMessage(`{"sheet":"明细","range":"C2","expectedInputs":[[2]],"inputs":[[5]]}`))
				writeDone <- err
			}()
			officeTestQueued(t, document, writeID)
			officeTestDispatch(t, service, document, instance, writeID)
			old := map[string]any{"kind": "cell", "sheet": "明细", "range": "C2", "inputs": [][]any{{2}}}
			updated := map[string]any{"kind": "cell", "sheet": "明细", "range": "C2", "inputs": [][]any{{5}}}
			response := officeTestBridge(t, service, document, "POST", "results", map[string]any{"instanceId": instance, "operationId": writeID, "ok": true, "result": map[string]any{"before": old, "after": updated}}, "")
			if response.Code != 200 || <-writeDone != nil {
				t.Fatal("verified write setup failed")
			}
			readDone := make(chan error, 1)
			go func() {
				_, err := service.command(context.Background(), document, readID, "sheet.read", json.RawMessage(`{"sheet":"明细","range":"C2"}`))
				readDone <- err
			}()
			officeTestQueued(t, document, readID)
			officeTestDispatch(t, service, document, instance, readID)
			if item := document.view().Activity.Items[0]; item.Phase != "verifying" || item.Status != "running" {
				t.Fatal("read did not initially associate with the confirmed write")
			}
			// The native read has captured `updated`, but its HTTP result is
			// delayed. Deliver a real human-state event or queue history first.
			var historyDone chan error
			if scenario == "human-state-before-read-receipt" {
				response = officeTestBridge(t, service, document, "POST", "state", map[string]any{"instanceId": instance, "dirty": true}, "")
				if response.Code != 200 {
					t.Fatal("interleaved human edit was rejected")
				}
			}
			if scenario == "history-queued-after-read" {
				historyDone = make(chan error, 1)
				go func() {
					_, err := service.commandAs(context.Background(), document, "verification_history_001", "history.undo", json.RawMessage(`{}`), officeActivityUser)
					historyDone <- err
				}()
				officeTestQueued(t, document, "verification_history_001")
			}
			response = officeTestBridge(t, service, document, "POST", "results", map[string]any{"instanceId": instance, "operationId": readID, "ok": true, "result": updated}, "")
			if response.Code != 200 || <-readDone != nil {
				t.Fatal("late read receipt was rejected instead of remaining an ordinary read")
			}
			var read officeActivity
			for _, item := range document.view().Activity.Items {
				if item.OperationID == readID {
					read = item
					break
				}
			}
			if scenario == "unchanged" {
				if !read.Verified || read.Phase != "verifying" || read.Status != "succeeded" {
					t.Fatal("unchanged exact scope lost its native verification")
				}
			} else if read.Verified || read.Phase != "reading" || read.Status != "succeeded" {
				t.Fatalf("old read snapshot claimed current verification after %s: %#v", scenario, read)
			}
			if historyDone != nil {
				officeTestDispatch(t, service, document, instance, "verification_history_001")
				response = officeTestBridge(t, service, document, "POST", "results", map[string]any{"instanceId": instance, "operationId": "verification_history_001", "ok": true, "result": map[string]any{"native": true, "historyScope": "editor-global"}}, "")
				if response.Code != 200 || <-historyDone != nil {
					t.Fatal("queued history did not complete")
				}
			}
		})
	}
}

func BenchmarkOfficeActivityViewBounded(b *testing.B) {
	document := &officeDocumentSession{}
	for i := 0; i < officeActivityLimit; i++ {
		changes := make([]officeActivityChange, officeActivityChangeLimit)
		for c := range changes {
			changes[c] = officeActivityChange{Address: officeActivityCell(2, c+1), Before: strings.Repeat("😀", 160), After: strings.Repeat("中", 160)}
		}
		count := officeActivityChangeLimit
		document.activity = append(document.activity, officeActivity{OperationID: "bounded_activity_" + strconv.Itoa(i), Operation: "sheet.apply", Actor: officeActivityAgent, Phase: "writing", Status: "succeeded", Target: officeActivityTarget{Kind: "cell", Sheet: "明细", Range: "A2:F2"}, Changes: changes, ChangeCount: &count, Verified: true})
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = document.activityViewLocked()
	}
}

func TestOfficeActivityRejectionAndUnknownCannotClaimSuccess(t *testing.T) {
	service, document, _ := officeTestService(t)
	_, err := service.command(context.Background(), document, "invalid_activity_001", "word.replace", json.RawMessage(`{"search":"target"}`))
	if err == nil || len(document.view().Activity.Items) != 1 || document.view().Activity.Items[0].Status != "rejected" || document.view().Activity.Items[0].StartedAt != "" {
		t.Fatal("pre-dispatch rejection appeared executed")
	}
	instance := officeTestReady(t, service, document)
	const id = "activity_unknown_001"
	done := make(chan error, 1)
	go func() {
		_, err := service.command(context.Background(), document, id, "word.replace", json.RawMessage(`{"search":"target","replacement":"new","expectedText":"target"}`))
		done <- err
	}()
	op := officeTestQueued(t, document, id)
	officeTestDispatch(t, service, document, instance, id)
	service.expire(document, op)
	if <-done != nil {
		t.Fatal("ledger failure did not return its receipt")
	}
	item := document.view().Activity.Items[0]
	if item.Status != "unknown" || item.Verified || item.ChangeCount != nil || len(item.Changes) != 0 {
		t.Fatal("uncertain outcome became success")
	}
	version := document.view().Activity.Version
	officeTestBridge(t, service, document, "POST", "results", map[string]any{"instanceId": instance, "operationId": id, "ok": true, "result": map[string]any{}}, "")
	if document.view().Activity.Version != version {
		t.Fatal("late result changed terminal activity")
	}
}

func TestOfficeActivitySourceIsHostBoundAndNextOnlyReturnsChangedProjection(t *testing.T) {
	service, document, _ := officeTestService(t)
	instance := officeTestReady(t, service, document)
	const id = "toolbar_history_activity"
	done := make(chan error, 1)
	go func() {
		_, err := service.commandAs(context.Background(), document, id, "history.undo", json.RawMessage(`{}`), officeActivityUser)
		done <- err
	}()
	op := officeTestQueued(t, document, id)
	response := officeTestBridge(t, service, document, "GET", "next", nil, instance)
	var dispatched struct {
		Command struct {
			Actor string `json:"actor"`
		} `json:"command"`
		Activity officeActivityView `json:"activity"`
	}
	if json.Unmarshal(response.Body.Bytes(), &dispatched) != nil || dispatched.Command.Actor != "user" || strings.Contains(response.Body.String(), `"activity"`) {
		t.Fatal("toolbar source was not preserved")
	}
	request := httptest.NewRequest("GET", document.browserOrigin+"/office/bridge/?instanceId="+instance+"&activityVersion=0", nil)
	request.SetPathValue("oid", document.id)
	request.SetPathValue("cap", document.bridgeToken)
	request.SetPathValue("resource", "next")
	response = httptest.NewRecorder()
	(&Server{office: service}).handleOfficeBridge(response, request)
	if json.Unmarshal(response.Body.Bytes(), &dispatched) != nil || len(dispatched.Activity.Items) != 1 || dispatched.Activity.Items[0].Actor != officeActivityUser {
		t.Fatal("upgraded client did not receive changed activity")
	}
	query := request.URL.Query()
	query.Set("activityVersion", strconv.FormatUint(dispatched.Activity.Version, 10))
	request.URL.RawQuery = query.Encode()
	response = httptest.NewRecorder()
	(&Server{office: service}).handleOfficeBridge(response, request)
	if strings.Contains(response.Body.String(), `"activity"`) || strings.Contains(response.Body.String(), `"session"`) {
		t.Fatal("unchanged fast poll recopied document/activity")
	}
	document.mu.Lock()
	service.failOperationLocked(document, op, officeFail(409, "NO_HISTORY", "nothing to undo"))
	document.mu.Unlock()
	<-done
	link := service.link(document.conversationID, document.cwd, nil, nil)
	_, err := link.Execute(context.Background(), json.RawMessage(`{"action":"command","document_session_id":"`+document.id+`","operation_id":"actor_spoof_attempt","operation":"word.read","args":{},"actor":"user"}`))
	if err == nil {
		t.Fatal("model forged human actor in tool request")
	}
}

func TestOfficeActivityDiscoveryOmitsHistoryWithoutEmptyVersionSnapshot(t *testing.T) {
	service, document, _ := officeTestService(t)
	_, err := service.command(context.Background(), document, "discovery_invalid_command", "word.replace", json.RawMessage(`{"search":"target"}`))
	if err == nil || document.view().Activity.Version == 0 {
		t.Fatal("nonempty history setup failed")
	}
	listed := service.list(document.conversationID, document.owner)
	if len(listed) != 1 || listed[0].ID != document.id || listed[0].Activity.Version != 0 || listed[0].Activity.Items != nil {
		t.Fatal("metadata discovery copied activity")
	}
	encoded, _ := json.Marshal(listed)
	if strings.Contains(string(encoded), `"activity"`) || strings.Contains(string(encoded), "discovery_invalid_command") {
		t.Fatal("discovery sent a fake empty activity snapshot or a prior diff")
	}
	status, _ := json.Marshal(document.view())
	if !strings.Contains(string(status), `"activity"`) || !strings.Contains(string(status), "discovery_invalid_command") {
		t.Fatal("status lost actual activity")
	}
}

func TestOfficeActivityRingKeepsPendingAndNeverRetainsLargeResults(t *testing.T) {
	_, document, _ := officeTestService(t)
	document.mu.Lock()
	defer document.mu.Unlock()
	pending := &officeOperation{id: "pending_activity_001", name: "word.read", args: json.RawMessage(`{}`)}
	document.addActivityLocked(pending, officeActivityAgent)
	for i := 0; i < 80; i++ {
		op := &officeOperation{id: "bounded_activity_" + strconv.Itoa(i), name: "word.read", args: json.RawMessage(`{}`)}
		document.addActivityLocked(op, officeActivityAgent)
		result, _ := json.Marshal(map[string]any{"ok": true, "result": map[string]any{"kind": "word", "text": strings.Repeat("private document body", 10000)}})
		document.finishActivityLocked(op, result, "", "")
	}
	if len(document.activity) != officeActivityLimit || document.activityLocked(pending) == nil {
		t.Fatal("activity ring grew or evicted pending operation")
	}
	projection, _ := json.Marshal(document.activityViewLocked())
	if len(projection) > 12000 || strings.Contains(string(projection), "private document body") {
		t.Fatal("status projection retained full text")
	}
}

func TestOfficeActivitySavedOnlyAfterWorkspaceCommit(t *testing.T) {
	service, document, _ := officeTestService(t)
	instance := officeTestReady(t, service, document)
	document.mu.Lock()
	document.dirtyEpoch = 1
	document.saveState = "dirty"
	document.mu.Unlock()
	done := officeTestStartSave(t, service, document, instance, "activity_save_commit_001")
	if item := document.view().Activity.Items[0]; item.Status != "running" || item.Verified || item.Revision != 0 {
		t.Fatal("export dispatch claimed workspace persistence")
	}
	document.mu.Lock()
	op := document.exportIntent
	document.mu.Unlock()
	if err := service.commitExport(document, op, instance, officeTestPackage(t, "saved")); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	view := document.view()
	item := view.Activity.Items[0]
	if item.Status != "succeeded" || !item.Verified || item.Revision != 1 || view.Dirty {
		t.Fatal("committed workspace save has no completed receipt")
	}
	var reply struct {
		Session officeSessionView `json:"session"`
	}
	if json.Unmarshal(op.result, &reply) != nil || reply.Session.Activity.Items[0].Status != "succeeded" || reply.Session.Pending {
		t.Fatal("save response returned stale queued state")
	}
}

func TestOfficeActivityTruncationIsUnicodeSafeAndCloned(t *testing.T) {
	if value, ok := officeActivityScalar(json.RawMessage(`1.0`)); !ok || value != "1" {
		t.Fatal("native JSON number normalization differed from editor input")
	}
	text, cut := officeActivityText(strings.Repeat("😀", 200), 160)
	if !cut || len([]rune(text)) != 161 || !strings.HasSuffix(text, "…") {
		t.Fatal("broken unicode truncation")
	}
	if row, col, rows, cols, ok := officeActivityRange("Z2:AA3"); !ok || row != 2 || col != 26 || rows != 2 || cols != 2 || officeActivityCell(3, 27) != "AA3" {
		t.Fatal("A1 cell mapping failed")
	}
}
