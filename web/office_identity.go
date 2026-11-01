package web

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/tools"
)

// A file reference names an owner's canonical workspace path, independently of
// the transient editor session, service boot, content revision and process cwd.
// JSON struct encoding keeps full ownership fields unambiguous.
func officeFileRef(owner *identity.Scope, panelPath string) string {
	data, _ := json.Marshal(struct {
		Owner *identity.Scope `json:"owner"`
		Path  string          `json:"path"`
	}{owner, filepath.ToSlash(filepath.Clean(panelPath))})
	return "office-file-" + officeHash(data)
}

// Call while holding the service mutex. Tests and legacy constructors may
// build a service directly; initialize once rather than exposing an empty epoch.
func (o *officeService) epochLocked() string {
	if o.bootEpoch == "" {
		o.bootEpoch = officeNewID()
	}
	return o.bootEpoch
}

func (l *officeLink) workspaceRoot() string {
	if l.service.manager == nil {
		return l.cwd
	}
	if l.owner != nil {
		return l.service.manager.UserRoot(*l.owner)
	}
	return l.service.manager.Cwd()
}

// Accept a legacy global prefix only for this complete authenticated owner.
// Other accounts' prefixes are rejected before path resolution; no suffix
// matching or arbitrary "users/..." removal can grant access.
func officeOwnerPath(manager *Manager, owner *identity.Scope, input string) (string, bool, error) {
	clean := filepath.ToSlash(filepath.Clean(input))
	if manager == nil || owner == nil || filepath.IsAbs(input) {
		return clean, false, nil
	}
	prefix, ok := manager.UserRootRel(*owner)
	if !ok {
		return "", false, officeFail(400, "PATH_REJECTED", "当前账号工作区身份不正确。")
	}
	if clean == prefix {
		return ".", true, nil
	}
	if strings.HasPrefix(clean, prefix+"/") {
		return strings.TrimPrefix(clean, prefix+"/"), true, nil
	}
	if clean == "users" || strings.HasPrefix(clean, "users/") {
		return "", false, officeFail(400, "PATH_REJECTED", "文档不在当前账号工作区。")
	}
	return clean, false, nil
}

func (l *officeLink) resolvePath(input string) (string, error) {
	path, legacy, err := officeOwnerPath(l.service.manager, l.owner, input)
	if err != nil {
		return "", err
	}
	root := l.cwd
	if legacy {
		root = l.workspaceRoot()
	} else if panelCwd, err := officePanelPath(l.workspaceRoot(), l.cwd); err == nil && panelCwd != "." && strings.HasPrefix(path, panelCwd+"/") {
		root = l.workspaceRoot()
	}
	absolute, err := tools.Resolve(root, path)
	if err != nil {
		return "", err
	}
	// Even accepted aliases remain restricted to the agent's session cwd.
	return tools.Resolve(l.cwd, absolute)
}

func (s *Server) officeRequestedPath(r *http.Request, owner *identity.Scope, input string) (string, string, error) {
	if input == "" {
		return "", "", officeFail(400, "PATH_REJECTED", "请提供文档路径。")
	}
	path, _, err := officeOwnerPath(s.mgr, owner, input)
	if err != nil {
		return "", "", err
	}
	root := s.requestRoot(r)
	absolute, err := tools.Resolve(root, path)
	if err != nil {
		return "", "", officeFail(400, "PATH_REJECTED", "文档不在当前账号工作区。")
	}
	canonical, err := officePanelPath(root, absolute)
	return absolute, canonical, err
}

// recoverOpenClaim only retrieves this exact authenticated opener's claim. It
// does not expire, transfer, release or replace another instance's canvas.
func (o *officeService) recoverOpenClaim(conversationID, cwd string, owner *identity.Scope, absolute, instance string) (*officeDocumentSession, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return nil, officeFail(503, "OFFICE_STOPPED", "Office 服务已停止。")
	}
	d := o.sessions[o.active[absolute]]
	if d == nil {
		return nil, nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return nil, nil
	}
	if d.role != nil && d.role.revoked.Load() {
		return nil, officeFail(409, "CONVERSATION_CLOSED", "当前会话的 Office 操作身份已撤销。")
	}
	if d.conversationID != conversationID || d.cwd != cwd || !officeSameOwner(d.owner, owner) {
		return nil, officeFail(409, "DOCUMENT_ALREADY_OPEN", "该文件已在另一个会话中打开，请先保存并关闭原编辑会话。")
	}
	if d.instanceID != "" && d.instanceID != instance {
		return nil, officeFail(409, "EDITOR_ALREADY_CONNECTED", "该文档已在另一编辑器实例中打开。")
	}
	if d.instanceID == instance && instance != "" {
		return d, nil
	}
	return nil, nil
}

// Releasing a failed bootstrap is narrower than closing a document. Ordinary
// account authorization does not let a second editor retire an existing canvas.
func (o *officeService) releaseBootstrap(d *officeDocumentSession, instance, token, fileRef, bootEpoch string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	d.mu.Lock()
	defer d.mu.Unlock()
	if o.sessions[d.id] != d {
		return officeFail(404, "DOCUMENT_NOT_FOUND", "文档会话不存在。")
	}
	if instance == "" || instance != d.instanceID || token == "" || !officeEqual(token, d.bootstrapToken) || fileRef != officeFileRef(d.owner, d.panelPath) || bootEpoch == "" || bootEpoch != d.bootEpoch {
		return officeFail(409, "EDITOR_INSTANCE_MISMATCH", "该启动请求不拥有当前文档。")
	}
	if d.closed {
		return nil
	}
	if d.canvasMounted || d.ready || len(d.capabilities) > 0 || d.dirtyEpoch != 0 || d.persistedEpoch != 0 || d.revision != 0 || len(d.operations) > 0 || len(d.queue) > 0 || d.exportIntent != nil {
		return officeFail(409, "BOOTSTRAP_RELEASE_UNSAFE", "编辑器已开始使用，请保留画布并正常保存、关闭。")
	}
	if err := officeCanCloseLocked(d); err != nil {
		return err
	}
	o.retireLocked(d)
	return nil
}

func (s *Server) handleOfficeBootstrapRelease(w http.ResponseWriter, r *http.Request) {
	document, ok := s.officeForRequest(w, r)
	if !ok {
		return
	}
	document.mu.Lock()
	origin := document.browserOrigin
	document.mu.Unlock()
	if origin != "" && !officeBoundOrigin(r, origin) {
		officeHTTPError(w, officeFail(403, "EDITOR_ORIGIN_REJECTED", "该编辑器属于另一个 Pogo 页面来源。"))
		return
	}
	var body struct {
		InstanceID     string `json:"instanceId"`
		BootstrapToken string `json:"bootstrapToken"`
		FileRef        string `json:"fileRef"`
		BootEpoch      string `json:"bootEpoch"`
	}
	if err := officeReadBody(r, &body); err != nil {
		officeHTTPError(w, err)
		return
	}
	if err := s.office.releaseBootstrap(document, body.InstanceID, body.BootstrapToken, body.FileRef, body.BootEpoch); err != nil {
		officeHTTPError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "session": document.view()})
}
