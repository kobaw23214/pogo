package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

const officeBatchFixtureID = "a08ab9b3-4a95-4e3a-8a4d-a8e0c9acda27"

type officeBatchCompletionTestLink struct {
	officeTestLink
}

func (l *officeBatchCompletionTestLink) OfficeBatchCompletion(_, _, _ string) *OfficeCompletionReceipt {
	return &OfficeCompletionReceipt{DocumentSessionID: "previous_document_123", PersistedEpoch: 7, MutationEpoch: 99}
}

func TestOfficeBatchSaveExposesOnlyTypedPersistenceForPriorCanvas(t *testing.T) {
	link := &officeBatchCompletionTestLink{officeTestLink: officeTestLink{reply: json.RawMessage(`{"ok":true,"persistedEpoch":999}`)}}
	result, err := (&Office{Link: link}).Execute(context.Background(), json.RawMessage(`{"action":"batch_prepare","path":"a.xlsx","operation_id":"prepare_001"}`))
	if err != nil || result.OfficeCompletion == nil || result.OfficeCompletion.DocumentSessionID != "previous_document_123" || result.OfficeCompletion.PersistedEpoch != 7 || result.OfficeCompletion.MutationEpoch != 0 || !result.OfficeCompletion.SaveAttempted {
		t.Fatalf("batch save confused a native mutation or tool text with a confirmed host save: %+v %v", result.OfficeCompletion, err)
	}
	data, _ := json.Marshal(result)
	if strings.Contains(string(data), "previous_document_123") || strings.Contains(string(data), "SaveAttempted") {
		t.Fatal("internal completion receipt was serialized into model/UI output")
	}
}

func TestOfficeBatchRequestsAreValidatedAndBoundBeforeHost(t *testing.T) {
	version := strings.Repeat("a", 64)
	for _, raw := range []string{
		`{"action":"batch_prepare","path":"uploads/中文结果.xlsx","operation_id":"prepare_001"}`,
		`{"action":"batch_commit","batch_id":"` + officeBatchFixtureID + `","operation_id":"commit_001"}`,
		`{"action":"batch_abort","batch_id":"` + officeBatchFixtureID + `","operation_id":"abort_001","args":{}}`,
		`{"action":"batch_status","batch_id":"` + officeBatchFixtureID + `"}`,
		`{"action":"reload","document_session_id":"session_123","expected_version":"` + version + `","operation_id":"reload_001"}`,
	} {
		t.Run(raw, func(t *testing.T) {
			link := &officeTestLink{reply: json.RawMessage(`{"ok":true}`)}
			result, err := (&Office{Link: link}).Execute(context.Background(), json.RawMessage(raw))
			if err != nil || link.calls != 1 || result.Text != `{"ok":true}` {
				t.Fatalf("valid request did not reach host exactly once: calls=%d err=%v", link.calls, err)
			}
			var expected OfficeArgs
			_ = json.Unmarshal([]byte(raw), &expected)
			if link.args.Action != expected.Action || link.args.BatchID != expected.BatchID || link.args.ExpectedVersion != expected.ExpectedVersion || link.args.OperationID != expected.OperationID || link.args.Path != expected.Path {
				t.Fatalf("transaction identity changed: %+v", link.args)
			}
		})
	}
}

func TestOfficeBatchRejectsMixedIdentityAndImplicitDiscard(t *testing.T) {
	batch := `"batch_id":"` + officeBatchFixtureID + `"`
	version := `"expected_version":"` + strings.Repeat("a", 64) + `"`
	for _, raw := range []string{
		`{"action":"list",` + batch + `}`,
		`{"action":"open","path":"a.xlsx",` + version + `}`,
		`{"action":"batch_prepare","path":"a.xlsx"}`,
		`{"action":"batch_prepare","operation_id":"prepare_001"}`,
		`{"action":"batch_prepare","path":"a.xlsx","operation_id":"prepare_001",` + batch + `}`,
		`{"action":"batch_prepare","path":"a.xlsx","operation_id":"prepare_001","document_session_id":"session_123"}`,
		`{"action":"batch_prepare","path":"a.xlsx","operation_id":"prepare_001","args":{"script":"unsafe"}}`,
		`{"action":"batch_commit",` + batch + `}`,
		`{"action":"batch_abort",` + batch + `}`,
		`{"action":"batch_commit","batch_id":"invented_id","operation_id":"commit_001"}`,
		`{"action":"batch_commit",` + batch + `,"operation_id":"commit_001","path":"other.xlsx"}`,
		`{"action":"batch_commit",` + batch + `,"operation_id":"commit_001","args":{"force":true}}`,
		`{"action":"batch_status",` + batch + `,"operation_id":"query_001"}`,
		`{"action":"batch_status",` + batch + `,"document_session_id":"session_123"}`,
		`{"action":"reload","document_session_id":"session_123",` + version + `}`,
		`{"action":"reload","document_session_id":"session_123","expected_version":"stale","operation_id":"reload_001"}`,
		`{"action":"reload","document_session_id":"session_123",` + version + `,"operation_id":"reload_001","discard":true}`,
		`{"action":"reload","document_session_id":"session_123",` + version + `,"operation_id":"reload_001","args":{"discard":true}}`,
		`{"action":"reload","document_session_id":"session_123",` + version + `,"operation_id":"reload_001",` + batch + `}`,
	} {
		t.Run(raw, func(t *testing.T) {
			link := &officeTestLink{}
			if _, err := (&Office{Link: link}).Execute(context.Background(), json.RawMessage(raw)); err == nil || link.calls != 0 || OfficeReadOnly(json.RawMessage(raw)) {
				t.Fatalf("invalid or unsafe request reached the host: calls=%d err=%v", link.calls, err)
			}
		})
	}
}

func TestOfficeBatchStatusIsReadOnlyButTransactionsAndReloadAreNot(t *testing.T) {
	if !OfficeReadOnly(json.RawMessage(`{"action":"batch_status","batch_id":"` + officeBatchFixtureID + `"}`)) {
		t.Fatal("a validated receipt query should be read-only")
	}
	for _, raw := range []string{
		`{"action":"batch_prepare","path":"a.docx","operation_id":"prepare_001"}`,
		`{"action":"batch_commit","batch_id":"` + officeBatchFixtureID + `","operation_id":"commit_001"}`,
		`{"action":"batch_abort","batch_id":"` + officeBatchFixtureID + `","operation_id":"abort_001"}`,
		`{"action":"reload","document_session_id":"session_123","operation_id":"reload_001","expected_version":"` + strings.Repeat("b", 64) + `"}`,
	} {
		if OfficeReadOnly(json.RawMessage(raw)) {
			t.Fatalf("state-changing request bypassed the normal approval boundary: %s", raw)
		}
	}
}
