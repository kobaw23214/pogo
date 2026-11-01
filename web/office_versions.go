package web

// Versions are immutable file revisions, separate from the live editor's Undo
// stack and from the small activity projection. A durable intent and both byte
// snapshots precede publication; only a confirmed file commit is restorable.
import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/yosukeno/pogo/identity"
)

const officeVersionLimit = 50
const officeVersionBytes = 256 << 20
const officeVersionPageLimit = 20
const officeVersionRecordBytes = 256 << 10

type officeVersionMetadata struct {
	Engine       string              `json:"engine,omitempty"`
	Actor        officeActivityActor `json:"actor,omitempty"`
	ScriptDigest string              `json:"scriptDigest,omitempty"`
	ExecutionID  string              `json:"executionId,omitempty"`
	Summary      string              `json:"summary,omitempty"`
	RestoredFrom string              `json:"restoredFrom,omitempty"`
}

type officeVersionRecord struct {
	ID             string              `json:"id"`
	FileRef        string              `json:"fileRef"`
	ConversationID string              `json:"conversationId"`
	Path           string              `json:"path"`
	FileType       string              `json:"fileType"`
	CreatedAt      string              `json:"createdAt"`
	CommittedAt    string              `json:"committedAt,omitempty"`
	OperationID    string              `json:"operationId"`
	BatchID        string              `json:"batchId,omitempty"`
	Engine         string              `json:"engine"`
	Actor          officeActivityActor `json:"actor"`
	ScriptDigest   string              `json:"scriptDigest,omitempty"`
	ExecutionID    string              `json:"executionId,omitempty"`
	Summary        string              `json:"summary,omitempty"`
	RestoredFrom   string              `json:"restoredFrom,omitempty"`
	BeforeVersion  string              `json:"beforeVersion"`
	AfterVersion   string              `json:"afterVersion"`
	BeforeBytes    int                 `json:"beforeBytes"`
	AfterBytes     int                 `json:"afterBytes"`
	Diff           officeVersionDiff   `json:"diff"`
	State          string              `json:"state"`
	Recovered      bool                `json:"recovered,omitempty"`
}

type officeVersionPage struct {
	OK         bool                  `json:"ok"`
	FileRef    string                `json:"fileRef"`
	Path       string                `json:"path"`
	Items      []officeVersionRecord `json:"items"`
	NextCursor string                `json:"nextCursor,omitempty"`
	Retention  struct {
		MaxVersions int `json:"maxVersions"`
		MaxBytes    int `json:"maxBytes"`
	} `json:"retention"`
}

type officeVersionScope struct {
	Owner          *identity.Scope
	ConversationID string
	Cwd            string
	Absolute       string
	PanelPath      string
	FileRef        string
	FileType       string
}

type officeVersionIntent struct {
	Schema         int                 `json:"schema"`
	Owner          *identity.Scope     `json:"owner"`
	ConversationID string              `json:"conversationId"`
	Cwd            string              `json:"cwd"`
	Absolute       string              `json:"absolute"`
	BeforeIdentity string              `json:"beforeIdentity"`
	AfterIdentity  string              `json:"afterIdentity"`
	Record         officeVersionRecord `json:"record"`
}

func officeVersionValidHash(value string) bool {
	return len(value) == 64 && strings.Trim(value, "0123456789abcdef") == ""
}

func (v officeVersionIntent) matches(scope officeVersionScope) bool {
	return v.Schema == 1 && validID(v.ConversationID) && v.Record.ConversationID == v.ConversationID && v.Absolute == scope.Absolute &&
		v.Record.FileRef == scope.FileRef && v.Record.Path == scope.PanelPath && v.Record.FileType == scope.FileType && officeSameOwner(v.Owner, scope.Owner)
}

func (o *officeService) versionScope(l *officeLink, path string) (officeVersionScope, error) {
	absolute, err := l.resolvePath(path)
	if err != nil {
		return officeVersionScope{}, officeFail(400, "PATH_REJECTED", "文档不在当前会话工作区。")
	}
	panel, err := officePanelPath(l.workspaceRoot(), absolute)
	if err != nil {
		return officeVersionScope{}, err
	}
	fileType := strings.TrimPrefix(strings.ToLower(filepath.Ext(absolute)), ".")
	if fileType != "docx" && fileType != "xlsx" {
		return officeVersionScope{}, officeFail(400, "UNSUPPORTED_DOCUMENT", "文档版本仅支持 .docx 和 .xlsx。")
	}
	return officeVersionScope{Owner: officeCopyOwner(l.owner), ConversationID: l.conversationID, Cwd: l.cwd, Absolute: absolute,
		PanelPath: panel, FileRef: officeFileRef(l.owner, panel), FileType: fileType}, nil
}

func officeVersionScopeForDocument(d *officeDocumentSession) officeVersionScope {
	return officeVersionScope{Owner: officeCopyOwner(d.owner), ConversationID: d.conversationID, Cwd: d.cwd, Absolute: d.absolute,
		PanelPath: d.panelPath, FileRef: officeFileRef(d.owner, d.panelPath), FileType: d.fileType}
}

func officeVersionScopeForBatch(b *officeBatch) officeVersionScope {
	r := b.record
	return officeVersionScope{Owner: officeCopyOwner(r.Owner), ConversationID: r.ConversationID, Cwd: r.Cwd, Absolute: r.Absolute,
		PanelPath: r.PanelPath, FileRef: r.FileRef, FileType: r.FileType}
}

func (o *officeService) versionRoot(scope officeVersionScope) (string, error) {
	root := o.batchDir
	if root != "" {
		root = filepath.Join(root, "office-versions")
	} else if o.manager != nil {
		root = filepath.Join(o.manager.cfg.SessionDir, "office-versions")
	} else {
		return "", officeFail(503, "VERSION_STORAGE_UNAVAILABLE", "当前服务未配置持久文档版本目录。")
	}
	key, _ := json.Marshal([]any{scope.Owner, scope.FileRef})
	dir := filepath.Join(root, officeHash(key))
	if err := os.MkdirAll(filepath.Join(dir, "blobs"), 0700); err != nil {
		return "", err
	}
	// This directory is outside the model's writable document workspace. Never
	// follow an accidentally substituted final directory or snapshot file.
	for _, name := range []string{dir, filepath.Join(dir, "blobs")} {
		info, statErr := os.Lstat(name)
		if statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", officeFail(409, "VERSION_STORAGE_INVALID", "文档版本存储身份不正确。")
		}
	}
	return dir, nil
}

func officeVersionAtomicWrite(dir, name string, data []byte) error {
	if filepath.Base(name) != name || name == "." || name == ".." {
		return errors.New("invalid version filename")
	}
	f, err := os.CreateTemp(dir, ".version-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
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
	if err = os.Rename(f.Name(), filepath.Join(dir, name)); err != nil {
		return err
	}
	return officeVersionSyncDir(dir)
}

func officeVersionSyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func officeVersionRead(dir, name string, limit int64) ([]byte, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	before, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || before.Size() > limit {
		return nil, officeFail(409, "VERSION_STORAGE_INVALID", "文档版本数据无法安全读取。")
	}
	f, err := root.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	after, err := f.Stat()
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) || after.Size() > limit {
		return nil, officeFail(409, "VERSION_STORAGE_INVALID", "文档版本数据身份已改变。")
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, officeFail(409, "VERSION_STORAGE_INVALID", "文档版本数据超过读取边界。")
	}
	return data, nil
}

func officeVersionSaveIntent(dir string, intent officeVersionIntent) error {
	if !officeVersionValidHash(intent.Record.ID) {
		return officeFail(400, "INVALID_VERSION_ID", "文档版本身份不正确。")
	}
	data, err := json.Marshal(intent)
	if err != nil {
		return err
	}
	if len(data) > officeVersionRecordBytes {
		return officeFail(413, "VERSION_RECORD_LIMIT", "文档版本记录超过边界。")
	}
	return officeVersionAtomicWrite(dir, intent.Record.ID+".json", data)
}

func officeVersionLoadIntent(dir, id string, scope officeVersionScope) (officeVersionIntent, error) {
	var intent officeVersionIntent
	if !officeVersionValidHash(id) {
		return intent, officeFail(400, "INVALID_VERSION_ID", "文档版本身份不正确。")
	}
	data, err := officeVersionRead(dir, id+".json", officeVersionRecordBytes)
	if err != nil {
		return intent, err
	}
	if json.Unmarshal(data, &intent) != nil || intent.Record.ID != id || !intent.matches(scope) ||
		!officeVersionValidHash(intent.Record.BeforeVersion) || !officeVersionValidHash(intent.Record.AfterVersion) ||
		intent.Record.BeforeBytes < 0 || intent.Record.BeforeBytes > officeMaxBytes || intent.Record.AfterBytes < 0 || intent.Record.AfterBytes > officeMaxBytes ||
		intent.Record.BatchID != "" && !officeSessionID.MatchString(intent.Record.BatchID) {
		return officeVersionIntent{}, officeFail(409, "VERSION_STORAGE_INVALID", "文档版本记录无法核对。")
	}
	return intent, nil
}

func officeVersionSaveBlob(dir string, data []byte) error {
	hash := officeHash(data)
	blobs := filepath.Join(dir, "blobs")
	previous, err := officeVersionRead(blobs, hash, officeMaxBytes)
	if err == nil {
		if officeHash(previous) != hash {
			return officeFail(409, "VERSION_SNAPSHOT_CHANGED", "文档版本快照已改变。")
		}
		return nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return officeVersionAtomicWrite(blobs, hash, data)
}

func officeVersionIntents(dir string, scope officeVersionScope) ([]officeVersionIntent, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	// A bounded store should never approach this count. Do not turn a damaged
	// directory into an unbounded startup scan.
	if len(entries) > 2048 {
		return nil, officeFail(409, "VERSION_STORAGE_LIMIT", "文档版本目录超过边界，请联系管理员。")
	}
	intents := []officeVersionIntent{}
	for _, entry := range entries {
		id := strings.TrimSuffix(entry.Name(), ".json")
		if !strings.HasSuffix(entry.Name(), ".json") || !officeVersionValidHash(id) {
			continue
		}
		v, err := officeVersionLoadIntent(dir, id, scope)
		if err != nil {
			return nil, err
		}
		intents = append(intents, v)
	}
	sort.Slice(intents, func(i, j int) bool {
		if intents[i].Record.CreatedAt != intents[j].Record.CreatedAt {
			return intents[i].Record.CreatedAt > intents[j].Record.CreatedAt
		}
		return intents[i].Record.ID > intents[j].Record.ID
	})
	return intents, nil
}

// Called only while versionMu is held. Protecting the pending commit keeps its
// snapshots alive even when retention must evict older confirmed versions.
func officeVersionGC(dir string, scope officeVersionScope, protect string) error {
	intents, err := officeVersionIntents(dir, scope)
	if err != nil {
		return err
	}
	retained := make([]officeVersionIntent, 0, len(intents))
	committed, pending := 0, 0
	for _, intent := range intents {
		keep := intent.Record.ID == protect
		if intent.Record.State == "committed" && committed < officeVersionLimit {
			keep = true
			committed++
		} else if intent.Record.State == "pending" && pending < 8 {
			keep = true
			pending++
		}
		if keep {
			retained = append(retained, intent)
		} else if err = os.Remove(filepath.Join(dir, intent.Record.ID+".json")); err != nil {
			return err
		}
	}
	bytesFor := func(items []officeVersionIntent) (map[string]int, int) {
		refs := map[string]int{}
		for _, item := range items {
			refs[item.Record.BeforeVersion] = item.Record.BeforeBytes
			refs[item.Record.AfterVersion] = item.Record.AfterBytes
		}
		total := 0
		for _, size := range refs {
			total += size
		}
		return refs, total
	}
	refs, total := bytesFor(retained)
	for total > officeVersionBytes {
		removed := false
		for i := len(retained) - 1; i >= 0; i-- {
			if retained[i].Record.ID == protect {
				continue
			}
			if err = os.Remove(filepath.Join(dir, retained[i].Record.ID+".json")); err != nil {
				return err
			}
			retained = append(retained[:i], retained[i+1:]...)
			removed = true
			break
		}
		if !removed {
			return officeFail(413, "VERSION_STORAGE_LIMIT", "当前文档版本快照超过存储边界。")
		}
		refs, total = bytesFor(retained)
	}
	// Make removal of references durable before deleting their last blob. A
	// crash may leave unused blobs, but must not resurrect a dangling version.
	if err = officeVersionSyncDir(dir); err != nil {
		return err
	}
	blobDir := filepath.Join(dir, "blobs")
	blobs, err := os.ReadDir(blobDir)
	if err != nil {
		return err
	}
	if len(blobs) > 4096 {
		return officeFail(409, "VERSION_STORAGE_LIMIT", "文档版本快照目录超过边界。")
	}
	for _, blob := range blobs {
		if officeVersionValidHash(blob.Name()) {
			if _, keep := refs[blob.Name()]; !keep {
				if err = os.Remove(filepath.Join(blobDir, blob.Name())); err != nil {
					return err
				}
			}
		}
	}
	return officeVersionSyncDir(blobDir)
}

func officeVersionCleanMetadata(meta officeVersionMetadata) (officeVersionMetadata, error) {
	if meta.Engine == "" {
		meta.Engine = "script"
	}
	if meta.Engine != "native" && meta.Engine != "script" && meta.Engine != "restore" {
		return meta, officeFail(400, "INVALID_ARGUMENT", "文档修改引擎不正确。")
	}
	if meta.Actor != officeActivityUser {
		meta.Actor = officeActivityAgent
	}
	if meta.ScriptDigest != "" && !officeVersionValidHash(meta.ScriptDigest) {
		return meta, officeFail(400, "INVALID_ARGUMENT", "脚本摘要不正确。")
	}
	if len(meta.ExecutionID) > 128 || strings.ContainsAny(meta.ExecutionID, "\r\n\x00") {
		return meta, officeFail(400, "INVALID_ARGUMENT", "脚本执行身份不正确。")
	}
	meta.Summary, _ = officeActivityText(meta.Summary, 240)
	if meta.RestoredFrom != "" {
		parts := strings.Split(meta.RestoredFrom, ":")
		if len(parts) != 2 || !officeVersionValidHash(parts[0]) || parts[1] != "before" && parts[1] != "after" {
			return meta, officeFail(400, "INVALID_VERSION_ID", "恢复来源版本不正确。")
		}
	}
	return meta, nil
}

// Caller owns the file mutation lock. No arbitrary script is executed here.
func (o *officeService) beginVersion(scope officeVersionScope, operationID, batchID string, before, after []byte, beforeInfo, afterInfo os.FileInfo, metadata officeVersionMetadata) (string, error) {
	if !officeOperationID.MatchString(operationID) || len(before) > officeMaxBytes || len(after) > officeMaxBytes || beforeInfo == nil || afterInfo == nil {
		return "", officeFail(400, "INVALID_ARGUMENT", "文档版本提交参数不正确。")
	}
	meta, err := officeVersionCleanMetadata(metadata)
	if err != nil {
		return "", err
	}
	key, _ := json.Marshal([]any{scope.Owner, scope.ConversationID, scope.FileRef, meta.Engine, operationID, batchID})
	id := officeHash(key)
	intent := officeVersionIntent{Schema: 1, Owner: officeCopyOwner(scope.Owner), ConversationID: scope.ConversationID, Cwd: scope.Cwd, Absolute: scope.Absolute,
		BeforeIdentity: officeFileIdentity(beforeInfo), AfterIdentity: officeFileIdentity(afterInfo), Record: officeVersionRecord{
			ID: id, FileRef: scope.FileRef, ConversationID: scope.ConversationID, Path: scope.PanelPath, FileType: scope.FileType, CreatedAt: time.Now().UTC().Format("2006-01-02T15:04:05.000000000Z"),
			OperationID: operationID, BatchID: batchID, Engine: meta.Engine, Actor: meta.Actor, ScriptDigest: meta.ScriptDigest, ExecutionID: meta.ExecutionID,
			Summary: meta.Summary, RestoredFrom: meta.RestoredFrom, BeforeVersion: officeHash(before), AfterVersion: officeHash(after), BeforeBytes: len(before), AfterBytes: len(after),
			State: "pending", Diff: officeVersionCompare(before, after, scope.FileType)}}
	if intent.BeforeIdentity == "" || intent.AfterIdentity == "" {
		return "", officeFail(409, "FILE_IDENTITY_CHANGED", "无法确认文档版本的文件身份。")
	}
	o.versionMu.Lock()
	defer o.versionMu.Unlock()
	dir, err := o.versionRoot(scope)
	if err != nil {
		return "", err
	}
	// A later trusted commit may be the first observer after a lost terminal
	// marker. Its exact preimage proves a pending predecessor was published,
	// even when the file will immediately advance again after this intent.
	prior, err := officeVersionIntents(dir, scope)
	if err != nil {
		return "", err
	}
	for _, previous := range prior {
		if previous.Record.State == "pending" && previous.Record.AfterVersion == intent.Record.BeforeVersion && previous.AfterIdentity == intent.BeforeIdentity {
			previous.Record.State, previous.Record.CommittedAt, previous.Record.Recovered = "committed", time.Now().UTC().Format(time.RFC3339Nano), true
			if err = officeVersionSaveIntent(dir, previous); err != nil {
				return "", err
			}
		}
	}
	previous, err := officeVersionLoadIntent(dir, id, scope)
	if err == nil {
		if previous.Record.BeforeVersion != intent.Record.BeforeVersion || previous.Record.AfterVersion != intent.Record.AfterVersion || previous.AfterIdentity != intent.AfterIdentity {
			return "", officeFail(409, "OPERATION_ID_CONFLICT", "相同操作身份不能提交另一份文档结果。")
		}
		return id, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	if err = officeVersionSaveBlob(dir, before); err != nil {
		return "", err
	}
	if err = officeVersionSaveBlob(dir, after); err != nil {
		return "", err
	}
	if err = officeVersionSaveIntent(dir, intent); err != nil {
		return "", err
	}
	if err = officeVersionGC(dir, scope, id); err != nil {
		return "", err
	}
	return id, nil
}

func (o *officeService) finishVersion(scope officeVersionScope, id string) error {
	if id == "" {
		return nil
	}
	o.versionMu.Lock()
	defer o.versionMu.Unlock()
	dir, err := o.versionRoot(scope)
	if err != nil {
		return err
	}
	intent, err := officeVersionLoadIntent(dir, id, scope)
	if err != nil {
		return err
	}
	if intent.Record.State == "committed" {
		return nil
	}
	intent.Record.State, intent.Record.CommittedAt = "committed", time.Now().UTC().Format(time.RFC3339Nano)
	return officeVersionSaveIntent(dir, intent)
}

// setBatchVersionMetadata accepts metadata from trusted execution/HTTP entry
// points, never actor identity declared by a document or arbitrary tool args.
func (o *officeService) setBatchVersionMetadata(l *officeLink, id string, metadata officeVersionMetadata) error {
	meta, err := officeVersionCleanMetadata(metadata)
	if err != nil {
		return err
	}
	b, err := o.batchFor(l, id)
	if err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.record.State != "prepared" {
		if b.record.State == "committed" && b.record.VersionMetadata == meta {
			return nil
		}
		return officeFail(409, "BATCH_NOT_PREPARED", "批处理当前不能设置执行回执。")
	}
	if b.record.VersionMetadata.Engine != "" && b.record.VersionMetadata != meta {
		return officeFail(409, "OPERATION_ID_CONFLICT", "脚本执行回执与原记录不一致。")
	}
	b.record.VersionMetadata = meta
	return o.saveBatch(b)
}

// Native save's actor identifies who initiated the save, not authorship of all
// content in the shared canvas. Human and agent changes may coexist in it.
func officeNativeVersionMetadata(d *officeDocumentSession, op *officeOperation) officeVersionMetadata {
	meta := officeVersionMetadata{Engine: "native", Actor: officeActivityAgent, Summary: "保存共享编辑器的当前内容；可能同时包含人工与 Agent 的修改。"}
	if activity := d.activityLocked(op); activity != nil {
		meta.Actor = activity.Actor
	}
	return meta
}

// Never acquire a batch mutex from versionMu: file commits take those locks in
// the opposite order. Its durable receipt is sufficient authority for recovery.
func (o *officeService) reconcileVersionIntent(dir string, scope officeVersionScope, intent *officeVersionIntent) error {
	if intent.Record.State != "pending" {
		return nil
	}
	confirmed := false
	if intent.Record.BatchID != "" {
		batchRoot, err := o.batchRoot(intent.ConversationID)
		if err != nil {
			return err
		}
		data, err := officeVersionRead(batchRoot, intent.Record.BatchID+".json", 32768)
		if err == nil {
			var receipt officeBatchRecord
			if json.Unmarshal(data, &receipt) != nil || receipt.ID != intent.Record.BatchID || receipt.ConversationID != intent.ConversationID || receipt.Cwd != intent.Cwd ||
				receipt.Absolute != scope.Absolute || receipt.FileRef != scope.FileRef || !officeSameOwner(receipt.Owner, scope.Owner) {
				return officeFail(409, "VERSION_STORAGE_INVALID", "文档版本的批处理回执不匹配。")
			}
			confirmed = receipt.State == "committed" && receipt.ProposedVersion == intent.Record.AfterVersion && receipt.ProposedIdentity == intent.AfterIdentity
			if !confirmed && (receipt.State == "failed" || receipt.State == "conflict" || receipt.State == "aborted" || receipt.State == "expired" || receipt.State == "interrupted") {
				intent.Record.State = "aborted"
				return officeVersionSaveIntent(dir, *intent)
			}
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	if !confirmed {
		data, info, parentInfo, err := officeDiskRead(scope.Cwd, scope.Absolute)
		if err == nil && officeHash(data) == intent.Record.AfterVersion && officeFileIdentity(info) == intent.AfterIdentity {
			// A process may have stopped between rename and directory fsync.
			// Flush the matching parent before persisting a recovered commit.
			parent, _, currentParent, openErr := officeOpenParent(scope.Cwd, scope.Absolute)
			if openErr == nil {
				if os.SameFile(parentInfo, currentParent) {
					if handle, handleErr := parent.Open("."); handleErr == nil {
						confirmed = handle.Sync() == nil
						handle.Close()
					}
				}
				parent.Close()
			}
		}
	}
	if confirmed {
		intent.Record.State, intent.Record.CommittedAt, intent.Record.Recovered = "committed", time.Now().UTC().Format(time.RFC3339Nano), true
		return officeVersionSaveIntent(dir, *intent)
	}
	return nil
}

type officeVersionCursor struct {
	ID        string `json:"id"`
	CreatedAt string `json:"createdAt"`
}

func (o *officeService) listVersions(ctx context.Context, l *officeLink, path, cursor string, limit int) (officeVersionPage, error) {
	page := officeVersionPage{OK: true, Items: []officeVersionRecord{}}
	page.Retention.MaxVersions, page.Retention.MaxBytes = officeVersionLimit, officeVersionBytes
	scope, err := o.versionScope(l, path)
	if err != nil {
		return page, err
	}
	page.FileRef, page.Path = scope.FileRef, scope.PanelPath
	if limit == 0 {
		limit = officeVersionPageLimit
	}
	if limit < 1 || limit > officeVersionPageLimit || len(cursor) > 512 {
		return page, officeFail(400, "INVALID_ARGUMENT", "文档版本分页参数不正确。")
	}
	var boundary officeVersionCursor
	if cursor != "" {
		data, decodeErr := base64.RawURLEncoding.DecodeString(cursor)
		if decodeErr != nil || json.Unmarshal(data, &boundary) != nil || !officeVersionValidHash(boundary.ID) {
			return page, officeFail(400, "INVALID_ARGUMENT", "文档版本分页游标不正确。")
		}
		if _, err = time.Parse(time.RFC3339Nano, boundary.CreatedAt); err != nil {
			return page, officeFail(400, "INVALID_ARGUMENT", "文档版本分页游标不正确。")
		}
	}
	o.versionMu.Lock()
	defer o.versionMu.Unlock()
	dir, err := o.versionRoot(scope)
	if err != nil {
		return page, err
	}
	intents, err := officeVersionIntents(dir, scope)
	if err != nil {
		return page, err
	}
	for i := range intents {
		if err = ctx.Err(); err != nil {
			return page, err
		}
		if err = o.reconcileVersionIntent(dir, scope, &intents[i]); err != nil {
			return page, err
		}
		record := intents[i].Record
		if record.State != "committed" || cursor != "" && (record.CreatedAt > boundary.CreatedAt || record.CreatedAt == boundary.CreatedAt && record.ID >= boundary.ID) {
			continue
		}
		if len(page.Items) == limit {
			last := page.Items[len(page.Items)-1]
			data, _ := json.Marshal(officeVersionCursor{ID: last.ID, CreatedAt: last.CreatedAt})
			page.NextCursor = base64.RawURLEncoding.EncodeToString(data)
			break
		}
		page.Items = append(page.Items, record)
	}
	if err = officeVersionGC(dir, scope, ""); err != nil {
		return page, err
	}
	return page, nil
}

func (o *officeService) restoreVersion(ctx context.Context, l *officeLink, path, versionID, expected, operationID string) (map[string]any, error) {
	return o.restoreVersionSide(ctx, l, path, versionID, expected, operationID, "after", officeActivityAgent)
}

func (o *officeService) restoreVersionSide(ctx context.Context, l *officeLink, path, versionID, expected, operationID, side string, actor officeActivityActor) (map[string]any, error) {
	if !officeVersionValidHash(versionID) || !officeVersionValidHash(expected) || !officeOperationID.MatchString(operationID) {
		return nil, officeFail(400, "INVALID_ARGUMENT", "请提供文档版本、当前磁盘版本与稳定操作身份。")
	}
	if side == "" {
		side = "after"
	}
	if side != "before" && side != "after" {
		return nil, officeFail(400, "INVALID_ARGUMENT", "恢复侧只支持 before 或 after。")
	}
	scope, err := o.versionScope(l, path)
	if err != nil {
		return nil, err
	}
	meta := officeVersionMetadata{Engine: "restore", Actor: actor, Summary: "恢复此前已保存的文件版本；不属于编辑器原生撤销。", RestoredFrom: versionID + ":" + side}
	meta, err = officeVersionCleanMetadata(meta)
	if err != nil {
		return nil, err
	}
	// A terminal retry must remain recoverable even if retention has since
	// removed the source version. Its transaction receipt binds source and side.
	if existing, lookupErr := o.batchFor(l, officeBatchID(l, operationID)); lookupErr == nil {
		existing.mu.Lock()
		committed := existing.record.State == "committed" || existing.record.State == "committing"
		conflict := existing.record.Absolute != scope.Absolute || existing.record.ExpectedVersion != expected || existing.record.Purpose != "batch" ||
			existing.record.VersionMetadata.Engine != "" && existing.record.VersionMetadata != meta
		conflict = conflict || committed && existing.record.VersionMetadata != meta
		existing.mu.Unlock()
		if conflict {
			return nil, officeFail(409, "OPERATION_ID_CONFLICT", "相同恢复操作不能用于不同文档、版本或恢复侧。")
		}
		if committed {
			return o.commitBatch(ctx, l, officeBatchID(l, operationID), operationID)
		}
	} else {
		var typed *officeError
		if !errors.As(lookupErr, &typed) || typed.code != "BATCH_NOT_FOUND" {
			return nil, lookupErr
		}
	}
	var snapshot []byte
	err = func() error {
		o.versionMu.Lock()
		defer o.versionMu.Unlock()
		dir, err := o.versionRoot(scope)
		if err != nil {
			return err
		}
		intent, err := officeVersionLoadIntent(dir, versionID, scope)
		if errors.Is(err, fs.ErrNotExist) {
			return officeFail(404, "VERSION_NOT_FOUND", "此文档版本不存在或已超出保留范围。")
		}
		if err != nil {
			return err
		}
		if err = o.reconcileVersionIntent(dir, scope, &intent); err != nil {
			return err
		}
		if intent.Record.State != "committed" {
			return officeFail(409, "VERSION_UNCONFIRMED", "只能恢复已确认提交的文档版本。")
		}
		hash := intent.Record.AfterVersion
		if side == "before" {
			hash = intent.Record.BeforeVersion
		}
		snapshot, err = officeVersionRead(filepath.Join(dir, "blobs"), hash, officeMaxBytes)
		if err != nil || officeHash(snapshot) != hash {
			return officeFail(409, "VERSION_SNAPSHOT_CHANGED", "此文档版本的完整快照无法核对，未执行恢复。")
		}
		return officeCheckPackage(snapshot, scope.FileType)
	}()
	if err != nil {
		return nil, err
	}
	// Expected version is checked again after the editor's freeze/save barrier.
	// If saving later human edits advances the disk, restoration is refused.
	prepared, err := o.prepareBatch(ctx, l, scope.Absolute, operationID, "batch", expected)
	if err != nil {
		return nil, err
	}
	id, _ := prepared["batch_id"].(string)
	if prepared["state"] == "committed" {
		if err = o.setBatchVersionMetadata(l, id, meta); err != nil {
			return nil, err
		}
		return o.commitBatch(ctx, l, id, operationID)
	}
	if prepared["state"] != "prepared" {
		return nil, officeFail(409, "BATCH_NOT_PREPARED", "文档恢复尚未准备完成，请核对操作记录。")
	}
	b, err := o.batchFor(l, id)
	if err != nil {
		return nil, err
	}
	if err = o.setBatchVersionMetadata(l, id, meta); err != nil {
		return nil, err
	}
	err = func() error {
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.record.State != "prepared" || b.record.ExpectedVersion != expected || b.record.Purpose != "batch" || b.record.VersionMetadata != meta {
			return officeFail(409, "BATCH_NOT_PREPARED", "文档恢复状态已改变。")
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		stageParent, name, info, err := officeOpenParent(l.cwd, b.record.StagedPath)
		if err != nil {
			return err
		}
		defer stageParent.Close()
		if officeFileIdentity(info) != b.record.StageDirIdentity {
			return officeFail(409, "STAGE_CHANGED", "文档恢复副本目录已改变。")
		}
		temp := ".restore-" + id + ".tmp"
		f, err := stageParent.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		defer stageParent.Remove(temp)
		_, writeErr := f.Write(snapshot)
		syncErr := f.Sync()
		closeErr := f.Close()
		if err = errors.Join(writeErr, syncErr, closeErr); err != nil {
			return err
		}
		return stageParent.Rename(temp, name)
	}()
	if err != nil {
		// This path has not attempted publication. Abort only this transaction;
		// never discard user edits or describe a later uncertain commit as undone.
		_, _ = o.abortBatch(l, id, officeVersionOperationID(operationID, "_abort"))
		return nil, err
	}
	return o.commitBatch(ctx, l, id, operationID)
}

func officeVersionOperationID(operationID, suffix string) string {
	if len(operationID)+len(suffix) <= 100 {
		return operationID + suffix
	}
	return fmt.Sprintf("%s_%s", operationID[:40], officeHash([]byte(operationID + suffix))[:32])
}
