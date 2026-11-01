// Session attachments end to end: upload lands in the session workspace's
// uploads/, a message carrying it records a file block whose fallback note
// names the path, and an image attachment still flows through the vision
// helper. The helper is scripted through the vision seams; no test touches a
// network.
package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yosukeno/pogo/config"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/prefs"
)

func pngBytes2(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		for y := 0; y < h; y++ {
			img.Set(x, y, color.RGBA{R: 0x33, G: 0x66, B: 0x99, A: 0xff})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// visionSeams2 scripts the helper; the counter reports how often it ran.
func visionSeams2(t *testing.T, answer string, err error) *int32 {
	t.Helper()
	savedName, savedResolve, savedExchange := visionModelName, visionResolve, visionExchange
	var calls int32
	visionModelName = func() string { return "eyes" }
	visionResolve = func(string) (config.Resolved, error) {
		return config.Resolved{Model: "eyes", BaseURL: "http://vision.local/v1"}, nil
	}
	visionExchange = func(context.Context, llm.Client, string, string) (string, error) {
		calls++
		return answer, err
	}
	t.Cleanup(func() {
		visionModelName, visionResolve, visionExchange = savedName, savedResolve, savedExchange
	})
	return &calls
}

func upload2(t *testing.T, h *harness, sid string, data []byte, name string) AttachmentMeta {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", name)
	fw.Write(data)
	mw.Close()
	req, _ := http.NewRequest(http.MethodPost, h.srv.URL+"/api/sessions/"+sid+"/attachments", &buf)
	req.Header.Set("Authorization", "Bearer "+h.token)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := h.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(resp.Body)
		t.Fatalf("upload %s: %d (%s)", name, resp.StatusCode, bytes.TrimSpace(msg))
	}
	var meta AttachmentMeta
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		t.Fatal(err)
	}
	return meta
}

// Upload lands inside the session's own workspace — the whole feature — with a
// sanitised unique name, and unsupported types are refused at the door.
func TestAttachmentLandsInSessionWorkspace(t *testing.T) {
	h := newHarness(t, func(int) (llm.Response, error) { return plainTurn() })
	sid := h.createSession()
	cwd := h.session(sid).cwd

	meta := upload2(t, h, sid, pngBytes2(t, 8, 6), "shot.png")
	if meta.Kind != "image" || meta.Path != "uploads/shot.png" {
		t.Fatalf("meta = %+v", meta)
	}
	if _, err := os.Stat(filepath.Join(cwd, "uploads", "shot.png")); err != nil {
		t.Fatalf("file not in the session workspace: %v", err)
	}
	// Same name twice: two files, not an overwrite.
	upload2(t, h, sid, []byte("just some csv\na,b\n"), "shot.png-2.csv")
	upload2(t, h, sid, pngBytes2(t, 8, 6), "shot.png")
	if _, err := os.Stat(filepath.Join(cwd, "uploads", "shot-2.png")); err != nil {
		t.Errorf("collision not suffixed: %v", err)
	}

	// Refused types say so before anything is written.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "tool.exe")
	fw.Write([]byte("MZ9000"))
	mw.Close()
	req, _ := http.NewRequest(http.MethodPost, h.srv.URL+"/api/sessions/"+sid+"/attachments", &buf)
	req.Header.Set("Authorization", "Bearer "+h.token)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	bad, err := h.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	bad.Body.Close()
	if bad.StatusCode != http.StatusBadRequest {
		t.Errorf("exe upload: %d, want 400", bad.StatusCode)
	}
}

func TestDocumentAndMediaUploadPreview(t *testing.T) {
	h := newHarness(t, func(int) (llm.Response, error) { return plainTurn() })
	sid := h.createSession()
	for _, tc := range []struct {
		name, kind, mime string
		data             []byte
	}{
		{"report.pdf", "pdf", "application/pdf", []byte("%PDF-1.4\n1 0 obj\n<< /Type /Catalog >>\nendobj\n%%EOF\n")},
		{"voice.wav", "audio", "audio/wave", []byte("RIFF\x04\x00\x00\x00WAVE")},
		{"movie.mp4", "video", "video/mp4", []byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42isom")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			meta := upload2(t, h, sid, tc.data, tc.name)
			if meta.Kind != tc.kind || meta.Mime != tc.mime {
				t.Fatalf("upload metadata = %+v, want %s %s", meta, tc.kind, tc.mime)
			}
			preview := getJSON(t, h, "/api/files/content?path="+meta.Path, http.StatusOK)
			if preview["binary"] != true || preview["mime"] != tc.mime {
				t.Errorf("preview = %v, want binary %s", preview, tc.mime)
			}
			resp := h.do(http.MethodGet, "/api/files/content?path="+meta.Path+"&raw=1", "")
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != tc.mime {
				t.Errorf("raw response = %d %s, want 200 %s", resp.StatusCode, resp.Header.Get("Content-Type"), tc.mime)
			}
		})
	}
}

// A document attachment reaches the model as a path note, and the transcript
// records the file block — never the bytes.
func TestDocumentAttachmentBecomesFileBlock(t *testing.T) {
	h := newHarness(t, func(int) (llm.Response, error) { return plainTurn() })
	sid := h.createSession()

	meta := upload2(t, h, sid, []byte("hello word"), "报告.docx")
	if meta.Kind != "word" || meta.Path != "uploads/报告.docx" {
		t.Fatalf("meta = %+v", meta)
	}
	h.post("/api/sessions/"+sid+"/messages",
		fmt.Sprintf(`{"prompt":"总结这份文档","attachments":[%q]}`, meta.Path), http.StatusAccepted)

	waitFor(t, 5*time.Second, func() bool {
		raw, err := os.ReadFile(filepath.Join(h.mgr.cfg.SessionDir, sid+".jsonl"))
		return err == nil && strings.Contains(string(raw), `"file_path":"uploads/报告.docx"`) &&
			!strings.Contains(string(raw), "hello word")
	})
}

// An image attachment on a text-only model still gets its description, cached
// by content under uploads/.cache/.
func TestImageAttachmentDescribedAndCached(t *testing.T) {
	seams := visionSeams2(t, "一个蓝色矩形", nil)
	h := newHarness(t, func(int) (llm.Response, error) { return plainTurn() })
	sid := h.createSession()
	cwd := h.session(sid).cwd

	meta := upload2(t, h, sid, pngBytes2(t, 8, 6), "pic.png")
	h.post("/api/sessions/"+sid+"/messages",
		fmt.Sprintf(`{"prompt":"什么颜色","attachments":[%q]}`, meta.Path), http.StatusAccepted)

	waitFor(t, 5*time.Second, func() bool {
		raw, err := os.ReadFile(filepath.Join(h.mgr.cfg.SessionDir, sid+".jsonl"))
		return err == nil && strings.Contains(string(raw), "一个蓝色矩形")
	})
	if *seams != 1 {
		t.Errorf("helper ran %d times, want 1", *seams)
	}
	// The cache exists under .cache/, keyed by content hash.
	matches, _ := filepath.Glob(filepath.Join(cwd, "uploads", ".cache", "*.txt"))
	if len(matches) == 0 {
		t.Error("no description cache under uploads/.cache/")
	}
}

// The ask path memoises per workspace: same path, one run.
func TestVisionAskMemoisesPerWorkspace(t *testing.T) {
	calls := visionSeams2(t, "the answer", nil)
	h := newHarness(t, func(int) (llm.Response, error) { return plainTurn() })
	sid := h.createSession()
	cwd := h.session(sid).cwd
	upload2(t, h, sid, pngBytes2(t, 4, 4), "a.png")

	for i := 0; i < 2; i++ {
		if _, err := h.mgr.visionAsk(context.Background(), cwd, "uploads/a.png", "图里有什么"); err != nil {
			t.Fatal(err)
		}
	}
	if *calls != 1 {
		t.Errorf("identical question ran the helper %d times, want 1", *calls)
	}
}

// --- helpers shared with the previous suite shape ---

// plainTurn is the minimal responder: one assistant text turn.
func plainTurn() (llm.Response, error) {
	return llm.Response{
		Message:    llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockText, Text: "ok"}}},
		StopReason: llm.StopEndTurn,
	}, nil
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition never became true")
}

// historyClient records every history it is handed, for wire-side assertions.
type historyClient struct {
	model string
	seen  *[][]llm.Message
	mu    *sync.Mutex
}

func (c *historyClient) Model() string { return c.model }

func (c *historyClient) Stream(_ context.Context, _ string, history []llm.Message, _ []llm.ToolSchema, _ func(llm.Delta)) (llm.Response, error) {
	c.mu.Lock()
	*c.seen = append(*c.seen, append([]llm.Message(nil), history...))
	c.mu.Unlock()
	return plainTurn()
}

// An upload is announced on the event stream and listed by the GET endpoint
// the moment it lands — before and regardless of any message carrying the
// path. The pair is what lets a pane show the file without a reload.
func TestAttachmentListedAndAnnounced(t *testing.T) {
	h := newHarness(t, func(int) (llm.Response, error) { return plainTurn() })
	sid := h.createSession()

	// .txt sits outside the default allowlist (image + office + media); this test is
	// about listing and announcing, not the gate, so the text group is
	// switched on rather than the fixture bending to the default.
	pf := filepath.Join(t.TempDir(), "prefs.json")
	t.Setenv(prefs.EnvFile, pf)
	p := prefs.Default()
	p.AttachmentExts = append(defaultAttachExts(), ".txt")
	if err := prefs.Save(pf, p); err != nil {
		t.Fatal(err)
	}

	meta := upload2(t, h, sid, []byte("hello listing"), "listed.txt")

	// The event stream heard it: logged, so even a late subscriber replays it.
	backlog, _, cancel := h.session(sid).Hub().Subscribe(0)
	defer cancel()
	announced := false
	for _, e := range backlog {
		if e.Type == EvAttachmentUploaded && e.Attachment != nil && e.Attachment.Path == meta.Path {
			announced = true
		}
	}
	if !announced {
		t.Error("no attachment_uploaded event in the hub log")
	}

	// The listing names it, with no message ever sent.
	resp := h.do(http.MethodGet, "/api/sessions/"+sid+"/attachments", "")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET attachments: %d", resp.StatusCode)
	}
	var out struct {
		Attachments []AttachmentMeta `json:"attachments"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Attachments) != 1 || out.Attachments[0].Path != meta.Path ||
		out.Attachments[0].Kind != "text" || out.Attachments[0].Size != int64(len("hello listing")) {
		t.Fatalf("listing = %+v", out.Attachments)
	}

	// A file gone from disk drops out of the listing.
	if err := os.Remove(filepath.Join(h.session(sid).cwd, filepath.FromSlash(meta.Path))); err != nil {
		t.Fatal(err)
	}
	resp2 := h.do(http.MethodGet, "/api/sessions/"+sid+"/attachments", "")
	defer resp2.Body.Close()
	out.Attachments = nil
	if err := json.NewDecoder(resp2.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if len(out.Attachments) != 0 {
		t.Fatalf("deleted file still listed: %+v", out.Attachments)
	}
}

// A folder upload carries its tree in the "dir" form field. The endpoint is
// where the traversal boundary actually stands, so the attack shapes are
// tested over HTTP, not just against sanitizeRelDir in isolation: a dir that
// escapes must die at the door with 400 and put nothing anywhere.
func TestFolderUploadDirField(t *testing.T) {
	h := newHarness(t, func(int) (llm.Response, error) { return plainTurn() })
	sid := h.createSession()

	// The happy path: csv is in the default allowlist, no prefs needed.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", "data.csv")
	fw.Write([]byte("a,b\n1,2\n"))
	_ = mw.WriteField("dir", "samples/2026")
	mw.Close()
	req, _ := http.NewRequest(http.MethodPost, h.srv.URL+"/api/sessions/"+sid+"/attachments", &buf)
	req.Header.Set("Authorization", "Bearer "+h.token)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := h.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(resp.Body)
		t.Fatalf("nested upload: %d (%s)", resp.StatusCode, bytes.TrimSpace(msg))
	}
	var meta AttachmentMeta
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		t.Fatal(err)
	}
	if meta.Path != "uploads/samples/2026/data.csv" {
		t.Errorf("path = %q, want uploads/samples/2026/data.csv", meta.Path)
	}
	if _, err := os.Stat(filepath.Join(h.session(sid).cwd, "uploads", "samples", "2026", "data.csv")); err != nil {
		t.Errorf("tree missing on disk: %v", err)
	}

	for _, evil := range []string{"..", "../..", "a/../../x", "samples/../..", `..\..`} {
		var b bytes.Buffer
		w := multipart.NewWriter(&b)
		fw, _ := w.CreateFormFile("file", "evil.csv")
		fw.Write([]byte("nope\n"))
		_ = w.WriteField("dir", evil)
		w.Close()
		req, _ := http.NewRequest(http.MethodPost, h.srv.URL+"/api/sessions/"+sid+"/attachments", &b)
		req.Header.Set("Authorization", "Bearer "+h.token)
		req.Header.Set("Content-Type", w.FormDataContentType())
		resp, err := h.srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("dir %q: status %d (%s), want 400 at the door", evil, resp.StatusCode, bytes.TrimSpace(body))
		}
	}
	// Nothing escaped: no evil.csv anywhere under the harness root, and the
	// workspace itself only holds the tree from the happy path.
	var strays []string
	_ = filepath.WalkDir(t.TempDir(), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(d.Name(), "evil.csv") {
			strays = append(strays, p)
		}
		return nil
	})
	if len(strays) > 0 {
		t.Errorf("a refused upload wrote files anyway: %v", strays)
	}
}

// The client_id a message POST carries comes back on the user_message event,
// so an optimistic client can retire its placeholder bubble.
func TestUserMessageEchoesClientID(t *testing.T) {
	h := newHarness(t, func(int) (llm.Response, error) { return plainTurn() })
	sid := h.createSession()

	h.post("/api/sessions/"+sid+"/messages",
		`{"prompt":"hello","client_id":"c-123"}`, http.StatusAccepted)

	waitFor(t, 5*time.Second, func() bool {
		backlog, _, cancel := h.session(sid).Hub().Subscribe(0)
		defer cancel()
		for _, e := range backlog {
			if e.Type == EvUserMessage && e.ClientID == "c-123" {
				return true
			}
		}
		return false
	})
}

// The describe step belongs to the run, not to the POST: the request answers
// while the vision helper is still blocked, the step is announced per image,
// and its result lands in the transcript alt and the describe_end event.
func TestDescribeRunsInsideTheRunNotTheRequest(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	savedName, savedResolve, savedExchange := visionModelName, visionResolve, visionExchange
	visionModelName = func() string { return "eyes" }
	visionResolve = func(string) (config.Resolved, error) {
		return config.Resolved{Model: "eyes", BaseURL: "http://vision.local/v1"}, nil
	}
	visionExchange = func(ctx context.Context, _ llm.Client, _, _ string) (string, error) {
		select {
		case <-release:
			return "蓝色矩形截图", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	t.Cleanup(func() {
		visionModelName, visionResolve, visionExchange = savedName, savedResolve, savedExchange
		unblock()
	})

	h := newHarness(t, func(int) (llm.Response, error) { return plainTurn() })
	sid := h.createSession()
	meta := upload2(t, h, sid, pngBytes2(t, 8, 6), "pic.png")

	// The POST must answer while the helper is still blocked.
	answered := make(chan struct{})
	go func() {
		h.post("/api/sessions/"+sid+"/messages",
			fmt.Sprintf(`{"prompt":"什么颜色","attachments":[%q]}`, meta.Path), http.StatusAccepted)
		close(answered)
	}()
	select {
	case <-answered:
	case <-time.After(3 * time.Second):
		t.Fatal("POST /messages blocked on the describe step")
	}

	// The step is visible while it runs: a start event, no end yet.
	backlog, _, cancel := h.session(sid).Hub().Subscribe(0)
	defer cancel()
	var started, ended bool
	for _, e := range backlog {
		if e.Type == EvImageDescribeStart && e.ImageRef == meta.Path {
			started = true
		}
		if e.Type == EvImageDescribeEnd {
			ended = true
		}
	}
	if !started || ended {
		t.Errorf("mid-describe log: started=%v ended=%v, want start only", started, ended)
	}

	// Letting the helper answer lands the alt in the transcript and the
	// description on the end event.
	unblock()
	waitFor(t, 5*time.Second, func() bool {
		raw, err := os.ReadFile(filepath.Join(h.mgr.cfg.SessionDir, sid+".jsonl"))
		return err == nil && strings.Contains(string(raw), "蓝色矩形截图")
	})
	waitFor(t, 5*time.Second, func() bool {
		backlog2, _, cancel2 := h.session(sid).Hub().Subscribe(0)
		defer cancel2()
		for _, e := range backlog2 {
			if e.Type == EvImageDescribeEnd && e.ImageRef == meta.Path &&
				e.Text == "蓝色矩形截图" && e.Error == "" {
				return true
			}
		}
		return false
	})
}

// A failed describe announces its failure and the run goes on with the
// fallback note, exactly the graceful-degradation shape the card shows.
func TestImageDescribeFailureIsAnnounced(t *testing.T) {
	seams := visionSeams2(t, "", errors.New("helper down"))
	h := newHarness(t, func(int) (llm.Response, error) { return plainTurn() })
	sid := h.createSession()
	meta := upload2(t, h, sid, pngBytes2(t, 8, 6), "pic.png")

	h.post("/api/sessions/"+sid+"/messages",
		fmt.Sprintf(`{"prompt":"什么颜色","attachments":[%q]}`, meta.Path), http.StatusAccepted)

	waitFor(t, 5*time.Second, func() bool {
		backlog, _, cancel := h.session(sid).Hub().Subscribe(0)
		defer cancel()
		for _, e := range backlog {
			if e.Type == EvImageDescribeEnd && e.ImageRef == meta.Path && e.Error != "" {
				return true
			}
		}
		return false
	})
	// The fallback alt still lands in the transcript, and the run still
	// produces its answer — the step failed, the conversation did not.
	waitFor(t, 5*time.Second, func() bool {
		raw, err := os.ReadFile(filepath.Join(h.mgr.cfg.SessionDir, sid+".jsonl"))
		return err == nil && strings.Contains(string(raw), "视觉服务当前不可用") &&
			strings.Contains(string(raw), "ok")
	})
	if *seams != 1 {
		t.Errorf("helper ran %d times, want 1", *seams)
	}
}

// The snapshot is a refresh's only source: the fold must carry the settled
// description on the user message's image block, the same shape the transcript
// has. Without the fold branch the describe card vanished on reload even
// though the run announced it live.
func TestDescribeAltSettlesIntoTheFold(t *testing.T) {
	visionSeams2(t, "蓝色矩形截图", nil)
	h := newHarness(t, func(int) (llm.Response, error) { return plainTurn() })
	sid := h.createSession()
	meta := upload2(t, h, sid, pngBytes2(t, 8, 6), "pic.png")

	h.post("/api/sessions/"+sid+"/messages",
		fmt.Sprintf(`{"prompt":"什么颜色","attachments":[%q]}`, meta.Path), http.StatusAccepted)

	waitFor(t, 5*time.Second, func() bool {
		snap := h.session(sid).Hub().Snapshot()
		for _, m := range snap.Messages {
			if m.Role != string(llm.RoleUser) {
				continue
			}
			for _, b := range m.Content {
				if b.Type == llm.BlockImage && b.ImageRef == meta.Path {
					return b.ImageAlt == "蓝色矩形截图"
				}
			}
		}
		return false
	})
}

// The sidebar derives its title from the transcript's first user message, but
// that append trails the describe step — with a slow helper the list would
// show an untitled session for tens of seconds. The live overlay fills the
// window from the fold, and steps aside once the disk read can derive it.
func TestSidebarTitleOverlaysTheFirstPrompt(t *testing.T) {
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	savedName, savedResolve, savedExchange := visionModelName, visionResolve, visionExchange
	visionModelName = func() string { return "eyes" }
	visionResolve = func(string) (config.Resolved, error) {
		return config.Resolved{Model: "eyes", BaseURL: "http://vision.local/v1"}, nil
	}
	visionExchange = func(ctx context.Context, _ llm.Client, _, _ string) (string, error) {
		select {
		case <-release:
			return "蓝色矩形截图", nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	t.Cleanup(func() {
		visionModelName, visionResolve, visionExchange = savedName, savedResolve, savedExchange
		unblock()
	})

	h := newHarness(t, func(int) (llm.Response, error) { return plainTurn() })
	sid := h.createSession()
	meta := upload2(t, h, sid, pngBytes2(t, 8, 6), "pic.png")
	h.post("/api/sessions/"+sid+"/messages",
		fmt.Sprintf(`{"prompt":"帮我看下这个图片说了什么","attachments":[%q]}`, meta.Path), http.StatusAccepted)

	listedTitle := func() string {
		t.Helper()
		resp := h.do(http.MethodGet, "/api/sessions", "")
		defer resp.Body.Close()
		var out struct {
			Sessions []struct {
				ID    string `json:"id"`
				Title string `json:"title"`
			} `json:"sessions"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		for _, s := range out.Sessions {
			if s.ID == sid {
				return s.Title
			}
		}
		return ""
	}

	// Mid-describe the transcript has no user message yet; the overlay still
	// serves the prompt as the title.
	waitFor(t, 5*time.Second, func() bool {
		return listedTitle() == "帮我看下这个图片说了什么"
	})
	raw, err := os.ReadFile(filepath.Join(h.mgr.cfg.SessionDir, sid+".jsonl"))
	if err == nil && strings.Contains(string(raw), "帮我看下这个图片说了什么") {
		t.Error("the transcript settled before the blocked describe ended; the test proves nothing")
	}

	// After the run the disk derivation takes over with the same value.
	unblock()
	waitFor(t, 5*time.Second, func() bool {
		raw, err := os.ReadFile(filepath.Join(h.mgr.cfg.SessionDir, sid+".jsonl"))
		return err == nil && strings.Contains(string(raw), "帮我看下这个图片说了什么")
	})
	if got := listedTitle(); got != "帮我看下这个图片说了什么" {
		t.Errorf("title after settle = %q, want the derived prompt", got)
	}
}
