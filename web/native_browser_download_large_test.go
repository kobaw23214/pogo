package web

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/yosukeno/pogo/prefs"
)

type downloadRepeatedReader struct{}

func (downloadRepeatedReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'a'
	}
	return len(p), nil
}

func TestNativeBrowserDownloadsIndependentOfUploadSize(t *testing.T) {
	filename := filepath.Join(t.TempDir(), "prefs.json")
	for _, uploadMB := range []int{10, 25, 1024} {
		p := prefs.Default()
		p.AttachmentMaxMB = uploadMB
		if err := prefs.Save(filename, p); err != nil {
			t.Fatal(err)
		}
		m := &Manager{browserDownloadPrefsPath: filename}
		allowed, limit := m.browserDownloadPolicy()
		if limit != 512<<20 || !allowed[".xlsx"] {
			t.Fatalf("upload limit affected downloads: %d %+v", limit, allowed)
		}
	}
}

func TestNativeBrowserDownloadsLargeStreamingMemory(t *testing.T) {
	const size int64 = 98426744
	b, s := downloadTestBrowser(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			writeJSON(w, 200, map[string]bool{"ok": true})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/content") {
			w.Header().Set("Content-Length", fmt.Sprint(size))
			_, _ = io.CopyN(w, downloadRepeatedReader{}, size)
			return
		}
		writeJSON(w, 200, map[string]any{"max_file_bytes": nativeDownloadLimit, "max_total_bytes": nativeDownloadTotalLimit, "downloads": []nativeBrowserDownload{{ID: downloadTestID, Filename: "large.csv", State: "completed", Size: size, TotalBytes: size}}})
	}, nativeDownloadLimit)
	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	result, err := b.Downloads(context.Background())
	runtime.ReadMemStats(&after)
	if err != nil || len(result.Downloads) != 1 || result.Downloads[0].State != "saved" {
		t.Fatalf("large import: %+v %v", result, err)
	}
	item := result.Downloads[0]
	info, err := os.Stat(filepath.Join(s.cwd, filepath.FromSlash(item.Path)))
	if err != nil || info.Size() != size || item.Size != size || result.MaxFileBytes != 512<<20 || result.MaxTotalBytes != 2<<30 {
		t.Fatalf("large file/limits: %+v %+v %v", item, info, err)
	}
	if allocated := after.TotalAlloc - before.TotalAlloc; allocated > 16<<20 {
		t.Fatalf("download buffered proportional to file: allocated %d for %d bytes", allocated, size)
	} else {
		t.Logf("streamed %d bytes with %d total Go allocation bytes", size, allocated)
	}
	if refs := sessionUploadLedger(s.cwd, s.ID); len(refs) != 1 || refs[0] != item.Path {
		t.Fatalf("large file missing from ledger: %v", refs)
	}
}

func TestNativeBrowserDownloadsErrorsKeepCodeButNotWorkerSecrets(t *testing.T) {
	b, _ := downloadTestBrowser(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			writeJSON(w, 200, map[string]bool{"ok": true})
			return
		}
		writeJSON(w, 200, map[string]any{"max_file_bytes": 32 << 20, "max_total_bytes": 128 << 20, "downloads": []nativeBrowserDownload{{ID: downloadTestID, Filename: "large.xlsx", State: "failed", TotalBytes: 98426744, ErrorCode: "DOWNLOAD_TOO_LARGE", Error: "https://site/?token=secret"}}})
	}, nativeDownloadLimit)
	result, err := b.Downloads(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	item := result.Downloads[0]
	if item.ErrorCode != "DOWNLOAD_TOO_LARGE" || item.ActualBytes != 98426744 || item.LimitBytes != 32<<20 || !strings.Contains(item.Error, "93.9 MiB") || !strings.Contains(item.Error, "32 MiB") || strings.Contains(item.Error, "secret") || result.RejectedCount != 0 {
		t.Fatalf("failure lost attribution: %+v", result)
	}
	if result.MaxFileBytes != 32<<20 || result.MaxTotalBytes != 128<<20 {
		t.Fatal("older worker limit was hidden")
	}
	state := map[string]json.RawMessage{}
	b.addDownloadState(state)
	if string(state["downloads_max_file_bytes"]) != "33554432" {
		t.Fatal("browser state lacks actual worker limit")
	}
	for _, code := range []string{"DOWNLOAD_TOTAL_LIMIT", "DOWNLOAD_CANCELED", "DOWNLOAD_TIMEOUT", "DOWNLOAD_EXPIRED", "DOWNLOAD_FILE_UNAVAILABLE", "https://secret"} {
		clean, ok := cleanBrowserDownload(nativeBrowserDownload{ID: downloadTestID, Filename: "x.csv", State: "failed", ErrorCode: code, Error: "private", ActualBytes: (2 << 30) + 1, LimitBytes: 2 << 30}, nativeDownloadLimit, nativeDownloadTotalLimit)
		if !ok || strings.Contains(clean.Error, "private") || strings.Contains(clean.Error, "secret") {
			t.Fatal("unsafe metadata")
		}
		want := code
		if strings.HasPrefix(code, "https:") {
			want = "DOWNLOAD_FAILED"
		}
		if clean.ErrorCode != want {
			t.Fatalf("code lost: %s => %s", code, clean.ErrorCode)
		}
	}
}

func TestNativeBrowserDownloadsTruncatedHTTPNeverPublishesOrRetries(t *testing.T) {
	b, s := downloadTestBrowser(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			writeJSON(w, 200, map[string]bool{"ok": true})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/content") {
			w.Header().Set("Content-Length", "10")
			_, _ = io.WriteString(w, "short")
			return
		}
		writeJSON(w, 200, map[string]any{"downloads": []nativeBrowserDownload{{ID: downloadTestID, Filename: "report.csv", State: "completed", Size: 10}}})
	}, 1024)
	result, err := b.Downloads(context.Background())
	if err != nil || result.Downloads[0].ErrorCode != "DOWNLOAD_SIZE_MISMATCH" || result.Downloads[0].Path != "" {
		t.Fatalf("truncated: %+v %v", result, err)
	}
	if refs := sessionUploadLedger(s.cwd, s.ID); len(refs) != 0 {
		t.Fatal("truncated file registered")
	}
	_ = filepath.WalkDir(s.cwd, func(path string, entry os.DirEntry, err error) error {
		if err == nil && !entry.IsDir() {
			t.Errorf("partial file remained: %s", path)
		}
		return err
	})
}

func TestNativeBrowserDownloadsStatusDoesNotWaitForBackgroundImport(t *testing.T) {
	started := make(chan struct{})
	stopped := make(chan struct{})
	metadataStarted := make(chan struct{})
	metadataRelease := make(chan struct{})
	b, s := downloadTestBrowser(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/content") {
			w.Header().Set("Content-Length", "1000")
			w.WriteHeader(200)
			_, _ = io.WriteString(w, "a")
			w.(http.Flusher).Flush()
			close(started)
			<-r.Context().Done()
			close(stopped)
			return
		}
		close(metadataStarted)
		<-metadataRelease
		writeJSON(w, 200, map[string]any{"downloads": []nativeBrowserDownload{{ID: downloadTestID, Filename: "report.csv", State: "completed", Size: 1000}}})
	}, 1024)
	b.downloads.start()
	initial, _ := b.Downloads(context.Background())
	if initial.Initialized || initial.ObservedAt != "" || !initial.RefreshPending {
		t.Fatalf("initial cached absence looked fresh: %+v", initial)
	}
	select {
	case <-metadataStarted:
	case <-time.After(time.Second):
		t.Fatal("worker metadata poll did not begin")
	}
	queued, _ := b.Downloads(context.Background())
	if queued.Initialized || queued.ObservedAt != "" || !queued.RefreshPending {
		t.Fatal("query fabricated metadata observation")
	}
	close(metadataRelease)
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("background import did not begin")
	}
	began := time.Now()
	snapshot, err := b.Downloads(context.Background())
	if err != nil || time.Since(began) > 100*time.Millisecond || len(snapshot.Downloads) != 1 || snapshot.Downloads[0].State != "completed" {
		t.Fatalf("status waited for transfer: %+v %v", snapshot, err)
	}
	if !snapshot.Initialized || snapshot.ObservedAt == "" || !snapshot.RefreshPending {
		t.Fatalf("in-flight content freshness missing: %+v", snapshot)
	}
	if _, err := time.Parse(time.RFC3339Nano, snapshot.ObservedAt); err != nil {
		t.Fatal(err)
	}
	again, _ := b.Downloads(context.Background())
	if again.ObservedAt != snapshot.ObservedAt || !again.RefreshPending {
		t.Fatal("status query changed worker observation timestamp")
	}
	b.Close()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("Close did not cancel content request")
	}
	if len(sessionUploadLedger(s.cwd, s.ID)) != 0 {
		t.Fatal("cancelled partial file registered")
	}
	_ = filepath.WalkDir(s.cwd, func(path string, entry os.DirEntry, err error) error {
		if err == nil && !entry.IsDir() {
			t.Errorf("partial file survived Close: %s", path)
		}
		return err
	})
}

func downloadOfficeFixture(t *testing.T, ext string) []byte {
	t.Helper()
	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	mainName, content := "xl/workbook.xml", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet.main+xml"
	if ext == ".docx" {
		mainName, content = "word/document.xml", "application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"
	}
	for name, data := range map[string]string{"[Content_Types].xml": `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Override PartName="/` + mainName + `" ContentType="` + content + `"/></Types>`, "_rels/.rels": "<Relationships/>", mainName: "<document/>", "padding.bin": "arbitrary package entry"} {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(entry, data)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestNativeBrowserDownloadsOfficeChecksRealBoundedPackage(t *testing.T) {
	for _, ext := range []string{".xlsx", ".docx"} {
		raw := downloadOfficeFixture(t, ext)
		if !validDownloadOfficeZIP(bytes.NewReader(raw), int64(len(raw)), ext) {
			t.Fatalf("valid %s package rejected", ext)
		}
		wrong := ".xlsx"
		if ext == wrong {
			wrong = ".docx"
		}
		if validDownloadOfficeZIP(bytes.NewReader(raw), int64(len(raw)), wrong) {
			t.Fatal("extension trusted without proper package")
		}
		if validDownloadOfficeZIP(bytes.NewReader(raw[:len(raw)-1]), int64(len(raw)-1), ext) {
			t.Fatal("truncated archive accepted")
		}
		forged := append([]byte{}, raw...)
		end := len(forged) - 22
		binary.LittleEndian.PutUint16(forged[end+8:end+10], 1)
		binary.LittleEndian.PutUint16(forged[end+10:end+12], 1)
		if validDownloadOfficeZIP(bytes.NewReader(forged), int64(len(forged)), ext) {
			t.Fatal("forged directory count accepted")
		}
		forged = append([]byte{}, raw...)
		binary.LittleEndian.PutUint32(forged[end+12:end+16], 46)
		if validDownloadOfficeZIP(bytes.NewReader(forged), int64(len(forged)), ext) {
			t.Fatal("forged directory size accepted")
		}
	}
	filename := filepath.Join(t.TempDir(), "false.xlsx")
	if err := os.WriteFile(filename, []byte("<html>login required</html>"), 0600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(filename)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	info, _ := file.Stat()
	if _, _, _, err := classifyBrowserDownloadFile("report.xlsx", file, info.Size(), map[string]bool{".xlsx": true}); err == nil {
		t.Fatal("login HTML accepted as workbook")
	}
}

func TestNativeBrowserDownloadsOneByteOverLimitExplainsExactSize(t *testing.T) {
	message := downloadFailureMessage("DOWNLOAD_TOO_LARGE", (512<<20)+1, 512<<20)
	if !strings.Contains(message, "536870913") || !strings.Contains(message, "536870912") {
		t.Fatal(message)
	}
}

func TestNativeBrowserDownloadsZIPParserCannotSelectUnreviewedEnd(t *testing.T) {
	raw := downloadOfficeFixture(t, ".xlsx")
	// The standard decoder selects the later EOCD embedded in a comment. The
	// precheck must not skip that record and accept the earlier outer one.
	nested := append([]byte{}, raw...)
	comment := append([]byte{'P', 'K', 5, 6}, make([]byte, 18)...)
	comment = append(comment, 1, 2, 3)
	binary.LittleEndian.PutUint16(nested[len(nested)-2:], uint16(len(comment)))
	nested = append(nested, comment...)
	if validDownloadOfficeZIP(bytes.NewReader(nested), int64(len(nested)), ".xlsx") {
		t.Fatal("inner EOCD escaped prechecked directory")
	}
	// Archive comments without an alternate structural record remain supported.
	ordinary := append([]byte{}, raw...)
	binary.LittleEndian.PutUint16(ordinary[len(ordinary)-2:], 7)
	ordinary = append(ordinary, []byte("comment")...)
	if !validDownloadOfficeZIP(bytes.NewReader(ordinary), int64(len(ordinary)), ".xlsx") {
		t.Fatal("ordinary archive comment rejected")
	}
	locator := append([]byte{}, raw...)
	end := len(locator) - 22
	copy(locator[end-20:end-16], []byte{'P', 'K', 6, 7})
	if validDownloadOfficeZIP(bytes.NewReader(locator), int64(len(locator)), ".xlsx") {
		t.Fatal("ZIP64 alternate-directory locator accepted")
	}
}
