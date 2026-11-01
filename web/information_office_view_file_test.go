package web

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/yosukeno/pogo/information"
)

type informationViewGatedReader struct {
	once       sync.Once
	started    chan struct{}
	proceed    chan struct{}
	underlying io.Reader
}

func (r *informationViewGatedReader) Read(p []byte) (int, error) {
	r.once.Do(func() { close(r.started) })
	<-r.proceed
	return r.underlying.Read(p)
}

type informationViewZeroReader struct{}

func (informationViewZeroReader) Read(p []byte) (int, error) {
	clear(p)
	return len(p), nil
}

func informationViewLargeWorkbook(t *testing.T, payloadBytes int64) *os.File {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "large-workbook-*.xlsx")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	z := zip.NewWriter(f)
	for name, text := range map[string]string{
		"[Content_Types].xml": `<Types/>`,
		"xl/workbook.xml":     `<workbook/>`,
	} {
		part, err := z.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(part, text); err != nil {
			t.Fatal(err)
		}
	}
	// An uncompressed resource exercises a genuinely 90+ MiB HTTP body, not
	// merely a tiny ZIP whose metadata claims to represent a large workbook.
	part, err := z.CreateHeader(&zip.FileHeader{Name: "xl/media/fixture.bin", Method: zip.Store})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.CopyN(part, informationViewZeroReader{}, payloadBytes); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestInformationOfficeViewLargeWorkbookStreamsImmutableSnapshotAndHTTPRanges(t *testing.T) {
	h, o, _, _ := informationViewFixture(t)
	f := informationViewLargeWorkbook(t, 94<<20)
	owner := h.psrv.localIdentity("alice").Scope
	store, err := h.mgr.InformationStore(&owner)
	if err != nil {
		t.Fatal(err)
	}
	asset, err := store.PutAsset("large.xlsx", "", f, information.MaxAssetBytes)
	if err != nil {
		t.Fatal(err)
	}
	if asset.Size <= 94<<20 || asset.Size > informationOfficeViewMaxBytes {
		t.Fatal("large test fixture has unexpected size", asset.Size)
	}
	_, entry, err := store.Create(1, information.CreateInput{Kind: "file", Name: asset.Name, File: &asset})
	if err != nil {
		t.Fatal(err)
	}
	var opened informationViewTestReply
	request := map[string]any{"version": 1, "instanceId": "large_viewer_instance", "operationId": "large_viewer_operation"}
	target := "/api/information/entries/" + entry.ID + "/office-view"
	if err := json.Unmarshal(infoJSON(t, h, aliceToken, "POST", target, request, 200), &opened); err != nil {
		t.Fatal(err)
	}
	o.mu.Lock()
	d := o.sessions[opened.Session.ID]
	o.mu.Unlock()
	if d == nil || !d.readOnly || d.informationSnapshot == nil || len(d.snapshot) != 0 || d.informationSnapshot.size != asset.Size {
		t.Fatal("large viewer retained a heap-sized snapshot or lost its binding")
	}
	if opened.Source.SHA256 != asset.SHA256 || opened.Session.CanvasVersion != asset.SHA256 || opened.Source.Version != 1 || !opened.Session.ReadOnly {
		t.Fatal("large source lost immutable version binding")
	}
	fileURL := h.srv.URL + "/office/files/" + d.id + "/" + d.fileToken
	for _, check := range []struct {
		method, byteRange string
		status            int
		bodySize          int64
	}{
		{"HEAD", "", 200, 0},
		{"GET", "bytes=0-127", 206, 128},
		{"GET", "bytes=-512", 206, 512},
		{"GET", fmt.Sprintf("bytes=%d-", asset.Size), 416, -1},
		{"GET", "", 200, asset.Size},
	} {
		req, _ := http.NewRequest(check.method, fileURL, nil)
		if check.byteRange != "" {
			req.Header.Set("Range", check.byteRange)
		}
		resp, err := h.srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.New()
		read, err := io.Copy(digest, resp.Body)
		resp.Body.Close()
		if err != nil || resp.StatusCode != check.status || check.bodySize >= 0 && read != check.bodySize {
			t.Fatalf("%s %q: status=%d bytes=%d error=%v", check.method, check.byteRange, resp.StatusCode, read, err)
		}
		if check.status != 416 && (resp.Header.Get("Cache-Control") != "no-store" || resp.Header.Get("Accept-Ranges") != "bytes") {
			t.Fatal("large source lost private ranged HTTP serving", resp.Header)
		}
		if check.method == "HEAD" && resp.ContentLength != asset.Size {
			t.Fatal("HEAD lost exact source length", resp.ContentLength)
		}
		if check.method == "GET" && check.byteRange == "" && hex.EncodeToString(digest.Sum(nil)) != asset.SHA256 {
			t.Fatal("streamed source differs from immutable library version")
		}
	}
	var repeated informationViewTestReply
	json.Unmarshal(infoJSON(t, h, aliceToken, "POST", target, request, 200), &repeated)
	if repeated.Session.ID != opened.Session.ID {
		t.Fatal("large source retry opened duplicate viewer")
	}
	infoJSON(t, h, bobToken, "GET", "/api/office/sessions/"+d.id+"/status", nil, 404)
	infoJSON(t, h, bobToken, "POST", target, request, 404)
	file := d.informationSnapshot.file
	infoJSON(t, h, aliceToken, "POST", "/api/office/sessions/"+d.id+"/close", nil, 200)
	if _, err := file.Stat(); !errors.Is(err, os.ErrClosed) || d.informationSnapshot != nil {
		t.Fatal("closing large viewer did not release private snapshot", err)
	}
	if resp, err := h.srv.Client().Get(fileURL); err != nil {
		t.Fatal(err)
	} else {
		resp.Body.Close()
		if resp.StatusCode != 409 {
			t.Fatal("closed large-file capability remained usable", resp.StatusCode)
		}
	}
	if officeMaxBytes != 20<<20 {
		t.Fatal("readonly change expanded normal writer limits")
	}
}

func TestInformationOfficeViewSnapshotPinsConcurrentReadsUntilClose(t *testing.T) {
	original := officeTestPackage(t, "frozen independent reader offsets")
	snapshot, err := informationOfficeViewFreeze(context.Background(), bytes.NewReader(original), int64(len(original)), officeHash(original), "docx")
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.retire()
	file := snapshot.file
	readers, releases := []io.ReadSeeker{}, []func(){}
	for i := 0; i < 8; i++ {
		reader, release, err := snapshot.acquire()
		if err != nil {
			t.Fatal(err)
		}
		readers, releases = append(readers, reader), append(releases, release)
	}
	snapshot.retire()
	if _, _, err := snapshot.acquire(); err == nil {
		t.Fatal("retired snapshot admitted a new fetch")
	}
	var wg sync.WaitGroup
	failures := make(chan error, len(readers))
	for i, reader := range readers {
		wg.Add(1)
		go func(i int, reader io.ReadSeeker) {
			defer wg.Done()
			defer releases[i]()
			data, err := io.ReadAll(reader)
			if err != nil || !bytes.Equal(data, original) {
				failures <- fmt.Errorf("independent reader %d: bytes=%d error=%v", i, len(data), err)
			}
		}(i, reader)
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	for _, release := range releases {
		release() // Each release is idempotent, including a deferred second call.
	}
	if _, err := file.Stat(); !errors.Is(err, os.ErrClosed) {
		t.Fatal("last concurrent reader did not close retired snapshot", err)
	}
}

func TestInformationOfficeViewSnapshotRejectsOversizeMismatchCancellationAndPackageBomb(t *testing.T) {
	temporary := t.TempDir()
	t.Setenv("TMPDIR", temporary)
	original := officeTestPackage(t, "private snapshot validation")
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, test := range []struct {
		name     string
		context  context.Context
		size     int64
		hash     string
		fileType string
	}{
		{"oversize", context.Background(), informationOfficeViewMaxBytes + 1, officeHash(original), "docx"},
		{"changed size", context.Background(), int64(len(original)) + 1, officeHash(original), "docx"},
		{"changed hash", context.Background(), int64(len(original)), strings.Repeat("0", 64), "docx"},
		{"cancelled", cancelled, int64(len(original)), officeHash(original), "docx"},
		{"wrong extension", context.Background(), int64(len(original)), officeHash(original), "xlsx"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if snapshot, err := informationOfficeViewFreeze(test.context, bytes.NewReader(original), test.size, test.hash, test.fileType); err == nil || snapshot != nil {
				if snapshot != nil {
					snapshot.retire()
				}
				t.Fatal("invalid or cancelled document retained a snapshot")
			}
		})
	}
	for _, test := range []struct {
		sizes []uint64
		xml   bool
	}{
		{[]uint64{informationOfficeViewMaxPartBytes + 1}, false},
		{[]uint64{informationOfficeViewMaxPartBytes, informationOfficeViewMaxPartBytes, 1}, false},
		{[]uint64{informationOfficeViewMaxXMLBytes / 2, informationOfficeViewMaxXMLBytes/2 + 1}, true},
	} {
		var buffer bytes.Buffer
		archive := zip.NewWriter(&buffer)
		for i, size := range test.sizes {
			name := fmt.Sprintf("part-%d", i)
			if test.xml {
				name += ".xml"
			}
			_, err := archive.CreateRaw(&zip.FileHeader{Name: name, Method: zip.Store, UncompressedSize64: size})
			if err != nil {
				t.Fatal(err)
			}
		}
		if err := archive.Close(); err != nil {
			t.Fatal(err)
		}
		if err := informationOfficeViewPackage(bytes.NewReader(buffer.Bytes()), int64(buffer.Len()), "docx"); err == nil {
			t.Fatal("expanded package budget was not enforced")
		}
	}
	if entries, err := os.ReadDir(temporary); err != nil || len(entries) != 0 {
		t.Fatal("failed snapshot left a temporary document", entries, err)
	}
}

func TestInformationOfficeViewStorageBudgetCoversCopyAndRetiredReaders(t *testing.T) {
	original := officeTestPackage(t, "bounded temporary viewer storage")
	size := int64(len(original))
	budget := &informationOfficeViewStorageBudget{limit: size}
	blocked := &informationViewGatedReader{started: make(chan struct{}), proceed: make(chan struct{}), underlying: bytes.NewReader(original)}
	var unblock sync.Once
	resume := func() { unblock.Do(func() { close(blocked.proceed) }) }
	t.Cleanup(resume)
	type result struct {
		snapshot *informationOfficeViewSnapshot
		err      error
	}
	completed := make(chan result, 1)
	go func() {
		snapshot, err := informationOfficeViewFreezeWithBudget(context.Background(), blocked, size, officeHash(original), "docx", budget)
		completed <- result{snapshot, err}
	}()
	<-blocked.started
	// The first request has not copied any bytes, but already owns its space.
	if other, err := informationOfficeViewFreezeWithBudget(context.Background(), bytes.NewReader(original), size, officeHash(original), "docx", budget); err == nil || other != nil {
		if other != nil {
			other.retire()
		}
		t.Fatal("in-progress snapshot was excluded from the byte budget")
	} else {
		var failure *officeError
		if !errors.As(err, &failure) || failure.status != 429 || failure.code != "VIEWER_STORAGE_LIMIT" {
			t.Fatal("full viewer storage has no actionable limit response", err)
		}
	}
	resume()
	opened := <-completed
	if opened.err != nil {
		t.Fatal(opened.err)
	}
	defer opened.snapshot.retire()
	reader, release, err := opened.snapshot.acquire()
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	opened.snapshot.retire()
	if _, err := budget.reserve(1); err == nil {
		t.Fatal("closing viewer freed space while a fetch still held its file")
	}
	if data, err := io.ReadAll(reader); err != nil || !bytes.Equal(data, original) {
		t.Fatal("retiring viewer broke an already admitted read", err)
	}
	release()
	if budget.used != 0 {
		t.Fatal("last reader did not release snapshot budget", budget.used)
	}
	if _, err := informationOfficeViewFreezeWithBudget(context.Background(), bytes.NewReader(original), size, strings.Repeat("0", 64), "docx", budget); err == nil || budget.used != 0 {
		t.Fatal("failed copy retained byte budget", err, budget.used)
	}
	reopened, err := informationOfficeViewFreezeWithBudget(context.Background(), bytes.NewReader(original), size, officeHash(original), "docx", budget)
	if err != nil {
		t.Fatal("released space could not be reused", err)
	}
	reopened.retire()
	if budget.used != 0 {
		t.Fatal("closed snapshot retained byte budget", budget.used)
	}
}

func TestInformationOfficeViewStorageBudgetConcurrentReservationsStayBounded(t *testing.T) {
	budget := &informationOfficeViewStorageBudget{limit: 3}
	results := make(chan func(), 32)
	var wg sync.WaitGroup
	for i := 0; i < cap(results); i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, _ := budget.reserve(1)
			results <- release
		}()
	}
	wg.Wait()
	close(results)
	count := 0
	for release := range results {
		if release != nil {
			count++
			release()
			release()
		}
	}
	if count != 3 || budget.used != 0 {
		t.Fatal("concurrent reservations escaped the bound", count, budget.used)
	}
}
