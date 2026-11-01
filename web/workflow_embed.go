package web

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/taskqueue"
	"github.com/yosukeno/pogo/workflow"
)

const workflowEmbedPrefix = "/api/workflow-embed/publications/"
const workflowEmbedPagePrefix = "/embed/workflows/"

type workflowEmbedCredential struct {
	PublicationID     string
	Revision          int
	Subject           string
	Username          string
	ParentOrigin      string
	RunID             string
	BusinessID        string
	Inputs            map[string]string
	BusinessApprovers map[string][]string
	ExpiresAt         time.Time
}
type workflowEmbedRuntime struct {
	publications *workflowPublicationStore
	mu           sync.Mutex
	codes        map[string]workflowEmbedCredential
	sessions     map[string]workflowEmbedCredential
	now          func() time.Time
}

func newWorkflowEmbedRuntime(dir string) (*workflowEmbedRuntime, error) {
	p, err := openWorkflowPublications(dir)
	if err != nil {
		return nil, err
	}
	return &workflowEmbedRuntime{publications: p, codes: map[string]workflowEmbedCredential{}, sessions: map[string]workflowEmbedCredential{}, now: time.Now}, nil
}
func workflowEmbedDigest(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}
func workflowEmbedBearer(r *http.Request) string {
	a := r.Header.Get("Authorization")
	if !strings.HasPrefix(a, "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(a, "Bearer ")
}
func (e *workflowEmbedRuntime) pruneLocked() {
	now := e.now()
	for key, c := range e.codes {
		if !now.Before(c.ExpiresAt) {
			delete(e.codes, key)
		}
	}
	for key, c := range e.sessions {
		if !now.Before(c.ExpiresAt) {
			delete(e.sessions, key)
		}
	}
}
func (e *workflowEmbedRuntime) issue(c workflowEmbedCredential, session bool) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.pruneLocked()
	if len(e.codes)+len(e.sessions) >= 10000 {
		return "", errors.New("嵌入连接过多，请稍后重试")
	}
	prefix := "wlc_"
	target := e.codes
	if session {
		prefix = "wes_"
		target = e.sessions
	}
	token := prefix + rand.Text() + rand.Text()
	target[workflowEmbedDigest(token)] = c
	return token, nil
}
func (s *Server) registerWorkflowEmbedRoutes() {
	s.mux.HandleFunc("GET /api/manage/workflows/{wid}/publications", s.handleWorkflowPublicationList)
	s.mux.HandleFunc("POST /api/manage/workflows/{wid}/publications", s.handleWorkflowPublicationCreate)
	s.mux.HandleFunc("PUT /api/manage/workflows/{wid}/publications/{pid}", s.handleWorkflowPublicationUpdate)
	s.mux.HandleFunc("POST /api/manage/workflows/{wid}/publications/{pid}/rotate-secret", s.handleWorkflowPublicationRotate)
	s.mux.HandleFunc(workflowEmbedPrefix, s.handleWorkflowEmbed)
	s.mux.HandleFunc(workflowEmbedPagePrefix, s.handleWorkflowEmbedPage)
}
func (s *Server) canManageWorkflowPublications(r *http.Request, wid string) bool {
	p := s.workflowPrincipal(r)
	if p.Kind != identity.PrincipalLocal && p.Kind != identity.PrincipalOperator {
		return false
	}
	role, err := s.workflowRole(p)
	if err != nil || role != "space_admin" {
		return false
	}
	for _, action := range []string{wfPublish, wfManageAccess} {
		allowed, err := s.workflowAllowed(r, wid, action)
		if err != nil || !allowed {
			return false
		}
	}
	return true
}
func (s *Server) publicationManagement(w http.ResponseWriter, r *http.Request) bool {
	if !s.canManageWorkflowPublications(r, r.PathValue("wid")) {
		writeErr(w, http.StatusForbidden, errors.New("只有空间管理员可以管理跨系统发布入口"))
		return false
	}
	if _, ok := s.mgr.workflows.get(r.PathValue("wid")); !ok {
		writeErr(w, http.StatusNotFound, ErrNotFound)
		return false
	}
	return true
}
func (s *Server) workflowPublicationSubjects() []map[string]string {
	out := []map[string]string{}
	if !s.multiuser() {
		return out
	}
	s.auth.mu.RLock()
	defer s.auth.mu.RUnlock()
	for name, u := range s.auth.users {
		if name != bootstrapUser && !u.Admin {
			out = append(out, map[string]string{"username": name, "display_name": name})
		}
	}
	slices.SortFunc(out, func(a, b map[string]string) int { return strings.Compare(a["username"], b["username"]) })
	return out
}
func (s *Server) validatePublication(p *workflowPublication) error {
	if err := validateWorkflowPublication(p); err != nil {
		return err
	}
	if !s.multiuser() {
		return errors.New("跨系统身份接入需要先启用本地账号")
	}
	for _, m := range p.Mappings {
		if _, ok := s.workflowEmbedLocalPrincipal(m.Username); !ok {
			return errors.New("身份映射只能使用现有普通本地账号，不能使用运维管理员")
		}
	}
	env, err := s.mgr.workflows.readVersion(p.WorkflowID, p.VersionID)
	if err != nil {
		return errors.New("入口必须绑定现有已发布版本")
	}
	def := workflow.Definition{ID: p.WorkflowID, Name: env.Workflow.Name, Description: env.Workflow.Description, Revision: env.Workflow.Revision, Nodes: env.Workflow.Nodes}
	for _, key := range def.InputKeys() {
		found := false
		for _, f := range p.InputFields {
			if f.Key == key && f.Required {
				found = true
				break
			}
		}
		if !found {
			return errors.New("流程使用的 inputs 字段必须在入口中声明为必填：" + key)
		}
	}
	return nil
}
func (s *Server) workflowEmbedLocalPrincipal(username string) (identity.Principal, bool) {
	if !s.multiuser() {
		return identity.Principal{}, false
	}
	s.auth.mu.RLock()
	u, ok := s.auth.users[username]
	s.auth.mu.RUnlock()
	if !ok || username == bootstrapUser || u.Admin {
		return identity.Principal{}, false
	}
	p := identity.LocalPrincipal(identity.LocalScope(username), username, false)
	role, err := s.workflowRole(p)
	if err != nil || role == "space_admin" {
		return identity.Principal{}, false
	}
	return p, true
}
func (s *Server) handleWorkflowPublicationList(w http.ResponseWriter, r *http.Request) {
	if !s.publicationManagement(w, r) {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"publications": s.workflowEmbed.publications.list(r.PathValue("wid")), "subjects": s.workflowPublicationSubjects()})
}
func (s *Server) handleWorkflowPublicationCreate(w http.ResponseWriter, r *http.Request) {
	if !s.publicationManagement(w, r) {
		return
	}
	var p workflowPublication
	if err := decodeBody(r, &p); err != nil {
		writeErr(w, 400, err)
		return
	}
	p.ID = "pub_" + rand.Text()
	p.WorkflowID = r.PathValue("wid")
	p.Revision = 0
	if p.HistoryScope == "" {
		p.HistoryScope = "business"
	}
	if p.VersionID == "" {
		if ptr, ok := s.mgr.workflows.pointer(p.WorkflowID); ok {
			p.VersionID = ptr.VersionID
		}
	}
	callbackSecret, err := s.prepareWorkflowPublicationCallback(&p, nil)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	if err := s.validatePublication(&p); err != nil {
		writeErr(w, 400, err)
		return
	}
	secret, hash := workflowClientSecret()
	p.SecretHash = hash
	if err := s.workflowEmbed.publications.save(p, 0); err != nil {
		writeErr(w, 500, err)
		return
	}
	p.Revision = 1
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 201, map[string]any{"publication": p, "client_secret": secret, "callback_secret": callbackSecret})
}
func (s *Server) publicationForManagement(w http.ResponseWriter, r *http.Request) (workflowPublication, bool) {
	if !s.publicationManagement(w, r) {
		return workflowPublication{}, false
	}
	p, ok := s.workflowEmbed.publications.get(r.PathValue("pid"))
	if !ok || p.WorkflowID != r.PathValue("wid") {
		writeErr(w, 404, ErrNotFound)
		return workflowPublication{}, false
	}
	return p, true
}
func (s *Server) handleWorkflowPublicationUpdate(w http.ResponseWriter, r *http.Request) {
	old, ok := s.publicationForManagement(w, r)
	if !ok {
		return
	}
	var p workflowPublication
	if err := decodeBody(r, &p); err != nil {
		writeErr(w, 400, err)
		return
	}
	p.ID = old.ID
	p.WorkflowID = old.WorkflowID
	p.SecretHash = old.SecretHash
	if p.HistoryScope == "" {
		p.HistoryScope = old.HistoryScope
	}
	// Older management clients omit callback fields. Preserve configured
	// subscriptions; disabling is an explicit callback.enabled=false update.
	if p.Callback == nil && old.Callback != nil {
		callback := *old.Callback
		p.Callback = &callback
	}
	if p.Revision != old.Revision {
		writeErr(w, 409, errors.New("发布入口已改变，请刷新后重试"))
		return
	}
	callbackSecret, err := s.prepareWorkflowPublicationCallback(&p, &old)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	if err := s.validatePublication(&p); err != nil {
		writeErr(w, 400, err)
		return
	}
	if err := s.workflowEmbed.publications.save(p, p.Revision); err != nil {
		writeErr(w, 409, err)
		return
	}
	p.Revision++
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, struct {
		workflowPublication
		CallbackSecret string `json:"callback_secret,omitempty"`
	}{p, callbackSecret})
}
func (s *Server) handleWorkflowPublicationRotate(w http.ResponseWriter, r *http.Request) {
	p, ok := s.publicationForManagement(w, r)
	if !ok {
		return
	}
	secret, hash := workflowClientSecret()
	p.SecretHash = hash
	if err := s.workflowEmbed.publications.save(p, p.Revision); err != nil {
		writeErr(w, 409, err)
		return
	}
	p.Revision++
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"publication": p, "client_secret": secret})
}

func publicWorkflowPublication(p workflowPublication) map[string]any {
	return map[string]any{"id": p.ID, "workflow_id": p.WorkflowID, "version_id": p.VersionID, "name": p.Name, "enabled": p.Enabled, "input_fields": p.InputFields, "history_scope": p.HistoryScope}
}
func (s *Server) workflowEmbedSessionView(p workflowPublication, c workflowEmbedCredential, r *http.Request) map[string]any {
	allowed, _ := s.workflowAllowed(r, p.WorkflowID, wfInvoke)
	return map[string]any{"publication": publicWorkflowPublication(p), "subject": c.Username, "parent_origin": c.ParentOrigin, "run_id": c.RunID, "business_id": c.BusinessID, "inputs": c.Inputs, "expires_at": c.ExpiresAt.UTC().Format(time.RFC3339), "can_invoke": allowed}
}
func (s *Server) workflowEmbedCredentialValid(p workflowPublication, c workflowEmbedCredential) (identity.Principal, bool) {
	if !p.Enabled || p.ID != c.PublicationID || p.Revision != c.Revision || !slices.Contains(p.AllowedOrigins, c.ParentOrigin) || !s.workflowEmbed.now().Before(c.ExpiresAt) {
		return identity.Principal{}, false
	}
	if p.HistoryScope == "business" && !validWorkflowBusinessID(c.BusinessID) {
		return identity.Principal{}, false
	}
	mapped := false
	for _, m := range p.Mappings {
		if m.Subject == c.Subject && m.Username == c.Username {
			mapped = true
			break
		}
	}
	if !mapped {
		return identity.Principal{}, false
	}
	principal, ok := s.workflowEmbedLocalPrincipal(c.Username)
	if !ok {
		return identity.Principal{}, false
	}
	role, err := s.workflowRole(principal)
	if err != nil || role == "disabled" {
		return identity.Principal{}, false
	}
	if _, ok := s.mgr.workflows.get(p.WorkflowID); !ok {
		return identity.Principal{}, false
	}
	return principal, true
}
func (s *Server) handleWorkflowEmbed(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, workflowEmbedPrefix), "/")
	if len(parts) < 2 || !validID(parts[0]) {
		http.NotFound(w, r)
		return
	}
	p, ok := s.workflowEmbed.publications.get(parts[0])
	if !ok {
		writeErr(w, 404, ErrNotFound)
		return
	}
	if len(parts) == 2 && parts[1] == "launch" && r.Method == http.MethodPost {
		s.handleWorkflowEmbedLaunch(w, r, p)
		return
	}
	if len(parts) == 2 && parts[1] == "session" && r.Method == http.MethodPost {
		s.handleWorkflowEmbedRedeem(w, r, p)
		return
	}
	token := workflowEmbedBearer(r)
	if !strings.HasPrefix(token, "wes_") {
		writeErr(w, 401, errors.New("嵌入会话无效，请从业务系统重新打开"))
		return
	}
	s.workflowEmbed.mu.Lock()
	s.workflowEmbed.pruneLocked()
	c, ok := s.workflowEmbed.sessions[workflowEmbedDigest(token)]
	s.workflowEmbed.mu.Unlock()
	principal, valid := s.workflowEmbedCredentialValid(p, c)
	if !ok || !valid {
		writeErr(w, 401, errors.New("嵌入会话已过期或入口授权已改变，请重新打开"))
		return
	}
	r = withWorkflowPrincipal(r, principal)
	r.SetPathValue("wid", p.WorkflowID)
	if len(parts) == 2 && parts[1] == "session" && r.Method == http.MethodGet {
		writeJSON(w, 200, s.workflowEmbedSessionView(p, c, r))
		return
	}
	if parts[1] != "runs" {
		http.NotFound(w, r)
		return
	}
	if len(parts) == 2 {
		if r.Method == http.MethodGet {
			s.handleWorkflowEmbedRunList(w, r, p, c)
			return
		}
		if r.Method == http.MethodPost {
			s.handleWorkflowEmbedStart(w, r, p, c)
			return
		}
	}
	if len(parts) >= 3 && validID(parts[2]) {
		r.SetPathValue("rid", parts[2])
		if !s.workflowEmbedRequireRunScope(w, r, p, c) {
			return
		}
		if len(parts) == 3 && r.Method == http.MethodGet {
			s.handleWorkflowRunGet(w, r)
			return
		}
		if len(parts) == 4 && r.Method == http.MethodPost {
			switch parts[3] {
			case "cancel":
				s.handleWorkflowRunCancel(w, r)
				return
			case "retry":
				if p.HistoryScope == "business" {
					key := r.Header.Get("Idempotency-Key")
					if len(key) > 128 {
						writeErr(w, 400, errors.New("运行请求标识过长"))
						return
					}
					if key != "" {
						r.Header.Set("Idempotency-Key", workflowEmbedBusinessRequestKey(p, c, "retry:"+parts[2], key))
					}
				}
				s.handleWorkflowRunRetry(w, r)
				return
			}
		}
		if len(parts) == 6 && parts[3] == "nodes" && validID(parts[4]) {
			r.SetPathValue("nid", parts[4])
			if parts[5] == "resume" && r.Method == http.MethodPost {
				s.handleWorkflowRunResume(w, r)
				return
			}
			if parts[5] == "artifact" && r.Method == http.MethodGet {
				s.handleWorkflowRunArtifact(w, r)
				return
			}
		}
	}
	http.NotFound(w, r)
}

func validWorkflowBusinessID(id string) bool {
	return id != "" && len(id) <= 256 && strings.TrimSpace(id) == id && strings.IndexFunc(id, unicode.IsControl) < 0
}

// The credential's business boundary comes from the trusted backend launch,
// never from a query parameter or a browser-supplied input field. The catalog
// contains the immutable attribution even when the public record is redacted.
func (s *Server) workflowEmbedRunInScope(p workflowPublication, c workflowEmbedCredential, rid string) (bool, error) {
	if p.HistoryScope != "business" {
		return true, nil
	}
	if !validWorkflowBusinessID(c.BusinessID) {
		return false, nil
	}
	db, err := s.workflowAccessStore(false)
	if err != nil || db == nil {
		return false, err
	}
	meta, err := db.GetWorkflowRun(rid)
	if errors.Is(err, taskqueue.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return meta.WorkflowID == p.WorkflowID && meta.PublicationID == p.ID && meta.BusinessID == c.BusinessID, nil
}

func (s *Server) workflowEmbedRequireRunScope(w http.ResponseWriter, r *http.Request, p workflowPublication, c workflowEmbedCredential) bool {
	allowed, err := s.workflowEmbedRunInScope(p, c, r.PathValue("rid"))
	if err != nil {
		workflowAccessError(w, err)
		return false
	}
	if !allowed {
		writeErr(w, http.StatusNotFound, ErrNotFound)
		return false
	}
	return true
}

func workflowEmbedBusinessRequestKey(p workflowPublication, c workflowEmbedCredential, operation, key string) string {
	// Hash the tuple instead of concatenating user strings: ids can contain
	// punctuation and each original key must remain within the admission cap.
	return "business:" + workflowEmbedDigest(p.ID+"\x00"+c.BusinessID+"\x00"+operation+"\x00"+key)
}

func (s *Server) handleWorkflowEmbedRunList(w http.ResponseWriter, r *http.Request, p workflowPublication, c workflowEmbedCredential) {
	if p.HistoryScope != "business" {
		s.handleWorkflowRunList(w, r)
		return
	}
	out := []workflow.RunRecord{}
	for _, rec := range s.mgr.wfruns.list(p.WorkflowID) {
		inScope, err := s.workflowEmbedRunInScope(p, c, rec.ID)
		if err != nil {
			workflowAccessError(w, err)
			return
		}
		if !inScope {
			continue
		}
		allowed, err := s.workflowRunAllowed(r, rec, wfRunRead, "")
		if err != nil {
			workflowAccessError(w, err)
			return
		}
		if allowed {
			out = append(out, s.projectWorkflowRun(r, rec))
		}
	}
	// Pagination follows both the business boundary and ordinary authorization.
	// Otherwise another business's records could displace this business's page.
	if raw := r.URL.Query().Get("limit"); raw != "" {
		limit, err := strconv.Atoi(raw)
		offset, offsetErr := strconv.Atoi("0" + r.URL.Query().Get("offset"))
		if err != nil || offsetErr != nil || limit < 1 || limit > 200 || offset < 0 {
			writeErr(w, http.StatusBadRequest, errors.New("运行列表分页参数无效"))
			return
		}
		if offset >= len(out) {
			out = []workflow.RunRecord{}
		} else {
			out = out[offset:]
			if len(out) > limit {
				out = out[:limit]
			}
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"runs": out})
}

func (s *Server) handleWorkflowEmbedLaunch(w http.ResponseWriter, r *http.Request, p workflowPublication) {
	token := workflowEmbedBearer(r)
	hash := workflowEmbedDigest(token)
	if !strings.HasPrefix(token, "wic_") || subtle.ConstantTimeCompare([]byte(hash), []byte(p.SecretHash)) != 1 {
		writeErr(w, 401, errors.New("接入凭据无效"))
		return
	}
	var body struct {
		Subject          string              `json:"subject"`
		Origin           string              `json:"origin"`
		RunID            string              `json:"run_id"`
		BusinessID       string              `json:"business_id"`
		Inputs           map[string]string   `json:"inputs"`
		ApproverSubjects map[string][]string `json:"approver_subjects"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeErr(w, 400, err)
		return
	}
	origin, err := normalizeWorkflowEmbedOrigin(body.Origin)
	if err != nil || !slices.Contains(p.AllowedOrigins, origin) {
		writeErr(w, 403, errors.New("未允许该嵌入来源"))
		return
	}
	if len(body.BusinessID) > 256 || strings.ContainsAny(body.BusinessID, "\r\n") {
		writeErr(w, 400, errors.New("业务标识无效"))
		return
	}
	c := workflowEmbedCredential{PublicationID: p.ID, Revision: p.Revision, Subject: body.Subject, ParentOrigin: origin, RunID: body.RunID, BusinessID: body.BusinessID, Inputs: body.Inputs, ExpiresAt: s.workflowEmbed.now().Add(time.Minute)}
	for _, m := range p.Mappings {
		if m.Subject == body.Subject {
			c.Username = m.Username
			break
		}
	}
	// Authentication errors retain the same answer regardless of which
	// business identifier the untrusted caller supplied.
	validation := p
	validation.HistoryScope = "workflow"
	principal, ok := s.workflowEmbedCredentialValid(validation, c)
	if !ok {
		writeErr(w, 403, errors.New("该外部用户尚未授权或发布入口已停用"))
		return
	}
	if p.HistoryScope == "business" && !validWorkflowBusinessID(body.BusinessID) {
		writeErr(w, 400, errors.New("按业务单隔离的入口必须提供有效的业务单标识"))
		return
	}
	r = withWorkflowPrincipal(r, principal)
	r.SetPathValue("wid", p.WorkflowID)
	// Prefill can omit required fields, but cannot introduce undeclared data.
	partial := p
	partial.InputFields = slices.Clone(p.InputFields)
	for i := range partial.InputFields {
		partial.InputFields[i].Required = false
	}
	inputs, err := validateWorkflowPublicationInputs(partial, body.Inputs)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	c.Inputs = inputs
	c.BusinessApprovers, err = s.workflowEmbedBusinessApprovers(p, body.ApproverSubjects)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if body.RunID != "" {
		if !validID(body.RunID) {
			writeErr(w, 400, errors.New("运行标识无效"))
			return
		}
		r.SetPathValue("rid", body.RunID)
		if !s.workflowEmbedRequireRunScope(w, r, p, c) {
			return
		}
		if _, ok := s.workflowRunFor(w, r, wfRunRead); !ok {
			return
		}
	} else {
		allowed, err := s.workflowAllowed(r, p.WorkflowID, wfRead)
		if err != nil || !allowed {
			writeErr(w, 403, errors.New("没有访问此工作流的权限"))
			return
		}
	}
	code, err := s.workflowEmbed.issue(c, false)
	if err != nil {
		writeErr(w, 429, err)
		return
	}
	writeJSON(w, 201, map[string]any{"launch_code": code, "expires_at": c.ExpiresAt.UTC().Format(time.RFC3339), "embed_path": workflowEmbedPagePrefix + p.ID + "?parent_origin=" + url.QueryEscape(origin)})
}
func (s *Server) handleWorkflowEmbedRedeem(w http.ResponseWriter, r *http.Request, p workflowPublication) {
	var body struct {
		Code         string `json:"code"`
		ParentOrigin string `json:"parent_origin"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeErr(w, 400, err)
		return
	}
	key := workflowEmbedDigest(body.Code)
	s.workflowEmbed.mu.Lock()
	s.workflowEmbed.pruneLocked()
	c, ok := s.workflowEmbed.codes[key]
	if ok && c.PublicationID == p.ID && c.ParentOrigin == body.ParentOrigin {
		delete(s.workflowEmbed.codes, key)
	} else {
		ok = false
	}
	s.workflowEmbed.mu.Unlock()
	principal, valid := s.workflowEmbedCredentialValid(p, c)
	if !ok || !valid {
		writeErr(w, 401, errors.New("启动码无效、已使用或过期，请重新打开"))
		return
	}
	c.ExpiresAt = s.workflowEmbed.now().Add(15 * time.Minute)
	token, err := s.workflowEmbed.issue(c, true)
	if err != nil {
		writeErr(w, 429, err)
		return
	}
	r = withWorkflowPrincipal(r, principal)
	view := s.workflowEmbedSessionView(p, c, r)
	view["session_token"] = token
	writeJSON(w, 201, view)
}
func (s *Server) handleWorkflowEmbedStart(w http.ResponseWriter, r *http.Request, p workflowPublication, c workflowEmbedCredential) {
	allowed, err := s.workflowAllowed(r, p.WorkflowID, wfInvoke)
	if err != nil || !allowed {
		writeErr(w, 403, errors.New("没有运行此工作流的权限"))
		return
	}
	var body struct {
		Inputs     map[string]string `json:"inputs"`
		RequestKey string            `json:"request_key"`
	}
	if err := decodeBody(r, &body); err != nil {
		writeErr(w, 400, err)
		return
	}
	inputs, err := validateWorkflowPublicationInputs(p, body.Inputs)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	if len(body.RequestKey) > 128 {
		writeErr(w, 400, errors.New("运行请求标识过长"))
		return
	}
	key := body.RequestKey
	if key == "" {
		key = r.Header.Get("Idempotency-Key")
	}
	if len(key) > 128 {
		writeErr(w, 400, errors.New("运行请求标识过长"))
		return
	}
	if key != "" {
		if p.HistoryScope == "business" {
			key = workflowEmbedBusinessRequestKey(p, c, "start", key)
		} else {
			key = "publication:" + p.ID + ":" + key
		}
	}
	env, err := s.mgr.workflows.readVersion(p.WorkflowID, p.VersionID)
	if err != nil {
		writeErr(w, 409, errors.New("入口绑定的发布版本不可读"))
		return
	}
	def := workflow.Definition{ID: p.WorkflowID, Name: env.Workflow.Name, Description: env.Workflow.Description, Revision: env.Workflow.Revision, Nodes: env.Workflow.Nodes}
	principal := s.workflowPrincipal(r)
	meta, err := s.mgr.admitWorkflowWithOptions(def, principal.Scope, "publication", p.ID, key, "", workflowAdmissionOptions{InitiatorID: principal.ID, VersionID: p.VersionID, Inputs: inputs, PublicationID: p.ID, IntegrationID: p.ID, ExternalSubject: c.Subject, BusinessID: c.BusinessID, Callback: workflowPublicationCallbackTarget(p), BusinessApprovers: c.BusinessApprovers})
	if err != nil {
		status := 400
		if errors.Is(err, taskqueue.ErrConflict) {
			status = 409
		}
		writeErr(w, status, err)
		return
	}
	writeJSON(w, 201, map[string]any{"id": meta.ID})
}

type workflowEmbedPageWriter struct {
	http.ResponseWriter
	policy string
	wrote  bool
}

func (w *workflowEmbedPageWriter) WriteHeader(status int) {
	if w.wrote {
		return
	}
	w.wrote = true
	w.Header().Set("Content-Security-Policy", w.policy)
	w.Header().Del("X-Frame-Options")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.ResponseWriter.WriteHeader(status)
}
func (w *workflowEmbedPageWriter) Write(b []byte) (int, error) {
	w.WriteHeader(http.StatusOK)
	return w.ResponseWriter.Write(b)
}
func (w *workflowEmbedPageWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (s *Server) handleWorkflowEmbedPage(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimPrefix(r.URL.Path, workflowEmbedPagePrefix)
	p, ok := s.workflowEmbed.publications.get(id)
	policy := "frame-ancestors 'none'"
	if ok && p.Enabled {
		policy = "frame-ancestors " + strings.Join(p.AllowedOrigins, " ")
	}
	writer := &workflowEmbedPageWriter{ResponseWriter: w, policy: policy}
	if r.Method != http.MethodGet || !ok || !p.Enabled {
		writer.WriteHeader(404)
		return
	}
	s.handleUI(writer, r)
}
