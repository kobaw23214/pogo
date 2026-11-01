package web

// A transform keeps Python as a first-class document executor while making
// publication of its output the same versioned transaction as a live edit.
// The durable state is written before a script starts: an uncertain script is
// never run again merely because its HTTP/tool reply was lost.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/yosukeno/pogo/tools"
)

const officeTransformMaxScriptBytes = 64 << 10
const officeTransformMaxTimeout = 120 * time.Second

type officeTransformRecord struct {
	Schema        int                `json:"schema"`
	ID            string             `json:"id"`
	Conversation  string             `json:"conversation_id"`
	Path          string             `json:"path"`
	RequestHash   string             `json:"request_hash"`
	ScriptHash    string             `json:"script_hash"`
	OperationID   string             `json:"operation_id"`
	BatchID       string             `json:"batch_id,omitempty"`
	State         string             `json:"state"`
	Command       string             `json:"command,omitempty"`
	Output        string             `json:"output,omitempty"`
	CommandResult *tools.BashDetails `json:"command_result,omitempty"`
	ErrorCode     string             `json:"error_code,omitempty"`
	ErrorMessage  string             `json:"error_message,omitempty"`
	Batch         map[string]any     `json:"batch,omitempty"`
	UpdatedAt     string             `json:"updated_at"`
}

// Ref-counted locks avoid an unbounded map of every past operation. They only
// serialize a live process; the durable running state is the restart boundary.
var officeTransformLocks = struct {
	sync.Mutex
	entries map[string]*officeTransformLock
}{entries: map[string]*officeTransformLock{}}

type officeTransformLock struct {
	mu   sync.Mutex
	refs int
}

func lockOfficeTransform(key string) func() {
	officeTransformLocks.Lock()
	entry := officeTransformLocks.entries[key]
	if entry == nil {
		entry = &officeTransformLock{}
		officeTransformLocks.entries[key] = entry
	}
	entry.refs++
	officeTransformLocks.Unlock()
	entry.mu.Lock()
	return func() {
		entry.mu.Unlock()
		officeTransformLocks.Lock()
		entry.refs--
		if entry.refs == 0 {
			delete(officeTransformLocks.entries, key)
		}
		officeTransformLocks.Unlock()
	}
}

func officeTransformTimeout(seconds float64) (time.Duration, error) {
	if math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 0 || seconds > officeTransformMaxTimeout.Seconds() {
		return 0, officeFail(400, "INVALID_ARGUMENT", "文档脚本 timeout 必须在 0 至 120 秒之间；省略时为 120 秒。")
	}
	if seconds == 0 {
		return officeTransformMaxTimeout, nil
	}
	timeout := time.Duration(seconds * float64(time.Second))
	if timeout <= 0 {
		return 0, officeFail(400, "INVALID_ARGUMENT", "文档脚本 timeout 小于执行时钟精度。")
	}
	return timeout, nil
}

func saveOfficeTransform(filename string, record *officeTransformRecord) error {
	record.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(filename), ".transform-receipt-")
	if err != nil {
		return err
	}
	temp := f.Name()
	defer os.Remove(temp)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(temp, filename); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(filename))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func loadOfficeTransform(filename string) (*officeTransformRecord, error) {
	f, err := os.Open(filename)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return nil, officeFail(409, "TRANSFORM_RECEIPT_INVALID", "文档脚本记录不可用，不能重新执行脚本。")
	}
	var record officeTransformRecord
	decoder := json.NewDecoder(io.LimitReader(f, (1<<20)+1))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&record) != nil || record.Schema != 1 || decoder.Decode(new(any)) != io.EOF {
		return nil, officeFail(409, "TRANSFORM_RECEIPT_INVALID", "文档脚本记录不可用，不能重新执行脚本。")
	}
	return &record, nil
}

func officeTransformView(record *officeTransformRecord) map[string]any {
	executed := record.CommandResult != nil && record.CommandResult.ExitCode >= 0
	executionStatus := "not_started"
	if executed {
		executionStatus = "failed"
		if record.CommandResult.ExitCode == 0 && !record.CommandResult.TimedOut {
			executionStatus = "succeeded"
		}
	} else if record.State == "running" || record.State == "outcome_unknown" {
		executionStatus = "unknown"
	}
	view := map[string]any{
		"ok": record.State == "committed", "state": record.State,
		"operation_id": record.OperationID, "script_hash": record.ScriptHash,
		"executor": "python", "result_published": record.State == "committed",
		"command_executed": executed, "execution_status": executionStatus,
	}
	if record.State == "committed" {
		view["writeOccurred"] = true
	}
	if record.BatchID != "" {
		view["batch_id"] = record.BatchID
	}
	if record.Command != "" {
		view["command"] = record.Command
	}
	if record.Output != "" {
		view["output"] = record.Output
	}
	if record.CommandResult != nil {
		view["command_result"] = record.CommandResult
	}
	if record.ErrorCode != "" {
		view["error"] = map[string]any{"code": record.ErrorCode, "message": record.ErrorMessage}
	}
	if record.Batch != nil {
		view["batch"] = record.Batch
		for _, key := range []string{"file_ref", "base_version", "disk_version", "session", "versionId"} {
			if value, ok := record.Batch[key]; ok {
				view[key] = value
			}
		}
	}
	if record.State == "running" || record.State == "outcome_unknown" || record.State == "committing" {
		// The workspace outcome has not been confirmed. False would claim more
		// than the durable transaction knows, and invites unsafe fallback writes.
		delete(view, "result_published")
		view["outcome_unknown"] = true
	}
	return view
}

func (l *officeLink) executeTransform(ctx context.Context, request officeToolRequest) (json.RawMessage, error) {
	// Check command capability before preparing a batch: preparation can save
	// and freeze a user's live canvas, so rejecting only inside the executor
	// would already have changed the user's editor without a usable runner.
	if l == nil || l.commandSession == nil || !l.commandEnabled || l.commandSession.gate == nil || l.commandSession.ID != l.conversationID || l.commandSession.cwd != l.cwd || !officeSameOwner(l.commandSession.owner, l.owner) {
		return nil, officeFail(403, "COMMAND_UNAVAILABLE", "当前 Agent 没有命令执行权限；本次未准备文档副本或冻结画布。")
	}
	if l.role == nil || l.role.revoked.Load() {
		return nil, officeFail(409, "CONVERSATION_CLOSED", "当前会话已关闭，文档脚本未执行。")
	}
	if !officeOperationID.MatchString(request.OperationID) || strings.TrimSpace(request.Script) == "" || len(request.Script) > officeTransformMaxScriptBytes || strings.ContainsRune(request.Script, 0) {
		return nil, officeFail(400, "INVALID_ARGUMENT", "transform 需要稳定 operation_id、准确 path 和不超过 64 KiB 的 Python script。")
	}
	timeout, err := officeTransformTimeout(request.Timeout)
	if err != nil {
		return nil, err
	}
	absolute, err := l.resolvePath(request.Path)
	if err != nil {
		return nil, officeFail(400, "PATH_REJECTED", "文档不在当前会话工作区。")
	}
	dir, err := l.service.batchRoot(l.conversationID)
	if err != nil {
		return nil, err
	}
	id := officeBatchID(l, "transform_"+request.OperationID)
	filename := filepath.Join(dir, "transform-"+id+".json")
	unlock := lockOfficeTransform(filename)
	defer unlock()
	scriptHash := officeHash([]byte(request.Script))
	requestBytes, _ := json.Marshal([]any{absolute, scriptHash, timeout.Nanoseconds()})
	requestHash := officeHash(requestBytes)
	record, err := loadOfficeTransform(filename)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if record != nil {
		if record.ID != id || record.Conversation != l.conversationID || record.Path != absolute || record.RequestHash != requestHash {
			return nil, officeFail(409, "OPERATION_ID_CONFLICT", "相同 transform operation_id 不能用于不同脚本、文件或超时。")
		}
		switch record.State {
		case "running", "outcome_unknown":
			record.State = "outcome_unknown"
			record.ErrorCode, record.ErrorMessage = "SCRIPT_OUTCOME_UNKNOWN", "原脚本执行结果尚未确认，不能重新执行；请检查原批处理状态。"
			if err := saveOfficeTransform(filename, record); err != nil {
				return nil, err
			}
			return json.Marshal(officeTransformView(record))
		case "committed", "failed", "aborted":
			return json.Marshal(officeTransformView(record))
		case "executed", "committing":
			return l.commitTransform(ctx, filename, record)
		case "preparing", "prepared":
			// No script has been started. Preparing the same stable transaction
			// is safe to recover; execution is fenced by the durable running state.
		default:
			return nil, officeFail(409, "TRANSFORM_RECEIPT_INVALID", "文档脚本状态不正确，不能重新执行。")
		}
	} else {
		record = &officeTransformRecord{Schema: 1, ID: id, Conversation: l.conversationID, Path: absolute, RequestHash: requestHash, ScriptHash: scriptHash, OperationID: request.OperationID, State: "preparing"}
		if err := saveOfficeTransform(filename, record); err != nil {
			return nil, err
		}
	}
	prepareID := "transform_prepare_" + officeHash([]byte(request.OperationID))[:32]
	prepared, err := l.service.prepareBatch(ctx, l, absolute, prepareID, "batch", "")
	if err != nil {
		return l.failTransform(ctx, filename, record, "TRANSFORM_PREPARE_FAILED", err)
	}
	record.Batch = prepared
	record.BatchID, _ = prepared["batch_id"].(string)
	staged, _ := prepared["staged_path"].(string)
	if record.BatchID == "" || staged == "" || prepared["state"] != "prepared" {
		return l.failTransform(ctx, filename, record, "BATCH_NOT_PREPARED", errors.New("文档批处理未返回可执行副本，脚本未运行。"))
	}
	record.State = "prepared"
	if err := saveOfficeTransform(filename, record); err != nil {
		return nil, err
	}
	if err := l.service.setBatchVersionMetadata(l, record.BatchID, officeVersionMetadata{
		ScriptDigest: scriptHash, ExecutionID: record.ID, Summary: "Python 批量修改文档",
	}); err != nil {
		return l.failTransform(ctx, filename, record, "TRANSFORM_METADATA_FAILED", err)
	}
	// The host executor must separately review the real command and Python
	// source under this conversation's existing command permissions. An Office
	// approval alone never authorizes running a script.
	record.State = "running"
	if err := saveOfficeTransform(filename, record); err != nil {
		return nil, err
	}
	result, runErr := l.runOfficeTransformCommand(ctx, request.Script, staged, timeout)
	if details, ok := result.Details.(tools.BashDetails); ok {
		copy := details
		record.CommandResult = &copy
		record.Command = details.Command
	}
	record.Output = result.Text
	if runErr != nil {
		return l.failTransform(ctx, filename, record, "SCRIPT_FAILED", runErr)
	}
	if record.CommandResult == nil || record.CommandResult.ExitCode != 0 || !record.CommandResult.Sandboxed {
		return l.failTransform(ctx, filename, record, "SCRIPT_RECEIPT_INVALID", errors.New("脚本缺少已隔离执行并成功退出的可信回执，未提交文件。"))
	}
	record.State = "executed"
	if err := saveOfficeTransform(filename, record); err != nil {
		// It is safe to leave an uncommitted stage. Never commit after losing
		// the execution result, and never infer that a later retry may rerun it.
		return nil, err
	}
	return l.commitTransform(ctx, filename, record)
}

func (l *officeLink) failTransform(ctx context.Context, filename string, record *officeTransformRecord, code string, cause error) (json.RawMessage, error) {
	record.State, record.ErrorCode = "failed", code
	record.ErrorMessage = cause.Error()
	if len(record.ErrorMessage) > 16<<10 {
		record.ErrorMessage = record.ErrorMessage[:16<<10]
	}
	if record.BatchID != "" {
		// Abort is bookkeeping only and never publishes staged bytes. Use an
		// independent cleanup path so cancellation also releases the lease.
		op := "transform_abort_" + officeHash([]byte(record.OperationID))[:32]
		if batch, err := l.service.abortBatch(l, record.BatchID, op); err == nil {
			record.Batch = batch
		}
	}
	if err := saveOfficeTransform(filename, record); err != nil {
		return nil, err
	}
	return json.Marshal(officeTransformView(record))
}

func (l *officeLink) commitTransform(ctx context.Context, filename string, record *officeTransformRecord) (json.RawMessage, error) {
	record.State = "committing"
	if err := saveOfficeTransform(filename, record); err != nil {
		return nil, err
	}
	op := "transform_commit_" + officeHash([]byte(record.OperationID))[:32]
	batch, err := l.service.commitBatch(ctx, l, record.BatchID, op)
	if err != nil {
		if current, lookupErr := l.service.batchFor(l, record.BatchID); lookupErr == nil {
			current.mu.Lock()
			state := current.record.State
			if state == "failed" || state == "conflict" || state == "aborted" {
				record.State, record.Batch = "failed", l.service.batchView(current)
				record.ErrorCode, record.ErrorMessage = "TRANSFORM_COMMIT_FAILED", err.Error()
				current.mu.Unlock()
				if writeErr := saveOfficeTransform(filename, record); writeErr != nil {
					return nil, writeErr
				}
				return json.Marshal(officeTransformView(record))
			}
			current.mu.Unlock()
		}
		// Committing may have crossed the atomic publish boundary. Preserve the
		// same batch/commit IDs; an identical retry only reconciles that result.
		record.ErrorCode, record.ErrorMessage = "TRANSFORM_COMMIT_PENDING", "文档提交尚未确认；请使用相同 transform 请求核对，不能重新运行脚本。"
		if writeErr := saveOfficeTransform(filename, record); writeErr != nil {
			return nil, writeErr
		}
		return json.Marshal(officeTransformView(record))
	}
	record.Batch = batch
	if batch["state"] == "committed" {
		record.State, record.ErrorCode, record.ErrorMessage = "committed", "", ""
	}
	if err := saveOfficeTransform(filename, record); err != nil {
		return nil, err
	}
	return json.Marshal(officeTransformView(record))
}
