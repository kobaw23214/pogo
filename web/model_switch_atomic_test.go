package web

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yosukeno/pogo/config"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/skills"
)

type modelSwitchCapture struct {
	mu          sync.Mutex
	builds      int
	model, wire string
	blockModel  string
	entered     chan struct{}
	release     chan struct{}
}

type modelSwitchClient struct {
	model string
	cap   *modelSwitchCapture
}

func (c *modelSwitchCapture) buildCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.builds
}

func (c *modelSwitchClient) Model() string { return c.model }
func (c *modelSwitchClient) Stream(_ context.Context, _ string, _ []llm.Message, schemas []llm.ToolSchema, _ func(llm.Delta)) (llm.Response, error) {
	c.cap.mu.Lock()
	defer c.cap.mu.Unlock()
	c.cap.model, c.cap.wire = c.model, ""
	for _, schema := range schemas {
		if schema.Name == "browser" {
			raw, _ := llm.ToolsWireJSON([]llm.ToolSchema{schema})
			c.cap.wire = string(raw)
		}
	}
	return plainTurn()
}

func newModelSwitchHarness(t *testing.T, dir string) (*harness, *modelSwitchCapture) {
	t.Helper()
	previousVision := visionModelName
	visionModelName = func() string { return "" }
	t.Cleanup(func() { visionModelName = previousVision })
	cap := &modelSwitchCapture{}
	h := newHarness(t, func(int) (llm.Response, error) { return plainTurn() }, func(c *Config) {
		c.SessionDir = dir
		dirs, err := skills.MaterializeEmbedded(filepath.Join(dir, "embedded-skills"))
		if err != nil {
			t.Fatal(err)
		}
		list, diagnostics := skills.Load(skills.Options{Cwd: dir, UserDir: filepath.Join(dir, "user-skills"), Embedded: dirs})
		if len(diagnostics) > 0 {
			t.Fatal(diagnostics)
		}
		for _, sk := range list {
			if sk.Name == "browser-use" {
				c.Skills = append(c.Skills, sk)
			}
		}
		if len(c.Skills) != 1 {
			t.Fatal("trusted browser-use Skill is unavailable")
		}
		c.BrowserURL, c.BrowserToken = "http://127.0.0.1:9", "model-switch-test-only"
		c.NewClient = func(cfg config.Resolved, _ func(llm.RetryInfo)) llm.Client {
			cap.mu.Lock()
			cap.builds++
			block, entered, release := cfg.Model == cap.blockModel, cap.entered, cap.release
			cap.mu.Unlock()
			if block {
				close(entered)
				<-release
			}
			return &modelSwitchClient{model: cfg.Model, cap: cap}
		}
	})
	no, yes := false, true
	config.SetCatalogForTest([]config.Model{
		{ID: "k3", Provider: "kimi", ContextWindow: 200000, Vision: &no},
		{ID: "switch-visual", Provider: "kimi", ContextWindow: 400000, Vision: &yes},
		{ID: "switch-text", Provider: "kimi", ContextWindow: 300000, Vision: &no},
	})
	return h, cap
}

func createModelSwitchSession(t *testing.T, h *harness) (studioAgent, *Session) {
	t.Helper()
	skillID := ""
	for _, sk := range h.mgr.Skills() {
		if sk.Name == "browser-use" {
			skillID = skillIDOf(sk)
		}
	}
	if skillID == "" {
		t.Fatal("trusted browser-use Skill is unavailable")
	}
	a := postStudioAgent(t, h, studioAgentCapsBody("Atomic switch", "ready", map[string]any{
		"models":              []string{"k3", "switch-visual", "switch-text"},
		"skills":              []string{skillID},
		"tools":               []string{"browser"},
		"skill_tool_bindings": []string{"browser-use"},
	}), http.StatusCreated)
	return a, h.session(startStudioConversation(t, h, a.ID))
}

func assertModelSwitchViews(t *testing.T, h *harness, cap *modelSwitchCapture, aid string, s *Session, want string) {
	t.Helper()
	if got := s.Model().Model; got != want {
		t.Fatalf("session model = %q, want %q", got, want)
	}
	binding, ok := h.mgr.studio.binding(s.ID)
	if !ok || binding.currentModel() != want {
		t.Fatalf("binding model = %q, want %q", binding.currentModel(), want)
	}
	if got := sessionCapabilities(t, h, aid, s.ID)["model"]; got != want {
		t.Fatalf("capabilities model = %v, want %q", got, want)
	}
	if got := s.Hub().Snapshot().Run.Model; got != want {
		t.Fatalf("hub model = %q, want %q", got, want)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for range s.agent.Run(ctx, "Return without calling any tool") {
	}
	cap.mu.Lock()
	defer cap.mu.Unlock()
	if cap.model != want || cap.wire == "" || strings.Contains(cap.wire, `"screenshot"`) != (want == "switch-visual") {
		t.Fatalf("client/browser declaration disagrees with model: client=%q want=%q screenshot=%v", cap.model, want, strings.Contains(cap.wire, `"screenshot"`))
	}
}

func TestStudioModelSwitchRejectedStateDoesNotPersist(t *testing.T) {
	for _, state := range []string{"active", "compacting", "closing"} {
		t.Run(state, func(t *testing.T) {
			dir := t.TempDir()
			h, cap := newModelSwitchHarness(t, dir)
			a, s := createModelSwitchSession(t, h)
			before, err := os.ReadFile(h.mgr.studio.path)
			if err != nil {
				t.Fatal(err)
			}
			seq, built := s.hub.Seq(), cap.buildCount()
			resetState := func() {
				s.mu.Lock()
				s.active, s.compacting, s.closing = nil, false, false
				s.mu.Unlock()
			}
			defer resetState()
			s.mu.Lock()
			s.active, s.compacting, s.closing = nil, state == "compacting", state == "closing"
			if state == "active" {
				s.active = &activeRun{}
			}
			s.mu.Unlock()
			if state == "closing" {
				if _, err := s.SetModel("switch-visual"); !errors.Is(err, ErrNotFound) {
					t.Fatalf("closed switch: %v", err)
				}
			} else {
				h.post("/api/sessions/"+s.ID+"/control", `{"action":"set_model","model":"switch-visual"}`, http.StatusConflict)
			}
			resetState()
			after, err := os.ReadFile(h.mgr.studio.path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) || s.hub.Seq() != seq || cap.buildCount() != built {
				t.Fatal("rejected switch changed persistence, client or events")
			}
			assertModelSwitchViews(t, h, cap, a.ID, s, "k3")
			h.mgr.Close()
			h2, cap2 := newModelSwitchHarness(t, dir)
			assertModelSwitchViews(t, h2, cap2, a.ID, h2.session(s.ID), "k3")
		})
	}
}

func TestStudioModelSwitchWriteFailureLeavesAllViewsUnchanged(t *testing.T) {
	dir := t.TempDir()
	h, cap := newModelSwitchHarness(t, dir)
	a, s := createModelSwitchSession(t, h)
	before, err := os.ReadFile(h.mgr.studio.path)
	if err != nil {
		t.Fatal(err)
	}
	seq, built, overhead := s.hub.Seq(), cap.buildCount(), s.agent.OverheadTokens()
	// Fail the final atomic rename, independently of filesystem permissions.
	path, backup := h.mgr.studio.path, h.mgr.studio.path+".test-backup"
	if err := os.Rename(path, backup); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	restore := func() {
		once.Do(func() {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				t.Error(err)
			}
			if err := os.Rename(backup, path); err != nil {
				t.Error(err)
			}
		})
	}
	defer restore()
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetModel("switch-visual"); err == nil {
		t.Fatal("expected binding write failure")
	}
	if s.hub.Seq() != seq || cap.buildCount() != built || s.agent.OverheadTokens() != overhead {
		t.Fatal("failed write changed events, client or browser schema accounting")
	}
	assertModelSwitchViews(t, h, cap, a.ID, s, "k3")
	restore()
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("failed switch changed the durable binding")
	}
	h.mgr.Close()
	h2, cap2 := newModelSwitchHarness(t, dir)
	assertModelSwitchViews(t, h2, cap2, a.ID, h2.session(s.ID), "k3")
}

func TestStudioModelSwitchSerializesBindingRuntimeAndEvents(t *testing.T) {
	dir := t.TempDir()
	h, cap := newModelSwitchHarness(t, dir)
	a, s := createModelSwitchSession(t, h)
	cap.mu.Lock()
	cap.blockModel, cap.entered, cap.release = "switch-text", make(chan struct{}), make(chan struct{})
	cap.mu.Unlock()
	var once sync.Once
	release := func() { once.Do(func() { close(cap.release) }) }
	defer release()
	first, second := make(chan error, 1), make(chan error, 1)
	go func() { _, err := s.SetModel("switch-text"); first <- err }()
	select {
	case <-cap.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("first switch did not reach its commit")
	}
	go func() { _, err := s.SetModel("switch-visual"); second <- err }()
	// While the first switch owns the session, a second cannot publish a newer
	// binding ahead of its runtime. The old implementation writes it here.
	deadline := time.Now().Add(100 * time.Millisecond)
	for time.Now().Before(deadline) {
		binding, _ := h.mgr.studio.binding(s.ID)
		if binding.currentModel() != "switch-text" {
			t.Fatal("second switch persisted before acquiring the session")
		}
		time.Sleep(time.Millisecond)
	}
	release()
	for _, done := range []chan error{first, second} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("switch did not complete")
		}
	}
	assertModelSwitchViews(t, h, cap, a.ID, s, "switch-visual")
	h.mgr.Close()
	h2, cap2 := newModelSwitchHarness(t, dir)
	assertModelSwitchViews(t, h2, cap2, a.ID, h2.session(s.ID), "switch-visual")
}
