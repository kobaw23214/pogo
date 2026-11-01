package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

type officeTestLink struct {
	calls int
	args  OfficeArgs
	reply json.RawMessage
	err   error
}

type officeCompletionTestLink struct {
	officeTestLink
}

func (l *officeCompletionTestLink) OfficeCompletion(session, _ string) *OfficeCompletionReceipt {
	return &OfficeCompletionReceipt{DocumentSessionID: session, MutationEpoch: 4, PersistedEpoch: 2}
}

func TestOfficeCompletionCannotAttributeOldMutationToRejectedOrReadRequest(t *testing.T) {
	for _, request := range []struct {
		name, raw string
		err       error
	}{
		{"conflict", `{"action":"command","document_session_id":"session_123","operation":"word.replace","operation_id":"old_write_123","args":{}}`, errors.New("operation ID conflict")},
		{"read", `{"action":"command","document_session_id":"session_123","operation":"word.read","operation_id":"old_write_123","args":{}}`, nil},
		{"save", `{"action":"save","document_session_id":"session_123","operation_id":"old_write_123"}`, errors.New("operation ID conflict")},
	} {
		t.Run(request.name, func(t *testing.T) {
			link := &officeCompletionTestLink{officeTestLink: officeTestLink{err: request.err}}
			result, _ := (&Office{Link: link}).Execute(context.Background(), json.RawMessage(request.raw))
			if result.OfficeCompletion == nil || result.OfficeCompletion.MutationEpoch != 0 || result.OfficeCompletion.PersistedEpoch != 2 {
				t.Fatalf("rejected/read call acquired old mutation: %+v", result.OfficeCompletion)
			}
			if result.OfficeCompletion.SaveAttempted != (request.name == "save") {
				t.Fatal("save attempt was not retained on error")
			}
			encoded, err := json.Marshal(result)
			if err != nil || strings.Contains(string(encoded), "MutationEpoch") || strings.Contains(string(encoded), "DocumentSessionID") {
				t.Fatalf("internal completion receipt entered serialized tool result: %s %v", encoded, err)
			}
		})
	}
}

func (l *officeTestLink) Execute(_ context.Context, raw json.RawMessage) (json.RawMessage, error) {
	l.calls++
	_ = json.Unmarshal(raw, &l.args)
	if l.reply == nil {
		l.reply = json.RawMessage(`{"session":{"id":"session_123","ready":true}}`)
	}
	return l.reply, l.err
}

func TestOfficeRefusesInvalidRequestsBeforeHost(t *testing.T) {
	for _, raw := range []string{
		`{"action":"command","document_session_id":"session_123","operation":"word.replace","args":{}}`,
		`{"action":"save","document_session_id":"session_123"}`,
		`{"action":"list","path":"x"}`,
		`{"action":"open","path":"x.docx","token":"secret"}`,
		`{"action":"command","document_session_id":"session_123","operation":"eval","operation_id":"test_123","args":{"script":"x"}}`,
		`{"action":"command","document_session_id":"session_123","operation":"word.read","args":null}`,
		`{"action":"command","document_session_id":"session_123","operation":"word.read","args":[]}`,
		`{"action":"list"} {"action":"open","path":"x.docx"}`,
		`{"action":"save","document_session_id":"other","operation_id":"test_123"}`,
		`{"action":"save","document_session_id":"session_123","operation_id":"save_123","args":{"unexpected":null}}`,
		`{"action":"save","document_session_id":"session_123","operation_id":"save_123","args":[]}`,
		`{"action":"save","document_session_id":"session_123","operation_id":"save_123","args":"{}"}`,
		`{"action":"save","document_session_id":"session_123","operation_id":"save_123","args":false}`,
		`{"action":"save","document_session_id":"session_123","operation_id":"save_123","args":1}`,
		`{"action":"save","document_session_id":"session_123","operation_id":"save_123","args":{}} trailing`,
		`{"action":"save","document_session_id":"session_123","operation_id":"save_123","operation":"word.read","args":{}}`,
	} {
		t.Run(raw, func(t *testing.T) {
			link := &officeTestLink{}
			if _, err := (&Office{Link: link}).Execute(context.Background(), json.RawMessage(raw)); err == nil {
				t.Fatal("invalid request was accepted")
			}
			if link.calls != 0 {
				t.Fatal("invalid request reached host")
			}
		})
	}
}

func TestOfficeNormalizesEmptyNonCommandArgs(t *testing.T) {
	for _, args := range []string{"", `,"args":{}`, `,"args": { }`, `,"args":null`} {
		for _, request := range []string{
			`{"action":"list"`,
			`{"action":"open","path":"a.docx"`,
			`{"action":"save","document_session_id":"session_123","operation_id":"save_current_123"`,
			`{"action":"close","document_session_id":"session_123"`,
		} {
			raw := request + args + `}`
			t.Run(raw, func(t *testing.T) {
				link := &officeTestLink{}
				if _, err := (&Office{Link: link}).Execute(context.Background(), json.RawMessage(raw)); err != nil {
					t.Fatal(err)
				}
				if link.calls != 1 || len(link.args.Args) != 0 {
					t.Fatalf("empty args were not normalized before forwarding: %#v", link.args)
				}
				if link.args.Action == "save" && link.args.OperationID != "save_current_123" {
					t.Fatalf("save operation identity changed: %#v", link.args)
				}
			})
		}
	}
}

func TestOfficePreservesStableMutationID(t *testing.T) {
	link := &officeTestLink{}
	tool := &Office{Link: link}
	raw := json.RawMessage(`{"action":"command","document_session_id":"session_123","operation_id":"write_12345","operation":"sheet.apply","args":{"sheet":"销售","range":"C2","expectedInputs":[["3"]],"inputs":[[5]]}}`)
	result, err := tool.Execute(context.Background(), raw)
	if err != nil || !json.Valid([]byte(result.Text)) || link.args.OperationID != "write_12345" {
		t.Fatalf("mutation identity/result: %s %#v %v", result.Text, link.args, err)
	}
	if tool.ExecutionMode() != Sequential || tool.IsReadOnly() {
		t.Fatal("live editor commands must stay sequential and unavailable to read-only registries")
	}
	r := New(Options{Synth: []Tool{tool}, ReadOnly: true})
	if _, exists := r.Get(OfficeToolName); exists {
		t.Fatal("read-only registry acquired a mutating Office capability")
	}
}

func TestOfficeForwardsWordReadReferenceWithoutRetypingDocument(t *testing.T) {
	link := &officeTestLink{}
	tool := &Office{Link: link}
	raw := json.RawMessage(`{"action":"command","document_session_id":"session_123","operation_id":"replace_12345","operation":"word.replace","args":{"search":"付款期限为 15 天","replacement":"付款期限为 30 天","expectedReadId":"read_current_123","expectedCount":1}}`)
	if _, err := tool.Execute(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	var args map[string]json.RawMessage
	if err := json.Unmarshal(link.args.Args, &args); err != nil {
		t.Fatal(err)
	}
	if link.calls != 1 || link.args.OperationID != "replace_12345" || string(args["expectedReadId"]) != `"read_current_123"` {
		t.Fatalf("read-reference request changed in transit: %#v", link.args)
	}
	if _, exists := args["expectedText"]; exists {
		t.Fatal("tool invented a full-document precondition instead of forwarding the read reference")
	}
	// The host owns snapshot lookup and the exact current-document guard. The
	// model-facing schema must allow the opaque reference and legacy text input.
	props := tool.InputSchema()["properties"].(map[string]any)
	argProps := props["args"].(map[string]any)["properties"].(map[string]any)
	for _, field := range []string{"expectedReadId", "expectedText"} {
		if argProps[field].(map[string]any)["type"] != "string" {
			t.Fatalf("missing supported Word precondition field: %s", field)
		}
	}
}

func TestOfficeReadOnlyIsValidatedPerOperation(t *testing.T) {
	for _, raw := range []string{`{"action":"list"}`, `{"action":"command","document_session_id":"session_123","operation":"word.read"}`, `{"action":"command","document_session_id":"session_123","operation":"sheet.read","args":{"sheet":"销售","range":"C2"}}`} {
		if !OfficeReadOnly(json.RawMessage(raw)) {
			t.Fatalf("expected read: %s", raw)
		}
	}
	for _, raw := range []string{`{"action":"open","path":"a.docx"}`, `{"action":"save","document_session_id":"session_123","operation_id":"save_123"}`, `{"action":"command","document_session_id":"session_123","operation":"history.redo","operation_id":"redo_123"}`, `{"action":"list","args":{"script":"x"}}`} {
		if OfficeReadOnly(json.RawMessage(raw)) {
			t.Fatalf("mutation/invalid request classified as read: %s", raw)
		}
	}
}

func TestOfficeCancellationAndUnknownResponse(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	link := &officeTestLink{}
	if _, err := (&Office{Link: link}).Execute(ctx, json.RawMessage(`{"action":"list"}`)); !errors.Is(err, context.Canceled) || link.calls != 0 {
		t.Fatalf("canceled operation reached host: %v", err)
	}
	for _, reply := range []json.RawMessage{json.RawMessage(`not-json`), json.RawMessage(`"` + strings.Repeat("x", 1<<20) + `"`)} {
		if _, err := (&Office{Link: &officeTestLink{reply: reply}}).Execute(context.Background(), json.RawMessage(`{"action":"list"}`)); err == nil {
			t.Fatal("unverifiable host output accepted")
		}
	}
}

func TestOfficeUnsupportedOperationReturnsActionableContract(t *testing.T) {
	link := &officeTestLink{}
	_, err := (&Office{Link: link}).Execute(context.Background(), json.RawMessage(`{"action":"command","document_session_id":"session_123","operation":"sheet.write","operation_id":"change_123","args":{}}`))
	if err == nil || link.calls != 0 {
		t.Fatal("unsupported operation reached the editor")
	}
	var reply struct {
		OK            bool                           `json:"ok"`
		WriteOccurred bool                           `json:"writeOccurred"`
		Error         struct{ Code, Message string } `json:"error"`
	}
	if json.Unmarshal([]byte(err.Error()), &reply) != nil || reply.OK || reply.WriteOccurred || reply.Error.Code != "UNSUPPORTED_OPERATION" || !strings.Contains(reply.Error.Message, "sheet.apply") || !strings.Contains(reply.Error.Message, "args") {
		t.Fatalf("missing bounded recovery contract: %v", err)
	}
}

type officeTestStructuredError struct{}

func (officeTestStructuredError) Error() string { return "plain host error" }
func (officeTestStructuredError) OfficeToolError() json.RawMessage {
	return json.RawMessage(`{"ok":false,"error":{"code":"WRONG_EDITOR","message":"先 list 选择 Excel"},"writeOccurred":false}`)
}

func TestOfficePreservesStructuredHostRejection(t *testing.T) {
	link := &officeTestLink{err: officeTestStructuredError{}}
	_, err := (&Office{Link: link}).Execute(context.Background(), json.RawMessage(`{"action":"command","document_session_id":"session_123","operation":"sheet.read","args":{"sheet":"明细","range":"C2"}}`))
	if err == nil || !json.Valid([]byte(err.Error())) || !strings.Contains(err.Error(), "WRONG_EDITOR") {
		t.Fatalf("host rejection lost its recovery code: %v", err)
	}
}

func TestOfficeNormalizesOnlyUnambiguousNestedOperation(t *testing.T) {
	for _, test := range []struct {
		args      string
		operation string
		readOnly  bool
	}{
		{`{"action":"command","document_session_id":"session_123","args":{"operation":"sheet.read","sheet":"明细","range":"A1:E6"}}`, "sheet.read", true},
		{`{"action":"command","document_session_id":"session_123","args":{"operation":"sheet.list"}}`, "sheet.list", true},
		{`{"action":"command","document_session_id":"session_123","operation_id":"mutate_001","args":{"operation":"sheet.apply","sheet":"明细","range":"C2","expectedInputs":[[3]],"inputs":[[5]]}}`, "sheet.apply", false},
	} {
		link := &officeTestLink{}
		_, err := (&Office{Link: link}).Execute(context.Background(), json.RawMessage(test.args))
		if err != nil || link.args.Operation != test.operation || OfficeReadOnly(json.RawMessage(test.args)) != test.readOnly {
			t.Fatalf("normalization/classification: %#v %v", link.args, err)
		}
		var args map[string]json.RawMessage
		_ = json.Unmarshal(link.args.Args, &args)
		if _, exists := args["operation"]; exists {
			t.Fatal("nested operation leaked to editor arguments")
		}
	}
	for _, raw := range []string{
		`{"action":"command","document_session_id":"session_123","operation":"sheet.read","args":{"operation":"sheet.apply"}}`,
		`{"action":"command","document_session_id":"session_123","operation":"sheet.read","args":{"operation":"sheet.read"}}`,
		`{"action":"command","document_session_id":"session_123","args":{"operation":"eval"}}`,
		`{"action":"command","document_session_id":"session_123","args":{"operation":"sheet.apply","sheet":"明细","range":"C2","expectedInputs":[[3]],"inputs":[[5]]}}`,
		`{"action":"command","document_session_id":"session_123","args":{"operation":null}}`,
		`{"action":"command","document_session_id":"session_123","args":{"operation":"sheet.read","operation":"sheet.apply"}}`,
		`{"action":"command","document_session_id":"session_123","operation":"sheet.read","operation":"sheet.apply","operation_id":"mutate_001","args":{}}`,
	} {
		link := &officeTestLink{}
		if _, err := (&Office{Link: link}).Execute(context.Background(), json.RawMessage(raw)); err == nil || link.calls != 0 {
			t.Fatalf("ambiguous/unsupported/unsafe request dispatched: %s", raw)
		}
	}
}
