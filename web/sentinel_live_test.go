package web

// These opt-in acceptance cases use a real configured model and the real Linux
// script sandbox. The user prompts intentionally do not supply Pogo frames,
// collector code, or tool-call sequences: authoring is part of the acceptance.
import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/sentinel"
	"github.com/yosukeno/pogo/sentinelsdk"
)

func sentinelLiveModel() string { return os.Getenv("POGO_SENTINEL_LIVE_MODEL") }

func sentinelLiveChecks(source *sentinelLiveSource) int {
	stats := source.Stats()
	return stats.StatusRequests + stats.FollowRequests
}

func sentinelLiveSourceDescription(s *sentinelLiveSource) string {
	return fmt.Sprintf(`测试源是任意自定义 TCP 服务，不是 HTTP。在 %s 建立连接，发送一行 STATUS %s 加换行，会返回一行 JSON 后关闭；发 FOLLOW %s 加换行，则先收到历史记录，随后持续收到新记录的 JSON 行。FOLLOW 还支持在 token 后加空格和最后收到的 id，用于断线重连后只读取该 id 之后的记录。记录包含 id（不可变观察的唯一标识）、sequence（递增序号）、severity、status、acked（布尔）、text。网络访问仅用于这个测试源。`, s.Address(), s.Token(), s.Token())
}

func sentinelLiveAwait(t *testing.T, timeout time.Duration, description string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", description)
}

func sentinelLiveWatches(t *testing.T, h *sentinelLiveHarness) []sentinel.Watch {
	t.Helper()
	db, err := h.mgr.sentinels.database(true)
	if err != nil {
		t.Fatal(err)
	}
	watches, err := db.ListAll()
	if err != nil {
		t.Fatal(err)
	}
	return watches
}

func sentinelLiveBatches(t *testing.T, h *sentinelLiveHarness, id string) []sentinel.Batch {
	t.Helper()
	db, err := h.mgr.sentinels.database(true)
	if err != nil {
		t.Fatal(err)
	}
	batches, err := db.ListBatches(id, 100)
	if err != nil {
		t.Fatal(err)
	}
	return batches
}

func sentinelLiveCreatePaused(t *testing.T, h *sentinelLiveHarness, source *sentinelLiveSource, goal string) sentinel.Watch {
	t.Helper()
	t.Cleanup(func() {
		h.evidence(t, "source", source.Stats())
		h.evidence(t, "watches", sentinelLiveWatches(t, h))
		for _, w := range sentinelLiveWatches(t, h) {
			h.evidence(t, "batches-"+w.Spec.ID, sentinelLiveBatches(t, h, w.Spec.ID))
		}
	})
	prompt := "请为我建立一个通用脚本哨兵。" + goal + "\n" + sentinelLiveSourceDescription(source) + `
请自己编写最小可用脚本并在现有服务实际试跑验证，处理错误后再注册；不需要搭建额外测试服务器或大型测试框架，也不要长时间空等。现在只准备并注册为暂停状态，不要开始长期监听，等我下一条消息明确同意后再启动。长期目标和需要的上下文要保存到哨兵委托中，确保后续独立醒来还能执行。只在 Pogo 站内报告，报告应列出观察 id 和 text、解释触发原因；不发送外部消息、不修改任何外部数据。不要创建定时任务或子 Agent。本次只建立一个哨兵。`
	if err := h.run(t, prompt, 10*time.Minute); err != nil {
		t.Fatalf("real-model preparation failed: %v", err)
	}
	// Validate the entire preparation phase, not only its final database state.
	tests, testOK, created := map[string]bool{}, false, false
	for _, call := range h.capture.ToolCalls() {
		if call.Name == "watch_test" {
			tests[call.ID] = true
		}
		if call.Name == "watch" {
			var req struct {
				Action  string `json:"action"`
				Enabled bool   `json:"enabled"`
			}
			_ = json.Unmarshal(call.Input, &req)
			if req.Action == "resume" || req.Enabled {
				t.Fatal("model activated collection before the user authorized it")
			}
			created = created || req.Action == "create"
		}
	}
	for _, call := range h.capture.Snapshot() {
		for _, msg := range call.History {
			for _, block := range msg.Content {
				if block.Type == llm.BlockToolResult && tests[block.ToolUseID] && !block.IsError {
					testOK = true
				}
			}
		}
	}
	if !created || !testOK {
		t.Fatal("model did not use watch_test successfully before registration")
	}
	watches := sentinelLiveWatches(t, h)
	if len(watches) != 1 {
		t.Fatalf("expected exactly one registration, got %d", len(watches))
	}
	w := watches[0]
	if os.Getenv("POGO_SENTINEL_LIVE_REQUIRE_SDK") == "1" {
		prepared := false
		for _, call := range h.capture.ToolCalls() {
			if call.Name != "watch" {
				continue
			}
			var action struct {
				Action string `json:"action"`
			}
			_ = json.Unmarshal(call.Input, &action)
			prepared = prepared || action.Action == "prepare"
		}
		var bundle sentinelBundle
		manifest, err := os.ReadFile(filepath.Join(w.Spec.ScriptDir, ".pogo-watch-manifest.json"))
		if err != nil || json.Unmarshal(manifest, &bundle) != nil || !prepared || bundle.SDKVersion != sentinelsdk.Version {
			t.Fatalf("SDK acceptance did not prepare and seal the platform helper: prepared=%v version=%q err=%v", prepared, bundle.SDKVersion, err)
		}
		if len(w.Spec.Command) < 3 {
			t.Fatal("SDK acceptance expected a Python entry in the sealed bundle")
		}
		entry, err := os.ReadFile(w.Spec.Command[2])
		if err != nil || !strings.Contains(string(entry), "pogo_watch") {
			t.Fatal("SDK acceptance entry did not import the prepared protocol helper", err)
		}
		h.evidence(t, "sdk-bundle", map[string]any{"sdk_version": bundle.SDKVersion, "files": bundle.Files, "entry_bytes": len(entry), "entry_lines": strings.Count(string(entry), "\n")})
	}
	if w.Spec.Enabled || w.Pending != 0 || len(sentinelLiveBatches(t, h, w.Spec.ID)) != 0 {
		t.Fatalf("registration did not remain paused: %+v", w)
	}
	if source.Stats().Connections == 0 {
		t.Fatal("model did not actually validate its collector against the source")
	}
	h.evidence(t, "prepared", w)
	before := source.Stats()
	time.Sleep(4 * time.Second)
	if after := source.Stats(); after.StatusRequests != before.StatusRequests || after.FollowRequests != before.FollowRequests || after.ActiveFollowers != 0 {
		t.Fatal("collection continued while the registration was paused")
	}
	return w
}

func sentinelLiveEnable(t *testing.T, h *sentinelLiveHarness, watch sentinel.Watch) {
	t.Helper()
	if err := h.run(t, "我确认启用刚才准备的哨兵，允许它持续监听并按之前的条件自动唤醒和保存站内报告，直到我暂停。请启用现有那一个，不要另建。", 3*time.Minute); err != nil {
		t.Fatalf("real-model activation failed: %v", err)
	}
	watches := sentinelLiveWatches(t, h)
	if len(watches) != 1 || watches[0].Spec.ID != watch.Spec.ID || !watches[0].Spec.Enabled {
		t.Fatalf("activation did not enable the existing registration: %+v", watches)
	}
}

func sentinelLiveReport(t *testing.T, h *sentinelLiveHarness, watch sentinel.Watch, expectedID, expectedText string, count int) sentinel.Batch {
	t.Helper()
	var result sentinel.Batch
	sentinelLiveAwait(t, 4*time.Minute, "durable real-model report for "+expectedID, func() bool {
		batches := sentinelLiveBatches(t, h, watch.Spec.ID)
		for _, b := range batches {
			if b.Status == "interrupted" {
				t.Fatalf("event processing interrupted: %+v", b)
			}
			if b.Status == "completed" && b.Decision == "report" && strings.Contains(b.Summary, expectedID) && strings.Contains(b.Summary, expectedText) {
				result = b
				return true
			}
		}
		return false
	})
	batches := sentinelLiveBatches(t, h, watch.Spec.ID)
	if len(batches) != count {
		t.Fatalf("expected %d logical report batches, got %+v", count, batches)
	}
	matched := false
	for _, event := range result.Events {
		matched = matched || strings.Contains(string(event.Data), expectedID) && strings.Contains(string(event.Data), expectedText)
	}
	if !matched || result.SessionID == "" || result.RunID == "" {
		t.Fatalf("report lost observed event or execution identity: %+v", result)
	}
	sess, err := h.mgr.Get(result.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	receipt, err := sess.readWakeReceipt(fmt.Sprintf("%s-%d", result.ID, result.Attempt))
	if err != nil || receipt.Status != "completed" || receipt.RunID != result.RunID || receipt.Summary != result.Summary || receipt.Decision != result.Decision {
		t.Fatalf("durable receipt mismatch: %+v %v", receipt, err)
	}
	h.evidence(t, "report-"+expectedID, result)
	return result
}

func sentinelLiveQuiet(t *testing.T, h *sentinelLiveHarness, watch sentinel.Watch, duration time.Duration) {
	t.Helper()
	calls := h.capture.Count()
	if calls != len(h.capture.Snapshot()) {
		t.Fatal("quiet measurement began with a model call still in progress")
	}
	for _, batch := range sentinelLiveBatches(t, h, watch.Spec.ID) {
		if batch.Status != "completed" {
			t.Fatalf("quiet measurement has unsettled model work: %+v", batch)
		}
	}
	batches := len(sentinelLiveBatches(t, h, watch.Spec.ID))
	time.Sleep(duration)
	if h.capture.Count() != calls || len(sentinelLiveBatches(t, h, watch.Spec.ID)) != batches {
		t.Fatal("quiet or duplicate observations caused additional model work")
	}
	for _, current := range sentinelLiveWatches(t, h) {
		if current.Spec.ID == watch.Spec.ID && (current.Pending != 0 || current.Error != "") {
			t.Fatalf("quiet collector is unhealthy or has pending work: %+v", current)
		}
	}
}

func sentinelLivePause(t *testing.T, h *sentinelLiveHarness, source *sentinelLiveSource, watch sentinel.Watch) {
	t.Helper()
	if err := h.run(t, "现在暂停刚才那个哨兵，保留它和历史报告。", 2*time.Minute); err != nil {
		t.Fatalf("real-model pause failed: %v", err)
	}
	watches := sentinelLiveWatches(t, h)
	if len(watches) != 1 || watches[0].Spec.Enabled {
		t.Fatalf("watch did not pause: %+v", watches)
	}
	// Allow already admitted collector cancellation to settle before measuring.
	time.Sleep(time.Second)
	before := source.Stats()
	sentinelLiveQuiet(t, h, watch, 5*time.Second)
	after := source.Stats()
	if after.StatusRequests != before.StatusRequests || after.FollowRequests != before.FollowRequests || after.ActiveFollowers != 0 {
		t.Fatalf("paused collector still accessed the source: before=%+v after=%+v", before, after)
	}
}

func TestSentinelLivePoll(t *testing.T) {
	h := newSentinelLiveHarness(t, sentinelLiveModel())
	source := startSentinelLiveSource(t)
	source.Publish(sentinelLiveObservation{ID: "baseline-poll", Severity: "low", Status: "open", Text: "normal"})
	w := sentinelLiveCreatePaused(t, h, source, "每 3 秒检查一次该服务的当前记录。只有 severity 为 high 的新观察才唤醒并报告；相同 id 只报告一次，普通记录保持安静。")
	if w.Spec.Mode != "poll" {
		t.Fatalf("periodic current-state check should use poll, got %s", w.Spec.Mode)
	}
	checks := sentinelLiveChecks(source)
	sentinelLiveEnable(t, h, w)
	sentinelLiveAwait(t, 20*time.Second, "periodic checks", func() bool { return sentinelLiveChecks(source) >= checks+3 })
	checks = sentinelLiveChecks(source)
	sentinelLiveQuiet(t, h, w, 8*time.Second)
	if sentinelLiveChecks(source) < checks+1 {
		t.Fatal("quiet measurement did not include ongoing periodic checks")
	}
	event := source.Publish(sentinelLiveObservation{ID: "poll-high-001", Severity: "high", Status: "open", Text: "queue-latency-anchor"})
	sentinelLiveReport(t, h, w, event.ID, event.Text, 1)
	source.Publish(event)
	sentinelLiveQuiet(t, h, w, 8*time.Second)
	sentinelLivePause(t, h, source, w)
}

func TestSentinelLiveStream(t *testing.T) {
	h := newSentinelLiveHarness(t, sentinelLiveModel())
	source := startSentinelLiveSource(t)
	source.Publish(sentinelLiveObservation{ID: "baseline-stream", Severity: "low", Status: "open", Text: "normal"})
	w := sentinelLiveCreatePaused(t, h, source, "我需要及时收到 severity 为 high 的新观察报告，普通记录保持安静。服务支持持续订阅，请使用事件流，不要用定时轮询；断线后应自动续接、从最后记录恢复，相同 id 只报告一次。")
	if w.Spec.Mode != "stream" {
		t.Fatalf("continuous subscription should use stream, got %s", w.Spec.Mode)
	}
	sentinelLiveEnable(t, h, w)
	sentinelLiveAwait(t, 30*time.Second, "live source subscription", func() bool { return source.Stats().ActiveFollowers > 0 })
	sentinelLiveQuiet(t, h, w, 6*time.Second)
	if source.Stats().ActiveFollowers == 0 {
		t.Fatal("quiet event subscription was not still connected")
	}
	first := source.Publish(sentinelLiveObservation{ID: "stream-high-001", Severity: "high", Status: "open", Text: "first-stream-anchor"})
	firstReport := sentinelLiveReport(t, h, w, first.ID, first.Text, 1)
	source.Publish(first)
	sentinelLiveQuiet(t, h, w, 5*time.Second)
	follows := source.Stats().FollowRequests
	source.DropFollowers()
	sentinelLiveAwait(t, 30*time.Second, "stream reconnect", func() bool { return source.Stats().FollowRequests > follows && source.Stats().ActiveFollowers > 0 })
	commands := source.Stats().Commands
	if len(commands) == 0 || commands[len(commands)-1].Command != "FOLLOW" || commands[len(commands)-1].AfterID != first.ID {
		t.Fatalf("stream did not resume from its last committed observation: %+v", commands)
	}
	second := source.Publish(sentinelLiveObservation{ID: "stream-high-002", Severity: "high", Status: "open", Text: "second-stream-anchor"})
	secondReport := sentinelLiveReport(t, h, w, second.ID, second.Text, 2)
	if secondReport.SessionID == "" || secondReport.SessionID != firstReport.SessionID {
		t.Fatal("successive event reports did not retain the same work conversation")
	}
	retained := false
	for _, call := range h.capture.Snapshot() {
		prior, current := false, false
		for _, message := range call.History {
			for _, block := range message.Content {
				prior = prior || strings.Contains(block.Text, first.ID)
				current = current || strings.Contains(block.Text, second.ID)
			}
		}
		retained = retained || prior && current
	}
	if !retained {
		t.Fatal("the second wake's real provider request did not retain its prior observation context")
	}
	sentinelLivePause(t, h, source, w)
}

func TestSentinelLiveComposite(t *testing.T) {
	h := newSentinelLiveHarness(t, sentinelLiveModel())
	source := startSentinelLiveSource(t)
	source.Publish(sentinelLiveObservation{ID: "baseline-composite", Severity: "low", Status: "open", Text: "normal"})
	// A genuine transient upstream failure must be diagnosed or retried by the
	// model during preparation; no completed script is supplied by the harness.
	source.BreakNext("STATUS")
	source.BreakNext("FOLLOW")
	w := sentinelLiveCreatePaused(t, h, source, "每 2 秒检查一次当前记录。只有 severity=high 且 status=open 且 acked=false 这三个条件同时满足的新观察才唤醒和报告，其他情况保持安静；同一个 id 只报告一次。这个服务偶尔可能返回异常的一行响应，需要处理这种情况。")
	// A compound predicate is independent of collection mode. Some agents use
	// a continuous feed plus a two-second checkpoint; judge the business result
	// and actual source delivery rather than requiring a particular adapter.
	sentinelLiveEnable(t, h, w)
	for _, event := range []sentinelLiveObservation{
		{ID: "nonmatch-severity", Severity: "low", Status: "open", Acked: false, Text: "low severity"},
		{ID: "nonmatch-closed", Severity: "high", Status: "closed", Acked: false, Text: "closed"},
		{ID: "nonmatch-acked", Severity: "high", Status: "open", Acked: true, Text: "already acknowledged"},
	} {
		checks := sentinelLiveChecks(source)
		delivered := source.Stats().DeliveredLines
		source.Publish(event)
		sentinelLiveAwait(t, 15*time.Second, "negative condition being sampled", func() bool {
			stats := source.Stats()
			if w.Spec.Mode == "stream" {
				return stats.ActiveFollowers > 0 && stats.DeliveredLines > delivered
			}
			return sentinelLiveChecks(source) >= checks+2
		})
		sentinelLiveQuiet(t, h, w, 5*time.Second)
	}
	event := source.Publish(sentinelLiveObservation{ID: "composite-high-001", Severity: "high", Status: "open", Acked: false, Text: "compound-trigger-anchor"})
	sentinelLiveReport(t, h, w, event.ID, event.Text, 1)
	if source.Stats().MalformedReplies < 1 {
		t.Fatal("transient upstream failure was not exercised")
	}
	sentinelLivePause(t, h, source, w)
}
