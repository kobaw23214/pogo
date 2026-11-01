// Rewind's upload semantics: attachments are message-bound, not workspace
// state. Withdrawing a message forks the transcript but leaves the uploads on
// disk — the composer's draft rides back as path chips reusing the same
// bytes, so a re-send uploads nothing. Cleanup belongs to the session's own
// delete (gcSessionUploads), which these pin from both ends.
package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yosukeno/pogo/llm"
)

// runEnded counts settled runs in the session's log.
func runEnded(t *testing.T, h *harness, sid string) int {
	t.Helper()
	backlog, _, cancel := h.session(sid).Hub().Subscribe(0)
	defer cancel()
	ends := 0
	for _, e := range backlog {
		if e.Type == EvRunEnd {
			ends++
		}
	}
	return ends
}

// Withdrawing the only message that referenced an upload keeps the bytes:
// the draft comes back to the composer as a path chip, a re-send uploads
// nothing, and the file's cleanup is the session's delete — asserted here
// end to end: kept after the rewind, gone (and unlisted) once the session
// goes.
func TestRewindKeepsTheStrandedUploadUntilTheSessionGoes(t *testing.T) {
	visionSeams2(t, "蓝色矩形截图", nil)
	h := newHarness(t, func(int) (llm.Response, error) { return plainTurn() })
	sid := h.createSession()
	cwd := h.session(sid).cwd

	meta := upload2(t, h, sid, pngBytes2(t, 8, 6), "pic.png")
	h.post("/api/sessions/"+sid+"/messages",
		fmt.Sprintf(`{"prompt":"什么颜色","attachments":[%q]}`, meta.Path), http.StatusAccepted)
	waitFor(t, 5*time.Second, func() bool { return runEnded(t, h, sid) == 1 })

	abs := filepath.Join(cwd, meta.Path)
	if _, err := os.Stat(abs); err != nil {
		t.Fatalf("the upload must exist before the rewind: %v", err)
	}
	listed := func() int {
		t.Helper()
		resp := h.do(http.MethodGet, "/api/sessions/"+sid+"/attachments", "")
		defer resp.Body.Close()
		var out struct {
			Attachments []AttachmentMeta `json:"attachments"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		return len(out.Attachments)
	}
	if got := listed(); got != 1 {
		t.Fatalf("session-files before rewind = %d, want 1", got)
	}

	u1 := h.session(sid).Hub().Snapshot().Messages[0]
	h.post("/api/sessions/"+sid+"/control",
		fmt.Sprintf(`{"action":"rewind","message_id":%q,"mode":"chat"}`, u1.ID), http.StatusOK)

	// The transcript no longer names the file, but the ledger does — the
	// draft's path chip needs both the bytes and the listing to live on.
	if _, err := os.Stat(abs); err != nil {
		t.Errorf("the rewound message's upload was swept: %v", err)
	}
	if got := listed(); got != 1 {
		t.Errorf("session-files after rewind = %d, want 1 (the draft still points here)", got)
	}

	h.do(http.MethodDelete, "/api/sessions/"+sid, "")
	if _, err := os.Stat(abs); !os.IsNotExist(err) {
		t.Errorf("the upload outlived its session: %v", err)
	}
}

// A file the surviving branch still references is not stranded: sending the
// same attachment twice and withdrawing the second message keeps the bytes
// for the first.
func TestRewindKeepsTheUploadTheSurvivingBranchUses(t *testing.T) {
	visionSeams2(t, "蓝色矩形截图", nil)
	h := newHarness(t, func(int) (llm.Response, error) { return plainTurn() })
	sid := h.createSession()
	cwd := h.session(sid).cwd

	meta := upload2(t, h, sid, pngBytes2(t, 8, 6), "pic.png")
	for i, prompt := range []string{"第一次", "第二次"} {
		h.post("/api/sessions/"+sid+"/messages",
			fmt.Sprintf(`{"prompt":%q,"attachments":[%q]}`, prompt, meta.Path), http.StatusAccepted)
		want := i + 1
		waitFor(t, 5*time.Second, func() bool { return runEnded(t, h, sid) >= want })
	}

	// [user1, assistant1, user2, assistant2] — the second prompt forks away.
	u2 := h.session(sid).Hub().Snapshot().Messages[2]
	h.post("/api/sessions/"+sid+"/control",
		fmt.Sprintf(`{"action":"rewind","message_id":%q,"mode":"chat"}`, u2.ID), http.StatusOK)

	if _, err := os.Stat(filepath.Join(cwd, meta.Path)); err != nil {
		t.Errorf("the surviving branch's upload went away with the rewind: %v", err)
	}
}
