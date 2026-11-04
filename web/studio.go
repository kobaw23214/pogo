package web

// Agent Studio manages definitions and enters the existing Pogo Web workbench.
// Each conversation pins its definition and uses the existing user's workspace,
// tools and approval policy. Older text-only conversations keep their profile.
import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/yosukeno/pogo/agent"
	"github.com/yosukeno/pogo/catalog"
	"github.com/yosukeno/pogo/config"
	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/session"
	"github.com/yosukeno/pogo/skillruntime"
	"github.com/yosukeno/pogo/skills"
	"github.com/yosukeno/pogo/tools"
)

const studioRuntime = "agent-studio-v1"
const studioWebRuntime = "agent-studio-web-v1"

type studioInput struct {
	Name         string `json:"name"`
	Description  string `json:"description"`
	Instructions string `json:"instructions"`
	Model        string `json:"model"`
	// Models is the allowlist the conversation's model switch may move inside.
	// A pointer so "omitted" (legacy: exactly [Model]) stays distinct from an
	// explicit list, and an explicit list round-trips losslessly — never
	// omitempty on the slice itself.
	Models *[]string `json:"models,omitempty"`
	// Skills selects Skill catalog IDs (see GET /api/manage/capabilities).
	// New definitions normalize omitted selections to an explicit empty array.
	Skills *[]string `json:"skills,omitempty"`
	// MCPServers selects MCP server configuration names (mcp.json keys).
	// New definitions normalize omitted selections to an explicit empty array.
	MCPServers *[]string `json:"mcp_servers,omitempty"`
	// Tools selects native built-in tool names (see GET /api/tools' builtin
	// section). Same pointer semantics as Skills: nil inherits the full set
	// (legacy definitions, zero migration), an explicit array seeds the
	// conversation binding; ToolPolicy synchronizes later edits while idle.
	// The protected core stays registered whatever
	// the array says — tools.Options.Enabled enforces that at build time.
	Tools *[]string `json:"tools,omitempty"`
	// Groups whose native grants are controlled by trusted embedded Skill selection.
	// Omitted on old definitions: retain their original grants until explicit adoption.
	SkillToolBindings []string `json:"skill_tool_bindings,omitempty"`
	// Computed during reference validation; never accepted from the client.
	skillBinding json.RawMessage
	Color        string         `json:"color"`
	Icon         string         `json:"icon"`
	Category     string         `json:"category"`
	Starters     []starterEntry `json:"starters"`
	Status       string         `json:"status"`
}

// starterEntry is one opening question, in either of two wire shapes: a plain
// string (the original contract, still what simple Agents send) or a card
// object {title, label, prompt, icon} — the shape the chat homepage's card
// grid renders with a heading and a corner tag. A plain string round-trips as
// a plain string; only a card with a title, label or icon upgrades to the
// object shape, so untouched legacy data stays byte-identical.
type starterEntry struct {
	Title       string             `json:"title,omitempty"`
	Label       string             `json:"label,omitempty"`
	Prompt      string             `json:"prompt"`
	Icon        string             `json:"icon,omitempty"`
	Parts       []MessageInputPart `json:"parts,omitempty"`
	InputLayout string             `json:"input_layout,omitempty"`
}

func (s *starterEntry) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		var plain string
		if err := json.Unmarshal(b, &plain); err != nil {
			return err
		}
		s.Prompt = plain
		return nil
	}
	type alias starterEntry
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*s = starterEntry(a)
	return nil
}

func (s starterEntry) MarshalJSON() ([]byte, error) {
	if s.Title == "" && s.Label == "" && s.Icon == "" && s.Parts == nil && s.InputLayout == "" {
		return json.Marshal(s.Prompt)
	}
	type alias starterEntry
	return json.Marshal(alias(s))
}

type studioAgent struct {
	studioInput
	SkillBinding      json.RawMessage `json:"skill_binding,omitempty"`
	ID                string          `json:"id"`
	Revision          int             `json:"revision"`
	CreatedAt         string          `json:"created_at"`
	UpdatedAt         string          `json:"updated_at"`
	ConversationCount int             `json:"conversation_count"`
	Deleted           bool            `json:"deleted,omitempty"`
}

type studioBinding struct {
	Agent     studioAgent     `json:"agent"`
	Owner     *identity.Scope `json:"owner,omitempty"`
	CreatedAt string          `json:"created_at"`
	Runtime   string          `json:"runtime,omitempty"`
	// CurrentModel is the model the conversation last switched to, inside the
	// pinned definition's allowlist. Empty means the definition's default. The
	// definition snapshot above stays immutable; this is the one per-
	// conversation value a model switch may move, and it persists across restarts.
	CurrentModel string `json:"current_model,omitempty"`
	// ModelPolicy is an independently versioned, host-applied model-only update.
	// Agent and SkillLock retain their original capability/instruction snapshot;
	// SkillPolicy independently moves only the effective Skill projection.
	ModelPolicy *studioModelPolicy `json:"model_policy,omitempty"`
	SkillPolicy *studioSkillPolicy `json:"skill_policy,omitempty"`
	ToolPolicy  *studioToolPolicy  `json:"tool_policy,omitempty"`
	// Host-generated alongside the conversation binding, never client supplied.
	SkillLock json.RawMessage `json:"skill_lock,omitempty"`
	MCPExport bool            `json:"mcp_export,omitempty"`
}

// studioTemplateInput is the creation-dialog starting point an admin can save
// from the current draft: identity, category and starters, never capabilities.
type studioTemplateInput struct {
	Name         string         `json:"name"`
	Description  string         `json:"description"`
	Instructions string         `json:"instructions"`
	Color        string         `json:"color"`
	Icon         string         `json:"icon"`
	Category     string         `json:"category"`
	Starters     []starterEntry `json:"starters"`
}

type studioTemplate struct {
	studioTemplateInput
	ID        string `json:"id"`
	CreatedAt string `json:"created_at"`
}

// studioTemplates is the on-disk shape of agent-templates.json. It is a
// separate file from agent-studio.json on purpose: templates are workspace
// furniture, not authoritative agent data, and an older binary that does not
// know the field must keep loading the registry unchanged.
type studioTemplates struct {
	Version   int                       `json:"version"`
	Templates map[string]studioTemplate `json:"templates"`
}

// allowedModels resolves the definition's allowlist: an omitted models field
// is the legacy single pin of exactly the default model.
func (a studioAgent) allowedModels() []string {
	if a.Models == nil {
		return []string{a.Model}
	}
	return append([]string(nil), (*a.Models)...)
}

// allowsModel reports whether model (a canonical catalog ID) is inside the
// definition's allowlist.
func (a studioAgent) allowsModel(model string) bool {
	for _, m := range a.allowedModels() {
		if m == model {
			return true
		}
	}
	return false
}

// currentModel is the model a restored conversation resumes with: the last
// switch when it is still inside the allowlist, the default otherwise.
func (b studioBinding) currentModel() string {
	if b.CurrentModel != "" && slices.Contains(b.allowedModels(), b.CurrentModel) {
		return b.CurrentModel
	}
	return b.defaultModel()
}

func (b studioBinding) runtime() string {
	if b.Runtime == "" {
		return studioRuntime
	}
	return b.Runtime
}

type studioData struct {
	Version       int                      `json:"version"`
	Agents        map[string]studioAgent   `json:"agents"`
	Bindings      map[string]studioBinding `json:"bindings"`
	ResetSessions []string                 `json:"reset_sessions,omitempty"`
	ResetShares   []string                 `json:"reset_shares,omitempty"`
}

type studioStore struct {
	// runtimeSkillEdits prevents deleting a package between reference validation
	// and saving a definition or a pinned conversation binding.
	runtimeSkillEdits sync.RWMutex
	mu                sync.RWMutex
	path              string
	data              studioData
	// tplPath/templates hold the dialog's starting points in their own file
	// (see studioTemplates); guarded by the same mu as the registry.
	tplPath   string
	templates studioTemplates
	// refs validates Skill/MCP server references against the manager's live
	// catalogs. NewManager installs it; a store built bare in a test leaves it
	// nil and accepts the shape-only validation.
	refs           func(*studioInput) error
	bindSkillTools func(*studioInput, *studioAgent) error
}

func openStudioStore(dir string) (*studioStore, error) {
	path := filepath.Join(dir, "agent-studio.json")
	s := &studioStore{path: path, tplPath: filepath.Join(dir, "agent-templates.json"),
		data:      studioData{Version: studioFormatVersion, Agents: map[string]studioAgent{}, Bindings: map[string]studioBinding{}},
		templates: studioTemplates{Version: 1, Templates: map[string]studioTemplate{}}}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		if _, markerErr := os.Stat(path + ".initialized"); markerErr == nil {
			return nil, errors.New("agent studio registry is missing from an initialized data directory")
		} else if !errors.Is(markerErr, os.ErrNotExist) {
			return nil, markerErr
		}
		if err := s.save(s.data); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	} else {
		raw, readErr := io.ReadAll(io.LimitReader(f, (32<<20)+1))
		closeErr := f.Close()
		if readErr != nil {
			return nil, readErr
		}
		if closeErr != nil {
			return nil, closeErr
		}
		if len(raw) > 32<<20 {
			return nil, errors.New("agent studio registry exceeds size limit")
		}
		var header struct {
			Version int `json:"version"`
		}
		if err := json.Unmarshal(raw, &header); err != nil {
			return nil, fmt.Errorf("agent studio registry: %w", err)
		}
		if header.Version > 0 && header.Version < studioFormatVersion {
			if err := s.resetFormat(raw); err != nil {
				return nil, err
			}
		} else {
			dec := json.NewDecoder(strings.NewReader(string(raw)))
			dec.DisallowUnknownFields()
			if err := dec.Decode(&s.data); err != nil {
				return nil, fmt.Errorf("agent studio registry: %w", err)
			}
			var trailing any
			if err := dec.Decode(&trailing); err != io.EOF {
				return nil, errors.New("agent studio registry has trailing data")
			}
		}
		if s.data.Version != studioFormatVersion || s.data.Agents == nil || s.data.Bindings == nil {
			return nil, errors.New("invalid agent studio registry")
		}
		for id, a := range s.data.Agents {
			if !validID(id) || id != a.ID || a.Revision < 1 {
				return nil, errors.New("invalid agent definition")
			}
		}
		for id, b := range s.data.Bindings {
			if !validID(id) || b.Agent.ID == "" || b.Agent.Revision < 1 {
				return nil, errors.New("invalid agent conversation binding")
			}
			if _, ok := s.data.Agents[b.Agent.ID]; !ok {
				return nil, errors.New("agent conversation references a missing definition")
			}
			if b.runtime() != studioRuntime && b.runtime() != studioWebRuntime {
				return nil, errors.New("unsupported agent conversation runtime")
			}
		}
	}
	if err := os.WriteFile(path+".initialized", []byte("agent-studio-v2\n"), 0600); err != nil {
		return nil, err
	}
	if err := s.loadTemplates(); err != nil {
		return nil, err
	}
	return s, nil
}

// loadTemplates reads agent-templates.json; a missing file is a first boot and
// gets the built-in starting points, so every deployment opens with the curated
// row and admins curate from there. Corrupt or future-version files fail closed
// like the registry does.
func (s *studioStore) loadTemplates() error {
	f, err := os.Open(s.tplPath)
	if errors.Is(err, os.ErrNotExist) {
		s.templates = studioTemplates{Version: 1, Templates: map[string]studioTemplate{}}
		for _, tpl := range builtinStudioTemplates() {
			s.templates.Templates[tpl.ID] = tpl
		}
		return s.saveTemplates(s.templates)
	}
	if err != nil {
		return err
	}
	defer f.Close()
	dec := json.NewDecoder(io.LimitReader(f, 8<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s.templates); err != nil {
		return fmt.Errorf("agent studio templates: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return errors.New("agent studio templates has trailing data")
	}
	if s.templates.Version != 1 || s.templates.Templates == nil {
		return errors.New("invalid agent studio templates")
	}
	for id, tpl := range s.templates.Templates {
		if !validID(id) || id != tpl.ID {
			return errors.New("invalid agent template definition")
		}
	}
	return nil
}

func (s *studioStore) saveTemplates(next studioTemplates) error {
	if err := os.MkdirAll(filepath.Dir(s.tplPath), 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	if len(b) > 8<<20 {
		return errors.New("agent studio templates capacity reached")
	}
	f, err := os.CreateTemp(filepath.Dir(s.tplPath), ".studio-templates-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp, s.tplPath)
}

func (s *studioStore) save(next studioData) error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	if len(b) > 32<<20 {
		return errors.New("agent studio registry capacity reached")
	}
	f, err := os.CreateTemp(filepath.Dir(s.path), ".studio-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *studioStore) binding(id string) (studioBinding, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, ok := s.data.Bindings[id]
	return b, ok
}

func (s *studioStore) get(id string) (studioAgent, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	a, ok := s.data.Agents[id]
	return a, ok && !a.Deleted
}

func (s *studioStore) list(admin bool) []studioAgent {
	s.mu.RLock()
	defer s.mu.RUnlock()
	counts := map[string]int{}
	for _, b := range s.data.Bindings {
		counts[b.Agent.ID]++
	}
	out := []studioAgent{}
	for _, a := range s.data.Agents {
		if a.Deleted || (!admin && a.Status != "ready") {
			continue
		}
		a.ConversationCount = counts[a.ID]
		if !admin {
			a.Instructions = ""
		}
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt == out[j].UpdatedAt {
			return out[i].ID < out[j].ID
		}
		return out[i].UpdatedAt > out[j].UpdatedAt
	})
	return out
}

func validateStudioInput(in *studioInput) error {
	// Skills/MCPServers stay nil when absent: nil means legacy "inherit the
	// global catalog", a non-nil empty slice means pinned-to-nothing.
	in.Name, in.Description, in.Instructions = strings.TrimSpace(in.Name), strings.TrimSpace(in.Description), strings.TrimSpace(in.Instructions)
	in.Model = strings.TrimSpace(in.Model)
	if in.Name == "" || utf8.RuneCountInString(in.Name) > 60 {
		return errors.New("名称需要 1–60 个字符")
	}
	if utf8.RuneCountInString(in.Description) > 240 || utf8.RuneCountInString(in.Instructions) > 12000 {
		return errors.New("描述或指令超过长度限制")
	}
	if in.Status != "ready" && in.Status != "draft" {
		return errors.New("无效的 Agent 状态")
	}
	if in.Model == "" {
		return errors.New("请选择模型")
	}
	if _, err := config.Resolve(in.Model); err != nil {
		return err
	}
	if in.Models != nil {
		models, err := normalizeModelList(*in.Models)
		if err != nil {
			return err
		}
		if len(models) == 0 {
			return errors.New("允许的模型列表不能为空；留空此字段以使用单一默认模型")
		}
		if !slices.Contains(models, in.Model) {
			return errors.New("默认模型必须包含在允许的模型列表中")
		}
		in.Models = &models
	}
	if in.Skills != nil {
		ids, err := normalizeIDList(*in.Skills, "Skill")
		if err != nil {
			return err
		}
		in.Skills = &ids
	}
	if in.MCPServers != nil {
		ids, err := normalizeIDList(*in.MCPServers, "MCP 服务器")
		if err != nil {
			return err
		}
		in.MCPServers = &ids
	}
	if in.Tools != nil {
		ids, err := normalizeIDList(*in.Tools, "内置工具")
		if err != nil {
			return err
		}
		in.Tools = &ids
	}
	colors := map[string]bool{"#5965e8": true, "#e8734a": true, "#2fa37c": true, "#d9a036": true, "#c65d8f": true, "#4a90c2": true}
	in.Color = strings.ToLower(in.Color)
	if !colors[in.Color] {
		in.Color = "#5965e8"
	}
	icons := map[string]bool{"spark": true, "search": true, "pen": true, "code": true, "compass": true, "chart": true}
	if !icons[in.Icon] && !skills.CustomIconIDOK(in.Icon) {
		in.Icon = "spark"
	}
	if in.Category == "" {
		in.Category = "general"
	}
	if utf8.RuneCountInString(in.Category) > 30 {
		return errors.New("分类过长")
	}
	kept := in.Starters[:0]
	for _, starter := range in.Starters {
		if err := normalizeStarterContent(&starter); err != nil {
			return err
		}
		starter.Title = strings.TrimSpace(starter.Title)
		starter.Label = strings.TrimSpace(starter.Label)
		starter.Prompt = strings.TrimSpace(starter.Prompt)
		starter.Icon = strings.TrimSpace(starter.Icon)
		if starter.Prompt == "" {
			continue
		}
		if (starter.Parts == nil && utf8.RuneCountInString(starter.Prompt) > 200) || utf8.RuneCountInString(starter.Title) > 60 || utf8.RuneCountInString(starter.Label) > 40 {
			return errors.New("开场问题最多 200 个字符（标题 60、标签 40）")
		}
		if starter.Icon != "" && !skills.StarterIconOK(starter.Icon) {
			return errors.New("开场卡片图标无效：" + starter.Icon)
		}
		kept = append(kept, starter)
	}
	in.Starters = kept
	if in.Starters == nil {
		in.Starters = []starterEntry{}
	}
	return nil
}

// normalizeModelList trims, dedups (first occurrence wins, order kept) and
// resolves every entry to its canonical catalog ID, so an alias can never
// smuggle a second spelling of a refused model past the allowlist check.
func normalizeModelList(list []string) ([]string, error) {
	if len(list) > 16 {
		return nil, errors.New("允许的模型最多 16 个")
	}
	out := make([]string, 0, len(list))
	seen := map[string]bool{}
	for _, entry := range list {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			return nil, errors.New("模型 ID 不能为空")
		}
		cfg, err := config.Resolve(entry)
		if err != nil {
			return nil, err
		}
		if seen[cfg.Model] {
			continue
		}
		seen[cfg.Model] = true
		out = append(out, cfg.Model)
	}
	return out, nil
}

// normalizeIDList trims and deduplicates capability selections without an
// arbitrary item-count ceiling. Request-size boundaries still apply, and
// existence and grants are checked separately against the live catalogs.
func normalizeIDList(list []string, what string) ([]string, error) {
	out := make([]string, 0, len(list))
	seen := map[string]bool{}
	for _, entry := range list {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			return nil, fmt.Errorf("%s ID 不能为空", what)
		}
		if seen[entry] {
			continue
		}
		seen[entry] = true
		out = append(out, entry)
	}
	return out, nil
}

func (s *studioStore) put(id string, input studioInput, expected int) (studioAgent, error) {
	s.runtimeSkillEdits.RLock()
	defer s.runtimeSkillEdits.RUnlock()
	if err := validateStudioInput(&input); err != nil {
		return studioAgent{}, err
	}
	if s.bindSkillTools != nil {
		var previous *studioAgent
		if id != "" {
			old, ok := s.get(id)
			if !ok {
				return studioAgent{}, ErrNotFound
			}
			if old.Revision != expected {
				return studioAgent{}, errors.New("Agent 已被更新，请刷新后重试")
			}
			previous = &old
		}
		if err := s.bindSkillTools(&input, previous); err != nil {
			return studioAgent{}, err
		}
	}
	if s.refs != nil {
		if err := s.refs(&input); err != nil {
			return studioAgent{}, err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	a := studioAgent{studioInput: input, SkillBinding: input.skillBinding, Revision: 1, CreatedAt: now, UpdatedAt: now}
	if id != "" {
		old, ok := s.data.Agents[id]
		if !ok || old.Deleted {
			return a, ErrNotFound
		}
		if expected != old.Revision {
			return a, errors.New("Agent 已被更新，请刷新后重试")
		}
		a.ID, a.CreatedAt, a.Revision = id, old.CreatedAt, old.Revision+1

	} else {
		if len(s.data.Agents) >= 500 {
			return a, errors.New("Agent 数量已达到当前上限")
		}
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return a, err
		}
		a.ID = "ag_" + hex.EncodeToString(nonce[:])
	}
	if len(a.SkillBinding) > 0 {
		var err error
		a.SkillBinding, err = skillruntime.NormalizeBinding(a.SkillBinding, a.ID, a.Revision)
		if err != nil {
			return a, err
		}
	}
	next := s.data
	next.Agents = maps.Clone(s.data.Agents)
	next.Agents[a.ID] = a
	if err := s.save(next); err != nil {
		return a, err
	}
	s.data = next
	return a, nil
}

func (s *studioStore) remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.data.Agents[id]
	if !ok || a.Deleted {
		return ErrNotFound
	}
	a.Deleted, a.Status, a.UpdatedAt = true, "draft", time.Now().UTC().Format(time.RFC3339Nano)
	next := s.data
	next.Agents = maps.Clone(s.data.Agents)
	next.Agents[id] = a
	if err := s.save(next); err != nil {
		return err
	}
	s.data = next
	return nil
}

// builtinStudioTemplates are the curated starting points seeded into a fresh
// workspace. They are plain templates once seeded — deletable, replaceable —
// which is why they live here rather than in the frontend: the file, not the
// bundle, is the truth.
func builtinStudioTemplates() []studioTemplate {
	seed := []studioTemplateInput{
		{
			Name:         "写作伙伴",
			Description:  "润色、改写与结构建议，让文字更干净有力。",
			Instructions: "你是一位中文写作伙伴。帮助用户把想法变成清晰、有节奏的文字：先理解写作目的与读者，再给出结构与表达建议。修改时保留用户自己的语气，并说明每处重要改动的理由；涉及事实而你不确定时明确标注，不编造引用与数据。",
			Icon:         "pen",
			Color:        "#c65d8f",
			Category:     "writing",
			Starters:     []starterEntry{{Prompt: "帮我把这段文字改得更简洁有力"}, {Prompt: "为这篇文章拟三个不同风格的标题"}, {Prompt: "检查这段话的逻辑与语气是否一致"}},
		},
		{
			Name:         "研究助手",
			Description:  "拆解复杂问题，区分事实与假设，给出有依据的结论。",
			Instructions: "你是一位严谨的研究助手。面对复杂问题，先澄清目标与边界，再把它拆解成可以逐一验证的小问题；清楚区分事实、假设与推测，给出结论时说明依据与不确定性。优先使用用户提供的资料，不杜撰来源、数据或引文。",
			Icon:         "search",
			Color:        "#4a90c2",
			Category:     "research",
			Starters:     []starterEntry{{Prompt: "帮我梳理这个问题的研究思路"}, {Prompt: "把这份资料提炼成要点和待验证的假设"}, {Prompt: "对比这两个方案的优劣并给出建议"}},
		},
		{
			Name:         "代码讲解",
			Description:  "把代码讲明白：整体思路、关键逻辑与风险点。",
			Instructions: "你是一位耐心的代码讲解者。用平实的语言解释代码在做什么、为什么这样写：先讲整体思路，再逐段说明关键逻辑、边界情况与潜在风险。给出改写建议时附带简短示例，并解释取舍。不臆测未提供的上下文，不确定的地方直接说明。",
			Icon:         "code",
			Color:        "#2fa37c",
			Category:     "code",
			Starters:     []starterEntry{{Prompt: "逐行解释这段代码的作用"}, {Prompt: "这个报错可能是什么原因？"}, {Prompt: "帮我 review 这段改动有哪些风险"}},
		},
		{
			Name:         "工作规划",
			Description:  "把目标拆成步骤，排定优先级，盯住里程碑。",
			Instructions: "你是一位务实的工作规划伙伴。把模糊的目标整理成可执行的计划：明确想要的结果、拆解步骤、排定优先级，并标出依赖与风险。计划要留有余量，检查点具体可衡量；信息不足时先问关键问题，而不是编造细节。",
			Icon:         "compass",
			Color:        "#d9a036",
			Category:     "planning",
			Starters:     []starterEntry{{Prompt: "把这周的目标拆成每天可执行的计划"}, {Prompt: "帮我把这个项目排出里程碑"}, {Prompt: "事情有点乱，帮我理一下优先级"}},
		},
		{
			Name:         "恶意代码分析",
			Description:  "样本检索、能力标签、反编译与 Yara 规则的静态分析工作流。",
			Instructions: "你是一位严谨的恶意代码分析伙伴。本部署不是编码任务：工作区里没有要构建、测试或 lint 的项目，不要为「验证工作」跑 build/test——结论靠分析证据支撑；工作区的 git 只用于文件回滚，不要提交或改版本历史；分析时不写代码，需要落盘的产物写 ~/mal-out/。工作方式遵循 malware-analysis 技能：库级问答优先用一等工具（mal_search / mal_sample / mal_similar / mal_family / mal_stats / mal_tags），文件级分析（mal-hash / mal-decompile / mal-yara / mal-similar）经 bash 调用技能脚本，固定子命令覆盖不了的聚合用 malquery sql。红线：样本按真实恶意软件对待——只读分析，绝不执行样本，也不对样本目录做任何写操作。结论要区分数据库里的事实与模型推断，逐条给出证据与置信度；合成或演示数据必须明确标注，不把自动生成的规则说成经过生产验证。",
			Icon:         "search",
			Color:        "#e8734a",
			Category:     "research",
			Starters: []starterEntry{
				{Icon: "graph", Title: "让 agent 操作样本库页面", Label: "/skill:browser-use", Prompt: "/skill:browser-use 打开样本库页面（面板里的样本库），在检索页把「最大检出数」筛成 0，浏览结果列表并告诉我前 5 条的家族、类型和首见日期；最后说明页面上哪些数据带「演示数据」标注。"},
				{Icon: "search", Title: "找一个杀软未检出的样本", Label: "零检出 + 有二进制", Prompt: "在样本库里找一个 detections 为 0 且有二进制文件的样本。查看它的详情和 CAPA 标签，判断它是否具有恶意行为，给出结论、逐条证据和置信度。不要仅凭检出数为 0 就下结论。"},
				{Icon: "code", Title: "反编译看函数意图", Label: "Ghidra 伪 C", Prompt: "对上面那个样本做 Ghidra 反编译，分析关键函数的功能意图。只做静态分析，绝不执行样本。请说明哪些结论直接来自伪 C 代码，哪些属于推测。"},
				{Icon: "shield", Title: "为样本生成 Yara 规则", Label: "yarGen + 人工复核", Prompt: "为该样本生成一条 Yara 规则，解释其中最有区分度的字符串，并指出哪些通用字符串应当人工删除。不要把自动生成的规则说成已经过生产验证。"},
				{Icon: "book", Title: "解读一个信息丰富的样本", Label: "标签 + 沙箱日志 + 摘要", Prompt: "找一个 Qakbot 样本，查看它的标签、压缩后的动态沙箱日志和 LLM 摘要，按「主要行为、持久化、网络活动、风险」四部分向我解释。区分数据库里的事实与模型推断。"},
				{Icon: "spark", Title: "查看家族画像与影响面", Label: "注意标注演示数据", Prompt: "介绍 Qakbot 家族：库内样本数量、类型构成、高频行为标签、活动时间跨度和百科介绍，再给出传播趋势、地域分布和攻击基础设施。明确说明每一部分的数据来源，合成的演示数据必须标注出来。"},
			},
		},
	}
	out := make([]studioTemplate, len(seed))
	for i, in := range seed {
		if err := validateTemplateInput(&in); err != nil {
			panic(err)
		}
		out[i] = studioTemplate{studioTemplateInput: in, ID: fmt.Sprintf("tpl_builtin_%d", i+1), CreatedAt: "2026-01-01T00:00:00Z"}
	}
	return out
}

func validateTemplateInput(in *studioTemplateInput) error {
	in.Name, in.Description, in.Instructions = strings.TrimSpace(in.Name), strings.TrimSpace(in.Description), strings.TrimSpace(in.Instructions)
	if in.Name == "" || utf8.RuneCountInString(in.Name) > 60 {
		return errors.New("模板名称需要 1–60 个字符")
	}
	if utf8.RuneCountInString(in.Description) > 240 || utf8.RuneCountInString(in.Instructions) > 12000 {
		return errors.New("模板简介或指令超过长度限制")
	}
	colors := map[string]bool{"#5965e8": true, "#e8734a": true, "#2fa37c": true, "#d9a036": true, "#c65d8f": true, "#4a90c2": true}
	in.Color = strings.ToLower(in.Color)
	if !colors[in.Color] {
		in.Color = "#5965e8"
	}
	icons := map[string]bool{"spark": true, "search": true, "pen": true, "code": true, "compass": true, "chart": true}
	if !icons[in.Icon] && !skills.CustomIconIDOK(in.Icon) {
		in.Icon = "spark"
	}
	if in.Category == "" {
		in.Category = "general"
	}
	if utf8.RuneCountInString(in.Category) > 30 {
		return errors.New("分类过长")
	}
	kept := in.Starters[:0]
	for _, starter := range in.Starters {
		if err := normalizeStarterContent(&starter); err != nil {
			return err
		}
		starter.Title = strings.TrimSpace(starter.Title)
		starter.Label = strings.TrimSpace(starter.Label)
		starter.Prompt = strings.TrimSpace(starter.Prompt)
		starter.Icon = strings.TrimSpace(starter.Icon)
		if starter.Prompt == "" {
			continue
		}
		if (starter.Parts == nil && utf8.RuneCountInString(starter.Prompt) > 200) || utf8.RuneCountInString(starter.Title) > 60 || utf8.RuneCountInString(starter.Label) > 40 {
			return errors.New("开场问题最多 200 个字符（标题 60、标签 40）")
		}
		if starter.Icon != "" && !skills.StarterIconOK(starter.Icon) {
			return errors.New("开场卡片图标无效：" + starter.Icon)
		}
		kept = append(kept, starter)
	}
	in.Starters = kept
	if in.Starters == nil {
		in.Starters = []starterEntry{}
	}
	return nil
}

func (s *studioStore) listTemplates() []studioTemplate {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]studioTemplate, 0, len(s.templates.Templates))
	for _, tpl := range s.templates.Templates {
		out = append(out, tpl)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt == out[j].CreatedAt {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt < out[j].CreatedAt
	})
	return out
}

func (s *studioStore) putTemplate(input studioTemplateInput) (studioTemplate, error) {
	if err := validateTemplateInput(&input); err != nil {
		return studioTemplate{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.templates.Templates) >= 200 {
		return studioTemplate{}, errors.New("模板数量已达到当前上限")
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return studioTemplate{}, err
	}
	tpl := studioTemplate{
		studioTemplateInput: input,
		ID:                  "tpl_" + hex.EncodeToString(nonce[:]),
		CreatedAt:           time.Now().UTC().Format(time.RFC3339Nano),
	}
	next := studioTemplates{Version: 1, Templates: maps.Clone(s.templates.Templates)}
	next.Templates[tpl.ID] = tpl
	if err := s.saveTemplates(next); err != nil {
		return tpl, err
	}
	s.templates = next
	return tpl, nil
}

func (s *studioStore) removeTemplate(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.templates.Templates[id]; !ok {
		return ErrNotFound
	}
	next := studioTemplates{Version: 1, Templates: maps.Clone(s.templates.Templates)}
	delete(next.Templates, id)
	if err := s.saveTemplates(next); err != nil {
		return err
	}
	s.templates = next
	return nil
}

// setCurrentModel persists a conversation's in-allowlist model switch. The
// pinned definition itself is not touched: two conversations of one Agent
// choose independently, and a definition edit still cannot reach back into
// either.
func (s *studioStore) setCurrentModel(id, model string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.data.Bindings[id]
	if !ok {
		return ErrNotFound
	}
	if b.CurrentModel == model {
		return nil
	}
	b.CurrentModel = model
	next := s.data
	next.Bindings = maps.Clone(s.data.Bindings)
	next.Bindings[id] = b
	if err := s.save(next); err != nil {
		return err
	}
	s.data = next
	return nil
}

func (s *studioStore) bind(id string, a studioAgent, owner *identity.Scope, runtimes ...string) error {
	runtime := studioRuntime
	if len(runtimes) > 0 {
		runtime = runtimes[0]
	}
	if runtime != studioRuntime && runtime != studioWebRuntime {
		return errors.New("unsupported agent conversation runtime")
	}
	return s.bindLocked(id, a, owner, runtime, nil)
}

func (s *studioStore) bindLocked(id string, a studioAgent, owner *identity.Scope, runtime string, runtimeSkillLock json.RawMessage, mcpExport ...bool) error {
	s.runtimeSkillEdits.RLock()
	defer s.runtimeSkillEdits.RUnlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.data.Agents[a.ID]
	if !ok || current.Deleted || current.Status != "ready" || current.Revision != a.Revision {
		return errors.New("Agent 已更新，请重新进入对话")
	}
	if _, exists := s.data.Bindings[id]; exists {
		return errors.New("conversation already bound")
	}
	if owner != nil {
		copied := *owner
		owner = &copied
	}
	next := s.data
	next.Bindings = maps.Clone(s.data.Bindings)
	next.Bindings[id] = studioBinding{Agent: a, Owner: owner, Runtime: runtime, SkillLock: append([]byte(nil), runtimeSkillLock...), CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if len(mcpExport) > 0 && mcpExport[0] {
		b := next.Bindings[id]
		b.MCPExport = true
		next.Bindings[id] = b
	}
	if err := s.save(next); err != nil {
		return err
	}
	s.data = next
	return nil
}

// permit serializes activation with a disable/delete. A disabling writer cannot
// miss a newly-started run when it subsequently cancels this Agent's sessions.
func (s *studioStore) permit(id string) (func(), error) {
	s.mu.RLock()
	a, ok := s.data.Agents[id]
	if !ok || a.Deleted || a.Status != "ready" {
		s.mu.RUnlock()
		return nil, errors.New("此 Agent 暂不可用，请返回管理页")
	}
	return s.mu.RUnlock, nil
}

func (s *Server) studioRoutes() {
	s.runtimeSkillRoutes()
	s.mux.HandleFunc("GET /api/manage/agents", s.handleStudioList)
	s.mux.HandleFunc("POST /api/manage/agents", s.admin(s.handleStudioCreate))
	s.mux.HandleFunc("POST /api/manage/agents/generate-starters", s.admin(s.handleStudioGenerateStarters))
	s.mux.HandleFunc("PATCH /api/manage/agents/{aid}", s.admin(s.handleStudioUpdate))
	s.mux.HandleFunc("DELETE /api/manage/agents/{aid}", s.admin(s.handleStudioDelete))
	s.mux.HandleFunc("GET /api/manage/capabilities", s.admin(s.handleCapabilities))
	s.mux.HandleFunc("GET /api/manage/templates", s.admin(s.handleTemplatesList))
	s.mux.HandleFunc("POST /api/manage/templates", s.admin(s.handleTemplatesCreate))
	s.mux.HandleFunc("DELETE /api/manage/templates/{tid}", s.admin(s.handleTemplatesDelete))
	s.mux.HandleFunc("GET /api/apps/{aid}", s.handleStudioApp)
	s.mux.HandleFunc("GET /api/apps/{aid}/sessions", s.handleStudioSessions)
	s.mux.HandleFunc("POST /api/apps/{aid}/sessions", s.handleStudioConversation)
	s.mux.HandleFunc("GET /api/apps/{aid}/sessions/{sid}/capabilities", s.handleStudioSessionCapabilities)
	s.mux.HandleFunc("POST /api/apps/{aid}/sessions/{sid}/model-config/sync", s.handleStudioModelSync)
	s.mux.HandleFunc("POST /api/apps/{aid}/sessions/{sid}/skill-config/sync", s.handleStudioSkillSync)
	s.mux.HandleFunc("POST /api/apps/{aid}/sessions/{sid}/tools-config/sync", s.handleStudioToolSync)
	s.mux.HandleFunc("POST /api/apps/{aid}/sessions/{sid}/cancel", s.handleStudioCancel)
}

func (s *Server) handleStudioList(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"agents": s.mgr.studio.list(s.adminRequest(r)), "can_manage": s.adminRequest(r)})
}

func (s *Server) handleStudioCreate(w http.ResponseWriter, r *http.Request) {
	var input studioInput
	if err := decodeBody(r, &input); err != nil {
		writeErr(w, 400, err)
		return
	}
	a, err := s.mgr.studio.put("", input, 0)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, http.StatusCreated, a)
}

func (s *Server) handleStudioUpdate(w http.ResponseWriter, r *http.Request) {
	var input struct {
		studioInput
		Revision int `json:"revision"`
	}
	if err := decodeBody(r, &input); err != nil {
		writeErr(w, 400, err)
		return
	}
	a, err := s.mgr.studio.put(r.PathValue("aid"), input.studioInput, input.Revision)
	if err != nil {
		status := 400
		if errors.Is(err, ErrNotFound) {
			status = 404
		} else if strings.Contains(err.Error(), "已被更新") {
			status = 409
		}
		writeErr(w, status, err)
		return
	}
	if a.Status != "ready" {
		s.mgr.cancelStudioRuns(a.ID)
	}
	s.mgr.syncStudioModels(a.ID)
	s.mgr.syncStudioSkills(a.ID)
	s.mgr.syncStudioTools(a.ID)
	writeJSON(w, 200, a)
}

func (s *Server) handleStudioDelete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("aid")
	if err := s.mgr.studio.remove(id); err != nil {
		status := 500
		if errors.Is(err, ErrNotFound) {
			status = 404
		}
		writeErr(w, status, err)
		return
	}
	s.mgr.cancelStudioRuns(id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleTemplatesList(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"templates": s.mgr.studio.listTemplates()})
}

func (s *Server) handleTemplatesCreate(w http.ResponseWriter, r *http.Request) {
	var input studioTemplateInput
	if err := decodeBody(r, &input); err != nil {
		writeErr(w, 400, err)
		return
	}
	tpl, err := s.mgr.studio.putTemplate(input)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, http.StatusCreated, tpl)
}

func (s *Server) handleTemplatesDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.mgr.studio.removeTemplate(r.PathValue("tid")); err != nil {
		status := 500
		if errors.Is(err, ErrNotFound) {
			status = 404
		}
		writeErr(w, status, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (m *Manager) cancelStudioRuns(id string) {
	m.mu.Lock()
	list := []*Session{}
	for _, sess := range m.sessions {
		if sess.studioID == id {
			list = append(list, sess)
		}
	}
	m.mu.Unlock()
	for _, sess := range list {
		sess.Cancel()
	}
}

func (s *Server) studioApp(w http.ResponseWriter, r *http.Request) (studioAgent, bool) {
	a, ok := s.mgr.studio.get(r.PathValue("aid"))
	if !ok || a.Status != "ready" {
		writeErr(w, 404, errors.New("Agent 不存在或尚未就绪"))
		return a, false
	}
	return a, true
}

func (s *Server) handleStudioApp(w http.ResponseWriter, r *http.Request) {
	a, ok := s.studioApp(w, r)
	if !ok {
		return
	}
	if !s.adminRequest(r) {
		a.Instructions = ""
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, a)
}

func (s *Server) handleStudioSessions(w http.ResponseWriter, r *http.Request) {
	a, ok := s.studioApp(w, r)
	if !ok {
		return
	}
	list, err := s.mgr.List()
	if err != nil {
		writeErr(w, 500, err)
		return
	}
	busy, titles := s.mgr.BusyIDs(), s.mgr.LiveTitles()
	out := []sessionSummary{}
	for _, info := range list {
		b, bound := s.mgr.studio.binding(info.ID)
		if !bound || b.Agent.ID != a.ID || !s.ownsSession(r, info.ID) {
			continue
		}
		if info.Title == "" {
			info.Title = titles[info.ID]
		}
		out = append(out, sessionSummary{Info: info, Busy: busy[info.ID]})
	}
	// cwd is the base the client resolves workspace-relative paths against —
	// and the conversation endpoint resolves them against the owner's root, so
	// that root is what this must report in multi-user mode. The server root
	// here would have the sidebar's per-project "+" build
	// "users/<key>/users/<key>/…" workspaces that resolve nowhere.
	root := s.mgr.Cwd()
	if scope, ok := s.ownerScope(r); ok {
		root = s.mgr.UserRoot(scope)
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]any{"sessions": out, "cwd": root})
}

func (s *Server) handleStudioConversation(w http.ResponseWriter, r *http.Request) {
	a, ok := s.studioApp(w, r)
	if !ok {
		return
	}
	var input struct {
		Workspace string `json:"workspace"`
	}
	if err := decodeBody(r, &input); err != nil && !errors.Is(err, io.EOF) {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	var owner *identity.Scope
	if scope, ok := s.ownerScope(r); ok {
		owner = &scope
	}
	sess, err := s.mgr.createStudioConversation(a, owner, input.Workspace)
	if err != nil {
		writeErr(w, 400, err)
		return
	}
	writeJSON(w, 201, map[string]any{"id": sess.ID, "agent_id": a.ID, "revision": a.Revision, "model": a.Model})
}

func (s *Server) handleStudioCancel(w http.ResponseWriter, r *http.Request) {
	b, ok := s.mgr.studio.binding(r.PathValue("sid"))
	if !ok || b.Agent.ID != r.PathValue("aid") || !s.ownsSession(r, r.PathValue("sid")) {
		writeErr(w, 404, ErrNotFound)
		return
	}
	sess, err := s.mgr.Get(r.PathValue("sid"))
	if err != nil {
		writeErr(w, 404, ErrNotFound)
		return
	}
	writeJSON(w, 200, map[string]any{"cancelled": sess.Cancel()})
}

func (m *Manager) createStudioConversation(a studioAgent, owner *identity.Scope, workspaces ...string) (*Session, error) {
	return m.createStudioBoundConversation(a, owner, false, workspaces...)
}

func (m *Manager) createStudioBoundConversation(a studioAgent, owner *identity.Scope, mcpExport bool, workspaces ...string) (*Session, error) {
	return m.createStudioBoundConversationKind(a, owner, mcpExport, nil, workspaces...)
}

func (m *Manager) createStudioBoundConversationKind(a studioAgent, owner *identity.Scope, mcpExport bool, sentinelIdentity *session.SentinelIdentity, workspaces ...string) (*Session, error) {
	return m.createStudioExecutionConversation(a, owner, mcpExport, sentinelIdentity, nil, workspaces...)
}

func (m *Manager) createScheduleConversation(a studioAgent, owner *identity.Scope, identity session.BackgroundTaskIdentity, workspace string) (*Session, error) {
	if !validBackgroundTaskIdentity(&identity) || identity.AgentID != a.ID || identity.OwnerID != sessionResultOwnerKey(owner) {
		return nil, errors.New("invalid scheduled conversation identity")
	}
	return m.createStudioExecutionConversation(a, owner, false, nil, &identity, workspace)
}

func (m *Manager) createStudioExecutionConversation(a studioAgent, owner *identity.Scope, mcpExport bool, sentinelIdentity *session.SentinelIdentity, backgroundIdentity *session.BackgroundTaskIdentity, workspaces ...string) (*Session, error) {
	return m.createStudioExecutionConversationWithWorkflow(a, owner, mcpExport, sentinelIdentity, backgroundIdentity, nil, workspaces...)
}
func (m *Manager) createStudioExecutionConversationWithWorkflow(a studioAgent, owner *identity.Scope, mcpExport bool, sentinelIdentity *session.SentinelIdentity, backgroundIdentity *session.BackgroundTaskIdentity, workflowIdentity *session.AgentWorkflowIdentity, workspaces ...string) (*Session, error) {
	if owner != nil && !owner.Valid() {
		return nil, errors.New("invalid conversation owner")
	}
	if err := m.prepareRuntimeSkillAgent(context.Background(), a); err != nil {
		return nil, err
	}
	cfg, err := config.Resolve(a.Model)
	if err != nil {
		return nil, err
	}
	base := m.cfg.Cwd
	if owner != nil {
		base = m.UserRoot(*owner)
	}
	workspace := ""
	if len(workspaces) > 0 {
		workspace = workspaces[0]
	}
	// No explicit workspace means the Agent's own slice of the owner's root:
	// agents/<id>, created on first use. Two Agents of one user therefore keep
	// separate default trees (uploads and scratch included), while memory,
	// journal and the checkpoint repo stay scoped to the user root — shared
	// across that user's Agents. A hand-edited agent-studio.json could carry a
	// path-shaped id, so the derivation only runs on the generated shape;
	// anything else falls back to the owner's root as before.
	if workspace == "" && validAgentDirName(a.ID) {
		workspace = "agents/" + a.ID
		if err := os.MkdirAll(filepath.Join(base, workspace), 0o755); err != nil {
			return nil, err
		}
	}
	cwd, err := m.resolveWorkspaceUnder(base, workspace)
	if err != nil {
		return nil, err
	}
	var store *session.Store
	if workflowIdentity != nil {
		store, err = session.CreateAgentWorkflowWebConversation(m.cfg.SessionDir, cwd, cfg.Model, *workflowIdentity, m.studioSkillNames(a)...)
	} else if sentinelIdentity != nil {
		store, err = session.CreateSentinelWebConversation(m.cfg.SessionDir, cwd, cfg.Model, *sentinelIdentity, m.studioSkillNames(a)...)
	} else if backgroundIdentity != nil {
		store, err = session.CreateBackgroundTaskWebConversation(m.cfg.SessionDir, cwd, cfg.Model, *backgroundIdentity, m.studioSkillNames(a)...)
	} else {
		store, err = session.CreateAgentWebConversation(m.cfg.SessionDir, cwd, cfg.Model, m.studioSkillNames(a)...)
	}
	if err != nil {
		return nil, err
	}
	id := strings.TrimSuffix(filepath.Base(store.Path()), ".jsonl")
	var runtimeSkillLock json.RawMessage
	if len(a.SkillBinding) > 0 {
		// Document-type workspaceData assets (report libraries and the like)
		// land in the conversation's own tree before the lock pins versions:
		// the files and the tool contracts a session sees come from the same
		// binding resolution. Cheap when the marker is current.
		if err := m.cfg.SkillRuntime.MaterializeWorkspaceData(a.SkillBinding, cwd); err != nil {
			_ = os.Remove(store.Path())
			return nil, fmt.Errorf("materialize skill workspace data: %w", err)
		}
		runtimeSkillLock, err = m.cfg.SkillRuntime.MakeLock(a.SkillBinding, id)
		if err != nil {
			_ = os.Remove(store.Path())
			return nil, err
		}
	}
	if err := m.studio.bindLocked(id, a, owner, studioWebRuntime, runtimeSkillLock, mcpExport); err != nil {
		_ = os.Remove(store.Path())
		return nil, err
	}
	if m.cfg.Catalog != nil && owner != nil {
		if err := m.cfg.Catalog.Create(context.Background(), catalog.Session{ID: id, Scope: *owner, State: "creating"}); err != nil {
			return nil, err
		}
		if err := m.cfg.Catalog.Activate(context.Background(), *owner, id); err != nil {
			return nil, err
		}
	}
	sess := m.newSession(store, cfg, nil, cwd, llm.Usage{}, llm.Usage{}, owner)
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		sess.closeUnpublished()
		return nil, errors.New("server is shutting down")
	}
	m.sessions[id] = sess
	m.mu.Unlock()
	return sess, nil
}

// validAgentDirName gates the per-Agent default workspace derivation: ids are
// server-generated ("ag_" + hex), but agent-studio.json is a hand-editable
// file, and "../x" as an id must not become a path. The published-chat URL
// grammar ([a-zA-Z0-9_-]+) is the shape held to here.
func validAgentDirName(id string) bool {
	if len(id) < 3 || len(id) > 64 {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// applyStudioEffort clones provider settings: this Agent's default must not
// modify a process-wide provider map or silently disable these models' thinking.
func applyStudioEffort(cfg config.Resolved) config.Resolved {
	id := strings.ToLower(cfg.Model)
	if id == "k3" || id == "kimi-k3" || strings.HasPrefix(id, "k3-") || id == "glm-5.3" {
		cfg.ExtraBody = maps.Clone(cfg.ExtraBody)
		if cfg.ExtraBody == nil {
			cfg.ExtraBody = map[string]any{}
		}
		cfg.ExtraBody["reasoning_effort"] = "max"
		cfg.ExtraBody["thinking"] = map[string]any{"type": "enabled"}
	}
	return cfg
}

func (m *Manager) newStudioSession(store *session.Store, cfg config.Resolved, history []session.Timed, usage, delegated llm.Usage, owner *identity.Scope, a studioAgent) *Session {
	cfg = applyStudioEffort(cfg)
	hub := NewHub()
	hub.Seed(history)
	hub.SetCompactions(store.Compactions(""))
	modelHistory := store.PromptTimedMessages("")
	hub.SetRunInfo(cfg.Model, cfg.Provider, cfg.ContextWindow)
	hub.SetUsage(usage)
	policy := NewPolicy()
	gate := NewWebGate(hub, policy, m.cfg.GateTimeout)
	ctx, cancel := context.WithCancel(context.Background())
	s := &Session{ID: strings.TrimSuffix(filepath.Base(store.Path()), ".jsonl"), mgr: m, hub: hub, policy: policy, gate: gate, store: store, cfg: cfg, cwd: m.cfg.Cwd, root: m.cfg.Cwd, owner: owner, studioID: a.ID, persisted: len(modelHistory), recorded: session.Recorded{Usage: usage, Delegated: delegated}, lastUsed: time.Now(), sessCtx: ctx, bgCancel: cancel, browserTool: m.browserAllowed(owner)}
	if binding, ok := m.studio.binding(s.ID); ok {
		s.studioModels = binding.allowedModels()
	}
	// The legacy text runtime predates per-Agent selections: /skill:name
	// keeps resolving against the global catalog, exactly as it always has.
	s.invocationSkills = m.Skills()
	s.browser = NewBrowserGate(gate)
	s.bg = tools.NewBgRunner(ctx, tools.BgHooks{})
	editCfg, _ := agent.ParseContextEdit(m.cfg.ContextEdit, cfg.ContextWindow)
	compactCfg, _ := agent.ParseCompactSpec(m.cfg.Compact, cfg.ContextWindow)
	instructions := "You are " + a.Name + ". Respond helpfully in the user's language. This conversation has no external tools, filesystem, or browsing access. Do not claim to have performed actions outside this conversation.\n\n" + a.Instructions
	s.agent = agent.New(agent.Config{Client: s.client(cfg), Registry: tools.NewRegistry(), SystemPrompt: instructions, MaxTurns: m.effectiveMaxTurns(), ContextEdit: editCfg, Compact: compactCfg, Gate: gate,
		ToolOutputBudget: agent.ToolOutputBudgetConfig{ContextWindow: cfg.ContextWindow, MaxOutputTokens: cfg.MaxTokens}})
	m.applyMaxTurns(s.agent)
	msgs := make([]llm.Message, 0, len(modelHistory))
	for _, message := range modelHistory {
		msgs = append(msgs, message.Message)
	}
	s.agent.SetMessages(msgs)
	s.agent.SetUsage(usage, delegated)
	hub.SetOverhead(s.agent.OverheadBreakdown())
	hub.SetClearTrigger(s.agent.ContextEditTrigger())
	return s
}

// Older text-only conversations retain their original execution boundary.
// Real Web conversations use the ordinary owned-session endpoints and policy.
func (s *Server) guardStudioSessions(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) >= 3 && parts[0] == "api" && parts[1] == "sessions" {
			if binding, bound := s.mgr.studio.binding(parts[2]); bound {
				if !s.ownsSession(r, parts[2]) {
					writeErr(w, http.StatusNotFound, ErrNotFound)
					return
				}
				if binding.runtime() == studioWebRuntime {
					next.ServeHTTP(w, r)
					return
				}
				allowed := r.Method == "GET" && (len(parts) == 3 || (len(parts) == 4 && parts[3] == "stream"))
				allowed = allowed || (r.Method == "POST" && len(parts) == 4 && parts[3] == "messages")
				if !allowed {
					writeErr(w, http.StatusForbidden, errors.New("此 Agent 使用独立对话入口"))
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}
