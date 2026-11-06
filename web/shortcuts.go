package web

// Saved user references are private runtime data, separate from published
// opening questions, Agent definitions and the model's filesystem workspace.
import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/inputref"
	"github.com/yosukeno/pogo/skills"
)

const shortcutsMaxBody = 2 << 20
const shortcutsMaxCount = 256
const shortcutsMaxFolders = 256
const shortcutsMaxFolderDepth = 16
const shortcutsMaxRevision int64 = 1<<53 - 1

var shortcutsMu sync.Mutex
var errShortcutsConflict = errors.New("资料已在其他窗口更新，请核对后重新保存")
var errShortcutTreeInvalid = errors.New("私有快捷方式目录结构无效")
var shortcutFolderID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,160}$`)

type userShortcut struct {
	ID          string             `json:"id"`
	Title       string             `json:"title"`
	Content     string             `json:"content"`
	Icon        string             `json:"icon"`
	Language    string             `json:"language,omitempty"`
	Parts       []MessageInputPart `json:"parts,omitempty"`
	InputLayout string             `json:"input_layout,omitempty"`
	ParentID    string             `json:"parent_id,omitempty"`
}

type shortcutFolder struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	ParentID string `json:"parent_id,omitempty"`
}

type shortcutView struct {
	ScopeID           string               `json:"scope_id"`
	AgentID           string               `json:"agent_id"`
	Revision          int64                `json:"revision"`
	Shortcuts         []userShortcut       `json:"shortcuts"`
	Folders           []shortcutFolder     `json:"folders"`
	StarterShortcuts  []userShortcut       `json:"starter_shortcuts,omitempty"`
	StarterActions    []skills.StarterCard `json:"starter_actions,omitempty"`
	CanManageStarters bool                 `json:"can_manage_starters,omitempty"`
}

type shortcutDocument struct {
	Version int `json:"version"`
	shortcutView
	// Tool receipts share the same atomic document as their one saved root.
	// HTTP edits preserve them, so a timed-out Agent retry cannot create a copy.
	ToolCreates map[string]shortcutCreateReceipt `json:"tool_creates,omitempty"`
}

func shortcutScopeID(owner *identity.Scope, agentID string) (string, error) {
	if owner != nil && !owner.Valid() || agentID != "" && !validAgentDirName(agentID) {
		return "", errors.New("资料保存范围不可用")
	}
	raw, err := json.Marshal(struct {
		Version string          `json:"version"`
		Owner   *identity.Scope `json:"owner"`
		AgentID string          `json:"agent_id"`
	}{"pogo-user-shortcuts-v1", owner, agentID})
	if err != nil {
		return "", errors.New("资料保存范围不可用")
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

func shortcutPath(dir string, owner *identity.Scope, agentID string) (string, error) {
	if dir == "" {
		return "", errors.New("资料存储不可用")
	}
	scope, err := shortcutScopeID(owner, agentID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "user-shortcuts", scope+".json"), nil
}

func validateUserShortcuts(entries []userShortcut) ([]userShortcut, error) {
	if entries == nil || len(entries) > shortcutsMaxCount {
		return nil, errors.New("资料列表需要为数组，最多保存 256 项")
	}
	result := make([]userShortcut, len(entries))
	seen := map[string]bool{}
	for index, entry := range entries {
		entry.Title = strings.TrimSpace(entry.Title)
		if seen[entry.ID] || entry.ParentID != "" && !shortcutFolderID.MatchString(entry.ParentID) || !skills.ValidInputShortcut(MessageInputPart{Type: "shortcut", ShortcutID: entry.ID, Title: entry.Title, Text: entry.Content, Icon: entry.Icon, Language: entry.Language, Parts: entry.Parts}) {
			return nil, errors.New("资料的标识、标题、正文或图标无效；正文最多 64 KiB")
		}
		if msg := skills.ValidateInputParts([]MessageInputPart{{Type: "shortcut", ShortcutID: entry.ID, Title: entry.Title, Text: entry.Content, Icon: entry.Icon, Language: entry.Language, Parts: entry.Parts}}, entry.InputLayout, "shortcut"); msg != "" {
			return nil, errors.New(msg)
		}
		entry.Parts = inputref.Clone(entry.Parts)
		seen[entry.ID] = true
		result[index] = entry
	}
	return result, nil
}

// The two system directories are virtual read-only sources. Only real private
// folders can be parents; their safe IDs cannot contain the reserved '@'. A
// whole-tree replacement never silently cascades deletions or drops children.
func validateShortcutFolders(folders []shortcutFolder, entries []userShortcut) ([]shortcutFolder, error) {
	if len(folders) > shortcutsMaxFolders {
		return nil, errShortcutTreeInvalid
	}
	result := make([]shortcutFolder, len(folders))
	parents := make(map[string]string, len(folders))
	ids := make(map[string]bool, len(folders)+len(entries))
	for _, entry := range entries {
		ids[entry.ID] = true
	}
	for i, folder := range folders {
		folder.Name = strings.TrimSpace(folder.Name)
		if !shortcutFolderID.MatchString(folder.ID) || ids[folder.ID] || folder.ParentID != "" && !shortcutFolderID.MatchString(folder.ParentID) || !utf8.ValidString(folder.Name) || folder.Name == "" || utf8.RuneCountInString(folder.Name) > 120 {
			return nil, errShortcutTreeInvalid
		}
		for _, r := range folder.Name {
			if unicode.IsControl(r) {
				return nil, errShortcutTreeInvalid
			}
		}
		ids[folder.ID] = true
		parents[folder.ID] = folder.ParentID
		result[i] = folder
	}
	for _, folder := range result {
		if folder.ParentID != "" {
			if _, ok := parents[folder.ParentID]; !ok {
				return nil, errShortcutTreeInvalid
			}
		}
	}
	for _, entry := range entries {
		if entry.ParentID != "" {
			if _, ok := parents[entry.ParentID]; !ok {
				return nil, errShortcutTreeInvalid
			}
		}
	}
	depths, visiting := map[string]int{}, map[string]bool{}
	var depth func(string) (int, error)
	depth = func(id string) (int, error) {
		if id == "" {
			return 0, nil
		}
		if visiting[id] {
			return 0, errShortcutTreeInvalid
		}
		if d := depths[id]; d != 0 {
			return d, nil
		}
		visiting[id] = true
		d, err := depth(parents[id])
		if err != nil || d >= shortcutsMaxFolderDepth {
			return 0, errShortcutTreeInvalid
		}
		visiting[id] = false
		depths[id] = d + 1
		return d + 1, nil
	}
	for id := range parents {
		if _, err := depth(id); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func readShortcutDocument(path, scopeID, agentID string) (shortcutDocument, error) {
	view := shortcutView{ScopeID: scopeID, AgentID: agentID, Shortcuts: []userShortcut{}, Folders: []shortcutFolder{}}
	st, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return shortcutDocument{Version: 1, shortcutView: view}, nil
	}
	if err != nil || !st.Mode().IsRegular() || st.Size() > shortcutsMaxBody {
		return shortcutDocument{}, errors.New("无法读取已保存资料；原文件已保留")
	}
	f, err := os.Open(path)
	if err != nil {
		return shortcutDocument{}, errors.New("无法读取已保存资料；原文件已保留")
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, shortcutsMaxBody+1))
	if err != nil || len(raw) > shortcutsMaxBody {
		return shortcutDocument{}, errors.New("已保存资料超过读取上限或无法读取；原文件已保留")
	}
	var doc shortcutDocument
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var extra any
	if dec.Decode(&doc) != nil || dec.Decode(&extra) != io.EOF || doc.Version != 1 || doc.ScopeID != scopeID || doc.AgentID != agentID || doc.Revision < 1 || doc.Revision > shortcutsMaxRevision {
		return shortcutDocument{}, errors.New("已保存资料格式无效；原文件已保留")
	}
	entries, err := validateUserShortcuts(doc.Shortcuts)
	if err != nil {
		return shortcutDocument{}, errors.New("已保存资料格式无效；原文件已保留")
	}
	doc.Shortcuts = entries
	doc.Folders, err = validateShortcutFolders(doc.Folders, entries)
	if err != nil {
		return shortcutDocument{}, errors.New("已保存资料目录格式无效；原文件已保留")
	}
	if !validShortcutCreateReceipts(doc.ToolCreates, doc.Revision) {
		return shortcutDocument{}, errors.New("已保存资料回执格式无效；原文件已保留")
	}
	return doc, nil
}

func readShortcutFile(path, scopeID, agentID string) (shortcutView, error) {
	doc, err := readShortcutDocument(path, scopeID, agentID)
	return doc.shortcutView, err
}

func (m *Manager) userShortcuts(owner *identity.Scope, agentID string) (shortcutView, error) {
	path, err := shortcutPath(m.cfg.SessionDir, owner, agentID)
	if err != nil {
		return shortcutView{}, err
	}
	scope, _ := shortcutScopeID(owner, agentID)
	shortcutsMu.Lock()
	defer shortcutsMu.Unlock()
	return readShortcutFile(path, scope, agentID)
}

func (m *Manager) replaceUserShortcuts(owner *identity.Scope, agentID string, revision int64, entries []userShortcut, folderUpdates ...[]shortcutFolder) (shortcutView, error) {
	path, err := shortcutPath(m.cfg.SessionDir, owner, agentID)
	if err != nil {
		return shortcutView{}, err
	}
	if revision < 0 || revision >= shortcutsMaxRevision {
		return shortcutView{}, errors.New("资料版本超出支持范围")
	}
	if len(folderUpdates) > 1 || len(folderUpdates) == 1 && folderUpdates[0] == nil {
		return shortcutView{}, errShortcutTreeInvalid
	}
	entries, err = validateUserShortcuts(entries)
	if err != nil {
		return shortcutView{}, err
	}
	scope, _ := shortcutScopeID(owner, agentID)
	if err := m.validateShortcutAssets(owner, agentID, entries); err != nil {
		return shortcutView{}, err
	}
	shortcutsMu.Lock()
	defer shortcutsMu.Unlock()
	doc, err := readShortcutDocument(path, scope, agentID)
	if err != nil {
		return shortcutView{}, err
	}
	current := doc.shortcutView
	if current.Revision != revision {
		return current, errShortcutsConflict
	}
	folders := current.Folders
	if len(folderUpdates) == 1 {
		folders = folderUpdates[0]
	}
	folders, err = validateShortcutFolders(folders, entries)
	if err != nil {
		return shortcutView{}, err
	}
	view := shortcutView{ScopeID: scope, AgentID: agentID, Revision: revision + 1, Shortcuts: entries, Folders: folders}
	var encoded bytes.Buffer
	enc := json.NewEncoder(&encoded)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(shortcutDocument{Version: 1, shortcutView: view, ToolCreates: doc.ToolCreates}); err != nil || encoded.Len() > shortcutsMaxBody {
		return shortcutView{}, errors.New("资料列表超过存储上限")
	}
	if err := writePrivateFileAtomic(path, encoded.Bytes()); err != nil {
		return shortcutView{}, errors.New("资料保存失败；原版本已保留")
	}
	return view, nil
}

func (s *Server) shortcutRoutes() {
	s.mux.HandleFunc("GET /api/shortcuts", s.handleGetShortcuts)
	s.mux.HandleFunc("PUT /api/shortcuts", s.handlePutShortcuts)
	s.mux.HandleFunc("POST /api/sessions/{sid}/shortcuts/assets", s.handleShortcutAssetsSave)
	s.mux.HandleFunc("POST /api/sessions/{sid}/shortcuts/resolve", s.handleShortcutAssetsResolve)
	s.mux.HandleFunc("POST /api/sessions/{sid}/shortcuts/starters/{starter}/resolve", s.handleStarterShortcutResolve)
}

// Agent definitions are shared Studio apps. Visibility is the existing Studio
// policy; private entries additionally include every current owner dimension.
func (s *Server) shortcutOwner(w http.ResponseWriter, r *http.Request) (*identity.Scope, string, bool) {
	var owner *identity.Scope
	if scope, ok := s.ownerScope(r); ok {
		owner = &scope
	} else if s.multiuser() {
		writeErr(w, http.StatusUnauthorized, errors.New("请先登录"))
		return nil, "", false
	}
	values := r.URL.Query()["agent_id"]
	if len(values) > 1 {
		writeErr(w, http.StatusBadRequest, errors.New("只能指定一个 Agent"))
		return nil, "", false
	}
	agentID := r.URL.Query().Get("agent_id")
	if agentID != "" {
		a, ok := s.mgr.studio.get(agentID)
		if !validAgentDirName(agentID) || !ok || a.Status != "ready" && !s.adminRequest(r) {
			writeErr(w, http.StatusNotFound, errors.New("Agent 不存在或不可访问"))
			return nil, "", false
		}
	}
	return owner, agentID, true
}

func (s *Server) handleGetShortcuts(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	owner, agentID, ok := s.shortcutOwner(w, r)
	if !ok {
		return
	}
	view, err := s.mgr.userShortcuts(owner, agentID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.projectStarterShortcuts(&view, r)
	writeJSON(w, http.StatusOK, view)
}

func (s *Server) handlePutShortcuts(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	owner, agentID, ok := s.shortcutOwner(w, r)
	if !ok {
		return
	}
	var input struct {
		ScopeID   string          `json:"scope_id"`
		Revision  *int64          `json:"revision"`
		Shortcuts []userShortcut  `json:"shortcuts"`
		Folders   json.RawMessage `json:"folders"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, shortcutsMaxBody))
	dec.DisallowUnknownFields()
	var extra any
	if dec.Decode(&input) != nil || dec.Decode(&extra) != io.EOF || input.ScopeID == "" || input.Revision == nil {
		writeErr(w, http.StatusBadRequest, errors.New("请求需要 scope_id、revision、shortcuts，以及可选的 folders"))
		return
	}
	scope, err := shortcutScopeID(owner, agentID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if input.ScopeID != scope {
		view, err := s.mgr.userShortcuts(owner, agentID)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		s.projectStarterShortcuts(&view, r)
		writeJSON(w, http.StatusConflict, map[string]any{"code": "SHORTCUT_SCOPE_CHANGED", "error": "资料保存范围已改变，请核对后重新保存", "current": view})
		return
	}
	if *input.Revision < 0 || *input.Revision >= shortcutsMaxRevision {
		writeErr(w, http.StatusBadRequest, errors.New("资料版本超出支持范围"))
		return
	}
	if _, err := validateUserShortcuts(input.Shortcuts); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	var folderUpdates [][]shortcutFolder
	if input.Folders != nil {
		var folders []shortcutFolder
		folderDecoder := json.NewDecoder(bytes.NewReader(input.Folders))
		folderDecoder.DisallowUnknownFields()
		if folderDecoder.Decode(&folders) != nil || folders == nil {
			writeErr(w, http.StatusBadRequest, errShortcutTreeInvalid)
			return
		}
		folderUpdates = append(folderUpdates, folders)
	}
	view, err := s.mgr.replaceUserShortcuts(owner, agentID, *input.Revision, input.Shortcuts, folderUpdates...)
	if errors.Is(err, errShortcutAssetInvalid) {
		writeErr(w, 400, err)
		return
	}
	if errors.Is(err, errShortcutsConflict) {
		s.projectStarterShortcuts(&view, r)
		writeJSON(w, http.StatusConflict, map[string]any{"code": "SHORTCUT_REVISION_CONFLICT", "error": err.Error(), "current": view})
		return
	}
	if errors.Is(err, errShortcutTreeInvalid) {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	s.projectStarterShortcuts(&view, r)
	writeJSON(w, http.StatusOK, view)
}
