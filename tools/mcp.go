package tools

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yosukeno/pogo/mcpobserve"
)

// This file is the client half of pogo's MCP support: tools that live on an
// external server, wrapped to satisfy the same Tool contract the built-ins
// and the manifest tools do. The wrapper exists so that everything the loop
// already does for a tool — schema validation, the approval gate, truncation
// and spill, the wire events — it does here too, unchanged. The protocol
// session behind the wrapper is the mcpclient pool's business; this file
// only knows how to call it through MCPCaller.
//
// The annotations a server sends (readOnlyHint, idempotentHint) are
// untrusted hints per the MCP spec. They are used here for scheduling only —
// whether siblings may overlap the call — never to waive the approval gate:
// the gate's trust decision is the operator's "trust" flag on the server
// entry, which is the only party who can make it.

// MCPCaller performs one tools/call against the server behind a tool. The
// pool injects a closure for the definition snapshot the wrapper was built
// from; it — not this struct — decides which session is current and enforces
// that the session is compatible with that snapshot before calling it. A
// mismatch or nil-session answer ("reconnecting") is a normal error here, not
// a panic path.
type MCPCaller func(ctx context.Context, toolName string, args json.RawMessage) (*mcp.CallToolResult, error)

// MCPDetails accompanies an MCP tool call. Same contract as SynthDetails:
// populated even when the call fails, with the same json names where the
// question is the same (duration_ms, timed_out, truncated,
// full_output_path), so an interface needs one way to ask it.
type MCPDetails struct {
	Server     string `json:"server"`
	Tool       string `json:"tool"`
	DurationMS int64  `json:"duration_ms"`
	TimedOut   bool   `json:"timed_out,omitempty"`
	Truncated  bool   `json:"truncated,omitempty"`
	// FullOutputPath names the file holding the whole output when it did not
	// fit — same field and same json name as BashDetails/SynthDetails.
	FullOutputPath string `json:"full_output_path,omitempty"`
	// StructuredContent is the call's structured result, verbatim, for
	// interfaces to render. The model-facing copy is folded into Text — see
	// Execute for why both must exist.
	StructuredContent any `json:"structured_content,omitempty"`
	// Title, Description, Annotations and InputSchema echo the declaration's
	// display metadata on every call, so an interface renders the card without
	// a second lookup. They are server-provided text: shown, never acted on.
	Title       string          `json:"title,omitempty"`
	Description string          `json:"description,omitempty"`
	Annotations *MCPAnnotations `json:"annotations,omitempty"`
	InputSchema map[string]any  `json:"input_schema,omitempty"`
	// Content is the call's typed content blocks — the structured copy of what
	// Text flattens into placeholders, so an interface can render an image
	// instead of its one-line stand-in. Absent on the failure paths, where no
	// result came back.
	Content []MCPContentBlock `json:"content,omitempty"`
}

// MCPAnnotations is the display copy of the server's tool annotations. Per
// the file comment they stay untrusted hints: an interface may badge them,
// the gate never reads them.
type MCPAnnotations struct {
	ReadOnlyHint    bool `json:"read_only_hint,omitempty"`
	DestructiveHint bool `json:"destructive_hint,omitempty"`
	IdempotentHint  bool `json:"idempotent_hint,omitempty"`
	OpenWorldHint   bool `json:"open_world_hint,omitempty"`
}

// MCPContentBlock is one content block of a call's result, kept typed for
// interfaces. Data travels inline only up to maxMCPInlineData; past it the
// block is metadata plus Omitted, so a megabyte of base64 never enters the
// event stream or the session file.
type MCPContentBlock struct {
	Type     string `json:"type"` // text | image | audio | resource | resource_link | unknown
	Text     string `json:"text,omitempty"`
	MIMEType string `json:"mime_type,omitempty"`
	Data     string `json:"data,omitempty"` // base64, image/audio only
	URI      string `json:"uri,omitempty"`
	Name     string `json:"name,omitempty"`
	Size     int    `json:"size,omitempty"`
	Omitted  bool   `json:"omitted,omitempty"`
}

// maxMCPInlineData caps one block's base64 payload in details.
const maxMCPInlineData = 512 * 1024

// MCPTool is one tool on one external MCP server.
type MCPTool struct {
	// SpillDir holds output that did not fit in the result. Set by New, which
	// also makes it readable; empty means DefaultSpillDir. Same
	// caller-builds-session-assigns split as SynthTool.
	SpillDir string

	// Transport names the wire the server behind this tool is reached over
	// ("streamable", "sse", "stdio") for observability records. Optional and
	// informational only: empty means unknown, and nothing in execution reads
	// it. Set via WithTransport by whoever knows the server's transport; the
	// pool does not report one, so wrappers it builds leave it empty rather
	// than guess.
	Transport string

	server       string
	toolName     string // the name the server knows
	wireName     string
	title        string
	desc         string
	catalogGroup string
	catalogRole  string
	// fullDesc is the description exactly as the server declared it, before
	// the model-facing truncation desc carries. Retained so mcp_describe can
	// show the whole contract; never sent as a tool declaration.
	fullDesc string
	// serverInstructions is the InitializeResult.Instructions snapshot of the
	// session this wrapper was built from, frozen with the rest of the
	// definition. It is untrusted server prose: shown by mcp_describe, never
	// promoted to system instructions or executed.
	serverInstructions string
	schema             map[string]any
	output             map[string]any
	annot              *mcp.ToolAnnotations
	fingerprint        string
	readOnly           bool
	idempotent         bool
	timeout            time.Duration
	caller             MCPCaller
	// Registry-managed calls defer previewing and persistence to the shared
	// result boundary. Direct callers keep the historical bounded-text contract.
	managedResults bool
}

// DefaultMCPToolTimeout is the per-call budget when a tool is constructed
// without one. Matches DefaultSynthTimeout, for the same reason: an
// unattended agent needs a ceiling more than it needs the flexibility.
const DefaultMCPToolTimeout = 120 * time.Second

// wireHashLength is how much of a long name survives truncation; the hash
// tail keeps the remaining entropy. 55 + 1 + 8 = MaxWireNameLength.
const wireKeepLength = 55

// WireNameFor maps a server and tool name to the function name the provider
// APIs see: "mcp_<server>_<tool>", sanitized to the wire alphabet, and — when
// that would exceed MaxWireNameLength — truncated with an fnv32a hash of the
// full sanitized name as the suffix, so the mapping stays deterministic and
// two tools that share a 55-character prefix still get distinct names.
// Exported because the web layer renders the same names for audit and must
// not re-derive them.
//
// Sanitizing rather than refusing: server names are already validated by
// mcpclient, but tool names are whatever the server sent, and an MCP Tool
// name may contain characters ('.', '/', unicode) the wire alphabet forbids.
// Claude Code has shipped real API errors on over-long and over-wide MCP
// names (anthropics/claude-code #20830); the mapping is decided here, once,
// not discovered per call.
func WireNameFor(server, tool string) string {
	raw := "mcp_" + server + "_" + tool
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			return r
		default:
			return '_'
		}
	}, raw)
	if len(safe) <= MaxWireNameLength {
		return safe
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(safe)) // hash of the *full* name, before truncation
	return safe[:wireKeepLength] + "_" + fmt.Sprintf("%08x", h.Sum32())
}

// firstHiddenRune is the remote-server counterpart of manifest.go's
// firstInvisible. The in-repo rule refuses every control character because an
// in-repo description has no business typesetting itself; a remote server's
// prose is different — it is written for display in the server's own UI, and
// "\n", "\r" and "\t" in it are visible layout whitespace, not concealment.
// Refusing them silently dropped every multi-line tool a real server
// declared (the pool only logs the skip), while probing the same server
// listed them all. Everything firstInvisible exists to catch stays refused:
// the rest of the control range and the whole Cf class — zero-widths, bidi
// overrides — are hidden text in model input, which is the attack.
func firstHiddenRune(s string) (bool, rune) {
	for _, r := range s {
		if r == '\n' || r == '\r' || r == '\t' {
			continue
		}
		if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
			return true, r
		}
	}
	return false, 0
}

// truncatedMark is appended to a description that did not fit, so the model
// (and a human reading the declaration) can tell the text was cut rather than
// ending mid-thought.
const truncatedMark = "…[truncated]"

// MCPCatalogMetaKey names the optional _meta key a server may use to publish
// catalog hints for layered discovery. The value must be a JSON object with a
// optional string "group" and a "role" of MCPCatalogRoleDirectory or
// MCPCatalogRoleOperation; missing or malformed fields are ignored, never
// fatal. These are display/navigation hints only: they carry no execution or
// approval meaning, and a directory role never auto-trusts or auto-executes.
const MCPCatalogMetaKey = "io.pogo/catalog"

const (
	// MCPCatalogRoleDirectory marks a tool that groups or points at other
	// tools — navigation, not work.
	MCPCatalogRoleDirectory = "directory"
	// MCPCatalogRoleOperation marks a tool that does the work itself.
	MCPCatalogRoleOperation = "operation"
)

// mcpCatalogHint extracts the safe public catalog hints from a tool's _meta.
// Only this one key and only these two string fields are read; arbitrary meta
// is never copied onto the wrapper. Allowed fields are still untrusted
// server-provided text, just like titles and descriptions.
func mcpCatalogHint(meta map[string]any) (group, role string) {
	if meta == nil {
		return "", ""
	}
	raw, ok := meta[MCPCatalogMetaKey]
	if !ok {
		return "", ""
	}
	obj, ok := raw.(map[string]any)
	if !ok {
		return "", ""
	}
	if g, ok := obj["group"].(string); ok {
		group = g
	}
	if r, ok := obj["role"].(string); ok &&
		(r == MCPCatalogRoleDirectory || r == MCPCatalogRoleOperation) {
		role = r
	}
	return group, role
}

// NewMCPTool validates and wraps one server-declared tool. The checks mirror
// the manifest loader's for the same reason: descriptions and names are model
// input, and a bad one costs a session a wasted turn or worse. A tool that
// fails here is skipped by the pool with a diagnostic — one bad entry must
// not take the server's other tools down with it.
func NewMCPTool(server string, spec *mcp.Tool, timeout time.Duration, caller MCPCaller) (*MCPTool, error) {
	if spec == nil || caller == nil {
		return nil, fmt.Errorf("server %q: tool spec and caller are required", server)
	}
	// encoding/json is the wire's canonicalizer: it sorts map keys, so two
	// representations of the same JSON object agree even when Go maps iterate
	// in different orders. Marshal the incoming value itself (not credentials,
	// configuration, or the caller), then freeze the hash. Later mutation of
	// spec cannot rewrite the identity of a wrapper already handed out.
	definition, err := json.Marshal(spec)
	if err != nil {
		return nil, fmt.Errorf("server %q tool %q: definition is not JSON-serializable: %w", server, spec.Name, err)
	}
	// Remarshal into a fresh Tool as the actual clone. In particular, this
	// replaces the caller's nested schema maps and annotation bool pointers
	// with privately owned JSON values. Numbers follow normal JSON decoding,
	// exactly as they did when the SDK decoded the server response.
	var frozen mcp.Tool
	if err := json.Unmarshal(definition, &frozen); err != nil {
		return nil, fmt.Errorf("server %q tool %q: definition is not valid JSON: %w", server, spec.Name, err)
	}
	// The SDK delivers InputSchema as `any`; in practice it is the decoded
	// JSON object. Anything else is not a schema we can serve, and serving a
	// garbled one is worse than serving none — validation happens against it.
	var schema map[string]any
	if frozen.InputSchema == nil {
		// The empty-object schema goes through object(), not a bare literal:
		// moonshot/kimi reject "required":null, which a hand-written map with
		// a nil properties or required entry would marshal to — the bug
		// object()'s comment exists to document.
		schema = object(nil, map[string]any{})
	} else {
		var ok bool
		schema, ok = frozen.InputSchema.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("server %q tool %q: inputSchema is %T, not a JSON object", server, spec.Name, frozen.InputSchema)
		}
	}
	var output map[string]any
	if spec.OutputSchema != nil {
		if frozen.OutputSchema == nil {
			return nil, fmt.Errorf("server %q tool %q: outputSchema is null, not a JSON object", server, spec.Name)
		}
		var ok bool
		output, ok = frozen.OutputSchema.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("server %q tool %q: outputSchema is %T, not a JSON object", server, spec.Name, frozen.OutputSchema)
		}
	}

	if bad, ch := firstInvisible(frozen.Name); bad {
		return nil, fmt.Errorf("server %q tool name contains an invisible or control character (U+%04X)", server, ch)
	}
	desc := frozen.Description
	if bad, ch := firstHiddenRune(desc); bad {
		// Rejected rather than stripped, same as manifest.go's rule: hidden
		// text in model input is how tool-poisoning payloads hide, and a
		// remote server is exactly the place to expect one.
		return nil, fmt.Errorf("server %q tool %q: description contains an invisible or control character (U+%04X)", server, spec.Name, ch)
	}
	if utf8.RuneCountInString(desc) > MaxSynthDescriptionLength {
		// Truncated with a visible mark rather than rejected: over-long is
		// sloppy, not hostile, and the whole server should not lose a tool
		// over it. The limit still holds — the mark fits inside it.
		runes := []rune(desc)
		keep := MaxSynthDescriptionLength - utf8.RuneCountInString(truncatedMark)
		desc = string(runes[:keep]) + truncatedMark
	}

	// The guard is for direct construction, like SynthTool's: zero would
	// mean "expire before starting", not "no limit".
	if timeout <= 0 {
		timeout = DefaultMCPToolTimeout
	}

	readOnly, idempotent := false, false
	if an := frozen.Annotations; an != nil {
		readOnly, idempotent = an.ReadOnlyHint, an.IdempotentHint
	}
	group, role := mcpCatalogHint(frozen.Meta)
	// Display-name precedence per the spec: the tool's own title, then the
	// annotations' — the name is always there as details.Tool either way.
	title := frozen.Title
	if title == "" && frozen.Annotations != nil {
		title = frozen.Annotations.Title
	}
	return &MCPTool{
		server: server, toolName: frozen.Name, wireName: WireNameFor(server, frozen.Name),
		title: title, desc: desc, fullDesc: frozen.Description,
		catalogGroup: group, catalogRole: role,
		schema: schema, output: output, annot: frozen.Annotations,
		fingerprint: fmt.Sprintf("%x", sha256.Sum256(definition)), readOnly: readOnly, idempotent: idempotent,
		timeout: timeout, caller: caller,
	}, nil
}

func (t *MCPTool) Name() string        { return t.wireName }
func (t *MCPTool) Description() string { return t.desc }

// InputSchema returns a detached JSON-schema object. Mutating the result (or
// a nested map or slice in it) must not turn a shared pool declaration into a
// different tool, so callers get their own copy on every call.
func (t *MCPTool) InputSchema() map[string]any { return detachedJSONMap(t.schema) }

// OutputSchema returns a detached copy of the server's output schema, or nil
// when the declaration had none. Unlike Description, it is not model-facing
// text and is therefore retained without truncation.
func (t *MCPTool) OutputSchema() map[string]any { return detachedJSONMap(t.output) }

// Annotations returns a detached copy of all SDK-supported annotations. The
// scheduling code keeps only the two execution hints; the fingerprint keeps
// every field so display or safety metadata cannot change invisibly.
func (t *MCPTool) Annotations() *mcp.ToolAnnotations {
	if t.annot == nil {
		return nil
	}
	clone := *t.annot
	if t.annot.DestructiveHint != nil {
		v := *t.annot.DestructiveHint
		clone.DestructiveHint = &v
	}
	if t.annot.OpenWorldHint != nil {
		v := *t.annot.OpenWorldHint
		clone.OpenWorldHint = &v
	}
	return &clone
}

// DefinitionFingerprint identifies the complete incoming mcp.Tool: name,
// full description before model-facing truncation, both schemas, annotations,
// title, icons, and _meta. Connection configuration, credentials, and the
// caller deliberately do not participate: changes there do not alter the
// declaration's semantics.
func (t *MCPTool) DefinitionFingerprint() string { return t.fingerprint }

// WithSpillDir returns a session-local shallow copy. The copied wrapper is
// safe because every declaration accessor detaches its data and no method
// mutates the shared definition state.
func (t *MCPTool) WithSpillDir(dir string) *MCPTool {
	clone := *t
	clone.SpillDir = dir
	return &clone
}

// withManagedResults returns a wrapper whose complete result is captured by
// the registry's result manager before a model-facing preview is selected.
// The flag belongs to the clone, so catalog/direct callers retain their
// existing truncation behavior.
func (t *MCPTool) withManagedResults() *MCPTool {
	clone := *t
	clone.managedResults = true
	return &clone
}

// WithServerInstructions returns a copy carrying the connected session's
// InitializeResult.Instructions. The pool calls it at catalog construction,
// so the snapshot of instructions freezes with the snapshot of tools that
// belongs to the same session. The same shallow-copy safety argument as
// WithSpillDir applies; a clone still satisfies the pool's pointer-identity
// binding as long as the clone is the pointer the catalog stores.
func (t *MCPTool) WithServerInstructions(instructions string) *MCPTool {
	clone := *t
	clone.serverInstructions = instructions
	return &clone
}

// WithTransport returns a copy carrying the transport name for observability
// records. Same shallow-copy safety argument as WithSpillDir. Unknown values
// are passed through as-is — the field is a label, not a decision input — but
// callers that cannot name the transport reliably should leave it empty.
func (t *MCPTool) WithTransport(transport string) *MCPTool {
	clone := *t
	clone.Transport = transport
	return &clone
}

// FullDescription is the server-declared description before model-facing
// truncation. Like everything else on the wrapper it is untrusted display
// text, and it stays off the tool declaration either way.
func (t *MCPTool) FullDescription() string { return t.fullDesc }

// ServerInstructions is the frozen instructions snapshot, "" when the
// server's handshake carried none. Untrusted external data — see the field.
func (t *MCPTool) ServerInstructions() string { return t.serverInstructions }

// ServerName and ServerToolName expose the provenance pair for inspection
// and audit output. They are metadata, not a call path: Execute still goes
// through the bound caller.
func (t *MCPTool) ServerName() string     { return t.server }
func (t *MCPTool) ServerToolName() string { return t.toolName }

// DisplayTitle is the title shown next to the tool (its own, or the
// annotations'), "" when the server declared none.
func (t *MCPTool) DisplayTitle() string { return t.title }

// CatalogGroup and CatalogRole are the catalog hints the server declared
// under MCPCatalogMetaKey, "" when absent or malformed. They are untrusted
// navigation hints for layered discovery: a directory role is a browsing
// affordance only — it never waives validation, the gate, or any other
// execution rule.
func (t *MCPTool) CatalogGroup() string { return t.catalogGroup }
func (t *MCPTool) CatalogRole() string  { return t.catalogRole }

// detachedJSONMap copies the JSON values NewMCPTool froze from the wire. The
// default nil-input schema also contains []string, so preserve that concrete
// type alongside the types produced by json.Unmarshal; all other leaf values
// are immutable JSON scalars.
func detachedJSONMap(source map[string]any) map[string]any {
	if source == nil {
		return nil
	}
	out := make(map[string]any, len(source))
	for key, value := range source {
		out[key] = detachedJSONValue(value)
	}
	return out
}

func detachedJSONValue(value any) any {
	switch value := value.(type) {
	case map[string]any:
		return detachedJSONMap(value)
	case []any:
		out := make([]any, len(value))
		for i, item := range value {
			out[i] = detachedJSONValue(item)
		}
		return out
	case []string:
		out := make([]string, len(value))
		copy(out, value)
		return out
	case json.RawMessage:
		return append(json.RawMessage(nil), value...)
	default:
		return value
	}
}

// IsReadOnly reports the server's readOnlyHint. Honoured as a scheduling and
// session-filtering hint (ReadOnlyCapable), not as a trust grant — see the
// file comment.
func (t *MCPTool) IsReadOnly() bool { return t.readOnly }

// ExecutionMode is Parallel only for read-only idempotent tools — SynthTool's
// rule, applied to a declaration made by a remote server rather than a bundle
// author, which if anything argues for less trust, not more.
func (t *MCPTool) ExecutionMode() ExecutionMode {
	if t.readOnly && t.idempotent {
		return Parallel
	}
	return Sequential
}

func (t *MCPTool) Execute(ctx context.Context, raw json.RawMessage) (Result, error) {
	// One outbound observation per execution: metadata only (server, tool,
	// wire name, outcome), never arguments or output. With no recorder on the
	// context this costs one cheap no-op span and changes nothing.
	ctx, span := mcpobserve.Start(ctx, mcpobserve.Call{
		Direction: "outbound",
		Server:    t.server,
		Tool:      t.toolName,
		WireTool:  t.wireName,
		Transport: t.Transport,
	})
	defer span.End("interrupted", "unknown_outcome")
	runCtx, cancel := context.WithTimeout(ctx, t.timeout)
	defer cancel()

	started := time.Now()
	res, err := t.caller(runCtx, t.toolName, raw)
	details := MCPDetails{
		Server: t.server, Tool: t.toolName,
		DurationMS: time.Since(started).Milliseconds(),
		Title:      t.title, Description: t.desc,
		Annotations: mcpAnnotationsOf(t.annot),
		// Detached like the accessor's copy: details outlive the call, and a
		// shared map would let one session's renderer mutate the pool's
		// declaration for every other session.
		InputSchema: detachedJSONMap(t.schema),
	}

	if err != nil {
		// Abort and timeout are named for the model. Transport/protocol
		// failures retain their full error text as result data, separately
		// from the short error value that marks the call as failed.
		switch {
		case ctx.Err() != nil:
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				span.EndDiagnostic("timeout", "timeout", mcpobserve.DiagnoseError(ctx.Err(), nil))
			} else {
				span.EndDiagnostic("cancelled", "cancelled", mcpobserve.DiagnoseError(ctx.Err(), nil))
			}
			return t.resultWithPayload("Tool call aborted", details, nil), errors.New("Tool call aborted")
		case errors.Is(runCtx.Err(), context.DeadlineExceeded):
			details.TimedOut = true
			span.EndDiagnostic("timeout", "timeout", mcpobserve.ExplicitDiagnostic("deadline_exceeded", "Tool call timed out after %s", t.timeout))
			message := fmt.Sprintf("Tool call timed out after %s", t.timeout)
			return t.resultWithPayload(message, details, nil), errors.New(message)
		default:
			diagnostic := mcpobserve.DiagnoseError(err, raw)
			var rpc *jsonrpc.Error
			switch {
			case errors.Is(err, context.DeadlineExceeded):
				span.EndDiagnostic("timeout", "timeout", diagnostic)
			case errors.Is(err, context.Canceled):
				span.EndDiagnostic("cancelled", "cancelled", diagnostic)
			case errors.As(err, &rpc):
				span.EndDiagnostic("error", "protocol_error", diagnostic)
			default:
				span.EndDiagnostic("error", "transport_error", diagnostic)
			}
			message := diagnostic.Summary
			if message == "" {
				message = "MCP call failed"
			}
			result := t.resultWithPayload(err.Error(), details, nil)
			if !t.managedResults || err == context.Canceled || err == context.DeadlineExceeded {
				// Direct callers retain their error contract. Standard context
				// sentinels are already bounded and retain identity in managed mode.
				return result, err
			}
			return result, &mcpExecutionError{message: message, cause: err}
		}
	}
	if res == nil {
		// A nil result with a nil error is a protocol violation by the caller
		// (the pool's contract is exactly one non-nil). It must not read as a
		// success — and dereferencing it below would panic.
		span.EndDiagnostic("error", "protocol_error", mcpobserve.ExplicitDiagnostic("empty_result", "MCP server returned no result"))
		return t.resultWithPayload("MCP server returned no result", details, nil), errors.New("MCP server returned no result")
	}

	text := renderMCPContent(res, &details)
	if text == "" {
		text = "(no output)"
	}
	payload := &ResultPayload{Text: text, Format: "text"}
	if res.StructuredContent != nil {
		if data, marshalErr := json.Marshal(res.StructuredContent); marshalErr == nil {
			payload.Structured = data
			payload.Format = "json"
		} else {
			// renderMCPContent already tells the model about this failure.
			// Do not label a text fallback as the complete structured result.
			payload.Incomplete = true
		}
	}
	if data, marshalErr := json.Marshal(res); marshalErr == nil {
		payload.Native = data
	} else {
		payload.Incomplete = true
		text += "\n\n[The native MCP result could not be serialized; original protocol content was not preserved.]"
		payload.Text = text
	}
	out := t.resultWithPayload(text, details, payload)

	if res.IsError {
		// The tool itself reported the failure. The text still goes to the
		// model (it is usually the explanation), and the error marks the
		// result is_error — both halves of the contract, same as bash.
		span.EndDiagnostic("error", "tool_error", mcpobserve.DiagnoseResult(res, raw))
		msg := mcpobserve.DiagnoseResult(res, raw).Summary
		if msg == "" {
			msg = "tool reported error"
		}
		return out, errors.New(msg)
	}
	span.End("ok", "")
	return out, nil
}

// mcpExecutionError keeps errors.Is/As useful for transport callers without
// allowing a server's potentially huge response body into Error(). Full
// diagnostic text remains in Result.Payload, subject to the result boundary.
type mcpExecutionError struct {
	message string
	cause   error
}

func (e *mcpExecutionError) Error() string { return e.message }
func (e *mcpExecutionError) Unwrap() error { return e.cause }

func (t *MCPTool) resultWithPayload(text string, details MCPDetails, payload *ResultPayload) Result {
	if payload == nil {
		payload = &ResultPayload{Text: text, Format: "text"}
	}
	if !t.managedResults {
		k := KeepTail(t.SpillDir, t.wireName, text)
		details.Truncated = k.Truncated
		details.FullOutputPath = k.Path
		text = k.Text()
	}
	return Result{Text: text, Details: details, Payload: payload}
}

// renderMCPContent flattens a call result into the text the model reads.
// Text blocks and embedded resource text join in order. Resource links retain
// their URI and name; binary blocks carry metadata rather than base64. The
// original protocol response is separately captured in Result.Payload.Native.
//
// StructuredContent is appended as JSON rather than left in Details only.
// kimi-code#2554 is the cautionary tale: a result the server returned
// structured-only (no text blocks) reached the model as an empty string, and
// the model concluded the tool had nothing to say — the data was in Details,
// which the model never reads. Details keeps the verbatim copy for
// interfaces; Text keeps the model from losing it.
func renderMCPContent(res *mcp.CallToolResult, details *MCPDetails) string {
	var blocks []string
	var textBlocks []string
	for _, c := range res.Content {
		switch b := c.(type) {
		case *mcp.TextContent:
			blocks = append(blocks, b.Text)
			textBlocks = append(textBlocks, b.Text)
			details.Content = append(details.Content, MCPContentBlock{Type: "text", Text: b.Text})
		case *mcp.ImageContent:
			blocks = append(blocks, omittedContent(mimeOr(b.MIMEType, "image"), len(b.Data)))
			details.Content = append(details.Content, inlineDataBlock("image", b.MIMEType, b.Data))
		case *mcp.AudioContent:
			blocks = append(blocks, omittedContent(mimeOr(b.MIMEType, "audio"), len(b.Data)))
			details.Content = append(details.Content, inlineDataBlock("audio", b.MIMEType, b.Data))
		case *mcp.EmbeddedResource:
			block := MCPContentBlock{Type: "resource"}
			if b.Resource != nil {
				block.URI = b.Resource.URI
				block.MIMEType = b.Resource.MIMEType
				block.Size = len(b.Resource.Blob) + len(b.Resource.Text)
			}
			// Keep the existing UI details shape without duplicating a large
			// resource body in events. Text resources are model-readable source
			// material, though, and must reach the shared preview boundary.
			label := fmt.Sprintf("[embedded resource: uri=%q, mime_type=%q, %d bytes]", block.URI, mimeOr(block.MIMEType, "resource"), block.Size)
			if b.Resource != nil && (b.Resource.Text != "" || b.Resource.Blob == nil) {
				blocks = append(blocks, label+"\n"+b.Resource.Text)
			} else {
				blocks = append(blocks, label+"\nBinary body omitted from this text view; use the saved native result or an MCP resource reader, when available.")
			}
			details.Content = append(details.Content, block)
		case *mcp.ResourceLink:
			block := MCPContentBlock{Type: "resource_link", URI: b.URI, Name: b.Name, MIMEType: b.MIMEType}
			if b.Size != nil {
				block.Size = int(*b.Size)
			}
			blocks = append(blocks, fmt.Sprintf("[resource link: name=%q, uri=%q, mime_type=%q, %d bytes; read with an MCP resource reader when supported]", b.Name, b.URI, mimeOr(b.MIMEType, "resource-link"), block.Size))
			details.Content = append(details.Content, block)
		default:
			blocks = append(blocks, omittedContent("unknown", 0))
			details.Content = append(details.Content, MCPContentBlock{Type: "unknown"})
		}
	}
	if res.StructuredContent != nil {
		details.StructuredContent = res.StructuredContent
		if data, err := json.Marshal(res.StructuredContent); err == nil {
			// Servers that want to support older clients commonly return the
			// same JSON both as a text block and as structuredContent. Feeding
			// that duplicate to the model doubles every such result, so elide
			// only the appended copy when one complete text block already has
			// the same JSON value. Every original text and media block stays.
			if !containsIdenticalJSONValue(textBlocks, data) {
				blocks = append(blocks, "[structured content]\n"+string(data))
			}
		} else {
			blocks = append(blocks, "[structured content could not be serialized; this text view is incomplete]")
		}
	}
	return strings.Join(blocks, "\n\n")
}

// inlineDataBlock describes an image or audio block for details. The SDK
// delivers the payload already base64-encoded, so Data is a plain copy; past
// maxMCPInlineData the block degrades to metadata plus Omitted, the same
// thing the model-facing placeholder says.
func inlineDataBlock(kind, mime string, data []byte) MCPContentBlock {
	block := MCPContentBlock{Type: kind, MIMEType: mime, Size: len(data)}
	if len(data) <= maxMCPInlineData {
		block.Data = string(data)
	} else {
		block.Omitted = true
	}
	return block
}

// mcpAnnotationsOf flattens the SDK's annotations for display, dropping the
// pointer shape (destructive/openWorld default true/false when absent) into
// the plain bools a badge row reads. Nil when nothing is hinted, so the
// details stay lean for the common un-annotated server.
func mcpAnnotationsOf(an *mcp.ToolAnnotations) *MCPAnnotations {
	if an == nil {
		return nil
	}
	out := &MCPAnnotations{
		ReadOnlyHint:   an.ReadOnlyHint,
		IdempotentHint: an.IdempotentHint,
	}
	if an.DestructiveHint != nil {
		out.DestructiveHint = *an.DestructiveHint
	}
	if an.OpenWorldHint != nil {
		out.OpenWorldHint = *an.OpenWorldHint
	}
	if *out == (MCPAnnotations{}) {
		return nil
	}
	return out
}

func omittedContent(mime string, size int) string {
	return fmt.Sprintf("[%s content omitted, %d bytes]", mime, size)
}

func mimeOr(mime, kind string) string {
	if mime != "" {
		return mime
	}
	return kind + "/unknown"
}

var (
	_ Tool            = (*MCPTool)(nil)
	_ ReadOnlyCapable = (*MCPTool)(nil)
)
