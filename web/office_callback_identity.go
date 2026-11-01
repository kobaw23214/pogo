package web

import (
	"encoding/json"
	"path/filepath"
)

// Callback identities are small sealed receipts, separate from active writer
// journals and their quota. They retain only the authority to archive an old
// engine's signed output after takeover/core restart; never an editing grant.
func (o *officeService) persistCallbackIdentityLocked(d *officeDocumentSession) error {
	if !d.closed || !d.retirementAuthorized || !d.recoverySealed || d.recoveryCopy == nil || d.recoveryCopy.State != "ready" || d.fileToken == "" {
		return nil
	}
	dir, err := o.editorJournalRoot()
	if err != nil || dir == "" {
		return err
	}
	record := officeEditorRecordLocked(d)
	record.Closed, record.RecoverySealed = true, true
	data, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return officeVersionAtomicWrite(dir, "callback-"+d.id+".json", data)
}

func (o *officeService) loadCallbackIdentity(id string) (*officeDocumentSession, error) {
	if !officeSessionID.MatchString(id) {
		return nil, officeFail(404, "DOCUMENT_NOT_FOUND", "原文档回执身份不存在。")
	}
	dir, err := o.editorJournalRoot()
	if err != nil || dir == "" {
		return nil, officeFail(404, "DOCUMENT_NOT_FOUND", "原文档回执身份不存在。")
	}
	data, err := officeVersionRead(dir, "callback-"+id+".json", officeMaxJSON)
	var r officeEditorRecord
	valid := err == nil && json.Unmarshal(data, &r) == nil && officeCallbackRecordValid(r, id)
	if !valid {
		// If persisting the sealed identity failed, compaction deliberately
		// kept the closed writer index. It can serve the same narrow purpose.
		data, err = officeVersionRead(dir, id+".json", officeMaxJSON)
		valid = err == nil && json.Unmarshal(data, &r) == nil && officeCallbackRecordValid(r, id)
	}
	if !valid {
		r, valid = o.loadResetCallbackIdentity(id)
	}
	if !valid {
		return nil, officeFail(404, "DOCUMENT_NOT_FOUND", "原文档回执身份不存在。")
	}
	d := &officeDocumentSession{id: r.ID, documentID: r.DocumentID, conversationID: r.ConversationID, cwd: r.Cwd, absolute: r.Absolute, panelPath: r.PanelPath, title: filepath.Base(r.PanelPath), fileType: r.FileType, key: r.EngineKey, fileToken: r.FileToken, browserOrigin: r.BrowserOrigin, owner: officeCopyOwner(r.Owner), generation: r.Generation, dirtyEpoch: r.DirtyEpoch, persistedEpoch: r.PersistedEpoch, revision: r.Revision, baseHash: r.BaseHash, recoveryCopy: r.RecoveryCopy, closed: true, recoverySealed: true, callbackOnly: true, retirementAuthorized: true}
	return d, nil
}

func officeCallbackRecordValid(r officeEditorRecord, id string) bool {
	return r.Schema == 1 && r.Closed && r.ID == id && r.EngineKey == "pogo-"+id && officeOperationID.MatchString(r.FileToken) && officeVersionValidHash(r.BaseHash) && r.DocumentID != "" && r.PanelPath != "" && (r.FileType == "docx" || r.FileType == "xlsx")
}
