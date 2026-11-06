// Package llm is the only place that knows about the wire protocol.
// Everything above it works with the neutral types declared here, mirroring pi's
// split between packages/ai and the agent harness.
package llm

import (
	"context"
	"encoding/json"
	"time"

	"github.com/yosukeno/pogo/inputref"
)

type Role string

const (
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
)

// BlockType enumerates the content blocks we care about.
type BlockType string

const (
	BlockText       BlockType = "text"
	BlockThinking   BlockType = "thinking"
	BlockToolUse    BlockType = "tool_use"
	BlockToolResult BlockType = "tool_result"
	// BlockImage is a user attachment or tool observation. Its persisted form carries only a
	// session-workspace-relative path plus metadata — never the bytes, so a
	// session file stays small — and the description field is what a text-only
	// model receives in the image's place (see convert.go's fallback).
	BlockImage BlockType = "image"
	// BlockFile is a non-image attachment: a document or text file that lives
	// in the session workspace. It never carries bytes to the model — the
	// read tools and the docs skill reach it by path, which is the whole
	// point of putting attachments in the workspace.
	BlockFile BlockType = "file"
)

// Block is a single piece of message content. The flat shape keeps JSONL session
// records easy to read and diff.
type Block struct {
	Type BlockType `json:"type"`

	// Text / Thinking
	Text string `json:"text,omitempty"`

	// InputElement marks a bounded user-supplied page observation for local UI.
	InputElement bool `json:"input_element,omitempty"`
	// InputElementFavicon is frozen local display metadata. Provider adapters
	// never include these image bytes in model input or token accounting.
	InputElementFavicon string `json:"input_element_favicon,omitempty"`
	// InputSkill is trusted local provenance for an explicitly selected skill
	// atom. Text contains the host-loaded instructions; providers only see Text.
	InputSkill string `json:"input_skill,omitempty"`
	// InputMCP preserves a selected external method for local history/editing.
	// Providers receive only the host-produced Text, not this metadata.
	InputMCP *inputref.MCPReference `json:"input_mcp,omitempty"`
	// InputShortcut is frozen display data for an explicitly referenced user
	// library entry. Text contains its titled ordinary model-facing prose;
	// this metadata retains the exact original body for history/editing only.
	InputShortcut *InputShortcutSnapshot   `json:"input_shortcut,omitempty"`
	InputOffice   *inputref.OfficeSnapshot `json:"input_office,omitempty"`

	// ToolUse
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`
	// Failed tool arguments travel as a JSON string on local UI/storage wires.
	// These additive flags distinguish it from a legitimate JSON string input.
	// Provider adapters still send the original argument text, never these flags.
	InputInvalid bool   `json:"input_invalid,omitempty"`
	InputError   string `json:"input_error,omitempty"`
	// Local provenance only; never included in provider requests.
	ToolOrigin   string `json:"tool_origin,omitempty"`
	OriginalName string `json:"original_name,omitempty"`

	// ToolResult
	ToolUseID string `json:"tool_use_id,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
	// ResultID is the host-issued handle for saved tool output. It survives local
	// persistence and context editing; provider adapters only receive the recovery
	// hint carried in Text, never this metadata as a protocol field.
	ResultID string `json:"result_id,omitempty"`
	// Images are observations belonging to this tool result. The provider
	// adapter sends them after all results in the tool-call batch, preserving
	// tool-call/result adjacency. Image bytes are resolved only for the request.
	Images []Block `json:"images,omitempty"`
	// Details is a tool's structured payload (tools.EditDetails and friends) in
	// its marshalled form. It is carried here so that it reaches the session file,
	// and it is the one field on this type that must never reach a provider.
	//
	// That it cannot is a property of convert.go, not a rule someone has to
	// remember: toWireMessages builds each wire struct field by field, so a field
	// it does not mention has no path to the API. TestDetailsNeverReachTheWire
	// pins it (for Source too).
	//
	// Why it lives on the wire-shaped type at all: pi keeps two message types, a
	// rich persisted one and a lean protocol one, converting between them. That is
	// the cleaner design and also the more expensive one — it would mean a second
	// type threaded through the agent, the session and the Hub. One field plus one
	// test buys the same user-visible result: a diff that is still there after a
	// reload.
	Details json.RawMessage `json:"details,omitempty"`

	// Source is the citation stamp (agent.Source in marshalled form): which
	// registered source a tool_result's text was filed under. Same deal as
	// Details — persisted with the block so a reloaded session can still resolve
	// the labels its answers cite, and kept off the provider wire by convert.go's
	// field-by-field construction. The stamp the *model* sees is the [cite:...]
	// header in Text; this copy is for interfaces.
	Source json.RawMessage `json:"source,omitempty"`

	// DurationMS is a display-only wall-clock duration: for a thinking block, how
	// long it took to stream (first delta to first non-thinking one); for a
	// tool_result block, how long the call took to execute. Same guarantees as
	// Details — convert.go's field-by-field build is what keeps it off the wire.
	DurationMS int64 `json:"duration_ms,omitempty"`

	// Image fields, all persisted by reference: ImageRef is the session-
	// workspace path ("uploads/shot.png") the store resolves back to bytes,
	// ImageAlt the description a vision model wrote for the benefit of a
	// text-only one. DataURL is the wire-side exception — filled in at request
	// time by the agent's image resolver, never persisted (json:"-"), so the
	// session file never carries the base64 payload.
	ImageRef  string `json:"image_ref,omitempty"`
	ImageMime string `json:"image_mime,omitempty"`
	ImageName string `json:"image_name,omitempty"`
	ImageAlt  string `json:"image_alt,omitempty"`
	ImageW    int    `json:"image_w,omitempty"`
	ImageH    int    `json:"image_h,omitempty"`
	DataURL   string `json:"-"`

	// File fields: an attachment the model reads by path rather than seeing.
	// FilePath is session-workspace relative; the fallback note in convert.go
	// is what a provider receives, pointing at the tools and the docs skill.
	FilePath string `json:"file_path,omitempty"`
	FileName string `json:"file_name,omitempty"`
	FileSize int64  `json:"file_size,omitempty"`
	FileKind string `json:"file_kind,omitempty"`
}

type InputShortcutSnapshot struct {
	ID         string          `json:"id"`
	Title      string          `json:"title"`
	Text       string          `json:"text"`
	Icon       string          `json:"icon,omitempty"`
	Language   string          `json:"language,omitempty"`
	Parts      []inputref.Part `json:"parts,omitempty"`
	BlockCount int             `json:"block_count,omitempty"`
}

// Message keeps a block list even though the OpenAI wire format is flatter.
// Grouping all of one turn's tool results into a single message is what lets the
// loop stay protocol-agnostic; the expansion into role:"tool" entries happens at
// the boundary in openai.go.
type Message struct {
	Role         Role                `json:"role"`
	Content      []Block             `json:"content"`
	AnswerReview *AnswerReviewStatus `json:"answer_review,omitempty"`
	// Local conversation provenance. Provider adapters build their own message
	// structs, so these receipt fields never become model input.
	InboxID     string `json:"inbox_id,omitempty"`
	WakeID      string `json:"wake_id,omitempty"`
	RunID       string `json:"run_id,omitempty"`
	Steering    bool   `json:"steering,omitempty"`
	InputLayout string `json:"input_layout,omitempty"`
	// SourceState is versioned, host-owned citation bookkeeping for a compacted
	// continuation. It is persisted with the session, never rendered as message
	// text, counted as prompt tokens or forwarded by provider adapters.
	SourceState json.RawMessage `json:"source_state,omitempty"`
}

// UserText builds a plain user message.
func UserText(text string) Message {
	return Message{Role: RoleUser, Content: []Block{{Type: BlockText, Text: text}}}
}

// ToolCalls returns the tool_use blocks of an assistant message.
func (m Message) ToolCalls() []Block {
	var out []Block
	for _, b := range m.Content {
		if b.Type == BlockToolUse {
			out = append(out, b)
		}
	}
	return out
}

// Text concatenates all text blocks, which is what the -p mode prints.
func (m Message) Text() string {
	s := ""
	for _, b := range m.Content {
		if b.Type == BlockText {
			s += b.Text
		}
	}
	return s
}

// StopReason is the subset of finish reasons the loop branches on.
type StopReason string

const (
	StopEndTurn   StopReason = "end_turn"
	StopToolUse   StopReason = "tool_use"
	StopMaxTokens StopReason = "max_tokens"
	StopAborted   StopReason = "aborted"
	StopError     StopReason = "error"
)

// BytesPerToken is the divisor every size estimate in this project uses.
//
// It lives here, beside convert.go, because that file defines what actually goes
// on the wire and therefore what an estimate is allowed to count: thinking blocks
// are not replayed and Block.Details cannot reach a provider at all, so neither
// belongs in any estimate. Two packages estimate sizes for different purposes —
// session.Compose for the record, the agent for its context-edit trigger — and a
// divisor that differed between them would make their numbers incomparable rather
// than merely imprecise.
//
// Four is roughly right for English and reads *high* for Chinese — not low, which
// is what this comment used to claim ("off by about two and a half times", with the
// implied direction being an underestimate). Recovered from 25 real sessions the
// ratio of measured to estimated is 0.98 for mostly-ASCII prompts and 0.83 for
// CJK-heavy ones: both providers run tokenizers trained on Chinese, where a
// two-character word costs six bytes and often one token, so the honest divisor for
// that text is nearer six. The full table and the methodological trap behind it are
// in session.Composition.Calibration, which is where the measurement lives.
//
// The direction is the part worth carrying: four bytes per token is the conservative
// side on these providers, so a threshold set from this estimate fires early rather
// than late. Anyone tightening one on the opposite assumption would be removing the
// margin instead of adding it.
const BytesPerToken = 4

// EstimateTokens is the estimated prompt cost of some messages, counting only what
// convert.go actually sends. Used for the parts of a prompt no provider has
// measured yet — see Agent.promptTokens, which adds this to the last measured
// total rather than estimating the whole history.
func EstimateTokens(msgs []Message) int64 {
	var b int64
	for _, m := range msgs {
		for _, blk := range m.Content {
			switch blk.Type {
			case BlockText, BlockToolResult:
				b += int64(len(blk.Text))
				for _, im := range blk.Images {
					if im.Type == BlockImage {
						if im.DataURL != "" {
							b += 1600
						} else {
							b += int64(len(im.ImageAlt))
						}
					}
				}
			case BlockToolUse:
				b += int64(len(blk.Input)) + int64(len(blk.Name))
			case BlockImage:
				// Mirrors what convert.go sends: the bytes when the model sees
				// images (providers bill by pixels, and ~1600 tokens is the
				// middle of what a 1568px image costs on the OpenAI and
				// Anthropic scales), the description text when it does not.
				if blk.DataURL != "" {
					b += 1600
				} else {
					b += int64(len(blk.ImageAlt))
				}
			case BlockFile:
				// The fallback note is all a provider sees.
				b += int64(len(blk.FilePath)) + int64(len(blk.FileName)) + 64
			case BlockThinking:
				// Not replayed; see convert.go.
			}
		}
	}
	return b / BytesPerToken
}

// Usage is one call's token accounting, exactly as the provider reported it.
// Every field is a count of tokens.
//
// The fields are not disjoint, and that trips people up: Input is the whole
// prompt the provider billed for, and CacheRead is the part of that prompt which
// came from the cached prefix. So CacheRead <= Input, and the fresh part of the
// prompt is Input - CacheRead. Adding them together double-counts.
//
// Reasoning is the opposite arrangement for the completion side: on providers
// that report it, the reasoning tokens are already inside Output.
type Usage struct {
	// Input is prompt tokens: system prompt + tool schemas + the whole
	// conversation so far, resent on every turn.
	Input int64 `json:"input"`
	// Output is completion tokens, reasoning included where the provider
	// reports it separately below.
	Output int64 `json:"output"`
	// CacheRead is the subset of Input served from the provider's cached prefix,
	// billed at a lower rate. Not an addition to Input.
	CacheRead int64 `json:"cache_read,omitempty"`
	// Reasoning is the subset of Output spent on thinking. Not an addition to
	// Output.
	Reasoning int64 `json:"reasoning,omitempty"`
}

// FreshInput is the part of the prompt that was not served from cache. This is
// the number that moves when the prompt prefix changes, so it is the one worth
// looking at when a turn suddenly costs more than the one before it.
func (u Usage) FreshInput() int64 {
	if u.CacheRead > u.Input {
		// Defensive: a provider that reports cached_tokens against a prompt total
		// it computed differently would otherwise produce a negative number.
		return 0
	}
	return u.Input - u.CacheRead
}

// Timing is where one streaming call spent its wall clock.
//
// It exists because a slow turn has three quite different causes and they are
// indistinguishable from the outside: a slow connection, a model that takes a
// long time to start, or a model that streams slowly once it has started. TTFB
// and TTFT separate the first two; Total minus TTFT is the third.
type Timing struct {
	// HeadersReceived distinguishes a real response-header latency from a call
	// that timed out before receiving any response. TTFB is zero in that case.
	HeadersReceived bool `json:"headers_received"`
	// TTFB is request start to response headers: DNS, dial, TLS, and however
	// long the provider queued the request.
	TTFB time.Duration `json:"ttfb"`
	// TTFT is request start to the first content delta — thinking, text, or a
	// tool call name. This is the wait a user actually perceives, and on a
	// reasoning model it is usually most of it.
	//
	// Zero when the call produced no content at all, which is why the aggregate
	// in the agent counts calls rather than dividing by turns.
	TTFT time.Duration `json:"ttft"`
	// FirstText is request start to the first visible text delta, rather than a
	// reasoning delta. Zero means none arrived, not a zero-latency answer.
	FirstText time.Duration `json:"first_text"`
	// LastContent locates the last thinking/text/tool delta before completion or
	// failure. Byte counts are observed UTF-8 bytes, not billed token estimates.
	LastContent   time.Duration `json:"last_content"`
	ThinkingBytes int64         `json:"thinking_bytes"`
	TextBytes     int64         `json:"text_bytes"`
	// Total is the whole call as the caller experienced it, including any
	// retries and their backoff. TTFB and TTFT are measured against the final
	// attempt, so Total can be much larger than either.
	Total time.Duration `json:"total"`
}

// ToolSchema is the tool declaration sent to the model.
type ToolSchema struct {
	Name        string
	Description string
	// RemoteName is an optional MCP alias from the captured registry. It is
	// never sent as a declaration and cannot activate a hidden tool.
	RemoteName string
	// InputSchema is a hand-written JSON Schema object. No reflection: four
	// literals are cheaper to read than a generator.
	InputSchema map[string]any
}

// DeltaKind describes an incremental update during streaming.
type DeltaKind string

const (
	DeltaText          DeltaKind = "text"
	DeltaThinking      DeltaKind = "thinking"
	DeltaToolCallStart DeltaKind = "tool_call_start"
	// DeltaToolCallArgs carries one fragment of a tool call's arguments while the
	// model is still generating them: the raw text rides in Text, the call's
	// identity in ToolID. It exists so a UI can preview a large argument as it
	// streams in — the settled arguments still arrive with the response itself.
	DeltaToolCallArgs DeltaKind = "tool_call_args"
)

// Delta is what the client streams out while a response is being produced.
// The agent loop forwards these as events; it never inspects partial state.
type Delta struct {
	Kind     DeltaKind
	Text     string
	ToolName string
	ToolID   string
}

// Response is the settled result of one LLM call.
type Response struct {
	// ProtocolIssue describes a completed response that requested a tool in an
	// unusable format. No calls from that response may execute.
	ProtocolIssue *ToolProtocolIssue
	Message       Message
	StopReason    StopReason
	Usage         Usage
	// Timing is filled in by the client for every outcome, including an abort:
	// how long a cancelled turn spent waiting is worth knowing too.
	Timing Timing
	// RequestProfile is safe host metadata, not model output or provider usage.
	RequestProfile *RequestProfile
}

// Client is what the agent loop depends on. Keeping it an interface is what
// makes swapping models at runtime a pointer assignment.
type Client interface {
	Model() string
	Stream(ctx context.Context, systemPrompt string, history []Message, tools []ToolSchema, onDelta func(Delta)) (Response, error)
}
