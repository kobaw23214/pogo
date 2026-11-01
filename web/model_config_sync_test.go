package web

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/yosukeno/pogo/config"
	"github.com/yosukeno/pogo/llm"
)

func saveModelConfigFixture(t *testing.T, h *harness, models []map[string]any) []string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"file": map[string]any{
		"providers": map[string]any{"kimi": map[string]any{"base_url": "https://updated.example/v1", "key_env": "KIMI_API_KEY"}},
		"models":    models,
	}})
	if err != nil {
		t.Fatal(err)
	}
	response := h.do(http.MethodPut, "/api/config", string(raw))
	defer response.Body.Close()
	var result struct {
		OK       bool     `json:"ok"`
		Warnings []string `json:"warnings"`
	}
	if err := json.NewDecoder(response.Body).Decode(&result); err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || !result.OK {
		t.Fatalf("save returned status=%d result=%+v", response.StatusCode, result)
	}
	return result.Warnings
}

func TestModelConfigSaveRefreshesExistingSessionsWithoutChangingChoicesOrHistory(t *testing.T) {
	t.Setenv(config.PathEnv, filepath.Join(t.TempDir(), "providers.json"))
	var clientsMu sync.Mutex
	var clients []config.Resolved
	h := newHarness(t, scriptedTurns(textTurn("existing answer")), func(c *Config) {
		c.ContextEdit, c.Compact = "auto", "auto"
		c.NewClient = func(cfg config.Resolved, _ func(llm.RetryInfo)) llm.Client {
			clientsMu.Lock()
			clients = append(clients, cfg)
			clientsMu.Unlock()
			return &scriptedClient{model: cfg.Model, respond: func() (llm.Response, error) {
				out := textTurn("existing answer")
				out.Usage = llm.Usage{Input: 1234, Output: 25}
				return out, nil
			}}
		}
	})
	config.SetCatalogForTest(append(slices.Clone(config.Catalog()), config.Model{ID: "manual-choice", Provider: "kimi", ContextWindow: 128000}))
	ordinary := h.session(h.createSession())
	a := postStudioAgent(t, h, studioAgentCapsBody("Keep model choice", "ready", map[string]any{"models": []string{"k3", "manual-choice"}}), http.StatusCreated)
	studio := h.session(startStudioConversation(t, h, a.ID))
	if _, err := studio.SetModel("manual-choice"); err != nil {
		t.Fatal(err)
	}
	for _, s := range []*Session{ordinary, studio} {
		studioAsk(t, h, s.ID, "retain this original exchange")
		s.wait()
	}
	beforeBinding, _ := h.mgr.studio.binding(studio.ID)
	beforeBindingJSON, _ := json.Marshal(beforeBinding)
	before := map[string]Snapshot{}
	histories := map[string][]byte{}
	for _, s := range []*Session{ordinary, studio} {
		before[s.ID] = s.Hub().Snapshot()
		var err error
		histories[s.ID], err = os.ReadFile(s.Path())
		if err != nil {
			t.Fatal(err)
		}
	}
	clientsMu.Lock()
	buildsBefore := len(clients)
	clientsMu.Unlock()
	models := []map[string]any{
		{"id": "k3", "provider": "kimi", "context_window": 512000, "max_tokens": 7777, "vision": true},
		{"id": "manual-choice", "provider": "kimi", "context_window": 256000, "max_tokens": 8888},
	}
	if warnings := saveModelConfigFixture(t, h, models); len(warnings) != 0 {
		t.Fatalf("valid reload warnings=%v", warnings)
	}
	for _, tc := range []struct {
		s      *Session
		model  string
		window int
		output int64
	}{{ordinary, "k3", 512000, 7777}, {studio, "manual-choice", 256000, 8888}} {
		cfg := tc.s.Model()
		if cfg.Model != tc.model || cfg.ContextWindow != tc.window || cfg.MaxTokens != tc.output || cfg.BaseURL != "https://updated.example/v1" {
			t.Fatalf("session retained stale model configuration: %+v", cfg)
		}
		after := tc.s.Hub().Snapshot()
		if after.Run.Model != tc.model || after.Run.ContextWindow != tc.window || after.Run.ModelConfigPending || after.ClearTrigger == before[tc.s.ID].ClearTrigger || tc.s.agent.CompactThreshold() <= int64(before[tc.s.ID].Run.ContextWindow) {
			t.Fatalf("runtime, meter or thresholds did not synchronize: before=%+v after=%+v compact=%d", before[tc.s.ID].Run, after.Run, tc.s.agent.CompactThreshold())
		}
		if !reflect.DeepEqual(after.Messages, before[tc.s.ID].Messages) || !reflect.DeepEqual(after.Usage, before[tc.s.ID].Usage) || after.ContextTokens != before[tc.s.ID].ContextTokens {
			t.Fatal("model reload changed history, accumulated usage or measured occupancy")
		}
		if data, err := os.ReadFile(tc.s.Path()); err != nil || !bytes.Equal(data, histories[tc.s.ID]) {
			t.Fatalf("model reload rewrote transcript: %v", err)
		}
		events, _, cancel := tc.s.Hub().Subscribe(before[tc.s.ID].Seq)
		cancel()
		commits := 0
		for _, event := range events {
			if event.Type != EvModelChanged {
				continue
			}
			commits++
			if event.ContextWindow != tc.window || event.ClearTrigger == nil || *event.ClearTrigger != after.ClearTrigger || event.OverheadTokens == nil || *event.OverheadTokens != after.OverheadTokens {
				t.Fatalf("connected client received stale meter thresholds: event=%+v snapshot=%+v", event, after)
			}
		}
		if commits != 1 {
			t.Fatalf("same-model save emitted %d commit events, want one", commits)
		}
	}
	if !ordinary.Model().SeesImages {
		t.Fatal("same-model vision update was not applied")
	}
	afterBinding, _ := h.mgr.studio.binding(studio.ID)
	afterBindingJSON, _ := json.Marshal(afterBinding)
	if !bytes.Equal(beforeBindingJSON, afterBindingJSON) {
		t.Fatal("catalog update changed Agent binding, allowed models or explicit model choice")
	}
	clientsMu.Lock()
	if len(clients) != buildsBefore+2 || clients[len(clients)-1].BaseURL != "https://updated.example/v1" {
		t.Errorf("same-model clients were not rebuilt from saved settings: builds=%d before=%d", len(clients), buildsBefore)
	}
	clientsMu.Unlock()
	marks := []int64{ordinary.Hub().Seq(), studio.Hub().Seq()}
	saveModelConfigFixture(t, h, models)
	for i, s := range []*Session{ordinary, studio} {
		if s.Hub().Seq() != marks[i] {
			t.Fatal("identical save emitted redundant model events")
		}
	}
	clientsMu.Lock()
	defer clientsMu.Unlock()
	if len(clients) != buildsBefore+2 {
		t.Fatal("identical save unnecessarily rebuilt clients")
	}
}

func TestModelConfigSaveDuringRunDefersUntilFinish(t *testing.T) {
	for _, studio := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "studio"}[studio], func(t *testing.T) {
			t.Setenv(config.PathEnv, filepath.Join(t.TempDir(), "providers.json"))
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(release) })
			h := newHarness(t, func(n int) (llm.Response, error) {
				if n == 1 {
					close(entered)
					<-release
				}
				return textTurn("finished original task"), nil
			})
			var s *Session
			if studio {
				a := createStudioAgent(t, h, "Busy reload", "rules", "ready")
				s = h.session(startStudioConversation(t, h, a.ID))
			} else {
				s = h.session(h.createSession())
			}
			stream := h.start(s.ID, "work before settings save")
			defer stream.close()
			select {
			case <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("model did not begin")
			}
			saveModelConfigFixture(t, h, []map[string]any{{"id": "k3", "provider": "kimi", "context_window": 600000, "max_tokens": 10000}})
			pending := stream.wait(t, EvModelConfigPending)
			if !pending.ModelConfigPending || s.Model().ContextWindow != 200000 || s.Hub().Snapshot().Run.ContextWindow != 200000 || !s.Hub().Snapshot().Run.ModelConfigPending {
				t.Fatal("active run was switched early or reconnect snapshot lost pending state")
			}
			once.Do(func() { close(release) })
			stream.wait(t, EvRunEnd)
			committed := stream.wait(t, EvModelChanged)
			s.wait()
			if committed.Model != "k3" || committed.ContextWindow != 600000 || committed.ClearTrigger == nil || *committed.ClearTrigger != s.Hub().Snapshot().ClearTrigger || committed.OverheadTokens == nil || s.Model().MaxTokens != 10000 || s.Hub().Snapshot().Run.ModelConfigPending {
				t.Fatalf("finish did not enact same-model configuration: %+v", committed)
			}
		})
	}
}

func TestModelConfigSameModelSyncClearsUnknownWindowAndDefersCompaction(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	defer once.Do(func() { close(release) })
	h := newHarness(t, func(n int) (llm.Response, error) {
		if n == 2 {
			close(entered)
			<-release
			return textTurn("Summary: retain original task and constraints."), nil
		}
		return textTurn(strings.Repeat("original answer ", 1000)), nil
	}, func(c *Config) { c.Compact = "auto" })
	a := createStudioAgent(t, h, "Same model settings", "rules", "ready")
	s := h.session(startStudioConversation(t, h, a.ID))
	studioAsk(t, h, s.ID, "remember this request")
	s.wait()
	_, events, cancel := s.Hub().Subscribe(s.Hub().Seq())
	defer cancel()
	completed := make(chan error, 1)
	go func() {
		_, err := s.Compact(context.Background())
		completed <- err
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("manual compaction did not begin")
	}
	config.SetCatalogForTest([]config.Model{{ID: "k3", Provider: "kimi", ContextWindow: 0}})
	result, err := s.SyncStudioModelPolicy()
	if err != nil || !result.Deferred || result.Changed || s.Model().ContextWindow != 200000 || !s.Hub().Snapshot().Run.ModelConfigPending {
		t.Fatalf("compacting same-model sync=%+v error=%v", result, err)
	}
	once.Do(func() { close(release) })
	select {
	case err := <-completed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("manual compaction did not finish")
	}
	if after := s.Hub().Snapshot(); after.Run.ContextWindow != 0 || after.Run.ModelConfigPending || after.ClearTrigger != 0 || s.agent.CompactThreshold() != 0 {
		t.Fatalf("unknown saved context window retained stale limits: run=%+v clear=%d compact=%d", after.Run, after.ClearTrigger, s.agent.CompactThreshold())
	}
	for {
		select {
		case event := <-events:
			if event.Type != EvModelChanged {
				continue
			}
			if event.ContextWindow != 0 || event.ClearTrigger == nil || *event.ClearTrigger != 0 || event.OverheadTokens == nil {
				t.Fatalf("unknown window must explicitly clear connected meter threshold: %+v", event)
			}
			return
		case <-time.After(time.Second):
			t.Fatal("compaction release did not publish saved model configuration")
		}
	}
}

func TestModelConfigSaveRemovedModelWarnsWithoutSwitchingExistingChoice(t *testing.T) {
	t.Setenv(config.PathEnv, filepath.Join(t.TempDir(), "providers.json"))
	h := newHarness(t, idleRespond)
	s := h.session(h.createSession())
	before := s.Model()
	warnings := saveModelConfigFixture(t, h, []map[string]any{{"id": "replacement", "provider": "kimi", "context_window": 900000}})
	if len(warnings) == 0 || !reflect.DeepEqual(s.Model(), before) || s.Hub().Snapshot().Run.Model != "k3" {
		t.Fatalf("missing saved model was silently replaced: warnings=%v run=%+v", warnings, s.Hub().Snapshot().Run)
	}
}
