package web

// UI icons are shared presentation assets. They contain one bounded path, not
// uploaded SVG documents, URLs, executable code or provider prompts. An icon's
// drawing is immutable; archiving hides it from new selections without breaking
// historical messages and public answer snapshots that already reference it.
import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/yosukeno/pogo/skills"
)

const (
	uiIconsMaxBody           = 2 << 20
	uiIconsMaxCount          = 256
	uiIconsMaxPath           = 4096
	uiIconsMaxRevision int64 = 1<<53 - 1
)

var uiIconsMu sync.Mutex
var errUIIconsConflict = errors.New("图标库已在其他窗口更新，请核对后重新保存")

type uiIcon struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Path      string   `json:"path"`
	Modules   []string `json:"modules"`
	Archived  bool     `json:"archived,omitempty"`
	CreatedAt string   `json:"created_at,omitempty"`
}

type uiIconHidden struct {
	Shortcuts []string `json:"shortcuts"`
	Starters  []string `json:"starters"`
	Agents    []string `json:"agents"`
}

type uiIconLibrary struct {
	Revision      int64        `json:"revision"`
	Icons         []uiIcon     `json:"icons"`
	HiddenBuiltin uiIconHidden `json:"hidden_builtin"`
}

type uiIconDocument struct {
	Version int `json:"version"`
	uiIconLibrary
}

func emptyUIIconLibrary() uiIconLibrary {
	return uiIconLibrary{Icons: []uiIcon{}, HiddenBuiltin: uiIconHidden{Shortcuts: []string{}, Starters: []string{}, Agents: []string{}}}
}

func uiIconID(path string) string {
	digest := sha256.Sum256([]byte(path))
	return "ui_" + hex.EncodeToString(digest[:16])
}

// validUIIconPath parses the narrow SVG path grammar rather than trusting a
// character allowlist. Every command must have complete argument groups. The
// finite coordinate and segment budgets also bound rasterization work.
func validUIIconPath(path string) bool {
	if path == "" || len(path) > uiIconsMaxPath || strings.TrimSpace(path) != path {
		return false
	}
	commandArgs := map[byte]int{'M': 2, 'L': 2, 'H': 1, 'V': 1, 'C': 6, 'S': 4, 'Q': 4, 'T': 2, 'A': 7, 'Z': 0}
	i, commands, segments := 0, 0, 0
	first := true
	for i < len(path) {
		for i < len(path) && (path[i] == ' ' || path[i] == '\t' || path[i] == '\r' || path[i] == '\n') {
			i++
		}
		if i == len(path) {
			break
		}
		command := path[i]
		upper := command
		if upper >= 'a' && upper <= 'z' {
			upper -= 'a' - 'A'
		}
		arity, ok := commandArgs[upper]
		if !ok || first && upper != 'M' {
			return false
		}
		first = false
		i++
		commands++
		if commands > 256 {
			return false
		}
		if arity == 0 {
			segments++
			continue
		}
		values := []float64{}
		needNumber := false
		for i < len(path) {
			for i < len(path) && (path[i] == ' ' || path[i] == '\t' || path[i] == '\r' || path[i] == '\n') {
				i++
			}
			if i == len(path) {
				break
			}
			if path[i] == ',' {
				if len(values) == 0 || needNumber {
					return false
				}
				needNumber = true
				i++
				continue
			}
			b := path[i]
			if b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' {
				break
			}
			start := i
			if path[i] == '+' || path[i] == '-' {
				i++
			}
			digits := 0
			for i < len(path) && path[i] >= '0' && path[i] <= '9' {
				i++
				digits++
			}
			if i < len(path) && path[i] == '.' {
				i++
				for i < len(path) && path[i] >= '0' && path[i] <= '9' {
					i++
					digits++
				}
			}
			if digits == 0 {
				return false
			}
			n, err := strconv.ParseFloat(path[start:i], 64)
			if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || math.Abs(n) > 512 {
				return false
			}
			values = append(values, n)
			if len(values) > 1536 {
				return false
			}
			needNumber = false
		}
		if needNumber || len(values) == 0 || len(values)%arity != 0 {
			return false
		}
		segments += len(values) / arity
		if segments > 256 {
			return false
		}
		if upper == 'A' {
			for j := 0; j < len(values); j += 7 {
				if values[j] < 0 || values[j+1] < 0 || math.Abs(values[j+2]) > 360 || (values[j+3] != 0 && values[j+3] != 1) || (values[j+4] != 0 && values[j+4] != 1) {
					return false
				}
			}
		}
	}
	return !first && segments > 0 && segments <= 256
}

func validUIIconName(name string) bool {
	if !utf8.ValidString(name) || strings.TrimSpace(name) == "" || utf8.RuneCountInString(name) > 80 {
		return false
	}
	for _, c := range name {
		if unicode.IsControl(c) {
			return false
		}
	}
	return true
}

func validUIIconModules(modules []string) bool {
	if modules == nil || len(modules) > 3 {
		return false
	}
	seen := map[string]bool{}
	for _, module := range modules {
		if seen[module] || module != "shortcuts" && module != "starters" && module != "agents" {
			return false
		}
		seen[module] = true
	}
	return true
}

func validateUIIconLibrary(in uiIconLibrary) (uiIconLibrary, error) {
	if in.Revision < 0 || in.Revision > uiIconsMaxRevision || in.Icons == nil || len(in.Icons) > uiIconsMaxCount {
		return uiIconLibrary{}, errors.New("图标库需要有效版本和数组，最多保存 256 个图标")
	}
	// Validation and normalization own their slices. Two optimistic saves may
	// start from the same read snapshot without mutating one another's draft.
	in.Icons = append([]uiIcon{}, in.Icons...)
	for index := range in.Icons {
		in.Icons[index].Modules = slices.Clone(in.Icons[index].Modules)
	}
	in.HiddenBuiltin.Shortcuts = slices.Clone(in.HiddenBuiltin.Shortcuts)
	in.HiddenBuiltin.Starters = slices.Clone(in.HiddenBuiltin.Starters)
	in.HiddenBuiltin.Agents = slices.Clone(in.HiddenBuiltin.Agents)
	seen := map[string]bool{}
	for index := range in.Icons {
		icon := &in.Icons[index]
		icon.Name, icon.Path = strings.TrimSpace(icon.Name), strings.TrimSpace(icon.Path)
		if !validUIIconName(icon.Name) || !validUIIconPath(icon.Path) || !skills.CustomIconIDOK(icon.ID) || icon.ID != uiIconID(icon.Path) || seen[icon.ID] || !validUIIconModules(icon.Modules) {
			return uiIconLibrary{}, errors.New("图标名称、路径、标识或模块不符合要求；仅支持安全的 16×16 线条路径")
		}
		if icon.CreatedAt != "" {
			if _, err := time.Parse(time.RFC3339, icon.CreatedAt); err != nil {
				return uiIconLibrary{}, errors.New("图标创建时间无效")
			}
		}
		seen[icon.ID] = true
	}
	for module, names := range map[string][]string{"shortcuts": in.HiddenBuiltin.Shortcuts, "starters": in.HiddenBuiltin.Starters, "agents": in.HiddenBuiltin.Agents} {
		if names == nil || len(names) > 53 {
			return uiIconLibrary{}, errors.New("内置图标配置需要数组")
		}
		seen := map[string]bool{}
		for _, name := range names {
			valid := skills.StarterIconOK(name) && !skills.CustomIconIDOK(name)
			if module == "agents" && (name == "pen" || name == "compass" || name == "chart") {
				valid = true
			}
			if !valid || seen[name] {
				return uiIconLibrary{}, errors.New("内置图标配置包含无效名称")
			}
			seen[name] = true
		}
	}
	return in, nil
}

func (m *Manager) uiIconPath() (string, error) {
	if m.cfg.SessionDir == "" {
		return "", errors.New("图标存储不可用")
	}
	return filepath.Join(m.cfg.SessionDir, "ui-icons.json"), nil
}

func readUIIconLibrary(path string) (uiIconLibrary, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return emptyUIIconLibrary(), nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > uiIconsMaxBody {
		return uiIconLibrary{}, errors.New("无法读取图标库；原文件已保留")
	}
	f, err := os.Open(path)
	if err != nil {
		return uiIconLibrary{}, errors.New("无法读取图标库；原文件已保留")
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, uiIconsMaxBody+1))
	if err != nil || len(data) > uiIconsMaxBody {
		return uiIconLibrary{}, errors.New("无法读取图标库；原文件已保留")
	}
	var doc uiIconDocument
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var extra any
	if decoder.Decode(&doc) != nil || decoder.Decode(&extra) != io.EOF || doc.Version != 1 || doc.Revision < 1 {
		return uiIconLibrary{}, errors.New("图标库格式无效；原文件已保留")
	}
	view, err := validateUIIconLibrary(doc.uiIconLibrary)
	if err != nil {
		return uiIconLibrary{}, errors.New("图标库格式无效；原文件已保留")
	}
	return view, nil
}

func (m *Manager) uiIcons() (uiIconLibrary, error) {
	path, err := m.uiIconPath()
	if err != nil {
		return uiIconLibrary{}, err
	}
	uiIconsMu.Lock()
	defer uiIconsMu.Unlock()
	return readUIIconLibrary(path)
}

func (m *Manager) replaceUIIcons(in uiIconLibrary) (uiIconLibrary, error) {
	path, err := m.uiIconPath()
	if err != nil {
		return uiIconLibrary{}, err
	}
	in, err = validateUIIconLibrary(in)
	if err != nil {
		return uiIconLibrary{}, err
	}
	if in.Revision >= uiIconsMaxRevision {
		return uiIconLibrary{}, errors.New("图标版本超出支持范围")
	}
	uiIconsMu.Lock()
	defer uiIconsMu.Unlock()
	current, err := readUIIconLibrary(path)
	if err != nil {
		return uiIconLibrary{}, err
	}
	if current.Revision != in.Revision {
		return current, errUIIconsConflict
	}
	existing := map[string]uiIcon{}
	incoming := map[string]bool{}
	for _, icon := range current.Icons {
		existing[icon.ID] = icon
	}
	for index := range in.Icons {
		icon := &in.Icons[index]
		incoming[icon.ID] = true
		if old, ok := existing[icon.ID]; ok {
			if old.Path != icon.Path || icon.CreatedAt != "" && icon.CreatedAt != old.CreatedAt {
				return uiIconLibrary{}, errors.New("已保存图标的画法与创建时间不可更改，请另存新图标")
			}
			icon.CreatedAt = old.CreatedAt
		} else {
			icon.CreatedAt = time.Now().UTC().Format(time.RFC3339)
		}
	}
	for id := range existing {
		if !incoming[id] {
			return uiIconLibrary{}, errors.New("请归档图标而不是删除，保证历史引用仍可显示")
		}
	}
	in.Revision++
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if encoder.Encode(uiIconDocument{Version: 1, uiIconLibrary: in}) != nil || buffer.Len() > uiIconsMaxBody {
		return uiIconLibrary{}, errors.New("图标库超过存储上限")
	}
	if writeWorkflowFileAtomic(path, buffer.Bytes()) != nil {
		return uiIconLibrary{}, errors.New("图标保存失败；原版本已保留")
	}
	return in, nil
}

func (s *Server) uiIconRoutes() {
	s.mux.HandleFunc("GET /api/ui-icons", s.handleGetUIIcons)
	s.mux.HandleFunc("PUT /api/ui-icons", s.admin(s.handlePutUIIcons))
	s.mux.HandleFunc("POST /api/ui-icons/generate", s.admin(s.handleGenerateUIIcons))
	// Outside /api on purpose: public shared answers must resolve only their
	// already-known immutable drawing IDs, with no access to the icon catalog.
	s.mux.HandleFunc("GET /icon-assets/{asset}", s.handleUIIconAsset)
	s.mux.HandleFunc("GET /icon-assets/", http.NotFound)
}

func (s *Server) uiIconView(r *http.Request, view uiIconLibrary) map[string]any {
	return map[string]any{"revision": view.Revision, "icons": view.Icons, "hidden_builtin": view.HiddenBuiltin, "can_manage": s.adminRequest(r)}
}

func (s *Server) handleGetUIIcons(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if _, ok := identityFrom(r); s.multiuser() && !ok {
		writeErr(w, http.StatusUnauthorized, errors.New("请先登录"))
		return
	}
	view, err := s.mgr.uiIcons()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, s.uiIconView(r, view))
}

func (s *Server) handlePutUIIcons(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	var input struct {
		Revision      *int64       `json:"revision"`
		Icons         []uiIcon     `json:"icons"`
		HiddenBuiltin uiIconHidden `json:"hidden_builtin"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, uiIconsMaxBody))
	decoder.DisallowUnknownFields()
	var extra any
	if decoder.Decode(&input) != nil || decoder.Decode(&extra) != io.EOF || input.Revision == nil {
		writeErr(w, http.StatusBadRequest, errors.New("请求需要且只能包含 revision、icons 和 hidden_builtin"))
		return
	}
	view := uiIconLibrary{Revision: *input.Revision, Icons: input.Icons, HiddenBuiltin: input.HiddenBuiltin}
	if _, err := validateUIIconLibrary(view); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	saved, err := s.mgr.replaceUIIcons(view)
	if errors.Is(err, errUIIconsConflict) {
		writeJSON(w, http.StatusConflict, map[string]any{"code": "UI_ICONS_REVISION_CONFLICT", "error": err.Error(), "current": s.uiIconView(r, saved)})
		return
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, s.uiIconView(r, saved))
}

func (s *Server) handleUIIconAsset(w http.ResponseWriter, r *http.Request) {
	asset := r.PathValue("asset")
	id := strings.TrimSuffix(asset, ".json")
	if asset != id+".json" || !skills.CustomIconIDOK(id) || r.URL.RawQuery != "" {
		http.NotFound(w, r)
		return
	}
	view, err := s.mgr.uiIcons()
	if err != nil {
		http.NotFound(w, r)
		return
	}
	for _, icon := range view.Icons {
		if icon.ID != id {
			continue
		}
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("ETag", `"`+id+`"`)
		if r.Header.Get("If-None-Match") == `"`+id+`"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"id": icon.ID, "path": icon.Path, "width": 16, "height": 16})
		return
	}
	http.NotFound(w, r)
}
