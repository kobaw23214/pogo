package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/yosukeno/pogo/skillobserve"
	"github.com/yosukeno/pogo/skillruntime"
)

// RuntimeTool exposes a host-resolved capability without command or path inputs.
type RuntimeTool struct {
	Session    *skillruntime.BoundSession
	Descriptor skillruntime.Descriptor
	// Identity binds the owning bundle's observation identity, resolved by the
	// trusted registration path. A zero Identity records nothing.
	Identity skillobserve.Identity
}

// RuntimeSkillRead provides progressive access to bundled reference documents
// without giving ordinary filesystem tools access to the private Skill store.
type RuntimeSkillRead struct {
	Session *skillruntime.BoundSession
	// Backend is the host's scoped document view. It includes any selected
	// runtime bundle through the same authorization path, without a second
	// tool named skill_read or private filesystem paths.
	Backend SkillAuthorBackend
}

func (t *RuntimeSkillRead) Name() string { return SkillReadName }
func (t *RuntimeSkillRead) Description() string {
	return "Read SKILL.md or a bundled document from a Skill available to this Agent. Use its scoped ID or authorized name, a relative path (default SKILL.md), and optionally an exact bundle revision. The default view respects the conversation's selected pinned version. Before editing, use skill_search and read the latest returned revision explicitly; inspect files and editable state. Reading does not enable the Skill or grant its tools."
}
func (t *RuntimeSkillRead) InputSchema() map[string]any {
	return skillAuthorObject([]string{"skill"}, map[string]any{"skill": prop("string", "Scoped Skill ID or authorized name"), "path": prop("string", "Relative document path; defaults to SKILL.md"), "revision": prop("string", "Optional exact immutable bundle revision")})
}
func (t *RuntimeSkillRead) ExecutionMode() ExecutionMode { return Sequential }
func (t *RuntimeSkillRead) IsReadOnly() bool             { return true }
func (t *RuntimeSkillRead) Execute(ctx context.Context, args json.RawMessage) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	var input SkillReadRequest
	if err := decodeSkillAuthorArgs(t.InputSchema(), args, &input); err != nil {
		return Result{}, err
	}
	if input.Path == "" {
		input.Path = "SKILL.md"
	}
	if skillAuthorBackendAvailable(t.Backend) {
		return readAuthoredSkill(ctx, t.Backend, input)
	}
	if t.Session == nil {
		return Result{}, errors.New("Skill document access is unavailable in this session")
	}
	if input.Revision != "" {
		return Result{}, errors.New("exact Skill revision reads require a host-bound document view")
	}
	body, truncated, err := t.Session.ReadDocument(input.Skill, input.Path)
	if err != nil {
		return Result{}, err
	}
	text := string(body)
	if truncated {
		text += "\n[Document truncated at 256 KiB]"
	}
	if len(body) == 0 {
		text = fmt.Sprintf("%s is empty", input.Path)
	}
	return Result{Text: text}, nil
}

func (t *RuntimeTool) Name() string                 { return t.Descriptor.Name }
func (t *RuntimeTool) Description() string          { return t.Descriptor.Description }
func (t *RuntimeTool) InputSchema() map[string]any  { return t.Descriptor.InputSchema }
func (t *RuntimeTool) ExecutionMode() ExecutionMode { return Sequential }
func (t *RuntimeTool) IsReadOnly() bool             { return t.Descriptor.ReadOnly }
func (t *RuntimeTool) Execute(ctx context.Context, args json.RawMessage) (res Result, err error) {
	// One observation span per call that entered Execute, the same contract as
	// SynthTool's: only the outcome category is recorded — never the arguments,
	// the output or the error text. The contract ID is the tool's honest
	// identity; the wire name is the descriptor's in every channel (the MCP
	// surface registers it unchanged), so there is no wire-name swap here.
	_, span := skillobserve.Start(ctx, t.Identity, "execute", "runtime", t.Descriptor.ID, t.Descriptor.Name)
	defer func() {
		if r := recover(); r != nil {
			span.End("error", "internal_error")
			panic(r)
		}
		status, kind := "ok", ""
		if err != nil {
			status, kind = "error", "execution_error"
			switch {
			case errors.Is(ctx.Err(), context.Canceled):
				status, kind = "cancelled", "cancelled"
			case errors.Is(ctx.Err(), context.DeadlineExceeded):
				status, kind = "timeout", "timeout"
			}
		}
		span.End(status, kind)
	}()
	result, err := t.Session.Call(ctx, t.Descriptor.ID, args)
	if err != nil {
		// Like MCPDetails on a failed MCP call: the capability identity still
		// reaches the interface, so the failed call renders as a skill card
		// with its arguments rather than as a bare error line.
		return Result{Details: skillruntime.CallResult{Capability: t.Descriptor.ID}}, err
	}
	encoded, err := json.Marshal(result)
	return Result{Text: string(encoded), Details: result}, err
}
