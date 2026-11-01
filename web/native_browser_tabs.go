package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

var nativeContextRef = regexp.MustCompile(`^ctx_[a-f0-9]{24}$`)
var nativeTabOperationID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

func validNativeContextRef(value string) bool { return nativeContextRef.MatchString(value) }

type nativeBrowserTarget struct {
	TabID      string
	ContextRef string
}

func (t nativeBrowserTarget) empty() bool { return t.TabID == "" && t.ContextRef == "" }
func (t nativeBrowserTarget) valid() bool {
	return nativeTabID.MatchString(t.TabID) && validNativeContextRef(t.ContextRef)
}
func invalidNativeTarget() error {
	return &nativeBrowserHTTPError{status: http.StatusBadRequest, message: "browser page binding requires tab_id and context_ref"}
}
func (t nativeBrowserTarget) suffix(path string) string {
	if t.empty() {
		return path
	}
	return path + "?" + url.Values{"tab_id": {t.TabID}, "context_ref": {t.ContextRef}}.Encode()
}
func (t nativeBrowserTarget) matches(state map[string]json.RawMessage) bool {
	var contextRef, viewedTab string
	if json.Unmarshal(state["context_ref"], &contextRef) != nil || json.Unmarshal(state["viewed_tab"], &viewedTab) != nil || contextRef != t.ContextRef || viewedTab != t.TabID {
		return false
	}
	var identity string
	if value, ok := state["frame_identity"]; ok && (json.Unmarshal(value, &identity) != nil || !validNativeFrameIdentity(identity) || strings.Split(identity, ":")[1] != t.TabID) {
		return false
	}
	return true
}
func parseNativeBrowserTarget(r *http.Request) (nativeBrowserTarget, error) {
	query := r.URL.Query()
	target := nativeBrowserTarget{TabID: query.Get("tab_id"), ContextRef: query.Get("context_ref")}
	if len(query["tab_id"]) > 1 || len(query["context_ref"]) > 1 || ((query.Has("tab_id") || query.Has("context_ref")) && !target.valid()) {
		return target, invalidNativeTarget()
	}
	return target, nil
}

func validNativeBrowserControl(input nativeBrowserControl) bool {
	if !(nativeBrowserTarget{ContextRef: input.ContextRef, TabID: input.TabID}).valid() {
		return false
	}
	if input.AdoptIfUnowned {
		return input.Mode == "human" && !input.Force && input.LeaseToken == "" && input.Generation != nil && *input.Generation >= 1 && *input.Generation <= 1<<53-1 &&
			input.ControlEpoch != nil && *input.ControlEpoch == 0
	}
	return input.Generation == nil && input.ControlEpoch == nil && (input.Mode == "human" || (input.Mode == "agent" && input.LeaseToken != "" && !input.Force))
}

// Tab rows are public display metadata. A worker may retain per-tab leases or
// target handles internally; none may leak into ordinary state or tab lists.
func nativeBrowserPublicTabs(raw json.RawMessage) json.RawMessage {
	var rows []map[string]json.RawMessage
	if len(raw) == 0 || json.Unmarshal(raw, &rows) != nil || rows == nil {
		return nil
	}
	public := make([]map[string]any, 0, len(rows))
	seen := make(map[string]bool)
	for _, row := range rows {
		var id string
		if json.Unmarshal(row["id"], &id) != nil || !nativeTabID.MatchString(id) || seen[id] {
			continue
		}
		seen[id] = true
		tab := map[string]any{"id": id}
		for _, key := range []string{"url", "title"} {
			var value string
			if json.Unmarshal(row[key], &value) == nil {
				tab[key] = value
			}
		}
		if favicon, valid := nativeBrowserFavicon(row["favicon"]); valid && tab["url"] != "about:blank" {
			tab["favicon"] = favicon
		}
		var control string
		if json.Unmarshal(row["control"], &control) == nil && (control == "human" || control == "agent") {
			tab["control"] = control
		}
		var epoch int64
		if string(row["control_epoch"]) != "null" && json.Unmarshal(row["control_epoch"], &epoch) == nil && epoch >= 0 && epoch <= 1<<53-1 {
			tab["control_epoch"] = epoch
		}
		// A popup may reuse only its exact opener's still-current human lease.
		// Public ancestry is a binding hint, never the lease itself.
		var opener string
		var inheritedEpoch int64
		if json.Unmarshal(row["opener_tab"], &opener) == nil && nativeTabID.MatchString(opener) && opener != id &&
			string(row["inherited_control_epoch"]) != "null" && json.Unmarshal(row["inherited_control_epoch"], &inheritedEpoch) == nil && inheritedEpoch >= 0 && inheritedEpoch <= 1<<53-1 {
			tab["opener_tab"] = opener
			tab["inherited_control_epoch"] = inheritedEpoch
		}
		public = append(public, tab)
	}
	result, _ := json.Marshal(public)
	return result
}

type nativeBrowserTabsInput struct {
	Action       string `json:"action"`
	OpID         string `json:"op_id"`
	ContextRef   string `json:"context_ref"`
	TabID        string `json:"tab_id,omitempty"`
	LeaseToken   string `json:"lease_token,omitempty"`
	Generation   *int64 `json:"generation,omitempty"`
	ControlEpoch *int64 `json:"control_epoch,omitempty"`
}
type nativeBrowserTabsResult struct {
	TabID      string                     `json:"tab_id"`
	ContextRef string                     `json:"context_ref"`
	State      map[string]json.RawMessage `json:"state"`
	// Only an authenticated human new-page result may carry its own lease.
	// Ordinary state and tab rows remain free of input authority.
	LeaseToken string `json:"lease_token,omitempty"`
}

func validNativeTabsInput(input nativeBrowserTabsInput) bool {
	if !validNativeContextRef(input.ContextRef) || !nativeTabOperationID.MatchString(input.OpID) {
		return false
	}
	switch input.Action {
	case "new":
		return input.TabID == "" && input.LeaseToken == "" && input.Generation == nil && input.ControlEpoch == nil
	case "close":
		if !nativeTabID.MatchString(input.TabID) || (input.Generation == nil) != (input.ControlEpoch == nil) {
			return false
		}
		if input.Generation != nil && (*input.Generation < 1 || *input.Generation > 1<<53-1 || *input.ControlEpoch < 0 || *input.ControlEpoch > 1<<53-1) {
			return false
		}
		// Existing human leases remain valid. A close without a lease must
		// bind the exact observed generation and control epoch; only the
		// worker can confirm this Page has no different human owner.
		return input.LeaseToken != "" || input.Generation != nil
	default:
		return false
	}
}
func (b *nativeBrowser) Tabs(ctx context.Context, input nativeBrowserTabsInput) (*nativeBrowserTabsResult, error) {
	if !validNativeTabsInput(input) {
		return nil, &nativeBrowserHTTPError{status: http.StatusBadRequest, message: "invalid browser tab operation"}
	}
	// An explicit close must reach the worker while navigation/input is still
	// running. Its page-scoped identity/lease fence interrupts only that Page;
	// waiting on the execution lock would put close behind the stuck load.
	// The caller binds an existing context. No ensure/reset or implicit takeover.
	// A new background page is idempotent at the worker and must not cancel an
	// unrelated Agent action when its response is delayed or the client leaves.
	raw, err := b.request(ctx, http.MethodPost, "/tabs", input)
	if err != nil {
		return nil, err
	}
	var response struct {
		TabID      string          `json:"tab_id"`
		ContextRef string          `json:"context_ref"`
		State      json.RawMessage `json:"state"`
		LeaseToken string          `json:"lease_token"`
	}
	if json.Unmarshal(raw, &response) != nil || !nativeTabID.MatchString(response.TabID) || response.ContextRef != input.ContextRef || (input.Action == "close" && response.TabID != input.TabID) {
		return nil, errors.New("invalid native browser tab result; retry only with the same operation ID")
	}
	state, err := nativeBrowserState(response.State, false)
	if err != nil {
		return nil, err
	}
	var stateContext string
	if json.Unmarshal(state["context_ref"], &stateContext) != nil || stateContext != input.ContextRef {
		return nil, errors.New("native browser tab result has a different context")
	}
	result := &nativeBrowserTabsResult{TabID: response.TabID, ContextRef: response.ContextRef, State: state}
	if input.Action == "new" {
		target := nativeBrowserTarget{TabID: response.TabID, ContextRef: response.ContextRef}
		if !target.matches(state) {
			return nil, errors.New("native browser new tab result has a different page binding")
		}
		var control string
		_ = json.Unmarshal(state["control"], &control)
		if response.LeaseToken != "" || control == "human" {
			var epoch, generation int64
			if control != "human" || response.LeaseToken == "" || len(response.LeaseToken) > 512 || strings.IndexFunc(response.LeaseToken, func(r rune) bool { return r < 33 || r > 126 }) >= 0 ||
				string(state["control_epoch"]) == "null" || json.Unmarshal(state["control_epoch"], &epoch) != nil || epoch < 0 || epoch > 1<<53-1 ||
				string(state["generation"]) == "null" || json.Unmarshal(state["generation"], &generation) != nil || generation < 1 || generation > 1<<53-1 {
				return nil, errors.New("native browser new human tab result has an invalid lease binding")
			}
			result.LeaseToken = response.LeaseToken
		}
	}
	return result, nil
}
func (s *Server) handleBrowserTabs(w http.ResponseWriter, r *http.Request) {
	sess, ok := s.nativeBrowserSession(w, r)
	if !ok {
		return
	}
	var input nativeBrowserTabsInput
	if err := decodeNativeBrowserBody(r, &input); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if !validNativeTabsInput(input) {
		writeErr(w, http.StatusBadRequest, errors.New("new tab requires a context and operation ID; closing requires a bound tab and its human lease or observed generation/control_epoch"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), DefaultBrowserOpTimeout)
	defer cancel()
	result, err := sess.nativeBrowser.Tabs(ctx, input)
	if err != nil {
		writeNativeBrowserError(w, err)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	sess.touch()
	writeJSON(w, http.StatusOK, result)
}
