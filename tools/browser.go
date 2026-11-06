package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/yosukeno/pogo/llm"
)

// BrowserLink connects the model-facing tool to its authorized executor:
// a native browser service, directly or through a delegated parent channel. The tool does not depend on the transport or browser ownership.
type BrowserLink interface {
	// Execute runs one operation and waits for the worker's answer. Errors are
	// channel failures — no browser connected, no browser task armed, timeout,
	// cancelled — while page-level failures (a stale ref, a refused navigation)
	// come back inside the reply with OK=false, because those are information
	// the model can act on, not infrastructure faults. The Browser tool converts
	// that flag to a failed tool result while retaining those observations.
	Execute(ctx context.Context, op json.RawMessage) (BrowserReply, error)
}

// BrowserReply is a native executor result, also carried by delegated links.
type BrowserReply struct {
	Backend string `json:"backend,omitempty"`
	// ContextRef is optional display identity captured by the authenticated
	// channel. It never grants page ownership or becomes model evidence.
	ContextRef string `json:"context_ref,omitempty"`
	ActiveTab  string `json:"active_tab,omitempty"`
	Version    string `json:"version,omitempty"`
	// Control fields describe only an explicit page-claim result. They never
	// provide a human input lease or implicitly grant execution authority.
	Control      string `json:"control,omitempty"`
	ControlEpoch *int64 `json:"control_epoch,omitempty"`
	OK           bool   `json:"ok"`
	Error        string `json:"error,omitempty"`
	URL          string `json:"url,omitempty"`
	Title        string `json:"title,omitempty"`
	// Favicon is bounded image data observed with this page URL. It is retained
	// only in UI details; model text and evidence never include icon bytes.
	Favicon string `json:"favicon,omitempty"`
	// Text is the serialized page state — the snapshot's indexed element list,
	// or the matching lines of a later action's built-in mini snapshot.
	Text string `json:"text,omitempty"`
	// Data carries structured diagnostics and evaluation results. Keep JSON
	// numbers and fields intact.
	Data json.RawMessage `json:"data,omitempty"`
	// Evidence is a native, bounded visible-DOM observation or a calculation
	// using worker-owned quantity references. It is not a page-supplied hook.
	Evidence json.RawMessage `json:"evidence,omitempty"`
	// Interactive is the count of indexed elements on the current page, the
	// pulse check that tells the model a click landed somewhere new.
	Interactive int `json:"interactive,omitempty"`
	// Scroll says where the viewport sits ("scroll 40% (800/2000px)"), which is
	// what turns a budget-truncated snapshot into something the model can act
	// on rather than merely notice.
	Scroll string `json:"scroll,omitempty"`
	// Dialogs reports page modals. Native dialogs remain pending for human
	// intervention. Reporting is not approval.
	Dialogs []string `json:"dialogs,omitempty"`
	// Steps carries a batch's per-step outcomes, in order, ending at the first
	// failure. Naming the step that stopped is the difference between a plan the
	// model can resume and one it has to restart.
	Steps      []BrowserStep      `json:"steps,omitempty"`
	Screenshot *BrowserScreenshot `json:"screenshot,omitempty"`
	// OpenedTabs lists new tabs observed during the action. ActiveTab identifies
	// the page this reply observes; FromTab identifies where the action began,
	// without asserting that the source tab still exists or remains unchanged.
	OpenedTabs []BrowserTabRef `json:"opened_tabs,omitempty"`
	FromTab    string          `json:"from_tab,omitempty"`
	// PendingRequests counts requests observed on related pages during the
	// action that remained pending when the bounded settle period ended.
	// This observation establishes neither causation nor business completion.
	PendingRequests int `json:"pending_requests,omitempty"`
	SettleMS        int `json:"settle_ms,omitempty"`
	// ObservationPending distinguishes a dispatched action from a complete
	// observation of its outcome. It never authorizes replaying that action.
	ObservationPending bool `json:"observation_pending,omitempty"`
	// Timing is diagnostic metadata, not page evidence or model-facing prose.
	Timing *BrowserTiming `json:"timing,omitempty"`
	Batch  *BrowserBatch  `json:"batch,omitempty"`
}

// BrowserTiming reports worker-side monotonic durations in milliseconds.
// ExecutionMS excludes queueing. SnapshotMS includes its evidence work, while
// EvidenceMS also includes direct extract evidence; stages are nested and must
// not all be added together. These values do not include model/provider time.
type BrowserTiming struct {
	TotalMS     float64 `json:"total_ms"`
	QueueMS     float64 `json:"queue_ms"`
	ExecutionMS float64 `json:"execution_ms"`
	// DispatchMS is the phase before post-action observation, including
	// Playwright's automatic waits; it is not pure input transport latency.
	DispatchMS float64 `json:"dispatch_ms"`
	// NavigationMS covers explicit navigate/back and new-tab preparation. It
	// is nested in DispatchMS, not all network work triggered by every click.
	NavigationMS float64 `json:"navigation_ms"`
	// ConditionWaitMS covers explicit wait conditions and observation-retry
	// DOMContentLoaded waits. Normal wait conditions are also in DispatchMS.
	ConditionWaitMS             float64 `json:"condition_wait_ms"`
	SettleMS                    float64 `json:"settle_ms"`
	SnapshotMS                  float64 `json:"snapshot_ms"`
	EvidenceMS                  float64 `json:"evidence_ms"`
	SnapshotCount               int     `json:"snapshot_count"`
	EvidenceCount               int     `json:"evidence_count"`
	IntermediateEvidenceSkipped int     `json:"intermediate_evidence_skipped"`
}

// BrowserBatch describes execution coverage, not business completion. Steps
// contains only attempted actions; skipped actions must not be marked as run.
type BrowserBatch struct {
	Requested  int    `json:"requested"`
	Executed   int    `json:"executed"`
	Skipped    int    `json:"skipped"`
	StopReason string `json:"stop_reason"`
}

// BrowserTabRef names a tab in a reply.
type BrowserTabRef struct {
	ID  string `json:"id"`
	URL string `json:"url,omitempty"`
}

// BrowserScreenshot carries bytes only between the authenticated runtime and
// the session store. The session replaces Data with ImageRef before a tool
// result is published or persisted.
type BrowserScreenshot struct {
	Data     string `json:"data,omitempty"`
	Mime     string `json:"mime"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	ImageRef string `json:"image_ref,omitempty"`
}

// BrowserStep is one step's outcome inside a batch.
type BrowserStep struct {
	Action     string  `json:"action"`
	OK         bool    `json:"ok"`
	Error      string  `json:"error,omitempty"`
	Text       string  `json:"text,omitempty"`
	DurationMS float64 `json:"duration_ms,omitempty"`
}

// BrowserStepResult is the diagnostic subset retained by the host. Intermediate
// page text is not copied into UI details merely because a batch was timed.
type BrowserStepResult struct {
	Action     string  `json:"action"`
	OK         bool    `json:"ok"`
	Error      string  `json:"error,omitempty"`
	DurationMS float64 `json:"duration_ms,omitempty"`
}

// Browser actions, split by whether they can change the page. The split is
// the documented taxonomy of the action set; whether page operations may run
// at all is the channel's decision (web.BrowserGate), which asks once per run
// rather than per action.
const (
	BrowserActionSnapshot     = "snapshot"
	BrowserActionClick        = "click"
	BrowserActionType         = "type"
	BrowserActionNavigate     = "navigate"
	BrowserActionWait         = "wait"
	BrowserActionExtract      = "extract"
	BrowserActionScroll       = "scroll"
	BrowserActionFind         = "find"
	BrowserActionSelect       = "select"
	BrowserActionPress        = "press"
	BrowserActionHover        = "hover"
	BrowserActionBatch        = "batch"
	BrowserActionScreenshot   = "screenshot"
	BrowserActionTabs         = "tabs"
	BrowserActionBookmarks    = "bookmarks"
	BrowserActionDownloads    = "downloads"
	BrowserActionUpload       = "upload"
	BrowserActionTab          = "tab"
	BrowserActionCalculate    = "calculate"
	BrowserActionDevTools     = "devtools"
	BrowserActionOpenPages    = "open_pages"
	BrowserActionObservePages = "observe_pages"
)

// MaxBrowserBatchSteps bounds one batch. The worker enforces the same number;
// checking here too means an oversized plan fails before it costs a round trip.
const MaxBrowserBatchSteps = 12

// Browser waits use the worker's minute-long default unless the caller asks
// for a shorter bound. Keep the declaration, validation and wire value aligned.
const DefaultBrowserWaitSeconds = 60
const MaxBrowserWaitSeconds = 60

// MaxBrowserDevToolsParamsBytes bounds the serialized protocol parameters.
// Their method-specific shape belongs to the authorized native executor.
const MaxBrowserDevToolsParamsBytes = 64 << 10

// MaxBrowserBookmarkQueryRunes bounds a title/URL search of the account's
// Pogo bookmarks. This host metadata never requires executing page code.
const MaxBrowserBookmarkQueryRunes = 200

const (
	DefaultBrowserBookmarkLimit = 20
	MaxBrowserBookmarkLimit     = 50
)

// BrowserReadOnly reports whether an action only reads the page or workspace
// metadata. It is the
// tools-side classification of the action set, kept for callers that need to
// reason about what a plan does (the worker uses the same split);
// the web channel arms per task, not per action.
//
// devtools is deliberately on the write side: arbitrary expressions
// and protocol methods can change the page or pause execution. scroll and find
// move nothing and are classified with the readers.
func BrowserReadOnly(action string) bool {
	switch action {
	case BrowserActionSnapshot, BrowserActionWait, BrowserActionExtract,
		BrowserActionScroll, BrowserActionFind, BrowserActionScreenshot, BrowserActionTabs, BrowserActionCalculate, BrowserActionBookmarks, BrowserActionDownloads:
		return true
	case BrowserActionObservePages:
		return true
	}
	return false
}

// BrowserMetadataAction identifies standalone host metadata reads. Callers must
// still validate the complete action schema before bypassing any page gate.
func BrowserMetadataAction(action string) bool {
	return action == BrowserActionBookmarks || action == BrowserActionDownloads
}

// BrowserActionOf pulls the action out of raw browser arguments, for callers
// (the policy) that classify before the tool validates anything else. An
// unparseable or unknown action reviews as a write: the safe side of a split
// decision is the one that asks.
func BrowserActionOf(args json.RawMessage) string {
	var a struct {
		Action string `json:"action"`
	}
	if json.Unmarshal(args, &a) != nil {
		return ""
	}
	return strings.TrimSpace(a.Action)
}

// Browser exposes a bounded action set matched to the connected executor and
// the model's image path. Unadvertised executors cannot run browser actions.
type Browser struct {
	Link      BrowserLink
	ImageMode BrowserImageMode
	// SpillDir holds a reply too large to put in the result. Set by New, which
	// also makes it readable; empty means DefaultSpillDir.
	SpillDir string
}

func (b *Browser) Name() string                 { return "browser" }
func (b *Browser) ExecutionMode() ExecutionMode { return Sequential }

func (b *Browser) Description() string { return b.description() }

func (b *Browser) InputSchema() map[string]any { return b.inputSchema() }

// browserArgs is the closed argument set every action validates against. Fields
// beyond the schema (a model inventing "selector") are not carried through:
// the op the worker receives is rebuilt from validated fields, so the worker never
// executes an argument nobody vetted. That rule holds inside a batch too — each
// step goes through the same validation as a lone call.
type browserArgs struct {
	Action       string               `json:"action"`
	Target       string               `json:"target,omitempty"`
	TabAction    string               `json:"tab_action,omitempty"`
	TabID        string               `json:"tab_id,omitempty"`
	ContextRef   string               `json:"context_ref,omitempty"`
	ControlEpoch *int64               `json:"control_epoch,omitempty"`
	X            *float64             `json:"x,omitempty"`
	Y            *float64             `json:"y,omitempty"`
	Version      string               `json:"version,omitempty"`
	Text         *string              `json:"text,omitempty"`
	Submit       bool                 `json:"submit,omitempty"`
	URL          string               `json:"url,omitempty"`
	GoneText     string               `json:"gone_text,omitempty"`
	Seconds      float64              `json:"seconds,omitempty"`
	DownloadID   string               `json:"download_id,omitempty"`
	Direction    string               `json:"direction,omitempty"`
	Amount       float64              `json:"amount,omitempty"`
	Role         string               `json:"role,omitempty"`
	Name         string               `json:"name,omitempty"`
	Placeholder  string               `json:"placeholder,omitempty"`
	Label        string               `json:"label,omitempty"`
	Query        string               `json:"query,omitempty"`
	Limit        *int                 `json:"limit,omitempty"`
	Offset       int                  `json:"offset,omitempty"`
	TestID       string               `json:"testid,omitempty"`
	Key          string               `json:"key,omitempty"`
	Full         bool                 `json:"full,omitempty"`
	Actions      []browserArgs        `json:"actions,omitempty"`
	Files        []string             `json:"files,omitempty"`
	UploadMode   string               `json:"upload_mode,omitempty"`
	Operation    string               `json:"operation,omitempty"`
	QuantityRefs []string             `json:"quantity_refs,omitempty"`
	Method       string               `json:"method,omitempty"`
	Params       json.RawMessage      `json:"params,omitempty"`
	URLs         []string             `json:"urls,omitempty"`
	Pages        []BrowserPageBinding `json:"pages,omitempty"`
	Concurrency  *int                 `json:"concurrency,omitempty"`
}

var browserQuantityRef = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,160}$`)

var browserClaimContext = regexp.MustCompile(`^ctx_[0-9a-f]{24}$`)
var browserClaimTab = regexp.MustCompile(`^t[1-9][0-9]{0,9}$`)

const maxBrowserControlEpoch int64 = 1<<53 - 1

func validBrowserCalculation(operation string, refs []string) bool {
	if len(refs) < 1 || len(refs) > 16 || (operation == "compare" || operation == "difference") && len(refs) != 2 {
		return false
	}
	if operation != "compare" && operation != "sort" && operation != "difference" && operation != "sum" {
		return false
	}
	seen := make(map[string]bool, len(refs))
	for _, ref := range refs {
		if !browserQuantityRef.MatchString(ref) || seen[ref] {
			return false
		}
		seen[ref] = true
	}
	return true
}

var browserScrollDirs = map[string]bool{"down": true, "up": true, "top": true, "bottom": true}

// validateBrowserOp rebuilds one validated op from raw arguments, or explains
// what is missing. Split out from Execute so a batch validates every step by
// the same rules before any of them runs — a plan that would fail at step 5 is
// better refused whole than applied halfway.
var browserDevToolsTarget = regexp.MustCompile(`^dt_[0-9a-f]{24}_[1-9][0-9]*$`)

func validBrowserDevToolsTarget(target string) bool {
	return len(target) <= 160 && browserDevToolsTarget.MatchString(target)
}

func validateBrowserOperation(a browserArgs) (browserArgs, error) {
	action := strings.TrimSpace(a.Action)
	text := ""
	if a.Text != nil {
		text = *a.Text
	}
	switch action {
	case BrowserActionUpload:
		mode := a.UploadMode
		if mode == "" {
			mode = "input"
		}
		if strings.TrimSpace(a.Target) == "" || len(a.Files) < 1 || len(a.Files) > 8 || (mode != "input" && mode != "chooser" && mode != "drop") {
			return browserArgs{}, fmt.Errorf("browser upload: target and 1–8 workspace file paths are required; upload_mode is input, chooser or drop")
		}
		seen := map[string]bool{}
		for _, path := range a.Files {
			if strings.TrimSpace(path) == "" || len(path) > 4096 || strings.IndexFunc(path, unicode.IsControl) >= 0 || seen[path] {
				return browserArgs{}, fmt.Errorf("browser upload: files must be unique, nonempty workspace paths without control characters")
			}
			seen[path] = true
		}
		return browserArgs{Action: action, Target: a.Target, Files: a.Files, UploadMode: mode}, nil
	case BrowserActionBookmarks:
		if strings.IndexFunc(a.Query, unicode.IsControl) >= 0 {
			return browserArgs{}, fmt.Errorf("browser bookmarks: query must not contain control characters")
		}
		query := strings.TrimSpace(a.Query)
		if utf8.RuneCountInString(query) > MaxBrowserBookmarkQueryRunes {
			return browserArgs{}, fmt.Errorf("browser bookmarks: query exceeds the %d character limit", MaxBrowserBookmarkQueryRunes)
		}
		limit := DefaultBrowserBookmarkLimit
		if a.Limit != nil {
			limit = *a.Limit
		}
		if limit < 1 || limit > MaxBrowserBookmarkLimit || a.Offset < 0 {
			return browserArgs{}, fmt.Errorf("browser bookmarks: limit must be 1–%d and offset must be nonnegative", MaxBrowserBookmarkLimit)
		}
		return browserArgs{Action: action, Query: query, Limit: &limit, Offset: a.Offset}, nil
	case BrowserActionDownloads:
		if a.DownloadID == "" {
			if a.Seconds != 0 {
				return browserArgs{}, fmt.Errorf("browser downloads: seconds requires download_id; omit both for an immediate list")
			}
			return browserArgs{Action: action}, nil
		}
		if !validBrowserDownloadID(a.DownloadID) {
			return browserArgs{}, fmt.Errorf("browser downloads: download_id must be a UUID returned by this conversation's downloads")
		}
		seconds := a.Seconds
		if seconds == 0 {
			seconds = DefaultBrowserWaitSeconds
		}
		if seconds < 0 || seconds > MaxBrowserWaitSeconds {
			return browserArgs{}, fmt.Errorf("browser downloads: seconds must be between 0 and %d; 0 uses the default", MaxBrowserWaitSeconds)
		}
		return browserArgs{Action: action, DownloadID: strings.ToLower(a.DownloadID), Seconds: seconds}, nil
	case BrowserActionDevTools:
		if strings.TrimSpace(a.Method) == "" {
			return browserArgs{}, fmt.Errorf("browser devtools: method is required; use help to discover supported methods")
		}
		if a.Target != "" && !validBrowserDevToolsTarget(a.Target) {
			return browserArgs{}, fmt.Errorf("browser devtools: target must be an opaque target_ref from targets.list")
		}
		if len(a.Params) > 0 {
			if len(a.Params) > MaxBrowserDevToolsParamsBytes {
				return browserArgs{}, fmt.Errorf("browser devtools: params exceeds the 64 KiB limit")
			}
			params := bytes.TrimSpace(a.Params)
			if len(params) == 0 || params[0] != '{' || !json.Valid(params) {
				return browserArgs{}, fmt.Errorf("browser devtools: params must be a JSON object")
			}
		}
		return browserArgs{Action: action, Method: a.Method, Params: a.Params, Target: a.Target}, nil
	case BrowserActionCalculate:
		if !validBrowserCalculation(a.Operation, a.QuantityRefs) {
			return browserArgs{}, fmt.Errorf("browser calculate: operation must be compare|sort|difference|sum and quantity_refs must be 1–16 unique observed references (compare/difference need two)")
		}
		return browserArgs{Action: action, Operation: a.Operation, QuantityRefs: a.QuantityRefs}, nil
	case BrowserActionScreenshot, BrowserActionTabs:
		return browserArgs{Action: action}, nil
	case BrowserActionTab:
		if a.TabAction == "claim" {
			if !browserClaimTab.MatchString(a.Target) || !browserClaimContext.MatchString(a.ContextRef) || a.ControlEpoch == nil || *a.ControlEpoch < 0 || *a.ControlEpoch > maxBrowserControlEpoch {
				return browserArgs{}, fmt.Errorf("browser tab claim: target, context_ref and control_epoch must come from the same fresh tabs row")
			}
			if a.URL != "" {
				return browserArgs{}, fmt.Errorf("browser tab claim: claiming preserves the page; omit url and observe again before navigation")
			}
			return browserArgs{Action: action, TabAction: "claim", Target: a.Target, ContextRef: a.ContextRef, ControlEpoch: a.ControlEpoch}, nil
		}
		if a.ContextRef != "" || a.ControlEpoch != nil {
			return browserArgs{}, fmt.Errorf("browser tab: context_ref and control_epoch are only supported with tab_action claim")
		}
		if a.TabAction == "new" {
			if a.Target != "" {
				return browserArgs{}, fmt.Errorf("browser tab: new creates a separate page; omit target")
			}
			return browserArgs{Action: action, TabAction: "new", URL: a.URL}, nil
		}
		if a.TabAction != "" && a.TabAction != "switch" {
			return browserArgs{}, fmt.Errorf("browser tab: tab_action must be new, switch or claim")
		}
		if a.Target == "" {
			return browserArgs{}, fmt.Errorf("browser tab: switching requires a target from tabs; to create a separate page use tab_action new")
		}
		if a.URL != "" {
			return browserArgs{}, fmt.Errorf("browser tab: url is only supported with tab_action new; switching never navigates")
		}
		return browserArgs{Action: action, Target: a.Target, TabAction: a.TabAction}, nil
	case BrowserActionSnapshot:
		return browserArgs{Action: action, Full: a.Full}, nil

	case BrowserActionFind:
		if a.Role == "" && a.Name == "" && text == "" && a.Placeholder == "" && a.Label == "" && a.TestID == "" {
			return browserArgs{}, fmt.Errorf("browser find: give at least one of role, name, text, placeholder, label or testid")
		}
		return browserArgs{
			Action: action, Role: a.Role, Name: a.Name, Text: a.Text,
			Placeholder: a.Placeholder, Label: a.Label, TestID: a.TestID, Full: a.Full,
		}, nil

	case BrowserActionClick, BrowserActionExtract, BrowserActionHover:
		if action == BrowserActionClick && (a.X != nil || a.Y != nil) {
			if a.Target != "" || a.X == nil || a.Y == nil || *a.X < 0 || *a.Y < 0 || *a.X > 16384 || *a.Y > 16384 || a.Version == "" {
				return browserArgs{}, fmt.Errorf("browser click: coordinate clicks need nonnegative x/y and screenshot version, without target")
			}
			return browserArgs{Action: action, X: a.X, Y: a.Y, Version: a.Version}, nil
		}
		if a.Target == "" {
			return browserArgs{}, fmt.Errorf("browser %s: target is required (a ref like e12 from the latest snapshot, or a CSS selector)", action)
		}
		return browserArgs{Action: action, Target: a.Target}, nil

	case BrowserActionType:
		if a.Target == "" {
			return browserArgs{}, fmt.Errorf("browser type: target is required")
		}
		return browserArgs{Action: action, Target: a.Target, Text: a.Text, Submit: a.Submit}, nil

	case BrowserActionSelect:
		if a.Target == "" {
			return browserArgs{}, fmt.Errorf("browser select: target is required")
		}
		if a.Text == nil {
			return browserArgs{}, fmt.Errorf("browser select: text is required (the option label or value to choose)")
		}
		return browserArgs{Action: action, Target: a.Target, Text: a.Text}, nil

	case BrowserActionPress:
		key := a.Key
		if key == "" {
			key = text
		}
		if key == "" {
			return browserArgs{}, fmt.Errorf("browser press: key is required (e.g. Enter, Escape, ArrowDown, Control+a)")
		}
		return browserArgs{Action: action, Target: a.Target, Key: key}, nil

	case BrowserActionScroll:
		dir := strings.ToLower(strings.TrimSpace(a.Direction))
		if dir == "" {
			dir = "down"
		}
		if a.Target == "" && !browserScrollDirs[dir] {
			return browserArgs{}, fmt.Errorf("browser scroll: direction %q is not one of down, up, top, bottom", a.Direction)
		}
		amount := a.Amount
		if amount < 0 {
			amount = 0
		}
		return browserArgs{Action: action, Target: a.Target, Direction: dir, Amount: amount}, nil

	case BrowserActionNavigate:
		if a.URL == "" {
			return browserArgs{}, fmt.Errorf("browser navigate: url is required (an original http(s) URL)")
		}
		return browserArgs{Action: action, URL: a.URL}, nil

	case BrowserActionWait:
		if text == "" && a.GoneText == "" && a.URL == "" && a.Target == "" {
			return browserArgs{}, fmt.Errorf("browser wait: needs a condition, one of text, gone_text, url (a glob) or target; " +
				"seconds only limits the wait and is not a sleep. To see an action's result, wait for text you expect to appear, " +
				"or use gone_text with text that should disappear")
		}
		secs := a.Seconds
		if secs <= 0 || secs > MaxBrowserWaitSeconds {
			secs = DefaultBrowserWaitSeconds
		}
		return browserArgs{
			Action: action, Text: a.Text, GoneText: a.GoneText,
			URL: a.URL, Target: a.Target, Seconds: secs,
		}, nil

	default:
		return browserArgs{}, fmt.Errorf("browser: unknown action %q (see the available actions in this executor's schema)", action)
	}
}

// ValidateArgs runs before approval and before Execute. It replaces the generic
// schema message for the mistakes models make most often with one they can
// apply in a single turn. Nothing is inferred: a call without action is refused
// even when its shape implies a batch. The checks Execute repeats are the same.
func (b *Browser) ValidateArgs(args json.RawMessage) error {
	if err := b.missingAction(args); err != nil {
		return err
	}
	if err := validateAgainst(b.InputSchema(), args, ""); err != nil {
		return err
	}
	_, _, err := b.prepare(args)
	return err
}

func (b *Browser) missingAction(args json.RawMessage) error {
	var fields map[string]json.RawMessage
	if json.Unmarshal(args, &fields) != nil || fields == nil {
		return nil // the schema check names the wrong shape
	}
	if _, ok := fields["action"]; !ok {
		if _, batch := fields["actions"]; batch {
			return fmt.Errorf(`browser: "action" is missing. Only a batch takes "actions": resend as {"action":"batch","actions":[...]}, each step keeping its own action`)
		}
		return fmt.Errorf(`browser: "action" is missing; set it to one of %s`, strings.Join(b.actions(true), ", "))
	}
	var action string
	var steps []json.RawMessage
	if json.Unmarshal(fields["action"], &action) != nil || strings.TrimSpace(action) != BrowserActionBatch ||
		json.Unmarshal(fields["actions"], &steps) != nil {
		return nil
	}
	for i, step := range steps {
		var stepFields map[string]json.RawMessage
		if json.Unmarshal(step, &stepFields) != nil || stepFields == nil {
			continue
		}
		if _, ok := stepFields["action"]; !ok {
			return fmt.Errorf(`browser batch: step %d has no "action"; every step names its own, e.g. {"action":"click","target":"e5"}`, i+1)
		}
	}
	return nil
}

// prepare rebuilds the validated operation sent to the executor. A batch is
// validated whole, so a plan that would fail at step 5 never runs step 1.
func (b *Browser) prepare(args json.RawMessage) (browserArgs, string, error) {
	if err := b.validateCapabilities(args, false); err != nil {
		return browserArgs{}, "", err
	}
	var a browserArgs
	if len(args) > 0 {
		if err := json.Unmarshal(args, &a); err != nil {
			return browserArgs{}, "", fmt.Errorf("browser: bad arguments: %v", err)
		}
	}
	a.Action = strings.TrimSpace(a.Action)
	if a.Action != BrowserActionBatch {
		op, err := validateBrowserOp(a)
		return op, a.Action, err
	}
	if len(a.Actions) == 0 {
		return browserArgs{}, a.Action, fmt.Errorf("browser batch: actions is required (a non-empty list of steps)")
	}
	if len(a.Actions) > MaxBrowserBatchSteps {
		return browserArgs{}, a.Action, fmt.Errorf("browser batch: %d steps exceeds the limit of %d; split the plan", len(a.Actions), MaxBrowserBatchSteps)
	}
	steps := make([]browserArgs, 0, len(a.Actions))
	for i, raw := range a.Actions {
		if strings.TrimSpace(raw.Action) == BrowserActionBatch {
			return browserArgs{}, a.Action, fmt.Errorf("browser batch: step %d is itself a batch; flatten the plan", i+1)
		}
		step, err := validateBrowserOp(raw)
		if err != nil {
			return browserArgs{}, a.Action, fmt.Errorf("browser batch: step %d: %v", i+1, err)
		}
		steps = append(steps, step)
	}
	op, err := applyBrowserPageBinding(a, browserArgs{Action: BrowserActionBatch, Actions: steps})
	return op, a.Action, err
}

func (b *Browser) Execute(ctx context.Context, args json.RawMessage) (Result, error) {
	if err := b.validateCapabilities(args, false); err != nil {
		return Result{}, err
	}
	if BrowserBackendOf(b.Link) != BrowserBackendNative {
		return Result{}, fmt.Errorf("browser: native browser is unavailable; configure the browser runtime service")
	}
	op, action, err := b.prepare(args)
	if err != nil {
		return Result{}, err
	}
	raw, err := json.Marshal(op)
	if err != nil {
		return Result{}, err
	}
	reply, err := b.Link.Execute(ctx, raw)
	if err != nil {
		if guidance := browserFailureRecovery(action, err.Error()); guidance != "" {
			return Result{}, fmt.Errorf("browser %s: %w\n%s", action, err, guidance)
		}
		return Result{}, fmt.Errorf("browser %s: %w", action, err)
	}
	if action == BrowserActionDownloads {
		// A metadata read cannot carry a stale page screenshot or qualify as a
		// webpage observation, even if a transport accidentally attaches one.
		reply = BrowserReply{OK: reply.OK, Error: reply.Error, Data: reply.Data, Backend: reply.Backend}
	}
	if !reply.OK {
		// A response from the executor is not evidence that its action worked.
		// Keep partial observations and completed batch steps, while the normal
		// error channel marks model history, UI and retained results consistently.
		return b.browserResult(op, reply, formatBrowserFailure(action, reply)), fmt.Errorf("browser %s execution failed; inspect the preserved result before continuing", action)
	}
	view := reply
	if action == BrowserActionDevTools && BrowserBackendOf(b.Link) == BrowserBackendNative {
		view.Data = browserDevToolsModelData(op.Method, reply.Data)
	}
	return b.browserResult(op, reply, formatBrowserReply(action, view)), nil
}

// The model reads the bounded view; interfaces and persisted tool details retain
// the original redacted diagnostics. Details never travel to a model provider.
func (b *Browser) browserResult(op browserArgs, reply BrowserReply, text string) Result {
	result := b.bound(op.Action, reply, text)
	if browserPagesAction(op.Action) {
		result.Payload = &ResultPayload{Text: text, Structured: append(json.RawMessage(nil), reply.Data...), Format: "json"}
		if collection, err := decodeBrowserPages(op.Action, reply.Data); err == nil {
			result.Payload.Incomplete = collection.Coverage.Observed < collection.Coverage.Requested
		} else {
			result.Payload.Incomplete = true
		}
		return result
	}
	if op.Action == BrowserActionTab && op.TabAction == "claim" && reply.OK && reply.ActiveTab == op.Target && reply.ContextRef == op.ContextRef &&
		reply.Control == "agent" && op.ControlEpoch != nil && *op.ControlEpoch < maxBrowserControlEpoch && reply.ControlEpoch != nil && *reply.ControlEpoch == *op.ControlEpoch+1 {
		details := result.Details.(map[string]any)
		details["tab_action"] = "claim"
		details["control"] = reply.Control
		details["control_epoch"] = *reply.ControlEpoch
	}
	if op.Action == BrowserActionDevTools && BrowserBackendOf(b.Link) == BrowserBackendNative && len(reply.Data) > 0 {
		details := result.Details.(map[string]any)
		details["devtools_method"] = op.Method
		if json.Valid(reply.Data) {
			details["devtools_data"] = append(json.RawMessage(nil), reply.Data...)
		} else {
			details["devtools_data_raw"] = string(reply.Data)
		}
	}
	return result
}

// bound applies one output ceiling to the composed reply, including page text,
// evidence, diagnostics and batch results. Oversized replies remain recoverable
// from the spill artifact. Keeping the head preserves page identity and the
// beginning of the observation rather than dropping them for its tail.
func (b *Browser) bound(action string, r BrowserReply, text string) Result {
	d := browserDetails(action, r)
	imageHint := b.imageHint()
	if im := r.Screenshot; im != nil && im.ImageRef != "" {
		text += "\n" + imageHint
	}
	k := KeepHead(b.SpillDir, "browser", text)
	if k.Truncated {
		// browserDetails is a map rather than a struct, so this rides along under
		// the same json name BashDetails and SynthDetails use — one question, one
		// spelling, whatever the shape of the details around it.
		d["truncated"] = true
		if k.Path != "" {
			d["full_output_path"] = k.Path
		}
	}
	res := Result{Text: k.Text(), Details: d}
	if im := r.Screenshot; im != nil && im.ImageRef != "" {
		if b.ImageMode != BrowserImageNone {
			res.Images = []llm.Block{{Type: llm.BlockImage, ImageRef: im.ImageRef,
				ImageMime: im.Mime, ImageName: im.ImageRef, ImageW: im.Width, ImageH: im.Height,
				ImageAlt: fmt.Sprintf("Browser viewport %dx%d; URL %s; page version %s. %s", im.Width, im.Height, r.URL, r.Version, imageHint)}}
		}
		d["screenshot_ref"] = im.ImageRef
	}
	return res
}

// formatBrowserFailure keeps a failed batch legible: the steps that did run are
// information the model needs to know what state the page is in now, so they
// travel with the error rather than being swallowed by it.
func formatBrowserFailure(action string, r BrowserReply) string {
	if browserPagesAction(action) {
		return formatBrowserPages(action, r)
	}
	if action == BrowserActionDownloads {
		return formatBrowserDownloadFailure(r)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "browser %s failed: %s", action, r.Error)
	if guidance := browserFailureRecovery(action, r.Error); guidance != "" {
		fmt.Fprintf(&b, "\n%s", guidance)
	}
	if r.Backend != "" || r.ActiveTab != "" {
		fmt.Fprintf(&b, "\nreported runtime: %s; tab: %s; version: %s", orUnknown(r.Backend), orUnknown(r.ActiveTab), orUnknown(r.Version))
	}
	if r.URL != "" {
		fmt.Fprintf(&b, "\nlast reported page: %s", r.URL)
		if r.Title != "" {
			fmt.Fprintf(&b, " · %s", r.Title)
		}
	}
	// A dialog is often the explanation for the failure above it, so it travels
	// with the failure rather than being dropped on the floor.
	for _, d := range r.Dialogs {
		fmt.Fprintf(&b, "\n%s — %s", browserDialogLabel(r), d)
	}
	if progress := formatBrowserBatch(r.Batch); progress != "" {
		fmt.Fprintf(&b, "\n%s", progress)
	}
	if steps := formatBrowserSteps(r.Steps); steps != "" {
		fmt.Fprintf(&b, "\n%s", steps)
	}
	if evidence := formatBrowserEvidence(r.Evidence); evidence != "" {
		fmt.Fprintf(&b, "\n%s", evidence)
	}
	if r.Text != "" {
		fmt.Fprintf(&b, "\npartial page observation (does not confirm action completion):\n%s", r.Text)
	}
	// A failed protocol call can still carry exception details, observed events
	// or an unavailable-data explanation. Preserve it without retrying the call.
	if len(r.Data) > 0 {
		fmt.Fprintf(&b, "\ndata: %s", r.Data)
	}
	if im := r.Screenshot; im != nil && im.ImageRef != "" {
		fmt.Fprintf(&b, "\nviewport screenshot: %s (%dx%d pixels)", im.ImageRef, im.Width, im.Height)
	}
	return b.String()
}

func formatBrowserBatch(batch *BrowserBatch) string {
	if batch == nil {
		return ""
	}
	text := fmt.Sprintf("batch: %d/%d steps executed; %d not executed; stop=%s", batch.Executed, batch.Requested, batch.Skipped, batch.StopReason)
	if batch.Skipped > 0 {
		text += ". Observe the current page before planning the remaining steps; do not replay completed clicks or submissions."
	}
	return text
}

func formatBrowserEvidence(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var rendered bytes.Buffer
	if err := json.Indent(&rendered, raw, "", "  "); err != nil {
		return "Visible DOM evidence was malformed; do not use it as verified data.\n"
	}
	return fmt.Sprintf("visible DOM evidence / as-observed calculation:\n%s\n", rendered.String())
}

func formatBrowserSteps(steps []BrowserStep) string {
	if len(steps) == 0 {
		return ""
	}
	var b strings.Builder
	for i, s := range steps {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "  %d. %s", i+1, s.Action)
		switch {
		case !s.OK:
			fmt.Fprintf(&b, " — failed: %s", s.Error)
		case s.Text != "" && i < len(steps)-1:
			// The last step's text is printed in full below; intermediate steps
			// only need to say they produced something.
			fmt.Fprintf(&b, " — ok (%d chars)", len(s.Text))
		default:
			b.WriteString(" — ok")
		}
	}
	return b.String()
}

// formatBrowserReply renders one successful reply for the model. A snapshot
// carries the element list verbatim (the pane already budgeted it); every other
// action ends with the mini snapshot — where the page ended up plus its
// interactive-element count — so the model always knows what its action did
// without spending a separate snapshot call.
func formatBrowserReply(action string, r BrowserReply) string {
	if browserPagesAction(action) {
		return formatBrowserPages(action, r)
	}
	if action == BrowserActionBookmarks {
		return formatBrowserBookmarks(r)
	}
	if action == BrowserActionDownloads {
		return formatBrowserDownloads(r)
	}
	var b strings.Builder
	if r.Backend != "" {
		fmt.Fprintf(&b, "runtime: %s; tab: %s; version: %s\n", r.Backend, r.ActiveTab, r.Version)
	}
	fmt.Fprintf(&b, "page: %s", orUnknown(r.URL))
	if r.Title != "" {
		fmt.Fprintf(&b, " · %s", r.Title)
	}
	if r.Interactive > 0 {
		fmt.Fprintf(&b, " · %d interactive", r.Interactive)
	}
	// The scroll position is only news when the page is taller than the pane:
	// on a page that fits, saying so once per action is noise.
	if r.Scroll != "" && r.Scroll != "whole page visible" {
		fmt.Fprintf(&b, " · %s", r.Scroll)
	}
	b.WriteString("\n")
	// Dialogs go above the page state: what the page said in a modal is usually
	// the reason the state looks the way it does.
	for _, d := range r.Dialogs {
		fmt.Fprintf(&b, "%s — %s\n", browserDialogLabel(r), d)
	}
	b.WriteString(formatBrowserSettle(r))
	if progress := formatBrowserBatch(r.Batch); progress != "" {
		b.WriteString(progress)
		b.WriteString("\n")
	}
	if steps := formatBrowserSteps(r.Steps); steps != "" {
		b.WriteString(steps)
		b.WriteString("\n")
	}
	if len(r.Evidence) > 0 {
		b.WriteString(formatBrowserEvidence(r.Evidence))
	}
	if r.Text != "" {
		b.WriteString(r.Text)
	}
	if len(r.Data) > 0 {
		fmt.Fprintf(&b, "\ndata: %s", string(r.Data))
	}
	if im := r.Screenshot; im != nil && im.ImageRef != "" {
		fmt.Fprintf(&b, "\nviewport screenshot: %s (%dx%d pixels)", im.ImageRef, im.Width, im.Height)
	}
	return b.String()
}

// formatBrowserSettle reports observed tab and network changes. The source tab
// may have changed or closed, and pending traffic need not be the action's work.
// Ordinary replies without either observation carry no additional note.
func formatBrowserSettle(r BrowserReply) string {
	var b strings.Builder
	if len(r.OpenedTabs) > 0 {
		ids := make([]string, 0, len(r.OpenedTabs))
		for _, tab := range r.OpenedTabs {
			ids = append(ids, tab.ID)
		}
		fmt.Fprintf(&b, "opened tab %s during the action; this reply observes active tab %s", strings.Join(ids, ", "), orUnknown(r.ActiveTab))
		if r.FromTab != "" && r.FromTab != r.ActiveTab {
			fmt.Fprintf(&b, "; action began in tab %s (its current state is not verified)", r.FromTab)
		}
		b.WriteString("\n")
	}
	if r.PendingRequests > 0 {
		fmt.Fprintf(&b, "network pending: %d request(s) observed on related pages during the action remained pending after %d ms. "+
			"Verify the expected result with a condition-based wait; do not repeat the action.\n",
			r.PendingRequests, r.SettleMS)
	}
	return b.String()
}

// browserDetails is the UI's copy of the reply. It returns the map type rather
// than any so that bound can add to it: a caller that has to type-assert its own
// package's return value in order to extend it is one refactor away from silently
// dropping the addition.
func browserDetails(action string, r BrowserReply) map[string]any {
	if browserPagesAction(action) {
		return browserPagesDetails(action, r)
	}
	// Operation/status text is formatted separately from the worker's readable
	// page text. Keep that distinction for evidence presentation and the bounded
	// missing-citation review; this flag never claims the content is correct.
	readable := strings.TrimSpace(r.Text)
	d := map[string]any{
		"action":               action,
		"ok":                   r.OK,
		"url":                  r.URL,
		"title":                r.Title,
		"interactive":          r.Interactive,
		"has_readable_content": r.OK && !r.ObservationPending && readable != "" && readable != "No matching elements or readable page text.",
	}
	if action == BrowserActionBookmarks {
		// Pogo bookmarks are saved-scope metadata, not an observation of any web
		// page's content. Do not let this read qualify as webpage evidence.
		d["content_source"] = "pogo_bookmarks"
		d["has_readable_content"] = false
	}
	if action == BrowserActionDownloads {
		d["content_source"] = "pogo_downloads"
		d["has_readable_content"] = false
		d["url"], d["title"] = "", ""
	}
	if r.Favicon != "" {
		d["favicon"] = r.Favicon
	}
	if r.Backend != "" {
		d["backend"] = r.Backend
		d["active_tab"] = r.ActiveTab
		d["version"] = r.Version
	}
	if r.ContextRef != "" {
		d["context_ref"] = r.ContextRef
	}
	if r.Scroll != "" {
		d["scroll"] = r.Scroll
	}
	if len(r.Steps) > 0 {
		d["steps"] = len(r.Steps)
		steps := make([]BrowserStepResult, len(r.Steps))
		for i, step := range r.Steps {
			steps[i] = BrowserStepResult{Action: step.Action, OK: step.OK, Error: step.Error, DurationMS: step.DurationMS}
		}
		d["step_results"] = steps
	}
	if r.Timing != nil {
		d["timing"] = *r.Timing
	}
	if r.Batch != nil {
		d["batch"] = *r.Batch
	}
	if r.ObservationPending {
		d["observation_pending"] = true
	}
	return d
}

func orUnknown(s string) string {
	if s == "" {
		return "(unknown)"
	}
	return s
}

func browserDialogLabel(_ BrowserReply) string { return "dialog pending" }
