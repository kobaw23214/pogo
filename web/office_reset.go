package web

// Reset is an explicit user choice to abandon an old canvas and edit the saved
// bytes at the original path. It is neither recovery nor a successful save.
import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/yosukeno/pogo/tools"
)

type officeResetRequest struct {
	ConversationID         string `json:"conversationId"`
	Path                   string `json:"path"`
	InstanceID             string `json:"instanceId"`
	ExpectedSessionID      string `json:"expectedSessionId"`
	ExpectedGeneration     uint64 `json:"expectedGeneration"`
	ExpectedDirtyEpoch     uint64 `json:"expectedDirtyEpoch"`
	ExpectedPersistedEpoch uint64 `json:"expectedPersistedEpoch"`
	ExpectedLeaseEpoch     uint64 `json:"expectedLeaseEpoch"`
	ExpectedVersion        string `json:"expectedVersion"`
	ExpectedCanvasVersion  string `json:"expectedCanvasVersion"`
	ConfirmDiscardChanges  bool   `json:"confirmDiscardChanges"`
	AllowInterruptOnline   bool   `json:"allowInterruptOnline"`
}

type officeResetAudit struct {
	Schema       int                `json:"schema"`
	Kind         string             `json:"kind"`
	ConfirmedAt  string             `json:"confirmedAt"`
	Fingerprint  string             `json:"fingerprint"`
	Request      officeResetRequest `json:"request"`
	Original     officeEditorRecord `json:"original"`
	SavedVersion string             `json:"savedVersion"`
}

func officeCanResetLocked(d *officeDocumentSession) bool {
	return !d.closed && !d.journalFrozen && !d.saving && !d.exportBusy && !officePendingLocked(d) && d.freeze == nil && d.batchLease == "" && !d.replacementPending
}
func officeResetNeedsOnlineConfirmationLocked(d *officeDocumentSession) bool {
	state := d.connectionStateLocked(time.Now())
	return state == "online" || state == "unconfirmed"
}
func officeResetMatchesLocked(d *officeDocumentSession, p officeResetRequest) bool {
	return d.id == p.ExpectedSessionID && d.generation == p.ExpectedGeneration && d.dirtyEpoch == p.ExpectedDirtyEpoch && d.persistedEpoch == p.ExpectedPersistedEpoch && d.leaseEpoch == p.ExpectedLeaseEpoch && d.baseHash == p.ExpectedCanvasVersion
}

func (o *officeService) persistResetAuditLocked(d *officeDocumentSession, p officeResetRequest, fingerprint string) error {
	dir, err := o.editorJournalRoot()
	if err != nil || dir == "" {
		return officeFail(503, "RESET_AUDIT_UNCONFIRMED", "清理确认尚未安全记录，原编辑权没有改变。")
	}
	name := "reset-" + d.id + ".json"
	if data, e := officeVersionRead(dir, name, officeMaxJSON); e == nil {
		var old officeResetAudit
		if json.Unmarshal(data, &old) != nil || !officeResetAuditValid(old, d.id) || !officeSameOwner(old.Original.Owner, d.owner) || old.Original.DocumentID != d.documentID || old.Original.EngineKey != d.key || old.Original.FileToken != d.fileToken || old.Original.Absolute != d.absolute || old.Original.Cwd != d.cwd || old.Original.PanelPath != d.panelPath {
			return officeFail(409, "RESET_AUDIT_CHANGED", "已有清理确认的原身份无法核对，未清理占用。")
		}
		if old.Fingerprint == fingerprint {
			return nil
		}
		// A prior confirmation may have reached disk while the writer index
		// failed. Preserve it verbatim before accepting a refreshed intention;
		// the caller still holds this active writer's exact CAS/file locks.
		history := "reset-history-" + d.id + "-" + officeHash([]byte(old.Fingerprint)) + ".json"
		if retained, readErr := officeVersionRead(dir, history, officeMaxJSON); readErr == nil {
			if !bytes.Equal(retained, data) {
				return officeFail(409, "RESET_AUDIT_CHANGED", "原清理确认的历史记录不同，未覆盖。")
			}
		} else if errors.Is(readErr, fs.ErrNotExist) {
			if err = officeVersionAtomicWrite(dir, history, data); err != nil {
				return officeFail(503, "RESET_AUDIT_UNCONFIRMED", "原清理确认尚未封存，原编辑权没有改变。")
			}
		} else {
			return officeFail(503, "RESET_AUDIT_UNCONFIRMED", "原清理确认的历史不可读取，原编辑权没有改变。")
		}

	} else if !errors.Is(e, fs.ErrNotExist) {
		return officeFail(503, "RESET_AUDIT_UNCONFIRMED", "清理确认记录不可读取，原编辑权没有改变。")
	}
	original := officeEditorRecordLocked(d)
	original.Closed = true
	original.RecoverySealed = true
	audit := officeResetAudit{Schema: 1, Kind: "discard-canvas", ConfirmedAt: time.Now().UTC().Format(time.RFC3339Nano), Fingerprint: fingerprint, Request: p, Original: original, SavedVersion: p.ExpectedVersion}
	data, err := json.Marshal(audit)
	if err == nil {
		err = officeVersionAtomicWrite(dir, name, data)
	}
	if err != nil {
		return officeFail(503, "RESET_AUDIT_UNCONFIRMED", "清理确认尚未安全记录，原编辑权没有改变。")
	}
	return nil
}

func officeResetAuditValid(a officeResetAudit, id string) bool {
	p, r := a.Request, a.Original
	body, _ := json.Marshal(p)
	return a.Schema == 1 && a.Kind == "discard-canvas" && p.ConfirmDiscardChanges && p.ExpectedSessionID == id && r.ID == id && r.Closed && r.RecoverySealed && r.EngineKey == "pogo-"+id && r.DocumentID != "" && r.PanelPath != "" && r.Generation == p.ExpectedGeneration && r.DirtyEpoch == p.ExpectedDirtyEpoch && r.PersistedEpoch == p.ExpectedPersistedEpoch && r.LeaseEpoch == p.ExpectedLeaseEpoch && r.BaseHash == p.ExpectedCanvasVersion && officeVersionValidHash(r.BaseHash) && a.SavedVersion == p.ExpectedVersion && officeVersionValidHash(a.SavedVersion) && a.Fingerprint == "reset:"+officeHash(body)
}

// A reset receipt authorizes only the original engine's signed archival
// callback. It is deliberately excluded from active-editor restoration.
func (o *officeService) loadResetCallbackIdentity(id string) (officeEditorRecord, bool) {
	dir, err := o.editorJournalRoot()
	if err != nil || dir == "" {
		return officeEditorRecord{}, false
	}
	data, err := officeVersionRead(dir, "reset-"+id+".json", officeMaxJSON)
	var audit officeResetAudit
	if err != nil || json.Unmarshal(data, &audit) != nil || !officeResetAuditValid(audit, id) || !officeCallbackRecordValid(audit.Original, id) {
		return officeEditorRecord{}, false
	}

	return audit.Original, true
}

func (s *Server) handleOfficeReset(w http.ResponseWriter, r *http.Request) {
	if !s.officeAvailable(w) {
		return
	}
	var p officeResetRequest
	if err := officeReadBody(r, &p); err != nil {
		officeHTTPError(w, err)
		return
	}
	if !p.ConfirmDiscardChanges {
		officeHTTPError(w, officeFail(400, "RESET_CONFIRMATION_REQUIRED", "请明确确认放弃原窗口尚未写回的修改，再清理占用。"))
		return
	}
	if !officeOperationID.MatchString(p.InstanceID) || !officeSessionID.MatchString(p.ExpectedSessionID) || !officeVersionValidHash(p.ExpectedVersion) || !officeVersionValidHash(p.ExpectedCanvasVersion) {
		officeHTTPError(w, officeFail(400, "INVALID_ARGUMENT", "清理占用的身份或版本不正确。"))
		return
	}
	session, absolute, panel, d, err := s.officeAccessDocument(r, p.ConversationID, p.Path)
	if err != nil {
		officeHTTPError(w, err)
		return
	}
	if d == nil {
		officeHTTPError(w, officeFail(409, "EDITOR_LEASE_CHANGED", "原编辑占用已改变，请重新查看。"))
		return
	}
	if _, ok := officeRequestOrigin(r); !ok {
		officeHTTPError(w, officeFail(403, "EDITOR_ORIGIN_REJECTED", "请从当前 Pogo 页面清理占用。"))
		return
	}
	encoded, _ := json.Marshal(p)
	fingerprint := "reset:" + officeHash(encoded)
	d.mu.Lock()
	repeated := !d.closed && d.handoffFromSessionID == p.ExpectedSessionID && d.handoffFingerprint == fingerprint && d.instanceID == p.InstanceID && d.conversationID == p.ConversationID
	matches := officeResetMatchesLocked(d, p)
	d.mu.Unlock()
	if repeated {
		s.writeOfficeConfigRequest(w, r, d, p.InstanceID, releaseMaintenanceGrant(r), p.Path)
		return
	}
	if !matches {
		officeHTTPError(w, officeFail(409, "EDITOR_LEASE_CHANGED", "原编辑状态已变化，未清理占用。"))
		return
	}
	if err = s.office.waitEditorReady(r.Context()); err != nil {
		officeHTTPError(w, err)
		return
	}
	var fresh *officeDocumentSession
	err = tools.WithFileMutationLock(absolute, func() error {
		session.mu.Lock()
		defer session.mu.Unlock()
		if session.closing {
			return officeFail(409, "CONVERSATION_CLOSING", "会话正在关闭。")
		}
		s.office.mu.Lock()
		defer s.office.mu.Unlock()
		d.mu.Lock()
		defer d.mu.Unlock()
		if s.office.closed || s.office.active[absolute] != d.id || !officeResetMatchesLocked(d, p) {
			return officeFail(409, "EDITOR_LEASE_CHANGED", "原编辑状态已变化，未清理占用。")
		}
		if !officeCanResetLocked(d) {
			return officeFail(409, "EDITOR_BUSY", "仍有编辑、保存或批处理操作待确认，请等待其结束再清理占用。")
		}
		if officeResetNeedsOnlineConfirmationLocked(d) && !p.AllowInterruptOnline {
			return officeFail(409, "EDITOR_STILL_CONNECTED", "原编辑窗口已在线，请重新确认是否中断该窗口。")
		}
		data, _, _, e := officeDiskRead(session.cwd, absolute)
		if e != nil {
			return e
		}
		if officeHash(data) != p.ExpectedVersion {
			return officeFail(409, "FILE_CHANGED", "已保存文件版本已变化，请重新查看后确认。")
		}
		if e = s.office.persistResetAuditLocked(d, p, fingerprint); e != nil {
			return e
		}
		// Persist the exact abandoned state before fencing. Epochs and canonical
		// bytes remain untouched; the audit never claims unexported edits saved.
		d.closed = true
		if e = s.office.persistEditorLocked(d); e != nil {
			d.closed = false
			return e
		}
		d.closed = false
		d.retirementAuthorized = true
		d.recoverySealed = true
		d.callbackOnly = true // The durable reset audit now owns this retired identity.
		s.office.retireLocked(d)
		if dir, e := s.office.editorJournalRoot(); e == nil && dir != "" {
			// Unknown/failed recoveries must not accumulate as writer records:
			// explicit discard is durable in reset-<id>, outside the writer quota.
			if e = os.Remove(filepath.Join(dir, d.id+".json")); e == nil {
				_ = officeVersionSyncDir(dir)
			}
		}
		canonical, e := tools.Resolve(session.cwd, ".")
		if e != nil {
			return e
		}
		relative, e := filepath.Rel(canonical, absolute)
		if e != nil {
			return e
		}
		journal := s.mgr.JournalFor(session.root).ForSession(session.ID)
		fresh, e = s.office.createDocumentLocked(session.ID, session.cwd, session.owner, session.hub, absolute, panel, journal, canonical, relative, d.fileType)
		if e != nil {
			return e
		}
		fresh.handoffFromSessionID, fresh.handoffFingerprint, fresh.instanceID = p.ExpectedSessionID, fingerprint, p.InstanceID
		fresh.touchWriterLocked()
		return s.office.persistEditorLocked(fresh)
	})
	if err != nil {
		officeHTTPError(w, err)
		return
	}
	s.writeOfficeConfigRequest(w, r, fresh, p.InstanceID, releaseMaintenanceGrant(r), p.Path)
}
