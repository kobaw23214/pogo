package web

// A browser's departure is not a saved-file receipt. This private journal
// retains abandoned writer identities and recovery copies across core restarts.
// It is outside the model-writable workspace and never persists bridge secrets.
import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/yosukeno/pogo/identity"
)

const officeEditorJournalLimit = 256
const officeRecoveryCopyLimit = 128
const officeRecoveryBytesLimit = 256 << 20

type officeEditorRecord struct {
	Schema               int                 `json:"schema"`
	Owner                *identity.Scope     `json:"owner"`
	ConversationID       string              `json:"conversationId"`
	Cwd                  string              `json:"cwd"`
	Absolute             string              `json:"absolute"`
	PanelPath            string              `json:"panelPath"`
	ID                   string              `json:"id"`
	DocumentID           string              `json:"documentId"`
	EngineKey            string              `json:"engineKey"`
	FileToken            string              `json:"fileToken"`
	BrowserOrigin        string              `json:"browserOrigin"`
	FileType             string              `json:"fileType"`
	BaseHash             string              `json:"baseHash"`
	Generation           uint64              `json:"generation"`
	DirtyEpoch           uint64              `json:"dirtyEpoch"`
	PersistedEpoch       uint64              `json:"persistedEpoch"`
	Revision             uint64              `json:"revision"`
	SavedAt              string              `json:"savedAt,omitempty"`
	LeaseEpoch           uint64              `json:"leaseEpoch"`
	LastSeenAt           time.Time           `json:"lastSeenAt"`
	LeaseExpiresAt       time.Time           `json:"leaseExpiresAt"`
	Closed               bool                `json:"closed"`
	Blocked              bool                `json:"blocked"`
	Pending              bool                `json:"pending"`
	RecoverySealed       bool                `json:"recoverySealed"`
	RecoveryCopy         *officeRecoveryCopy `json:"recoveryCopy,omitempty"`
	LegacyImported       bool                `json:"legacyImported,omitempty"`
	LegacyEvidenceFile   string              `json:"legacyEvidenceFile,omitempty"`
	LegacyEvidenceSHA256 string              `json:"legacyEvidenceSHA256,omitempty"`
	HandoffFromSessionID string              `json:"handoffFromSessionId,omitempty"`
	HandoffFingerprint   string              `json:"handoffFingerprint,omitempty"`
}

type officeRecoveryArchive struct {
	Schema    int                `json:"schema"`
	Owner     *identity.Scope    `json:"owner"`
	PanelPath string             `json:"panelPath"`
	Copy      officeRecoveryCopy `json:"copy"`
	Sealed    bool               `json:"sealed"`
}

func (o *officeService) editorJournalRoot() (string, error) {
	root := o.batchDir
	if root == "" && o.manager != nil {
		root = o.manager.cfg.SessionDir
	}
	// Small unit fixtures may intentionally have no persistent service. Real
	// HTTP services always have Manager.SessionDir and fail closed on I/O errors.
	if root == "" {
		return "", nil
	}
	dir := filepath.Join(root, "office-editors")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", officeFail(503, "EDITOR_STORAGE_UNCONFIRMED", "编辑状态目录身份无法确认。")
	}
	return dir, nil
}

func officeEditorRecordLocked(d *officeDocumentSession) officeEditorRecord {
	return officeEditorRecord{Schema: 1, Owner: officeCopyOwner(d.owner), ConversationID: d.conversationID, Cwd: d.cwd, Absolute: d.absolute, PanelPath: d.panelPath, ID: d.id, DocumentID: d.documentID, EngineKey: d.key, FileToken: d.fileToken, BrowserOrigin: d.browserOrigin, FileType: d.fileType, BaseHash: d.baseHash, Generation: d.generation, DirtyEpoch: d.dirtyEpoch, PersistedEpoch: d.persistedEpoch, Revision: d.revision, SavedAt: d.savedAt, LeaseEpoch: d.leaseEpoch, LastSeenAt: d.lastSeenAt, LeaseExpiresAt: d.leaseExpiresAt, Closed: d.closed, Blocked: d.blocked, Pending: d.saving || d.exportBusy || officePendingLocked(d) || d.freeze != nil || d.batchLease != "", RecoverySealed: d.recoverySealed, RecoveryCopy: d.recoveryCopy, LegacyImported: d.legacyImported, LegacyEvidenceFile: d.legacyEvidenceFile, LegacyEvidenceSHA256: d.legacyEvidenceSHA256, HandoffFromSessionID: d.handoffFromSessionID, HandoffFingerprint: d.handoffFingerprint}
}

func (o *officeService) persistEditorLocked(d *officeDocumentSession) error {
	if d.readOnly || d.journalFrozen || d.callbackOnly {
		return nil
	}
	dir, err := o.editorJournalRoot()
	if err == nil && dir != "" {
		var data []byte
		data, err = json.Marshal(officeEditorRecordLocked(d))
		if err == nil {
			err = officeVersionAtomicWrite(dir, d.id+".json", data)
		}
	}
	if err != nil {
		d.journalError = "编辑状态尚未持久记录，请保留当前画布。"
		return officeFail(503, "EDITOR_STORAGE_UNCONFIRMED", d.journalError)
	}
	d.journalError = ""
	return nil
}

func (o *officeService) loadEditorRecords() ([]officeEditorRecord, error) {
	dir, err := o.editorJournalRoot()
	if err != nil || dir == "" {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	result := []officeEditorRecord{}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "callback-") || strings.HasPrefix(entry.Name(), "reset-") {
			continue
		}
		if strings.HasPrefix(entry.Name(), "legacy-") {
			continue
		}
		if strings.HasPrefix(entry.Name(), "recovery-") {
			continue
		}
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		if len(result) >= officeEditorJournalLimit {
			return nil, officeFail(503, "EDITOR_STORAGE_UNCONFIRMED", "编辑状态记录超过读取边界，需要核对。")
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		if !officeSessionID.MatchString(id) {
			return nil, officeFail(503, "EDITOR_STORAGE_UNCONFIRMED", "编辑状态记录身份不正确。")
		}
		data, e := officeVersionRead(dir, entry.Name(), officeMaxJSON)
		if e != nil {
			return nil, e
		}
		var record officeEditorRecord
		if json.Unmarshal(data, &record) != nil || record.Schema != 1 || record.ID != id || !validID(record.ConversationID) || record.Cwd == "" || !filepath.IsAbs(record.Cwd) || !filepath.IsAbs(record.Absolute) || !officeVersionValidHash(record.BaseHash) || (record.FileType != "docx" && record.FileType != "xlsx") || record.DocumentID == "" || (!officeOperationID.MatchString(record.FileToken) && !(record.LegacyImported && record.FileToken == "")) || record.EngineKey != "pogo-"+id {
			return nil, officeFail(503, "EDITOR_STORAGE_UNCONFIRMED", "编辑状态记录需要核对，未自动释放占用。")
		}
		if record.LegacyImported {
			if filepath.Base(record.LegacyEvidenceFile) != record.LegacyEvidenceFile || !strings.HasPrefix(record.LegacyEvidenceFile, "legacy-") || !officeVersionValidHash(record.LegacyEvidenceSHA256) {
				return nil, officeFail(503, "EDITOR_STORAGE_UNCONFIRMED", "旧版本编辑状态缺少原始证据，未自动释放占用。")
			}
			evidence, e := officeVersionRead(dir, record.LegacyEvidenceFile, officeMaxRetainedBytes+officeMaxBytes)
			if e != nil || officeHash(evidence) != record.LegacyEvidenceSHA256 {
				return nil, officeFail(503, "EDITOR_STORAGE_UNCONFIRMED", "旧版本编辑状态证据无法核对。")
			}
		}
		result = append(result, record)
	}
	return result, nil
}

func (o *officeService) restoreEditorJournal() error {
	records, err := o.loadEditorRecords()
	if err != nil {
		return err
	}
	for _, r := range records {
		if r.Closed {
			continue
		}
		if o.active[r.Absolute] != "" {
			return officeFail(503, "EDITOR_STORAGE_UNCONFIRMED", "同一文档有重复编辑记录，需要核对。")
		}
		// Account/file authorization is checked again on every public operation.
		// Do not silently drop a dirty record if its conversation/file vanished.
		parent, name, parentInfo, e := officeOpenParent(r.Cwd, r.Absolute)
		var data []byte
		var info os.FileInfo
		unavailable := e != nil
		if !unavailable {
			data, info, e = officeReadFile(parent, name)
			unavailable = e != nil
			if unavailable {
				parent.Close()
				parent = nil
			}
		}
		panel, e := officePanelPath(r.Cwd, r.Absolute)
		if e != nil {
			if parent != nil {
				parent.Close()
			}
			return e
		}
		_ = panel // Original panelPath is account-root relative, not necessarily cwd-relative.
		d := &officeDocumentSession{id: r.ID, documentID: r.DocumentID, conversationID: r.ConversationID, cwd: r.Cwd, absolute: r.Absolute, relative: strings.TrimPrefix(r.Absolute, r.Cwd+string(os.PathSeparator)), panelPath: r.PanelPath, title: filepath.Base(r.Absolute), fileType: r.FileType, editor: "word", key: r.EngineKey, fileToken: r.FileToken, bridgeToken: randomID(32), browserOrigin: r.BrowserOrigin, bootEpoch: o.epochLocked(), owner: officeCopyOwner(r.Owner), generation: r.Generation, dirtyEpoch: r.DirtyEpoch, persistedEpoch: r.PersistedEpoch, revision: r.Revision, savedAt: r.SavedAt, baseHash: r.BaseHash, diskVersion: officeHash(data), snapshot: data, baseInfo: info, parentInfo: parentInfo, parent: parent, operations: map[string]*officeOperation{}, leaseEpoch: r.LeaseEpoch, lastSeenAt: r.LastSeenAt, leaseExpiresAt: r.LeaseExpiresAt, restarted: true, recoverySealed: r.RecoverySealed, recoveryCopy: r.RecoveryCopy, blocked: r.Blocked || r.Pending, syncRevision: 1, saveState: "saved"}
		if d.fileType == "xlsx" {
			d.editor = "cell"
		}
		if d.dirtyEpoch > d.persistedEpoch {
			d.saveState = "dirty"
		}
		if d.blocked {
			d.saveState = "error"
			d.lastError = "原编辑连接已中断，上一步结果或未保存内容需要恢复。"
		}
		if unavailable {
			d.blocked = true
			d.saveState = "error"
			d.lastError = "原文档已移动或暂不可读，原编辑状态仍保留，未自动释放。"
		}
		d.externalChange = d.diskVersion != d.baseHash
		d.role = o.roleLocked(d.conversationID)
		d.legacyImported, d.legacyEvidenceFile, d.legacyEvidenceSHA256 = r.LegacyImported, r.LegacyEvidenceFile, r.LegacyEvidenceSHA256
		d.handoffFromSessionID, d.handoffFingerprint = r.HandoffFromSessionID, r.HandoffFingerprint
		// The blob+archive are committed before the mutable writer index. If a
		// crash interrupted that final index write, recover the exact sealed
		// receipt rather than losing bytes the engine was allowed to retire.
		archives, archiveErr := o.loadRecoveryArchives(d.owner, d.panelPath)
		if archiveErr != nil {
			if parent != nil {
				parent.Close()
			}
			return archiveErr
		}
		for _, a := range archives {
			c := a.Copy
			if c.SourceSessionID == d.id && c.Generation == d.generation && c.DirtyEpoch == d.dirtyEpoch && c.PersistedEpoch == d.persistedEpoch && c.BaseVersion == d.baseHash && a.Sealed && (d.recoveryCopy == nil || d.recoveryCopy.State != "ready" || c.CreatedAt > d.recoveryCopy.CreatedAt) {
				copy := c
				d.recoveryCopy = &copy
				d.recoverySealed = true
			}
		}
		if !unavailable {
			if err = o.registerCommandProtectedSession(d); err != nil {
				if parent != nil {
					parent.Close()
				}
				return err
			}
		}
		o.sessions[d.id] = d
		o.active[d.absolute] = d.id
		if d.generation > o.generation {
			o.generation = d.generation
		}
	}
	return nil
}

func (o *officeService) storeRecoveryCopyLocked(d *officeDocumentSession, data []byte) error {
	o.recoveryArchiveMu.Lock()
	defer o.recoveryArchiveMu.Unlock()
	dir, err := o.editorJournalRoot()
	if err != nil {
		return err
	}
	if dir == "" {
		return officeFail(503, "EDITOR_STORAGE_UNCONFIRMED", "恢复副本需要持久存储。")
	}
	copy := d.recoveryCopy
	if copy == nil || !officeSessionID.MatchString(copy.ID) {
		return officeFail(409, "RECOVERY_CHANGED", "恢复身份已变化。")
	}
	if existing, e := officeVersionRead(dir, officeRecoveryKeyName(copy.ID, d.fileType), officeMaxBytes); e == nil && officeHash(existing) != officeHash(data) {
		return officeFail(409, "RECOVERY_STORAGE_CHANGED", "恢复副本已存在且内容不同，不会覆盖。")
	}
	if err = o.checkRecoveryQuotaLocked(d, len(data)); err != nil {
		return err
	}
	if err = officeVersionAtomicWrite(dir, officeRecoveryKeyName(copy.ID, d.fileType), data); err != nil {
		return err
	}
	copy.Hash, copy.Bytes, copy.State, copy.Error = officeHash(data), len(data), "ready", ""
	archive, _ := json.Marshal(officeRecoveryArchive{Schema: 1, Owner: officeCopyOwner(d.owner), PanelPath: d.panelPath, Copy: *copy, Sealed: d.recoverySealed})
	if err = officeVersionAtomicWrite(dir, officeRecoveryArchiveName(d.owner, d.panelPath, copy.ID), archive); err != nil {
		copy.State = "unknown"
		return err
	}
	if err = o.persistEditorLocked(d); err != nil {
		copy.State = "unknown"
		return err
	}
	if d.closed {
		// A final callback can arrive after normal close already compacted its
		// writer index. Keep the immutable blob/archive, without resurrecting
		// a closed writer record on every successful late callback.
		o.compactCleanEditorRecordLocked(d)
	}
	return nil
}

func (o *officeService) readRecoveryCopyLocked(d *officeDocumentSession) ([]byte, error) {
	if !officeRecoveryMatchesLocked(d) {
		return nil, officeFail(409, "RECOVERY_REQUIRED", "尚无与当前修改对应的恢复副本。")
	}
	dir, err := o.editorJournalRoot()
	if err != nil {
		return nil, err
	}
	data, err := officeVersionRead(dir, officeRecoveryKeyName(d.recoveryCopy.ID, d.fileType), officeMaxBytes)
	if err != nil || len(data) != d.recoveryCopy.Bytes || officeHash(data) != d.recoveryCopy.Hash {
		return nil, officeFail(409, "RECOVERY_STORAGE_CHANGED", "恢复副本无法核对，未释放原占用。")
	}
	return data, nil
}

func (o *officeService) recoveryCopiesFor(owner *identity.Scope, panel string) ([]officeRecoveryCopy, error) {
	records, err := o.loadRecoveryArchives(owner, panel)
	if err != nil {
		return nil, err
	}
	copies := []officeRecoveryCopy{}
	for _, r := range records {
		if officeSameOwner(r.Owner, owner) && r.PanelPath == panel {
			copies = append(copies, r.Copy)
		}
	}
	sort.Slice(copies, func(i, j int) bool { return copies[i].CreatedAt > copies[j].CreatedAt })
	if len(copies) > 20 {
		copies = copies[:20]
	}
	return copies, nil
}

func (o *officeService) readArchivedRecoveryCopy(owner *identity.Scope, panel, id string) (officeRecoveryCopy, []byte, error) {
	dir, err := o.editorJournalRoot()
	if err != nil {
		return officeRecoveryCopy{}, nil, err
	}
	raw, err := officeVersionRead(dir, officeRecoveryArchiveName(owner, panel, id), officeMaxJSON)
	if err != nil {
		return officeRecoveryCopy{}, nil, officeFail(404, "RECOVERY_NOT_FOUND", "恢复副本不存在。")
	}
	var r officeRecoveryArchive
	if json.Unmarshal(raw, &r) != nil || r.Schema != 1 {
		return officeRecoveryCopy{}, nil, officeFail(409, "RECOVERY_STORAGE_CHANGED", "恢复副本记录无法核对。")
	}
	{
		if !officeSameOwner(r.Owner, owner) || r.PanelPath != panel || r.Copy.ID != id || r.Copy.State != "ready" {
			return officeRecoveryCopy{}, nil, officeFail(404, "RECOVERY_NOT_FOUND", "恢复副本不存在。")
		}
		dir, e := o.editorJournalRoot()
		if e != nil {
			return officeRecoveryCopy{}, nil, e
		}
		data, e := officeVersionRead(dir, officeRecoveryKeyName(id, r.Copy.FileType), officeMaxBytes)
		if e != nil || officeHash(data) != r.Copy.Hash || len(data) != r.Copy.Bytes {
			return officeRecoveryCopy{}, nil, officeFail(409, "RECOVERY_STORAGE_CHANGED", "恢复副本完整性尚未确认。")
		}
		return r.Copy, data, nil
	}
}

func officeRecoveryArchivePrefix(owner *identity.Scope, panel string) string {
	key, _ := json.Marshal([]any{owner, panel})
	return "recovery-" + officeHash(key) + "-"
}
func officeRecoveryArchiveName(owner *identity.Scope, panel, id string) string {
	return officeRecoveryArchivePrefix(owner, panel) + id + ".json"
}

func (o *officeService) loadRecoveryArchives(owner *identity.Scope, panel string) ([]officeRecoveryArchive, error) {
	dir, err := o.editorJournalRoot()
	if err != nil || dir == "" {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	archives := []officeRecoveryArchive{}
	prefix := officeRecoveryArchivePrefix(owner, panel)
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), prefix) || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(strings.TrimPrefix(entry.Name(), prefix), ".json")
		if !officeSessionID.MatchString(id) {
			return nil, officeFail(503, "EDITOR_STORAGE_UNCONFIRMED", "恢复记录身份需要核对。")
		}
		data, e := officeVersionRead(dir, entry.Name(), officeMaxJSON)
		if errors.Is(e, fs.ErrNotExist) {
			continue
		}
		if e != nil {
			return nil, e
		}
		var archive officeRecoveryArchive
		if json.Unmarshal(data, &archive) != nil || archive.Schema != 1 || archive.Copy.ID != id || archive.Copy.State != "ready" || !officeVersionValidHash(archive.Copy.Hash) || (archive.Copy.FileType != "docx" && archive.Copy.FileType != "xlsx") {
			return nil, officeFail(503, "EDITOR_STORAGE_UNCONFIRMED", "恢复记录无法核对。")
		}
		archives = append(archives, archive)
	}
	return archives, nil
}

func (o *officeService) checkRecoveryQuotaLocked(d *officeDocumentSession, newBytes int) error {
	archives, err := o.loadRecoveryArchives(d.owner, d.panelPath)
	if err != nil {
		return err
	}
	sort.Slice(archives, func(i, j int) bool { return archives[i].Copy.CreatedAt < archives[j].Copy.CreatedAt })
	total := newBytes
	count := 1
	for _, r := range archives {
		if r.Copy.ID != d.recoveryCopy.ID {
			total += r.Copy.Bytes
			count++
		}
	}
	dir, err := o.editorJournalRoot()
	if err != nil {
		return err
	}
	for _, r := range archives {
		if count <= officeRecoveryCopyLimit && total <= officeRecoveryBytesLimit {
			break
		}
		// Only a confirmed canonical save by this exact writer can cover an
		// older checkpoint. Unresolved/other-writer copies are never discarded.
		if r.Copy.ID == d.recoveryCopy.ID || r.Copy.SourceSessionID != d.id || r.Copy.DirtyEpoch > d.persistedEpoch || r.Copy.BaseVersion == d.baseHash {
			continue
		}
		if err = os.Remove(filepath.Join(dir, officeRecoveryArchiveName(d.owner, d.panelPath, r.Copy.ID))); err != nil {
			return err
		}
		_ = os.Remove(filepath.Join(dir, officeRecoveryKeyName(r.Copy.ID, r.Copy.FileType)))
		if err = officeVersionSyncDir(dir); err != nil {
			return err
		}
		count--
		total -= r.Copy.Bytes
	}
	if count > officeRecoveryCopyLimit || total > officeRecoveryBytesLimit {
		return officeFail(507, "RECOVERY_STORAGE_LIMIT", "未处理的恢复副本已达保留上限，请先下载核对；原修改不会自动删除。")
	}
	return nil
}

// Retain dirty/in-flight records on shutdown. Retiring runtime capabilities is
// necessary, but must not rewrite their durable record as successfully closed.
func (o *officeService) checkpointEditorsBeforeShutdownLocked() {
	for _, d := range o.sessions {
		d.mu.Lock()
		if !d.closed {
			_ = o.persistEditorLocked(d)
		}
		d.journalFrozen = true
		d.mu.Unlock()
	}
}

func (o *officeService) compactCleanEditorRecordLocked(d *officeDocumentSession) {
	if d.readOnly {
		return
	}
	if !d.closed || (!d.retirementAuthorized && (d.dirtyEpoch > d.persistedEpoch || d.blocked || d.saving || d.exportBusy || officePendingLocked(d))) {
		return
	}
	if d.recoveryCopy != nil && d.recoveryCopy.State != "ready" {
		return
	}
	if err := o.persistCallbackIdentityLocked(d); err != nil {
		d.journalError = "原文档的晚到回执身份尚未安全保留。"
		return
	}
	dir, err := o.editorJournalRoot()
	if err != nil || dir == "" {
		return
	}
	if err = os.Remove(filepath.Join(dir, d.id+".json")); err == nil {
		_ = officeVersionSyncDir(dir)
	} else if !errors.Is(err, fs.ErrNotExist) {
		d.journalError = "编辑关闭状态需要核对。"
	}
}
