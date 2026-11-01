package web

// A ready fallback snapshot is immutable. A later force-save may contain more
// engine changes, so keep its bytes in a separate archive without changing the
// writer's recovery/takeover binding or canonical saved state.
import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"time"

	"github.com/google/uuid"
)

func (o *officeService) archiveLateRecoveryCallback(ctx context.Context, d *officeDocumentSession, originalID, receiptHash string, source *url.URL) error {
	if !officeSessionID.MatchString(originalID) || !officeVersionValidHash(receiptHash) {
		return officeFail(409, "RECOVERY_CHANGED", "恢复回执没有已签发的关联身份。")
	}
	d.mu.Lock()
	original, _, err := o.readArchivedRecoveryCopy(d.owner, d.panelPath, originalID)
	if err != nil || !officeRecoveryTargetMatchesLocked(d, &original) || !d.recoverySealed || !officeExportDocumentPath(source.Path, d.key) {
		d.mu.Unlock()
		return officeFail(409, "RECOVERY_CHANGED", "晚到快照不属于已确认的原恢复目标。")
	}
	fileType := d.fileType
	d.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, "GET", source.String(), nil)
	if err != nil {
		return err
	}
	response, err := o.cfg.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return officeFail(502, "RECOVERY_FETCH_FAILED", "晚到的恢复快照尚未下载完成。")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, officeMaxBytes+1))
	if err != nil {
		return err
	}
	if len(data) > officeMaxBytes {
		return officeFail(413, "DOCUMENT_LIMIT", "恢复快照超过文档大小上限。")
	}
	if err = officeCheckPackage(data, fileType); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if !officeRecoveryTargetMatchesLocked(d, &original) || !d.recoverySealed || d.fileType != fileType {
		return officeFail(409, "RECOVERY_CHANGED", "恢复目标已变化，未接受晚到快照。")
	}
	if original.EngineReceiptHash == receiptHash {
		if original.Hash != officeHash(data) {
			return officeFail(409, "RECOVERY_STORAGE_CHANGED", "重复回执的字节与已保留快照不一致。")
		}
		return nil
	}
	// The ID is deterministic for the issued recovery and signed receipt.
	// Repeated callbacks must verify, rather than overwrite, the same artifact.
	id := uuid.NewSHA1(uuid.NameSpaceOID, []byte("pogo-office-late\x00"+originalID+"\x00"+receiptHash)).String()
	copy := officeRecoveryCopy{ID: id, State: "ready", Title: "补充恢复副本-" + d.title, FileType: fileType, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), SourceSessionID: original.SourceSessionID, Generation: original.Generation, DirtyEpoch: original.DirtyEpoch, PersistedEpoch: original.PersistedEpoch, BaseVersion: original.BaseVersion, Hash: officeHash(data), Bytes: len(data), EngineReceiptHash: receiptHash, Source: "forcesave", Coverage: "engine-snapshot", OriginalRecoveryID: originalID}
	o.recoveryArchiveMu.Lock()
	defer o.recoveryArchiveMu.Unlock()
	dir, err := o.editorJournalRoot()
	if err != nil {
		return err
	}
	if dir == "" {
		return officeFail(503, "EDITOR_STORAGE_UNCONFIRMED", "恢复快照需要持久存储。")
	}
	if existing, readErr := officeVersionRead(dir, officeRecoveryArchiveName(d.owner, d.panelPath, id), officeMaxJSON); readErr == nil {
		var archive officeRecoveryArchive
		if json.Unmarshal(existing, &archive) != nil || archive.Schema != 1 || !officeSameOwner(archive.Owner, d.owner) || archive.PanelPath != d.panelPath || archive.Copy.ID != id || archive.Copy.OriginalRecoveryID != originalID || archive.Copy.EngineReceiptHash != receiptHash || archive.Copy.SourceSessionID != d.id || archive.Copy.Generation != original.Generation || archive.Copy.DirtyEpoch != original.DirtyEpoch || archive.Copy.PersistedEpoch != original.PersistedEpoch || archive.Copy.BaseVersion != original.BaseVersion || archive.Copy.Hash != copy.Hash || archive.Copy.Bytes != len(data) || archive.Sealed {
			return officeFail(409, "RECOVERY_STORAGE_CHANGED", "晚到恢复快照的既有记录不同，不会覆盖。")
		}
		stored, e := officeVersionRead(dir, officeRecoveryKeyName(id, fileType), officeMaxBytes)
		if e != nil || officeHash(stored) != copy.Hash || len(stored) != len(data) {
			return officeFail(409, "RECOVERY_STORAGE_CHANGED", "晚到恢复快照的既有字节无法核对。")
		}
		return nil
	} else if !errors.Is(readErr, fs.ErrNotExist) {
		return officeFail(409, "RECOVERY_STORAGE_CHANGED", "既有晚到恢复记录无法安全读取，不会替换。")
	}
	if stored, e := officeVersionRead(dir, officeRecoveryKeyName(id, fileType), officeMaxBytes); e == nil {
		if officeHash(stored) != copy.Hash {
			return officeFail(409, "RECOVERY_STORAGE_CHANGED", "晚到恢复快照已存在不同字节，不会覆盖。")
		}
	} else if !errors.Is(e, fs.ErrNotExist) {
		return officeFail(409, "RECOVERY_STORAGE_CHANGED", "既有晚到恢复字节无法安全读取，不会替换。")
	}
	// Only this storage projection enters quota accounting; it is never a
	// live editor and is never written into the original writer's journal.
	projection := &officeDocumentSession{id: d.id, owner: d.owner, panelPath: d.panelPath, baseHash: d.baseHash, persistedEpoch: d.persistedEpoch, recoveryCopy: &copy}
	if err = o.checkRecoveryQuotaLocked(projection, len(data)); err != nil {
		return err
	}
	if err = officeVersionAtomicWrite(dir, officeRecoveryKeyName(id, fileType), data); err != nil {
		return err
	}
	archive, _ := json.Marshal(officeRecoveryArchive{Schema: 1, Owner: officeCopyOwner(d.owner), PanelPath: d.panelPath, Copy: copy, Sealed: false})
	return officeVersionAtomicWrite(dir, officeRecoveryArchiveName(d.owner, d.panelPath, id), archive)
}
