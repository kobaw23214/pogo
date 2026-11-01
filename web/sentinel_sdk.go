package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/yosukeno/pogo/sentinelsdk"
	"github.com/yosukeno/pogo/tools"
)

const sentinelSDKGuide = `Use the installed helper; do not rewrite pogo_watch.py or hand-write protocol frames/ACKs.
API: from pogo_watch import Watcher, event
with Watcher(mode="poll" or "stream") as w:
    w.workspace  # authorized workspace path
    w.state      # deep copy of the last host-confirmed state (may be None)
    w.cursor     # deep copy of the last host-confirmed source cursor (may be None)
    w.emit([event(stable_source_event_key, summary, data)], state=new_state, cursor=new_cursor)
    w.checkpoint(state=new_state, cursor=new_cursor)  # empty events; never wakes a model
    w.fail("capacity_exceeded" or "incomplete_observation" or "source_unavailable" or "history_invalid")
    history = w.history(saved_history, max_entries=128, max_bytes=12288)
    history.seen(stable_source_version); history.remember(stable_source_version)
    history.snapshot()  # include this proposed JSON inside your state; not a commit receipt
state/cursor may be omitted to inherit prior values; explicit None is JSON null. Mutating a returned copy does not commit anything: pass the new value explicitly. Stream state/cursor advance only after a matching ACK; do not catch/ignore SDK protocol errors. Poll proposes exactly one frame, then ends; the host accepts only after successful process exit. The poll client's properties are not a durable-commit receipt.
For poll: query once, compute events and state/cursor, then make one emit or checkpoint as the final operation. No sleep/loop is needed: the host schedules the next poll.
For stream: connect/authenticate your authorized source using w.cursor, then call w.checkpoint() to demonstrate protocol readiness. For every new source record, emit an event if its business condition matches; otherwise checkpoint its cursor so quiet records also persist progress. The SDK handles the ACK wait before the next record. The source subscription stays in your own script. On source EOF or I/O failure, raise/exit rather than silently sleeping forever: this same host supervisor can restart the script with its last accepted state/cursor. No extra reconnect/ACK framework is required.
A restored cursor means continuation, not a fresh baseline. Assess each record after the restored position using source identity/version rules; never discard the first post-restart record merely because it arrived first. If the source returns only records after a cursor, those records are not an initial baseline.
Choose stable source event/version keys; the host's durable queue deduplicates replays. The same key must retain the same summary/data, including after a path disappears or is renamed. Do not add current timestamps to an immutable event or keep an unbounded seen-ID list. The SDK computes distinct frame IDs for event-to-quiet transitions while preserving exact replay IDs.
For an explicitly requested periodic measurement, each interval's timestamped sample is a distinct observation even when the value is unchanged. Preserve every requested sample using its own identity; only deduplicate retries of that same sample. Do not substitute change-only reporting for periodic sampling. This differs from replaying an existing immutable source event with a fabricated new timestamp.
Fail closed on incomplete observations: a self-imposed byte/row/item limit, truncated page, unreadable source or insufficient identity history is not an empty/no-change result. Call w.fail with a fixed code BEFORE advancing that observation's cursor. It emits no success frame, poisons the client even if caught, and uses reserved exit codes 80-83 so the host can show a safe reason without echoing sensitive source logs. The host still controls retries/backoff; a failed poll cannot commit a partial proposal, while a stream retains prior ACKed progress. Do not silently report partial totals as complete, convert missing values to zero, ignore oversized inputs, evict needed identities, or reset a saved cursor to make a checkpoint fit. Input still being produced may remain explicitly pending and retryable for a later poll; its consumption cursor must not advance past it and it must not be marked complete or ignored.
Optional bounded history never evicts: restore its exact saved value (None only for genuinely new history), call seen/remember using stable source versions, and place snapshot() inside the next proposed state. Count and byte limits use the host's escaped JSON encoding. A full or incompatible history stops visibly instead of discarding older keys; the enclosing state still has the 16 KiB limit. SDK state/cursor/frame/event size ceilings raise CapacityError (an ObservationFailure/SystemExit, exit 80), not an ordinary JSON-format ValidationError. This includes extra state added around a valid history. Inside with Watcher, even a caught stateless event-construction size error poisons that client; use the context manager and never replace a capacity failure with a quiet fallback. This finite helper does not provide permanent/unbounded deduplication; if required history cannot fit, use a durable source cursor or explicitly surface the unsupported capacity. The platform cannot infer completeness when arbitrary business code deliberately ignores input or suppresses errors.
SDK 2 keeps wire protocol v1 and the existing valid emit/checkpoint APIs. New prepare directories receive SDK 2; existing files are never overwritten. Older sealed bundles keep their pinned helper and state/cursor, without automatic upgrade or reset. To adopt new helpers, preserve the prior source state contract, prepare in a new directory, re-test and explicitly replace the registration; do not imply cross-registration identity history migrates automatically.
No source client, network permission, model API, or external action is supplied by this helper. Network access still needs explicit network=true in background_task check and create (or legacy watch_test when that is the only granted trial tool), under the existing approval policy. Logs go to stderr, never stdout; do not log credentials. An initial checkpoint validates the protocol, not business truth or all source conditions. The starter deliberately raises NotImplementedError until you replace its business code. When background_task is available, run it with action=check, source=sentinel on the implemented entry and inspect its actual result before create. A watch-only session may use its separately granted legacy watch_test instead; neither entry grants a missing capability. Check validates a first protocol frame only and never registers, enables, commits saved progress or wakes a model; missing frames remain unverified. Do not use extra mock servers or long empty waits when a short source check suffices.`

const sentinelSDKVersionConflict = "pogo_watch.py 与当前 SDK 版本不一致（可能是旧版或内容改动）；准备操作不会覆盖已有文件。请保留业务代码与已确认进度，在新目录准备并测试当前 SDK 后显式替换注册。旧封存版本不会自动升级或重置游标"

// prepareSDK only publishes constant helper/template files. It does not open
// the event store or grant execution/network rights. Identity remains host-bound.
func (b *sentinelToolBackend) prepareSDK(ctx context.Context, req tools.WatchRequest) (any, error) {
	if err := b.m.validateSentinelWorkspace(b.workspace); err != nil {
		return nil, err
	}
	canonical, err := filepath.EvalSymlinks(b.workspace)
	if err != nil || canonical != b.workspace {
		return nil, errors.New("哨兵工作区路径已变化或包含符号链接")
	}
	before, err := os.Lstat(b.workspace)
	if err != nil || !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("哨兵工作区必须是原绑定目录")
	}
	root, err := os.OpenRoot(b.workspace)
	if err != nil {
		return nil, err
	}
	actual, err := root.Stat(".")
	if err != nil || !os.SameFile(before, actual) {
		root.Close()
		return nil, errors.New("哨兵工作区已改变，请重新检查")
	}
	parent, err := sentinelSDKDirectory(root, filepath.Dir(req.ScriptPath))
	root.Close()
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	entry := filepath.Base(req.ScriptPath)
	// Preflight both destinations before publishing either one. All existing
	// files are preserved, including a conflicting/tampered helper.
	if _, err = sentinelSDKExisting(parent, entry, nil); err != nil {
		return nil, err
	}
	source := sentinelsdk.Source()
	if _, err = sentinelSDKExisting(parent, sentinelsdk.FileName, source); err != nil {
		return nil, err
	}
	starter, err := sentinelsdk.Starter(req.Mode)
	if err != nil {
		return nil, err
	}
	sdkCreated, err := sentinelSDKPublish(ctx, parent, sentinelsdk.FileName, source, true)
	if err != nil {
		return nil, err
	}
	entryCreated, err := sentinelSDKPublish(ctx, parent, entry, starter, false)
	if err != nil {
		return nil, err
	}
	if exists, err := sentinelSDKExisting(parent, sentinelsdk.FileName, source); err != nil {
		return nil, err
	} else if !exists {
		return nil, errors.New("SDK 文件已变化，请重试准备")
	}
	if exists, err := sentinelSDKExisting(parent, entry, nil); err != nil {
		return nil, err
	} else if !exists {
		return nil, errors.New("业务入口已变化，请重试准备")
	}
	return map[string]any{
		"sdk_version": sentinelsdk.Version, "sdk_path": filepath.ToSlash(filepath.Join(filepath.Dir(req.ScriptPath), sentinelsdk.FileName)),
		"script_path": req.ScriptPath, "mode": req.Mode, "sdk_created": sdkCreated, "entry_created": entryCreated,
		"documentation": sentinelSDKGuide, "registered": false, "enabled": false,
	}, nil
}

// Walk with directory handles. Root confines access but also permits symlinks
// inside itself, so explicitly refuse aliases at every level as well.
func sentinelSDKDirectory(root *os.Root, relative string) (*os.Root, error) {
	current, err := root.OpenRoot(".")
	if err != nil {
		return nil, err
	}
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		if part == "" || part == "." || part == ".." {
			current.Close()
			return nil, errors.New("SDK 需要独立的工作区子目录")
		}
		before, err := current.Lstat(part)
		if errors.Is(err, os.ErrNotExist) {
			if err = current.Mkdir(part, 0700); err != nil && !errors.Is(err, os.ErrExist) {
				current.Close()
				return nil, err
			}
			before, err = current.Lstat(part)
		}
		if err != nil || !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
			current.Close()
			return nil, errors.New("SDK 目录不可为符号链接或普通文件")
		}
		next, err := current.OpenRoot(part)
		if err != nil {
			current.Close()
			return nil, err
		}
		actual, statErr := next.Stat(".")
		after, afterErr := current.Lstat(part)
		current.Close()
		if statErr != nil || afterErr != nil || after.Mode()&os.ModeSymlink != 0 || !os.SameFile(before, actual) || !os.SameFile(before, after) {
			next.Close()
			return nil, errors.New("SDK 目录已变化，请重试")
		}
		current = next
	}
	return current, nil
}

func sentinelSDKExisting(root *os.Root, name string, expected []byte) (bool, error) {
	before, err := root.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 {
		return false, fmt.Errorf("%s 必须是普通文件，不能使用符号链接", name)
	}
	file, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return false, err
	}
	defer file.Close()
	actual, err := file.Stat()
	if err != nil || !actual.Mode().IsRegular() || !os.SameFile(before, actual) {
		return false, fmt.Errorf("%s 已变化，请重试", name)
	}
	if expected != nil {
		if actual.Size() != int64(len(expected)) {
			return false, errors.New(sentinelSDKVersionConflict)
		}
		data, err := io.ReadAll(io.LimitReader(file, int64(len(expected))+1))
		if err != nil {
			return false, err
		}
		if !bytes.Equal(data, expected) {
			return false, errors.New(sentinelSDKVersionConflict)
		}
	}
	return true, nil
}

// Publish complete files with a no-replace hard link. Rename would clobber an
// entry another writer created after our check. A failed second publication
// leaves a valid helper in place for an idempotent retry, never rolls it back.
func sentinelSDKPublish(ctx context.Context, root *os.Root, name string, data []byte, sdk bool) (bool, error) {
	var expected []byte
	if sdk {
		expected = data
	}
	if exists, err := sentinelSDKExisting(root, name, expected); exists || err != nil {
		return false, err
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	temp := ".pogo-sdk-prepare-" + rand.Text()
	file, err := root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return false, err
	}
	defer root.Remove(temp)
	if _, err = file.Write(data); err == nil {
		err = file.Sync()
	}
	info, statErr := file.Stat()
	closeErr := file.Close()
	if err != nil {
		return false, err
	}
	if statErr != nil {
		return false, statErr
	}
	if closeErr != nil {
		return false, closeErr
	}
	if err = ctx.Err(); err != nil {
		return false, err
	}
	if err = root.Link(temp, name); err != nil {
		if errors.Is(err, os.ErrExist) {
			var exists bool
			exists, err = sentinelSDKExisting(root, name, expected)
			if err == nil && !exists {
				err = fmt.Errorf("%s 已变化，请重试准备", name)
			}
		}
		return false, err
	}
	published, err := root.Lstat(name)
	if err != nil || !published.Mode().IsRegular() || !os.SameFile(info, published) {
		return false, fmt.Errorf("%s 发布期间发生变化，请检查后重试", name)
	}
	directory, err := root.Open(".")
	if err != nil {
		return false, err
	}
	err = directory.Sync()
	directory.Close()
	return true, err
}
