package tools

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestBrowserBookmarksIsNativeStandaloneReadMetadata(t *testing.T) {
	for _, backend := range []string{BrowserBackendNative, "legacy", "", "future-executor"} {
		browser := &Browser{Link: &capabilityBrowserLink{backend: backend}, ImageMode: BrowserImageNone}
		props := browser.InputSchema()["properties"].(map[string]any)
		child := props["actions"].(map[string]any)["items"].(map[string]any)["properties"].(map[string]any)
		for _, tc := range []struct {
			fields map[string]any
			want   bool
		}{{props, backend == BrowserBackendNative}, {child, false}} {
			actions := tc.fields["action"].(map[string]any)["enum"].([]string)
			if slices.Contains(actions, BrowserActionBookmarks) != tc.want {
				t.Fatalf("wrong bookmarks availability for %q: %v", backend, actions)
			}
			for _, field := range []string{"query", "limit", "offset"} {
				if _, exists := tc.fields[field]; exists != tc.want {
					t.Fatalf("wrong %s availability for %q", field, backend)
				}
			}
		}
	}
	if !BrowserReadOnly(BrowserActionBookmarks) {
		t.Fatal("reading account bookmarks must not be classified as a page write")
	}
	text := (&Browser{Link: &stubLink{}}).Description()
	for _, want := range []string{"bookmarks", "account-scoped Pogo favorites", "not page DOM", "returned URL"} {
		if !strings.Contains(text, want) {
			t.Fatalf("model cannot discover bookmark boundary or workflow: missing %q", want)
		}
	}
}

func TestBrowserBookmarksValidatesBeforeDispatch(t *testing.T) {
	for _, raw := range []string{
		`{"action":"bookmarks","query":null}`,
		`{"action":"bookmarks","query":3}`,
		`{"action":"bookmarks","query":[]}`,
		`{"action":"bookmarks","url":"https://example.com"}`,
		`{"action":"bookmarks","target":"t1"}`,
		`{"action":"bookmarks","account_id":"another-account"}`,
		`{"action":"bookmarks","agent_id":"ag_another_agent"}`,
		`{"action":"bookmarks","operation":"delete"}`,
		`{"action":"bookmarks","limit":0}`,
		`{"action":"bookmarks","limit":51}`,
		`{"action":"bookmarks","limit":1.5}`,
		`{"action":"bookmarks","limit":null}`,
		`{"action":"bookmarks","offset":-1}`,
		`{"action":"bookmarks","offset":0.5}`,
		`{"action":"bookmarks","offset":null}`,
		`{"action":"bookmarks","query":"\nSaved"}`,
		`{"action":"bookmarks","query":"Saved\t"}`,
		`{"action":"bookmarks","query":"Saved\u0085site"}`,
		`{"action":"snapshot","query":"saved site"}`,
		`{"action":"bookmarks","query":"` + strings.Repeat("中", MaxBrowserBookmarkQueryRunes+1) + `"}`,
	} {
		link := &stubLink{}
		browser := &Browser{Link: link}
		if err := browser.ValidateArgs(json.RawMessage(raw)); err == nil {
			t.Errorf("invalid bookmark arguments accepted before policy: %s", raw)
		}
		if _, err := browser.ParseBookmarkQuery(json.RawMessage(raw)); err == nil {
			t.Errorf("invalid bookmark arguments accepted by metadata parser: %s", raw)
		}
		if _, err := browser.Execute(context.Background(), json.RawMessage(raw)); err == nil || len(link.ops) != 0 {
			t.Fatalf("invalid bookmark arguments reached executor: %s / %v", raw, err)
		}
	}
	link := &stubLink{}
	browser := &Browser{Link: link}
	raw := json.RawMessage(`{"action":"batch","actions":[{"action":"click","target":"e1"},{"action":"bookmarks"}]}`)
	if _, err := browser.Execute(context.Background(), raw); err == nil || !strings.Contains(err.Error(), "step 2") || len(link.ops) != 0 {
		t.Fatalf("bookmark metadata partly dispatched through page batch: %v / %v", err, link.ops)
	}
}

func TestBrowserBookmarksQueryNormalizationAndOriginalURLSurvive(t *testing.T) {
	for _, query := range []string{"", "  网站名称  ", strings.Repeat("中", MaxBrowserBookmarkQueryRunes)} {
		data := json.RawMessage(`{"source":"pogo_bookmarks","scope":"account","status":"ready","revision":7,"total":1,"matched":1,"bookmarks":[{"id":"bookmark-1","title":"Saved site","url":"https://Example.com/path?x=1&lang=zh#section"}]}`)
		link := &stubLink{replies: []BrowserReply{{OK: true, Data: data}}}
		browser := &Browser{Link: link, ImageMode: BrowserImageNone}
		raw := op(t, map[string]any{"action": BrowserActionBookmarks, "query": query})
		if err := browser.ValidateArgs(raw); err != nil {
			t.Fatal(err)
		}
		parsed, err := browser.ParseBookmarkQuery(raw)
		if err != nil || parsed.Query != strings.TrimSpace(query) || parsed.Limit != DefaultBrowserBookmarkLimit || parsed.Offset != 0 || len(link.ops) != 0 {
			t.Fatalf("metadata parse changed arguments or executed a read: %+v / %v / %v", parsed, err, link.ops)
		}
		result, err := browser.Execute(context.Background(), raw)
		if err != nil || len(link.ops) != 1 {
			t.Fatalf("bookmark read was not dispatched once: %v / %v", err, link.ops)
		}
		var wire browserArgs
		if err := json.Unmarshal(link.ops[0], &wire); err != nil || wire.Action != BrowserActionBookmarks || wire.Query != strings.TrimSpace(query) || wire.Limit == nil || *wire.Limit != DefaultBrowserBookmarkLimit {
			t.Fatalf("bookmark query changed in transit: %s / %v", link.ops[0], err)
		}
		if !strings.Contains(result.Text, string(data)) || !strings.Contains(result.Text, "not page DOM") {
			t.Fatalf("saved URL or provenance was lost: %s", result.Text)
		}
		for _, unwanted := range []string{"page:", "runtime:", "visible DOM evidence"} {
			if strings.Contains(result.Text, unwanted) {
				t.Fatalf("host metadata falsely framed as page observation: %s", result.Text)
			}
		}
		details := result.Details.(map[string]any)
		if details["content_source"] != "pogo_bookmarks" || details["has_readable_content"] != false || details["url"] != "" || len(result.Images) != 0 {
			t.Fatalf("bookmarks qualified as webpage evidence: %+v", result)
		}
	}
}

func TestBrowserBookmarksPaginationSurvivesWire(t *testing.T) {
	for _, limit := range []int{1, MaxBrowserBookmarkLimit} {
		link := &stubLink{}
		browser := &Browser{Link: link}
		raw := op(t, map[string]any{"action": BrowserActionBookmarks, "query": "Saved", "offset": 100, "limit": limit})
		if err := browser.ValidateArgs(raw); err != nil {
			t.Fatal(err)
		}
		if parsed, err := browser.ParseBookmarkQuery(raw); err != nil || parsed.Limit != limit || parsed.Offset != 100 || parsed.Query != "Saved" || len(link.ops) != 0 {
			t.Fatalf("metadata pagination parser changed arguments or executed a read: %+v / %v", parsed, err)
		}
		if _, err := browser.Execute(context.Background(), raw); err != nil || len(link.ops) != 1 {
			t.Fatalf("pagination did not execute once: %v / %v", err, link.ops)
		}
		var sent browserArgs
		if err := json.Unmarshal(link.ops[0], &sent); err != nil || sent.Limit == nil || *sent.Limit != limit || sent.Offset != 100 || sent.Query != "Saved" {
			t.Fatalf("pagination changed in transport: %s / %v", link.ops[0], err)
		}
	}
}

func TestBrowserBookmarkParserRejectsPageOperations(t *testing.T) {
	link := &stubLink{}
	browser := &Browser{Link: link}
	for _, raw := range []string{`{"action":"snapshot"}`, `{"action":"navigate","url":"https://example.com"}`, `{"action":"batch","actions":[{"action":"snapshot"}]}`} {
		if _, err := browser.ParseBookmarkQuery(json.RawMessage(raw)); err == nil || len(link.ops) != 0 {
			t.Fatalf("page operation was parsed or dispatched as metadata: %s / %v", raw, err)
		}
	}
}

func TestBrowserBookmarksUnavailableIsNotEmptyLibrary(t *testing.T) {
	for _, tc := range []struct {
		data json.RawMessage
		want string
	}{
		{json.RawMessage(`{"source":"pogo_bookmarks","scope":"account","status":"not_initialized","revision":0,"total":0,"matched":0,"bookmarks":[]}`), "does not mean there are no local favorites"},
		{nil, "unavailable or malformed"},
		{json.RawMessage(`{"status":"ready"}`), "unavailable or malformed"},
		{json.RawMessage(`{"source":"page","scope":"account","status":"ready"}`), "unavailable or malformed"},
		{json.RawMessage(`{"source":"pogo_bookmarks","scope":"account","status":"unknown"}`), "unavailable or malformed"},
	} {
		result, err := (&Browser{Link: &stubLink{replies: []BrowserReply{{OK: true, Data: tc.data}}}}).Execute(context.Background(), json.RawMessage(`{"action":"bookmarks"}`))
		if err != nil || !strings.Contains(result.Text, tc.want) {
			t.Fatalf("unavailable metadata was confused with empty bookmarks: %s / %v", result.Text, err)
		}
	}
	if got := browserDetails(BrowserActionBookmarks, BrowserReply{OK: true, Text: "saved site is not website content"})["has_readable_content"]; got != false {
		t.Fatal("host bookmark text must not qualify as webpage content")
	}
}

func TestBrowserAgentBookmarkFormattingKeepsTrustedScopeAndMetadataOnly(t *testing.T) {
	data := json.RawMessage(`{"source":"pogo_bookmarks","scope":"agent","scope_id":"opaque-scope","agent_id":"ag_bound","status":"ready","bookmarks":[{"id":"one","title":"Bound Agent site","url":"https://bound.example/"}]}`)
	result, err := (&Browser{Link: &stubLink{replies: []BrowserReply{{OK: true, Data: data}}}}).Execute(context.Background(), json.RawMessage(`{"action":"bookmarks"}`))
	if err != nil || !strings.Contains(result.Text, "current Agent bookmarks") || !strings.Contains(result.Text, "Bound Agent site") || len(result.Images) != 0 {
		t.Fatalf("Agent metadata malformed: %+v %v", result, err)
	}
	for _, bad := range []string{`{"source":"pogo_bookmarks","scope":"agent","status":"ready"}`, `{"source":"pogo_bookmarks","scope":"agent","agent_id":"ag_bound","status":"ready"}`} {
		if !strings.Contains(formatBrowserBookmarks(BrowserReply{OK: true, Data: json.RawMessage(bad)}), "unavailable or malformed") {
			t.Fatal("unbound Agent scope accepted")
		}
	}
}

func TestFDBrowserBookmarksDoNotTakeOrWaitForPageLease(t *testing.T) {
	data := json.RawMessage(`{"source":"pogo_bookmarks","scope":"account","status":"ready","revision":3,"total":1,"matched":1,"bookmarks":[{"id":"saved","title":"Saved site","url":"https://example.com/"}]}`)
	link := &fdCapabilityLink{backend: BrowserBackendNative, recordingLink: recordingLink{reply: BrowserReply{OK: true, Data: data}}}
	s := &Subagent{Browser: link, browserLease: newBrowserLease()}
	if err := s.browserLease.acquire(context.Background(), "page-agent"); err != nil {
		t.Fatal(err)
	}
	child, cleanup := pipePair(t, s, "bookmark-agent")
	child.backend = BrowserBackendNative
	defer cleanup()
	defer s.browserLease.release("page-agent")
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"action":"bookmarks","query":"saved","limit":20}`),
		json.RawMessage(`{"action":"bookmarks","url":"https://example.com/"}`),
	} {
		type result struct {
			reply BrowserReply
			err   error
		}
		done := make(chan result, 1)
		go func() {
			reply, err := child.Execute(context.Background(), raw)
			done <- result{reply, err}
		}()
		select {
		case got := <-done:
			valid := !strings.Contains(string(raw), `"url"`)
			if valid && (got.err != nil || !got.reply.OK || string(got.reply.Data) != string(data)) || !valid && got.err == nil {
				t.Fatalf("metadata forwarding or validation failed: %+v", got)
			}
		case <-time.After(2 * time.Second):
			// Release before cleanup so a regression cannot strand the test.
			s.browserLease.release("page-agent")
			<-done
			t.Fatal("account bookmark read or validation waited on the page lease")
		}
	}
	if s.browserLease.holder != "page-agent" || len(link.seen()) != 1 {
		t.Fatalf("metadata stole page control or malformed call dispatched: holder=%q calls=%v", s.browserLease.holder, link.seen())
	}
}

func TestFDBrowserPageActionsStillRespectPageLease(t *testing.T) {
	link := &fdCapabilityLink{backend: BrowserBackendNative, recordingLink: recordingLink{reply: BrowserReply{OK: true, URL: "https://example.com/"}}}
	s := &Subagent{Browser: link, browserLease: newBrowserLease()}
	if err := s.browserLease.acquire(context.Background(), "page-agent"); err != nil {
		t.Fatal(err)
	}
	child, cleanup := pipePair(t, s, "second-page-agent")
	defer cleanup()
	defer s.browserLease.release("page-agent")
	done := make(chan error, 1)
	go func() {
		_, err := child.Execute(context.Background(), json.RawMessage(`{"action":"snapshot"}`))
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("page action bypassed a held page lease: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	s.browserLease.release("page-agent")
	select {
	case err := <-done:
		if err != nil || len(link.seen()) != 1 {
			t.Fatalf("page action failed after lease release: %v / %v", err, link.seen())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("page action did not resume after lease release")
	}
}
