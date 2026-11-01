package agent

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/tools"
)

func TestDownloadReceiptsAreDistinctHostSources(t *testing.T) {
	s := newSourceStore("")
	for _, tc := range []struct {
		action, output string
		details        any
	}{
		{"downloads", "recorded state:completed", map[string]any{"url": "about:blank", "title": "browser", "full_output_path": "spill/downloads.txt"}},
		{" downloads ", "recorded state:saved; path:uploads/report.xlsx", map[string]any{"url": "https://unrelated.example", "title": "Unrelated webpage"}},
		{"downloads", "browser downloads: transport unavailable", nil},
	} {
		args, _ := json.Marshal(map[string]any{"action": tc.action})
		src := s.Next("browser", tc.details, "download-check-"+tc.action, tc.output, args)
		if src == nil || src.Type != SourceTool || src.Title != "浏览器下载状态（主机记录）" || !strings.HasPrefix(src.Locator, "tool://browser-downloads/") {
			t.Fatalf("download receipt became a webpage or generic placeholder: %+v", src)
		}
		if src.Excerpt != tc.output || src.Hash == "" || src.CallID != "download-check-"+tc.action {
			t.Fatalf("receipt lost its observed output or identity: %+v", src)
		}
	}
	if s.All()[0].FullPath != "spill/downloads.txt" {
		t.Fatal("bounded receipt lost access to its full recorded output")
	}
	if invalid := ValidateCitations("下载状态未能读取。[1](#cite:S3)", s); len(invalid) != 0 {
		t.Fatalf("failure receipt cannot be cited: %v", invalid)
	}
}

func TestDownloadReceiptSnapshotsSurviveRestoreWithoutInventingOldLabels(t *testing.T) {
	s := newSourceStore("")
	args := json.RawMessage(`{"action":"downloads"}`)
	pending := s.Next("browser", nil, "check-pending", "state:completed; no workspace path", args)
	saved := s.Next("browser", nil, "check-saved", "state:saved; path:uploads/report.xlsx", args)
	legacy := llm.Block{Type: llm.BlockToolResult, ToolUseID: "legacy-download", Text: "downloads without a citation header"}
	history := []llm.Message{{Role: llm.RoleUser, Content: []llm.Block{legacy, sourceResultBlock(t, *pending), sourceResultBlock(t, *saved)}}}
	restored := newSourceStore("")
	for range 2 {
		restored.observe(history)
		for _, want := range []*Source{pending, saved} {
			if got, ok := restored.Get(want.Label); !ok || *got != *want {
				t.Fatalf("restored receipt changed: %+v, want %+v", got, want)
			}
		}
		if len(restored.All()) != 2 {
			t.Fatal("reload manufactured a source for an old unstamped result")
		}
	}
	if invalid := ValidateCitations("未保存。[1](#cite:S1) 后来保存。[1](#cite:S2)", restored); len(invalid) != 0 {
		t.Fatalf("restored receipt labels invalid: %v", invalid)
	}
	if invalid := ValidateCitations("猜测旧结果的编号。[1](#cite:S84)", restored); len(invalid) != 1 || invalid[0] != "S84" {
		t.Fatalf("invented legacy download citation passed: %v", invalid)
	}
	if next := restored.Next("browser", nil, "check-after-reload", "no change", args); next.Label != "S3" {
		t.Fatalf("reload reused or skipped a source label: %+v", next)
	}
}

type downloadReceiptLink struct {
	reply tools.BrowserReply
	err   error
	calls int
}

func (*downloadReceiptLink) BrowserBackend() string { return tools.BrowserBackendNative }
func (l *downloadReceiptLink) Execute(_ context.Context, op json.RawMessage) (tools.BrowserReply, error) {
	l.calls++
	if string(op) != `{"action":"downloads"}` {
		return tools.BrowserReply{}, errors.New("unexpected browser operation")
	}
	return l.reply, l.err
}

func downloadReceiptCall() llm.Response {
	r := toolCalls("browser")
	r.Message.Content[0].Input = json.RawMessage(`{"action":"downloads"}`)
	return r
}

func downloadReceiptAnswer(text string) llm.Response {
	return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockText, Text: text}}}, StopReason: llm.StopEndTurn}
}

func TestDownloadReceiptsStampActualResultsWithoutForcingCitationRewrite(t *testing.T) {
	for _, tc := range []struct {
		name, data, answer string
		replyError         string
		transportError     error
	}{
		{"saved", `{"supported":true,"downloads":[{"state":"saved","path":"uploads/report.xlsx"}]}`, "文件已保存到 uploads/report.xlsx。", "", nil},
		{"pending", `{"supported":true,"downloads":[{"state":"completed"}]}`, "浏览器下载已完成，但还没有工作区路径，不能当成已保存文件。", "", nil},
		{"rejected", `{"supported":true,"downloads":[{"state":"rejected","error":"DOWNLOAD_TOO_LARGE"}]}`, "下载被大小限制拒绝，文件未保存。", "", nil},
		{"unsupported", `{"supported":false,"downloads":[]}`, "当前浏览器不支持下载传输，无法确认文件已保存。", "", nil},
		{"malformed", `{}`, "下载状态记录不可用，不能确认文件已保存。", "", nil},
		{"failed", `{"error_code":"TRANSFER_UNAVAILABLE"}`, "下载状态读取失败，无法确认文件已保存。", "TRANSFER_UNAVAILABLE", nil},
		{"transport", ``, "下载状态连接失败，无法确认文件已保存。", "", errors.New("browser unavailable")},
	} {
		for _, cited := range []bool{false, true} {
			name := tc.name
			if cited {
				name += "-cited"
			}
			t.Run(name, func(t *testing.T) {
				link := &downloadReceiptLink{reply: tools.BrowserReply{OK: tc.replyError == "", Error: tc.replyError, Data: json.RawMessage(tc.data)}, err: tc.transportError}
				answer := tc.answer
				if cited {
					answer += " [1](#cite:S1)"
				}
				client := &fakeClient{responses: []llm.Response{downloadReceiptCall(), downloadReceiptAnswer(answer)}}
				a := New(Config{Client: client, Registry: tools.NewRegistry(&tools.Browser{Link: link}), Citations: true})
				events := citeRun(t, a, "检查本次下载状态")
				if len(eventsOf(events, EventSteer)) != 0 || client.callCount() != 2 || link.calls != 1 {
					t.Fatalf("status receipt caused an unnecessary rewrite/retry: model=%d, tool=%d", client.callCount(), link.calls)
				}
				ends := eventsOf(events, EventToolEnd)
				if len(ends) != 1 || ends[0].Source == nil || ends[0].Source.Type != SourceTool {
					t.Fatalf("download did not emit its host source: %+v", ends)
				}
				if strings.Contains(ends[0].ToolOutput, "[cite:") {
					t.Fatal("event output unexpectedly includes a model-facing stamp")
				}
				var result llm.Block
				for _, msg := range a.Messages() {
					for _, block := range msg.Content {
						if block.Type == llm.BlockToolResult {
							result = block
						}
					}
				}
				if !strings.HasPrefix(result.Text, "[cite:S1 | browser | 浏览器下载状态（主机记录）]\n") || StripCiteHeader(result.Text) != ends[0].ToolOutput {
					t.Fatalf("model did not receive the actual receipt's label: %s", result.Text)
				}
				var persisted Source
				if json.Unmarshal(result.Source, &persisted) != nil || persisted != *ends[0].Source {
					t.Fatalf("source persistence differs from emitted source: %s", result.Source)
				}
				restored := New(Config{Client: &fakeClient{}, Registry: tools.NewRegistry(), Citations: true})
				restored.SetMessages(a.Messages())
				if got, ok := restored.sources.Get("S1"); !ok || *got != persisted {
					t.Fatalf("refresh lost receipt identity: %+v", got)
				}
			})
		}
	}
}

func TestDownloadReceiptRejectsInventedCitationAndKeepsCorrectedAnswer(t *testing.T) {
	link := &downloadReceiptLink{reply: tools.BrowserReply{OK: true, Data: json.RawMessage(`{"supported":true,"downloads":[{"state":"rejected","error":"DOWNLOAD_TOO_LARGE"}]}`)}}
	client := &fakeClient{responses: []llm.Response{
		downloadReceiptCall(),
		downloadReceiptAnswer("下载被拒绝，文件未保存。[1](#cite:S84)"),
		downloadReceiptAnswer("下载被大小限制拒绝，文件未保存。[1](#cite:S1)"),
	}}
	a := New(Config{Client: client, Registry: tools.NewRegistry(&tools.Browser{Link: link}), Citations: true})
	events := citeRun(t, a, "检查本次下载状态")
	steers := eventsOf(events, EventSteer)
	if len(steers) != 1 || client.callCount() != 3 || link.calls != 1 {
		t.Fatalf("invented citation not corrected exactly once: steers=%d model=%d tool=%d", len(steers), client.callCount(), link.calls)
	}
	msgs := a.Messages()
	last := msgs[len(msgs)-1].Content[0].Text
	if !strings.Contains(last, "文件未保存") || !strings.Contains(last, "#cite:S1") || len(ValidateCitations(last, a.sources)) != 0 {
		t.Fatalf("correction lost complete supported answer: %s", last)
	}
}
