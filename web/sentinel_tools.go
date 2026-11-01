package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/yosukeno/pogo/sentinel"
	"github.com/yosukeno/pogo/sentinelexec"
	"github.com/yosukeno/pogo/sentinelsdk"
	"github.com/yosukeno/pogo/tools"
)

type sentinelToolBackend struct {
	m                            *Manager
	s                            *Session
	ownerKey, agentID, workspace string
}

func (m *Manager) sentinelBackend(s *Session) tools.WatchBackend {
	if s.studioID == "" || m.sentinels == nil {
		return nil
	}
	return &sentinelToolBackend{m: m, s: s, ownerKey: sessionResultOwnerKey(s.owner), agentID: s.studioID, workspace: s.cwd}
}
func sentinelToolsSelected(selection *[]string) bool {
	return tools.BuiltinSelected(selection, tools.WatchName) || tools.BuiltinSelected(selection, tools.BackgroundTaskName)
}
func (b *sentinelToolBackend) access(ctx context.Context, capability ...string) error {
	name := tools.WatchName
	if len(capability) > 0 {
		name = capability[0]
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	b.s.mu.Lock()
	bound := !b.s.closing && b.s.studioID == b.agentID && b.s.cwd == b.workspace && sessionResultOwnerKey(b.s.owner) == b.ownerKey
	b.s.mu.Unlock()
	if !bound {
		return errors.New("哨兵会话绑定已失效")
	}
	a, ok := b.m.studio.get(b.agentID)
	selected := ok && b.m.studioNativeSelected(a, name)
	if name == tools.WatchName {
		selected = ok && sentinelToolsSelected(b.m.studioNativeSelection(a))
	}
	if !ok || a.Status != "ready" || !selected {
		return errors.New("Agent 或哨兵工具已不可用")
	}
	b.m.mu.Lock()
	valid := b.m.scheduleOwnerValid
	b.m.mu.Unlock()
	if valid == nil || !valid(b.s.owner) {
		return errors.New("哨兵账号不可用或运行时尚未就绪")
	}
	return nil
}
func (b *sentinelToolBackend) owned(db *sentinel.Store, id string) (sentinel.Watch, error) {
	w, e := db.Get(id)
	if e == nil && (w.Spec.OwnerID != b.ownerKey || w.Spec.AgentID != b.agentID) {
		e = sentinel.ErrNotFound
	}
	return w, e
}

// Data grants must never include the host's private control state.
func (m *Manager) validateSentinelWorkspace(workspace string) error {
	roots := []string{m.cfg.SessionDir}
	if home, err := os.UserHomeDir(); err == nil {
		roots = append(roots, filepath.Join(home, ".pogo"))
	}
	for _, root := range roots {
		if root == "" {
			continue
		}
		if canonical, err := filepath.EvalSymlinks(root); err == nil {
			root = canonical
		}
		child, e := filepath.Rel(root, workspace)
		if e == nil && child != ".." && !strings.HasPrefix(child, ".."+string(filepath.Separator)) && !filepath.IsAbs(child) {
			return errors.New("哨兵工作区不可位于服务私有状态目录内")
		}
		rel, err := filepath.Rel(workspace, root)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			return errors.New("哨兵工作区不可包含服务私有状态，请使用独立的 Agent 工作目录")
		}
	}
	return nil
}

type sentinelBundle struct {
	Files      map[string]string `json:"files"`
	SDKVersion string            `json:"sdk_version,omitempty"`
}

func verifySentinelBundle(dir string) error {
	const manifestName = ".pogo-watch-manifest.json"
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	// Open through a directory handle and bound actual reads, rather than
	// trusting a prior size check or following aliases outside the sealed tree.
	readRegular := func(rel string, limit int64) ([]byte, error) {
		before, err := root.Lstat(rel)
		if err != nil || !before.Mode().IsRegular() || before.Size() > limit {
			return nil, errors.New("哨兵脚本不是已保存的普通文件，或超过大小限制")
		}
		f, err := root.Open(rel)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		after, err := f.Stat()
		if err != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) {
			return nil, errors.New("哨兵脚本正在变化，请重新注册版本")
		}
		raw, err := io.ReadAll(io.LimitReader(f, limit+1))
		if err != nil || int64(len(raw)) > limit || int64(len(raw)) != after.Size() {
			return nil, errors.New("哨兵脚本正在变化或超过大小限制")
		}
		return raw, nil
	}
	raw, err := readRegular(manifestName, 64<<10)
	if err != nil {
		return err
	}
	var manifest sentinelBundle
	if json.Unmarshal(raw, &manifest) != nil || len(manifest.Files) == 0 || len(manifest.Files) > 256 {
		return errors.New("哨兵脚本版本清单损坏")
	}
	for rel, digest := range manifest.Files {
		if !filepath.IsLocal(rel) || rel != filepath.Clean(rel) || rel == "." || rel == manifestName || len(digest) != sha256.Size*2 {
			return errors.New("哨兵脚本路径或摘要无效")
		}
		if _, err := hex.DecodeString(digest); err != nil {
			return errors.New("哨兵脚本摘要无效")
		}
	}
	seen := make(map[string]bool, len(manifest.Files))
	var total int64
	directories := 0
	err = fs.WalkDir(root.FS(), ".", func(rel string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("哨兵脚本包包含符号链接，请重新注册版本")
		}
		if entry.IsDir() {
			directories++
			if directories > 512 {
				return errors.New("哨兵脚本包目录过多")
			}
			return nil
		}
		if rel == manifestName {
			return nil
		}
		digest, ok := manifest.Files[rel]
		if !ok {
			return errors.New("哨兵脚本包出现未登记文件，请重新注册版本")
		}
		data, err := readRegular(rel, (32<<20)-total)
		if err != nil {
			return err
		}
		total += int64(len(data))
		hash := sha256.Sum256(data)
		if hex.EncodeToString(hash[:]) != digest {
			return errors.New("哨兵脚本已被修改，请重新注册版本")
		}
		seen[rel] = true
		return nil
	})
	if err != nil {
		return err
	}
	if len(seen) != len(manifest.Files) {
		return errors.New("哨兵脚本包缺少已登记文件，请重新注册版本")
	}
	return nil
}
func (b *sentinelToolBackend) seal(script string, args []string) (string, []string, error) {
	if err := b.m.validateSentinelWorkspace(b.workspace); err != nil {
		return "", nil, err
	}
	if script == "" || filepath.IsAbs(script) || filepath.Clean(script) != script || strings.HasPrefix(script, "..") {
		return "", nil, errors.New("脚本必须位于当前工作区")
	}
	source := filepath.Join(b.workspace, script)
	real, err := filepath.EvalSymlinks(source)
	if err != nil || real != source {
		return "", nil, errors.New("脚本不可用或包含符号链接")
	}
	info, err := os.Stat(source)
	if err != nil || !info.Mode().IsRegular() {
		return "", nil, errors.New("脚本必须是普通文件")
	}
	parent := filepath.Dir(source)
	if parent == b.workspace {
		return "", nil, errors.New("请将脚本和依赖放入独立子目录，例如 sentinels/my-watch/main.py")
	}
	root := filepath.Join(b.m.cfg.SessionDir, "sentinels", "scripts")
	if err = os.MkdirAll(root, 0700); err != nil {
		return "", nil, err
	}
	dir, err := os.MkdirTemp(root, "script-")
	if err != nil {
		return "", nil, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.RemoveAll(dir)
		}
	}()
	workspaceRoot, err := os.OpenRoot(b.workspace)
	if err != nil {
		return "", nil, err
	}
	defer workspaceRoot.Close()
	// Resolve the selected bundle beneath the authorized workspace handle.
	// A concurrent parent rename/symlink must never redirect a read outside it.
	sourceRoot, err := workspaceRoot.OpenRoot(filepath.Dir(script))
	if err != nil {
		return "", nil, err
	}
	defer sourceRoot.Close()
	manifest := sentinelBundle{Files: map[string]string{}}
	total, count := int64(0), 0
	directories := 1
	err = fs.WalkDir(sourceRoot.FS(), ".", func(rel string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if rel == "." {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("脚本包不支持符号链接")
		}
		if entry.IsDir() {
			if strings.HasPrefix(entry.Name(), ".") || entry.Name() == "__pycache__" {
				return filepath.SkipDir
			}
			directories++
			if directories > 512 {
				return errors.New("脚本包目录过多")
			}
			return os.MkdirAll(filepath.Join(dir, rel), 0700)
		}
		if entry.Name() == ".pogo-watch-manifest.json" {
			return errors.New("脚本包包含保留文件名")
		}
		st, e := entry.Info()
		if e != nil {
			return e
		}
		if !st.Mode().IsRegular() {
			return errors.New("脚本包只支持普通文件")
		}
		total += st.Size()
		count++
		if total > 32<<20 || count > 256 {
			return errors.New("脚本和依赖总计最多 32 MiB、256 个文件")
		}
		f, e := sourceRoot.Open(rel)
		if e != nil {
			return e
		}
		defer f.Close()
		opened, e := f.Stat()
		if e != nil || !opened.Mode().IsRegular() || !os.SameFile(st, opened) {
			return errors.New("脚本包正在变化，请停止修改后重试")
		}
		data, e := io.ReadAll(io.LimitReader(f, st.Size()+1))
		if e != nil {
			return e
		}
		if int64(len(data)) != st.Size() {
			return errors.New("脚本包正在变化，请停止修改后重试")
		}
		if entry.Name() == sentinelsdk.FileName {
			if !bytes.Equal(data, sentinelsdk.Source()) {
				return errors.New("pogo_watch.py 是平台协议辅助库，内容已改变；请保留业务入口并重新准备可信 SDK，不要自行改写辅助库")
			}
			manifest.SDKVersion = sentinelsdk.Version
		}
		hash := sha256.Sum256(data)
		manifest.Files[rel] = hex.EncodeToString(hash[:])
		return os.WriteFile(filepath.Join(dir, rel), data, st.Mode().Perm()&0100|0400)
	})
	if err != nil {
		return "", nil, err
	}
	if err = writeSentinelJSON(filepath.Join(dir, ".pogo-watch-manifest.json"), manifest); err != nil {
		return "", nil, err
	}
	entry := filepath.Join(dir, filepath.Base(source))
	var command []string
	switch strings.ToLower(filepath.Ext(source)) {
	case ".py":
		command = []string{"/opt/venv/bin/python3", "-u", entry}
	case ".sh":
		command = []string{"/bin/bash", entry}
	default:
		if info.Mode().Perm()&0111 == 0 {
			return "", nil, errors.New("其他语言脚本需提供可执行文件或解释器启动脚本")
		}
		command = []string{entry}
	}
	command = append(command, args...)
	ok = true
	return dir, command, nil
}
func (b *sentinelToolBackend) Watch(ctx context.Context, raw json.RawMessage) (any, error) {
	if err := b.access(ctx); err != nil {
		return nil, err
	}
	var req tools.WatchRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	if req.Action == "prepare" {
		if err := (&tools.Watch{}).ValidateArgs(raw); err != nil {
			return nil, err
		}
		return b.prepareSDK(ctx, req)
	}
	db, err := b.m.sentinels.database(true)
	if err != nil {
		return nil, err
	}
	if req.Action == "list" {
		return db.List(b.ownerKey, b.agentID)
	}
	if req.Action == "create" {
		dir, command, e := b.seal(req.ScriptPath, req.Args)
		if e != nil {
			return nil, e
		}
		owner, _ := json.Marshal(b.s.owner)
		spec := sentinel.Spec{OwnerID: b.ownerKey, Owner: owner, AgentID: b.agentID, Workspace: b.workspace, ScriptDir: dir, Command: command, Title: req.Title, Task: req.Task, Mode: req.Mode, IntervalSeconds: req.IntervalSeconds, TimeoutSeconds: req.TimeoutSeconds, DebounceMS: req.DebounceMS, MaxTurns: b.m.boundBackgroundMaxTurns(req.MaxTurns)}
		spec.ParentSessionID = b.m.sentinelCreationParent(b.s)
		spec.ExpiresAt, e = tools.ResolveTaskDeadline(req.ExpiresAt, req.StopAfterSeconds, time.Now())
		if e != nil {
			_ = os.RemoveAll(dir)
			return nil, e
		}
		if spec.IntervalSeconds == 0 && spec.Mode == "poll" {
			spec.IntervalSeconds = 60
		}
		if spec.TimeoutSeconds == 0 {
			spec.TimeoutSeconds = 30
		}
		if req.Network != nil {
			spec.Network = *req.Network
		}
		if spec.Network && os.Getenv(tools.EnvSandboxNet) != "1" {
			_ = os.RemoveAll(dir)
			return nil, errors.New("部署未授权脚本网络访问")
		}
		if req.Enabled != nil {
			spec.Enabled = *req.Enabled
		}
		if spec.Enabled && runtime.GOOS != "linux" {
			_ = os.RemoveAll(dir)
			return nil, tools.ErrScopedCommandUnavailable
		}
		w, e := db.Create(spec)
		if e != nil {
			_ = os.RemoveAll(dir)
		}
		return w, e
	}
	w, err := b.owned(db, req.WatchID)
	if err != nil {
		return nil, err
	}
	switch req.Action {
	case "status":
		batches, e := db.ListBatches(w.Spec.ID, 100)
		if e != nil {
			return nil, e
		}
		count, next := sentinelBudget(batches)
		return map[string]any{"watch": w, "batches": batches, "dispatch": map[string]any{"budget_blocked": count >= 100, "approx_recent_attempts": count, "next_budget_check_at": next}, "limits": map[string]any{"pending_events": sentinel.MaxPendingEvents, "batch_events": sentinel.MaxBatchEvents, "event_store_bytes": 256 << 20, "conservative_daily_wakes": 100, "collector_slots": 4, "stream_slots": 3}}, nil
	case "pause":
		b.m.sentinels.mu.Lock()
		out, e := db.SetEnabled(w.Spec.ID, false)
		b.m.sentinels.mu.Unlock()
		b.m.sentinels.stopWatch(w.Spec.ID)
		return out, e
	case "resume":
		if runtime.GOOS != "linux" {
			return nil, tools.ErrScopedCommandUnavailable
		}
		if err = verifySentinelBundle(w.Spec.ScriptDir); err != nil {
			return nil, err
		}
		b.m.sentinels.mu.Lock()
		defer b.m.sentinels.mu.Unlock()
		return db.SetEnabled(w.Spec.ID, true)
	case "retry":
		batches, e := db.ListBatches(w.Spec.ID, 100)
		if e != nil {
			return nil, e
		}
		for _, batch := range batches {
			if batch.Status == "interrupted" {
				return map[string]any{"retry_queued": true, "batch_id": batch.ID}, db.RetryBatch(batch.ID)
			}
		}
		return nil, errors.New("没有需要显式重试的中断批次")
	case "remove":
		b.m.sentinels.stopWatch(w.Spec.ID)
		if e := db.Remove(w.Spec.ID); e != nil {
			return nil, e
		}
		return map[string]any{"removed": true}, nil
	default:
		return nil, errors.New("未知哨兵操作")
	}
}

var (
	errSentinelTestDone    = errors.New("sentinel dry run received its first frame")
	errSentinelTestNoFrame = errors.New("sentinel_stream_test_no_frame")
)

func (b *sentinelToolBackend) Test(ctx context.Context, raw json.RawMessage) (any, error) {
	return b.checkScriptFor(ctx, raw, tools.WatchTestName)
}

// The capability comes from a trusted native entry point, never tool JSON.
// Both entries share one bounded runner, while revoking watch_test still
// revokes the legacy entry independently of background_task.
func (b *sentinelToolBackend) checkScriptFor(ctx context.Context, raw json.RawMessage, capability string) (any, error) {
	if capability != tools.WatchTestName && capability != tools.BackgroundTaskName {
		return nil, errors.New("unsupported sentinel script-check capability")
	}
	if err := b.access(ctx, capability); err != nil {
		return nil, err
	}
	if err := (&tools.WatchTest{}).ValidateArgs(raw); err != nil {
		return nil, err
	}
	var req tools.WatchTestRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return nil, err
	}
	var cfg sentinelexec.Config
	if req.WatchID != "" {
		db, e := b.m.sentinels.database(true)
		if e != nil {
			return nil, e
		}
		w, e := b.owned(db, req.WatchID)
		if e != nil {
			return nil, e
		}
		cfg = sentinelexec.Config{ScriptDir: w.Spec.ScriptDir, Command: w.Spec.Command, Workspace: w.Spec.Workspace, Network: w.Spec.Network, Mode: w.Spec.Mode, State: w.State, Cursor: w.Cursor}
	} else {
		dir, command, e := b.seal(req.ScriptPath, req.Args)
		if e != nil {
			return nil, e
		}
		defer os.RemoveAll(dir)
		cfg = sentinelexec.Config{ScriptDir: dir, Command: command, Workspace: b.workspace, Mode: req.Mode}
		if req.Network != nil {
			cfg.Network = *req.Network
		}
	}
	if cfg.Network && os.Getenv(tools.EnvSandboxNet) != "1" {
		return nil, errors.New("部署未授权脚本网络访问")
	}
	if err := b.m.validateSentinelWorkspace(cfg.Workspace); err != nil {
		return nil, err
	}
	if err := verifySentinelBundle(cfg.ScriptDir); err != nil {
		return nil, err
	}
	timeout := req.TimeoutSeconds
	if timeout == 0 {
		timeout = 10
	}
	testCtx, cancel := context.WithTimeout(ctx, time.Duration(min(max(timeout, 1), 30))*time.Second)
	defer cancel()
	cfg.Timeout = time.Duration(min(max(timeout, 1), 30)) * time.Second
	var frame *sentinel.Frame
	err := b.m.sentinels.runner(testCtx, cfg, func(f sentinel.Frame) error {
		frame = &f
		if cfg.Mode == "stream" {
			return errSentinelTestDone
		}
		return nil
	})
	if errors.Is(err, errSentinelTestDone) {
		err = nil
	}
	if err != nil {
		// A quiet source is a valid operational state, but silence cannot prove
		// that the script implements the boot/frame protocol. Only classify our
		// own bounded stream test; parent cancellation and poll failures retain
		// their original identity and must never become a successful dry run.
		if cfg.Mode == "stream" && frame == nil && errors.Is(err, context.DeadlineExceeded) && testCtx.Err() == context.DeadlineExceeded && ctx.Err() == nil {
			return nil, fmt.Errorf("%w: stream test reached its own timeout without a valid protocol frame; a quiet source does not mean protocol verification succeeded. Emit and flush an events:[] checkpoint on startup or cursor changes, then await the accepted ACK, or use an explicitly authorized test event. Do not claim this test passed or the protocol is verified", errSentinelTestNoFrame)
		}
		return nil, err
	}
	if frame == nil {
		return nil, errors.New("sentinel test ended without a valid protocol frame; protocol verification did not complete")
	}
	return map[string]any{
		"test_only": true, "frame": frame, "agent_woken": false, "cursor_committed": false,
		"verification_scope":           "valid protocol frame; source facts and business conditions require separate evidence",
		"business_conditions_verified": false, "stream_ack_cycle_verified": false,
	}, nil
}
