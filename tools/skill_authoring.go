package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"reflect"
	"strings"

	"github.com/yosukeno/pogo/skillauthor"
)

const (
	SkillSearchName   = "skill_search"
	SkillReadName     = "skill_read"
	SkillSaveName     = "skill_save"
	maxSkillReadBytes = 256 << 10
)

// SkillAuthorBackend is supplied by the host for one authenticated owner,
// Agent and session. Inputs cannot select a private store or another Agent.
// Save validates and commits an immutable document bundle before returning a
// receipt, and keeps activation at a safe session boundary.
type SkillAuthorBackend interface {
	Search(context.Context, SkillSearchRequest) (SkillSearchResult, error)
	Read(context.Context, SkillReadRequest) (SkillReadResult, error)
	Save(context.Context, skillauthor.SaveRequest) (SkillSaveResult, error)
}

type SkillSummary struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Revision    string `json:"revision"`
	Version     int    `json:"version"`
	Editable    bool   `json:"editable"`
	Enabled     bool   `json:"enabled"`
	Source      string `json:"source"`
}

type SkillSearchRequest struct {
	Query  string `json:"query,omitempty"`
	Offset int    `json:"offset,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

type SkillSearchResult struct {
	Skills  []SkillSummary `json:"skills"`
	Offset  int            `json:"offset"`
	Total   int            `json:"total"`
	HasMore bool           `json:"has_more"`
}

type SkillReadRequest struct {
	Skill    string `json:"skill"`
	Path     string `json:"path,omitempty"`
	Revision string `json:"revision,omitempty"`
}

type SkillFileInfo struct {
	Path      string `json:"path"`
	SizeBytes int    `json:"size_bytes"`
}

type SkillReadResult struct {
	Skill    SkillSummary    `json:"skill"`
	Files    []SkillFileInfo `json:"files"`
	Content  string          `json:"content"`
	Path     string          `json:"path"`
	Revision string          `json:"revision"`
}

// SkillSaveResult is a host-issued receipt shared by model and UI. Structural
// validation and persistence do not mean the authored procedure was exercised.
type SkillSaveResult struct {
	Type           string       `json:"type"`
	Action         string       `json:"action"`
	Saved          bool         `json:"saved"`
	AgentID        string       `json:"agent_id"`
	Skill          SkillSummary `json:"skill"`
	RequestID      string       `json:"request_id"`
	Reused         bool         `json:"reused"`
	Enabled        bool         `json:"enabled"`
	Pending        bool         `json:"pending"`
	BehaviorTested bool         `json:"behavior_tested"`
}

type SkillSearch struct{ Backend SkillAuthorBackend }
type SkillSave struct{ Backend SkillAuthorBackend }

func (*SkillSearch) Name() string                 { return SkillSearchName }
func (*SkillSave) Name() string                   { return SkillSaveName }
func (*SkillSearch) IsReadOnly() bool             { return true }
func (*SkillSave) IsReadOnly() bool               { return false }
func (*SkillSearch) ExecutionMode() ExecutionMode { return Parallel }
func (*SkillSave) ExecutionMode() ExecutionMode   { return Sequential }
func (*SkillSearch) Description() string {
	return "Search Skills visible to this Agent by name or description. Results are scoped metadata with opaque IDs, revision, version, source and editable/enabled state. Empty query lists bounded candidates; paginate using offset. Reading or finding a Skill never enables it or grants its tools."
}
func (*SkillSave) Description() string {
	return "Create or update one private document-only Skill for the current Agent. Submit the complete file snapshot: SKILL.md with name and description, plus optional reference documents. No executable scripts, tools.json, dependencies or permissions. For update, use the exact skill_id and expected_revision obtained from skill_read; a conflict never overwrites newer edits. Use a stable request_id only for identical retries. Omitted activate enables a new Skill or preserves an existing selection; true enables it, false saves it disabled. Selection changes for a running session wait until safe idle. A successful save proves structural validity and persistence, not behavioral testing."
}
func (*SkillSearch) InputSchema() map[string]any {
	return skillAuthorObject(nil, map[string]any{
		"query":  prop("string", "Optional name or description query, at most 1024 bytes"),
		"offset": prop("integer", "Zero-based result offset, default 0"),
		"limit":  prop("integer", "Result count, default 30, maximum 100"),
	})
}
func (*SkillSave) InputSchema() map[string]any {
	file := skillAuthorObject([]string{"path", "content"}, map[string]any{
		"path":    prop("string", "Relative document path inside the Skill; SKILL.md is required"),
		"content": prop("string", "Complete UTF-8 document contents"),
	})
	return skillAuthorObject([]string{"request_id", "files"}, map[string]any{
		"request_id":        prop("string", "1-128 characters: start with a letter/digit, then letters/digits/underscore/dot/colon/hyphen; reuse only for an identical retry"),
		"skill_id":          prop("string", "Opaque current Agent Skill ID for updates; omit to create"),
		"expected_revision": prop("string", "Exact bundle revision returned by skill_read, required for updates"),
		"files":             map[string]any{"type": "array", "items": file, "description": "Complete document bundle, not a patch; omitted old files are removed in the new version"},
		"activate":          prop("boolean", "Omit to enable a new Skill or preserve existing selection; true enables, false saves disabled; running sessions apply changes at safe idle"),
	})
}
func skillAuthorObject(required []string, props map[string]any) map[string]any {
	o := object(required, props)
	o["additionalProperties"] = false
	return o
}
func decodeSkillAuthorArgs(schema map[string]any, raw json.RawMessage, target any) error {
	if len(raw) > 2<<20 {
		return errors.New("Skill arguments exceed 2 MiB")
	}
	if err := validateAgainst(schema, raw, ""); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return errors.New("Skill arguments contain trailing JSON")
	}
	return nil
}
func skillAuthorBackendAvailable(b SkillAuthorBackend) bool {
	if b == nil {
		return false
	}
	v := reflect.ValueOf(b)
	switch v.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
		return !v.IsNil()
	}
	return true
}
func skillAuthorAvailable(ctx context.Context, b SkillAuthorBackend) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !skillAuthorBackendAvailable(b) {
		return errors.New("Skill authoring is unavailable in this session")
	}
	return nil
}
func skillOpaqueID(s string) bool {
	if len(s) < 1 || len(s) > 160 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func skillRequestID(s string) bool {
	if len(s) < 1 || len(s) > 128 {
		return false
	}
	for i, c := range s {
		letterDigit := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
		if !letterDigit && (i == 0 || c != '_' && c != '.' && c != ':' && c != '-') {
			return false
		}
	}
	return true
}

func skillRevision(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'f' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

func authoredSkillID(s string) bool {
	if !strings.HasPrefix(s, "as_") || len(s) != 35 {
		return false
	}
	for _, c := range s[3:] {
		if !(c >= 'a' && c <= 'f' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}
func skillRelativePath(s string) bool {
	if s == "" || len(s) > 1024 || strings.ContainsAny(s, "\\:\x00") || strings.HasPrefix(s, "/") || path.Clean(s) != s {
		return false
	}
	for _, part := range strings.Split(s, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}
func validSkillSummary(s SkillSummary) bool {
	return skillOpaqueID(s.ID) && strings.TrimSpace(s.Name) != "" && len(s.Name) <= 128 && len(s.Description) <= 4096 && len(s.Revision) <= 256 && s.Version >= 0 && len(s.Source) <= 64 && !strings.ContainsAny(s.Source, "/\\\x00")
}
func skillAuthorResult(v any, details any) (Result, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return Result{}, err
	}
	if len(raw) > 384<<10 {
		return Result{}, errors.New("Skill response exceeds the bounded document view")
	}
	return Result{Text: string(raw), Details: details}, nil
}
func (t *SkillSearch) Execute(ctx context.Context, raw json.RawMessage) (Result, error) {
	if err := skillAuthorAvailable(ctx, t.Backend); err != nil {
		return Result{}, err
	}
	var in SkillSearchRequest
	if err := decodeSkillAuthorArgs(t.InputSchema(), raw, &in); err != nil {
		return Result{}, err
	}
	if len(in.Query) > 1024 || in.Offset < 0 || in.Offset > 10000 || in.Limit < 0 || in.Limit > 100 {
		return Result{}, errors.New("invalid Skill search query, offset or limit")
	}
	if in.Limit == 0 {
		in.Limit = 30
	}
	out, err := t.Backend.Search(ctx, in)
	if err != nil {
		return Result{}, err
	}
	if out.Offset != in.Offset || out.Total < 0 || len(out.Skills) > in.Limit || out.Total < len(out.Skills) {
		return Result{}, errors.New("Skill host returned invalid search bounds")
	}
	for _, s := range out.Skills {
		if !validSkillSummary(s) {
			return Result{}, errors.New("Skill host returned invalid scoped metadata")
		}
	}
	if out.Skills == nil {
		out.Skills = []SkillSummary{}
	}
	return skillAuthorResult(out, nil)
}
func (t *SkillSave) Execute(ctx context.Context, raw json.RawMessage) (Result, error) {
	if err := skillAuthorAvailable(ctx, t.Backend); err != nil {
		return Result{}, err
	}
	var in skillauthor.SaveRequest
	if err := decodeSkillAuthorArgs(t.InputSchema(), raw, &in); err != nil {
		return Result{}, err
	}
	if !skillRequestID(in.RequestID) || in.SkillID != "" && (!authoredSkillID(in.SkillID) || !skillRevision(in.ExpectedRevision)) || in.SkillID == "" && in.ExpectedRevision != "" {
		return Result{}, errors.New("invalid Skill request ID or update revision")
	}
	name, description, err := skillauthor.ValidateFiles(in.Files)
	if err != nil {
		return Result{}, err
	}
	out, err := t.Backend.Save(ctx, in)
	if err != nil {
		return Result{}, err
	}
	if out.Type != "skill_authoring" || out.Action != "save" || !out.Saved || !skillOpaqueID(out.AgentID) || out.RequestID != in.RequestID || !validSkillSummary(out.Skill) || out.Skill.Source != "authored" || !authoredSkillID(out.Skill.ID) || !out.Skill.Editable || !skillRevision(out.Skill.Revision) || out.Skill.Version < 1 || out.Skill.Name != name || out.Skill.Description != description || out.Enabled != out.Skill.Enabled || out.BehaviorTested || in.SkillID != "" && out.Skill.ID != in.SkillID {
		return Result{}, errors.New("Skill host returned an invalid save receipt")
	}
	return skillAuthorResult(out, out)
}

func readAuthoredSkill(ctx context.Context, b SkillAuthorBackend, in SkillReadRequest) (Result, error) {
	if err := skillAuthorAvailable(ctx, b); err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(in.Skill) == "" || len(in.Skill) > 160 || !skillRelativePath(in.Path) || len(in.Revision) > 256 {
		return Result{}, errors.New("invalid Skill name, document path or revision")
	}
	out, err := b.Read(ctx, in)
	if err != nil {
		return Result{}, err
	}
	if !validSkillSummary(out.Skill) || out.Path != in.Path || !skillRelativePath(out.Path) || len(out.Content) > maxSkillReadBytes || len(out.Files) > 512 || out.Revision != out.Skill.Revision || in.Revision != "" && out.Revision != in.Revision {
		return Result{}, errors.New("Skill host returned an invalid document view")
	}
	for _, f := range out.Files {
		if !skillRelativePath(f.Path) || f.SizeBytes < 0 {
			return Result{}, fmt.Errorf("Skill host returned an invalid document listing")
		}
	}
	if out.Files == nil {
		out.Files = []SkillFileInfo{}
	}
	return skillAuthorResult(out, nil)
}
