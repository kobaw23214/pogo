package schedule

import (
	"encoding/json"
	"os"
	"testing"
)

func TestAgentRunOptionsPersistSealAndKeepLegacyEncoding(t *testing.T) {
	old := testJob("legacy-options")
	old.AgentID = "ag_options"
	before := DefinitionHash(old)
	bytes, err := json.Marshal(old)
	if err != nil {
		t.Fatal(err)
	}
	var roundtrip Job
	if err = json.Unmarshal(bytes, &roundtrip); err != nil {
		t.Fatal(err)
	}
	if roundtrip.ApprovalMode != "" || roundtrip.TurnLimitMode != "" || DefinitionHash(roundtrip) != before {
		t.Fatal("legacy options changed")
	}
	for _, change := range []func(*Job){func(j *Job) { j.TurnLimitMode = TurnLimitUnlimited }, func(j *Job) { j.ApprovalMode = "auto" }} {
		copy := old
		change(&copy)
		if DefinitionHash(copy) == before {
			t.Fatal("execution option not sealed")
		}
	}
	store := NewStore(t.TempDir())
	next := old
	next.TurnLimitMode = TurnLimitUnlimited
	next.ApprovalMode = "auto"
	if err = store.Add(next); err != nil {
		t.Fatal(err)
	}
	stored, err := NewStore(store.Dir()).Find(next.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.TurnLimitMode != TurnLimitUnlimited || stored.ApprovalMode != "auto" || stored.DefHash != DefinitionHash(stored) {
		t.Fatal("new options not persisted")
	}
	// Removing either field from a persisted definition must fail its existing seal.
	raw, err := os.ReadFile(store.jobsPath())
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) == 0 {
		t.Fatal("no persisted definition")
	}
}

func TestAgentRunOptionsValidateFiniteAndLegacyModes(t *testing.T) {
	for _, c := range []struct {
		mode, approval string
		turns          int
		valid          bool
	}{
		{"", "", 0, true}, {"legacy", "standard", 50, true}, {"legacy", "auto", 51, false}, {"", "", 51, false},
		{"unlimited", "auto", 0, true}, {"unlimited", "auto", 1, false}, {"limited", "strict", 1, true}, {"limited", "standard", 5000, true},
		{"limited", "auto", 0, false}, {"limited", "auto", -1, false}, {"other", "auto", 0, false}, {"unlimited", "full", 0, false},
	} {
		j := Job{AgentID: "ag_valid", TurnLimitMode: c.mode, ApprovalMode: c.approval, MaxTurns: c.turns}
		err := ValidateAgentRunOptions(j)
		if (err == nil) != c.valid {
			t.Fatalf("%+v validation=%v", c, err)
		}
	}
	if err := ValidateAgentRunOptions(Job{ApprovalMode: "auto"}); err == nil {
		t.Fatal("unbound legacy task received Agent permissions")
	}
}
