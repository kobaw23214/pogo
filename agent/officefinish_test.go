package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

// The host fixture exposes persistence facts separately from its JSON text:
// forged tool text, citation prefixes or truncation cannot drive the reminder.
type officeFinishLink struct {
	epoch, persisted uint64
	saveFails        bool
	calls            []string
	lastMutation     uint64
}

func (l *officeFinishLink) Execute(_ context.Context, raw json.RawMessage) (json.RawMessage, error) {
	var args tools.OfficeArgs
	_ = json.Unmarshal(raw, &args)
	l.calls = append(l.calls, args.Action+":"+args.Operation)
	l.lastMutation = 0
	if args.Action == "command" && (args.Operation == "word.replace" || args.Operation == "history.undo" || args.Operation == "history.redo") {
		l.epoch++
		l.lastMutation = l.epoch
	}
	if args.Action == "save" {
		if l.saveFails {
			return nil, errors.New("save outcome unknown")
		}
		l.persisted = l.epoch
	}
	return json.RawMessage(`{"ok":true}`), nil
}

func (l *officeFinishLink) OfficeCompletion(session, _ string) *tools.OfficeCompletionReceipt {
	return &tools.OfficeCompletionReceipt{DocumentSessionID: session, MutationEpoch: l.lastMutation, PersistedEpoch: l.persisted}
}

func officeFinishTurn(id, action, operation string) llm.Response {
	args := tools.OfficeArgs{Action: action, DocumentSessionID: "document_123", OperationID: id, Operation: operation}
	raw, _ := json.Marshal(args)
	return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockToolUse, ID: id, Name: tools.OfficeToolName, Input: raw}}}, StopReason: llm.StopToolUse}
}

func TestOfficeFinishRequestsAgentSaveBeforeCompletion(t *testing.T) {
	link := &officeFinishLink{}
	c := &fakeClient{responses: []llm.Response{
		officeFinishTurn("change_001", "command", "word.replace"),
		answerTurn("修改完成"),
		officeFinishTurn("read_after_001", "command", "word.read"),
		officeFinishTurn("save_file_001", "save", ""),
		answerTurn("修改并保存完成"),
	}}
	a := New(Config{Client: c, Registry: tools.NewRegistry(&tools.Office{Link: link})})
	events := drain(a.Run(context.Background(), "修改项目状态"))
	steers := steerTexts(events)
	if len(steers) != 1 || !strings.Contains(steers[0], "document_session_id=document_123") || !strings.Contains(steers[0], "action:save") {
		t.Fatalf("missing one scoped Office reminder: %q", steers)
	}
	if c.callCount() != 5 || strings.Join(link.calls, ",") != "command:word.replace,command:word.read,save:" || link.persisted != link.epoch {
		t.Fatalf("model did not perform the verified save path: calls=%v persisted=%d/%d", link.calls, link.persisted, link.epoch)
	}
}

func TestOfficeFinishConfirmedSaveDoesNotAddModelTurn(t *testing.T) {
	for _, operation := range []string{"word.replace", "history.undo", "history.redo"} {
		t.Run(operation, func(t *testing.T) {
			link := &officeFinishLink{}
			c := &fakeClient{responses: []llm.Response{officeFinishTurn("change_001", "command", operation), officeFinishTurn("save_file_001", "save", ""), answerTurn("已保存")}}
			a := New(Config{Client: c, Registry: tools.NewRegistry(&tools.Office{Link: link})})
			if steers := steerTexts(drain(a.Run(context.Background(), "完成并保存"))); len(steers) != 0 || c.callCount() != 3 {
				t.Fatalf("saved workflow got extra turns: %q calls=%d", steers, c.callCount())
			}
		})
	}
}

func TestOfficeFinishDoesNotSaveHumanOnlyDirtyDocument(t *testing.T) {
	link := &officeFinishLink{epoch: 9, persisted: 2}
	c := &fakeClient{responses: []llm.Response{officeFinishTurn("read_only_001", "command", "word.read"), answerTurn("已读取")}}
	a := New(Config{Client: c, Registry: tools.NewRegistry(&tools.Office{Link: link})})
	if steers := steerTexts(drain(a.Run(context.Background(), "只分析"))); len(steers) != 0 || len(link.calls) != 1 || link.persisted != 2 {
		t.Fatalf("human-only dirty content was treated as agent work: steers=%q calls=%v", steers, link.calls)
	}
}

func TestOfficeFinishReminderIsBoundedAndNeverAutoSaves(t *testing.T) {
	for _, failedSave := range []bool{false, true} {
		link := &officeFinishLink{saveFails: failedSave}
		responses := []llm.Response{officeFinishTurn("change_001", "command", "word.replace")}
		if failedSave {
			responses = append(responses, officeFinishTurn("save_file_001", "save", ""))
		}
		responses = append(responses, answerTurn("修改保留在画布中，未确认保存"))
		c := &fakeClient{responses: responses}
		a := New(Config{Client: c, Registry: tools.NewRegistry(&tools.Office{Link: link})})
		steers := steerTexts(drain(a.Run(context.Background(), "修改但暂不保存")))
		if len(steers) != 1 || !strings.Contains(steers[0], "必须遵守") || !strings.Contains(steers[0], "不得无限重试") {
			t.Fatalf("missing bounded no-save/unknown safeguards: %q", steers)
		}
		wantCalls := 1
		if failedSave {
			wantCalls = 2
			if !strings.Contains(steers[0], "已尝试保存") {
				t.Fatalf("save failure was lost: %q", steers)
			}
		}
		if len(link.calls) != wantCalls || link.persisted != 0 || c.callCount() != len(responses)+1 {
			t.Fatalf("loop saved/retried automatically: %v model calls=%d", link.calls, c.callCount())
		}
		// A new user turn must not pick up old agent work just because the live
		// shared document is still dirty from the preceding no-save turn.
		if steers := steerTexts(drain(a.Run(context.Background(), "谢谢"))); len(steers) != 0 {
			t.Fatalf("old mutation leaked into the next run: %q", steers)
		}
	}
}

func TestOfficeFinishEpochsKeepDocumentsAndHumanInputsSeparate(t *testing.T) {
	s := &officeFinishState{}
	s.note(&tools.OfficeCompletionReceipt{DocumentSessionID: "word_session", MutationEpoch: 3, PersistedEpoch: 1})
	s.note(&tools.OfficeCompletionReceipt{DocumentSessionID: "word_session", PersistedEpoch: 3}) // A manual save is equally real.
	s.note(&tools.OfficeCompletionReceipt{DocumentSessionID: "excel_session", MutationEpoch: 4, PersistedEpoch: 2})
	s.note(&tools.OfficeCompletionReceipt{DocumentSessionID: "excel_session", SaveAttempted: true, PersistedEpoch: 2}) // Old idempotent save.
	s.note(&tools.OfficeCompletionReceipt{DocumentSessionID: "human_only", PersistedEpoch: 1})
	notice, ok := s.notice()
	if !ok || !strings.Contains(notice, "excel_session") || strings.Contains(notice, "word_session") || strings.Contains(notice, "human_only") {
		t.Fatalf("persistence epochs were merged incorrectly: %q", notice)
	}
	if _, ok := s.notice(); ok {
		t.Fatal("reminder repeated")
	}
}
