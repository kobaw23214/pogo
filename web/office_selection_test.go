package web

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"
)

func officeSelectionQueue(t *testing.T, o *officeService, d *officeDocumentSession, id string, args json.RawMessage) <-chan json.RawMessage {
	t.Helper()
	done := make(chan json.RawMessage, 1)
	go func() {
		result, err := o.command(context.Background(), d, id, "selection.read", args)
		if err != nil {
			result, _ = json.Marshal(officeErrorReply(err))
		}
		done <- result
	}()
	officeTestQueued(t, d, id)
	return done
}

func officeSelectionTestBinding(t *testing.T, o *officeService, d *officeDocumentSession) (string, json.RawMessage) {
	t.Helper()
	instance := officeTestReady(t, o, d)
	caps := officeTestCapabilities(d.editor)
	caps["selectionRead"] = true
	if r := officeTestBridge(t, o, d, "POST", "ready", map[string]any{"instanceId": instance, "capabilities": caps}, ""); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	v := d.view()
	args, _ := json.Marshal(map[string]any{"documentId": v.DocumentID, "instanceId": instance, "canvasVersion": v.CanvasVersion, "dirtyEpoch": v.DirtyEpoch})
	return instance, args
}

func TestOfficeSelectionReadBindsCurrentUnsavedCanvasWithoutMutation(t *testing.T) {
	o, d, _ := officeTestService(t)
	instance, _ := officeSelectionTestBinding(t, o, d)
	if r := officeTestBridge(t, o, d, "POST", "state", map[string]any{"instanceId": instance, "dirty": true}, ""); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	before := d.view()
	args, _ := json.Marshal(map[string]any{"documentId": before.DocumentID, "instanceId": instance, "canvasVersion": before.CanvasVersion, "dirtyEpoch": before.DirtyEpoch})
	if !officeReadOnly("selection.read") || officeMutation("selection.read") || !slices.Contains(before.SupportedOperations, "selection.read") {
		t.Fatal("selection is not a capability-checked read")
	}
	done := officeSelectionQueue(t, o, d, "selection_current_001", args)
	officeTestDispatch(t, o, d, instance, "selection_current_001")
	r := officeTestBridge(t, o, d, "POST", "results", map[string]any{"instanceId": instance, "operationId": "selection_current_001", "ok": true, "result": map[string]any{"kind": "word", "text": "当前未保存选区"}}, "")
	if r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	result := <-done
	if !bytes.Contains(result, []byte("当前未保存选区")) || !bytes.Contains(result, []byte(`"ok":true`)) {
		t.Fatal(string(result))
	}
	after := d.view()
	if after.DirtyEpoch != before.DirtyEpoch || after.PersistedEpoch != before.PersistedEpoch || after.CanvasVersion != before.CanvasVersion || after.Revision != before.Revision || !after.Dirty || after.Blocked || d.saving {
		t.Fatal("selection read saved, dirtied, or replaced the canvas")
	}
}

func TestOfficeSelectionReadRejectsUnverifiedOrMismatchedBindingBeforeQueue(t *testing.T) {
	o, d, _ := officeTestService(t)
	instance := officeTestReady(t, o, d)
	v := d.view()
	valid := map[string]any{"documentId": v.DocumentID, "instanceId": instance, "canvasVersion": v.CanvasVersion, "dirtyEpoch": v.DirtyEpoch}
	args, _ := json.Marshal(valid)
	_, err := o.command(context.Background(), d, "selection_unsupported_001", "selection.read", args)
	officeExpectError(t, err, "UNSUPPORTED_CAPABILITY")
	caps := officeTestCapabilities(d.editor)
	caps["selectionRead"] = true
	if r := officeTestBridge(t, o, d, "POST", "ready", map[string]any{"instanceId": instance, "capabilities": caps}, ""); r.Code != 200 {
		t.Fatal(r.Body.String())
	}
	for _, field := range []string{"documentId", "instanceId", "canvasVersion", "dirtyEpoch"} {
		body := map[string]any{}
		for k, v := range valid {
			body[k] = v
		}
		if field == "dirtyEpoch" {
			body[field] = 10
		} else if field == "canvasVersion" {
			body[field] = string(bytes.Repeat([]byte("b"), 64))
		} else {
			body[field] = "other_valid_id_001"
		}
		encoded, _ := json.Marshal(body)
		_, err = o.command(context.Background(), d, "selection_mismatch_"+field, "selection.read", encoded)
		officeExpectError(t, err, "SELECTION_CHANGED")
	}
	if len(d.queue) != 0 || d.reservedBytes != 0 {
		t.Fatal("mismatched capture entered the editor queue")
	}
	for _, bad := range []string{`{}`, `{"dirtyEpoch":-1}`, `{"documentId":"doc","script":"anything"}`} {
		if officeValidateOperation("word", "selection.read", json.RawMessage(bad)) == nil {
			t.Fatal("unbound arbitrary capture accepted")
		}
	}
}

func TestOfficeSelectionReadRechecksAtDispatchAndReceipt(t *testing.T) {
	for _, phase := range []string{"queued", "dispatched"} {
		t.Run(phase, func(t *testing.T) {
			o, d, _ := officeTestService(t)
			instance, args := officeSelectionTestBinding(t, o, d)
			done := officeSelectionQueue(t, o, d, "selection_stale_001", args)
			if phase == "dispatched" {
				officeTestDispatch(t, o, d, instance, "selection_stale_001")
			}
			officeTestBridge(t, o, d, "POST", "state", map[string]any{"instanceId": instance, "dirty": true}, "")
			if phase == "queued" {
				r := officeTestBridge(t, o, d, "GET", "next", nil, instance)
				if r.Code != 200 {
					t.Fatal(r.Body.String())
				}
			} else {
				r := officeTestBridge(t, o, d, "POST", "results", map[string]any{"instanceId": instance, "operationId": "selection_stale_001", "ok": true, "result": map[string]any{"kind": "word", "text": "stale text must not be delivered"}}, "")
				if r.Code != 200 {
					t.Fatal(r.Body.String())
				}
			}
			result := <-done
			if !bytes.Contains(result, []byte("SELECTION_CHANGED")) || bytes.Contains(result, []byte("stale text")) {
				t.Fatal(string(result))
			}
			if v := d.view(); v.Blocked || v.DirtyEpoch != 1 || v.Revision != 0 {
				t.Fatal("rejected read changed history or editing epoch")
			}
		})
	}
}

func TestOfficeSelectionTimeoutIsReadOnlyAndDoesNotBlock(t *testing.T) {
	o, d, _ := officeTestService(t)
	instance, args := officeSelectionTestBinding(t, o, d)
	done := officeSelectionQueue(t, o, d, "selection_timeout_001", args)
	op := officeTestQueued(t, d, "selection_timeout_001")
	officeTestDispatch(t, o, d, instance, op.id)
	d.mu.Lock()
	op.deadline = time.Now().Add(-time.Second)
	o.expireLocked(d, op)
	d.mu.Unlock()
	result := <-done
	if bytes.Contains(result, []byte("OUTCOME_UNKNOWN")) || d.view().Blocked || d.view().Dirty {
		t.Fatal(string(result))
	}
}
