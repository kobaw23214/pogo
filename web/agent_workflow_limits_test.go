package web

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	af "github.com/yosukeno/pogo/agentworkflow"
	"github.com/yosukeno/pogo/config"
	"github.com/yosukeno/pogo/llm"
)

func TestAgentWorkflowExpandedDefinitionBudgetAndCapabilities(t *testing.T) {
	h := newMultiuserHarness(t, nil)
	a := awTestAgent(t, h, "capacity-owner")
	caps := awTestValue[agentWorkflowCaps](t, awTestCall(t, h, aliceToken, a.ID, "read", map[string]any{"action": "capabilities"}, 200), "capabilities")
	if caps.MaxSteps != 200 || caps.MaxParallelNodes != 10 {
		t.Fatalf("capacity discovery does not match runtime: %+v", caps)
	}
	steps := make([]af.Step, 200)
	for i := range steps {
		steps[i] = af.Step{ID: fmt.Sprintf("node_%03d", i), Kind: "agent", Task: strings.Repeat("x", 8192)}
	}
	// This exceeds the former 1 MiB HTTP/store budget, not merely 30 nodes.
	d := awTestValue[af.Definition](t, awTestCall(t, h, aliceToken, a.ID, "edit", map[string]any{"action": "create", "request_id": "large-definition", "name": "大图保存", "steps": steps}, 200), "workflow")
	if len(d.Steps) != 200 {
		t.Fatal("large saved definition was truncated", len(d.Steps))
	}
	steps = append(steps, af.Step{ID: "extra", Kind: "agent", Task: "第201个节点"})
	awTestCall(t, h, aliceToken, a.ID, "edit", map[string]any{"action": "create", "request_id": "too-many-definition", "name": "超过容量", "steps": steps}, 409)
	retained := awTestValue[af.Definition](t, awTestCall(t, h, aliceToken, a.ID, "read", map[string]any{"action": "get", "workflow_id": d.ID}, 200), "workflow")
	if retained.DefinitionHash != d.DefinitionHash || retained.Revision != d.Revision {
		t.Fatal("rejected oversized definition modified existing data")
	}
}

type awCapacityModel struct {
	mu           sync.Mutex
	started      chan string
	release      chan struct{}
	active, peak int
}

type awCapacityClient struct{ shared *awCapacityModel }

func (c *awCapacityClient) Model() string { return "k3" }
func (c *awCapacityClient) Stream(ctx context.Context, _ string, history []llm.Message, _ []llm.ToolSchema, _ func(llm.Delta)) (llm.Response, error) {
	var text string
	for _, message := range history {
		for _, block := range message.Content {
			text += block.Text
		}
	}
	match := awGraphTestStep.FindStringSubmatch(text)
	if len(match) != 2 {
		return llm.Response{}, fmt.Errorf("no real node prompt")
	}
	m := c.shared
	m.mu.Lock()
	m.active++
	m.peak = max(m.peak, m.active)
	m.mu.Unlock()
	defer func() { m.mu.Lock(); m.active--; m.mu.Unlock() }()
	m.started <- match[1]
	select {
	case <-m.release:
	case <-ctx.Done():
		return llm.Response{}, ctx.Err()
	}
	return textTurn("独立并发测试节点完成"), nil
}

func TestAgentWorkflowGraphRunsTenAgentNodesConcurrentlyAndQueuesTheEleventh(t *testing.T) {
	model := &awCapacityModel{started: make(chan string, 11), release: make(chan struct{})}
	h := newMultiuserHarness(t, nil, func(c *Config) {
		c.NewClient = func(config.Resolved, func(llm.RetryInfo)) llm.Client { return &awCapacityClient{shared: model} }
	})
	var release sync.Once
	defer release.Do(func() { close(model.release) })
	a := awTestAgent(t, h, "ten-parallel-owner")
	source := h.studioConversationAs(aliceToken, a.ID)
	d := af.Definition{Name: "十并发与第十一项排队", Steps: []af.Step{{ID: "fork", Kind: "parallel"}, {ID: "join", Kind: "join"}}, Graph: &af.Graph{Version: 1, Edges: []af.Edge{{Source: af.GraphInput, Target: "fork"}, {Source: "join", Target: af.GraphOutput}}}}
	for i := 0; i < 11; i++ {
		id := fmt.Sprintf("worker_%02d", i)
		d.Steps = append(d.Steps, af.Step{ID: id, Kind: "agent", Title: id, Task: "处理独立测试输入"})
		d.Graph.Edges = append(d.Graph.Edges, af.Edge{Source: "fork", Target: id}, af.Edge{Source: id, Target: "join"})
	}
	d = awTestValue[af.Definition](t, awTestCall(t, h, aliceToken, a.ID, "edit", map[string]any{"action": "create", "request_id": "ten-create", "name": d.Name, "steps": d.Steps, "graph": d.Graph}, 200), "workflow")
	run := awTestValue[af.Run](t, awTestCall(t, h, aliceToken, a.ID, "run", map[string]any{"action": "trial", "workflow_id": d.ID, "request_id": "ten-trial", "expected_revision": d.Revision, "expected_hash": d.DefinitionHash, "source_session_id": source}, 200), "run")
	started := map[string]bool{}
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for len(started) < 10 {
		select {
		case id := <-model.started:
			if started[id] {
				t.Fatal("same node was dispatched twice", id)
			}
			started[id] = true
		case <-deadline.C:
			t.Fatal("ten real model node sessions did not overlap", len(started))
		}
	}
	select {
	case id := <-model.started:
		t.Fatal("eleventh node exceeded the concurrent execution limit", id)
	case <-time.After(200 * time.Millisecond):
	}
	active := awTestValue[af.Run](t, awTestCall(t, h, aliceToken, a.ID, "read", map[string]any{"action": "run", "workflow_id": d.ID, "run_id": run.ID}, 200), "run")
	count, sessions := 0, map[string]bool{}
	for _, step := range active.Steps {
		if step.Status == af.Running {
			count++
			if step.SessionID == "" || sessions[step.SessionID] {
				t.Fatal("concurrent nodes share or lack an execution session")
			}
			sessions[step.SessionID] = true
		}
	}
	if count != 10 {
		t.Fatal("persisted running nodes do not match the enforced capacity", count)
	}
	release.Do(func() { close(model.release) })
	done := awTestRun(t, h, aliceToken, a.ID, d.ID, run.ID, af.Completed)
	model.mu.Lock()
	peak := model.peak
	model.mu.Unlock()
	if peak != 10 || len(done.Steps) != 13 {
		t.Fatal("wrong peak concurrency or truncated history", peak, len(done.Steps))
	}
	for _, step := range done.Steps {
		if step.Status != af.Completed {
			t.Fatal("queued node did not run after capacity was released", step)
		}
	}
}
