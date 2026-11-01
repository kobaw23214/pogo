// Session deletion collects the uploads the session owned — except the ones
// another session in the same workspace still references. Attribution comes
// from the transcript (sent attachments) and the per-session ledger (uploads
// that never reached a message); the reference check scans surviving
// transcripts of the same workspace only.
package web

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yosukeno/pogo/llm"
)

// The deletion matrix in one run: a shared file another session references
// stays (with its cache), an unsent upload only the ledger remembers goes, a
// file a *foreign-workspace* transcript mentions goes too — that spelling is
// a different uploads/ entirely and must not shield it.
func TestDeleteSessionGarbageCollectsUploads(t *testing.T) {
	h := newHarness(t, func(int) (llm.Response, error) { return plainTurn() })
	a := h.createSession()
	cwd := h.session(a).cwd

	sharedData := []byte("shared csv\n1,2\n")
	orphanData := []byte("orphan text\n")
	strayData := []byte("stray csv\n3,4\n")
	shared := upload2(t, h, a, sharedData, "shared.csv")
	// .csv, not .txt: the default upload allowlist is image + office + media, and
	// this test is about reclamation, not the gate. The extension carries no
	// meaning here — only the ledger and transcript references do.
	upload2(t, h, a, orphanData, "orphan.csv")
	upload2(t, h, a, strayData, "stray.csv")

	// shared.csv is sent in A, then referenced from a second session in the
	// same workspace — the exact multi-session reference the check protects.
	postAttach := func(sid, prompt, rel string) {
		t.Helper()
		h.post("/api/sessions/"+sid+"/messages",
			fmt.Sprintf(`{"prompt":%q,"attachments":[%q]}`, prompt, rel), http.StatusAccepted)
		waitFor(t, 5*time.Second, func() bool {
			raw, err := os.ReadFile(filepath.Join(h.mgr.cfg.SessionDir, sid+".jsonl"))
			return err == nil && strings.Contains(string(raw), `"file_path":"`+rel+`"`)
		})
		waitFor(t, 5*time.Second, func() bool { return !h.session(sid).Active() })
	}
	postAttach(a, "看看这份表", shared.Path)
	b := h.createSession()
	postAttach(b, "再看一次", shared.Path)

	// A transcript from another workspace mentions stray.csv's spelling.
	foreign := "{\"id\":\"m\",\"type\":\"meta\",\"time\":0,\"meta\":{\"cwd\":\"/elsewhere\",\"model\":\"\",\"skills\":[]}}\n" +
		"{\"id\":\"u\",\"parentId\":\"m\",\"type\":\"message\",\"time\":0,\"message\":{\"role\":\"user\",\"content\":[{\"type\":\"file\",\"file_path\":\"uploads/stray.csv\",\"file_name\":\"stray.csv\"}]}}\n"
	if err := os.WriteFile(filepath.Join(h.mgr.cfg.SessionDir, "foreignx0.jsonl"), []byte(foreign), 0o644); err != nil {
		t.Fatal(err)
	}

	if entries, _ := filepath.Glob(filepath.Join(cwd, "uploads", ".cache", "*.json")); len(entries) < 3 {
		t.Fatalf("want metadata cache for all three uploads, got %v", entries)
	}

	resp := h.do(http.MethodDelete, "/api/sessions/"+a, "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete session: %d, want 204", resp.StatusCode)
	}

	if _, err := os.Stat(filepath.Join(cwd, "uploads", "shared.csv")); err != nil {
		t.Error("upload another session references was removed")
	}
	for _, name := range []string{"orphan.csv", "stray.csv"} {
		if _, err := os.Stat(filepath.Join(cwd, "uploads", name)); !os.IsNotExist(err) {
			t.Errorf("%s survived the session's deletion: %v", name, err)
		}
	}

	// The cache follows the files: the survivor keeps its metadata entry, the
	// collected two lose theirs.
	cacheEntry := func(data []byte) string {
		sum := sha256.Sum256(data)
		return filepath.Join(cwd, "uploads", ".cache", hex.EncodeToString(sum[:])+".json")
	}
	if _, err := os.Stat(cacheEntry(sharedData)); err != nil {
		t.Error("referenced file's metadata cache was swept")
	}
	for _, data := range [][]byte{orphanData, strayData} {
		if _, err := os.Stat(cacheEntry(data)); !os.IsNotExist(err) {
			t.Error("collected file's metadata cache survived")
		}
	}

	// The deleted session's ledger retires; the referencing session's
	// transcript is untouched.
	if _, err := os.Stat(uploadLedgerPath(cwd, a)); !os.IsNotExist(err) {
		t.Error("deleted session's ledger survived")
	}
	if _, err := os.Stat(filepath.Join(h.mgr.cfg.SessionDir, b+".jsonl")); err != nil {
		t.Error("the referencing session's transcript was touched")
	}
}

// A session that never uploaded has nothing to collect, and the delete must
// not so much as create the uploads directory.
func TestDeleteSessionWithoutUploads(t *testing.T) {
	h := newHarness(t, func(int) (llm.Response, error) { return plainTurn() })
	sid := h.createSession()
	cwd := h.session(sid).cwd

	resp := h.do(http.MethodDelete, "/api/sessions/"+sid, "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("delete session: %d, want 204", resp.StatusCode)
	}
	if _, err := os.Stat(filepath.Join(cwd, "uploads")); !os.IsNotExist(err) {
		t.Errorf("uploads dir appeared from nowhere: %v", err)
	}
}
