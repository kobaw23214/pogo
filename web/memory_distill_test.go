package web

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/memory"
	"github.com/yosukeno/pogo/prefs"
)

// --- parsing -----------------------------------------------------------------

func TestDecodeDistillNotesCleanAnswer(t *testing.T) {
	notes, repaired := parseDistillNotes(`{"notes": [
		{"path": "sample-db.md", "content": "样本库的最大检出数筛选在检索页。"},
		{"path": "yara-review.md", "content": "Yara 规则必须人工复核后再投产。"}
	]}`, true)
	if repaired {
		t.Error("a clean answer was reported as repaired")
	}
	if len(notes) != 2 || notes[0].Path != "sample-db.md" || !strings.HasPrefix(notes[1].Content, "Yara") {
		t.Fatalf("notes = %+v", notes)
	}
}

// "Only output JSON" is a hope, not a property: models wrap the object in
// prose often enough that the parse has to slice it out.
func TestDecodeDistillNotesSlicesOutTheObject(t *testing.T) {
	raw := "已整理：\n{\"notes\": [{\"path\": \"a.md\", \"content\": \"x\"}]}\n完毕。"
	notes, _ := parseDistillNotes(raw, true)
	if len(notes) != 1 || notes[0].Path != "a.md" {
		t.Fatalf("notes = %+v, want the object parsed out of the prose", notes)
	}
}

// The path contract is the whole defence against a model writing outside the
// memory directory: flat lowercase slugs, no traversal, no subdirectories.
func TestDecodeDistillNotesEnforcesThePathContract(t *testing.T) {
	for _, path := range []string{
		"../outside.md", "/abs.md", "sub/dir.md", "UPPER.md", "under_score.md",
		"no-ext", ".md", "-lead.md", "trail-.md is fine.md", "", "a.md.bak",
	} {
		notes, _ := parseDistillNotes(`{"notes": [{"path": "`+path+`", "content": "x"}]}`, true)
		if notes != nil {
			t.Errorf("path %q was accepted", path)
		}
	}
	notes, _ := parseDistillNotes(`{"notes": [{"path": "fine-name-2.md", "content": "x"}]}`, true)
	if len(notes) != 1 {
		t.Errorf("a legitimate slug was rejected: %+v", notes)
	}
}

// Silence is a designed answer — both as the model's own choice and as every
// contract violation's outcome, because memory outlives the session and a note
// the host cannot stand behind costs every future session.
func TestDecodeDistillNotesSilence(t *testing.T) {
	for _, raw := range []string{
		"",                              // nothing came back
		"no object here",                // prose only
		`{"notes": []}`,                 // the model's own "nothing worth keeping"
		`{"notes": "nope"}`,             // wrong shape entirely
		`{"notes": [{"path": "a.md"}]}`, // contentless
		`{"notes": [{"path": "a.md", "content": "  "}]}`, // whitespace-only
	} {
		if notes, _ := parseDistillNotes(raw, true); notes != nil {
			t.Errorf("parseDistillNotes(%q) = %v, want silence", raw, notes)
		}
	}
}

func TestDecodeDistillNotesEnforcesTheBounds(t *testing.T) {
	// At most distillMaxNotes per pass: a model that disagrees is usually
	// filling, and one exchange rarely carries more durable facts than that.
	var b strings.Builder
	b.WriteString(`{"notes": [`)
	for i := 0; i < distillMaxNotes+2; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`{"path": "n` + strings.Repeat("x", i) + `0.md", "content": "c"}`)
	}
	b.WriteString(`]}`)
	if got, _ := parseDistillNotes(b.String(), true); len(got) != distillMaxNotes {
		t.Errorf("kept %d notes, want the cap of %d", len(got), distillMaxNotes)
	}

	// An oversized note is dropped rather than truncated: a cut-off paragraph
	// reads as a conclusion, which is worse than no note.
	notes, _ := parseDistillNotes(`{"notes": [{"path": "big.md", "content": "`+
		strings.Repeat("长", distillMaxNoteBytes)+`"}]}`, true)
	if notes != nil {
		t.Errorf("an oversized note was kept")
	}

	// The same topic twice in one pass keeps the first: the second is the model
	// contradicting itself, and the earlier draft won the model's own ordering.
	notes, _ = parseDistillNotes(`{"notes": [
		{"path": "a.md", "content": "first"},
		{"path": "a.md", "content": "second"}
	]}`, true)
	if len(notes) != 1 || !strings.HasPrefix(notes[0].Content, "first") {
		t.Errorf("duplicated topics were not collapsed: %+v", notes)
	}
}

func TestDecodeDistillNotesValidatesTypeAndCredentials(t *testing.T) {
	notes, _ := parseDistillNotes(`{"notes": [{"path": "feedback.md", "type": "feedback", "content": "Always run tests first."}]}`, true)
	if len(notes) != 1 || notes[0].Type != memory.NoteFeedback {
		t.Fatalf("typed note = %+v", notes)
	}
	for _, raw := range []string{
		`{"notes": [{"path": "bad.md", "type": "guess", "content": "x"}]}`,
		`{"notes": [{"path": "secret.md", "type": "project", "content": "api_key = \"abcdefghijklmnop\""}]}`,
	} {
		if notes, _ := parseDistillNotes(raw, true); notes != nil {
			t.Errorf("invalid note was accepted: %+v", notes)
		}
	}
}

// The repair fallback: an answer that came back in JSON but broke it still
// yields its notes, while the same breakage under a token cap stays silence —
// a truncated note is unfinished, not broken.
func TestParseDistillNotesRepairsBrokenJSON(t *testing.T) {
	notes, repaired := parseDistillNotes(`{"notes": [{"path": "a.md", "content": "x"}`, true)
	if !repaired || len(notes) != 1 {
		t.Errorf("the broken answer was not repaired: %v, %v", notes, repaired)
	}
	if notes, repaired := parseDistillNotes(`{"notes": [{"path": "a.md", "content": "x"}`, false); notes != nil || repaired {
		t.Errorf("repair ran with the gate closed: %v, %v", notes, repaired)
	}
}

// --- writing -----------------------------------------------------------------

// Topic updates keep one current file while host metadata records its origin.
func TestWriteDistilledNotesWritesAndOverwrites(t *testing.T) {
	dir := filepath.Join(t.TempDir(), ".pogo", "memory")
	if got := writeDistilledNotes(dir, []distillNote{{Path: "a.md", Content: "v1\n"}}, "session_1"); got != 1 {
		t.Fatalf("written = %d", got)
	}
	if got := writeDistilledNotes(dir, []distillNote{{Path: "a.md", Type: memory.NoteFeedback, Content: "v2\n"}}, "session_2"); got != 1 {
		t.Fatalf("overwrite written = %d", got)
	}
	note, err := memory.ReadNote(dir, "a.md")
	if err != nil || note.Content != "v2\n" || note.Type != memory.NoteFeedback || note.SourceSession != "session_2" || note.Origin != "auto-distill" {
		t.Fatalf("note = %+v, %v", note, err)
	}
	// A name the contract would never have accepted is not written even if the
	// caller let one through — the write side checks too.
	if got := writeDistilledNotes(dir, []distillNote{{Path: "../escape.md", Content: "x"}}); got != 0 {
		t.Fatalf("an out-of-contract name was written")
	}
	if got := writeDistilledNotes(dir, []distillNote{{Path: "secret.md", Content: `api_key = "abcdefghijklmnop"`}}); got != 0 {
		t.Fatalf("a credential was written")
	}
}

// --- the index ---------------------------------------------------------------

// Names only, and empty reads as empty: the index exists so the model
// overwrites a topic instead of duplicating it, which takes the file list and
// nothing else.
func TestMemoryIndex(t *testing.T) {
	if got := memoryIndex(filepath.Join(t.TempDir(), "nope")); got != "(empty)" {
		t.Errorf("a missing directory indexed as %q", got)
	}
	dir := t.TempDir()
	if got := memoryIndex(dir); got != "(empty)" {
		t.Errorf("an empty directory indexed as %q", got)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.md"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".hidden"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	got := memoryIndex(dir)
	if !strings.Contains(got, "a.md") || !strings.Contains(got, "b.md") || strings.Contains(got, ".hidden") {
		t.Errorf("index = %q", got)
	}
	if !strings.Contains(got, `"content":"x\n"`) {
		t.Errorf("topic updates need the existing body for merging: %q", got)
	}
}

// --- end to end --------------------------------------------------------------

func distillTurn(notes string) llm.Response {
	return llm.Response{
		Message:    llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockText, Text: notes}}},
		StopReason: llm.StopEndTurn,
		Usage:      llm.Usage{Input: 90, Output: 20},
	}
}

func waitForFile(t *testing.T, path string) string {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		data, err := os.ReadFile(path)
		if err == nil {
			return string(data)
		}
		if time.Now().After(deadline) {
			t.Fatalf("the note never landed at %s", path)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// realTempDir resolves the platform's temp symlinks (macOS /var →
// /private/var): the authored-skill store refuses symlinked roots, and a
// studio run builds one under SessionDir, so harness dirs must be canonical.
func realTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// armMemoryAuto points the preferences file at a temp path with the
// distillation switch in the given state — the settings-dialog state the
// Server reads at startup. The env is always set, even for "off", so a test
// never reads the developer machine's real prefs.json.
func armMemoryAuto(t *testing.T, on bool) string {
	t.Helper()
	path := filepath.Join(realTempDir(t), "prefs.json")
	t.Setenv("POGO_PREFS_FILE", path)
	if err := prefs.Save(path, prefs.Prefs{SpillRetentionDays: prefs.DefaultSpillRetentionDays, MemoryAuto: on}); err != nil {
		t.Fatal(err)
	}
	return path
}

// A completed interactive run distils into the Agent's own directory, off the
// answer's critical path, and the call's spend reaches the session's totals
// the way follow-up generation's does.
func TestMemoryDistillationWritesAfterRunEnd(t *testing.T) {
	armMemoryAuto(t, true)
	h, cap := newStudioHarness(t, []llm.Response{
		textTurn("已查清：样本库的最大检出数筛选在检索页。"),
		distillTurn(`{"notes": [{"path": "sample-db.md", "content": "样本库的最大检出数筛选在检索页。"}]}`),
	}, func(c *Config) { c.Cwd = realTempDir(t); c.SessionDir = realTempDir(t) })
	agent := createStudioAgent(t, h, "记忆员", "记住结论", "ready")
	sid := startStudioConversation(t, h, agent.ID)
	s := h.start(sid, "帮我查一下样本库的筛选入口")
	s.wait(t, EvRunEnd)

	sess := h.session(sid)
	if sess.memoryDir == "" {
		t.Fatal("the session carries no Agent memory directory")
	}
	content := waitForFile(t, filepath.Join(sess.memoryDir, "sample-db.md"))
	if !strings.Contains(content, "样本库") {
		t.Errorf("note content = %q", content)
	}
	if systems, _, _, _ := cap.snapshot(); len(systems) != 2 {
		t.Errorf("the model saw %d calls, want the run's answer plus the distillation", len(systems))
	}
	usage, _ := sess.store.UsageTotals()
	if usage.Input != 90 || usage.Output != 20 {
		t.Errorf("recorded usage = %+v, want the distillation's 90/20", usage)
	}
}

// The zero-value default — a prefs file without memory_auto — means no
// distillation call at all, and memory stays hand-written.
func TestMemoryDistillationStaysOffWithoutTheSwitch(t *testing.T) {
	armMemoryAuto(t, false)
	h, cap := newStudioHarness(t, []llm.Response{
		textTurn("done."),
		distillTurn(`{"notes": [{"path": "a.md", "content": "x"}]}`),
	}, func(c *Config) { c.Cwd = realTempDir(t); c.SessionDir = realTempDir(t) })
	agent := createStudioAgent(t, h, "记忆员", "记住结论", "ready")
	sid := startStudioConversation(t, h, agent.ID)
	s := h.start(sid, "hi")
	s.wait(t, EvRunEnd)
	// Give a hypothetical goroutine its chance: the assertion is that none ran.
	time.Sleep(300 * time.Millisecond)
	if systems, _, _, _ := cap.snapshot(); len(systems) != 1 {
		t.Errorf("the model saw %d calls, want only the run's own", len(systems))
	}
	if dir := h.session(sid).memoryDir; dir != "" {
		if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 {
			t.Errorf("notes were written with the switch off: %v", entries)
		}
	}
}

// The settings switch is live: flipped mid-process, the next run of an
// already-open conversation distils without a restart — and the flip is
// persisted, so a restart keeps it.
func TestMemoryDistillationFollowsTheSettingsSwitchWithoutRestart(t *testing.T) {
	prefsPath := armMemoryAuto(t, false)
	h, cap := newStudioHarness(t, []llm.Response{
		textTurn("第一轮结论。"),
		textTurn("第二轮结论。"),
		distillTurn(`{"notes": [{"path": "hot.md", "content": "热更新后提炼的内容。"}]}`),
	}, func(c *Config) { c.Cwd = realTempDir(t); c.SessionDir = realTempDir(t) })
	agent := createStudioAgent(t, h, "记忆员", "记住结论", "ready")
	sid := startStudioConversation(t, h, agent.ID)

	s := h.start(sid, "第一轮")
	s.wait(t, EvRunEnd)
	// Give a hypothetical goroutine its chance: the assertion is that none ran.
	time.Sleep(300 * time.Millisecond)
	if systems, _, _, _ := cap.snapshot(); len(systems) != 1 {
		t.Fatalf("with the switch off the model saw %d calls, want only the run's own", len(systems))
	}

	// Flip the switch the way the settings dialog does: one PUT applies the
	// live value first and persists second.
	resp := h.do(http.MethodPut, "/api/memory-auto", `{"memory_auto":true}`)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("PUT /api/memory-auto: %d", resp.StatusCode)
	}
	p, err := prefs.Load(prefsPath)
	if err != nil || !p.MemoryAuto {
		t.Fatalf("the choice must land in prefs.json: %+v (%v)", p, err)
	}

	s2 := h.start(sid, "第二轮")
	s2.wait(t, EvRunEnd)
	content := waitForFile(t, filepath.Join(h.session(sid).memoryDir, "hot.md"))
	if !strings.Contains(content, "热更新") {
		t.Errorf("note content = %q", content)
	}
	if systems, _, _, _ := cap.snapshot(); len(systems) != 3 {
		t.Errorf("after the toggle the model saw %d calls, want two runs plus the distillation", len(systems))
	}
}

func TestDistillMemorySerializesSessionsSharingRoot(t *testing.T) {
	var active, maximum int32
	h := newHarness(t, func(int) (llm.Response, error) {
		current := atomic.AddInt32(&active, 1)
		defer atomic.AddInt32(&active, -1)
		for {
			previous := atomic.LoadInt32(&maximum)
			if current <= previous || atomic.CompareAndSwapInt32(&maximum, previous, current) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		return distillTurn(`{"notes":[]}`), nil
	})
	root := realTempDir(t)
	sessions := []*Session{h.session(h.createSession()), h.session(h.createSession())}
	var wg sync.WaitGroup
	for _, sess := range sessions {
		sess.memoryDir = root
		wg.Add(1)
		go func(s *Session) {
			defer wg.Done()
			s.distillMemory(context.Background(), s.cfg, root, nil)
		}(sess)
	}
	wg.Wait()
	if maximum > 1 {
		t.Fatalf("two distillers read and replaced the same root concurrently: %d", maximum)
	}
}
