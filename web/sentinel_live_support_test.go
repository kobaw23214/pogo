package web

// This harness is deliberately separate from the scripted web test harness:
// its clients use the operator-supplied provider catalog and real network calls.
// It never loads an existing Agent, workspace, browser, or production database.
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yosukeno/pogo/config"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/mcpobserve"
)

const (
	sentinelLiveMaxCalls  = 40
	sentinelLiveMaxInput  = 500_000
	sentinelLiveMaxOutput = 64_000
)

type sentinelLiveHarness struct {
	mgr         *Manager
	server      *Server
	creator     *Session
	capture     *sentinelLiveCapture
	root        string
	sessionDir  string
	artifactDir string
	model       string
}

type sentinelLiveCall struct {
	Number       int              `json:"number"`
	Model        string           `json:"model"`
	StartedAt    string           `json:"started_at"`
	DurationMS   int64            `json:"duration_ms"`
	FirstDeltaMS int64            `json:"first_delta_ms"`
	LastDeltaMS  int64            `json:"last_delta_ms"`
	DeltaCount   int64            `json:"delta_count"`
	System       string           `json:"system"`
	History      []llm.Message    `json:"history"`
	Tools        []llm.ToolSchema `json:"tools"`
	Response     llm.Message      `json:"response"`
	StopReason   llm.StopReason   `json:"stop_reason"`
	Usage        llm.Usage        `json:"usage"`
	Error        string           `json:"error,omitempty"`
}

type sentinelLiveToolCall struct {
	Call  int             `json:"call"`
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

type sentinelLiveCapture struct {
	mu      sync.Mutex
	calls   []sentinelLiveCall
	started int
	usage   llm.Usage
	secrets []string
	output  string
}

type sentinelLiveClient struct {
	actual  llm.Client
	capture *sentinelLiveCapture
}

func newSentinelLiveHarness(t *testing.T, model string) *sentinelLiveHarness {
	t.Helper()
	if os.Getenv("POGO_SENTINEL_LIVE") != "1" {
		t.Skip("real-provider sentinel acceptance requires POGO_SENTINEL_LIVE=1")
	}
	if _, err := config.Load(); err != nil {
		t.Fatalf("load live provider catalog: %v", err)
	}
	if model == "" {
		model = os.Getenv("POGO_SENTINEL_LIVE_MODEL")
	}
	if model == "" {
		model = config.DefaultModel()
	}
	resolved, err := config.Resolve(model)
	if err != nil {
		t.Fatalf("resolve live model %q: %v", model, err)
	}
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := &sentinelLiveHarness{root: filepath.Join(base, "workspace"), sessionDir: filepath.Join(base, "sessions"), model: model}
	h.artifactDir = filepath.Join(base, "evidence")
	if out := os.Getenv("POGO_SENTINEL_LIVE_OUT"); out != "" {
		h.artifactDir = filepath.Join(out, sentinelLiveFileName(t.Name()))
	}
	for _, dir := range []string{h.root, h.sessionDir, h.artifactDir} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	h.capture = &sentinelLiveCapture{output: h.artifactDir}
	h.capture.addSecrets(resolved)
	// A live provider environment may carry unrelated service variables. Office
	// must stay disabled, without contacting or loading any running editor.
	for _, name := range []string{"POGO_OFFICE_BROWSER_URL", "POGO_OFFICE_SERVICE_URL", "POGO_OFFICE_PUBLIC_URL", "POGO_OFFICE_FETCH_URL", "POGO_OFFICE_JWT_SECRET_FILE"} {
		t.Setenv(name, "")
	}
	h.mgr, err = NewManager(Config{
		Cwd: h.root, SessionDir: h.sessionDir, Model: model,
		MaxTurns: 24, Retries: -1, GateTimeout: 3 * time.Second,
		RunTimeout: 10 * time.Minute, IdleTimeout: 6 * time.Minute,
		NewClient: func(cfg config.Resolved, onRetry func(llm.RetryInfo)) llm.Client {
			h.capture.addSecrets(cfg)
			maxTokens := cfg.MaxTokens
			if maxTokens <= 0 || maxTokens > 16384 {
				maxTokens = 16384
			}
			actual := llm.New(llm.Options{
				Compatibility: cfg.Compatibility, BaseURL: cfg.BaseURL,
				APIKey: cfg.APIKey, Model: cfg.Model, MaxTokens: maxTokens,
				Headers: cfg.Headers, ExtraBody: cfg.ExtraBody,
				Timeout: 6 * time.Minute, MaxRetries: -1, OnRetry: onRetry,
			})
			return &sentinelLiveClient{actual: actual, capture: h.capture}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// Drain/cancel every model stream before saving its final delta counts.
		h.mgr.Close()
		if h.server != nil {
			h.server.Close()
		}
		calls := h.capture.Snapshot()
		usageComplete := len(calls) == h.capture.Count()
		for _, call := range calls {
			if call.Error != "" || call.Usage.Input <= 0 || call.StopReason == "" {
				usageComplete = false
			}
		}
		h.evidence(t, "model-trace", calls)
		h.evidence(t, "model-budget", map[string]any{"calls": h.capture.Count(), "usage": h.capture.Usage(), "usage_complete": usageComplete, "usage_scope": "provider-reported usage; cancelled requests may be unreported", "max_calls": sentinelLiveMaxCalls, "max_input": sentinelLiveMaxInput, "max_output": sentinelLiveMaxOutput})
	})
	h.server, err = NewServer(h.mgr, ServerOptions{
		Token: "isolated-sentinel-live-token", PrefsStore: filepath.Join(base, "prefs.json"),
		SharesStore: filepath.Join(base, "shares.json"), StartersStore: filepath.Join(base, "starters.json"),
		MCPClientStore: filepath.Join(base, "mcp.json"),
	})
	if err != nil {
		t.Fatal(err)
	}
	empty := []string{}
	selected := []string{"bash", "write", "edit", "watch", "watch_test"}
	a, err := h.mgr.studio.put("", studioInput{
		Name: "Isolated live sentinel", Model: model, Status: "ready",
		Instructions: "Complete the user's request using the available tools. Report only verified outcomes. Do not delegate to other agents.",
		Skills:       &empty, MCPServers: &empty, Tools: &selected,
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	h.creator, err = h.mgr.createStudioConversation(a, nil)
	if err != nil {
		t.Fatal(err)
	}
	// This isolated creator may author/test scripts. Event-triggered workers
	// retain the product's Strict policy; this grant is not inherited by them.
	h.creator.SetPolicy(ModeAuto, 0)
	h.evidence(t, "environment", map[string]any{"model_alias": model, "model": resolved.Model, "agent_id": a.ID, "creator_session": h.creator.ID, "workspace": h.creator.cwd, "real_provider": true, "delegation": "disabled for bounded live acceptance"})
	return h
}

func (h *sentinelLiveHarness) run(t *testing.T, prompt string, timeout time.Duration) error {
	return h.runSession(t, h.creator, prompt, timeout)
}

func (h *sentinelLiveHarness) runSession(t *testing.T, sess *Session, prompt string, timeout time.Duration) error {
	t.Helper()
	if timeout <= 0 || timeout > 10*time.Minute {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	var active *activeRun
	receipt := &scheduleRunReceipt{}
	id, err := sess.startPreparedInput(prompt, nil, nil, "", "", mcpobserve.Metadata{}, "", ctx, nil, func(run *activeRun) {
		active = run
		run.observe = receipt.observe
	})
	if err != nil {
		return err
	}
	if active == nil {
		return errors.New("live run did not expose its completion handle")
	}
	select {
	case <-active.done:
	case <-ctx.Done():
		active.cancel()
		select {
		case <-active.done:
		case <-time.After(5 * time.Second):
			return errors.New("live run did not drain after its time budget expired")
		}
		return ctx.Err()
	}
	h.evidence(t, "run-"+id, map[string]any{"session_id": sess.ID, "prompt": prompt, "run_id": id, "ended": receipt.ended, "end_reason": receipt.endReason, "error": receipt.err, "summary": receipt.summary})
	if active.persistErr != nil {
		return fmt.Errorf("live run persistence: %w", active.persistErr)
	}
	if !receipt.ended || receipt.endReason != "completed" || receipt.err != "" {
		return fmt.Errorf("live run ended %q: %s", receipt.endReason, receipt.err)
	}
	return nil
}

func (c *sentinelLiveCapture) addSecrets(cfg config.Resolved) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if cfg.APIKey != "" {
		c.secrets = append(c.secrets, cfg.APIKey)
	}
	for name, value := range cfg.Headers {
		key := strings.ToLower(name)
		if value != "" && (strings.Contains(key, "auth") || strings.Contains(key, "key") || strings.Contains(key, "token") || strings.Contains(key, "secret")) {
			c.secrets = append(c.secrets, value)
		}
	}
}

func (c *sentinelLiveCapture) sanitizedJSON(value any) ([]byte, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	secrets := append([]string(nil), c.secrets...)
	c.mu.Unlock()
	text := string(data)
	for _, secret := range secrets {
		encoded, _ := json.Marshal(secret)
		text = strings.ReplaceAll(text, string(encoded[1:len(encoded)-1]), "[redacted]")
	}
	return []byte(text), nil
}

func (c *sentinelLiveCapture) Count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.started
}

func (c *sentinelLiveCapture) Usage() llm.Usage {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.usage
}

func (c *sentinelLiveCapture) Snapshot() []sentinelLiveCall {
	c.mu.Lock()
	calls := append([]sentinelLiveCall(nil), c.calls...)
	c.mu.Unlock()
	data, err := c.sanitizedJSON(calls)
	if err != nil {
		return nil
	}
	var copy []sentinelLiveCall
	_ = json.Unmarshal(data, &copy)
	return copy
}

func (c *sentinelLiveCapture) ToolCalls() []sentinelLiveToolCall {
	var result []sentinelLiveToolCall
	for _, call := range c.Snapshot() {
		for _, block := range call.Response.Content {
			if block.Type == llm.BlockToolUse {
				result = append(result, sentinelLiveToolCall{Call: call.Number, ID: block.ID, Name: block.Name, Input: block.Input})
			}
		}
	}
	return result
}

func (c *sentinelLiveClient) Model() string { return c.actual.Model() }

func (c *sentinelLiveClient) Stream(ctx context.Context, system string, history []llm.Message, schemas []llm.ToolSchema, onDelta func(llm.Delta)) (llm.Response, error) {
	c.capture.mu.Lock()
	if c.capture.started >= sentinelLiveMaxCalls || c.capture.usage.Input >= sentinelLiveMaxInput || c.capture.usage.Output >= sentinelLiveMaxOutput {
		c.capture.mu.Unlock()
		return llm.Response{}, errors.New("live acceptance model budget exhausted")
	}
	c.capture.started++
	number := c.capture.started
	c.capture.mu.Unlock()
	// Subagents use an external process and could bypass this recorder's shared
	// request budget. All acceptance scenarios are deliberately single-agent.
	allowed := make([]llm.ToolSchema, 0, len(schemas))
	for _, schema := range schemas {
		if schema.Name != "subagent" {
			allowed = append(allowed, schema)
		}
	}
	started := time.Now()
	record := sentinelLiveCall{Number: number, Model: c.Model(), StartedAt: started.UTC().Format(time.RFC3339Nano), FirstDeltaMS: -1, LastDeltaMS: -1, System: system, History: history, Tools: allowed}
	// Clone before streaming; the session may append history after this call.
	data, _ := json.Marshal(record)
	_ = json.Unmarshal(data, &record)
	var deltaMu sync.Mutex
	response, err := c.actual.Stream(ctx, system, history, allowed, func(delta llm.Delta) {
		elapsed := time.Since(started).Milliseconds()
		deltaMu.Lock()
		if record.DeltaCount == 0 {
			record.FirstDeltaMS = elapsed
		}
		record.LastDeltaMS = elapsed
		record.DeltaCount++
		deltaMu.Unlock()
		if onDelta != nil {
			onDelta(delta)
		}
	})
	deltaMu.Lock()
	defer deltaMu.Unlock()
	for _, block := range response.Message.Content {
		if block.Type == llm.BlockToolUse && block.Name == "subagent" {
			err = errors.New("live acceptance forbids delegation outside its shared model budget")
		}
	}
	record.DurationMS, record.Response, record.StopReason, record.Usage = time.Since(started).Milliseconds(), response.Message, response.StopReason, response.Usage
	if err != nil {
		record.Error = err.Error()
	}
	c.capture.mu.Lock()
	c.capture.calls = append(c.capture.calls, record)
	c.capture.usage.Input += response.Usage.Input
	c.capture.usage.Output += response.Usage.Output
	c.capture.usage.CacheRead += response.Usage.CacheRead
	c.capture.usage.Reasoning += response.Usage.Reasoning
	c.capture.mu.Unlock()
	if raw, marshalErr := c.capture.sanitizedJSON(record); marshalErr == nil {
		_ = os.WriteFile(filepath.Join(c.capture.output, fmt.Sprintf("model-call-%02d.json", number)), raw, 0600)
	}
	if err != nil {
		// Raw provider errors can echo credentials; preserve them only in the
		// sanitized evidence and do not forward them into model history/logs.
		return llm.Response{}, errors.New("live provider call failed; inspect sanitized model evidence")
	}
	return response, nil
}

func (h *sentinelLiveHarness) evidence(t *testing.T, label string, value any) {
	t.Helper()
	data, err := h.capture.sanitizedJSON(value)
	if err != nil {
		t.Errorf("marshal live evidence %q: %v", label, err)
		return
	}
	if err := os.WriteFile(filepath.Join(h.artifactDir, sentinelLiveFileName(label)+".json"), data, 0600); err != nil {
		t.Errorf("write live evidence %q: %v", label, err)
	}
}

func sentinelLiveFileName(value string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, value)
}
