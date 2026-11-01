// Command office-integration-check runs an isolated Pogo UI with a deterministic
// model. It tests the real Agent loop and live Office executor, not model quality.
// All workspace files must come from the artificial Office fixture manifest.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/yosukeno/pogo/config"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/skills"
	"github.com/yosukeno/pogo/tools"
	"github.com/yosukeno/pogo/web"
)

type fixtureModel struct {
	mu                           sync.Mutex
	step                         int
	waits                        int
	prompt, kind, document, seed string
	batchID, stagePath           string
	before                       map[string]any
	data                         string
}

func (*fixtureModel) Model() string { return "office-fixture" }

func resultText(history []llm.Message) (string, bool) {
	for i := len(history) - 1; i >= 0; i-- {
		for j := len(history[i].Content) - 1; j >= 0; j-- {
			b := history[i].Content[j]
			if b.Type == llm.BlockToolResult {
				return b.Text, b.IsError
			}
		}
	}
	return "", false
}

func snapshot(value map[string]any) map[string]any {
	if result, ok := value["result"].(map[string]any); ok {
		return result
	}
	return value
}

func (c *fixtureModel) Stream(ctx context.Context, _ string, history []llm.Message, schemas []llm.ToolSchema, onDelta func(llm.Delta)) (llm.Response, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return llm.Response{}, err
	}
	prompt := ""
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == llm.RoleUser {
			for _, b := range history[i].Content {
				if b.Type == llm.BlockText {
					prompt = b.Text
					break
				}
			}
			if prompt != "" {
				break
			}
		}
	}
	finish := func(text string) (llm.Response, error) {
		if onDelta != nil {
			onDelta(llm.Delta{Kind: llm.DeltaText, Text: text})
		}
		return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockText, Text: text}}}, StopReason: llm.StopEndTurn}, nil
	}
	if strings.HasPrefix(prompt, "OFFICE_BATCH_CHECK:") {
		return c.streamBatch(ctx, prompt, history, schemas, onDelta)
	}
	if !strings.HasPrefix(prompt, "OFFICE_CHECK:") {
		return finish("这是隔离验证模型，不连接真实模型服务。请在文件面板体验 Office 编辑；自动验证由测试程序发起。")
	}
	if prompt != c.prompt {
		c.prompt = prompt
		c.step = 0
		c.document = ""
		c.before = nil
		c.waits = 0
		parts := strings.Split(prompt, ":")
		if len(parts) != 3 || (parts[1] != "word" && parts[1] != "excel") {
			return finish("OFFICE_SCENARIO_FAILED: invalid scenario")
		}
		c.kind = parts[1]
		c.seed = parts[2]
	}
	if c.step > 0 {
		text, isError := resultText(history)
		if isError {
			return finish("OFFICE_SCENARIO_FAILED: " + text)
		}
		var reply map[string]any
		if err := json.Unmarshal([]byte(text), &reply); err != nil {
			return finish("OFFICE_SCENARIO_FAILED: tool result was not JSON")
		}
		r := snapshot(reply)
		switch c.step {
		case 1:
			items, _ := reply["sessions"].([]any)
			if session, ok := reply["session"].(map[string]any); ok {
				items = []any{session}
			}
			ready := false
			for _, item := range items {
				v, _ := item.(map[string]any)
				typ, _ := v["fileType"].(string)
				if (c.kind == "word" && typ == "docx") || (c.kind == "excel" && typ == "xlsx") {
					c.document, _ = v["id"].(string)
					ready, _ = v["ready"].(bool)
				}
			}
			if c.document == "" {
				return finish("OFFICE_SCENARIO_FAILED: no live document in this conversation")
			}
			if !ready {
				c.waits++
				if c.waits > 100 {
					return finish("OFFICE_SCENARIO_FAILED: the real editor did not connect")
				}
				select {
				case <-ctx.Done():
					return llm.Response{}, ctx.Err()
				case <-time.After(250 * time.Millisecond):
				}
				return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockToolUse, ID: fmt.Sprintf("wait_%s_%s_%d", c.kind, c.seed, c.waits), Name: tools.OfficeToolName, Input: json.RawMessage(`{"action":"list"}`)}}}, StopReason: llm.StopToolUse}, nil
			}
		case 2:
			c.before = r
			marker := "POGO_WORD_USER_EDIT_001"
			observed, _ := r["text"].(string)
			if c.kind == "excel" {
				marker = "POGO_EXCEL_USER_EDIT_001"
				observed = fmt.Sprint(r["values"])
			}
			if !strings.Contains(observed, marker) {
				c.waits++
				if c.waits > 100 {
					return finish("OFFICE_SCENARIO_FAILED: native user input was not observed")
				}
				select {
				case <-ctx.Done():
					return llm.Response{}, ctx.Err()
				case <-time.After(250 * time.Millisecond):
				}
				operation := "word.read"
				args := map[string]any{}
				if c.kind == "excel" {
					operation = "sheet.read"
					args = map[string]any{"sheet": "销售", "range": "B2:E2"}
				}
				id := fmt.Sprintf("human_%s_%s_%d", c.kind, c.seed, c.waits)
				input, _ := json.Marshal(map[string]any{"action": "command", "document_session_id": c.document, "operation_id": id, "operation": operation, "args": args})
				return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockToolUse, ID: id, Name: tools.OfficeToolName, Input: input}}}, StopReason: llm.StopToolUse}, nil
			}
		case 5:
			if c.kind == "word" && r["text"] != c.before["text"] {
				return finish("OFFICE_SCENARIO_FAILED: undo/read lost the original or human edit")
			}
			if c.kind == "excel" {
				v, _ := r["values"].([]any)
				if len(v) != 1 {
					return finish("OFFICE_SCENARIO_FAILED: missing restored cell values")
				}
				row, _ := v[0].([]any)
				if len(row) < 4 || fmt.Sprint(row[1]) != "3" || fmt.Sprint(row[2]) != "360" {
					return finish("OFFICE_SCENARIO_FAILED: undo/read did not restore the real formula values")
				}
			}
		case 7:
			if c.kind == "word" {
				text, _ := r["text"].(string)
				if !strings.Contains(text, "Agent 已更新正文") || !strings.Contains(text, "POGO_WORD_USER_EDIT_001") {
					return finish("OFFICE_SCENARIO_FAILED: redo/read did not preserve agent and user text")
				}
			}
			if c.kind == "excel" {
				v, _ := r["values"].([]any)
				if len(v) != 1 {
					return finish("OFFICE_SCENARIO_FAILED: missing edited cells")
				}
				row, _ := v[0].([]any)
				if len(row) < 4 || fmt.Sprint(row[1]) != "5" || fmt.Sprint(row[2]) != "600" || !strings.Contains(fmt.Sprint(row[3]), "POGO_EXCEL_USER_EDIT_001") {
					return finish("OFFICE_SCENARIO_FAILED: redo/read lost recalculation or human input")
				}
			}
		case 8:
			view, _ := reply["session"].(map[string]any)
			if view == nil || view["saveState"] != "saved" {
				return finish("OFFICE_SCENARIO_FAILED: no persisted save receipt")
			}
			return finish("OFFICE_SCENARIO_OK: " + c.kind + "；真实 Pogo Agent 已完成读取、修改、撤销、读取、重做、读取与持久化保存。此结果只验证执行链路，不评价模型自主判断。")
		}
	}
	var op map[string]any
	id := fmt.Sprintf("check_%s_%s_%d", c.kind, c.seed, c.step)
	command := func(name string, args map[string]any) map[string]any {
		return map[string]any{"action": "command", "document_session_id": c.document, "operation_id": id, "operation": name, "args": args}
	}
	read := func() map[string]any {
		if c.kind == "word" {
			return command("word.read", map[string]any{})
		}
		return command("sheet.read", map[string]any{"sheet": "销售", "range": "B2:E2"})
	}
	switch c.step {
	case 0:
		ext := "docx"
		if c.kind == "excel" {
			ext = "xlsx"
		}
		op = map[string]any{"action": "open", "path": "pogo-office-" + c.kind + "." + ext, "operation_id": id}
	case 1, 4, 6:
		op = read()
	case 2:
		if c.kind == "word" {
			op = command("word.replace", map[string]any{"search": "POGO_WORD_TARGET_001：这是等待 Agent 更新的正文，当前状态为草稿。", "replacement": "POGO_WORD_TARGET_001：Agent 已更新正文，并保留原来的字体和段落样式。", "expectedText": c.before["text"], "expectedCount": 1})
		} else {
			inputs, _ := c.before["inputs"].([]any)
			if len(inputs) != 1 {
				return finish("OFFICE_SCENARIO_FAILED: missing current inputs")
			}
			row, _ := inputs[0].([]any)
			if len(row) < 2 {
				return finish("OFFICE_SCENARIO_FAILED: missing current quantity")
			}
			op = command("sheet.apply", map[string]any{"sheet": "销售", "range": "C2", "expectedInputs": [][]any{{row[1]}}, "inputs": [][]any{{5}}})
		}
	case 3:
		op = command("history.undo", map[string]any{})
	case 5:
		op = command("history.redo", map[string]any{})
	case 7:
		op = map[string]any{"action": "save", "document_session_id": c.document, "operation_id": id}
	default:
		return finish("OFFICE_SCENARIO_FAILED: invalid phase")
	}
	encoded, _ := json.Marshal(op)
	c.step++
	return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockToolUse, ID: id, Name: tools.OfficeToolName, Input: encoded}}}, StopReason: llm.StopToolUse}, nil
}

// streamBatch is a deterministic executor fixture. It uses the production
// Office tool to preserve the live canvas, modifies only the staged synthetic
// workbook, and commits that exact lease back through the production tool.
func (c *fixtureModel) streamBatch(ctx context.Context, prompt string, history []llm.Message, schemas []llm.ToolSchema, onDelta func(llm.Delta)) (llm.Response, error) {
	finish := func(text string) (llm.Response, error) {
		if onDelta != nil {
			onDelta(llm.Delta{Kind: llm.DeltaText, Text: text})
		}
		return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockText, Text: text}}}, StopReason: llm.StopEndTurn}, nil
	}
	if c.prompt != prompt {
		c.prompt, c.step, c.batchID, c.stagePath = prompt, 0, "", ""
		c.seed = strings.TrimPrefix(prompt, "OFFICE_BATCH_CHECK:")
		if c.seed == "" || strings.ContainsAny(c.seed, ":/\\\n\r") {
			return finish("OFFICE_BATCH_FAILED: invalid scenario")
		}
	}
	if c.step > 0 {
		text, isError := resultText(history)
		if isError {
			return finish("OFFICE_BATCH_FAILED: " + text)
		}
		if c.step != 2 {
			var result map[string]any
			if json.Unmarshal([]byte(text), &result) != nil {
				return finish("OFFICE_BATCH_FAILED: invalid tool receipt")
			}
			if c.step == 1 {
				c.batchID, _ = result["batch_id"].(string)
				c.stagePath, _ = result["staged_path"].(string)
				rel, err := filepath.Rel(filepath.Join(c.data, "workspace"), c.stagePath)
				if result["state"] != "prepared" || c.batchID == "" || err != nil || filepath.Base(c.stagePath) != "document.xlsx" || strings.HasPrefix(rel, "..") || !strings.HasPrefix(filepath.Dir(rel), ".pogo-office-batch-") {
					return finish("OFFICE_BATCH_FAILED: non-synthetic staging path")
				}
			} else if c.step == 3 {
				if result["state"] != "committed" || result["batch_id"] != c.batchID {
					return finish("OFFICE_BATCH_FAILED: commit unconfirmed")
				}
				return finish("OFFICE_BATCH_OK: genuine Office prepare, staged all-sheet borders, and commit complete")
			}
		} else if !strings.Contains(text, "SYNTHETIC_BATCH_WRITTEN") {
			return finish("OFFICE_BATCH_FAILED: staging modification missing")
		}
	}
	if err := ctx.Err(); err != nil {
		return llm.Response{}, err
	}
	name := tools.OfficeToolName
	id := fmt.Sprintf("batch_%s_%d", c.seed, c.step)
	var input map[string]any
	switch c.step {
	case 0:
		input = map[string]any{"action": "batch_prepare", "path": "pogo-office-excel.xlsx", "operation_id": id}
	case 1:
		found := false
		for _, schema := range schemas {
			if schema.Name == "bash" {
				found = true
			}
		}
		if !found {
			return finish("OFFICE_BATCH_FAILED: isolated bash executor missing")
		}
		name = "bash"
		code := "from openpyxl import load_workbook; from openpyxl.styles import Border, Side; import sys; w=load_workbook(sys.argv[1]); side=Side(style='thin',color='8097AA'); b=Border(left=side,right=side,top=side,bottom=side); [(setattr(c,'border',b)) for s in w.worksheets for row in s.iter_rows() for c in row]; w.save(sys.argv[1]); print('SYNTHETIC_BATCH_WRITTEN',len(w.worksheets))"
		quote := func(v string) string { return "'" + strings.ReplaceAll(v, "'", "'\"'\"'") + "'" }
		input = map[string]any{"command": "/Users/wangy/.cache/codex-runtimes/codex-primary-runtime/dependencies/python/bin/python -c " + quote(code) + " " + quote(c.stagePath), "timeout": 20}
	case 2:
		input = map[string]any{"action": "batch_commit", "batch_id": c.batchID, "operation_id": id}
	default:
		return finish("OFFICE_BATCH_FAILED: invalid phase")
	}
	encoded, _ := json.Marshal(input)
	c.step++
	return llm.Response{Message: llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockToolUse, ID: id, Name: name, Input: encoded}}}, StopReason: llm.StopToolUse}, nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	addr := os.Getenv("OFFICE_CHECK_ADDR")
	if addr == "" {
		addr = "127.0.0.1:19897"
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil || host != "127.0.0.1" || port == "" || port == "0" {
		return errors.New("OFFICE_CHECK_ADDR must be an explicit loopback host and port")
	}
	devProxy := os.Getenv("OFFICE_CHECK_DEV_PROXY")
	if devProxy == "" {
		devProxy = "http://127.0.0.1:19899"
	}
	parsedProxy, err := url.Parse(devProxy)
	if err != nil || parsedProxy.Scheme != "http" || parsedProxy.Hostname() != "127.0.0.1" || parsedProxy.Port() == "" || parsedProxy.User != nil || parsedProxy.RawQuery != "" || parsedProxy.Fragment != "" || (parsedProxy.Path != "" && parsedProxy.Path != "/") {
		return errors.New("OFFICE_CHECK_DEV_PROXY must be an explicit loopback HTTP service")
	}
	publicURL := "http://" + addr
	data := os.Getenv("OFFICE_CHECK_DATA")
	if data == "" {
		return errors.New("OFFICE_CHECK_DATA must be a new isolated check directory")
	}
	data, err = filepath.Abs(data)
	if err != nil {
		return err
	}
	fixtureRoot := os.Getenv("OFFICE_CHECK_FIXTURES")
	var manifest struct {
		Synthetic bool `json:"synthetic"`
		Files     map[string]struct {
			Name string `json:"name"`
		} `json:"files"`
	}
	bytes, err := os.ReadFile(filepath.Join(fixtureRoot, "manifest.json"))
	if err != nil {
		return err
	}
	if json.Unmarshal(bytes, &manifest) != nil || !manifest.Synthetic || len(manifest.Files) != 2 {
		return errors.New("only the two artificial Office fixture files are permitted")
	}
	workspace := filepath.Join(data, "workspace")
	if err = os.MkdirAll(workspace, 0700); err != nil {
		return err
	}
	for _, file := range manifest.Files {
		if filepath.Base(file.Name) != file.Name {
			return errors.New("invalid fixture path")
		}
		target := filepath.Join(workspace, file.Name)
		if _, err = os.Stat(target); os.IsNotExist(err) {
			contents, e := os.ReadFile(filepath.Join(fixtureRoot, file.Name))
			if e != nil {
				return e
			}
			if err = os.WriteFile(target, contents, 0600); err != nil {
				return err
			}
		}
	}
	config.SetProvidersForTest(map[string]config.Provider{"isolated": {Name: "isolated", BaseURL: "http://127.0.0.1:1/unused"}})
	config.SetCatalogForTest([]config.Model{{ID: "office-fixture", Provider: "isolated", ContextWindow: 100000, MaxTokens: 1024}})
	skillRoot := os.Getenv("OFFICE_CHECK_DOCS_SKILL")
	if skillRoot == "" {
		return errors.New("OFFICE_CHECK_DOCS_SKILL must point to the reviewed builtin docs skill")
	}
	manager, err := web.NewManager(web.Config{Cwd: workspace, SessionDir: filepath.Join(data, "sessions"), Model: "office-fixture", MaxTurns: 120, GateTimeout: 5 * time.Second, RunTimeout: 5 * time.Minute, IdleTimeout: time.Hour, Skills: []skills.Skill{{Name: "docs", Description: "Word/Excel integration checks", Dir: skillRoot, Path: filepath.Join(skillRoot, "SKILL.md"), Source: "embedded"}}, NewClient: func(config.Resolved, func(llm.RetryInfo)) llm.Client { return &fixtureModel{data: data} }})
	if err != nil {
		return err
	}
	defer manager.Close()
	secret := make([]byte, 24)
	if _, err = rand.Read(secret); err != nil {
		return err
	}
	token := hex.EncodeToString(secret)
	if tokenFile := os.Getenv("OFFICE_CHECK_TOKEN_FILE"); tokenFile != "" {
		// Only this isolated harness supports a persisted synthetic credential,
		// so a process-restart check can keep the same browser authentication.
		tokenFile, err = filepath.Abs(tokenFile)
		if err != nil || tokenFile != filepath.Join(data, "private", "check-token") {
			return errors.New("OFFICE_CHECK_TOKEN_FILE must be the isolated private/check-token path")
		}
		if err = os.MkdirAll(filepath.Dir(tokenFile), 0700); err != nil {
			return err
		}
		if stored, readErr := os.ReadFile(tokenFile); readErr == nil {
			token = strings.TrimSpace(string(stored))
			if len(token) != 48 {
				return errors.New("the isolated check token is invalid")
			}
			if _, err = hex.DecodeString(token); err != nil {
				return errors.New("the isolated check token is invalid")
			}
		} else if !errors.Is(readErr, os.ErrNotExist) {
			return readErr
		} else {
			file, createErr := os.OpenFile(tokenFile, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if createErr != nil {
				return createErr
			}
			_, err = file.WriteString(token)
			closeErr := file.Close()
			if err != nil {
				return err
			}
			if closeErr != nil {
				return closeErr
			}
		}
	}
	var officeOptions *web.OfficeOptions
	if value := os.Getenv("OFFICE_CHECK_COMMAND_TIMEOUT"); value != "" {
		duration, parseErr := time.ParseDuration(value)
		if parseErr != nil || duration < 2*time.Second || duration > 35*time.Second {
			return errors.New("OFFICE_CHECK_COMMAND_TIMEOUT must be between 2s and 35s")
		}
		officeOptions = &web.OfficeOptions{ServiceURL: os.Getenv("POGO_OFFICE_SERVICE_URL"), PublicURL: os.Getenv("POGO_OFFICE_PUBLIC_URL"), FetchURL: os.Getenv("POGO_OFFICE_FETCH_URL"), JWTSecretFile: os.Getenv("POGO_OFFICE_JWT_SECRET_FILE"), CommandTimeout: duration}
	}
	server, err := web.NewServer(manager, web.ServerOptions{Token: token, DevProxy: devProxy, Office: officeOptions})
	if err != nil {
		return err
	}
	defer server.Close()
	if err = os.MkdirAll(filepath.Join(data, "private"), 0700); err != nil {
		return err
	}
	runtime, _ := json.Marshal(map[string]any{"url": publicURL, "token": token, "pid": os.Getpid(), "workspace": workspace, "model": "deterministic integration fixture"})
	if err = os.WriteFile(filepath.Join(data, "private/runtime.json"), runtime, 0600); err != nil {
		return err
	}
	httpServer := &http.Server{Addr: addr, Handler: server.Handler(), ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdown)
	}()
	fmt.Printf("Isolated Pogo UI: %s/ (test-only model, no external provider)\n", publicURL)
	err = httpServer.ListenAndServe()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
