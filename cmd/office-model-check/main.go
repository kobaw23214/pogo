// Command office-model-check hosts an isolated Pogo UI with an actual configured
// provider. It never scripts model responses or operates the Office editor itself.
package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/yosukeno/pogo/config"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/skills"
	"github.com/yosukeno/pogo/web"
)

type options struct {
	data, fixtures, skillRoot, model, providers, credentials, devProxy string
	port, maxTurns, runTimeoutSeconds, outputCap                       int
}

func optionsFromEnv() (options, error) {
	o := options{data: os.Getenv("OFFICE_MODEL_DATA"), fixtures: os.Getenv("OFFICE_MODEL_FIXTURES"), skillRoot: os.Getenv("OFFICE_MODEL_DOCS_SKILL"), model: os.Getenv("OFFICE_MODEL"), providers: os.Getenv("OFFICE_MODEL_CONFIG"), credentials: os.Getenv("OFFICE_MODEL_CREDENTIALS_FILE"), devProxy: os.Getenv("OFFICE_MODEL_DEV_PROXY"), port: 19893, maxTurns: 24, runTimeoutSeconds: 600, outputCap: 8192}
	for _, v := range []string{o.data, o.fixtures, o.skillRoot, o.model, o.providers} {
		if strings.TrimSpace(v) == "" {
			return o, errors.New("explicit isolated data, synthetic fixtures, docs skill, model and provider configuration are required")
		}
	}
	for key, target := range map[string]*int{"OFFICE_MODEL_PORT": &o.port, "OFFICE_MODEL_MAX_TURNS": &o.maxTurns, "OFFICE_MODEL_RUN_TIMEOUT_SECONDS": &o.runTimeoutSeconds} {
		if raw := strings.TrimSpace(os.Getenv(key)); raw != "" {
			v, err := strconv.Atoi(raw)
			if err != nil {
				return o, errors.New("invalid isolated server port or turn limit")
			}
			*target = v
		}
	}
	if o.port < 1024 || o.port > 65535 || o.maxTurns < 5 || o.maxTurns > 100 || o.runTimeoutSeconds < 60 || o.runTimeoutSeconds > 600 {
		return o, errors.New("port must be 1024–65535, max turns 5–100 and run timeout 60–600 seconds")
	}
	if o.devProxy != "" {
		u, err := url.Parse(o.devProxy)
		if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" {
			return o, errors.New("development UI must be an explicit loopback HTTP origin")
		}
	}
	return o, nil
}

// selectedCredential reads only the selected provider's key_env. This is a
// literal data parser: no shell evaluation, substitutions, expansion or export
// of unrelated values. The file is never copied into the check directory.
func selectedCredential(file, key string) (string, error) {
	if key == "" {
		return "", nil
	}
	f, err := os.Open(file)
	if err != nil {
		return "", errors.New("selected credential file is unavailable")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1024*1024 || info.Mode().Perm()&0077 != 0 {
		return "", errors.New("credential file must be a private regular file of at most 1 MiB")
	}
	scanner := bufio.NewScanner(io.LimitReader(f, 1024*1024+1))
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	value, found := "", false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		name, raw, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(name) != key {
			continue
		}
		if found {
			return "", errors.New("selected credential is declared more than once")
		}
		found = true
		value = strings.TrimSpace(raw)
		if len(value) >= 2 && (value[0] == '\'' && value[len(value)-1] == '\'' || value[0] == '"' && value[len(value)-1] == '"') {
			value = value[1 : len(value)-1]
		}
		if strings.ContainsAny(value, "\r\n\x00") {
			return "", errors.New("selected credential has an invalid value")
		}
	}
	if scanner.Err() != nil || !found || value == "" {
		return "", errors.New("selected provider credential is missing from the private file")
	}
	return value, nil
}

func loadModel(o options) (config.Resolved, error) {
	if err := os.Setenv(config.PathEnv, o.providers); err != nil {
		return config.Resolved{}, errors.New("provider configuration could not be selected")
	}
	if _, err := config.Load(); err != nil {
		return config.Resolved{}, errors.New("explicit provider configuration could not be loaded; diagnostic values withheld")
	}
	if o.credentials != "" {
		provider := ""
		for _, model := range config.Catalog() {
			matched := strings.EqualFold(model.ID, o.model)
			for _, alias := range model.Aliases {
				matched = matched || strings.EqualFold(alias, o.model)
			}
			if matched {
				provider = model.Provider
				break
			}
		}
		if provider == "" {
			return config.Resolved{}, errors.New("explicit model is not in the selected catalog")
		}
		key := config.KeyEnv(provider)
		if key != "" {
			value, err := selectedCredential(o.credentials, key)
			if err != nil {
				return config.Resolved{}, err
			}
			if err = os.Setenv(key, value); err != nil {
				return config.Resolved{}, errors.New("selected credential could not be loaded")
			}
		}
	}
	resolved, err := config.Resolve(o.model)
	if err != nil {
		return config.Resolved{}, errors.New("explicit model could not be resolved; check selected provider credentials")
	}
	return resolved, nil
}

func copyFixtures(o options) (string, error) {
	var manifest struct {
		Synthetic bool `json:"synthetic"`
		Files     map[string]struct {
			Name string `json:"name"`
		} `json:"files"`
	}
	bytes, err := os.ReadFile(filepath.Join(o.fixtures, "manifest.json"))
	if err != nil || json.Unmarshal(bytes, &manifest) != nil || !manifest.Synthetic || len(manifest.Files) != 2 {
		return "", errors.New("only the two artificial Office fixture files are permitted")
	}
	workspace := filepath.Join(o.data, "workspace")
	if err = os.MkdirAll(o.data, 0700); err != nil {
		return "", err
	}
	if err = os.Mkdir(workspace, 0700); err != nil {
		return "", errors.New("use a fresh isolated directory; an existing workspace cannot be overwritten or resumed")
	}
	seen := map[string]bool{}
	for _, file := range manifest.Files {
		if filepath.Base(file.Name) != file.Name || file.Name != "pogo-office-word.docx" && file.Name != "pogo-office-excel.xlsx" || seen[file.Name] {
			return "", errors.New("unexpected synthetic fixture filename")
		}
		seen[file.Name] = true
		contents, err := os.ReadFile(filepath.Join(o.fixtures, file.Name))
		if err != nil || len(contents) == 0 || len(contents) > 20*1024*1024 {
			return "", errors.New("synthetic fixture could not be read or exceeds the size limit")
		}
		if err = os.WriteFile(filepath.Join(workspace, file.Name), contents, 0600); err != nil {
			return "", err
		}
	}
	return workspace, nil
}

type callRecorder struct {
	mu              sync.Mutex
	file            *os.File
	provenancePath  string
	model, provider string
	outputCap       int
	calls           []map[string]any
}
type measuredClient struct {
	real     llm.Client
	recorder *callRecorder
	provider string
}

func (c *measuredClient) Model() string { return c.real.Model() }

func lastUserText(history []llm.Message) string {
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role != llm.RoleUser {
			continue
		}
		texts := []string{}
		for _, block := range history[i].Content {
			if block.Type == llm.BlockText {
				texts = append(texts, block.Text)
			}
		}
		if len(texts) > 0 {
			return strings.Join(texts, "\n")
		}
	}
	return ""
}

func hashJSON(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:]), nil
}

func (r *callRecorder) writeProvenance() error {
	value := map[string]any{"kind": "real-provider-streams", "model": r.model, "provider": r.provider, "synthetic": false, "syntheticDocuments": true, "testMaxOutputTokens": r.outputCap, "calls": r.calls}
	file, err := os.CreateTemp(filepath.Dir(r.provenancePath), ".model-provenance-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if err = file.Chmod(0600); err == nil {
		err = json.NewEncoder(file).Encode(value)
	}
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(file.Name(), r.provenancePath)
}

func (c *measuredClient) Stream(ctx context.Context, system string, history []llm.Message, tools []llm.ToolSchema, delta func(llm.Delta)) (llm.Response, error) {
	started := time.Now()
	response, err := c.real.Stream(ctx, system, history, tools, delta)
	completed := time.Now()
	calls := []map[string]string{}
	ids := []string{}
	for _, block := range response.Message.Content {
		if block.Type == llm.BlockToolUse {
			calls = append(calls, map[string]string{"name": block.Name, "id": block.ID})
			ids = append(ids, block.ID)
		}
	}
	// Only accounting and settled tool identities are recorded here. The actual
	// public arguments/results remain in Pogo's ordinary conversation transcript.
	c.recorder.mu.Lock()
	recordErr := json.NewEncoder(c.recorder.file).Encode(map[string]any{"type": "real_provider_call", "startedAt": started.UTC().Format(time.RFC3339Nano), "durationMS": completed.Sub(started).Milliseconds(), "model": c.Model(), "provider": c.provider, "usage": response.Usage, "stopReason": response.StopReason, "toolCalls": calls, "error": err != nil})
	responseHash, hashErr := hashJSON(response)
	userHash := sha256.Sum256([]byte(lastUserText(history)))
	if recordErr == nil && hashErr == nil {
		c.recorder.calls = append(c.recorder.calls, map[string]any{"model": c.Model(), "provider": c.provider, "responseSha256": responseHash, "lastUserTextSha256": hex.EncodeToString(userHash[:]), "toolUseIds": ids, "startedAt": started.UTC().Format(time.RFC3339Nano), "completedAt": completed.UTC().Format(time.RFC3339Nano), "completed": err == nil, "usage": response.Usage, "stopReason": response.StopReason})
		recordErr = c.recorder.writeProvenance()
	} else if recordErr == nil {
		recordErr = hashErr
	}
	c.recorder.mu.Unlock()
	if recordErr != nil {
		return llm.Response{}, errors.New("real model accounting could not be recorded")
	}
	if err != nil {
		if ctx.Err() != nil {
			return response, ctx.Err()
		}
		return response, errors.New("real provider request failed; response diagnostic withheld from test records")
	}
	return response, nil
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	o, err := optionsFromEnv()
	if err != nil {
		return err
	}
	o.data, err = filepath.Abs(o.data)
	if err != nil {
		return err
	}
	resolved, err := loadModel(o)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(o.port)))
	if err != nil {
		return errors.New("isolated loopback port is unavailable; no service was replaced")
	}
	defer listener.Close()
	workspace, err := copyFixtures(o)
	if err != nil {
		return err
	}
	if _, err = os.Stat(filepath.Join(o.skillRoot, "SKILL.md")); err != nil {
		return errors.New("reviewed builtin docs skill is unavailable")
	}
	for _, directory := range []string{"private", "verification"} {
		if err = os.MkdirAll(filepath.Join(o.data, directory), 0700); err != nil {
			return err
		}
	}
	accounting, err := os.OpenFile(filepath.Join(o.data, "verification/model-calls.jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer accounting.Close()
	recorder := &callRecorder{file: accounting, provenancePath: filepath.Join(o.data, "verification/model-provenance.json"), model: resolved.Model, provider: resolved.Provider, outputCap: o.outputCap, calls: []map[string]any{}}
	if err = recorder.writeProvenance(); err != nil {
		return errors.New("real model provenance record could not be initialized")
	}
	manager, err := web.NewManager(web.Config{Cwd: workspace, SessionDir: filepath.Join(o.data, "sessions"), Model: resolved.Model, Retries: -1, MaxTurns: o.maxTurns, GateTimeout: 5 * time.Second, RunTimeout: time.Duration(o.runTimeoutSeconds) * time.Second, IdleTimeout: time.Hour, Skills: []skills.Skill{{Name: "docs", Description: "Word and Excel live editing", Dir: o.skillRoot, Path: filepath.Join(o.skillRoot, "SKILL.md"), Source: "embedded"}}, NewClient: func(cfg config.Resolved, onRetry func(llm.RetryInfo)) llm.Client {
		maxTokens := cfg.MaxTokens
		if maxTokens == 0 || maxTokens > int64(o.outputCap) {
			maxTokens = int64(o.outputCap)
		}
		return &measuredClient{real: llm.New(llm.Options{BaseURL: cfg.BaseURL, APIKey: cfg.APIKey, Model: cfg.Model, MaxTokens: maxTokens, Headers: cfg.Headers, ExtraBody: cfg.ExtraBody, Compatibility: cfg.Compatibility, MaxRetries: -1, OnRetry: onRetry}), recorder: recorder, provider: cfg.Provider}
	}})
	if err != nil {
		return errors.New("isolated Pogo manager could not start")
	}
	defer manager.Close()
	secret := make([]byte, 24)
	if _, err = rand.Read(secret); err != nil {
		return err
	}
	token := hex.EncodeToString(secret)
	server, err := web.NewServer(manager, web.ServerOptions{Token: token, DevProxy: o.devProxy})
	if err != nil {
		return errors.New("isolated Pogo server could not start; check Office configuration")
	}
	defer server.Close()
	origin := fmt.Sprintf("http://127.0.0.1:%d", o.port)
	runtime, _ := json.MarshalIndent(map[string]any{"url": origin, "token": token, "pid": os.Getpid(), "workspace": workspace, "model": resolved.Model, "provider": resolved.Provider, "realModel": true, "syntheticDocuments": true, "maxTurns": o.maxTurns, "runTimeoutSeconds": o.runTimeoutSeconds}, "", "  ")
	if err = os.WriteFile(filepath.Join(o.data, "private/runtime.json"), runtime, 0600); err != nil {
		return err
	}
	// This public metadata intentionally excludes endpoints, headers, credentials
	// and editor capabilities. No model request is made just by starting the host.
	metadata, _ := json.MarshalIndent(map[string]any{"realModel": true, "model": resolved.Model, "provider": resolved.Provider, "contextWindow": resolved.ContextWindow, "configuredMaxOutputTokens": resolved.MaxTokens, "testMaxOutputTokens": o.outputCap, "syntheticDocuments": true, "startedAt": time.Now().UTC().Format(time.RFC3339)}, "", "  ")
	if err = os.WriteFile(filepath.Join(o.data, "verification/model-info.json"), metadata, 0600); err != nil {
		return err
	}
	httpServer := &http.Server{Handler: server.Handler(), ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdown)
	}()
	fmt.Printf("Isolated Pogo Office UI: %s/ (real configured model %s; synthetic documents only)\n", origin, resolved.Model)
	err = httpServer.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
