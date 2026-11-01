package web

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const downloadTestID = "d56a1f30-330d-4c91-a7da-af049b71fa06"

func downloadTestBrowser(t *testing.T, handler http.HandlerFunc, limit int64) (*nativeBrowser, *Session) {
	t.Helper()
	b := newNativeBrowser(nativeTestConfig(t, handler))
	s := &Session{ID: "download-conversation", cwd: t.TempDir(), hub: NewHub()}
	b.bindDownloads(s, func() (map[string]bool, int64) { return map[string]bool{".csv": true, ".xlsx": true}, limit })
	b.created = true
	t.Cleanup(b.downloads.stop)
	return b, s
}

func TestNativeBrowserDownloadsImportAndRetainEditedFile(t *testing.T) {
	var contentReads, acknowledgements atomic.Int32
	b, s := downloadTestBrowser(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete:
			acknowledgements.Add(1)
			http.Error(w, "temporary ACK failure", 503)
		case strings.HasSuffix(r.URL.Path, "/content"):
			contentReads.Add(1)
			io.WriteString(w, "item,amount\nA,12\n")
		case strings.HasSuffix(r.URL.Path, "/downloads"):
			writeJSON(w, 200, map[string]any{"downloads": []nativeBrowserDownload{{ID: downloadTestID, Filename: "../report.csv", State: "completed", CreatedAt: time.Now().UTC().Format(time.RFC3339)}}})
		default:
			t.Errorf("unexpected worker request %s %s", r.Method, r.URL.Path)
		}
	}, 1024)
	_, events, cancel := s.hub.Subscribe(0)
	defer cancel()
	first, err := b.Downloads(context.Background())
	if err != nil || len(first.Downloads) != 1 || first.Downloads[0].State != "saved" {
		t.Fatalf("import: %+v %v", first, err)
	}
	item := first.Downloads[0]
	namespace := sha256.Sum256([]byte("pogo-browser-download-v1:" + b.id))
	wanted := "uploads/browser/" + hex.EncodeToString(namespace[:12]) + "/" + downloadTestID + "/report.csv"
	if item.Path != wanted || item.Filename != "report.csv" {
		t.Fatalf("identity: %+v", item)
	}
	select {
	case event := <-events:
		if event.Type != EvBrowserDownloads || event.BrowserDownloads.Downloads[0].State != "completed" {
			t.Fatalf("missing importing state: %+v", event)
		}
	default:
		t.Fatal("missing importing update")
	}
	select {
	case event := <-events:
		if event.Type != EvAttachmentUploaded || event.Attachment.Path != wanted {
			t.Fatalf("wrong SSE %+v", event)
		}
	default:
		t.Fatal("missing attachment SSE")
	}
	select {
	case event := <-events:
		if event.Type != EvBrowserDownloads || event.BrowserDownloads == nil || event.BrowserDownloads.Downloads[0].State != "saved" {
			t.Fatalf("missing browser download update: %+v", event)
		}
	default:
		t.Fatal("missing download progress SSE")
	}
	if refs := sessionUploadLedger(s.cwd, s.ID); len(refs) != 1 || refs[0] != wanted {
		t.Fatalf("ledger: %v", refs)
	}
	if err := os.WriteFile(filepath.Join(s.cwd, filepath.FromSlash(item.Path)), []byte("manual and agent edits preserved"), 0600); err != nil {
		t.Fatal(err)
	}
	second, err := b.Downloads(context.Background())
	if err != nil || second.Downloads[0].State != "saved" {
		t.Fatalf("retry: %+v %v", second, err)
	}
	bytes, _ := os.ReadFile(filepath.Join(s.cwd, filepath.FromSlash(item.Path)))
	if string(bytes) != "manual and agent edits preserved" || contentReads.Load() != 1 || acknowledgements.Load() != 2 {
		t.Fatalf("ACK retry overwrote/reimported file: %q reads=%d acks=%d", bytes, contentReads.Load(), acknowledgements.Load())
	}
	select {
	case event := <-events:
		if event.Type != EvBrowserDownloads || event.BrowserDownloads.Downloads[0].State != "saved" || event.BrowserDownloads.ObservedAt == "" {
			t.Fatalf("duplicate attachment or invalid freshness event: %+v", event)
		}
	default:
	}
}

func TestNativeBrowserDownloadsAttachmentLimitsAndSymlinks(t *testing.T) {
	for _, test := range []struct {
		name, filename, body string
		limit                int64
		symlink              bool
		code                 string
	}{
		{"type", "report.exe", "binary", 1024, false, "DOWNLOAD_TYPE_NOT_ALLOWED"},
		{"size", "report.csv", strings.Repeat("a", 33), 32, false, "DOWNLOAD_TOO_LARGE"},
		{"empty", "report.csv", "", 1024, false, "DOWNLOAD_EMPTY"},
		{"symlink", "report.csv", "a,b\n1,2", 1024, true, "IMPORT_FAILED"},
	} {
		t.Run(test.name, func(t *testing.T) {
			b, s := downloadTestBrowser(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodDelete {
					writeJSON(w, 200, map[string]bool{"ok": true})
					return
				}
				if strings.HasSuffix(r.URL.Path, "/content") {
					io.WriteString(w, test.body)
					return
				}
				writeJSON(w, 200, map[string]any{"downloads": []nativeBrowserDownload{{ID: downloadTestID, Filename: test.filename, State: "completed"}}})
			}, test.limit)
			outside := t.TempDir()
			if test.symlink {
				if err := os.Symlink(outside, filepath.Join(s.cwd, "uploads")); err != nil {
					t.Fatal(err)
				}
			}
			_, events, cancel := s.hub.Subscribe(0)
			defer cancel()
			result, err := b.Downloads(context.Background())
			if err != nil || len(result.Downloads) != 1 || result.Downloads[0].State != "failed" || result.Downloads[0].ErrorCode != test.code || result.Downloads[0].Path != "" {
				t.Fatalf("rejection: %+v %v", result, err)
			}
			if entries, _ := os.ReadDir(outside); len(entries) != 0 {
				t.Fatal("wrote through workspace symlink")
			}
			// The intermediate importing update may precede the terminal rejection.
			select {
			case event := <-events:
				if event.Type == EvBrowserDownloads && event.BrowserDownloads.Downloads[0].State == "completed" {
					event = <-events
				}
				if event.Type != EvBrowserDownloads || event.BrowserDownloads.Downloads[0].State != "failed" {
					t.Fatalf("unimported file published: %+v", event)
				}
			default:
				t.Fatal("missing explicit failed-download update")
			}
		})
	}
}

func TestNativeBrowserDownloadsCloseCancelsImport(t *testing.T) {
	started := make(chan struct{})
	b, s := downloadTestBrowser(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/content") {
			close(started)
			<-r.Context().Done()
			return
		}
		writeJSON(w, 200, map[string]any{"downloads": []nativeBrowserDownload{{ID: downloadTestID, Filename: "report.csv", State: "completed"}}})
	}, 1024)
	done := make(chan error, 1)
	go func() { _, err := b.Downloads(context.Background()); done <- err }()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("download did not start")
	}
	closed := make(chan struct{})
	go func() { b.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("Close did not cancel/join active download")
	}
	if err := <-done; err == nil {
		t.Fatal("cancelled content was treated as success")
	}
	if _, err := os.Stat(filepath.Join(s.cwd, "uploads")); !os.IsNotExist(err) {
		t.Fatalf("cancelled import created files: %v", err)
	}
}

func TestNativeBrowserDownloadsNeverCreateContextAndAreNotBrowserActions(t *testing.T) {
	var calls atomic.Int32
	b, _ := downloadTestBrowser(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); http.NotFound(w, r) }, 1024)
	b.created = false
	result, err := b.Downloads(context.Background())
	if err != nil || len(result.Downloads) != 0 || calls.Load() != 0 {
		t.Fatalf("list created context: %+v %v calls=%d", result, err, calls.Load())
	}
	b.created = true
	// Hold the browser operation lock; downloads must not wait for navigation.
	<-b.op
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	reply, err := b.Execute(ctx, json.RawMessage(`{"action":"downloads"}`))
	b.release()
	if err != nil || !reply.OK || reply.Backend != "native" || reply.URL != "" || reply.Evidence != nil {
		t.Fatalf("metadata action: %+v %v", reply, err)
	}
	var list nativeBrowserDownloadList
	if json.Unmarshal(reply.Data, &list) != nil || list.Supported {
		t.Fatalf("old worker support: %s", reply.Data)
	}
}

func TestNativeBrowserDownloadsWorkerScopeAndUntrustedMetadata(t *testing.T) {
	var workerPaths []string
	b, _ := downloadTestBrowser(t, func(w http.ResponseWriter, r *http.Request) {
		workerPaths = append(workerPaths, r.URL.Path)
		if strings.HasSuffix(r.URL.Path, "/content") {
			io.WriteString(w, "a,b\n1,2")
			return
		}
		if r.Method == http.MethodDelete {
			writeJSON(w, 200, map[string]bool{"ok": true})
			return
		}
		writeJSON(w, 200, map[string]any{"rejected_count": 2, "last_rejection": "https://site/?token=secret", "downloads": []nativeBrowserDownload{
			{ID: "../../another/content", Filename: "attack.csv", State: "completed"},
			{ID: downloadTestID, Filename: "report.csv", State: "completed", Path: "../../other/file", ContextRef: "https://secret", TabID: "../../t2", Error: "cookie=private"},
		}})
	}, 1024)
	result, err := b.Downloads(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Downloads) != 1 || result.Downloads[0].ContextRef != "" || result.Downloads[0].TabID != "" || strings.Contains(result.LastRejection, "secret") {
		t.Fatalf("unsanitized metadata: %+v", result)
	}
	for _, p := range workerPaths {
		if !strings.HasPrefix(p, "/sessions/"+b.id+"/") {
			t.Fatalf("cross-context worker request: %s", p)
		}
	}
	state := map[string]json.RawMessage{}
	b.addDownloadState(state)
	if len(state["downloads"]) == 0 || string(state["downloads_rejected_count"]) != "2" {
		t.Fatalf("state lacks import metadata: %v", state)
	}
}

func TestNativeBrowserDownloadsRepairLedgerWithoutOverwriting(t *testing.T) {
	var reads atomic.Int32
	b, s := downloadTestBrowser(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			writeJSON(w, 200, map[string]bool{"ok": true})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/content") {
			reads.Add(1)
			io.WriteString(w, "a,b\n1,2")
			return
		}
		writeJSON(w, 200, map[string]any{"downloads": []nativeBrowserDownload{{ID: downloadTestID, Filename: "report.csv", State: "completed"}}})
	}, 1024)
	if err := os.MkdirAll(filepath.Join(s.cwd, "uploads"), 0755); err != nil {
		t.Fatal(err)
	}
	blocker := filepath.Join(s.cwd, "uploads", attachCacheDir)
	if err := os.WriteFile(blocker, []byte("ledger unavailable"), 0600); err != nil {
		t.Fatal(err)
	}
	first, err := b.Downloads(context.Background())
	if err != nil || first.Downloads[0].ErrorCode != "IMPORT_FAILED" {
		t.Fatalf("expected ledger failure: %+v %v", first, err)
	}
	landed := b.downloads.landed[downloadTestID]
	if landed.Path == "" {
		t.Fatal("lost pending attachment registration")
	}
	filename := filepath.Join(s.cwd, filepath.FromSlash(landed.Path))
	if err := os.WriteFile(filename, []byte("manual edit after download"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	second, err := b.Downloads(context.Background())
	if err != nil || second.Downloads[0].State != "saved" || reads.Load() != 1 {
		t.Fatalf("repair: %+v %v reads=%d", second, err, reads.Load())
	}
	data, _ := os.ReadFile(filename)
	if string(data) != "manual edit after download" || len(sessionUploadLedger(s.cwd, s.ID)) != 1 {
		t.Fatalf("repair overwrote bytes or failed ledger: %q", data)
	}
}

func TestNativeBrowserDownloadsConcurrentStartStop(t *testing.T) {
	b, _ := downloadTestBrowser(t, func(w http.ResponseWriter, r *http.Request) { t.Error("idle lifecycle race should not request worker") }, 1024)
	for i := 0; i < 30; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		d := &nativeBrowserDownloadStore{browser: b, ctx: ctx, cancel: cancel}
		var group sync.WaitGroup
		group.Add(2)
		go func() { defer group.Done(); d.start() }()
		go func() { defer group.Done(); d.stop() }()
		group.Wait()
		d.start() // closed stores cannot add another goroutine after Wait
		d.stop()
	}
}

func TestNativeBrowserDownloadsRejectMalformedLists(t *testing.T) {
	for _, raw := range []string{`{}`, `{"downloads":null}`, `{"downloads":"not a list"}`} {
		t.Run(raw, func(t *testing.T) {
			b, _ := downloadTestBrowser(t, func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, raw) }, 1024)
			if _, err := b.Downloads(context.Background()); err == nil {
				t.Fatal("malformed worker response reported a successful empty list")
			}
		})
	}
}
