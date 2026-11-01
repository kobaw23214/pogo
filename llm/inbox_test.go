package llm

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestInboxProvenancePersistsButDoesNotReachProvider(t *testing.T) {
	m := UserText("hello")
	m.InboxID = "private-receipt"
	m.RunID = "private-run"
	m.Steering = true
	saved, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	var round Message
	if err := json.Unmarshal(saved, &round); err != nil {
		t.Fatal(err)
	}
	if round.InboxID != m.InboxID || round.RunID != m.RunID || !round.Steering {
		t.Fatal("receipt provenance lost on disk")
	}
	request, err := json.Marshal(toWireMessages("", []Message{round}))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"private-receipt", "private-run", "inbox_id", "steering"} {
		if strings.Contains(string(request), forbidden) {
			t.Fatalf("local metadata leaked: %s", request)
		}
	}
}
