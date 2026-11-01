// Package web: the MCP server layer.
//
// This is the protocol half of the split harness-design.md §13 has carried since
// the tools.json format was chosen: the manifest catalog (MCP Tool objects plus
// x-pogo-exec) was always the future tools/list response body, with the protocol
// deferred until a real remote consumer existed. One has: another agent needs to
// drive this one over the network, MCP being the only vocabulary both sides
// already speak.
//
// Three placement decisions are load-bearing:
//
//   - In-process with the web server, not a separate binary. The MCP tools are
//     thin over Manager/Session — the same primitives POST /messages uses — and
//     an MCP-submitted task is an ordinary session: it shows in the web UI's
//     sidebar, a human can watch it stream, and an approval gate it raises can
//     be answered from the browser. That last property is the answer to the
//     problem every headless driver has with gates: fail-closed after a timeout
//     becomes fail-open-to-a-human instead.
//   - Both HTTP transports, /mcp (Streamable HTTP) and /sse (the deprecated
//     2024-11-05 transport), because "the other side speaks SSE" does not say
//     which of the two they mean and the SDK sells both for one server. The
//     legacy transport's handshake drops query parameters from its POST-back
//     endpoint (the SDK builds it with URL.Parse("?sessionid=…"), which replaces
//     the query), so /sse effectively requires Authorization: Bearer — /mcp
//     accepts the token by header or query like the rest of the API.
//   - Application-level submit/poll tools rather than protocol Tasks. Tasks
//     (2025-11-25, experimental) standardize exactly this shape, but the Go SDK
//     does not implement them yet and clients that do are rare. The tool
//     vocabulary below deliberately mirrors the Tasks state machine
//     (working/completed/failed/cancelled, end reasons, dispositions) so a
//     later migration is a re-labeling, not a redesign.
package web

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yosukeno/pogo/agent"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/mcpclient"
	"github.com/yosukeno/pogo/mcpobserve"
	"github.com/yosukeno/pogo/skillobserve"
	"github.com/yosukeno/pogo/skillruntime"
	"github.com/yosukeno/pogo/tools"
)

// MCPOptions configures the MCP endpoints. A nil *MCPOptions in ServerOptions
// leaves them off entirely — unless MCPClientStore is set, in which case the
// layer exists anyway and the "server" section of that file (plus the
// POGO_MCP* environment) decides whether the endpoints answer: a new
// network-facing capability is a grant, not a default, and the grant lives
// in configuration now rather than in this struct.
//
// The fields below are the static baseline of that configuration — the
// test/embedding path. When a store is in play they sit at the bottom of
// the precedence tiers (env > file > static, see mcpserve.go): Token,
// Policy and the caps here apply only where neither the environment nor the
// file speaks.
type MCPOptions struct {
	// Token, when set, is the credential the MCP paths require. With no store
	// in play, empty falls back to the web token (one secret to distribute);
	// with a store, empty means *no authentication* — the file channel's
	// deliberate semantics, see mcpserve.go's effectiveToken.
	Token string
	// Policy is the approval mode freshly submitted tasks start under:
	// "strict", "standard" or "auto". Standard is the conservative default —
	// bash gets human review — and a deployment that has hardened the
	// container (malagent's sandboxed, read-only image) can opt into "auto".
	// Follow-up submissions to an existing session never touch the policy:
	// whoever set it owns it. Read per request in the current configuration,
	// so a file edit binds the next submission.
	Policy string
	// MaxConcurrent bounds how many MCP-submitted tasks may be working at once:
	// every run is an LLM spend, and a remote caller's retry loop is not a
	// billing plan (hosted agents ship the same cap — Jules allows three).
	// Submissions past the cap are refused with a hint, not queued. <= 0 falls
	// back to 8.
	MaxConcurrent int
	// SubmitRPM bounds agent_submit_task calls per minute, process-global and
	// sliding-window: the deployments this serves hold one or two tokens, so
	// per-token buckets would only subdivide the same budget. Idempotent
	// replays (a resubmitted client_ref) do not consume quota — a retry is not
	// new demand. <= 0 falls back to 60.
	SubmitRPM int
	// MaxWaits bounds concurrent agent_task_wait calls: each one holds a
	// goroutine, a hub subscription and a hanging HTTP request for up to half
	// an hour, so an unbounded count is an unbounded resource grant. Over the
	// cap the caller is sent back to polling. <= 0 falls back to 32.
	MaxWaits int
	// Logger receives the audit lines: one per mutating call, sizes rather than
	// contents — the transcript is the record of what was said, the log only
	// needs to say that a machine came. Nil silences it.
	Logger *log.Logger
	// WebhookAllowInsecure lets a task's webhook_url be plain http and point at
	// loopback/private addresses. It exists for tests and is deliberately NOT
	// wired to any environment variable: production SSRF validation (https +
	// public DNS answers only) must not be a config slip away.
	WebhookAllowInsecure bool
	// WebhookBackoff overrides the terminal-event delivery retry schedule
	// (default: immediate, 30s, 2m). For tests.
	WebhookBackoff []time.Duration
}

// maxMCPTasks bounds the terminal-task registry. Working tasks are never
// dropped; among finished ones the oldest go. The registry is a convenience for
// pollers, not the record — the transcript on disk is — so a pruned entry just
// means status falls back to the session snapshot.
const maxMCPTasks = 1024

type mcpTask struct {
	slotHeld  bool // shared across the global and Agent-specific MCP endpoints
	sessionID string
	runID     string
	startedMS int64

	// webhookURL/webhookSecret are the terminal-event push channel the caller
	// asked for at submit time (A2A's PushNotificationConfig shape): the push
	// is a signal, not the payload — the answer is still pulled via
	// agent_task_result. Immutable after creation, like sessionID.
	webhookURL    string
	webhookSecret string

	// terminal transitions at most once, guarded by the layer mutex; done lets
	// a future blocking variant wait without polling the map.
	terminal    bool
	status      string // completed | failed | cancelled (working is implied by !terminal)
	endReason   string // the harness vocabulary, verbatim: turn_limit, stagnation…
	disposition string // agent.EndReason's own classification of endReason
	errText     string
	usage       *llm.Usage
	done        chan struct{}
}

type mcpLayer struct {
	mgr             *Manager
	parent          *mcpLayer
	agentID         string
	runSlots        chan struct{}
	exportMu        sync.Mutex
	exportSnapshots map[string]agentToolSnapshot

	// static is the programmatic baseline (ServerOptions.MCP), the
	// test/embedding path; serveWeb leaves it zero. Its fields sit at the
	// bottom of the precedence tiers resolved in mcpserve.go: env > file >
	// static. staticOn records that a baseline was supplied at all — the
	// enabled tier needs "present" distinct from "zero".
	static   MCPOptions
	staticOn bool

	// file is the current "server" section of mcp.json (mcpclient), hot
	// swapped by PUT /api/mcp and read per request, so enabled, token and
	// policy edits take effect without a restart. The three demand caps
	// deliberately do not follow it: a limiter's size is not a dial to turn
	// under in-flight callers, so they are resolved once below and a file
	// edit that disagrees surfaces as restart_required in serveState.
	file atomic.Pointer[mcpclient.ServeConfig]

	mu    sync.Mutex
	tasks map[string]*mcpTask
	// refs maps a client-supplied idempotency key (agent_submit_task's
	// client_ref) to the task it started, pruned alongside the registry entry
	// it points at. A replayed ref returns the original task: a network retry
	// must not be a double spend.
	refs map[string]string

	// Demand caps, fixed at construction by resolveCaps (env > file >
	// static > default) — see the file field comment for why not hot.
	maxConcurrent int
	submits       *submitLimiter
	waitSlots     chan struct{}
	log           *log.Logger
	// webhookAllowInsecure opens plain-http/private-host webhook targets —
	// tests only, never env-wirable. webhookBackoff is the delivery retry
	// schedule between attempts.
	webhookAllowInsecure bool
	webhookBackoff       []time.Duration

	// srv is the one *mcp.Server both transports hand out; synthNames is the
	// manifest-tool surface registered on it, kept so a skill reload can
	// re-sync the tool list in place — RemoveTools + AddTool, which the SDK
	// debounces and announces as tools/list_changed on its own.
	srv        *mcp.Server
	synthNames []string

	// forwardNames/forwardSig are the forwarded external-tool counterpart of
	// synthNames: the names the last registration pass added, and a
	// fingerprint of everything that pass read (the whitelist's words plus
	// each connected server's exposed names and definition fingerprints).
	// PUT /api/mcp rebuilds eagerly; the tools/list middleware compares the
	// fingerprint and rebuilds on drift, which is how pool reconnects and
	// catalog refreshes reach tools/list without mcpclient growing a
	// callback into the serve side.
	forwardMu    sync.Mutex
	forwardNames []string
	forwardSig   string

	// runtimeNames/runtimeSig are the packaged runtime Skills' counterpart of
	// the forward pair: the skill_* contract tools (plus the skill_upload_file
	// / skill_read_artifact pipeline helpers) the last registration pass
	// added, and a fingerprint of what the pass read — the whitelist's words,
	// each installed package's digest and revoked state, and executor
	// availability. runtimeSessions caches one pseudo-session BoundSession per
	// exposed package (session id "mcp-serve"): BoundSession revalidates the
	// whole chain on every Call, so a cached copy stays honest across revoke
	// and delete, and a resync simply rebuilds the set.
	runtimeMu       sync.Mutex
	runtimeNames    []string
	runtimeSig      string
	runtimeSessions []*skillruntime.BoundSession
}

func newMCPLayer(mgr *Manager, opts MCPOptions, staticOn bool, fileSC *mcpclient.ServeConfig) *mcpLayer {
	l := &mcpLayer{
		mgr: mgr, static: opts, staticOn: staticOn,
		tasks: make(map[string]*mcpTask), refs: map[string]string{},
		log:                  opts.Logger,
		webhookAllowInsecure: opts.WebhookAllowInsecure,
		webhookBackoff:       opts.WebhookBackoff,
	}
	l.file.Store(fileSC)
	l.maxConcurrent, l.submits, l.waitSlots = resolveCaps(opts, fileSC)
	l.runSlots = make(chan struct{}, l.maxConcurrent)
	if l.webhookBackoff == nil {
		l.webhookBackoff = []time.Duration{30 * time.Second, 2 * time.Minute}
	}
	return l
}

// audit writes one line per mutating MCP call — the machine channel's
// equivalent of the web UI's sidebar visibility. Sizes, not contents: the
// transcript already holds the words, the log only needs to say who came.
func (l *mcpLayer) audit(format string, args ...any) {
	if l.log != nil {
		l.log.Printf("mcp: "+format, args...)
	}
}

// submitLimiter is a sliding-window counter over submit timestamps — the
// smallest thing that answers "N per minute" without taking a dependency.
type submitLimiter struct {
	mu    sync.Mutex
	max   int
	times []time.Time
}

func newSubmitLimiter(rpm int) *submitLimiter {
	if rpm <= 0 {
		rpm = 60
	}
	return &submitLimiter{max: rpm}
}

// allow records a submit and reports whether it fits the window; when it does
// not, the returned duration is how long until the oldest entry ages out.
func (s *submitLimiter) allow() (time.Duration, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-time.Minute)
	kept := s.times[:0]
	for _, t := range s.times {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	s.times = kept
	if len(s.times) >= s.max {
		return time.Until(s.times[0].Add(time.Minute)), false
	}
	s.times = append(s.times, now)
	return 0, true
}

// server builds the MCP server with its full tool surface, then subscribes to
// the manager's skill reloads so an uploaded bundle's manifest tools appear on
// tools/list without a restart.
func (l *mcpLayer) server() *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{
		Name:  "pogo",
		Title: "pogo agent",
		// The handshake's version is the build's module version — "dev" for a
		// local build, the tag for an installed one — so a remote driver can
		// tell what it is talking to before it trusts a behavior.
		Version: mcpBuildVersion(),
	}, nil)
	l.srv = srv

	// The include_tools whitelist (mcp.json's server section): tools/list is
	// filtered, tools/call of an unlisted name is refused outright, and every
	// other method — notifications, ping — passes through untouched.
	// toolAllowed reads the live file section per request (effectivePolicy's
	// pattern), so PUT /api/mcp edits the exposed surface without a restart.
	srv.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			switch method {
			case "tools/list":
				// The pool reconnects and refreshes catalogs on its own
				// schedule and deliberately has no callback into this layer,
				// so the forwarded external tools catch up here — the exact
				// moment a client asks what exists. Cheap comparison, a
				// rebuild only when the catalog or the whitelist moved.
				l.ensureForwardSync()
				// Same self-heal for the packaged runtime Skills: the
				// install/disable/delete hooks resync eagerly, and this
				// signature comparison covers anything they missed.
				l.ensureRuntimeSync()
				res, err := next(ctx, method, req)
				if err != nil {
					return nil, err
				}
				if lr, ok := res.(*mcp.ListToolsResult); ok {
					filtered := make([]*mcp.Tool, 0, len(lr.Tools))
					for _, t := range lr.Tools {
						if l.toolAllowed(t.Name) {
							filtered = append(filtered, t)
						}
					}
					lr.Tools = filtered
				}
				return res, nil
			case "tools/call":
				// A protocol error, not an is_error result: the caller asked
				// for something this server does not expose at all, which is
				// the same shape as calling an unknown tool. (The receiving
				// params are the Raw variant — handlers unmarshal arguments
				// themselves.)
				if params, ok := req.GetParams().(*mcp.CallToolParamsRaw); ok && !l.toolAllowed(params.Name) {
					return nil, fmt.Errorf("tool %q is not exposed by this server (include_tools whitelist)", params.Name)
				}
			}
			return next(ctx, method, req)
		}
	})
	// Registered AFTER the whitelist middleware so it wraps it: the SDK applies
	// middleware in registration order (first added = innermost), and only an
	// outer observer sees an include_tools refusal — a refused call is exactly
	// the observation an operator does not want to lose. Both transports share
	// this one server, so the record survives even when the HTTP log's payload
	// truncation would hide the exchange.
	srv.AddReceivingMiddleware(l.observeMiddleware())
	// The agent-level tools. Raw handlers (Server.AddTool, json.RawMessage
	// arguments) rather than the typed generic: the point of this layer is
	// interop with unknown clients, and a schema drift inside a generic's
	// struct tag should not be what a remote caller discovers first.
	srv.AddTool(&mcp.Tool{
		Name:        "agent_submit_task",
		Title:       "Submit a task to the agent",
		Description: "Submit a prompt to the pogo agent and return immediately with a task id. Poll agent_task_status until the status is completed, failed or cancelled — input_required means a human's approval is pending, keep polling — then read agent_task_result for the answer. Each task runs in its own session, visible to a human in the pogo web UI; pass session_id to continue an existing conversation instead. Pass client_ref as an idempotency key, a fresh one per logical submission: resubmitting the same ref returns the original task instead of starting a duplicate, so a network retry is never a double spend. Submissions are rate- and concurrency-limited; a refusal says when to retry. Every MCP-driven run starts under this channel's configured approval policy, continued sessions included; under a policy that requires a human, a run blocks on each approval until someone answers in the web UI or the gate times out.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"prompt": map[string]any{
					"type":        "string",
					"description": "The task, in the same words you would give a coding agent.",
				},
				"session_id": map[string]any{
					"type":        "string",
					"description": "Continue this session (its transcript carries the context) instead of starting a new one. A session with a run still in flight is rejected — poll it first.",
				},
				"model": map[string]any{"type": "string", "description": "Model id for a new session; the server default when omitted."},
				"workspace": map[string]any{
					"type":        "string",
					"description": "Working directory for a new session, relative to the server root; the root itself when omitted.",
				},
				"client_ref": map[string]any{
					"type":        "string",
					"description": "Idempotency key, max 128 bytes. A resubmission of a ref that already started a task returns that task with deduplicated: true instead of starting a second. Lives as long as the task registry entry.",
				},
				"webhook_url": map[string]any{
					"type":        "string",
					"description": "POST this URL when the task reaches a terminal state: {event: task.finished, task_id, status, end_reason, occurred_at} — a signal, never the answer (pull that with agent_task_result). https and publicly resolving hosts only; validated at submit. Retried on failure.",
				},
				"webhook_secret": map[string]any{
					"type":        "string",
					"description": "When set, deliveries are signed Standard-Webhooks style: X-Pogo-Signature: t=<unix>,v1=<hex HMAC-SHA256 of \"<t>.<body>\">. Verify with the timestamp fresh (5 min tolerance) and a constant-time compare.",
				},
			},
			"required":             []string{"prompt"},
			"additionalProperties": false,
		},
		OutputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id":            map[string]any{"type": "string"},
				"session_id":         map[string]any{"type": "string"},
				"run_id":             map[string]any{"type": "string", "description": "Absent on a deduplicated resubmission."},
				"status":             map[string]any{"type": "string", "description": "working for a fresh submission; the original task's status when deduplicated."},
				"model":              map[string]any{"type": "string"},
				"provider":           map[string]any{"type": "string"},
				"deduplicated":       map[string]any{"type": "boolean"},
				"webhook_registered": map[string]any{"type": "boolean"},
				"note":               map[string]any{"type": "string"},
			},
			"required": []string{"task_id"},
		},
	}, l.submitTask)

	srv.AddTool(&mcp.Tool{
		Name:        "agent_task_status",
		Title:       "Poll a task",
		Description: "Report a task's state: working, input_required (a human's approval is pending in the web UI — not a terminal state, keep polling), completed, failed or cancelled, with the harness's end_reason and disposition when finished, and live detail while running — current turn, the agent's latest partial text, and whether a human's approval is currently pending. While a task is not terminal the response carries poll_interval_seconds as the suggested cadence; when you can block, prefer agent_task_wait over hand polling.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id": map[string]any{"type": "string", "description": "The task id agent_submit_task returned (it is the session id)."},
			},
			"required":             []string{"task_id"},
			"additionalProperties": false,
		},
		OutputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id": map[string]any{"type": "string"},
				"status": map[string]any{
					"type": "string",
					"enum": []string{"working", "input_required", "completed", "failed", "cancelled"},
				},
				"end_reason":        map[string]any{"type": "string"},
				"disposition":       map[string]any{"type": "string"},
				"error":             map[string]any{"type": "string"},
				"usage":             map[string]any{"type": "object"},
				"turn":              map[string]any{"type": "integer"},
				"model":             map[string]any{"type": "string"},
				"awaiting_approval": map[string]any{"type": "integer"},
				"pending_gates": map[string]any{
					"type":        "array",
					"description": "The approval tickets awaiting an answer: gate_id, tool, args (text, capped), danger hits, deadline (unix ms). Answer each with agent_task_approve.",
					"items":       map[string]any{"type": "object"},
				},
				"poll_interval_seconds": map[string]any{"type": "integer"},
				"progress_text":         map[string]any{"type": "string"},
				"note":                  map[string]any{"type": "string"},
				"recovered":             map[string]any{"type": "boolean", "description": "True when the answer comes from the session transcript after a server restart, not the task registry."},
				"timed_out":             map[string]any{"type": "boolean"},
				"interrupted":           map[string]any{"type": "boolean"},
			},
			"required": []string{"task_id", "status"},
		},
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, l.taskStatus)

	srv.AddTool(&mcp.Tool{
		Name:        "agent_task_result",
		Title:       "Read a finished task's answer",
		Description: "Return the agent's final answer for a finished task — the last assistant message of the run. Calling it on a task still working (or in input_required) is an error; poll agent_task_status first. end_reason and disposition say how the run ended when the answer is not there.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id": map[string]any{"type": "string", "description": "The task id agent_submit_task returned."},
			},
			"required":             []string{"task_id"},
			"additionalProperties": false,
		},
		OutputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id":     map[string]any{"type": "string"},
				"status":      map[string]any{"type": "string", "enum": []string{"completed", "failed", "cancelled"}},
				"end_reason":  map[string]any{"type": "string"},
				"disposition": map[string]any{"type": "string"},
				"error":       map[string]any{"type": "string"},
				"usage":       map[string]any{"type": "object"},
				"text":        map[string]any{"type": "string", "description": "The agent's final answer; also the result's text content."},
				"truncated":   map[string]any{"type": "boolean", "description": "True when text was clipped to fit; the note names the transcript with the full answer."},
				"note":        map[string]any{"type": "string"},
				"recovered":   map[string]any{"type": "boolean"},
			},
			"required": []string{"task_id", "status"},
		},
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, l.taskResult)

	srv.AddTool(&mcp.Tool{
		Name:        "agent_task_cancel",
		Title:       "Cancel a task",
		Description: "Cancel a task's run. Idempotent: cancelling a task with no run in flight reports cancelled: false rather than an error.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id": map[string]any{"type": "string", "description": "The task id agent_submit_task returned."},
			},
			"required":             []string{"task_id"},
			"additionalProperties": false,
		},
		OutputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id":   map[string]any{"type": "string"},
				"cancelled": map[string]any{"type": "boolean"},
			},
			"required": []string{"task_id", "cancelled"},
		},
		Annotations: &mcp.ToolAnnotations{DestructiveHint: boolPtr(true), IdempotentHint: true},
	}, l.taskCancel)

	srv.AddTool(&mcp.Tool{
		Name:        "agent_task_wait",
		Title:       "Wait for a task to finish",
		Description: "Block until the task reaches a terminal status (completed/failed/cancelled), the timeout elapses, or you cancel the call — then return the same shape agent_task_status does, with the final answer included when the task finished. Pass _meta.progressToken in the call to receive notifications/progress while waiting: one per turn boundary and tool completion, plus throttled tails of the agent's answer in the message. Waiting longer never affects the task: a cancelled or timed-out wait leaves the run alone.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id":         map[string]any{"type": "string", "description": "The task id agent_submit_task returned."},
				"timeout_seconds": map[string]any{"type": "number", "description": "How long to wait before returning the current status. Default 50 (under the common MCP client request timeout), clamped to 1–1800. Over the concurrent-wait cap the call is refused with a pointer back to polling."},
			},
			"required":             []string{"task_id"},
			"additionalProperties": false,
		},
		// The terminal path returns agent_task_result's shape, the timeout and
		// interruption paths agent_task_status's — the union, with only the two
		// fields every path sets required.
		OutputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id": map[string]any{"type": "string"},
				"status": map[string]any{
					"type": "string",
					"enum": []string{"working", "input_required", "completed", "failed", "cancelled"},
				},
				"end_reason":            map[string]any{"type": "string"},
				"disposition":           map[string]any{"type": "string"},
				"error":                 map[string]any{"type": "string"},
				"usage":                 map[string]any{"type": "object"},
				"text":                  map[string]any{"type": "string"},
				"truncated":             map[string]any{"type": "boolean"},
				"turn":                  map[string]any{"type": "integer"},
				"awaiting_approval":     map[string]any{"type": "integer"},
				"pending_gates":         map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
				"poll_interval_seconds": map[string]any{"type": "integer"},
				"timed_out":             map[string]any{"type": "boolean"},
				"interrupted":           map[string]any{"type": "boolean"},
				"note":                  map[string]any{"type": "string"},
				"recovered":             map[string]any{"type": "boolean"},
			},
			"required": []string{"task_id", "status"},
		},
		// Waiting never affects the task — a cancelled or timed-out wait leaves
		// the run alone — so the read-only hint is honest here.
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, l.taskWait)

	srv.AddTool(&mcp.Tool{
		Name:        "agent_task_steer",
		Title:       "Steer a running task",
		Description: "Deliver a message into a task that is still running: the agent receives it after the tool call it is on completes, before its next turn — a redirection, not an interruption. steered:false means no run was in flight (finished or never started); send a follow-up agent_submit_task with session_id instead.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id": map[string]any{"type": "string", "description": "The task id agent_submit_task returned."},
				"text":    map[string]any{"type": "string", "description": "The steering message, in the voice of the original task's author."},
			},
			"required":             []string{"task_id", "text"},
			"additionalProperties": false,
		},
		OutputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id": map[string]any{"type": "string"},
				"steered": map[string]any{"type": "boolean"},
				"note":    map[string]any{"type": "string"},
			},
			"required": []string{"task_id", "steered"},
		},
	}, l.taskSteer)

	srv.AddTool(&mcp.Tool{
		Name:        "agent_task_approve",
		Title:       "Answer a pending approval",
		Description: "Answer an approval gate on a task in input_required — the API closure of what a human would click in the web UI. agent_task_status lists pending_gates with gate_id, tool, the exact args, danger hits and deadline; answer each with this tool. decision=approve lets the call run — pass args to approve with edited arguments (the approve-after-editing path); decision=deny refuses it, and feedback tells the agent why so it can try another way. remember=tool|command grants a session-scoped pass for matching future calls (never for danger-matched or subagent-originated calls). Gates fail closed on timeout; an already-resolved gate is an error, not a double answer.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id": map[string]any{"type": "string", "description": "The task id agent_submit_task returned."},
				"gate_id": map[string]any{"type": "string", "description": "The gate to answer, from pending_gates. Optional when the task has exactly one pending gate."},
				"decision": map[string]any{
					"type": "string", "enum": []string{"approve", "deny"},
					"description": "approve runs the call (with args when given); deny refuses it.",
				},
				"args": map[string]any{
					"type":        "object",
					"description": "Modified tool arguments for an approve-with-edits decision. The tool runs with these instead of what the agent asked for.",
				},
				"feedback": map[string]any{"type": "string", "description": "Why a denial — fed back to the agent so it can choose differently."},
				"remember": map[string]any{
					"type": "string", "enum": []string{"tool", "command"},
					"description": "On approve: grant a session-scoped pass for this tool, or for this command in this workdir. Never for danger-matched or subagent calls.",
				},
			},
			"required":             []string{"task_id", "decision"},
			"additionalProperties": false,
		},
		OutputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id":  map[string]any{"type": "string"},
				"gate_id":  map[string]any{"type": "string"},
				"approved": map[string]any{"type": "boolean"},
				"note":     map[string]any{"type": "string"},
			},
			"required": []string{"task_id", "gate_id", "approved"},
		},
	}, l.taskApprove)

	srv.AddTool(&mcp.Tool{
		Name:        "agent_task_list",
		Title:       "List this channel's tasks",
		Description: "List the tasks this MCP channel has submitted, newest first — the recovery answer when a caller loses track of a task id, and the inventory of what the machine channel has running. The registry is in-memory with a 1024-terminal cap: tasks evicted from it, or from before a server restart, are absent here but still answer agent_task_status and agent_task_result by id (marked recovered).",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"status": map[string]any{
					"type": "string", "enum": []string{"working", "completed", "failed", "cancelled", "terminal"},
					"description": "Keep only this state; terminal means completed+failed+cancelled. Default lists everything.",
				},
				"limit": map[string]any{"type": "integer", "description": "Max tasks returned, default 50, capped at 200."},
			},
			"additionalProperties": false,
		},
		OutputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"tasks": map[string]any{
					"type": "array",
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"task_id":    map[string]any{"type": "string"},
							"status":     map[string]any{"type": "string"},
							"started_at": map[string]any{"type": "integer", "description": "Unix milliseconds."},
							"end_reason": map[string]any{"type": "string"},
						},
						"required": []string{"task_id", "status", "started_at"},
					},
				},
				"count": map[string]any{"type": "integer"},
			},
			"required": []string{"tasks", "count"},
		},
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, l.taskList)

	srv.AddTool(&mcp.Tool{
		Name:        "agent_task_file",
		Title:       "Read a file from a task's workspace",
		Description: "Read a file out of a task's workspace — the artifact channel for what does not fit agent_task_result's clip (a long report, a generated dump, the transcript itself). path is relative to the task's workspace, which agent_submit_task's response names; reads are size-capped (default 256KB, max 1MB) and report truncated:true plus the full byte size when clipped. Paths that escape the workspace — by .. or by symlink — are refused.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id":   map[string]any{"type": "string", "description": "The task id agent_submit_task returned."},
				"path":      map[string]any{"type": "string", "description": "File path relative to the task's workspace."},
				"max_bytes": map[string]any{"type": "integer", "description": "Read cap. Default 262144, capped at 1048576."},
			},
			"required":             []string{"task_id", "path"},
			"additionalProperties": false,
		},
		OutputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"task_id":   map[string]any{"type": "string"},
				"path":      map[string]any{"type": "string"},
				"size":      map[string]any{"type": "integer", "description": "Full size of the file in bytes."},
				"content":   map[string]any{"type": "string"},
				"truncated": map[string]any{"type": "boolean"},
			},
			"required": []string{"task_id", "path", "size", "content"},
		},
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, l.taskFile)

	if l.agentID == "" {
		l.addSynthTools(srv)
		l.mgr.OnSynthReload(l.resyncSynth)
		// The forwarded external tools register on the global layer only — a
		// per-agent sublayer has its own selected-tools channel
		// (agent_list_tools/agent_call_tool), and re-exposing the whole pool
		// under it would widen what an Agent-scoped token can reach. The pool
		// is usually still connecting at construction, so this first pass
		// typically registers nothing; ensureForwardSync fills in once a
		// catalog lands.
		l.rebuildForwardLocked(l.fileConfig(), l.mgr.MCPCatalogs(nil, nil))
		// The packaged runtime Skills share the global-only placement for the
		// same reason: an Agent-scoped token must not reach the whole
		// installed catalog. Opt-in like the forwarded tools — an empty
		// include_tools registers no contract tool at all.
		l.rebuildRuntimeLocked(l.fileConfig())
		l.mgr.OnRuntimeSkillChange(l.resyncRuntime)
	} else {
		l.addAgentTools(srv)
	}
	return srv
}

// inboundSpanKey carries the live observation span of an inbound tools/call
// to the handler serving it, so a handler that allocates local IDs after the
// span started (submit → session/run) can still correlate the record. The key
// is unexported and the value type fixed: nothing outside this file can put
// one on a context.
type inboundSpanKey struct{}

func inboundSpanFrom(ctx context.Context) *mcpobserve.Span {
	span, _ := ctx.Value(inboundSpanKey{}).(*mcpobserve.Span)
	return span
}

// Only registry-backed identities are correlation fields. Never retain an
// arbitrary task_id argument (which may contain payload or unrelated text).
func (l *mcpLayer) observedTask(id string) mcpobserve.Metadata {
	l.mu.Lock()
	task := l.tasks[id]
	var md mcpobserve.Metadata
	if task != nil {
		md = mcpobserve.Metadata{TaskID: id, SessionID: task.sessionID, RunID: task.runID}
	}
	l.mu.Unlock()
	if md.SessionID != "" && l.mgr != nil {
		if s, err := l.getSession(md.SessionID); err == nil {
			md.AgentID, md.AgentName = s.studioID, s.studioName
			if s.owner != nil {
				md.OwnerID = s.owner.SubjectID
			}
		}
	}
	return md
}

// observeMiddleware records every inbound tools/call as an mcpobserve.Call —
// agent tools, manifest tools, whitelist refusals, unknown names and protocol
// errors alike, because it wraps the whitelist and the SDK's own dispatch.
// Non-tool methods (initialize, tools/list, ping, notifications) pass through
// unrecorded: they are protocol housekeeping, not executions. Metadata only:
// the record carries names, IDs and the outcome category, never arguments,
// results or error text.
func (l *mcpLayer) observeMiddleware() mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			if method != "tools/call" {
				return next(ctx, method, req)
			}
			// Receiving params are the Raw variant — handlers unmarshal
			// arguments themselves. A params shape that is not CallToolParamsRaw
			// still gets recorded, with whatever name is knowable (possibly
			// none): unknown/garbled calls are observations too.
			var toolName string
			var rawArgs json.RawMessage
			if params, ok := req.GetParams().(*mcp.CallToolParamsRaw); ok && params != nil {
				toolName = params.Name
				rawArgs = params.Arguments
			}
			call := mcpobserve.Call{
				Direction: "inbound",
				Server:    "pogo",
				Tool:      toolName,
				// Served under the name the client sent — there is no provider
				// alphabet between this server and the wire.
				WireTool: toolName,
			}
			if extra := req.GetExtra(); extra != nil {
				call.RequestID = extra.Header.Get(mcpObservationRequestHeader)
				if call.RequestID != "" {
					call.Transport = "streamable"
				}
			}
			if ss, ok := req.GetSession().(*mcp.ServerSession); ok && ss != nil {
				call.MCPSessionID = ss.ID()
				if ip := ss.InitializeParams(); ip != nil && ip.ClientInfo != nil {
					call.Client = ip.ClientInfo.Name
				}
			}
			if l.mgr != nil {
				if rec := l.mgr.MCPRecorder(); rec != nil {
					ctx = mcpobserve.WithRecorder(ctx, rec)
				}
				if rec := l.mgr.SkillRecorder(); rec != nil {
					ctx = skillobserve.WithRecorder(ctx, rec)
				}
			}
			// A direct inbound call is the external caller entering through
			// the MCP entrypoint. This describes only this request's context:
			// a task it submits starts its own Background-based run, which
			// re-marks itself internal — an MCP-originated task is never
			// classified external merely because MCP submitted it.
			ctx = skillobserve.WithSource(ctx, "external", "mcp")
			traceReq, _ := req.(*mcp.CallToolRequest)
			ctx = withMCPRequestTrace(ctx, traceReq)
			md := mcpobserve.MetadataFrom(ctx)
			md.Entrypoint = "mcp"
			md.AgentID, md.AgentName = "", ""
			if l.mgr != nil && l.agentID != "" {
				if a, ok := l.mgr.studio.get(l.agentID); ok {
					md.AgentID, md.AgentName = a.ID, a.Name
				}
			}
			ctx = mcpobserve.WithMetadata(ctx, md)
			ctx, span := mcpobserve.Start(ctx, call)
			defer span.End("interrupted", "unknown_outcome")
			ctx = context.WithValue(ctx, inboundSpanKey{}, span)

			// Resolve only known local tasks; prompts and other payloads stay
			// untouched and unknown task arguments never enter the record.
			if strings.HasPrefix(toolName, "agent_task_") && len(rawArgs) > 0 {
				var meta struct {
					TaskID string `json:"task_id"`
				}
				if json.Unmarshal(rawArgs, &meta) == nil && meta.TaskID != "" {
					span.Correlate(l.observedTask(meta.TaskID))
				}
			}

			res, err := next(ctx, method, req)
			status, kind := "ok", ""
			switch {
			case err != nil:
				status, kind = "error", "protocol_error"
				switch {
				case errors.Is(ctx.Err(), context.Canceled), errors.Is(err, context.Canceled):
					status, kind = "cancelled", "cancelled"
				case errors.Is(ctx.Err(), context.DeadlineExceeded), errors.Is(err, context.DeadlineExceeded):
					status, kind = "timeout", "timeout"
				case toolName != "" && !l.toolAllowed(toolName):
					// The whitelist refusal is a protocol error on the wire;
					// its own category keeps it distinguishable from a broken
					// request in the monitor.
					status, kind = "error", "rejected"
				}
			case res == nil:
				// A request handler answering nil, nil violates the SDK's
				// contract (exactly one of result/error). Recording it as
				// success would fabricate an outcome nobody observed.
				status, kind = "error", "protocol_error"
			default:
				if ctr, ok := res.(*mcp.CallToolResult); !ok || ctr == nil {
					status, kind = "error", "protocol_error"
				} else if ctr.IsError {
					status, kind = "error", "tool_error"
				}
			}
			var diagnostic mcpobserve.Diagnostic
			if err != nil {
				diagnostic = mcpobserve.DiagnoseError(err, rawArgs)
			} else if ctr, ok := res.(*mcp.CallToolResult); ok && ctr != nil {
				diagnostic = mcpobserve.DiagnoseResult(ctr, rawArgs)
			} else if status != "ok" {
				diagnostic = mcpobserve.ExplicitDiagnostic("invalid_result", "MCP handler returned no valid tool result")
			}
			span.EndDiagnostic(status, kind, diagnostic)
			return res, err
		}
	}
}

// addSynthTools registers the manifest tools (mal.search and friends) as
// directly callable MCP tools — the "catalog is the tools/list" half of the
// §13 split. Only the registered subset is exposed: the unregistered long tail
// exists to keep per-turn schema cost down for the model, and the same economy
// applies to a remote client's context. Execution is the tool's own Execute —
// one code path for the model and for MCP, so a manifest fix reaches both.
//
// Names keep the manifest's dotted form (MCP allows dots; provider APIs do not,
// which is why the wire name differs — the dotted name is the honest identity
// here, where no provider sits in between).
func (l *mcpLayer) addSynthTools(srv *mcp.Server) {
	instances := map[string]tools.Tool{}
	for _, t := range l.mgr.SynthTools() {
		instances[t.Name()] = t // keyed by wire name (underscored)
	}
	l.synthNames = nil
	for _, info := range l.mgr.SynthCatalog() {
		if !info.Registered || info.InputSchema == nil {
			continue
		}
		t, ok := instances[info.WireName]
		if !ok {
			continue
		}
		l.synthNames = append(l.synthNames, info.Name)
		ann := &mcp.ToolAnnotations{
			Title:          info.Title,
			ReadOnlyHint:   info.ReadOnly,
			IdempotentHint: info.Idempotent,
		}
		if info.Destructive {
			ann.DestructiveHint = &info.Destructive
		}
		if info.OpenWorld {
			ann.OpenWorldHint = &info.OpenWorld
		}
		readOnly := info.ReadOnly
		name := info.Name
		srv.AddTool(&mcp.Tool{
			Name:        name,
			Title:       info.Title,
			Description: info.Description,
			InputSchema: info.InputSchema,
			Annotations: ann,
		}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			// The agent loop clears two bars before Execute — ValidateArgs, and
			// the policy's review matrix (web/policy.go) — and this channel
			// clears the same two, or the MCP path is a gate bypass. The
			// matrix's answer for a sessionless call: auto runs everything;
			// read-only runs when the sandbox backs the hint (the same fact the
			// loop's exemption rests on — an author's claim alone never earns
			// it); anything else is refused, because a direct call has no run
			// for a gate to park in. Drive it through agent_submit_task, where
			// a human can answer.
			if err := tools.ValidateArgs(t, req.Params.Arguments); err != nil {
				return mcpErr("invalid arguments: %v", err), nil
			}
			sandboxed := tools.InitSandbox().Active()
			// The policy is read per call, not captured at registration: a
			// file edit (PUT /api/mcp) flips it without a restart, and a
			// direct call must be judged by the rules in effect now.
			policy := l.effectivePolicy()
			allowed := policy == ModeAuto ||
				(readOnly && policy != ModeStrict && sandboxed)
			if !allowed {
				l.audit("tool %s refused: policy=%s sandbox=%v read_only=%v", name, policy, sandboxed, readOnly)
				return mcpErr("tool %s is reviewable under this deployment's %s policy and a direct MCP call has no approval gate; run it through agent_submit_task, where a human can answer", name, policy), nil
			}
			// A synth tool's contract is the agent loop's: a failed call is a
			// result the caller can read, not a protocol error.
			res, err := t.Execute(ctx, req.Params.Arguments)
			l.audit("tool %s args_bytes=%d failed=%v", name, len(req.Params.Arguments), err != nil)
			if err != nil {
				return &mcp.CallToolResult{
					Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}},
					IsError: true,
				}, nil
			}
			return &mcp.CallToolResult{
				Content:           []mcp.Content{&mcp.TextContent{Text: res.Text}},
				StructuredContent: res.Details,
			}, nil
		})
	}
}

// resyncSynth re-registers the manifest tools after a skill reload. The
// remove-everything-then-readd shape is deliberate: diffing old against new
// would save nothing (reloads are rare, human-driven events) while adding a
// second list that can drift from the one addSynthTools builds. The SDK
// debounces the pair into one tools/list_changed.
func (l *mcpLayer) resyncSynth() {
	if l.srv == nil {
		return
	}
	if names := append([]string(nil), l.synthNames...); len(names) > 0 {
		l.srv.RemoveTools(names...)
	}
	l.addSynthTools(l.srv)
}

// --- forwarded external tools -------------------------------------------------
//
// The pool's connected servers, re-exposed on this server's own /mcp under
// the dotted "<server>.<tool>" name — the reverse direction of the client
// half, so an operator can hand a remote agent exactly the slice of the
// connected tools the whitelist names and nothing more.

// mcpBuiltinTools is the static copy of the agent_* surface server()
// registers, in registration order. Two consumers: serve_catalog's builtin
// section (GET /api/mcp) and the forward registration's collision set. It is
// a hand-maintained mirror — the SDK offers no read-back of the registered
// set — so the serve-catalog test pins it against a live tools/list.
var mcpBuiltinTools = []mcpServeBuiltinTool{
	{Name: "agent_submit_task", Title: "Submit a task to the agent"},
	{Name: "agent_task_status", Title: "Poll a task"},
	{Name: "agent_task_result", Title: "Read a finished task's answer"},
	{Name: "agent_task_cancel", Title: "Cancel a task"},
	{Name: "agent_task_wait", Title: "Wait for a task to finish"},
	{Name: "agent_task_steer", Title: "Steer a running task"},
	{Name: "agent_task_approve", Title: "Answer a pending approval"},
	{Name: "agent_task_list", Title: "List this channel's tasks"},
	{Name: "agent_task_file", Title: "Read a file from a task's workspace"},
}

// forwardToolName maps an external server's tool to the name this server
// exposes it under: "<server>.<tool>", the same dotted family as the manifest
// tools' "mal.search", so one include_tools pattern language covers both
// ("filesystem.*" for a whole server, "filesystem.read_file" for one tool).
// Server names are already confined to [A-Za-z0-9_-] by mcpclient's
// validation; the tool half is whatever the server sent, so characters
// outside the MCP name alphabet fold to "_" — the wire-name mapping's rule,
// minus the hash tail: this name is for humans and whitelist patterns, and a
// collision the folding creates is caught loudly at registration rather than
// hidden behind a hash.
func forwardToolName(server, tool string) string {
	safe := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '_', r == '-', r == '.':
			return r
		default:
			return '_'
		}
	}, tool)
	return server + "." + safe
}

// forwardSignature fingerprints everything addForwardTools reads — the
// whitelist's own words plus, per connected server, every exposed name with
// its definition fingerprint — so ensureForwardSync can tell "nothing
// registration would read has changed" apart from "rebuild". A pool
// reconnect or catalog refresh moves it; an idle pool does not.
func forwardSignature(sc *mcpclient.ServeConfig, cats []mcpclient.ServerCatalog) string {
	var b strings.Builder
	if sc != nil {
		for _, p := range sc.IncludeTools {
			b.WriteString(p)
			b.WriteByte('\n')
		}
	}
	b.WriteByte(0)
	for _, cat := range cats {
		if cat.Status.State != mcpclient.StateConnected {
			continue
		}
		for _, t := range cat.Tools {
			b.WriteString(forwardToolName(cat.Status.Name, t.ServerToolName()))
			b.WriteByte('#')
			b.WriteString(t.DefinitionFingerprint())
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// addForwardTools registers the whitelisted slice of the connected servers'
// catalogs as directly callable MCP tools. Registration is strictly opt-in:
// an empty include_tools keeps the historical surface (agent_* plus the synth
// passthrough) and nothing else — an existing deployment must not gain a
// network-visible alias for its MCP tools by upgrading. A name the whitelist
// does not match is never registered at all, so the exposed set cannot drift
// wider than the list between edits.
//
// Only connected servers contribute: a disconnected one has no session to
// carry the call, and a greyed placeholder on tools/list only invites calls
// that cannot succeed. Collisions — against the builtin agent_* names, the
// synth names, or an earlier forward name the sanitization folded onto this
// one — skip the tool with an audit line rather than replace silently:
// AddTool replaces a same-named tool without complaint, so the check is ours
// to make.
//
// The handler is addSynthTools's twin — same ValidateArgs, same policy bar
// for a sessionless call, same result mapping. Whether the tool's code lives
// in a skill bundle or on another server, a direct MCP call clears the two
// bars the agent loop clears, or this path is a gate bypass around the server
// it fronts.
func (l *mcpLayer) addForwardTools(srv *mcp.Server, sc *mcpclient.ServeConfig, cats []mcpclient.ServerCatalog) {
	l.forwardNames = nil
	if sc == nil || len(sc.IncludeTools) == 0 {
		return
	}
	taken := map[string]bool{}
	for _, b := range mcpBuiltinTools {
		taken[b.Name] = true
	}
	for _, name := range l.synthNames {
		taken[name] = true
	}
	for _, cat := range cats {
		if cat.Status.State != mcpclient.StateConnected {
			continue
		}
		for _, t := range cat.Tools {
			name := forwardToolName(cat.Status.Name, t.ServerToolName())
			if len(name) > 128 {
				// The MCP name ceiling (the SDK's validateToolName). A server
				// this verbose loses the tool, loudly — never truncated,
				// which would be a second name for the same thing.
				l.audit("forwarded tool %s skipped: name exceeds 128 characters", cat.Status.Name+"."+t.ServerToolName())
				continue
			}
			if !sc.Includes(name) {
				continue
			}
			if taken[name] {
				l.audit("forwarded tool %s skipped: name already registered", name)
				continue
			}
			taken[name] = true
			l.forwardNames = append(l.forwardNames, name)
			tool := t
			readOnly := t.IsReadOnly()
			srv.AddTool(&mcp.Tool{
				Name:        name,
				Title:       t.DisplayTitle(),
				Description: t.Description(),
				InputSchema: t.InputSchema(),
				Annotations: t.Annotations(),
				// No OutputSchema: what this handler returns as structured
				// content is the call's details envelope, not the upstream
				// result shape — advertising the server's schema would be a
				// lie the SDK would happily serve.
			}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				// The gate, verbatim from addSynthTools: auto runs
				// everything; read-only runs when the sandbox backs the
				// hint; anything else is refused because a direct call has
				// no run for a gate to park in — agent_submit_task is where
				// a human can answer. The policy is read per call, so a PUT
				// /api/mcp edit binds the next call without a restart.
				if err := tools.ValidateArgs(tool, req.Params.Arguments); err != nil {
					return mcpErr("invalid arguments: %v", err), nil
				}
				sandboxed := tools.InitSandbox().Active()
				policy := l.effectivePolicy()
				allowed := policy == ModeAuto ||
					(readOnly && policy != ModeStrict && sandboxed)
				if !allowed {
					l.audit("forwarded tool %s refused: policy=%s sandbox=%v read_only=%v", name, policy, sandboxed, readOnly)
					return mcpErr("tool %s is reviewable under this deployment's %s policy and a direct MCP call has no approval gate; run it through agent_submit_task, where a human can answer", name, policy), nil
				}
				// The wrapper's own Execute carries the timeout, the
				// outbound observation and the result flattening; a failed
				// call stays a result the caller can read, per the loop's
				// contract restated at mcpErr.
				res, err := tool.Execute(ctx, req.Params.Arguments)
				l.audit("forwarded tool %s args_bytes=%d failed=%v", name, len(req.Params.Arguments), err != nil)
				if err != nil {
					return &mcp.CallToolResult{
						Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}},
						IsError: true,
					}, nil
				}
				return &mcp.CallToolResult{
					Content:           []mcp.Content{&mcp.TextContent{Text: res.Text}},
					StructuredContent: res.Details,
				}, nil
			})
		}
	}
}

// rebuildForwardLocked replaces the whole forwarded set in place and records
// the signature of the inputs it read — resyncSynth's shape, chosen for the
// same reason (a diff is a second list to keep honest; the SDK debounces the
// pair into one tools/list_changed). Callers hold forwardMu, construction
// excepted: server() runs before the layer is published.
func (l *mcpLayer) rebuildForwardLocked(sc *mcpclient.ServeConfig, cats []mcpclient.ServerCatalog) {
	if names := append([]string(nil), l.forwardNames...); len(names) > 0 {
		l.srv.RemoveTools(names...)
	}
	l.addForwardTools(l.srv, sc, cats)
	l.forwardSig = forwardSignature(sc, cats)
}

// resyncForward rebuilds the forwarded surface after PUT /api/mcp swapped the
// file config: the whitelist decides which external tools exist on this
// server at all, so an edit must register/remove them now rather than wait
// for the next tools/list. Per-agent sublayers carry no forwarded tools, and
// a layer that never built its server has nothing to rebuild.
func (l *mcpLayer) resyncForward() {
	if l.srv == nil || l.agentID != "" {
		return
	}
	l.forwardMu.Lock()
	defer l.forwardMu.Unlock()
	l.rebuildForwardLocked(l.fileConfig(), l.mgr.MCPCatalogs(nil, nil))
}

// ensureForwardSync is the self-healing half of the sync, run at the head of
// the whitelist middleware's tools/list branch: the pool has no callback into
// this layer, so the registered set is compared against the live catalog's
// signature and rebuilt only on drift. tools/list is rare enough that the
// comparison is free.
func (l *mcpLayer) ensureForwardSync() {
	if l.srv == nil || l.agentID != "" {
		return
	}
	sc := l.fileConfig()
	cats := l.mgr.MCPCatalogs(nil, nil)
	sig := forwardSignature(sc, cats)
	l.forwardMu.Lock()
	defer l.forwardMu.Unlock()
	if sig == l.forwardSig {
		return
	}
	l.rebuildForwardLocked(sc, cats)
}

// --- packaged runtime Skill tools --------------------------------------------
//
// Installed runtime Skill packages (pogo-skill.json bundles), re-exposed on
// the global /mcp endpoint under each contract's own tool name
// ("skill_malware_sample_search" — the name the agent loop sees, so a remote
// caller and a session share one vocabulary). Exposure is strictly opt-in,
// the forwarded tools' rule verbatim: an empty include_tools registers no
// contract tool at all, a non-empty one registers exactly the names it
// matches, and collisions against builtin/synth/forward names skip with an
// audit line rather than replace silently.

// mcpRuntimeSessionID is the pseudo session the global endpoint's runtime
// tools bind to. It exists only for the lock's session binding and the
// session-files/session-artifacts directories — one shared id across packages
// is what lets a fileId minted by skill_upload_file serve every skill_* tool.
const mcpRuntimeSessionID = "mcp-serve"

// runtimeService is the manager's packaged Skill runtime, nil when the
// prototype is off (or the layer was built around a nil manager, a test
// arrangement).
func (l *mcpLayer) runtimeService() *skillruntime.Service {
	if l.mgr == nil {
		return nil
	}
	return l.mgr.cfg.SkillRuntime
}

// runtimeSignature fingerprints everything addRuntimeTools reads — the
// whitelist's own words, each installed package's digest and revoked state,
// and executor availability — so ensureRuntimeSync can tell "nothing
// registration would read has changed" apart from "rebuild". An install,
// revoke, delete or a whitelist edit moves it; an idle catalog does not.
func runtimeSignature(svc *skillruntime.Service, sc *mcpclient.ServeConfig) string {
	var b strings.Builder
	if sc != nil {
		for _, p := range sc.IncludeTools {
			b.WriteString(p)
			b.WriteByte('\n')
		}
	}
	b.WriteByte(0)
	if svc != nil {
		fmt.Fprintf(&b, "executor=%v\n", svc.Executor != nil)
		for _, inst := range svc.Catalog() {
			b.WriteString(inst.Ref.PackageDigest)
			b.WriteByte('#')
			fmt.Fprintf(&b, "%v\n", inst.Revoked)
		}
	}
	return b.String()
}

func (l *mcpLayer) addRuntimeTools(srv *mcp.Server, sc *mcpclient.ServeConfig) {
	l.runtimeNames = nil
	l.runtimeSessions = nil
	svc := l.runtimeService()
	if svc == nil {
		return
	}
	installed := svc.Catalog()
	if len(installed) == 0 {
		return
	}
	// The two pipeline helpers are this server's own plumbing, the agent_*
	// tools' category: registered whenever an installed package exists to use
	// them, and filtered by the include_tools middleware like the rest of the
	// builtin surface — not gated on the whitelist at registration the way
	// the contract tools below are.
	l.addRuntimeHelperTools(srv)
	if sc == nil || len(sc.IncludeTools) == 0 {
		return
	}
	if svc.Executor == nil {
		// Hosts without the bwrap executor (non-Linux) cannot bind a session:
		// the catalog still lists the packages so the picker can plan, but
		// registration skips rather than advertising tools whose every call
		// would fail HOST_CAPABILITY_MISSING.
		l.audit("runtime skill tools skipped: executor unavailable")
		return
	}
	taken := map[string]bool{}
	for _, b := range mcpBuiltinTools {
		taken[b.Name] = true
	}
	for _, name := range l.synthNames {
		taken[name] = true
	}
	for _, name := range l.forwardNames {
		taken[name] = true
	}
	for _, name := range l.runtimeNames {
		taken[name] = true
	}
	for _, inst := range installed {
		if inst.Revoked {
			continue
		}
		id := skillruntime.PackageSkillID(inst.Ref)
		var selected []skillruntime.Descriptor
		for _, d := range svc.SkillTools(id) {
			if !sc.Includes(d.Name) {
				continue
			}
			if taken[d.Name] {
				l.audit("runtime tool %s skipped: name already registered", d.Name)
				continue
			}
			selected = append(selected, d)
		}
		if len(selected) == 0 {
			continue
		}
		bound, err := l.runtimeBoundSession(svc, inst)
		if err != nil {
			l.audit("runtime skill %s skipped: %v", id, err)
			continue
		}
		l.runtimeSessions = append(l.runtimeSessions, bound)
		for _, d := range selected {
			taken[d.Name] = true
			l.runtimeNames = append(l.runtimeNames, d.Name)
			tool := &tools.RuntimeTool{Session: bound, Descriptor: d, Identity: runtimeSkillIdentity(d.SkillID, d.SkillName)}
			name := d.Name
			readOnly := d.ReadOnly
			srv.AddTool(&mcp.Tool{
				Name:        name,
				Title:       d.ID, // the contract ID is the honest identity
				Description: d.Description,
				InputSchema: d.InputSchema,
				Annotations: &mcp.ToolAnnotations{ReadOnlyHint: readOnly},
			}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
				// The gate is addSynthTools' matrix with one substitution: a
				// runtime tool's confinement is its own bwrap sandbox (the
				// executor IS the isolation fact), so executor readiness —
				// not InitSandbox's bash-sandbox fact — backs the read-only
				// hint. auto runs everything; standard runs read-only
				// contracts while the executor is up; strict refuses
				// everything, because a direct call has no run for a gate to
				// park in. The policy is read per call, so a PUT /api/mcp
				// edit binds the next call without a restart.
				if err := tools.ValidateArgs(tool, req.Params.Arguments); err != nil {
					return mcpErr("invalid arguments: %v", err), nil
				}
				executorReady := svc.Executor != nil
				policy := l.effectivePolicy()
				allowed := policy == ModeAuto ||
					(readOnly && policy != ModeStrict && executorReady)
				if !allowed {
					l.audit("runtime tool %s refused: policy=%s executor=%v read_only=%v", name, policy, executorReady, readOnly)
					return mcpErr("tool %s is reviewable under this deployment's %s policy and a direct MCP call has no approval gate; run it through agent_submit_task, where a human can answer", name, policy), nil
				}
				// Same result contract as the synth passthrough: a failed
				// call is a result the caller can read, not a protocol error.
				res, err := tool.Execute(ctx, req.Params.Arguments)
				l.audit("runtime tool %s args_bytes=%d failed=%v", name, len(req.Params.Arguments), err != nil)
				if err != nil {
					return &mcp.CallToolResult{
						Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}},
						IsError: true,
					}, nil
				}
				return &mcp.CallToolResult{
					Content:           []mcp.Content{&mcp.TextContent{Text: res.Text}},
					StructuredContent: res.Details,
				}, nil
			})
		}
	}
}

// runtimeBoundSession builds the pseudo-session one package's contract tools
// execute through: plan the package-level binding, lock it under the pseudo
// session id, then open the session. The BoundSession is cached on the layer
// until the next resync — Call revalidates binding, lock and revocation on
// every invocation, so holding it between resyncs is safe.
func (l *mcpLayer) runtimeBoundSession(svc *skillruntime.Service, inst skillruntime.Installed) (*skillruntime.BoundSession, error) {
	plan, err := svc.PlanSelection(skillruntime.Selection{SkillIDs: []string{skillruntime.PackageSkillID(inst.Ref)}})
	if err != nil {
		return nil, err
	}
	lock, err := svc.MakeLock(plan.Binding, mcpRuntimeSessionID)
	if err != nil {
		return nil, err
	}
	return svc.Session(plan.Binding, lock, mcpRuntimeSessionID)
}

// addRuntimeHelperTools registers the two pipeline tools a remote caller
// needs to drive file-in/artifact-out contracts: upload (base64 → fileId) and
// artifact read-back. Like their agent_* counterparts they are plumbing, not
// reviewable operations — exempt from the direct-call policy matrix because
// they only move bytes between the caller and the pseudo session's private
// stores; the policy decision happens at the skill_* tool that consumes the
// fileId or mints the artifact. The include_tools middleware still filters
// them like the rest of the builtin surface.
func (l *mcpLayer) addRuntimeHelperTools(srv *mcp.Server) {
	srv.AddTool(&mcp.Tool{
		Name:        "skill_upload_file",
		Title:       "Upload a file to the Skill tools",
		Description: "Upload a file (base64, at most 32 MiB decoded) for the skill_* runtime tools. Returns a private fileId to pass as a file argument; never pass a host path to a Skill.",
		InputSchema: agentObject(map[string]any{"data_base64": agentString("Base64 file bytes")}, "data_base64"),
	}, l.skillUploadFile)
	srv.AddTool(&mcp.Tool{
		Name:        "skill_read_artifact",
		Title:       "Read a Skill artifact",
		Description: "Read a chunk of an artifact a skill_* runtime tool produced. Returns the artifact's name and media type plus base64 data, next_offset and eof; repeat to download the whole artifact.",
		InputSchema: agentObject(map[string]any{"artifact_id": agentString("Artifact ID returned by a Skill call"), "offset": map[string]any{"type": "integer", "minimum": 0}, "max_bytes": map[string]any{"type": "integer", "minimum": 1, "maximum": 1048576}}, "artifact_id"),
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, l.skillReadArtifact)
	l.runtimeNames = append(l.runtimeNames, "skill_upload_file", "skill_read_artifact")
}

// runtimeBound answers one live pseudo-session for the pipeline helpers — any
// package's, since they all share the pseudo session id and therefore the
// same session-files/session-artifacts directories. Nil when no bound session
// exists (executor missing, no whitelisted contract) or every one went stale
// (all packages revoked or deleted).
func (l *mcpLayer) runtimeBound() *skillruntime.BoundSession {
	l.runtimeMu.Lock()
	defer l.runtimeMu.Unlock()
	for _, b := range l.runtimeSessions {
		if b.Live() {
			return b
		}
	}
	return nil
}

// skillUploadFile is the global endpoint's version of agent_upload_file:
// same validation and rate-limit semantics, against the pseudo session
// instead of a conversation's bound session.
func (l *mcpLayer) skillUploadFile(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var in struct {
		Data string `json:"data_base64"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &in); err != nil {
		return mcpErr("invalid arguments: %v", err), nil
	}
	if len(in.Data) > base64.StdEncoding.EncodedLen(32<<20) {
		return mcpErr("file exceeds 32 MiB"), nil
	}
	if _, ok := l.submits.allow(); !ok {
		return mcpErr("MCP request rate limit reached"), nil
	}
	bound := l.runtimeBound()
	if bound == nil {
		return mcpErr("Skill execution is unavailable: no installed runtime Skill package is bound on this host"), nil
	}
	data, err := base64.StdEncoding.DecodeString(in.Data)
	if err != nil {
		return mcpErr("invalid base64"), nil
	}
	file, err := bound.Upload(data)
	if err != nil {
		return mcpErr("%v", err), nil
	}
	l.audit("skill_upload_file bytes=%d", len(data))
	return mcpOut(map[string]any{"fileId": file.FileID, "digest": file.Digest, "size": file.Size}, "file uploaded; pass the fileId to a skill_* tool"), nil
}

// skillReadArtifact is the global endpoint's version of agent_read_artifact,
// chunked base64 over the pseudo session's artifact store.
func (l *mcpLayer) skillReadArtifact(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var in struct {
		ArtifactID string `json:"artifact_id"`
		Offset     int    `json:"offset"`
		MaxBytes   int    `json:"max_bytes"`
	}
	if err := json.Unmarshal(req.Params.Arguments, &in); err != nil {
		return mcpErr("invalid arguments: %v", err), nil
	}
	bound := l.runtimeBound()
	if bound == nil {
		return mcpErr("Skill execution is unavailable: no installed runtime Skill package is bound on this host"), nil
	}
	artifact, err := bound.ReadArtifact(in.ArtifactID)
	if err != nil {
		return mcpErr("%v", err), nil
	}
	if in.Offset < 0 || in.Offset > len(artifact.Data) {
		return mcpErr("invalid artifact offset"), nil
	}
	if in.MaxBytes <= 0 {
		in.MaxBytes = 256 << 10
	}
	end := min(len(artifact.Data), in.Offset+min(in.MaxBytes, 1<<20))
	l.audit("skill_read_artifact artifact=%s offset=%d bytes=%d", in.ArtifactID, in.Offset, end-in.Offset)
	return mcpOut(map[string]any{"artifact_id": in.ArtifactID, "name": artifact.Artifact.Name, "media_type": artifact.Artifact.MediaType, "data_base64": base64.StdEncoding.EncodeToString(artifact.Data[in.Offset:end]), "next_offset": end, "eof": end == len(artifact.Data)}, "artifact chunk"), nil
}

// rebuildRuntimeLocked replaces the whole runtime set in place and records
// the signature of the inputs it read — rebuildForwardLocked's shape, chosen
// for the same reason (a diff is a second list to keep honest; the SDK
// debounces the pair into one tools/list_changed). Callers hold runtimeMu,
// construction excepted: server() runs before the layer is published.
func (l *mcpLayer) rebuildRuntimeLocked(sc *mcpclient.ServeConfig) {
	if names := append([]string(nil), l.runtimeNames...); len(names) > 0 {
		l.srv.RemoveTools(names...)
	}
	l.addRuntimeTools(l.srv, sc)
	l.runtimeSig = runtimeSignature(l.runtimeService(), sc)
}

// resyncRuntime rebuilds the runtime surface after the packaged Skill catalog
// changed (install/disable/delete fire Manager's OnRuntimeSkillChange) or PUT
// /api/mcp moved the whitelist — the same call cadence resyncForward keeps.
// Per-agent sublayers carry no runtime tools, and a layer that never built
// its server has nothing to rebuild.
func (l *mcpLayer) resyncRuntime() {
	if l.srv == nil || l.agentID != "" {
		return
	}
	l.runtimeMu.Lock()
	defer l.runtimeMu.Unlock()
	l.rebuildRuntimeLocked(l.fileConfig())
}

// ensureRuntimeSync is the self-healing half of the sync, run at the head of
// the whitelist middleware's tools/list branch next to ensureForwardSync:
// the registered set is compared against the live signature (whitelist,
// package digests and revoked flags, executor availability) and rebuilt only
// on drift.
func (l *mcpLayer) ensureRuntimeSync() {
	if l.srv == nil || l.agentID != "" {
		return
	}
	sc := l.fileConfig()
	sig := runtimeSignature(l.runtimeService(), sc)
	l.runtimeMu.Lock()
	defer l.runtimeMu.Unlock()
	if sig == l.runtimeSig {
		return
	}
	l.rebuildRuntimeLocked(sc)
}

// serveCatalog assembles GET /api/mcp's picker inventory: the four families
// this server may expose over /mcp, each named exactly as tools/list and
// include_tools know it, so the whitelist editor ticks rows instead of typing
// strings. Disconnected servers keep their rows (with their state and last
// error) — the picker greys a row rather than losing it, which is also how a
// whitelist entry pointing at a down server stays explainable. Revoked
// runtime packages keep theirs for the same reason.
func (l *mcpLayer) serveCatalog() *mcpServeCatalog {
	cat := &mcpServeCatalog{
		Builtin: append([]mcpServeBuiltinTool(nil), mcpBuiltinTools...),
		Skills:  []mcpServeSkillTool{},
		Servers: []mcpServeServer{},
		Runtime: []mcpServeRuntimePkg{},
	}
	for _, info := range l.mgr.SynthCatalog() {
		cat.Skills = append(cat.Skills, mcpServeSkillTool{Name: info.Name, Title: info.Title, Registered: info.Registered})
	}
	for _, sc := range l.mgr.MCPCatalogs(nil, nil) {
		row := mcpServeServer{
			Name:      sc.Status.Name,
			State:     sc.Status.State,
			LastError: sc.Status.LastError,
			Tools:     []mcpServeForwardRow{},
		}
		for _, t := range sc.Tools {
			row.Tools = append(row.Tools, mcpServeForwardRow{
				Name:        forwardToolName(sc.Status.Name, t.ServerToolName()),
				Remote:      t.ServerToolName(),
				Title:       t.DisplayTitle(),
				Description: t.Description(),
				ReadOnly:    t.IsReadOnly(),
			})
		}
		cat.Servers = append(cat.Servers, row)
	}
	// The packaged runtime Skills: one row per installed version, with each
	// contract's tool row under its skill_* name. The two pipeline helpers
	// join the builtin group while any package is installed — the same
	// condition addRuntimeTools registers them under, so the mirror stays
	// honest against a live tools/list.
	if svc := l.runtimeService(); svc != nil {
		installed := svc.Catalog()
		if len(installed) > 0 {
			cat.Builtin = append(cat.Builtin,
				mcpServeBuiltinTool{Name: "skill_upload_file", Title: "Upload a file to the Skill tools"},
				mcpServeBuiltinTool{Name: "skill_read_artifact", Title: "Read a Skill artifact"})
		}
		for _, inst := range installed {
			row := mcpServeRuntimePkg{
				ID:          skillruntime.PackageSkillID(inst.Ref),
				Name:        inst.Name,
				PackageName: inst.Name,
				Version:     inst.Ref.Version,
				State:       "available",
				Tools:       []mcpServeRuntimeTool{},
			}
			if svc.Executor == nil {
				row.State = "needs_configuration"
			}
			if inst.Revoked {
				row.State = "revoked"
			}
			for _, d := range svc.SkillTools(row.ID) {
				row.Tools = append(row.Tools, mcpServeRuntimeTool{
					Name:            d.Name,
					Contract:        d.ID,
					Description:     d.Description,
					ReadOnly:        d.ReadOnly,
					FileInputs:      d.FileInputs,
					WritesArtifacts: d.WritesArtifacts,
				})
			}
			cat.Runtime = append(cat.Runtime, row)
		}
	}
	return cat
}

// --- agent-level tools ------------------------------------------------------

type mcpSubmitIn struct {
	Prompt    string `json:"prompt"`
	SessionID string `json:"session_id"`
	Model     string `json:"model"`
	Workspace string `json:"workspace"`
	// ClientRef is the caller's idempotency key — Cursor's agentId, Stripe's
	// Idempotency-Key, the same shape. A resubmission of a ref that already
	// started a task returns that task instead of starting a second.
	ClientRef string `json:"client_ref"`
	// WebhookURL/WebhookSecret register a terminal-event push (validated before
	// the run starts). The push is a signal carrying ids and the outcome, never
	// the answer — the answer is pulled via agent_task_result.
	WebhookURL    string `json:"webhook_url"`
	WebhookSecret string `json:"webhook_secret"`
}

func (l *mcpLayer) submitTask(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var in mcpSubmitIn
	if len(req.Params.Arguments) > 0 {
		if err := json.Unmarshal(req.Params.Arguments, &in); err != nil {
			return mcpErr("invalid arguments: %v", err), nil
		}
	}
	in.Prompt = strings.TrimSpace(in.Prompt)
	if in.Prompt == "" {
		return mcpErr("prompt is required"), nil
	}
	if len(in.ClientRef) > 128 {
		return mcpErr("client_ref is capped at 128 bytes"), nil
	}
	if in.WebhookSecret != "" && in.WebhookURL == "" {
		return mcpErr("webhook_secret without a webhook_url signs nothing"), nil
	}
	if in.WebhookURL != "" {
		// Vet the push endpoint before the run starts, not after it ends: a bad
		// webhook is a submission error the caller should hear about now.
		if err := l.checkWebhookURL(in.WebhookURL); err != nil {
			return mcpErr("webhook_url: %v", err), nil
		}
	}

	refKey := in.ClientRef

	// Idempotency before any limiter: a replayed submission returns the task it
	// already started — a retry is not new demand and must not consume quota.
	// The ref only dedups onto its own session: a ref is per logical
	// submission, and a fresh prompt to a continued session needs a fresh ref.
	if refKey != "" {
		l.mu.Lock()
		existing := l.refs[refKey]
		l.mu.Unlock()
		if existing != "" && (in.SessionID == "" || in.SessionID == existing) {
			if task, view := l.viewOf(existing); task != nil {
				l.audit("submit deduplicated task=%s ref=%q", existing, in.ClientRef)
				// The replay names a task that already exists: correlating to
				// it is a fact about this call, not a new execution.
				if span := inboundSpanFrom(ctx); span != nil {
					span.Correlate(l.observedTask(existing))
				}
				return dedupOut(existing, view), nil
			}
		}
	}

	if wait, ok := l.submits.allow(); !ok {
		l.audit("submit refused: rate limit, retry in %s", wait.Round(time.Second))
		return mcpErr("submit rate limit reached; retry in %s", wait.Round(time.Second)), nil
	}

	var sess *Session
	fresh := in.SessionID == ""
	if fresh {
		var err error
		sess, err = l.createSession(in.Model, in.Workspace)
		if err != nil {
			return mcpErr("create session: %v", err), nil
		}
	} else {
		var err error
		sess, err = l.getSession(in.SessionID)
		if err != nil {
			return mcpErr("unknown session %s", in.SessionID), nil
		}
		if err := l.mgr.rejectBackgroundConversation(sess.ID); err != nil {
			return mcpErr("cannot continue background execution: %v", err), nil
		}
	}
	// Every MCP-driven run starts under this channel's policy, continued
	// sessions included — "whoever set it owns it" was the old rule, and it
	// let a machine caller inherit a human's /auto: continue an auto session,
	// run bash unreviewed. The channel's policy is the ceiling its runs get.
	// Read per submission: a policy edit (PUT /api/mcp) binds the next run,
	// not the process's start.
	sess.SetPolicy(l.effectivePolicy(), 0)

	// The subscription cursor is taken before Start so the watcher cannot miss
	// a run that fails before its goroutine gets scheduled: everything Start
	// publishes lands in the log at seqs greater than this one.
	from := sess.Hub().Snapshot().Seq

	task := &mcpTask{
		sessionID: sess.ID, startedMS: nowMS(), done: make(chan struct{}),
		webhookURL: in.WebhookURL, webhookSecret: in.WebhookSecret,
	}
	l.mu.Lock()
	// The idempotency reservation is re-checked inside the critical section:
	// the fast path above is a read, and two concurrent first-submits of one
	// ref would otherwise both pass it — check-then-act across an unlocked gap
	// is exactly how a retried submit becomes a double spend.
	if refKey != "" {
		if existing, ok := l.refs[refKey]; ok && existing != sess.ID {
			if t := l.tasks[existing]; t != nil {
				v := viewLocked(t)
				l.mu.Unlock()
				if fresh {
					_ = l.mgr.Delete(sess.ID)
				}
				l.audit("submit deduplicated task=%s ref=%q", existing, in.ClientRef)
				if span := inboundSpanFrom(ctx); span != nil {
					span.Correlate(l.observedTask(existing))
				}
				return dedupOut(existing, v), nil
			}
			// A ref whose task is gone is no reservation at all (pruneLocked
			// deletes them together; this is belt-and-braces): fall through.
		}
	}
	old := l.tasks[sess.ID]
	if old != nil && !old.terminal {
		l.mu.Unlock()
		return mcpErr("a run is already active; poll agent_task_status"), nil
	}
	// The concurrency cap is enforced inside the registry's critical section so
	// two racing submits cannot both wave themselves through.
	working := 0
	for _, t := range l.tasks {
		if !t.terminal {
			working++
		}
	}
	if working >= l.maxConcurrent {
		l.mu.Unlock()
		if fresh {
			// Roll the empty session back: a refused submission must not leave
			// one on disk.
			_ = l.mgr.Delete(sess.ID)
		}
		l.audit("submit refused: %d working tasks at the cap of %d", working, l.maxConcurrent)
		return mcpErr("concurrency limit reached: %d tasks working (cap %d); wait for one to finish or agent_task_cancel one", working, l.maxConcurrent), nil
	}
	if l.runSlots != nil {
		select {
		case l.runSlots <- struct{}{}:
			task.slotHeld = true
		default:
			l.mu.Unlock()
			if fresh {
				_ = l.mgr.Delete(sess.ID)
			}
			return mcpErr("MCP concurrency limit reached across Agent endpoints"), nil
		}
	}
	l.tasks[sess.ID] = task
	if refKey != "" {
		l.refs[refKey] = sess.ID
	}
	l.mu.Unlock()

	// The run continues this inbound call's trace: MetadataFrom already carries
	// the observe middleware's trace with this call as parent, and the task id
	// is the session's own (the registry's key), assigned here — never taken
	// from the caller's arguments. StartObserved overrides session/owner/run
	// with the authoritative local values regardless of what ctx claims.
	observed := mcpobserve.MetadataFrom(ctx)
	observed.TaskID = sess.ID
	runID, err := sess.StartObserved(in.Prompt, nil, "", observed)
	if err != nil {
		// Undo the registry insert: an entry with no run behind it would sit
		// non-terminal forever, holding a concurrency slot it never uses.
		l.mu.Lock()
		if task.slotHeld {
			<-l.runSlots
			task.slotHeld = false
		}
		if l.tasks[sess.ID] == task {
			if old != nil {
				l.tasks[sess.ID] = old
			} else {
				delete(l.tasks, sess.ID)
			}
			if refKey != "" {
				delete(l.refs, refKey)
			}
		}
		l.mu.Unlock()
		return mcpErr("start run: %v (a run may already be active; poll agent_task_status)", err), nil
	}
	l.mu.Lock()
	task.runID = runID
	l.mu.Unlock()
	go l.watch(sess, runID, from, task)
	// Correlate the inbound record with the IDs it allocated. Correlate only
	// fills correlation fields — the trace/parent the span started with stand.
	if span := inboundSpanFrom(ctx); span != nil {
		md := mcpobserve.Metadata{SessionID: sess.ID, RunID: runID, TaskID: sess.ID, AgentID: sess.studioID, AgentName: sess.studioName}
		if sess.owner != nil {
			md.OwnerID = sess.owner.SubjectID
		}
		span.Correlate(md)
	}

	cfg := sess.Model()
	out := map[string]any{
		"task_id":    sess.ID,
		"session_id": sess.ID,
		"run_id":     runID,
		"status":     "working",
		"model":      cfg.Model,
		"provider":   cfg.Provider,
	}
	if fresh {
		out["note"] = "poll agent_task_status; the task is visible to a human in the pogo web UI"
	}
	if in.WebhookURL != "" {
		out["webhook_registered"] = true
	}
	l.audit("submit task=%s ref=%q prompt_bytes=%d model=%s fresh=%v webhook=%v", sess.ID, in.ClientRef, len(in.Prompt), cfg.Model, fresh, in.WebhookURL != "")
	return mcpOut(out, "task %s submitted; poll agent_task_status until completed, failed or cancelled", sess.ID), nil
}

// watch folds the hub's event stream into the registry entry: everything up to
// and including the run's end, then out.
//
// The loop is resubscription-based rather than single-shot because the hub
// drops a subscriber that falls 256 events behind (subBuffer) by closing its
// channel — an event a poller then reads as "the run died" if the first
// version's read-through were the whole story. Falling behind is recoverable by
// design: the log replays from the last sequence number seen, so the watcher
// comes back with ?from=, exactly like a reconnecting browser. Only a *closed
// hub* — eviction or deletion, both of which end the run anyway — is terminal.
//
// run_end carries the run's id when it comes from the loop; the belt-and-
// braces abort consume publishes has none. Matching the start first keeps an
// anonymous end from being claimed by the wrong watcher. A snapshot in the
// backlog means the replay window rolled past us mid-run (>2000 logged
// events): the snapshot's own run state is the fallback, with end_reason
// "missed" saying where the detail went.
func (l *mcpLayer) watch(sess *Session, runID string, from int64, task *mcpTask) {
	last := from
	// started lives OUTSIDE the resubscription loop: after a drop the backlog
	// replays only events past `last`, so the run's start never reappears — a
	// per-round reset would leave started false forever, and since run_end
	// events carry no RunID from fromAgentEvent, no end would ever match. The
	// task would sit non-terminal for good: status stuck at working, the
	// concurrency slot held, the session pinned in memory by this very
	// subscription. Hoisting it is what makes resubscription a recovery.
	started := false
	for {
		backlog, ch, cancel := sess.Hub().Subscribe(last)
		var terminal *Event
		scan := func(e Event) bool {
			if e.Seq > last {
				last = e.Seq
			}
			switch e.Type {
			case EvSnapshot:
				snap := e.Snapshot
				if snap != nil && !snap.Run.Active {
					end := Event{Type: EvRunEnd, EndReason: "missed"}
					terminal = &end
					return true
				}
			case EvRunStart:
				if e.RunID == runID {
					started = true
				}
			case EvRunEnd:
				if started && (e.RunID == "" || e.RunID == runID) {
					terminal = &e
					return true
				}
			}
			return false
		}
		for _, e := range backlog {
			if scan(e) {
				break
			}
		}
		if terminal == nil {
			for e := range ch {
				if scan(e) {
					break
				}
			}
		}
		cancel()
		if terminal != nil {
			l.settle(sess.ID, *terminal)
			return
		}
		if sess.Hub().Closed() {
			// Nothing more will ever arrive; the run died with the session.
			l.settle(sess.ID, Event{Type: EvRunEnd, EndReason: string(agent.EndAborted), Error: "session closed before the run ended"})
			return
		}
		// Dropped for falling behind: around again, from the last seq.
	}
}

// settle records a terminal transition exactly once.
func (l *mcpLayer) settle(id string, e Event) {
	l.mu.Lock()
	defer l.mu.Unlock()
	t, ok := l.tasks[id]
	if !ok || t.terminal {
		return
	}
	t.status = mcpStatusFor(e.EndReason)
	t.endReason = e.EndReason
	t.disposition = string(agent.EndReason(e.EndReason).Disposition())
	t.errText = e.Error
	if e.Usage != nil {
		u := *e.Usage
		t.usage = &u
	}
	t.terminal = true
	if t.slotHeld {
		<-l.runSlots
		t.slotHeld = false
	}
	close(t.done)
	if t.webhookURL != "" {
		// Fired after the done close: a waiting caller is never queued behind
		// the network, and the push remains what it should be — a signal.
		go l.deliverWebhook(t.webhookURL, t.webhookSecret, id, t.status, t.endReason, t.disposition)
	}
	l.pruneLocked()
}

// terminalView is a copy of a task's settle-written fields. settle writes them
// under the registry mutex; every reader must take its copy under the same
// lock — reading them off the pointer after Unlock is a data race, and a
// poller hammering status while the watcher settles is the norm, not the edge.
type terminalView struct {
	terminal    bool
	status      string
	endReason   string
	disposition string
	errText     string
	usage       *llm.Usage
}

// viewLocked copies; the caller holds l.mu.
func viewLocked(t *mcpTask) terminalView {
	v := terminalView{
		terminal: t.terminal, status: t.status, endReason: t.endReason,
		disposition: t.disposition, errText: t.errText,
	}
	if t.usage != nil {
		u := *t.usage
		v.usage = &u
	}
	return v
}

// viewOf returns the registry entry plus its race-free terminal view. The
// pointer comes along only for what is immutable after creation (sessionID,
// runID, the done channel) — terminal state goes through the view.
func (l *mcpLayer) viewOf(id string) (*mcpTask, terminalView) {
	l.mu.Lock()
	defer l.mu.Unlock()
	t := l.tasks[id]
	if t == nil {
		return nil, terminalView{}
	}
	return t, viewLocked(t)
}

// dedupOut is the answer to a replayed client_ref: the original task's
// identity and current state, flagged so the caller knows no new run started.
func dedupOut(existing string, v terminalView) *mcp.CallToolResult {
	out := map[string]any{
		"task_id":      existing,
		"session_id":   existing,
		"deduplicated": true,
		"note":         "client_ref already submitted; this is the original task",
	}
	if v.terminal {
		out["status"] = v.status
	} else {
		out["status"] = "working"
	}
	return mcpOut(out, "client_ref already submitted as task %s; poll agent_task_status", existing)
}

// pruneLocked drops the oldest terminal entries past the cap. Working entries
// are immune: their pollers are the definition of "still needed".
func (l *mcpLayer) pruneLocked() {
	terminal := 0
	for _, t := range l.tasks {
		if t.terminal {
			terminal++
		}
	}
	for terminal > maxMCPTasks {
		var oldest *mcpTask
		for _, t := range l.tasks {
			if !t.terminal {
				continue
			}
			if oldest == nil || t.startedMS < oldest.startedMS {
				oldest = t
			}
		}
		if oldest == nil {
			return
		}
		delete(l.tasks, oldest.sessionID)
		// The idempotency key dies with the entry it points at — a ref whose
		// task is forgotten may legitimately be submitted again.
		for ref, id := range l.refs {
			if id == oldest.sessionID {
				delete(l.refs, ref)
			}
		}
		terminal--
	}
}

// mcpStatusFor maps the harness's end vocabulary onto the four-state machine
// the tools promise. Everything that is neither a clean completion nor an
// deliberate stop reads as failed, with end_reason and disposition carrying the
// "why" — the same split EndReason itself exists to make (see endreason.go).
func mcpStatusFor(endReason string) string {
	switch agent.EndReason(endReason) {
	case agent.EndCompleted:
		return "completed"
	case agent.EndAborted:
		return "cancelled"
	default:
		return "failed"
	}
}

func (l *mcpLayer) taskStatus(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	id, ok := mcpArg(req, "task_id")
	if !ok {
		return mcpErr("task_id is required"), nil
	}

	task, view := l.viewOf(id)

	out := map[string]any{"task_id": id}
	if task != nil {
		if !view.terminal {
			out["status"] = "working"
		} else {
			out["status"] = view.status
			out["end_reason"] = view.endReason
			out["disposition"] = view.disposition
			if view.errText != "" {
				out["error"] = view.errText
			}
			if view.usage != nil {
				out["usage"] = view.usage
			}
		}
	}

	// Live detail comes from the session regardless of registry state — a
	// restarted server loses the registry but not the sessions, and a poller
	// deserves the turn number either way.
	if sess, err := l.getSession(id); err == nil {
		snap := sess.Hub().Snapshot()
		if task == nil {
			// Recovered after a restart: the registry is gone, the transcript
			// is not. A run in flight is still working; anything else is
			// reported without an end_reason, and "recovered" says why.
			if snap.Run.Active {
				out["status"] = "working"
			} else if _, done := out["status"]; !done {
				out["status"] = "completed"
			}
			out["recovered"] = true
		}
		if out["status"] == "working" {
			out["turn"] = snap.Live.Turn
			out["model"] = snap.Run.Model
			if n := len(snap.Live.PendingGates); n > 0 {
				// A gate held open for a human is not "working" in any sense a
				// remote driver can act on — A2A's input-required, Devin's
				// waiting_for_approval and the Tasks extension's input_required
				// all name this state, so we do too. The polling contract is
				// untouched: terminal is still completed/failed/cancelled.
				out["status"] = "input_required"
				out["awaiting_approval"] = n
				out["pending_gates"] = pendingGatesView(snap.Live.PendingGates)
				out["note"] = "answer with agent_task_approve (per gate_id), or a human approves in the pogo web UI; the gate fails closed on timeout"
			}
			if tail := tailText(snap.Live.Text, 400); tail != "" {
				out["progress_text"] = tail
			}
		}
	} else if task == nil {
		return mcpErr("unknown task %s", id), nil
	}
	if out["status"] == "working" || out["status"] == "input_required" {
		// Save a poller from guessing: the cadence the Tasks extension calls
		// pollInterval. agent_task_wait exists so polling is the fallback, not
		// the default.
		out["poll_interval_seconds"] = 5
	}
	return mcpOut(out, "status: %v", out["status"]), nil
}

func (l *mcpLayer) taskResult(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	id, ok := mcpArg(req, "task_id")
	if !ok {
		return mcpErr("task_id is required"), nil
	}
	return l.resultFor(id, false)
}

// resultFor assembles the finished-task answer. fromWait relaxes the
// still-working refusal — agent_task_wait calls it after its own terminal
// detection, where a race between "run ended" and "registry settled" must not
// read as an error.
func (l *mcpLayer) resultFor(id string, fromWait bool) (*mcp.CallToolResult, error) {
	task, view := l.viewOf(id)
	if task != nil && !view.terminal && !fromWait {
		return mcpErr("task %s is still working; poll agent_task_status first", id), nil
	}

	sess, err := l.getSession(id)
	if err != nil {
		return mcpErr("unknown task %s", id), nil
	}
	snap := sess.Hub().Snapshot()
	if snap.Run.Active && !fromWait {
		return mcpErr("task %s is still working; poll agent_task_status first", id), nil
	}
	if task != nil && !view.terminal && !snap.Run.Active {
		// The run is over but the watcher has not settled the registry yet —
		// answering now would guess "completed" for a run that may have failed.
		// The watcher is one settle behind the hub event, so a short wait on
		// the done channel is what honesty costs here.
		select {
		case <-task.done:
			_, view = l.viewOf(id)
		case <-time.After(3 * time.Second):
		}
	}

	text := lastAssistantText(snap)
	out := map[string]any{
		"task_id": id,
		"status":  "completed",
	}
	if view.terminal {
		out["status"] = view.status
		out["end_reason"] = view.endReason
		out["disposition"] = view.disposition
		if view.errText != "" {
			out["error"] = view.errText
		}
		if view.usage != nil {
			out["usage"] = view.usage
		}
	} else if task == nil {
		out["recovered"] = true
	}

	if guarded, truncated := guardResultText(text); truncated {
		out["text"] = guarded
		out["truncated"] = true
		out["note"] = "answer clipped to fit; the full text is the session transcript: " + sess.Path()
		text = guarded
	} else {
		out["text"] = text
	}

	// The answer itself is the text content — that is what a calling model
	// reads — with the structured copy alongside for programmatic callers.
	if text == "" {
		out["note"] = "the run produced no assistant text (see end_reason/disposition)"
		return mcpOut(out, "no assistant text"), nil
	}
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: text}},
		StructuredContent: out,
	}, nil
}

func (l *mcpLayer) taskCancel(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	id, ok := mcpArg(req, "task_id")
	if !ok {
		return mcpErr("task_id is required"), nil
	}
	sess, err := l.getSession(id)
	if err != nil {
		return mcpErr("unknown task %s", id), nil
	}
	cancelled := sess.Cancel()
	l.audit("cancel task=%s cancelled=%v", id, cancelled)
	return mcpOut(map[string]any{"task_id": id, "cancelled": cancelled},
		"cancelled: %v", cancelled), nil
}

func (l *mcpLayer) taskSteer(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	id, ok := mcpArg(req, "task_id")
	if !ok {
		return mcpErr("task_id is required"), nil
	}
	var in struct {
		Text string `json:"text"`
	}
	if len(req.Params.Arguments) > 0 {
		if err := json.Unmarshal(req.Params.Arguments, &in); err != nil {
			return mcpErr("invalid arguments: %v", err), nil
		}
	}
	in.Text = strings.TrimSpace(in.Text)
	if in.Text == "" {
		return mcpErr("text is required"), nil
	}
	sess, err := l.getSession(id)
	if err != nil {
		return mcpErr("unknown task %s", id), nil
	}
	if err := l.mgr.rejectBackgroundConversation(sess.ID); err != nil {
		return mcpErr("cannot steer background execution: %v", err), nil
	}
	steered := sess.Steer(in.Text)
	l.audit("steer task=%s steered=%v text_bytes=%d", id, steered, len(in.Text))
	note := "delivered at the next turn boundary"
	if !steered {
		note = "no run in flight; continue with agent_submit_task and session_id instead"
	}
	return mcpOut(map[string]any{"task_id": id, "steered": steered, "note": note},
		"steered: %v", steered), nil
}

// taskApprove delivers a remote verdict into the same WebGate the browser
// answers — one gate, two answering channels, identical semantics, and the
// audit line names the machine channel.
func (l *mcpLayer) taskApprove(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var in struct {
		TaskID   string          `json:"task_id"`
		GateID   string          `json:"gate_id"`
		Decision string          `json:"decision"`
		Args     json.RawMessage `json:"args"`
		Feedback string          `json:"feedback"`
		Remember string          `json:"remember"`
	}
	if len(req.Params.Arguments) > 0 {
		if err := json.Unmarshal(req.Params.Arguments, &in); err != nil {
			return mcpErr("invalid arguments: %v", err), nil
		}
	}
	if strings.TrimSpace(in.TaskID) == "" {
		return mcpErr("task_id is required"), nil
	}
	allow := in.Decision == "approve"
	if !allow && in.Decision != "deny" {
		return mcpErr("decision must be approve or deny"), nil
	}
	if in.Remember != "" && in.Remember != "tool" && in.Remember != "command" {
		return mcpErr("remember must be tool or command"), nil
	}
	sess, err := l.getSession(in.TaskID)
	if err != nil {
		return mcpErr("unknown task %s", in.TaskID), nil
	}
	gateID := in.GateID
	if gateID == "" {
		// Convenience for the common case: exactly one card on the table.
		gates := sess.Hub().Snapshot().Live.PendingGates
		if len(gates) != 1 {
			return mcpErr("gate_id is required while %d gates are pending (see pending_gates in agent_task_status)", len(gates)), nil
		}
		gateID = gates[0].GateID
	}
	v := Verdict{Allow: allow, Args: in.Args, Reason: in.Feedback, Remember: in.Remember}
	if err := sess.Gate().Decide(gateID, v); err != nil {
		return mcpErr("%v", err), nil
	}
	l.audit("approve task=%s gate=%s allow=%v modified=%v remember=%q", in.TaskID, gateID, allow, len(in.Args) > 0, in.Remember)
	note := "the run continues"
	if !allow {
		note = "the refusal was fed back to the agent"
	}
	return mcpOut(map[string]any{
		"task_id": in.TaskID, "gate_id": gateID, "approved": allow, "note": note,
	}, "gate %s: %s", gateID, in.Decision), nil
}

// taskList answers "what has this channel submitted" from the registry —
// newest first, with a state filter for the caller that only wants terminal
// tasks (or only live ones). In-memory and capped, as the registry is.
func (l *mcpLayer) taskList(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var in struct {
		Status string `json:"status"`
		Limit  int    `json:"limit"`
	}
	if len(req.Params.Arguments) > 0 {
		if err := json.Unmarshal(req.Params.Arguments, &in); err != nil {
			return mcpErr("invalid arguments: %v", err), nil
		}
	}
	switch in.Status {
	case "", "working", "completed", "failed", "cancelled", "terminal":
	default:
		return mcpErr("status must be working, completed, failed, cancelled or terminal"), nil
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}

	type entry struct {
		id    string
		start int64
		view  terminalView
	}
	l.mu.Lock()
	entries := make([]entry, 0, len(l.tasks))
	for id, t := range l.tasks {
		entries = append(entries, entry{id: id, start: t.startedMS, view: viewLocked(t)})
	}
	l.mu.Unlock()
	sort.Slice(entries, func(i, j int) bool { return entries[i].start > entries[j].start }) // newest first

	tasks := make([]any, 0, limit)
	for _, e := range entries {
		status := "working"
		if e.view.terminal {
			status = e.view.status
		}
		if in.Status == "terminal" && status == "working" {
			continue
		}
		if in.Status != "" && in.Status != "terminal" && status != in.Status {
			continue
		}
		item := map[string]any{"task_id": e.id, "status": status, "started_at": e.start}
		if e.view.terminal {
			item["end_reason"] = e.view.endReason
		}
		tasks = append(tasks, item)
		if len(tasks) >= limit {
			break
		}
	}
	return mcpOut(map[string]any{"tasks": tasks, "count": len(tasks)}, "%d task(s)", len(tasks)), nil
}

// taskFile is the artifact channel: a path-guarded read of one file from the
// task's workspace. The guard's shape is the one the file tools live behind —
// resolve fully, then refuse anything that steps outside, by ".." or by link.
func (l *mcpLayer) taskFile(_ context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	var in struct {
		TaskID   string `json:"task_id"`
		Path     string `json:"path"`
		MaxBytes int64  `json:"max_bytes"`
	}
	if len(req.Params.Arguments) > 0 {
		if err := json.Unmarshal(req.Params.Arguments, &in); err != nil {
			return mcpErr("invalid arguments: %v", err), nil
		}
	}
	if strings.TrimSpace(in.TaskID) == "" {
		return mcpErr("task_id is required"), nil
	}
	if in.Path == "" {
		return mcpErr("path is required"), nil
	}
	sess, err := l.getSession(in.TaskID)
	if err != nil {
		return mcpErr("unknown task %s", in.TaskID), nil
	}
	readCap := in.MaxBytes
	if readCap <= 0 {
		readCap = 256 * 1024
	}
	if readCap > 1024*1024 {
		readCap = 1024 * 1024
	}

	base := filepath.Join(l.mgr.Cwd(), sess.Workspace())
	target, err := confineToWorkspace(base, in.Path)
	if err != nil {
		return mcpErr("%v", err), nil
	}
	data, err := os.ReadFile(target)
	if err != nil {
		return mcpErr("read %s: %v", in.Path, err), nil
	}
	out := map[string]any{
		"task_id": in.TaskID,
		"path":    in.Path,
		"size":    len(data),
	}
	content := data
	if int64(len(data)) > readCap {
		content = data[:readCap]
		out["truncated"] = true
		out["note"] = fmt.Sprintf("clipped to %d bytes; read again with a higher max_bytes (cap 1048576) or in slices", readCap)
	}
	text := strings.ToValidUTF8(string(content), "")
	out["content"] = text
	// The content rides as text — that is what a calling model reads — with the
	// structured copy alongside for programmatic callers.
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: text}},
		StructuredContent: out,
	}, nil
}

// confineToWorkspace resolves rel against base and refuses anything that lands
// outside — the symlink-aware version, so a planted link cannot smuggle a read
// out of the workspace any more than ".." can.
func confineToWorkspace(base, rel string) (string, error) {
	baseAbs, err := filepath.Abs(base)
	if err != nil {
		return "", err
	}
	baseResolved, err := filepath.EvalSymlinks(baseAbs)
	if err != nil {
		return "", fmt.Errorf("workspace: %v", err)
	}
	resolved, err := filepath.EvalSymlinks(filepath.Join(baseAbs, rel))
	if err != nil {
		return "", err // a missing file fails here, before any read
	}
	if resolved != baseResolved && !strings.HasPrefix(resolved, baseResolved+string(os.PathSeparator)) {
		return "", fmt.Errorf("path %q escapes the task workspace", rel)
	}
	return resolved, nil
}

// checkWebhookURL vets a caller-supplied push endpoint before it is stored:
// https only, and the host must resolve to public addresses — an agent
// harness's outbound POST is otherwise an SSRF probe of the LAN it sits on.
// (DNS is re-resolved at delivery, so a rebind between submit and settle is
// not covered; the honest layers for that are the signature and the
// payload's signal-not-answer discipline.)
func (l *mcpLayer) checkWebhookURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("invalid url %q", raw)
	}
	if l.webhookAllowInsecure {
		return nil // tests only; deliberately not env-wirable
	}
	if u.Scheme != "https" {
		return fmt.Errorf("must be https")
	}
	if u.User != nil {
		return fmt.Errorf("credentials in the url are not allowed")
	}
	ips, err := net.LookupIP(u.Hostname())
	if err != nil {
		return fmt.Errorf("host does not resolve: %v", err)
	}
	for _, ip := range ips {
		if !ip.IsGlobalUnicast() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsPrivate() || ip.IsMulticast() {
			return fmt.Errorf("host %s resolves to a non-public address %s", u.Hostname(), ip)
		}
	}
	return nil
}

// deliverWebhook POSTs the terminal signal, with retries on the layer's
// backoff schedule. A2A's discipline: the push says WHAT finished and carries
// the ids, never the answer — the answer is pulled via agent_task_result by a
// caller already holding the token.
func (l *mcpLayer) deliverWebhook(rawURL, secret, taskID, status, endReason, disposition string) {
	payload, err := json.Marshal(map[string]any{
		"event":       "task.finished",
		"task_id":     taskID,
		"status":      status,
		"end_reason":  endReason,
		"disposition": disposition,
		"occurred_at": time.Now().UnixMilli(),
	})
	if err != nil {
		return
	}
	delivery := randomID(8)
	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			if attempt > len(l.webhookBackoff) {
				l.audit("webhook gave up task=%s delivery=%s after %d attempts", taskID, delivery, attempt)
				return
			}
			time.Sleep(l.webhookBackoff[attempt-1])
		}
		if err := postWebhook(rawURL, secret, delivery, payload); err != nil {
			l.audit("webhook attempt %d failed task=%s delivery=%s: %v", attempt+1, taskID, delivery, err)
			continue
		}
		l.audit("webhook delivered task=%s delivery=%s attempts=%d", taskID, delivery, attempt+1)
		return
	}
}

// postWebhook makes one delivery attempt. Standard Webhooks' shape when a
// secret is set: the signature covers "<timestamp>.<body>", so a replayed old
// frame fails the timestamp check.
func postWebhook(rawURL, secret, delivery string, payload []byte) error {
	req, err := http.NewRequest(http.MethodPost, rawURL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Pogo-Delivery-Id", delivery)
	if secret != "" {
		ts := time.Now().Unix()
		mac := hmac.New(sha256.New, []byte(secret))
		fmt.Fprintf(mac, "%d.", ts)
		mac.Write(payload)
		req.Header.Set("X-Pogo-Signature", fmt.Sprintf("t=%d,v1=%x", ts, mac.Sum(nil)))
	}
	client := &http.Client{
		Timeout: 10 * time.Second,
		// No redirect chasing: a 30x to an internal address would re-open the
		// SSRF question submit-time validation closed.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("endpoint returned %s", resp.Status)
	}
	return nil
}

// taskWait blocks for a terminal status, streaming progress when the caller
// sent a progressToken. The design keeps three channels of truth apart:
//
//   - Termination prefers the registry's done channel (exact, settled once);
//     a 2s snapshot tick is the safety net for the registry-less recovered
//     case and for the watcher's rare "missed" path.
//   - Progress rides a hub subscription, resubscribing from the last seq on
//     drop — the same recovery a browser stream uses, for the same reason.
//   - ctx cancellation and the timeout are waits ending, never tasks ending:
//     the run's context is a different tree (Session.Start parents it on
//     Background), so nothing here can reach it.
func (l *mcpLayer) taskWait(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	id, ok := mcpArg(req, "task_id")
	if !ok {
		return mcpErr("task_id is required"), nil
	}
	var in struct {
		TimeoutSeconds float64 `json:"timeout_seconds"`
	}
	if len(req.Params.Arguments) > 0 {
		if err := json.Unmarshal(req.Params.Arguments, &in); err != nil {
			return mcpErr("invalid arguments: %v", err), nil
		}
	}
	timeout := 50 * time.Second // under the TS/Python SDKs' default request timeout
	if in.TimeoutSeconds > 0 {
		// Clamp on the float BEFORE converting: 1e300 seconds overflows the
		// int64 conversion to a negative, which the <1s branch would then read
		// as "one second" — the exact opposite of what the caller asked for.
		secs := in.TimeoutSeconds
		if secs > 1800 {
			secs = 1800
		}
		if secs < 1 {
			secs = 1
		}
		timeout = time.Duration(secs * float64(time.Second))
	}

	task, view := l.viewOf(id)
	sess, err := l.getSession(id)
	if task == nil && err != nil {
		return mcpErr("unknown task %s", id), nil
	}
	if task == nil {
		// Recovered task: no registry entry to close on, the snapshot tick is
		// the whole terminal detector.
		task = &mcpTask{sessionID: id, done: make(chan struct{})}
	}
	if view.terminal {
		return l.resultFor(id, true)
	}
	// A wait holds a goroutine, a hub subscription and a hanging HTTP request
	// for up to half an hour — unbounded waits are an unbounded resource
	// grant, and this channel gets none.
	select {
	case l.waitSlots <- struct{}{}:
		defer func() { <-l.waitSlots }()
	default:
		return mcpErr("too many concurrent waits (cap %d); poll agent_task_status at the suggested interval instead", cap(l.waitSlots)), nil
	}
	deadline := time.After(timeout)
	if sess == nil {
		// The session is gone but the registry entry is not yet settled (the
		// watcher will be along). No hub to subscribe to; the done channel,
		// the clock and the caller are the whole story.
		select {
		case <-ctx.Done():
			return mcpOut(map[string]any{"task_id": id, "status": "working", "interrupted": true},
				"wait interrupted; the task is unaffected"), nil
		case <-deadline:
			return l.waitSnapshot(id)
		case <-task.done:
			return l.resultFor(id, true)
		}
	}
	if snap := sess.Hub().Snapshot(); !snap.Run.Active && snap.Seq > 0 {
		return l.resultFor(id, true)
	}

	// The progress side-channel. progress must strictly increase per the
	// spec; a counter, not a percentage, because "how far along" is a thing
	// this harness genuinely does not know.
	var progress float64
	notify := func(msg string) {
		token := req.Params.GetProgressToken()
		if token == nil || req.Session == nil {
			return
		}
		progress++
		_ = req.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{
			ProgressToken: token,
			Progress:      progress,
			Message:       msg,
		})
	}

	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()

	last := sess.Hub().Snapshot().Seq
	lastTail := time.Now()
	for {
		backlog, ch, cancel := sess.Hub().Subscribe(last)
		process := func(e Event) {
			if e.Seq > last {
				last = e.Seq
			}
			switch e.Type {
			case EvTurnStart:
				notify(fmt.Sprintf("turn %d", e.Turn))
			case EvToolEnd:
				notify(fmt.Sprintf("tool %s finished", e.Name))
			case EvGateRequest:
				notify("waiting for a human approval in the pogo web UI")
			case EvToken:
				// The typing effect is not progress; a throttled tail of it
				// is. One per second is plenty for a caller whose model reads
				// words, not cursors.
				if time.Since(lastTail) >= time.Second {
					lastTail = time.Now()
					if tail := tailText(e.Text, 120); tail != "" {
						notify("… " + tail)
					}
				}
			case EvRunEnd:
				notify("run ended: " + e.EndReason)
			}
		}
		for _, e := range backlog {
			process(e)
		}

		var done chan struct{}
		if task != nil {
			done = task.done
		}
		// On a terminal signal the events that ended the run are already in this
		// subscription's buffer (the hub publishes before the watcher can settle
		// the registry, and a select between two ready cases is a coin flip).
		// Drain them before leaving, or the caller's progress stream loses the
		// run's final events whenever it was a step behind.
		drain := func() {
			for {
				select {
				case e, open := <-ch:
					if !open {
						return
					}
					process(e)
				default:
					return
				}
			}
		}
		terminal := false
	inner:
		for {
			select {
			case <-ctx.Done():
				cancel()
				out := map[string]any{"task_id": id, "status": "working", "interrupted": true}
				if n := len(sess.Hub().Snapshot().Live.PendingGates); n > 0 {
					// "working" would be a lie here: the run is parked on a
					// human, and the caller deserves to know on its way out.
					out["status"] = "input_required"
					out["awaiting_approval"] = n
				}
				return mcpOut(out, "wait interrupted; the task is unaffected"), nil
			case <-deadline:
				cancel()
				return l.waitSnapshot(id)
			case <-done:
				drain()
				terminal = true
				break inner
			case <-tick.C:
				if !sess.Hub().Snapshot().Run.Active {
					drain()
					terminal = true
					break inner
				}
			case e, open := <-ch:
				if !open {
					break inner // dropped for falling behind; around again
				}
				process(e)
			}
		}
		cancel()
		if terminal {
			return l.resultFor(id, true)
		}
		if sess.Hub().Closed() {
			return l.resultFor(id, true)
		}
	}
}

// waitSnapshot answers a timed-out wait with the status view, not an error:
// "still working after your timeout" is the tool doing its job.
func (l *mcpLayer) waitSnapshot(id string) (*mcp.CallToolResult, error) {
	task, view := l.viewOf(id)
	out := map[string]any{"task_id": id, "status": "working", "timed_out": true}
	if view.terminal {
		return l.resultFor(id, true)
	}
	if sess, err := l.getSession(id); err == nil {
		snap := sess.Hub().Snapshot()
		if !snap.Run.Active {
			return l.resultFor(id, true)
		}
		out["turn"] = snap.Live.Turn
		if n := len(snap.Live.PendingGates); n > 0 {
			// Same naming as agent_task_status: waiting on a human is its own
			// state, not silent working.
			out["status"] = "input_required"
			out["awaiting_approval"] = n
			out["pending_gates"] = pendingGatesView(snap.Live.PendingGates)
		}
	} else if task == nil {
		return mcpErr("unknown task %s", id), nil
	}
	return mcpOut(out, "still working after the timeout; wait again or poll"), nil
}

// --- helpers ----------------------------------------------------------------

func boolPtr(b bool) *bool { return &b }

// pendingGatesView renders the pending approvals for a remote approver: the
// same facts the web card shows — gate id, tool, the exact arguments as text
// (capped), the danger hits and the deadline. Claude's canUseTool precedent:
// the ticket carries the snapshot, the answer carries only the decision.
func pendingGatesView(gates []PendingGate) []any {
	const maxGateArgs = 4 * 1024
	out := make([]any, 0, len(gates))
	for _, g := range gates {
		entry := map[string]any{
			"gate_id":  g.GateID,
			"tool":     g.Tool,
			"deadline": g.Deadline,
		}
		if len(g.Args) > 0 {
			args := string(g.Args)
			if len(args) > maxGateArgs {
				args = strings.ToValidUTF8(args[:maxGateArgs], "")
				entry["args_truncated"] = true
			}
			entry["args"] = args
		}
		if len(g.Danger) > 0 {
			entry["danger"] = g.Danger
		}
		if g.Origin != "" {
			entry["origin"] = g.Origin
		}
		out = append(out, entry)
	}
	return out
}

// mcpArg reads one string argument out of raw tool arguments.
func mcpArg(req *mcp.CallToolRequest, name string) (string, bool) {
	var args map[string]any
	if len(req.Params.Arguments) > 0 {
		if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
			return "", false
		}
	}
	v, ok := args[name]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok && s != ""
}

// mcpErr and mcpOut build results the way the rest of pogo treats tool
// failures: an execution error is a result the caller can read and react to,
// never a protocol error — the loop's "tool failures never kill the run",
// restated for a remote driver.
func mcpErr(format string, args ...any) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf(format, args...)}},
		IsError: true,
	}
}

func mcpOut(structured map[string]any, format string, args ...any) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content:           []mcp.Content{&mcp.TextContent{Text: fmt.Sprintf(format, args...)}},
		StructuredContent: structured,
	}
}

// mcpBuildVersion is the version the MCP handshake reports: the module version
// of an installed build, "dev" for a local one (ReadBuildInfo's "(devel)").
func mcpBuildVersion() string {
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	return "dev"
}

// lastAssistantText returns the text blocks of the last assistant message, the
// run's answer. Tool-call-only tails (a run that died mid-batch) yield "" and
// the caller says so rather than guessing.
func lastAssistantText(snap Snapshot) string {
	for i := len(snap.Messages) - 1; i >= 0; i-- {
		m := snap.Messages[i]
		if m.Role != string(llm.RoleAssistant) {
			continue
		}
		var sb strings.Builder
		for _, b := range m.Content {
			if b.Type == llm.BlockText {
				if sb.Len() > 0 {
					sb.WriteByte('\n')
				}
				sb.WriteString(b.Text)
			}
		}
		return sb.String()
	}
	return ""
}

// tailText clips a live partial answer to the last n bytes, on a line boundary
// when one is near — the same shape tailOf gives the hub's pending output.
func tailText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[len(s)-n:]
	s = strings.ToValidUTF8(s, "")
	if i := strings.IndexByte(s, '\n'); i >= 0 && i < 200 {
		s = s[i+1:]
	}
	return s
}

// guardResultText caps a finished answer the way the agent's own tool outputs
// are capped (tools.MaxBytes is the precedent): a remote caller's context is
// as finite as the model's. Head-biased — reports open with their conclusions
// stated up front far more often than they end with them — with a tail kept
// because "truncated" mid-sentence plus the final section beats losing the
// ending entirely.
const (
	mcpResultHead = 48 * 1024
	mcpResultTail = 16 * 1024
)

func guardResultText(s string) (string, bool) {
	if len(s) <= mcpResultHead+mcpResultTail {
		return s, false
	}
	head := strings.ToValidUTF8(s[:mcpResultHead], "")
	tail := strings.ToValidUTF8(s[len(s)-mcpResultTail:], "")
	return head + "\n\n[…clipped " + fmt.Sprintf("%d", len(s)-mcpResultHead-mcpResultTail) + " bytes…]\n\n" + tail, true
}
