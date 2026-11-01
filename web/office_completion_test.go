package web

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/yosukeno/pogo/tools"
)

func TestOfficeCompletionRecordsOnlySuccessfulMutationEpoch(t *testing.T) {
	for _, succeeded := range []bool{true, false} {
		service, document, _ := officeTestService(t)
		instance := officeTestReady(t, service, document)
		link := service.link(document.conversationID, document.cwd, nil, nil).(*officeLink)
		// Unsaved human input is not an agent mutation.
		document.mu.Lock()
		document.dirtyEpoch = 10
		document.persistedEpoch = 2
		document.mu.Unlock()
		initial := link.OfficeCompletion(document.id, "missing_operation")
		if initial == nil || initial.MutationEpoch != 0 || initial.PersistedEpoch != 2 {
			t.Fatalf("human dirty state counted as agent write: %+v", initial)
		}
		id := "mutation_epoch_123"
		done := make(chan tools.Result, 1)
		go func() {
			raw, _ := json.Marshal(tools.OfficeArgs{Action: "command", DocumentSessionID: document.id, OperationID: id, Operation: "history.undo", Args: json.RawMessage(`{}`)})
			result, _ := (&tools.Office{Link: link}).Execute(context.Background(), raw)
			done <- result
		}()
		officeTestQueued(t, document, id)
		officeTestDispatch(t, service, document, instance, id)
		body := map[string]any{"instanceId": instance, "operationId": id, "ok": succeeded}
		if succeeded {
			body["result"] = map[string]any{"kind": "word", "text": "current user input preserved"}
		} else {
			body["error"] = map[string]any{"code": "PRECONDITION_FAILED", "message": "no write"}
		}
		if response := officeTestBridge(t, service, document, "POST", "results", body, ""); response.Code != 200 {
			t.Fatalf("mutation receipt rejected: %d %s", response.Code, response.Body.String())
		}
		result := <-done
		wantEpoch := uint64(0)
		if succeeded {
			wantEpoch = 11
		}
		if result.OfficeCompletion == nil || result.OfficeCompletion.MutationEpoch != wantEpoch || result.OfficeCompletion.PersistedEpoch != 2 {
			t.Fatalf("native receipt did not produce truthful completion metadata: %+v", result.OfficeCompletion)
		}
		// No new state-query endpoint/capability bypass: another conversation
		// and a revoked role cannot observe these persistence facts.
		other := service.link("other-conversation", document.cwd, nil, nil).(*officeLink)
		if other.OfficeCompletion(document.id, id) != nil {
			t.Fatal("completion metadata crossed conversation boundary")
		}
		service.revokeConversation(document.conversationID)
		if link.OfficeCompletion(document.id, id) != nil {
			t.Fatal("revoked role retained completion metadata")
		}
	}
}
