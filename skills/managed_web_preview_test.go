package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEmbeddedWebDevelopmentKeepsIPPathPreviewContract(t *testing.T) {
	dirs := materializeEmbeddedForTest(t)
	list, diagnostics := Load(Options{Cwd: t.TempDir(), UserDir: t.TempDir(), Embedded: dirs})
	reportSkillDiagnosticsInTest(t, diagnostics)
	skill, ok := Find(list, "web-development")
	if !ok || skill.BrowserTask {
		t.Fatal("web-development must load without granting browser authority")
	}
	for file, required := range map[string][]string{
		"SKILL.md": {
			"preview-mode=path", "http://内网IP:19770", "/_pogo/web/<48hex>/",
			"静态项目继续走 HTML Preview", "./assets/app.js", "./api/counter",
			"/api/counter` 是根绝对路径", "被 CSP 拦截", "服务端路由仍写 `/`、`/api/counter`",
			"new URL('./ws', window.location.href)", "'https:' ? 'wss:' : 'ws:'", "pathname", "同host",
			"没有 `allow-same-origin`", "全局修改 fetch/XHR/WebSocket",
			"`service_url`", "用于打开 owner 服务卡", "`preview_url` 用于当前客户端浏览器的 iframe 预览",
			"内置 `browser` 工具只能使用非空的 `browser_url`", "`browser_url:null`",
			"禁止回退到 `preview_url` 或 `service_url`", "改写 host、端口或猜测内网地址",
			"保留客户端 iframe 预览", "重新 open 获取非空 `browser_url`", "内置路径未确认不等于客户端网站不可用",
		},
		"references/python-web.md": {
			"fetch('./api/counter', {method})", `self.path == "/api/counter"`,
			"gateway 会剥离 `/_pogo/web/<48hex>/` 前缀", "不使用根绝对路径",
		},
	} {
		data, err := os.ReadFile(filepath.Join(skill.Dir, file))
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range required {
			if !strings.Contains(string(data), want) {
				t.Errorf("%s omits path-preview boundary %q", file, want)
			}
		}
		if strings.Contains(string(data), "fetch('/api/") || strings.Contains(string(data), `fetch("/api/`) {
			t.Errorf("%s contains a root-absolute browser API example", file)
		}
	}
}
