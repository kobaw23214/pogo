package web

// A library viewer is an ephemeral immutable disk snapshot, not a writer. It
// shares the actual Office runtime and selection bridge but has no filesystem
// parent, mutation lease, conversation, journal, or save callback.
import (
	"encoding/json"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/information"
)

const informationOfficeViewIdle = 20 * time.Minute
const informationOfficeViewMaximum = 2 * time.Hour

type informationOfficeViewSource struct {
	EntryID        string `json:"entry_id"`
	Version        int64  `json:"version"`
	SHA256         string `json:"sha256"`
	SnapshotSHA256 string `json:"snapshot_sha256"`
	Path           string `json:"path"`
	Title          string `json:"title"`
}
type informationOfficeViewReceipt struct {
	owner       *identity.Scope
	fingerprint string
	document    *officeDocumentSession
	expires     time.Time
}

func informationOfficeViewKey(owner *identity.Scope, operation string) string {
	encoded, _ := json.Marshal(owner)
	return string(encoded) + ":" + operation
}

// Call with service.mu held. Closed receipts remain bounded tombstones so a
// late response/retry cannot reopen a viewer the user already cancelled.
func (o *officeService) informationViewReceiptLocked(key, fingerprint string) (*officeDocumentSession, error) {
	if receipt, exists := o.viewReceipts[key]; exists {
		if receipt.fingerprint != fingerprint {
			return nil, officeFail(409, "OPERATION_ID_CONFLICT", "相同打开操作不能用于其他文档或版本。")
		}
		receipt.document.mu.Lock()
		closed := receipt.document.closed
		receipt.document.mu.Unlock()
		if closed {
			return nil, officeFail(410, "VIEWER_CLOSED", "此只读预览已关闭，请重新打开。")
		}
		return receipt.document, nil
	}
	return nil, nil
}

func (s *Server) handleInformationOfficeView(w http.ResponseWriter, r *http.Request) {
	store, owner, ok := s.informationStore(w, r)
	if !ok || !s.officeAvailable(w) {
		return
	}
	var body struct {
		Version     int64  `json:"version"`
		InstanceID  string `json:"instanceId"`
		OperationID string `json:"operationId"`
	}
	if !informationBody(w, r, &body) {
		return
	}
	if body.Version < 1 || body.Version > information.MaxRevision || !officeOperationID.MatchString(body.InstanceID) || !officeOperationID.MatchString(body.OperationID) {
		officeHTTPError(w, officeFail(400, "INVALID_ARGUMENT", "请选择文档的明确版本并提供当前预览身份。"))
		return
	}
	if _, allowed := officeRequestOrigin(r); !allowed {
		officeHTTPError(w, officeFail(403, "EDITOR_ORIGIN_REJECTED", "请从当前 Pogo 页面打开只读预览。"))
		return
	}
	key := informationOfficeViewKey(owner, body.OperationID)
	fingerprint := officeHash([]byte(r.PathValue("id") + ":" + strconv.FormatInt(body.Version, 10) + ":" + body.InstanceID))
	s.office.mu.Lock()
	existing, err := s.office.informationViewReceiptLocked(key, fingerprint)
	s.office.mu.Unlock()
	if err != nil {
		officeHTTPError(w, err)
		return
	}
	if existing != nil {
		s.writeOfficeConfigRequest(w, r, existing, body.InstanceID, releaseMaintenanceGrant(r), existing.panelPath)
		return
	}
	entry, err := store.Get(r.PathValue("id"), body.Version)
	if err != nil {
		informationError(w, store, err)
		return
	}
	if entry.File == nil {
		officeHTTPError(w, officeFail(415, "UNSUPPORTED_DOCUMENT", "请选择信息库中的 .docx 或 .xlsx 文件。"))
		return
	}
	fileType := strings.ToLower(strings.TrimPrefix(path.Ext(entry.File.Name), "."))
	if fileType != "docx" && fileType != "xlsx" {
		officeHTTPError(w, officeFail(415, "UNSUPPORTED_DOCUMENT", "请选择信息库中的 .docx 或 .xlsx 文件。"))
		return
	}
	if entry.File.Size > informationOfficeViewMaxBytes {
		officeHTTPError(w, officeFail(413, "DOCUMENT_LIMIT", "Office 只读选区支持不超过 256 MiB 的文档，原文件仍保留在信息库。"))
		return
	}
	f, asset, err := store.AssetRead(entry.File.Asset)
	if err != nil {
		informationError(w, store, err)
		return
	}
	defer f.Close()
	if asset.Size != entry.File.Size || asset.SHA256 != entry.File.SHA256 {
		informationError(w, store, information.ErrInvalid)
		return
	}
	snapshot, err := informationOfficeViewFreeze(r.Context(), f, entry.File.Size, asset.SHA256, fileType)
	if err != nil {
		officeHTTPError(w, err)
		return
	}
	// Transfer the snapshot only after this exact version becomes a session;
	// retries, limits, cancellation and readiness failures otherwise clean it.
	defer func() {
		if snapshot != nil {
			snapshot.retire()
		}
	}()
	if err := s.office.waitEditorReady(r.Context()); err != nil {
		officeHTTPError(w, err)
		return
	}
	if err := r.Context().Err(); err != nil {
		return
	}
	sealed, _ := json.Marshal(entry)
	source := &informationOfficeViewSource{EntryID: entry.ID, Version: entry.Version, SHA256: asset.SHA256, SnapshotSHA256: officeHash(sealed), Path: asset.Asset, Title: entry.Name}
	o := s.office
	o.mu.Lock()
	if o.closed {
		o.mu.Unlock()
		officeHTTPError(w, officeFail(503, "OFFICE_STOPPED", "Office 服务已停止。"))
		return
	}
	existing, err = o.informationViewReceiptLocked(key, fingerprint)
	if existing != nil || err != nil {
		o.mu.Unlock()
		if err != nil {
			officeHTTPError(w, err)
			return
		}
		s.writeOfficeConfigRequest(w, r, existing, body.InstanceID, releaseMaintenanceGrant(r), existing.panelPath)
		return
	}
	now, active, owned, receipts, ownedReceipts := time.Now(), 0, 0, 0, 0
	for receiptKey, receipt := range o.viewReceipts {
		receipt.document.mu.Lock()
		closed := receipt.document.closed
		receipt.document.mu.Unlock()
		if closed && now.After(receipt.expires) {
			delete(o.viewReceipts, receiptKey)
			continue
		}
		receipts++
		if officeSameOwner(receipt.owner, owner) {
			ownedReceipts++
		}
		if !closed {
			active++
			if officeSameOwner(receipt.owner, owner) {
				owned++
			}
		}
	}
	if active >= 32 || owned >= 2 || receipts >= 512 || ownedReceipts >= 32 {
		o.mu.Unlock()
		officeHTTPError(w, officeFail(429, "VIEWER_LIMIT", "只读预览数量已达上限，请关闭其他预览后重试。"))
		return
	}
	o.generation++
	id := officeNewID()
	d := &officeDocumentSession{readOnly: true, informationSource: source, informationSnapshot: snapshot, viewCreatedAt: now, syncRevision: 1, id: id, documentID: randomID(24), conversationID: "information-view-" + id, panelPath: asset.Asset, title: entry.File.Name, fileType: fileType, editor: "word", key: "pogo-view-" + id, bridgeToken: randomID(32), fileToken: randomID(32), owner: officeCopyOwner(owner), generation: o.generation, saveState: "saved", baseHash: asset.SHA256, diskVersion: asset.SHA256, operations: map[string]*officeOperation{}, bootEpoch: o.epochLocked()}
	d.role = &officeRole{} // Private viewer capability; never registered as a conversation writer.
	if fileType == "xlsx" {
		d.editor = "cell"
	}
	d.touchWriterLocked()
	if o.sessions == nil {
		o.sessions = map[string]*officeDocumentSession{}
	}
	if o.viewReceipts == nil {
		o.viewReceipts = map[string]informationOfficeViewReceipt{}
	}
	o.sessions[id] = d
	snapshot = nil
	o.viewReceipts[key] = informationOfficeViewReceipt{owner: officeCopyOwner(owner), fingerprint: fingerprint, document: d, expires: now.Add(informationOfficeViewMaximum + informationOfficeViewIdle)}
	d.viewTimer = time.AfterFunc(informationOfficeViewIdle, func() { o.expireInformationView(d) })
	o.mu.Unlock()
	s.writeOfficeConfigRequest(w, r, d, body.InstanceID, releaseMaintenanceGrant(r), source.Path)
}

func (s *Server) handleInformationOfficeViews(w http.ResponseWriter, r *http.Request) {
	_, owner, ok := s.informationStore(w, r)
	if !ok {
		return
	}
	views := []officeSessionView{}
	if s.office != nil {
		s.office.mu.Lock()
		for _, d := range s.office.sessions {
			if d.readOnly && officeSameOwner(d.owner, owner) {
				d.mu.Lock()
				if !d.closed {
					views = append(views, d.viewLocked())
				}
				d.mu.Unlock()
			}
		}
		s.office.mu.Unlock()
	}
	sort.Slice(views, func(i, j int) bool { return views[i].Generation < views[j].Generation })
	writeJSON(w, 200, map[string]any{"active": len(views), "views": views})
}

func (o *officeService) expireInformationView(d *officeDocumentSession) {
	o.mu.Lock()
	defer o.mu.Unlock()
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed || o.closed {
		return
	}
	deadline := d.lastSeenAt.Add(informationOfficeViewIdle)
	if maximum := d.viewCreatedAt.Add(informationOfficeViewMaximum); deadline.After(maximum) {
		deadline = maximum
	}
	if remaining := time.Until(deadline); remaining > 0 {
		d.viewTimer = time.AfterFunc(remaining, func() { o.expireInformationView(d) })
		return
	}
	o.retireLocked(d)
}
