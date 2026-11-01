package web

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"unicode"
	"unicode/utf8"

	"github.com/yosukeno/pogo/skillruntime"
	"github.com/yosukeno/pogo/skills"
)

const skillDocumentEditLimit = skills.DocumentPreviewLimit

var skillDocumentRevisionRE = regexp.MustCompile(`^[0-9a-f]{64}$`)
var errSkillDocumentConflict = errors.New("文档已被其他窗口修改，请重新加载后再保存；当前编辑内容仍可保留")

type skillDocumentEditMetadata struct {
	Editable bool
	Reason   string
}

func skillDocumentRevision(data []byte) string {
	value := sha256.Sum256(data)
	return hex.EncodeToString(value[:])
}

// A managed instruction edit is deliberately narrower than package preview:
// uploaded ordinary SKILL.md only. Runtime bundle bytes are digest-pinned.
func (m *Manager) skillDocumentEditMetadata(skill skills.Skill, relative string) skillDocumentEditMetadata {
	if relative != "SKILL.md" {
		return skillDocumentEditMetadata{Reason: "仅支持编辑 SKILL.md，其他包内文件保持只读"}
	}
	if !m.removableSkill(skill) || skill.Path != filepath.Join(skill.Dir, "SKILL.md") {
		return skillDocumentEditMetadata{Reason: "仅上传的普通 Skill 可编辑；内置、目录挂载及运行时包保持只读"}
	}
	root, err := m.openEditableSkillRoot(skill)
	if err != nil {
		return skillDocumentEditMetadata{Reason: "Skill 路径不可安全写入，符号链接或异常占用文件保持只读"}
	}
	defer root.Close()
	_, data, err := readEditableSkill(root)
	if err != nil || len(data) > skillDocumentEditLimit {
		return skillDocumentEditMetadata{Reason: "SKILL.md 不可读取或超过 256 KiB，无法在线编辑"}
	}
	return skillDocumentEditMetadata{Editable: true}
}

// Each operation is relative to an already-open upload root. Refuse aliases
// between packages and symlinked files, including links pointing inside root.
func (m *Manager) openEditableSkillRoot(skill skills.Skill) (*os.Root, error) {
	if !m.removableSkill(skill) || skill.Path != filepath.Join(skill.Dir, "SKILL.md") {
		return nil, errors.New("此 Skill 文档只读")
	}
	root, err := os.OpenRoot(m.uploadedSkillRoot())
	if err != nil {
		return nil, err
	}
	defer root.Close()
	name := filepath.Base(skill.Dir)
	before, err := root.Lstat(name)
	if err != nil || !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("Skill 目录不能是符号链接")
	}
	pkg, err := root.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	actual, err := pkg.Stat(".")
	if err != nil || !os.SameFile(before, actual) {
		pkg.Close()
		return nil, errors.New("Skill 目录已改变，请重新加载")
	}
	return pkg, nil
}
func readEditableSkill(root *os.Root) (os.FileInfo, []byte, error) {
	before, err := root.Lstat("SKILL.md")
	if err != nil || !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 {
		return nil, nil, errors.New("SKILL.md 必须是普通文件")
	}
	if before.Size() > skillDocumentEditLimit {
		return nil, nil, errors.New("SKILL.md 超过 256 KiB")
	}
	file, err := root.OpenFile("SKILL.md", os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()
	actual, err := file.Stat()
	if err != nil || !actual.Mode().IsRegular() || !os.SameFile(before, actual) {
		return nil, nil, errors.New("SKILL.md 已改变，请重新加载")
	}
	data, err := io.ReadAll(io.LimitReader(file, skillDocumentEditLimit+1))
	if err != nil {
		return nil, nil, err
	}
	if len(data) > skillDocumentEditLimit {
		return nil, nil, errors.New("SKILL.md 超过 256 KiB")
	}
	if !utf8.Valid(data) {
		return nil, nil, errors.New("SKILL.md 必须为 UTF-8 文本")
	}
	return actual, data, nil
}

func validateEditableSkillDocument(data []byte, skill skills.Skill) (string, error) {
	if len(data) > skillDocumentEditLimit {
		return "", errors.New("SKILL.md 最多 256 KiB")
	}
	if !utf8.Valid(data) || bytes.ContainsFunc(data, func(r rune) bool { return unicode.IsControl(r) && r != '\n' && r != '\r' && r != '\t' && r != '\f' }) {
		return "", errors.New("SKILL.md 必须是有效 UTF-8 文本，不能包含二进制控制字符")
	}
	if err := validateEditableFrontmatter(string(data)); err != nil {
		return "", err
	}
	name, description, err := skills.PackageMetadata(data, filepath.Base(skill.Dir))
	if err != nil {
		return "", err
	}
	if name != skill.Name {
		return "", errors.New("不能修改 Skill 的 name；更名请重新导入，以免破坏已有 Agent 绑定")
	}
	fields, _ := skills.ParseFrontmatter(string(data))
	truthy := func(v string) bool {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true", "yes", "on", "1":
			return true
		}
		return false
	}
	if truthy(fields["browser-task"]) != skill.BrowserTask || truthy(fields["disable-model-invocation"]) != skill.DisableModelInvocation {
		return "", errors.New("不能在文档编辑中修改 browser-task 或 disable-model-invocation 能力标记；请重新导入 Skill")
	}
	return description, nil
}

// Validate the supported frontmatter grammar instead of silently accepting an
// unterminated quote or duplicate key that the tolerant discovery parser skips.
func validateEditableFrontmatter(text string) error {
	text = strings.TrimPrefix(strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n"), "\ufeff")
	if !strings.HasPrefix(text, "---\n") {
		return errors.New("SKILL.md 需要包含完整 YAML frontmatter（name 和 description）")
	}
	lines := strings.Split(text, "\n")
	seen := map[string]bool{}
	closed := false
	for _, line := range lines[1:] {
		trim := strings.TrimSpace(line)
		if separator := strings.TrimRight(line, " \t"); separator == "---" || separator == "..." {
			closed = true
			break
		}
		if trim == "" || strings.HasPrefix(trim, "#") {
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if !ok || key == "" || strings.ContainsAny(key, " \t[]{}\"'") {
			return errors.New("frontmatter 格式无效，请使用顶层键值格式")
		}
		if seen[key] {
			return fmt.Errorf("frontmatter 字段重复：%s", key)
		}
		seen[key] = true
		if value != "" && (value[0] == '\'' || value[0] == '"') && (len(value) < 2 || value[len(value)-1] != value[0]) {
			return fmt.Errorf("frontmatter 字段 %s 的引号未闭合", key)
		}
		if (key == "name" || key == "description") && strings.HasPrefix(value, "[") || ((key == "name" || key == "description") && strings.HasPrefix(value, "{")) {
			return fmt.Errorf("frontmatter 字段 %s 必须是文本", key)
		}
	}
	if !closed {
		return errors.New("frontmatter 缺少结束分隔符 ---")
	}
	return nil
}

func (s *Server) handleManageSkillSaveContent(w http.ResponseWriter, r *http.Request) {
	relative, err := skills.DocumentPath(r.URL.Query().Get("path"))
	if err != nil || relative != "SKILL.md" {
		writeErr(w, 400, errors.New("仅支持保存当前 Skill 的 SKILL.md"))
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, (2<<20)+1))
	if err != nil {
		writeErr(w, 400, errors.New("无法读取文档内容"))
		return
	}
	if len(raw) > 2<<20 {
		writeErr(w, 413, errors.New("文档请求过大"))
		return
	}
	if !utf8.Valid(raw) {
		writeErr(w, 400, errors.New("文档请求必须为 UTF-8 JSON"))
		return
	}
	var input struct {
		Content          *string `json:"content"`
		ExpectedRevision string  `json:"expected_revision"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || input.Content == nil || !skillDocumentRevisionRE.MatchString(input.ExpectedRevision) {
		writeErr(w, 400, errors.New("需要完整 content 和有效 expected_revision"))
		return
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		writeErr(w, 400, errors.New("文档请求只能包含一个 JSON 对象"))
		return
	}
	content := []byte(*input.Content)
	if len(content) > skillDocumentEditLimit {
		writeErr(w, 413, errors.New("SKILL.md 最多 256 KiB"))
		return
	}
	m := s.mgr
	m.studio.runtimeSkillEdits.Lock()
	saved, err := m.saveManagedSkillDocument(r.PathValue("id"), content, input.ExpectedRevision)
	m.studio.runtimeSkillEdits.Unlock()
	if err != nil {
		status := 400
		switch {
		case errors.Is(err, ErrNotFound):
			status = 404
		case errors.Is(err, errSkillDocumentConflict):
			status = 409
		case errors.Is(err, errSkillDocumentReadOnly):
			status = 403
		case errors.Is(err, errSkillDocumentWrite):
			status = 500
		}
		writeErr(w, status, err)
		return
	}
	// Sync only after releasing the global write lock: the per-session updater
	// acquires its read side, and an active run keeps its captured prompt.
	m.refreshSkillDocumentMetadata()
	writeSkillDocumentBytes(w, saved, "SKILL.md", content, false, skillDocumentEditMetadata{Editable: true})
}

var errSkillDocumentReadOnly = errors.New("此 Skill 文档只读；仅支持编辑上传的普通 Skill 的 SKILL.md")
var errSkillDocumentWrite = errors.New("Skill 文档保存失败，请检查写入权限后重试")

func (m *Manager) saveManagedSkillDocument(id string, content []byte, expected string) (skills.Skill, error) {
	skill, ok := m.skillCatalogIDs()[id]
	if !ok {
		if m.cfg.SkillRuntime != nil {
			for _, entry := range m.cfg.SkillRuntime.Catalog() {
				if skillruntime.PackageSkillID(entry.Ref) == id {
					return skill, errSkillDocumentReadOnly
				}
			}
		}
		return skill, ErrNotFound
	}
	if !m.removableSkill(skill) {
		return skill, errSkillDocumentReadOnly
	}
	root, err := m.openEditableSkillRoot(skill)
	if err != nil {
		return skill, errSkillDocumentReadOnly
	}
	defer root.Close()
	oldInfo, old, err := readEditableSkill(root)
	if err != nil {
		return skill, errSkillDocumentReadOnly
	}
	if skillDocumentRevision(old) != expected {
		return skill, errSkillDocumentConflict
	}
	description, err := validateEditableSkillDocument(content, skill)
	if err != nil {
		return skill, err
	}
	if bytes.Equal(content, old) {
		return skill, nil
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return skill, errSkillDocumentWrite
	}
	temp := ".SKILL.md.edit-" + hex.EncodeToString(nonce[:])
	file, err := root.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, oldInfo.Mode().Perm())
	if err != nil {
		return skill, errSkillDocumentWrite
	}
	defer root.Remove(temp)
	if _, err = file.Write(content); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		return skill, errSkillDocumentWrite
	}
	finalInfo, current, err := readEditableSkill(root)
	if err != nil || !os.SameFile(oldInfo, finalInfo) || skillDocumentRevision(current) != expected {
		return skill, errSkillDocumentConflict
	}
	if err := root.Rename(temp, "SKILL.md"); err != nil {
		return skill, errSkillDocumentWrite
	}
	if dir, err := root.Open("."); err == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	// Preserve identity, paths, capability flags, aliases and every other Skill.
	// Only validated description metadata changes; tool manifests are untouched.
	list := append([]skills.Skill(nil), m.Skills()...)
	for i := range list {
		if skillIDOf(list[i]) == id {
			list[i].Description = description
			skill = list[i]
			break
		}
	}
	m.SetSkills(list)
	return skill, nil
}
func (m *Manager) refreshSkillDocumentMetadata() {
	m.mu.Lock()
	var sessions []*Session
	for _, s := range m.sessions {
		if s.studioID != "" {
			sessions = append(sessions, s)
		}
	}
	m.mu.Unlock()
	for _, s := range sessions {
		_, _ = s.SyncStudioSkillPolicy()
	}
}
