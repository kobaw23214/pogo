package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/tools"
)

func TestInformationSourceIsHostStampedVersionedData(t *testing.T) {
	store := newSourceStore("/work")
	for _, action := range []string{"list", "search"} {
		if src := store.Next(tools.InformationToolName, tools.InformationDetails{Action: action}, "meta", "metadata only", nil); src != nil {
			t.Fatal("metadata became content evidence")
		}
	}
	stamp := &tools.InformationCitation{EntryID: "inf_" + strings.Repeat("a", 32), Version: 3, SnapshotSHA256: strings.Repeat("b", 64)}
	details := tools.InformationDetails{Action: "read", Title: "业务规则", Kind: "shortcut", Information: stamp}
	src := store.Next(tools.InformationToolName, details, "read", "{\"content\":\"original saved body\"}", nil)
	if src == nil || src.Type != SourceInformation || src.Excerpt != "original saved body" || !strings.HasPrefix(src.Locator, "information://") || src.Information.Version != 3 {
		t.Fatalf("bad information source: %+v", src)
	}
	stamp.Version = 99
	if src.Information.Version != 3 {
		t.Fatal("source aliases mutable details")
	}
	raw, err := json.Marshal(src)
	if err != nil || !strings.Contains(string(raw), `"snapshot_sha256"`) || !strings.Contains(string(raw), `"entry_id"`) {
		t.Fatalf("lost persisted stamp: %s %v", raw, err)
	}
	if got := citeSummary(details, src); !strings.Contains(got, "version") {
		t.Fatal(got)
	}
}
