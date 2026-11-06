package tools

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

const BrowserBackendNative = "native"

// BrowserBackendProvider identifies the native executor, including delegated
// links. Missing or unsupported metadata cannot activate another executor.
type BrowserBackendProvider interface {
	BrowserBackend() string
}

func BrowserBackendOf(link BrowserLink) string {
	if provider, ok := link.(BrowserBackendProvider); ok {
		switch provider.BrowserBackend() {
		case BrowserBackendNative:
			return BrowserBackendNative
		}
	}
	return ""
}

// BrowserImageMode describes the model's image path, independently of whether
// the browser itself can capture pixels for the user or an evaluation trace.
type BrowserImageMode string

const (
	BrowserImageUnknown BrowserImageMode = ""
	BrowserImageDirect  BrowserImageMode = "direct"
	BrowserImageHelper  BrowserImageMode = "helper"
	BrowserImageNone    BrowserImageMode = "none"
)

func (b *Browser) actions(includeBatch bool) []string {
	actions := []string{BrowserActionSnapshot, BrowserActionFind, BrowserActionClick, BrowserActionType,
		BrowserActionSelect, BrowserActionPress, BrowserActionHover, BrowserActionScroll,
		BrowserActionNavigate, BrowserActionWait, BrowserActionExtract}
	if BrowserBackendOf(b.Link) == BrowserBackendNative {
		actions = append(actions, BrowserActionCalculate)
		if includeBatch {
			actions = append(actions, BrowserActionDevTools, BrowserActionBookmarks, BrowserActionDownloads, BrowserActionUpload, BrowserActionOpenPages, BrowserActionObservePages)
		}
	}
	if b.ImageMode != BrowserImageNone {
		actions = append(actions, BrowserActionScreenshot)
	}
	actions = append(actions, BrowserActionTabs, BrowserActionTab)
	if includeBatch {
		actions = append(actions, BrowserActionBatch)
	}
	return actions
}

func (b *Browser) coordinatesAvailable() bool {
	return b.ImageMode != BrowserImageNone
}

// validateCapabilities checks the original object before any normalization or
// transport call. A batch is inspected completely before any child can run.
// All transports use the same strict contract; retired actions never dispatch.
func (b *Browser) validateCapabilities(raw json.RawMessage, inBatch bool) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return fmt.Errorf("browser: arguments must be an object")
	}
	var action string
	if err := json.Unmarshal(fields["action"], &action); err != nil || strings.TrimSpace(action) == "" {
		return fmt.Errorf("browser: action is required in every operation")
	}
	action = strings.TrimSpace(action)
	if action == "eval" || action == "auth_status" || action == "api_read" {
		return fmt.Errorf("browser: action %q is retired; use native page actions or devtools", action)
	}
	if inBatch && action == BrowserActionBatch {
		return fmt.Errorf("browser: step is itself a batch; flatten the plan")
	}
	if inBatch && action == BrowserActionDevTools {
		return fmt.Errorf("browser: devtools cannot run inside batch; call it separately")
	}
	if inBatch && action == BrowserActionBookmarks {
		return fmt.Errorf("browser: bookmarks cannot run inside batch; read workspace metadata separately before page actions")
	}
	if inBatch && action == BrowserActionDownloads {
		return fmt.Errorf("browser: downloads cannot run inside batch; read conversation download metadata separately")
	}
	if !slices.Contains(b.actions(!inBatch), action) {
		return fmt.Errorf("browser: unknown action %q or action is unavailable for backend %s with image mode %q", action, BrowserBackendOf(b.Link), b.ImageMode)
	}
	allowed := map[string]bool{"action": true}
	add := func(names ...string) {
		for _, name := range names {
			allowed[name] = true
		}
	}
	switch action {
	case BrowserActionOpenPages:
		add("urls", "concurrency", "seconds")
	case BrowserActionObservePages:
		add("pages", "concurrency", "seconds", "full")
	case BrowserActionUpload:
		add("target", "files", "upload_mode")
	case BrowserActionBookmarks:
		add("query", "limit", "offset")
	case BrowserActionDownloads:
		add("download_id", "seconds")
	case BrowserActionDevTools:
		add("method", "params", "target")
	case BrowserActionCalculate:
		add("operation", "quantity_refs")
	case BrowserActionSnapshot:
		add("full")
	case BrowserActionFind:
		add("role", "name", "text", "placeholder", "label", "testid", "full")
	case BrowserActionClick:
		add("target")
		if b.coordinatesAvailable() {
			add("x", "y", "version")
		}
	case BrowserActionType:
		add("target", "text", "submit")
	case BrowserActionSelect:
		add("target", "text")
	case BrowserActionPress:
		add("target", "key")
	case BrowserActionHover, BrowserActionExtract:
		add("target")
	case BrowserActionTab:
		add("target", "tab_action", "url")
		var tabAction string
		_ = json.Unmarshal(fields["tab_action"], &tabAction)
		if tabAction == "claim" {
			if inBatch {
				return fmt.Errorf("browser: tab claim cannot run inside batch; inspect tabs and claim one chosen page separately")
			}
			if BrowserBackendOf(b.Link) != BrowserBackendNative {
				return fmt.Errorf("browser tab claim: native browser is required")
			}
			add("context_ref", "control_epoch")
			if _, present := fields["url"]; present {
				return fmt.Errorf("browser tab claim: omit url; claiming preserves the existing page")
			}
		}
	case BrowserActionScroll:
		add("target", "direction", "amount")
	case BrowserActionNavigate:
		add("url")
	case BrowserActionWait:
		add("target", "text", "gone_text", "url", "seconds")
	case BrowserActionBatch:
		add("actions")
	}
	if !inBatch && BrowserBackendOf(b.Link) == BrowserBackendNative && browserPageBindable(action) {
		add("tab_id", "context_ref", "control_epoch")
		_, tab := fields["tab_id"]
		_, context := fields["context_ref"]
		_, epoch := fields["control_epoch"]
		if tab || context || epoch {
			var binding BrowserPageBinding
			if !tab || !context || !epoch || json.Unmarshal(raw, &binding) != nil || !validBrowserPageBinding(binding) {
				return fmt.Errorf("browser %s: tab_id/context_ref/control_epoch must be supplied together from the same fresh Agent-owned page; call tabs, then copy all three fields from one Agent-controlled row. Do not drop a chosen binding to bypass a control change", action)
			}
		}
	}
	for name, value := range fields {
		if !allowed[name] {
			if action == BrowserActionClick && (name == "name" || name == "role" || name == "label" || name == "text" || name == "placeholder" || name == "testid") {
				return fmt.Errorf("browser click: field %q is not supported; use find with the observed name/role/text/label/placeholder/testid first, then click the returned target ref on the same page. Click does not accept locator fields directly", name)
			}
			return fmt.Errorf("browser %s: field %q is not supported; use the declared action fields", action, name)
		}
		if string(value) == "null" {
			return fmt.Errorf("browser %s: field %q cannot be null", action, name)
		}
	}
	if browserPagesAction(action) {
		if err := validateBrowserPagesFields(fields); err != nil {
			return err
		}
	}
	if action == BrowserActionDownloads {
		if raw, present := fields["download_id"]; present {
			var id string
			if json.Unmarshal(raw, &id) != nil || !validBrowserDownloadID(id) {
				return fmt.Errorf("browser downloads: download_id must be a UUID returned by this conversation's downloads")
			}
		} else if _, present := fields["seconds"]; present {
			return fmt.Errorf("browser downloads: seconds requires download_id; omit both for an immediate list")
		}
	}
	if action == BrowserActionDevTools {
		if raw, present := fields["target"]; present {
			var target string
			if json.Unmarshal(raw, &target) != nil || !validBrowserDevToolsTarget(target) {
				return fmt.Errorf("browser devtools: target must be an opaque target_ref from targets.list; omit it for the current page")
			}
		}
	}
	if action == BrowserActionClick {
		_, x := fields["x"]
		_, y := fields["y"]
		_, version := fields["version"]
		_, target := fields["target"]
		if (x || y || version) && (!x || !y || !version || target) {
			return fmt.Errorf("browser click: coordinate clicks require x/y/version together, without target")
		}
	}
	for _, name := range []string{"seconds", "amount"} {
		if raw, present := fields[name]; present {
			var value float64
			if err := json.Unmarshal(raw, &value); err != nil || value < 0 || name == "seconds" && value > MaxBrowserWaitSeconds {
				return fmt.Errorf("browser %s: %s is outside its declared numeric range", action, name)
			}
		}
	}
	if action == BrowserActionType || action == BrowserActionSelect {
		if _, present := fields["text"]; !present {
			return fmt.Errorf("browser %s: text is required; an explicit empty string is allowed", action)
		}
	}
	if action == BrowserActionBatch {
		var steps []json.RawMessage
		if err := json.Unmarshal(fields["actions"], &steps); err != nil || len(steps) == 0 {
			return fmt.Errorf("browser batch: actions is required (a non-empty list of steps)")
		}
		if len(steps) > MaxBrowserBatchSteps {
			return fmt.Errorf("browser batch: %d steps exceeds the limit of %d", len(steps), MaxBrowserBatchSteps)
		}
		for i, step := range steps {
			if err := b.validateCapabilities(step, true); err != nil {
				return fmt.Errorf("browser batch: step %d: %w", i+1, err)
			}
		}
	}
	return nil
}
