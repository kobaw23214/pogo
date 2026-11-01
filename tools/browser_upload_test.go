package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestBrowserUploadContract(t *testing.T) {
	b := &Browser{Link: &stubLink{replies: []BrowserReply{{OK: true, Backend: "native"}}}}
	valid := json.RawMessage(`{"action":"upload","target":"#file","files":["uploads/报告.xlsx"]}`)
	if err := b.ValidateArgs(valid); err != nil {
		t.Fatal(err)
	}
	if _, err := b.Execute(context.Background(), valid); err != nil {
		t.Fatal(err)
	}
	op, _, err := b.prepare(valid)
	if err != nil || op.UploadMode != "input" || op.Files[0] != "uploads/报告.xlsx" {
		t.Fatalf("upload changed source: %+v %v", op, err)
	}
	for _, raw := range []string{
		`{"action":"upload","target":"#file","files":[]}`,
		`{"action":"upload","files":["a.csv"]}`,
		`{"action":"upload","target":"#file","files":["a.csv","a.csv"]}`,
		`{"action":"upload","target":"#file","files":["a.csv"],"upload_mode":"drag"}`,
		`{"action":"batch","actions":[{"action":"click","target":"#ok"},{"action":"upload","target":"#file","files":["a.csv"]}]}`,
	} {
		if err := b.ValidateArgs(json.RawMessage(raw)); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	if BrowserReadOnly(BrowserActionUpload) || !strings.Contains(b.Description(), "upload") {
		t.Fatal("upload must be a documented page mutation")
	}
}
