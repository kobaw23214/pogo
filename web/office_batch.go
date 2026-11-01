package web

// Batch edits operate on an owned staging file. Neither a tool-supplied command
// nor an arbitrary path is executed by the Office service. The receipt, freeze
// handshake and file compare-and-swap are the authority for committing a result.
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/tools"
)

type officeDiscardConfirmation struct {
	InstanceID    string `json:"instanceId"`
	CanvasVersion string `json:"confirmedCanvasVersion"`
	DirtyEpoch    uint64 `json:"confirmedDirtyEpoch"`
}
type officeFreezeView struct {
	ExpiresAt              string `json:"expiresAt,omitempty"`
	DiscardChanges         bool   `json:"discardChanges"`
	ConfirmedCanvasVersion string `json:"confirmedCanvasVersion,omitempty"`
	ConfirmedDirtyEpoch    uint64 `json:"confirmedDirtyEpoch,omitempty"`
	ID                     string `json:"id"`
	Purpose                string `json:"purpose"`
	Phase                  string `json:"phase"`
}
type officeFreeze struct {
	expiresAt          string
	discard            *officeDiscardConfirmation
	id, purpose, phase string
	done               chan error
	acknowledged       bool
}

func (d *officeDocumentSession) freezeViewLocked() *officeFreezeView {
	if d.freeze == nil {
		return nil
	}
	v := &officeFreezeView{ID: d.freeze.id, Purpose: d.freeze.purpose, Phase: d.freeze.phase, ExpiresAt: d.freeze.expiresAt}
	if c := d.freeze.discard; c != nil {
		v.DiscardChanges = true
		v.ConfirmedCanvasVersion = c.CanvasVersion
		v.ConfirmedDirtyEpoch = c.DirtyEpoch
	}
	return v
}
func officeFileIdentity(info os.FileInfo) string {
	if info == nil {
		return ""
	}
	v := reflect.Indirect(reflect.ValueOf(info.Sys()))
	if !v.IsValid() || v.Kind() != reflect.Struct {
		return ""
	}
	dev, ino := v.FieldByName("Dev"), v.FieldByName("Ino")
	if !dev.IsValid() || !ino.IsValid() {
		return ""
	}
	return fmt.Sprint(dev.Interface(), ":", ino.Interface())
}
func officeOpenParent(cwd, absolute string) (*os.Root, string, os.FileInfo, error) {
	relative, err := filepath.Rel(cwd, absolute)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) || filepath.IsAbs(relative) {
		return nil, "", nil, officeFail(400, "PATH_REJECTED", "文档不在当前会话工作区。")
	}
	root, err := os.OpenRoot(cwd)
	if err != nil {
		return nil, "", nil, err
	}
	defer root.Close()
	parent, err := root.OpenRoot(filepath.Dir(relative))
	if err != nil {
		return nil, "", nil, err
	}
	info, err := parent.Stat(".")
	if err != nil {
		parent.Close()
		return nil, "", nil, err
	}
	return parent, filepath.Base(relative), info, nil
}
func officeDiskRead(cwd, absolute string) ([]byte, os.FileInfo, os.FileInfo, error) {
	parent, name, pinfo, err := officeOpenParent(cwd, absolute)
	if err != nil {
		return nil, nil, nil, err
	}
	defer parent.Close()
	data, info, err := officeReadFile(parent, name)
	if err != nil {
		return nil, nil, nil, err
	}
	after, err := parent.Lstat(name)
	if err != nil {
		return nil, nil, nil, err
	}
	check, _, latestParent, err := officeOpenParent(cwd, absolute)
	if err != nil {
		return nil, nil, nil, err
	}
	check.Close()
	if !os.SameFile(pinfo, latestParent) || !os.SameFile(info, after) || info.Size() != after.Size() || !info.ModTime().Equal(after.ModTime()) {
		return nil, nil, nil, officeFail(409, "FILE_CHANGED", "文件正在被其他操作修改，请稍后核对。")
	}
	return data, info, pinfo, nil
}

func (o *officeService) observeDisk(d *officeDocumentSession) { o.observeDiskVersion(d, false) }
func (o *officeService) observeDiskVersion(d *officeDocumentSession, force bool) {
	// This lock serializes I/O observations, independently of the editor lock.
	// A save may continue while hashing; its changed base invalidates this result.
	d.diskInspectMu.Lock()
	defer d.diskInspectMu.Unlock()
	d.mu.Lock()
	if d.closed || d.readOnly {
		d.mu.Unlock()
		return
	}
	base, revision := d.baseHash, d.revision
	cwd, absolute := d.cwd, d.absolute
	previousSignature, last := d.diskSignature, d.diskObservedAt
	expectedParent := d.parentInfo
	d.mu.Unlock()
	parent, name, pinfo, err := officeOpenParent(cwd, absolute)
	var stat os.FileInfo
	if err == nil {
		stat, err = parent.Lstat(name)
		parent.Close()
	}
	signature := ""
	if err == nil {
		signature = fmt.Sprintf("%s:%d:%d:%s", officeFileIdentity(stat), stat.Size(), stat.ModTime().UnixNano(), officeFileIdentity(pinfo))
	}
	if !force && err == nil && signature == previousSignature && time.Since(last) < 30*time.Second {
		return
	}
	var data []byte
	var info os.FileInfo
	if err == nil {
		data, info, pinfo, err = officeDiskRead(cwd, absolute)
	}
	version, reason := "", ""
	if err != nil {
		reason = "FILE_UNAVAILABLE"
		if errors.Is(err, fs.ErrNotExist) {
			reason = "FILE_MISSING"
		}
	} else {
		version = officeHash(data)
		if !os.SameFile(pinfo, expectedParent) {
			reason = "FILE_IDENTITY_CHANGED"
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed || d.baseHash != base || d.revision != revision {
		return
	}
	d.diskReadCount++
	d.diskObservedAt = time.Now()
	d.diskSignature = signature
	changed := err != nil || reason != "" || version != d.baseHash || !os.SameFile(info, d.baseInfo)
	if d.diskVersion != version || d.externalChange != changed || d.versionError != reason {
		d.syncRevision++
	}
	d.diskVersion, d.externalChange, d.versionError = version, changed, reason
}

type officeBatchRecord struct {
	VersionID        string                     `json:"version_id,omitempty"`
	VersionMetadata  officeVersionMetadata      `json:"version_metadata,omitempty"`
	ExpiresAt        string                     `json:"expires_at,omitempty"`
	Discard          *officeDiscardConfirmation `json:"discard,omitempty"`
	ProposedIdentity string                     `json:"proposed_identity,omitempty"`
	Schema           int                        `json:"schema"`
	ID               string                     `json:"batch_id"`
	Owner            *identity.Scope            `json:"owner"`
	ConversationID   string                     `json:"conversation_id"`
	Cwd              string                     `json:"cwd"`
	Absolute         string                     `json:"absolute"`
	PanelPath        string                     `json:"path"`
	FileRef          string                     `json:"file_ref"`
	FileType         string                     `json:"file_type"`
	PrepareOperation string                     `json:"prepare_operation"`
	CommitOperation  string                     `json:"commit_operation,omitempty"`
	AbortOperation   string                     `json:"abort_operation,omitempty"`
	Purpose          string                     `json:"purpose"`
	ExpectedVersion  string                     `json:"expected_version,omitempty"`
	BootEpoch        string                     `json:"boot_epoch"`
	State            string                     `json:"state"`
	StagedPath       string                     `json:"staged_path,omitempty"`
	StageDirIdentity string                     `json:"stage_dir_identity,omitempty"`
	BaseVersion      string                     `json:"base_version,omitempty"`
	BaseIdentity     string                     `json:"base_identity,omitempty"`
	ParentIdentity   string                     `json:"parent_identity,omitempty"`
	ProposedVersion  string                     `json:"proposed_version,omitempty"`
	DiskVersion      string                     `json:"disk_version,omitempty"`
	OldSessionID     string                     `json:"old_session_id,omitempty"`
	NewSessionID     string                     `json:"new_session_id,omitempty"`
	ErrorCode        string                     `json:"error_code,omitempty"`
	UpdatedAt        string                     `json:"updated_at"`
}
type officeBatch struct {
	timer  *time.Timer
	mu     sync.Mutex
	record officeBatchRecord
}

func officeBatchID(l *officeLink, op string) string {
	body, _ := json.Marshal([]any{l.owner, l.conversationID, l.cwd, op})
	h := officeHash(body)
	// A stable UUID-shaped scoped operation key makes a prepare retry recover the
	// same receipt after restart; this is not an authorization capability.
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}
func (o *officeService) batchRoot(conversationID string) (string, error) {
	if !validID(conversationID) {
		return "", officeFail(400, "INVALID_ARGUMENT", "会话身份不正确。")
	}
	root := o.batchDir
	if root == "" && o.manager != nil {
		root = filepath.Join(o.manager.cfg.SessionDir, "office-batches")
	}
	if root == "" {
		return "", officeFail(503, "BATCH_STORAGE_UNAVAILABLE", "当前服务未配置持久文档事务目录。")
	}
	dir := filepath.Join(root, conversationID)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	return dir, nil
}
func (o *officeService) saveBatch(b *officeBatch) error {
	dir, err := o.batchRoot(b.record.ConversationID)
	if err != nil {
		return err
	}
	b.record.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	data, err := json.Marshal(b.record)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".receipt-")
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
	if err = os.Rename(temp, filepath.Join(dir, b.record.ID+".json")); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
func (o *officeService) batchFor(l *officeLink, id string) (*officeBatch, error) {
	if !officeSessionID.MatchString(id) {
		return nil, officeFail(400, "INVALID_BATCH_ID", "批处理身份不正确。")
	}
	o.batchMu.Lock()
	defer o.batchMu.Unlock()
	key := l.conversationID + "/" + id
	if b := o.batches[key]; b != nil {
		if !b.matches(l) {
			return nil, officeFail(404, "BATCH_NOT_FOUND", "当前会话没有此文档批处理。")
		}
		return b, nil
	}
	dir, err := o.batchRoot(l.conversationID)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, id+".json")
	st, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, officeFail(404, "BATCH_NOT_FOUND", "当前会话没有此文档批处理。")
	}
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Size() > 32768 {
		return nil, officeFail(409, "BATCH_RECEIPT_INVALID", "批处理记录无法安全读取。")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	b := &officeBatch{}
	if json.Unmarshal(data, &b.record) != nil || b.record.Schema != 1 || b.record.ID != id || !b.matches(l) {
		return nil, officeFail(404, "BATCH_NOT_FOUND", "当前会话没有此文档批处理。")
	}
	if o.batches == nil {
		o.batches = map[string]*officeBatch{}
	}
	o.batches[key] = b
	return b, nil
}
func (b *officeBatch) matches(l *officeLink) bool {
	return b.record.ConversationID == l.conversationID && b.record.Cwd == l.cwd && officeSameOwner(b.record.Owner, l.owner)
}
func (o *officeService) batchView(b *officeBatch) map[string]any {
	r := b.record
	out := map[string]any{"ok": r.State == "prepared" || r.State == "committed" || r.State == "aborted", "batch_id": r.ID, "state": r.State, "file_ref": r.FileRef, "base_version": r.BaseVersion, "disk_version": r.DiskVersion, "expires_at": r.ExpiresAt}
	if r.VersionID != "" {
		out["versionId"] = r.VersionID
	}
	if r.State == "prepared" {
		out["staged_path"] = r.StagedPath
	}
	if r.ErrorCode != "" {
		out["error_code"] = r.ErrorCode
	}
	if r.NewSessionID != "" {
		o.mu.Lock()
		d := o.sessions[r.NewSessionID]
		o.mu.Unlock()
		if d != nil {
			out["session"] = d.metadataView()
		}
	}
	return out
}
func (o *officeService) reconcileBatch(b *officeBatch) error {
	o.mu.Lock()
	epoch := o.epochLocked()
	o.mu.Unlock()
	if b.record.BootEpoch == epoch && b.record.State != "committing" {
		return nil
	}
	switch b.record.State {
	case "committing":
		data, info, _, err := officeDiskRead(b.record.Cwd, b.record.Absolute)
		if err == nil && officeHash(data) == b.record.ProposedVersion && officeFileIdentity(info) == b.record.ProposedIdentity && b.record.ProposedIdentity != "" {
			b.record.State = "committed"
			b.record.DiskVersion = b.record.ProposedVersion
			b.record.ErrorCode = ""
		} else if err == nil && officeHash(data) == b.record.BaseVersion && officeFileIdentity(info) == b.record.BaseIdentity {
			b.record.State = "interrupted"
			b.record.ErrorCode = "BATCH_INTERRUPTED"
		} else {
			b.record.State = "outcome_unknown"
			b.record.ErrorCode = "BATCH_OUTCOME_UNKNOWN"
		}
	case "preparing", "prepared":
		b.record.State = "interrupted"
		b.record.ErrorCode = "BATCH_INTERRUPTED"
	default:
		return nil
	}
	return o.saveBatch(b)
}
func (o *officeService) findDocument(absolute string) *officeDocumentSession {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.sessions[o.active[absolute]]
}
func (o *officeService) reserveBatch(l *officeLink, b *officeBatch) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed || l.role.revoked.Load() {
		return officeFail(409, "CONVERSATION_CLOSED", "会话已关闭。")
	}
	if o.batchLeases == nil {
		o.batchLeases = map[string]string{}
	}
	if held := o.batchLeases[b.record.Absolute]; held != "" && held != b.record.ID {
		return officeFail(409, "DOCUMENT_BATCH_ACTIVE", "此文件已有未完成的批处理。")
	}
	if d := o.sessions[o.active[b.record.Absolute]]; d != nil {
		if d.conversationID != l.conversationID || d.cwd != l.cwd || !officeSameOwner(d.owner, l.owner) {
			return officeFail(409, "DOCUMENT_ALREADY_OPEN", "文件已在其他会话打开。")
		}
	}
	o.batchLeases[b.record.Absolute] = b.record.ID
	return nil
}
func (o *officeService) releaseBatch(b *officeBatch) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.batchLeases[b.record.Absolute] == b.record.ID {
		delete(o.batchLeases, b.record.Absolute)
	}
	if d := o.sessions[b.record.OldSessionID]; d != nil {
		d.mu.Lock()
		if d.batchLease == b.record.ID {
			d.batchLease = ""
			d.freeze = nil
			d.syncRevision++
		}
		d.mu.Unlock()
	}
}
func (o *officeService) cleanStage(b *officeBatch) {
	r := b.record
	if r.StagedPath == "" || filepath.Dir(r.StagedPath) != filepath.Join(r.Cwd, ".pogo-office-batch-"+r.ID) {
		return
	}
	parent, name, pinfo, err := officeOpenParent(r.Cwd, r.StagedPath)
	if err != nil {
		return
	}
	defer parent.Close()
	if officeFileIdentity(pinfo) != r.StageDirIdentity {
		return
	}
	if st, err := parent.Lstat(name); err == nil && st.Mode().IsRegular() {
		_ = parent.Remove(name)
	}
	cwd, err := os.OpenRoot(r.Cwd)
	if err != nil {
		return
	}
	defer cwd.Close()
	dir := filepath.Base(filepath.Dir(r.StagedPath))
	if st, err := cwd.Lstat(dir); err == nil && officeFileIdentity(st) == r.StageDirIdentity {
		_ = cwd.Remove(dir)
	}
}

func (o *officeService) freezeDocument(ctx context.Context, d *officeDocumentSession, b *officeBatch) error {
	if err := o.waitRecovery(ctx, d); err != nil {
		return err
	}
	o.observeDiskVersion(d, true)
	d.mu.Lock()
	if d.closed || d.blocked || d.replacementPending || d.freeze != nil || d.batchLease != "" {
		d.mu.Unlock()
		return officeFail(409, "EDITOR_NOT_READY", "文档当前不能安全开始版本切换。")
	}
	if b.record.Purpose == "batch" && d.externalChange {
		d.mu.Unlock()
		return officeFail(409, "EXTERNAL_CHANGE", "磁盘文件已有新版本，请先核对并载入。")
	}
	if d.instanceID == "" && !d.canvasMounted {
		d.batchLease = b.record.ID
		d.syncRevision++
		d.mu.Unlock()
		return nil
	}
	if !d.ready {
		d.mu.Unlock()
		return officeFail(409, "EDITOR_NOT_READY", "请保持当前编辑器连接后重试。")
	}
	f := &officeFreeze{discard: b.record.Discard, id: b.record.ID, purpose: b.record.Purpose, phase: "freeze_requested", done: make(chan error, 1)}
	d.freeze = f
	d.batchLease = b.record.ID
	d.syncRevision++
	d.mu.Unlock()
	timer := time.NewTimer(45 * time.Second)
	defer timer.Stop()
	select {
	case err := <-f.done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return officeFail(409, "FREEZE_TIMEOUT", "编辑器未能确认冻结和保存；本次未改动文件。")
	}
}
func (o *officeService) prepareBatch(ctx context.Context, l *officeLink, path, op, purpose, expected string) (map[string]any, error) {
	return o.prepareBatchConfirmed(ctx, l, path, op, purpose, expected, nil)
}
func (o *officeService) prepareBatchConfirmed(ctx context.Context, l *officeLink, path, op, purpose, expected string, discard *officeDiscardConfirmation) (map[string]any, error) {
	if !officeOperationID.MatchString(op) {
		return nil, officeFail(400, "INVALID_OPERATION_ID", "请提供有效操作 ID。")
	}
	absolute, err := l.resolvePath(path)
	if err != nil {
		return nil, officeFail(400, "PATH_REJECTED", "文档不在当前会话工作区。")
	}
	panel, err := officePanelPath(l.workspaceRoot(), absolute)
	if err != nil {
		return nil, err
	}
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(absolute)), ".")
	if ext != "xlsx" && ext != "docx" {
		return nil, officeFail(400, "UNSUPPORTED_DOCUMENT", "批处理仅支持 .xlsx 和 .docx。")
	}
	if err := o.registerCommandProtectedDocument(l, absolute); err != nil {
		return nil, err
	}
	id := officeBatchID(l, op)
	b, err := o.batchFor(l, id)
	if err == nil {
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.record.Absolute != absolute || b.record.Purpose != purpose || b.record.ExpectedVersion != expected || !officeDiscardEqual(b.record.Discard, discard) {
			return nil, officeFail(409, "OPERATION_ID_CONFLICT", "相同操作 ID 不能用于不同文件或请求。")
		}
		if err = o.reconcileBatch(b); err != nil {
			return nil, err
		}
		return o.batchView(b), nil
	}
	var typed *officeError
	if !errors.As(err, &typed) || typed.code != "BATCH_NOT_FOUND" {
		return nil, err
	}
	o.mu.Lock()
	epoch := o.epochLocked()
	o.mu.Unlock()
	b = &officeBatch{record: officeBatchRecord{Discard: discard, Schema: 1, ID: id, Owner: officeCopyOwner(l.owner), ConversationID: l.conversationID, Cwd: l.cwd, Absolute: absolute, PanelPath: panel, FileRef: officeFileRef(l.owner, panel), FileType: ext, PrepareOperation: op, Purpose: purpose, ExpectedVersion: expected, BootEpoch: epoch, State: "preparing"}}
	b.mu.Lock()
	defer b.mu.Unlock()
	o.batchMu.Lock()
	key := l.conversationID + "/" + id
	if o.batches == nil {
		o.batches = map[string]*officeBatch{}
	}
	if existing := o.batches[key]; existing != nil {
		o.batchMu.Unlock()
		return nil, officeFail(409, "BATCH_PENDING", "相同准备请求正在执行，请查询批处理状态。")
	}
	o.batches[key] = b
	o.batchMu.Unlock()
	if err = o.saveBatch(b); err != nil {
		return nil, err
	}
	fail := func(cause error) (map[string]any, error) {
		b.record.State = "failed"
		b.record.ErrorCode = "BATCH_PREPARE_FAILED"
		if errors.As(cause, &typed) {
			b.record.ErrorCode = typed.code
		}
		writeErr := o.saveBatch(b)
		o.releaseBatch(b)
		o.cleanStage(b)
		if writeErr != nil {
			return nil, writeErr
		}
		return nil, cause
	}
	if err = o.reserveBatch(l, b); err != nil {
		return fail(err)
	}
	d := o.findDocument(absolute)
	if d != nil {
		b.record.OldSessionID = d.id
		if err = o.saveBatch(b); err != nil {
			return fail(err)
		}
		if err = o.freezeDocument(ctx, d, b); err != nil {
			return fail(err)
		}
	}
	err = tools.WithFileMutationLock(absolute, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		data, info, pinfo, err := officeDiskRead(l.cwd, absolute)
		if err != nil {
			return err
		}
		if err = officeCheckPackage(data, ext); err != nil {
			return err
		}
		version := officeHash(data)
		if expected != "" && expected != version {
			return officeFail(409, "FILE_CHANGED", "磁盘文件再次变化，请重新核对版本。")
		}
		if d != nil {
			d.mu.Lock()
			defer d.mu.Unlock()
			if d.closed || d.blocked || !officeBatchCanvasSafe(d, b.record.Discard) || d.saving || d.exportBusy || officePendingLocked(d) {
				return officeFail(409, "UNSAVED_DOCUMENT", "旧画布仍有未保存修改或未完成操作。")
			}
			if purpose == "batch" && (version != d.baseHash || !os.SameFile(info, d.baseInfo)) {
				return officeFail(409, "FILE_CHANGED", "文件在准备期间改变，本次未交付副本。")
			}
			if d.freeze != nil && (!d.freeze.acknowledged || d.freeze.phase != "frozen") {
				return officeFail(409, "FREEZE_REQUIRED", "编辑器尚未确认冻结。")
			}
		}
		b.record.BaseVersion = version
		b.record.DiskVersion = version
		b.record.BaseIdentity = officeFileIdentity(info)
		b.record.ParentIdentity = officeFileIdentity(pinfo)
		if b.record.BaseIdentity == "" || b.record.ParentIdentity == "" {
			return officeFail(409, "FILE_IDENTITY_CHANGED", "无法确认文件身份。")
		}
		if purpose == "batch" {
			dir := filepath.Join(l.cwd, ".pogo-office-batch-"+id)
			cwdRoot, rootErr := os.OpenRoot(l.cwd)
			if rootErr != nil {
				return rootErr
			}
			defer cwdRoot.Close()
			if err = cwdRoot.Mkdir(filepath.Base(dir), 0700); err != nil {
				return err
			}
			st, err := cwdRoot.Lstat(filepath.Base(dir))
			if err != nil {
				return err
			}
			b.record.StageDirIdentity = officeFileIdentity(st)
			b.record.StagedPath = filepath.Join(dir, "document."+ext)
			stageRoot, err := cwdRoot.OpenRoot(filepath.Base(dir))
			if err != nil {
				return err
			}
			defer stageRoot.Close()
			stageInfo, err := stageRoot.Stat(".")
			if err != nil || officeFileIdentity(stageInfo) != b.record.StageDirIdentity {
				return officeFail(409, "STAGE_CHANGED", "暂存目录在创建期间改变。")
			}
			f, err := stageRoot.OpenFile("document."+ext, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if err != nil {
				return err
			}
			_, err = f.Write(data)
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
		}
		b.record.State = "prepared"
		b.record.ExpiresAt = time.Now().Add(15 * time.Minute).UTC().Format(time.RFC3339Nano)
		return o.saveBatch(b)
	})
	if err != nil {
		return fail(err)
	}
	if d != nil {
		d.mu.Lock()
		if d.freeze != nil {
			d.freeze.phase = "prepared"
			d.freeze.expiresAt = b.record.ExpiresAt
			d.syncRevision++
		}
		d.mu.Unlock()
	}
	o.scheduleBatchExpiry(b)
	return o.batchView(b), nil
}
func (o *officeService) expireBatchLocked(b *officeBatch) bool {
	if b.record.State != "prepared" {
		return false
	}
	deadline, err := time.Parse(time.RFC3339Nano, b.record.ExpiresAt)
	if err != nil || time.Now().Before(deadline) {
		return false
	}
	b.record.State = "expired"
	b.record.ErrorCode = "BATCH_EXPIRED"
	if err = o.saveBatch(b); err != nil {
		b.record.State = "prepared"
		return false
	}
	o.releaseBatch(b)
	o.cleanStage(b)
	return true
}
func (o *officeService) scheduleBatchExpiry(b *officeBatch) {
	deadline, err := time.Parse(time.RFC3339Nano, b.record.ExpiresAt)
	if err != nil {
		return
	}
	if b.timer != nil {
		b.timer.Stop()
	}
	b.timer = time.AfterFunc(time.Until(deadline), func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		o.mu.Lock()
		closed := o.closed
		o.mu.Unlock()
		if !closed {
			o.expireBatchLocked(b)
		}
	})
}

func officePendingLocked(d *officeDocumentSession) bool {
	for _, op := range d.queue {
		if !op.finished {
			return true
		}
	}
	return false
}
func officeDiscardEqual(a, b *officeDiscardConfirmation) bool {
	return a == nil && b == nil || a != nil && b != nil && *a == *b
}
func officeBatchCanvasSafe(d *officeDocumentSession, c *officeDiscardConfirmation) bool {
	if c == nil {
		return d.dirtyEpoch == d.persistedEpoch
	}
	return c.InstanceID == d.instanceID && c.CanvasVersion == d.baseHash && c.DirtyEpoch == d.dirtyEpoch
}
func (o *officeService) replaceBatchDocument(l *officeLink, b *officeBatch) (*officeDocumentSession, error) {
	var journal tools.Journal
	if o.manager != nil {
		journal = o.manager.JournalFor(l.workspaceRoot()).ForSession(l.conversationID)
	}

	o.mu.Lock()
	defer o.mu.Unlock()
	old := o.sessions[b.record.OldSessionID]
	if old != nil {
		old.mu.Lock()
		defer old.mu.Unlock()
		if old.closed || !officeBatchCanvasSafe(old, b.record.Discard) || old.saving || old.exportBusy || old.blocked || officePendingLocked(old) {
			return nil, officeFail(409, "UNSAVED_DOCUMENT", "旧画布状态改变，已提交文件保留，但不能自动切换画布。")
		}
	}
	relative, err := filepath.Rel(l.cwd, b.record.Absolute)
	if err != nil {
		return nil, err
	}
	// Build and validate the replacement first: an I/O failure must not retire
	// the still-visible old canvas. Registry publication and retirement share mu.
	d, err := o.createDocumentLocked(l.conversationID, l.cwd, l.owner, l.hub, b.record.Absolute, b.record.PanelPath, journal, l.cwd, relative, b.record.FileType)
	if err != nil {
		return nil, err
	}
	if d.baseHash != b.record.DiskVersion {
		d.mu.Lock()
		o.retireLocked(d)
		d.mu.Unlock()
		if old != nil {
			o.active[old.absolute] = old.id
		}
		return nil, officeFail(409, "FILE_CHANGED", "文件再次变化，画布未切换。")
	}
	d.replacementPending = old != nil && old.canvasMounted
	if old != nil {
		old.diskVersion = b.record.DiskVersion
		old.externalChange = old.baseHash != b.record.DiskVersion
		old.versionError = ""
		// prepare/commit already verified the user's exact discard consent and
		// dirty epoch, or a clean canvas. Ordinary conversation revocation has
		// no such authority and must retain an abandoned dirty writer.
		old.retirementAuthorized = b.record.Discard != nil
		o.retireLocked(old)
		old.replacedBy = d.id
		old.freeze = nil
		old.batchLease = ""
		old.syncRevision++
	}
	return d, nil
}

func (o *officeService) commitBatch(ctx context.Context, l *officeLink, id, op string) (map[string]any, error) {
	if !officeOperationID.MatchString(op) {
		return nil, officeFail(400, "INVALID_OPERATION_ID", "请提供有效操作 ID。")
	}
	b, err := o.batchFor(l, id)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if err = o.reconcileBatch(b); err != nil {
		return nil, err
	}
	if b.record.CommitOperation != "" && b.record.CommitOperation != op {
		return nil, officeFail(409, "OPERATION_ID_CONFLICT", "提交操作 ID 与原记录不一致。")
	}
	if b.record.State == "committed" {
		return o.recoverBatchCanvas(ctx, l, b)
	}
	o.expireBatchLocked(b)
	if b.record.State != "prepared" {
		return nil, officeFail(409, "BATCH_NOT_PREPARED", "批处理不处于可提交状态，请查询记录。")
	}
	if err = o.reserveBatch(l, b); err != nil {
		return nil, err
	}
	var resultSession *officeDocumentSession
	renamed := false
	err = tools.WithFileMutationLock(b.record.Absolute, func() error {
		if err := ctx.Err(); err != nil {
			return err
		}
		r := &b.record
		previous, info, pinfo, err := officeDiskRead(r.Cwd, r.Absolute)
		if err != nil {
			return err
		}
		if officeHash(previous) != r.BaseVersion || officeFileIdentity(info) != r.BaseIdentity || officeFileIdentity(pinfo) != r.ParentIdentity {
			return officeFail(409, "FILE_CHANGED", "原文件已有其他修改，批处理结果不会覆盖它。")
		}
		var data []byte
		if r.Purpose == "reload" {
			data = previous
		} else {
			stageParent, _, dir, err := officeOpenParent(r.Cwd, r.StagedPath)
			if stageParent != nil {
				stageParent.Close()
			}
			if err != nil || !dir.IsDir() || officeFileIdentity(dir) != r.StageDirIdentity {
				return officeFail(409, "STAGE_CHANGED", "批处理副本目录已改变。")
			}
			data, _, _, err = officeDiskRead(r.Cwd, r.StagedPath)
			if err != nil {
				return err
			}
			if err = officeCheckPackage(data, r.FileType); err != nil {
				return err
			}
		}
		if d := o.findDocument(r.Absolute); d != nil {
			d.mu.Lock()
			safe := d.id == r.OldSessionID && d.batchLease == r.ID && !d.closed && !d.blocked && officeBatchCanvasSafe(d, r.Discard) && !d.saving && !d.exportBusy && !officePendingLocked(d) && (d.freeze == nil || d.freeze.acknowledged)
			d.mu.Unlock()
			if !safe {
				return officeFail(409, "UNSAVED_DOCUMENT", "旧画布未保持冻结或有未完成操作。")
			}
		}
		r.CommitOperation = op
		r.ProposedVersion = officeHash(data)
		if r.Purpose == "reload" {
			r.ProposedIdentity = r.BaseIdentity
		}
		r.State = "committing"
		if err = o.saveBatch(b); err != nil {
			return err
		}
		if r.Purpose != "reload" {
			parent, _, pinnedParent, err := officeOpenParent(r.Cwd, r.Absolute)
			if err != nil {
				return err
			}
			defer parent.Close()
			if officeFileIdentity(pinnedParent) != r.ParentIdentity {
				return officeFail(409, "FILE_CHANGED", "目标目录已变化。")
			}
			tmp := ".pogo-office-" + r.ID + ".tmp"
			f, err := parent.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
			if err != nil {
				return err
			}
			defer parent.Remove(tmp)
			_, err = f.Write(data)
			if err == nil {
				err = f.Chmod(info.Mode().Perm())
			}
			if err == nil {
				err = f.Sync()
			}
			newInfo, statErr := f.Stat()
			if err == nil {
				err = statErr
			}
			if err == nil {
				r.ProposedIdentity = officeFileIdentity(newInfo)
				meta := r.VersionMetadata
				if meta.Engine == "" {
					meta.Engine = "script"
				}
				r.VersionID, err = o.beginVersion(officeVersionScopeForBatch(b), op, r.ID, previous, data, info, newInfo, meta)
				if err == nil {
					err = o.saveBatch(b)
				}
			}
			closeErr := f.Close()
			if err == nil {
				err = closeErr
			}
			if err != nil {
				return err
			}
			if o.manager != nil {
				o.manager.JournalFor(l.workspaceRoot()).ForSession(l.conversationID).BeforeChange(r.Absolute, previous, true)
			}
			latest, latestInfo, parentInfo, err := officeDiskRead(r.Cwd, r.Absolute)
			if err != nil || officeHash(latest) != r.BaseVersion || officeFileIdentity(latestInfo) != r.BaseIdentity || officeFileIdentity(parentInfo) != r.ParentIdentity {
				return officeFail(409, "FILE_CHANGED", "原文件在提交期间改变，本次未覆盖。")
			}
			if err = ctx.Err(); err != nil {
				return err
			}
			deadline, timeErr := time.Parse(time.RFC3339Nano, r.ExpiresAt)
			if timeErr != nil || !time.Now().Before(deadline) {
				return officeFail(409, "BATCH_EXPIRED", "批处理租约已到期，未提交文件。")
			}
			if err = parent.Rename(tmp, filepath.Base(r.Absolute)); err != nil {
				return err
			}
			renamed = true
			if dir, openErr := parent.Open("."); openErr == nil {
				syncErr := dir.Sync()
				dir.Close()
				if syncErr != nil {
					return syncErr
				}
			} else {
				return openErr
			}
		}
		r.DiskVersion = r.ProposedVersion
		r.State = "committed"
		r.ErrorCode = ""
		// Record the irreversible result before touching an editor. A retry never
		// repeats rename even when creating the new canvas or publishing fails.
		if err = o.saveBatch(b); err != nil {
			r.State = "committing"
			return officeFail(409, "BATCH_OUTCOME_UNKNOWN", "文件提交结果尚未持久确认，请查询批处理记录，不要重复写入。")
		}
		// Publication is already durably confirmed. If writing the display
		// ledger's terminal marker fails, its durable intent and the batch
		// receipt reconcile later; never repeat the file replacement.
		_ = o.finishVersion(officeVersionScopeForBatch(b), r.VersionID)
		if r.OldSessionID == "" {
			// Offline transforms and version restores do not need to launch an
			// editor. Only replace a canvas that existed before this transaction.
			return nil
		}
		resultSession, err = o.replaceBatchDocument(l, b)
		if err != nil {
			r.ErrorCode = "EDITOR_RELOAD_REQUIRED"
			if old := o.findDocument(r.Absolute); old != nil {
				o.observeDisk(old)
			}
			_ = o.saveBatch(b)
			return nil
		}
		r.NewSessionID = resultSession.id
		return o.saveBatch(b)
	})
	if err != nil {
		if b.record.State == "committing" && !renamed {
			b.record.State = "failed"
			b.record.ErrorCode = "BATCH_COMMIT_FAILED"
			_ = o.saveBatch(b)
			o.releaseBatch(b)
		}
		var typed *officeError
		if errors.As(err, &typed) && typed.code == "FILE_CHANGED" {
			b.record.State = "conflict"
			b.record.ErrorCode = typed.code
			_ = o.saveBatch(b)
			o.releaseBatch(b)
			if d := o.findDocument(b.record.Absolute); d != nil {
				o.observeDisk(d)
			}
		}
		return nil, err
	}
	o.releaseBatch(b)
	o.cleanStage(b)
	if resultSession != nil && l.hub != nil {
		v := resultSession.metadataView()
		l.hub.Publish(Event{Type: "office_open", OfficeDocumentSessionID: v.ID, OfficeConversationID: l.conversationID, OfficePath: v.Path, OfficeFileType: v.FileType})
	}
	return o.batchView(b), nil
}

// Repeating commit can retry only editor construction, never the file rename.
// A post-restart editor is reused when it already represents this version.
func (o *officeService) recoverBatchCanvas(ctx context.Context, l *officeLink, b *officeBatch) (map[string]any, error) {
	r := &b.record
	if r.OldSessionID == "" && r.NewSessionID == "" {
		return o.batchView(b), nil
	}
	o.mu.Lock()
	existing := o.sessions[r.NewSessionID]
	o.mu.Unlock()
	if existing != nil {
		return o.batchView(b), nil
	}
	data, _, _, err := officeDiskRead(r.Cwd, r.Absolute)
	if err != nil {
		return nil, err
	}
	if officeHash(data) != r.DiskVersion {
		return o.batchView(b), nil
	}
	if err = o.reserveBatch(l, b); err != nil {
		return nil, err
	}
	defer o.releaseBatch(b)
	if current := o.findDocument(r.Absolute); current != nil {
		if current.id != r.OldSessionID {
			current.mu.Lock()
			matches := !current.closed && current.baseHash == r.DiskVersion
			current.mu.Unlock()
			if matches {
				r.NewSessionID = current.id
				if err = o.saveBatch(b); err != nil {
					return nil, err
				}
				return o.batchView(b), nil
			}
			return nil, officeFail(409, "DOCUMENT_ALREADY_OPEN", "当前文件已有其他编辑会话，请在现有画布核对版本。")
		}
		current.mu.Lock()
		frozen := current.batchLease == r.ID && current.freeze != nil && current.freeze.acknowledged
		current.mu.Unlock()
		if !frozen {
			copy := *r
			copy.Purpose = "reload"
			if err = o.freezeDocument(ctx, current, &officeBatch{record: copy}); err != nil {
				return nil, err
			}
		}
	}
	var fresh *officeDocumentSession
	err = tools.WithFileMutationLock(r.Absolute, func() error {
		data, _, _, err := officeDiskRead(r.Cwd, r.Absolute)
		if err != nil {
			return err
		}
		if officeHash(data) != r.DiskVersion {
			return officeFail(409, "FILE_CHANGED", "文件再次变化，不会重复原批处理。")
		}
		fresh, err = o.replaceBatchDocument(l, b)
		return err
	})
	if err != nil {
		return nil, err
	}
	r.NewSessionID = fresh.id
	r.ErrorCode = ""
	if err = o.saveBatch(b); err != nil {
		return nil, err
	}
	o.cleanStage(b)
	if l.hub != nil {
		v := fresh.metadataView()
		l.hub.Publish(Event{Type: "office_open", OfficeDocumentSessionID: v.ID, OfficeConversationID: l.conversationID, OfficePath: v.Path, OfficeFileType: v.FileType})
	}
	return o.batchView(b), nil
}

func (o *officeService) abortBatch(l *officeLink, id, op string) (map[string]any, error) {
	if !officeOperationID.MatchString(op) {
		return nil, officeFail(400, "INVALID_OPERATION_ID", "请提供有效操作 ID。")
	}
	b, err := o.batchFor(l, id)
	if err != nil {
		return nil, err
	}
	if d := o.findDocument(b.record.Absolute); d != nil {
		d.mu.Lock()
		if f := d.freeze; f != nil && f.id == id && !f.acknowledged {
			f.acknowledged = true
			f.done <- officeFail(409, "BATCH_ABORTED", "批处理已由用户取消。")
			d.syncRevision++
		}
		d.mu.Unlock()
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if err = o.reconcileBatch(b); err != nil {
		return nil, err
	}
	if b.record.AbortOperation != "" && b.record.AbortOperation != op {
		return nil, officeFail(409, "OPERATION_ID_CONFLICT", "取消操作 ID 与原记录不一致。")
	}
	if b.record.State == "committed" || b.record.State == "committing" || b.record.State == "outcome_unknown" {
		return nil, officeFail(409, "BATCH_ALREADY_COMMITTED", "提交已发生或结果未确认，不能声明已取消。")
	}
	b.record.AbortOperation = op
	b.record.State = "aborted"
	b.record.ErrorCode = ""
	if err = o.saveBatch(b); err != nil {
		return nil, err
	}
	o.releaseBatch(b)
	o.cleanStage(b)
	return o.batchView(b), nil
}
func (l *officeLink) executeBatch(ctx context.Context, r officeToolRequest) (json.RawMessage, error) {
	var reply map[string]any
	var err error
	switch r.Action {
	case "batch_prepare":
		reply, err = l.service.prepareBatch(ctx, l, r.Path, r.OperationID, "batch", "")
	case "batch_commit":
		reply, err = l.service.commitBatch(ctx, l, r.BatchID, r.OperationID)
	case "batch_abort":
		reply, err = l.service.abortBatch(l, r.BatchID, r.OperationID)
	case "batch_status":
		var b *officeBatch
		b, err = l.service.batchFor(l, r.BatchID)
		if err == nil {
			b.mu.Lock()
			err = l.service.reconcileBatch(b)
			if err == nil {
				l.service.expireBatchLocked(b)
				reply = l.service.batchView(b)
			}
			b.mu.Unlock()
		}
	case "reload":
		var d *officeDocumentSession
		d, err = l.service.forLink(l, r.DocumentSessionID)
		if err == nil {
			reply, err = l.service.reloadDocument(ctx, l, d, r.OperationID, r.ExpectedVersion)
		}
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(reply)
}

// OfficeBatchCompletion reports only the old canvas's in-memory save ledger.
// A batch is not a native editor mutation. Looking up a receipt on disk, or
// trusting a tool reply's version/state, would invent completion after restart.
func (l *officeLink) OfficeBatchCompletion(action, batchID, operationID string) *tools.OfficeCompletionReceipt {
	if l == nil || l.service == nil || l.role == nil || l.role.revoked.Load() {
		return nil
	}
	switch action {
	case "batch_prepare", "reload", "restore_version":
		if !officeOperationID.MatchString(operationID) {
			return nil
		}
		batchID = officeBatchID(l, operationID)
	case "transform":
		if !officeOperationID.MatchString(operationID) {
			return nil
		}
		prepareID := "transform_prepare_" + officeHash([]byte(operationID))[:32]
		batchID = officeBatchID(l, prepareID)
	case "batch_commit", "batch_abort", "batch_status":
		if !officeSessionID.MatchString(batchID) {
			return nil
		}
	default:
		return nil
	}
	o := l.service
	o.batchMu.Lock()
	b := o.batches[l.conversationID+"/"+batchID]
	o.batchMu.Unlock()
	if b == nil {
		return nil
	}
	b.mu.Lock()
	if !b.matches(l) {
		b.mu.Unlock()
		return nil
	}
	id := b.record.OldSessionID
	b.mu.Unlock()
	d, err := o.forLink(l, id)
	if err != nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return &tools.OfficeCompletionReceipt{DocumentSessionID: d.id, PersistedEpoch: d.persistedEpoch, Blocked: d.blocked}
}
func (o *officeService) reloadDocument(ctx context.Context, l *officeLink, d *officeDocumentSession, op, expected string) (map[string]any, error) {
	return o.reloadDocumentConfirmed(ctx, l, d, op, expected, nil)
}
func (o *officeService) reloadDocumentConfirmed(ctx context.Context, l *officeLink, d *officeDocumentSession, op, expected string, discard *officeDiscardConfirmation) (map[string]any, error) {
	if len(expected) != 64 || strings.Trim(expected, "0123456789abcdef") != "" {
		return nil, officeFail(400, "INVALID_ARGUMENT", "请提供明确的磁盘版本。")
	}
	d.mu.Lock()
	unsafe := !officeBatchCanvasSafe(d, discard) || d.blocked || d.saving || d.exportBusy || officePendingLocked(d)
	d.mu.Unlock()
	if unsafe {
		return nil, officeFail(409, "UNSAVED_DOCUMENT", "画布仍有未保存内容，不能载入外部版本。")
	}
	prepared, err := o.prepareBatchConfirmed(ctx, l, d.absolute, op, "reload", expected, discard)
	if err != nil {
		return nil, err
	}
	if prepared["state"] == "committed" {
		return prepared, nil
	}
	if prepared["state"] != "prepared" {
		return nil, officeFail(409, "BATCH_NOT_PREPARED", "版本切换尚未准备完成。")
	}
	return o.commitBatch(ctx, l, prepared["batch_id"].(string), op)
}
