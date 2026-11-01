package tools

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestBrowserDownloadsIsNativeStandaloneMetadata(t *testing.T) {
	for _, backend := range []string{BrowserBackendNative, "", "legacy"} {
		b := &Browser{Link: &capabilityBrowserLink{backend: backend}}
		props := b.InputSchema()["properties"].(map[string]any)
		children := props["actions"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
		if slices.Contains(props["action"].(map[string]any)["enum"].([]string), BrowserActionDownloads) != (backend == BrowserBackendNative) || slices.Contains(children["action"].(map[string]any)["enum"].([]string), BrowserActionDownloads) {
			t.Fatal("download metadata leaked into unavailable backend or batch schema")
		}
	}
	if !BrowserReadOnly(BrowserActionDownloads) || !BrowserMetadataAction(BrowserActionDownloads) {
		t.Fatal("download metadata was classified as page control")
	}
	for _, raw := range []string{
		`{"action":"downloads","url":"https://example.com/export"}`,
		`{"action":"downloads","target":"t1"}`,
		`{"action":"downloads","path":"../other-user/report.xlsx"}`,
		`{"action":"downloads","query":"file"}`,
		`{"action":"downloads","limit":10}`,
		`{"action":"downloads","params":{}}`,
		`{"action":"downloads","account_id":"other"}`,
		`{"action":"downloads","actions":[{"action":"click","target":"e1"}]}`,
		`{"action":"batch","actions":[{"action":"click","target":"e1"},{"action":"downloads"}]}`,
	} {
		link := &stubLink{}
		b := &Browser{Link: link}
		if err := b.ValidateArgs(json.RawMessage(raw)); err == nil {
			t.Fatalf("invalid metadata request passed pre-policy validation: %s", raw)
		}
		if _, err := b.Execute(context.Background(), json.RawMessage(raw)); err == nil || len(link.ops) != 0 {
			t.Fatalf("invalid metadata request touched transport: %s / %v", raw, err)
		}
	}
}

func TestBrowserDownloadsSeparatesWorkspaceReceiptFromPageEvidence(t *testing.T) {
	data := json.RawMessage(`{"supported":true,"downloads":[{"id":"download1","filename":"测试.xlsx","state":"saved","path":"uploads/browser/worker/download1/测试.xlsx"},{"id":"download2","filename":"pending.xlsx","state":"completed"}],"rejected_count":0,"max_file_bytes":20971520}`)
	link := &stubLink{replies: []BrowserReply{{OK: true, Data: data, Text: "unrelated page text", URL: "https://unrelated.example", Evidence: json.RawMessage(`{"frames":[]}`), Screenshot: &BrowserScreenshot{ImageRef: "should-not-appear"}}}}
	result, err := (&Browser{Link: link}).Execute(context.Background(), json.RawMessage(`{"action":"downloads"}`))
	if err != nil || len(link.ops) != 1 || string(link.ops[0]) != `{"action":"downloads"}` {
		t.Fatalf("download metadata action not forwarded once unchanged: %v %s", err, link.ops)
	}
	for _, want := range []string{"Only state:saved", "not webpage evidence", "completed are not yet", "uploads/browser/worker/download1/测试.xlsx"} {
		if !strings.Contains(result.Text, want) {
			t.Fatalf("download receipt lost its availability boundary: missing %q in %s", want, result.Text)
		}
	}
	details := result.Details.(map[string]any)
	if details["content_source"] != "pogo_downloads" || details["has_readable_content"] != false || details["url"] != "" || len(result.Images) != 0 || strings.Contains(result.Text, "unrelated page text") || strings.Contains(result.Text, "visible DOM") {
		t.Fatalf("download metadata became page evidence: %+v", result)
	}
	for _, raw := range []string{`{}`, `{"supported":true}`, `{"supported":true,"downloads":null}`, `{"supported":true,"downloads":"invalid"}`} {
		if got := formatBrowserDownloads(BrowserReply{Data: json.RawMessage(raw)}); !strings.Contains(got, "unavailable or malformed") {
			t.Fatalf("malformed receipt was accepted as empty downloads: %s", got)
		}
	}
	if got := formatBrowserDownloads(BrowserReply{Data: json.RawMessage(`{"supported":false,"downloads":[]}`)}); !strings.Contains(got, "does not provide download transfer") {
		t.Fatalf("unsupported download backend was confused with an empty history: %s", got)
	}
}

func TestFDBrowserDownloadsDoNotAcquirePageLease(t *testing.T) {
	link := &fdCapabilityLink{backend: BrowserBackendNative, recordingLink: recordingLink{reply: BrowserReply{OK: true, Data: json.RawMessage(`{"supported":true,"downloads":[]}`)}}}
	s := &Subagent{Browser: link, browserLease: newBrowserLease()}
	if err := s.browserLease.acquire(context.Background(), "page-agent"); err != nil {
		t.Fatal(err)
	}
	child, cleanup := pipePair(t, s, "download-reader")
	child.backend = BrowserBackendNative
	defer cleanup()
	defer s.browserLease.release("page-agent")
	for _, raw := range []string{`{"action":"downloads"}`, `{"action":"downloads","url":"https://example.com"}`} {
		done := make(chan error, 1)
		go func() {
			_, err := child.Execute(context.Background(), json.RawMessage(raw))
			done <- err
		}()
		select {
		case err := <-done:
			if (err != nil) != strings.Contains(raw, "url") {
				t.Fatalf("download metadata contract changed over FD: %v", err)
			}
		case <-time.After(2 * time.Second):
			s.browserLease.release("page-agent")
			<-done
			t.Fatal("download metadata read waited on an unrelated page lease")
		}
	}
	if s.browserLease.holder != "page-agent" || len(link.seen()) != 1 {
		t.Fatal("metadata stole page lease or invalid request reached executor")
	}
}

func TestBrowserDownloadsOnlySavedWithPathConfirmsWorkspaceFile(t *testing.T) {
	data := json.RawMessage(`{"supported":true,"downloads":[
		{"state":"saved","path":"uploads/browser/report.xlsx"},
		{"state":"saved"},{"state":"saved","path":"  "},
		{"state":"completed","path":"/tmp/not-imported.xlsx"},{"state":"in_progress"},
		{"state":"failed","error":"TRANSFER_FAILED"},{"state":"rejected","error":"DOWNLOAD_TOO_LARGE"},
		null,{"state":"unexpected"},"invalid entry"
	]}`)
	got := formatBrowserDownloads(BrowserReply{OK: true, Data: data})
	for _, want := range []string{"recent records=10", "workspace files confirmed=1", "in_progress/completed=2", "failed/rejected/canceled=2", "unknown/unconfirmed=5", "TRANSFER_FAILED", "DOWNLOAD_TOO_LARGE", "uploads/browser/report.xlsx"} {
		if !strings.Contains(got, want) {
			t.Errorf("download summary lost %q: %s", want, got)
		}
	}
}

func TestBrowserDownloadsPreserveStatusBoundariesForUnavailableResponses(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reply BrowserReply
		want  string
	}{
		{"empty", BrowserReply{OK: true, Data: json.RawMessage(`{"supported":true,"downloads":[]}`)}, "recent records=0"},
		{"malformed", BrowserReply{OK: true, Data: json.RawMessage(`{}`)}, "unavailable or malformed"},
		{"unsupported", BrowserReply{OK: true, Data: json.RawMessage(`{"supported":false,"downloads":[{"state":"saved","path":"uploads/previous.xlsx"}]}`)}, "workspace files confirmed=1"},
		{"failed", BrowserReply{Error: "TRANSFER_UNAVAILABLE", Data: json.RawMessage(`{"error_code":"TRANSFER_UNAVAILABLE"}`), URL: "https://unrelated.example/", Text: "unrelated page"}, "status check failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			link := &stubLink{replies: []BrowserReply{tc.reply}}
			result, err := (&Browser{Link: link}).Execute(context.Background(), json.RawMessage(`{"action":"downloads"}`))
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{tc.want, "Only state:saved with a nonempty path", "Human takeover", "same download transfer and restrictions", "cannot bypass"} {
				if !strings.Contains(result.Text, want) {
					t.Errorf("missing %q in %s", want, result.Text)
				}
			}
			if !tc.reply.OK && (!strings.Contains(result.Text, "TRANSFER_UNAVAILABLE") || !strings.Contains(result.Text, "No current download status or newly saved file was confirmed")) {
				t.Errorf("failed status request misrepresented: %s", result.Text)
			}
			if strings.Contains(result.Text, "unrelated page") || strings.Contains(result.Text, "https://unrelated.example/") {
				t.Error("status failure leaked unrelated page data")
			}
		})
	}
}

func TestBrowserDownloadsDistinguishCachedObservationFromCitationTime(t *testing.T) {
	for _, tc := range []struct {
		name, data string
		want       []string
	}{
		{"legacy", `{"supported":true,"downloads":[]}`, []string{"Host observation time is unavailable", "freshness is unconfirmed", "Refresh state is unavailable"}},
		{"initial", `{"supported":true,"downloads":[],"initialized":false,"refresh_pending":true}`, []string{"not yet been observed", "waiting for the host's first check", "Refresh is in progress"}},
		{"not-started", `{"supported":true,"downloads":[],"initialized":false,"refresh_pending":false}`, []string{"not yet been observed", "waiting for the host's first check", "No refresh is reported in progress"}},
		{"legacy-pending", `{"supported":true,"downloads":[],"refresh_pending":true}`, []string{"Host observation time is unavailable", "freshness is unconfirmed", "Refresh is in progress"}},
		{"observed", `{"supported":true,"downloads":[],"observed_at":"2026-10-14T01:02:03.123Z","refresh_pending":false}`, []string{"Host observation time: 2026-10-14T01:02:03.123Z", "Citation creation time is not the observation time", "No refresh is reported in progress"}},
		{"refreshing", `{"supported":true,"downloads":[],"observed_at":"2026-10-14T01:02:03Z","refresh_pending":true}`, []string{"Host observation time: 2026-10-14T01:02:03Z", "Refresh is in progress"}},
		{"invalid-time", `{"supported":true,"downloads":[],"observed_at":"unknown","refresh_pending":false}`, []string{"Host observation time is unavailable", "freshness is unconfirmed"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := formatBrowserDownloads(BrowserReply{OK: true, Data: json.RawMessage(tc.data)})
			for _, want := range append(tc.want, "most recently observed host records", "An empty list does not establish that a recent click produced no download") {
				if !strings.Contains(got, want) {
					t.Errorf("missing %q in %s", want, got)
				}
			}
		})
	}
}
