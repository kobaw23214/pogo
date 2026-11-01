// Package workflowcode executes reviewed, pure-data workflow code. It never
// invokes a model or falls back to an unconfined host command.
package workflowcode

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	MaxSourceBytes        = 64 << 10
	MaxDataBytes          = 256 << 10
	MaxLogBytes           = 16 << 10
	DefaultTimeoutSeconds = 10
	MaxTimeoutSeconds     = 60
	maxMemoryBytes        = 256 << 20
)

type Request struct {
	Language       string          `json:"language"`
	Source         string          `json:"source"`
	Inputs         json.RawMessage `json:"inputs"`
	TimeoutSeconds int             `json:"timeout_seconds,omitempty"`
}

type Result struct {
	Output          json.RawMessage `json:"output,omitempty"`
	Stdout          string          `json:"stdout,omitempty"`
	Stderr          string          `json:"stderr,omitempty"`
	Runtime         string          `json:"runtime,omitempty"`
	SourceSHA256    string          `json:"source_sha256"`
	ErrorCode       string          `json:"error_code,omitempty"`
	ExitCode        *int            `json:"exit_code,omitempty"`
	TimedOut        bool            `json:"timed_out,omitempty"`
	DurationMS      int64           `json:"duration_ms"`
	StdoutTruncated bool            `json:"stdout_truncated,omitempty"`
	StderrTruncated bool            `json:"stderr_truncated,omitempty"`
}

type LanguageStatus struct {
	Language  string `json:"language"`
	Available bool   `json:"available"`
	Runtime   string `json:"runtime,omitempty"`
	Reason    string `json:"reason,omitempty"`
}

type executionError struct{ code, message string }

func (e *executionError) Error() string { return e.message }
func fail(code, message string) error   { return &executionError{code, message} }

var capabilitiesOnce sync.Once
var capabilities []LanguageStatus

// Capabilities probes only fixed, host-owned code. The result is cached for
// this process; changing an interpreter installation requires a service restart.
func Capabilities() []LanguageStatus {
	capabilitiesOnce.Do(func() {
		for _, language := range []string{"python", "javascript"} {
			source := "def main(inputs):\n    return {\"ready\": True}"
			if language == "javascript" {
				source = "function main(inputs) { return {ready: true}; }"
			}
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			result, err := Execute(ctx, Request{Language: language, Source: source, Inputs: json.RawMessage(`{}`), TimeoutSeconds: 3})
			cancel()
			row := LanguageStatus{Language: language, Available: err == nil, Runtime: result.Runtime}
			if err != nil {
				row.Reason = result.ErrorCode
			}
			capabilities = append(capabilities, row)
		}
	})
	return append([]LanguageStatus(nil), capabilities...)
}

// Execute runs main(inputs), returning an object. Source and data are passed
// separately; inputs are never interpolated into executable source text.
func Execute(parent context.Context, request Request) (result Result, err error) {
	started := time.Now()
	digest := sha256.Sum256([]byte(request.Source))
	result.SourceSHA256 = hex.EncodeToString(digest[:])
	defer func() {
		result.DurationMS = time.Since(started).Milliseconds()
		if err != nil && result.ErrorCode == "" {
			var detail *executionError
			if errors.As(err, &detail) {
				result.ErrorCode = detail.code
			} else {
				result.ErrorCode = "execution_failed"
			}
		}
	}()
	if request.Language != "python" && request.Language != "javascript" {
		return result, fail("invalid_language", "代码语言必须为python或javascript")
	}
	if strings.TrimSpace(request.Source) == "" || len(request.Source) > MaxSourceBytes || !utf8.ValidString(request.Source) || strings.ContainsRune(request.Source, 0) {
		return result, fail("invalid_source", "代码必须是非空UTF-8文本且不超过64KiB")
	}
	if request.TimeoutSeconds == 0 {
		request.TimeoutSeconds = DefaultTimeoutSeconds
	}
	if request.TimeoutSeconds < 1 || request.TimeoutSeconds > MaxTimeoutSeconds {
		return result, fail("invalid_timeout", "代码超时必须为1至60秒")
	}
	if len(request.Inputs) == 0 {
		request.Inputs = json.RawMessage(`{}`)
	}
	if err = validObject(request.Inputs); err != nil {
		return result, fail("invalid_input", "代码输入必须为不超过256KiB的JSON对象")
	}
	if parent.Err() != nil {
		return result, fail("cancelled", "代码执行已取消")
	}
	ctx, cancel := context.WithTimeout(parent, time.Duration(request.TimeoutSeconds)*time.Second)
	defer cancel()
	spec, err := interpreter(request.Language)
	if err != nil {
		return result, err
	}
	result.Runtime = spec.version
	cmd, closeSandbox, err := sandboxCommand(ctx, spec)
	if err != nil {
		return result, err
	}
	defer closeSandbox()
	reader, writer, err := os.Pipe()
	if err != nil {
		return result, fail("execution_failed", "无法建立代码输出通道")
	}
	defer reader.Close()
	defer writer.Close()
	// FD 3 is distinct from the user-visible stdout/stderr logs. Platform
	// sandbox configuration may append further, non-secret descriptors.
	cmd.ExtraFiles = append([]*os.File{writer}, cmd.ExtraFiles...)
	cmd.Stdin = bytes.NewReader(mustMarshal(struct {
		Source string          `json:"source"`
		Inputs json.RawMessage `json:"inputs"`
	}{request.Source, request.Inputs}))
	stdout, stderr := &boundedLog{}, &boundedLog{}
	cmd.Stdout, cmd.Stderr = stdout, stderr
	cmd.WaitDelay = time.Second
	configureProcess(cmd)
	if err = cmd.Start(); err != nil {
		return result, fail("sandbox_unavailable", "代码隔离执行器不可用，未降级执行")
	}
	writer.Close()
	protocol := make(chan struct {
		data []byte
		err  error
	}, 1)
	go func() {
		data, readErr := io.ReadAll(io.LimitReader(reader, MaxDataBytes+(32<<10)+1))
		if len(data) > MaxDataBytes+(32<<10) {
			cancel()
			readErr = fail("output_limit", "代码输出超过256KiB")
		}
		protocol <- struct {
			data []byte
			err  error
		}{data, readErr}
	}()
	resourceExceeded := make(chan bool, 1)
	monitorDone := make(chan struct{})
	go monitorMemory(ctx, cmd.Process.Pid, monitorDone, resourceExceeded, cancel)
	runErr := cmd.Wait()
	close(monitorDone)
	// Descendants are forbidden by the sandbox. Killing the process group is
	// still necessary on cancellation and after a failed runtime startup.
	killProcessGroup(cmd.Process.Pid)
	frame := <-protocol
	result.Stdout, result.StdoutTruncated = stdout.text()
	result.Stderr, result.StderrTruncated = stderr.text()
	if cmd.ProcessState != nil {
		value := cmd.ProcessState.ExitCode()
		result.ExitCode = &value
	}
	select {
	case <-resourceExceeded:
		return result, fail("memory_limit", "代码执行超过256MiB内存预算")
	default:
	}
	if ctx.Err() != nil {
		if parent.Err() != nil {
			return result, fail("cancelled", "代码执行已取消")
		}
		result.TimedOut = true
		return result, fail("timeout", "代码执行超时")
	}
	if frame.err != nil {
		return result, frame.err
	}
	var reply struct {
		Output    json.RawMessage `json:"output"`
		Error     string          `json:"error"`
		ErrorCode string          `json:"error_code"`
	}
	if len(frame.data) == 0 || json.Unmarshal(frame.data, &reply) != nil {
		if runErr != nil && len(frame.data) == 0 {
			return result, fail("execution_failed", "代码或隔离运行时执行失败，请查看错误日志")
		}
		return result, fail("invalid_output", "代码未返回合法JSON结果")
	}
	if reply.Error != "" {
		return result, fail("code_error", limitText(reply.Error, 2048))
	}
	if runErr != nil {
		return result, fail("execution_failed", "代码执行失败，请查看错误日志")
	}
	if err = validObject(reply.Output); err != nil {
		return result, fail("invalid_output", "main必须返回不超过256KiB的JSON对象，且不含NaN或Infinity")
	}
	result.Output = append(json.RawMessage(nil), reply.Output...)
	return result, nil
}

func validObject(raw []byte) error {
	if len(raw) > MaxDataBytes || !utf8.Valid(raw) {
		return errors.New("invalid JSON size or encoding")
	}
	var value map[string]json.RawMessage
	if json.Unmarshal(raw, &value) != nil || value == nil {
		return errors.New("expected object")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var decoded any
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	return validJSONValue(decoded, 0)
}

func validJSONValue(value any, depth int) error {
	if depth > 32 {
		return errors.New("JSON nesting exceeds 32 levels")
	}
	switch value := value.(type) {
	case json.Number:
		number, err := strconv.ParseFloat(string(value), 64)
		if err != nil || math.IsInf(number, 0) || math.IsNaN(number) {
			return errors.New("non-finite number")
		}
	case map[string]any:
		for _, child := range value {
			if err := validJSONValue(child, depth+1); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range value {
			if err := validJSONValue(child, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

func mustMarshal(value any) []byte { data, _ := json.Marshal(value); return data }
func readableText(value string) string {
	return strings.ReplaceAll(strings.ToValidUTF8(value, "�"), "\x00", `\0`)
}
func limitText(value string, max int) string {
	value = readableText(value)
	if len(value) > max {
		value = value[:max]
		for !utf8.ValidString(value) {
			value = value[:len(value)-1]
		}
	}
	return value
}

type boundedLog struct {
	mu        sync.Mutex
	data      []byte
	truncated bool
}

func (b *boundedLog) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(data)
	remaining := MaxLogBytes - len(b.data)
	if len(b.data) < MaxLogBytes {
		b.data = append(b.data, data[:min(len(data), MaxLogBytes-len(b.data))]...)
	}
	if n > remaining {
		b.truncated = true
	}
	return n, nil
}
func (b *boundedLog) text() (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	text := readableText(string(b.data))
	return limitText(text, MaxLogBytes), b.truncated || len(text) > MaxLogBytes
}

type interpreterSpec struct {
	language, executable, version string
	readDirs                      []string
}

var interpreterCache sync.Map

func interpreter(language string) (interpreterSpec, error) {
	if cached, exists := interpreterCache.Load(language); exists {
		return cached.(interpreterSpec), nil
	}
	name := "python3"
	if language == "javascript" {
		name = "node"
	}
	path, err := exec.LookPath(name)
	if err != nil {
		return interpreterSpec{}, fail("runtime_unavailable", fmt.Sprintf("%s解释器尚未安装", name))
	}
	path, err = resolveInterpreter(path, language)
	if err != nil {
		return interpreterSpec{}, fail("runtime_unavailable", "无法确定可信解释器路径")
	}
	spec := interpreterSpec{language: language, executable: path}
	probeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var args []string
	if language == "python" {
		args = []string{"-I", "-S", "-c", `import json,sys,sysconfig;print(json.dumps({"version":"Python "+sys.version.split()[0],"stdlib":sysconfig.get_path("stdlib"),"prefix":sys.base_prefix}))`}
	} else {
		args = []string{"--no-addons", "-e", `console.log(JSON.stringify({version:"Node.js "+process.versions.node}))`}
	}
	cmd := exec.CommandContext(probeCtx, path, args...)
	cmd.Dir = "/"
	cmd.Env = cleanEnvironment()
	output, err := cmd.Output()
	var info struct{ Version, Stdlib, Prefix string }
	if err != nil || len(output) > 8192 || json.Unmarshal(output, &info) != nil || info.Version == "" {
		return interpreterSpec{}, fail("runtime_unavailable", "解释器版本探测失败")
	}
	spec.version = info.Version
	for _, directory := range []string{info.Stdlib, pythonFrameworkRoot(path)} {
		if directory == "" {
			continue
		}
		if resolved, err := osPath(directory); err == nil {
			spec.readDirs = append(spec.readDirs, resolved)
		}
	}
	interpreterCache.Store(language, spec)
	return spec, nil
}

func cleanEnvironment() []string {
	return []string{"PATH=/nonexistent", "HOME=/nonexistent", "TMPDIR=/nonexistent", "LANG=C.UTF-8", "LC_ALL=C.UTF-8", "TZ=UTC", "PYTHONDONTWRITEBYTECODE=1"}
}
