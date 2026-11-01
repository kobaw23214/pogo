package web

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

func TestUserInputHTTPScopesAnswersToOwnerAndPendingRevision(t *testing.T) {
	h := newMultiuserHarness(t, func(int) (llm.Response, error) {
		return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockText, Text: "done"}}}}, nil
	})
	aliceSID, bobSID := h.createAs(aliceToken), h.createAs(bobToken)
	alice := h.session(aliceSID)
	if alice.userInput == nil || h.session(bobSID).userInput == nil {
		t.Fatal("interactive sessions did not receive their own question channel")
	}
	type answerResult struct {
		answers map[string][]string
		err     error
	}
	done := make(chan answerResult, 1)
	go func() {
		answers, err := alice.userInput.Ask(context.Background(), tools.UserInputRequest{CallID: "http-question", Questions: userInputQuestionsFixture()})
		done <- answerResult{answers, err}
	}()
	request := waitUserInputRequest(t, alice.hub)
	body := fmt.Sprintf(`{"action":"user_input_answer","request_id":%q,"revision":1,"answers":{"approach":["A"],"checks":["单测"]}}`, request.RequestID)
	for name, tc := range map[string]struct {
		bearer string
		sid    string
		want   int
	}{
		"other owner cannot answer":     {bobToken, aliceSID, http.StatusNotFound},
		"another session cannot answer": {aliceToken, bobSID, http.StatusNotFound},
	} {
		t.Run(name, func(t *testing.T) {
			resp := h.doAs(tc.bearer, http.MethodPost, "/api/sessions/"+tc.sid+"/control", body)
			resp.Body.Close()
			if resp.StatusCode != tc.want {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.want)
			}
		})
	}
	select {
	case result := <-done:
		t.Fatalf("cross-session attempt resumed the tool: %+v", result)
	default:
	}
	resp := h.doAs(aliceToken, http.MethodPost, "/api/sessions/"+aliceSID+"/control", body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("owner's answer failed with %d", resp.StatusCode)
	}
	select {
	case result := <-done:
		if result.err != nil || result.answers["approach"][0] != "A" {
			t.Fatalf("wrong resumed answer: %+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("HTTP answer did not resume the waiting tool")
	}
	resp = h.doAs(aliceToken, http.MethodPost, "/api/sessions/"+aliceSID+"/control", body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("idempotent HTTP retry failed with %d", resp.StatusCode)
	}
	stale := fmt.Sprintf(`{"action":"user_input_answer","request_id":%q,"revision":0,"answers":{}}`, request.RequestID)
	resp = h.doAs(aliceToken, http.MethodPost, "/api/sessions/"+aliceSID+"/control", stale)
	resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("stale revision returned %d", resp.StatusCode)
	}
}
