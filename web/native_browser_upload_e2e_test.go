package web

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/yosukeno/pogo/tools"
)

// Run explicitly against a disposable real Wayland worker and our local test
// website. Normal unit runs never contact a deployment or business website.
func TestNativeBrowserUploadRealWebsite(t *testing.T) {
	endpoint := os.Getenv("POGO_UPLOAD_E2E_WORKER")
	if endpoint == "" {
		t.Skip("disposable worker is opt-in")
	}
	cfg, err := parseNativeBrowserConfig(endpoint, os.Getenv("POGO_UPLOAD_E2E_TOKEN"))
	if err != nil {
		t.Fatal(err)
	}
	b := newNativeBrowser(cfg)
	b.uploadRoot = os.Getenv("POGO_UPLOAD_E2E_FILES")
	defer b.Close()
	tool := &tools.Browser{Link: b, ImageMode: tools.BrowserImageNone}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
	defer cancel()
	run := func(value map[string]any) tools.Result {
		t.Helper()
		raw, _ := json.Marshal(value)
		result, err := tool.Execute(ctx, raw)
		details, _ := result.Details.(map[string]any)
		if err != nil || details["ok"] == false {
			t.Fatalf("%s: %v %+v", value["action"], err, result)
		}
		return result
	}
	site := os.Getenv("POGO_UPLOAD_E2E_SITE")
	run(map[string]any{"action": "navigate", "url": site})
	for _, item := range []struct{ mode, target, file, result string }{
		{"input", "#direct", "测试配置.xlsx", "#direct-result"},
		{"input", "#hidden", "中文配置.csv", "#chooser-result"},
		{"chooser", "#chooser", "配置.json", "#chooser-result"},
		{"drop", "#drop", "测试配置.xlsx", "#drop-result"},
	} {
		result := run(map[string]any{"action": "upload", "target": item.target, "upload_mode": item.mode, "files": []string{item.file}})
		if !strings.Contains(result.Text, "site_parsing_verified") || !strings.Contains(result.Text, "selected") {
			t.Fatal("selection receipt was lost by host transport")
		}
		run(map[string]any{"action": "wait", "target": item.result + ".pass", "seconds": 8})
		parsed := run(map[string]any{"action": "extract", "target": item.result})
		if !strings.Contains(parsed.Text, "研发") {
			t.Fatalf("website did not parse actual bytes: %s", parsed.Text)
		}
		t.Logf("%s %s: frontend parsed Chinese data", item.mode, item.file)
	}
	run(map[string]any{"action": "select", "target": "#mapping", "text": "数量"})
	run(map[string]any{"action": "click", "target": "#apply"})
	configured := run(map[string]any{"action": "extract", "target": "#configuration"})
	if !strings.Contains(configured.Text, "数量列=数量") || !strings.Contains(configured.Text, "人工输入必须保留") {
		t.Fatal("configuration or manual input lost")
	}
	t.Log("Go host streamed workspace files into real Chromium; website parsed XLSX/CSV/JSON and configured fields; original manual text preserved")
}
