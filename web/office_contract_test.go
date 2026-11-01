package web

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestOfficeWordReplaceOptionalCountAndExplicitReplacement(t *testing.T) {
	for _, args := range []string{
		`{"search":"target","replacement":"new","expectedText":"target"}`,
		`{"search":"target","replacement":"","expectedReadId":"read_reference_001"}`,
	} {
		body, err := officeWordReplaceArguments(json.RawMessage(args))
		if err != nil || body.ExpectedCount != 1 {
			t.Fatalf("omitted count must still require one target: %#v %v", body, err)
		}
	}
	for _, args := range []string{
		`{"search":"target","expectedText":"target"}`,
		`{"search":"target","replacement":null,"expectedText":"target"}`,
		`{"search":"target","replacement":"new","expectedText":"target","expectedCount":null}`,
		`{"search":"target","replacement":"new","expectedText":"target","expectedCount":0}`,
		`{"search":"target","replacement":"new","expectedText":"target","expectedCount":2}`,
		`{"search":"target","replacement":"new"}`,
	} {
		if _, err := officeWordReplaceArguments(json.RawMessage(args)); err == nil {
			t.Fatalf("unsafe or incomplete replacement accepted: %s", args)
		}
	}
}

func TestOfficeWordReplaceMatchesNativeTextBounds(t *testing.T) {
	for _, test := range []struct {
		text  string
		valid bool
	}{
		{strings.Repeat("中", 4000), true},
		{strings.Repeat("😀", 2000), true},
		{strings.Repeat("a", 4001), false},
		{strings.Repeat("😀", 2001), false},
		{"target\x01", false},
	} {
		args, _ := json.Marshal(map[string]any{"search": test.text, "replacement": "new", "expectedText": test.text})
		_, err := officeWordReplaceArguments(args)
		if (err == nil) != test.valid {
			t.Fatalf("host and native bounds disagree: units=%d err=%v", officeTextUnits(test.text), err)
		}
	}
}

func TestOfficeMultilineReplacementReportsActualUnsupportedShape(t *testing.T) {
	service, document, _ := officeTestService(t)
	_, err := service.command(context.Background(), document, "multiline_replace_001", "word.replace", json.RawMessage(`{"search":"测试步骤\n原内容","replacement":"测试步骤\n丰富后的内容","expectedReadId":"read_reference_001","expectedCount":1}`))
	officeExpectError(t, err, "UNSUPPORTED_TEXT_SHAPE")
	if !strings.Contains(err.Error(), "单段") || len(document.queue) != 0 || document.reservedBytes != 0 {
		t.Fatal("multiline failure did not preserve a bounded no-write recovery")
	}
	var reply map[string]any
	if json.Unmarshal(err.(*officeError).OfficeToolError(), &reply) != nil || reply["writeOccurred"] != false {
		t.Fatal("shape rejection lost its no-write contract")
	}
}

func TestOfficeWordReplaceNormalizesBeforeDispatchAndDeduplication(t *testing.T) {
	service, document, _ := officeTestService(t)
	officeTestReady(t, service, document)
	const id = "optional_count_replace_001"
	done := make(chan error, 1)
	go func() {
		_, err := service.command(context.Background(), document, id, "word.replace", json.RawMessage(`{"search":"target","replacement":"new","expectedText":"target"}`))
		done <- err
	}()
	op := officeTestQueued(t, document, id)
	var body officeWordReplaceArgs
	if json.Unmarshal(op.args, &body) != nil || body.ExpectedCount != 1 {
		t.Fatal("native editor did not receive the exact single-target count")
	}
	document.mu.Lock()
	service.finishLocked(document, op, map[string]any{"ok": true, "operationId": id, "result": map[string]any{}})
	document.mu.Unlock()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	_, err := service.command(context.Background(), document, id, "word.replace", json.RawMessage(`{"search":"target","replacement":"new","expectedText":"target","expectedCount":1}`))
	if err != nil {
		t.Fatalf("equivalent normalized request conflicted: %v", err)
	}
	if len(document.operations) != 1 {
		t.Fatal("normalized retry queued another mutation")
	}
}

func TestOfficeWrongEditorRejectedBeforeQueue(t *testing.T) {
	service, document, _ := officeTestService(t)
	_, err := service.command(context.Background(), document, "wrong_editor_read_001", "sheet.read", json.RawMessage(`{"sheet":"明细","range":"C2"}`))
	officeExpectError(t, err, "WRONG_EDITOR")
	if len(document.queue) != 0 || len(document.operations) != 0 || document.reservedBytes != 0 {
		t.Fatal("wrong editor command was admitted")
	}
	if !strings.Contains(err.Error(), "list") || !strings.Contains(err.Error(), "editor=cell") {
		t.Fatal("missing target recovery guidance")
	}
}

func TestOfficeSessionMetadataReportsOnlyVerifiedKindOperations(t *testing.T) {
	service, document, _ := officeTestService(t)
	if len(document.view().SupportedOperations) != 0 {
		t.Fatal("unready editor advertised supported commands")
	}
	officeTestReady(t, service, document)
	view := document.view()
	if !slices.Contains(view.SupportedOperations, "word.read") || !slices.Contains(view.SupportedOperations, "word.replace") || slices.Contains(view.SupportedOperations, "sheet.apply") {
		t.Fatalf("wrong kind operations: %#v", view.SupportedOperations)
	}
	document.mu.Lock()
	document.blocked = true
	document.mu.Unlock()
	if got := document.view().SupportedOperations; !slices.Equal(got, []string{"capabilities", "word.read"}) {
		t.Fatalf("blocked editor advertised edits: %#v", got)
	}
}

func TestOfficeLiveSheetDiscoveryRequiresVerifiedCapabilityAndDoesNotDirty(t *testing.T) {
	service, document, _ := officeTestService(t)
	document.editor = "cell"
	instance := officeTestReady(t, service, document)
	_, err := service.command(context.Background(), document, "list_sheets_old_001", "sheet.list", json.RawMessage(`{}`))
	officeExpectError(t, err, "UNSUPPORTED_CAPABILITY")
	cap := officeTestCapabilities("cell")
	cap["sheetList"] = true
	response := officeTestBridge(t, service, document, "POST", "ready", map[string]any{"instanceId": instance, "capabilities": cap}, "")
	if response.Code != 200 || !slices.Contains(document.view().SupportedOperations, "sheet.list") {
		t.Fatal("verified sheet discovery was not advertised")
	}
	const id = "list_sheets_live_001"
	done := make(chan error, 1)
	go func() {
		_, err := service.command(context.Background(), document, id, "sheet.list", json.RawMessage(`{}`))
		done <- err
	}()
	officeTestQueued(t, document, id)
	officeTestDispatch(t, service, document, instance, id)
	response = officeTestBridge(t, service, document, "POST", "results", map[string]any{"instanceId": instance, "operationId": id, "ok": true, "result": map[string]any{"kind": "cell", "sheets": []string{"明细", "尚未保存的重命名"}}}, "")
	if response.Code != 200 {
		t.Fatal("read-only names receipt was rejected")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if view := document.view(); view.Dirty || view.DirtyEpoch != 0 {
		t.Fatal("worksheet discovery created a document edit")
	}
}
