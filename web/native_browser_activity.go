package web

import (
	"encoding/base64"
	"encoding/json"
	"math"
	"regexp"
	"strings"
)

// Keep viewer telemetry separate from page text, tool replies and credentials.
// Decoding through these shapes strips unknown worker fields (including input
// values) before state or frame metadata crosses the authenticated API.
type nativeBrowserAction struct {
	Seq       int64  `json:"seq"`
	ID        string `json:"id"`
	Actor     string `json:"actor"`
	Action    string `json:"action"`
	Phase     string `json:"phase"`
	Label     string `json:"label"`
	TabID     string `json:"tab_id"`
	Version   string `json:"version"`
	StartedAt int64  `json:"started_at"`
	UpdatedAt int64  `json:"updated_at"`
}

type nativeBrowserActivity struct {
	LastAction    *nativeBrowserAction  `json:"last_action"`
	RecentActions []nativeBrowserAction `json:"recent_actions"`
}

type nativeBrowserRect struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

type nativeBrowserOverlay struct {
	Version  string `json:"version"`
	TabID    string `json:"tab_id"`
	Viewport struct {
		Width  float64 `json:"width"`
		Height float64 `json:"height"`
	} `json:"viewport"`
	SampledAt  int64                `json:"sampled_at"`
	LastAction *nativeBrowserAction `json:"last_action,omitempty"`
	Pointer    *struct {
		X         float64 `json:"x"`
		Y         float64 `json:"y"`
		Seq       int64   `json:"seq"`
		ActionSeq int64   `json:"action_seq"`
		Kind      string  `json:"kind"`
		Actor     string  `json:"actor"`
	} `json:"pointer,omitempty"`
	Target *struct {
		Rect nativeBrowserRect `json:"rect"`
	} `json:"target,omitempty"`
	Focus *struct {
		Rect     nativeBrowserRect `json:"rect"`
		Editable bool              `json:"editable"`
	} `json:"focus,omitempty"`
}

var nativeTabID = regexp.MustCompile(`^t[1-9][0-9]{0,9}$`)

var nativeActionLabels = map[string]string{
	"devtools": "Inspecting browser internals",
	"snapshot": "Inspecting page", "find": "Finding target", "click": "Clicking target", "type": "Filling field",
	"select": "Selecting option", "press": "Pressing key", "hover": "Hovering target", "scroll": "Scrolling page",
	"wait": "Waiting for page", "extract": "Reading target", "navigate": "Opening page", "screenshot": "Capturing page",
	"tabs": "Inspecting tabs", "tab": "Changing tab", "input_text": "Entering text", "input_scroll": "Scrolling page", "input_back": "Going back", "dialog": "Answering dialog",
	"calculate": "Checking observed quantities",
	"upload":    "Selecting files",
}

func validNativeAction(a *nativeBrowserAction) bool {
	if a == nil {
		return true
	}
	label, known := nativeActionLabels[a.Action]
	if !known || a.Seq <= 0 || a.Seq > 1<<53-1 || a.ID == "" || len(a.ID) > 80 ||
		(a.Actor != "agent" && a.Actor != "human") || (a.Phase != "started" && a.Phase != "succeeded" && a.Phase != "failed") ||
		!nativeTabID.MatchString(a.TabID) || a.Version == "" || len(a.Version) > 256 ||
		a.StartedAt <= 0 || a.UpdatedAt < a.StartedAt || a.UpdatedAt > 1<<53-1 {
		return false
	}
	// The label is a product string, never page text or submitted content.
	a.Label = label
	return true
}

func sanitizeNativeActivity(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || len(raw) > 16<<10 {
		return nil
	}
	var activity nativeBrowserActivity
	if json.Unmarshal(raw, &activity) != nil || !validNativeAction(activity.LastAction) || len(activity.RecentActions) > 8 {
		return nil
	}
	for i := range activity.RecentActions {
		if !validNativeAction(&activity.RecentActions[i]) {
			return nil
		}
	}
	if activity.RecentActions == nil {
		activity.RecentActions = []nativeBrowserAction{}
	}
	out, _ := json.Marshal(activity)
	return out
}

func finiteNativeNumber(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }

func validNativeRect(rect nativeBrowserRect, width, height float64) bool {
	return finiteNativeNumber(rect.X) && finiteNativeNumber(rect.Y) && finiteNativeNumber(rect.Width) && finiteNativeNumber(rect.Height) &&
		rect.X >= 0 && rect.Y >= 0 && rect.Width > 0 && rect.Height > 0 && rect.X+rect.Width <= width+0.01 && rect.Y+rect.Height <= height+0.01
}

// Bad/missing optional telemetry hides the overlay, never the actual frame.
func sanitizeNativeOverlay(header, version string) string {
	if header == "" || len(header) > 12<<10 {
		return ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(header)
	if err != nil {
		return ""
	}
	var overlay nativeBrowserOverlay
	if json.Unmarshal(raw, &overlay) != nil || version == "" || overlay.Version != version || len(version) > 256 ||
		!nativeTabID.MatchString(overlay.TabID) || overlay.SampledAt <= 0 || overlay.SampledAt > 1<<53-1 || !validNativeAction(overlay.LastAction) {
		return ""
	}
	if parts := strings.Split(version, ":"); len(parts) >= 2 && parts[1] != overlay.TabID {
		return ""
	}
	w, h := overlay.Viewport.Width, overlay.Viewport.Height
	if !finiteNativeNumber(w) || !finiteNativeNumber(h) || w < 1 || h < 1 || w > 8192 || h > 8192 {
		return ""
	}
	if p := overlay.Pointer; p != nil {
		if !finiteNativeNumber(p.X) || !finiteNativeNumber(p.Y) || p.X < 0 || p.Y < 0 || p.X >= w || p.Y >= h ||
			p.Seq <= 0 || p.Seq > 1<<53-1 || p.ActionSeq <= 0 || p.ActionSeq > 1<<53-1 ||
			(p.Kind != "move" && p.Kind != "click") || (p.Actor != "agent" && p.Actor != "human") {
			return ""
		}
	}
	if overlay.Target != nil && !validNativeRect(overlay.Target.Rect, w, h) {
		return ""
	}
	if overlay.Focus != nil && !validNativeRect(overlay.Focus.Rect, w, h) {
		return ""
	}
	clean, _ := json.Marshal(overlay)
	return base64.RawURLEncoding.EncodeToString(clean)
}
