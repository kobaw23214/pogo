package web

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/yosukeno/pogo/tools"
)

func (b *nativeBrowser) pageExecutionMetadata(ctx context.Context, raw json.RawMessage) (nativeBrowserFocusMetadata, error) {
	var fields map[string]json.RawMessage
	var op struct {
		Action       string `json:"action"`
		TabID        string `json:"tab_id"`
		ContextRef   string `json:"context_ref"`
		ControlEpoch *int64 `json:"control_epoch"`
	}
	if json.Unmarshal(raw, &fields) != nil || json.Unmarshal(raw, &op) != nil {
		return nativeBrowserFocusMetadata{}, errors.New("invalid bound browser operation")
	}
	_, bound := fields["tab_id"]
	collection := op.Action == tools.BrowserActionOpenPages || op.Action == tools.BrowserActionObservePages
	if !bound && !collection {
		return b.focusMetadata(ctx, raw), nil
	}
	if bound && (!nativeTabID.MatchString(op.TabID) || !validNativeContextRef(op.ContextRef) || op.ControlEpoch == nil || *op.ControlEpoch < 0 || *op.ControlEpoch > 1<<53-1) {
		return nativeBrowserFocusMetadata{}, errors.New("browser page binding is incomplete; inspect current tabs")
	}
	metadataCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	rawState, err := b.request(metadataCtx, http.MethodGet, "/state", nil)
	if err != nil {
		return nativeBrowserFocusMetadata{}, errors.New("browser execution capabilities could not be confirmed; no page action was dispatched")
	}
	var state struct {
		ContextRef   string `json:"context_ref"`
		Generation   int64  `json:"generation"`
		Capabilities struct {
			PageBinding bool `json:"explicit_page_binding"`
			Collections bool `json:"page_collections"`
		} `json:"agent_capabilities"`
	}
	if json.Unmarshal(rawState, &state) != nil || !validNativeContextRef(state.ContextRef) || state.Generation < 1 || state.Generation > 1<<53-1 ||
		bound && (!state.Capabilities.PageBinding || state.ContextRef != op.ContextRef) || collection && !state.Capabilities.Collections {
		return nativeBrowserFocusMetadata{}, errors.New("the browser worker does not support this page binding or collection, or its context changed; update the app and browser worker together and inspect current tabs. No page action was dispatched")
	}
	return nativeBrowserFocusMetadata{contextRef: state.ContextRef, generation: state.Generation}, nil
}

// Only a fresh open_pages call can request a one-shot display selection. A
// collection remains a tool artifact, never the single active page's source.
// Verify the proposed background tab against both this host's pre-operation
// identity and its current bound state; missing/old worker metadata is inert.
func (b *nativeBrowser) sanitizePageCollectionFocus(ctx context.Context, reply *tools.BrowserReply, identity nativeBrowserFocusMetadata, action string) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(reply.Data, &fields) != nil || fields == nil {
		return
	}
	candidate := fields["focus"]
	delete(fields, "focus")
	defer func() {
		if data, err := json.Marshal(fields); err == nil {
			reply.Data = data
		}
	}()
	if action != tools.BrowserActionOpenPages || len(candidate) == 0 || !validNativeContextRef(identity.contextRef) {
		return
	}
	var collection tools.BrowserPagesResult
	if json.Unmarshal(reply.Data, &collection) != nil || collection.Kind != "browser-pages-v1" || collection.Operation != action || collection.Focus == nil || len(collection.Pages) > tools.MaxBrowserPages {
		return
	}
	focus := collection.Focus
	if focus.ContextRef != identity.contextRef || focus.Generation != identity.generation || focus.ControlEpoch < 0 || focus.ControlEpoch > 1<<53-1 ||
		!identity.matches(tools.BrowserReply{ActiveTab: focus.TabID, Version: focus.Version}) {
		return
	}
	matched := false
	for index, page := range collection.Pages {
		if page.Index != index || !page.OK || page.Status != "observed" || page.ObservationPending {
			continue
		}
		// The first successful input row is the sole display candidate. A
		// concurrently changed page is not replaced by a later surprise focus.
		matched = page.TabID == focus.TabID && page.ContextRef == focus.ContextRef && page.Generation == focus.Generation &&
			page.Version == focus.Version && page.ControlEpoch != nil && *page.ControlEpoch == focus.ControlEpoch
		break
	}
	if !matched {
		return
	}
	metadataCtx, cancel := context.WithTimeout(ctx, nativeBrowserFocusMetadataTimeout)
	defer cancel()
	target := nativeBrowserTarget{TabID: focus.TabID, ContextRef: focus.ContextRef}
	raw, err := b.request(metadataCtx, http.MethodGet, target.suffix("/state"), nil)
	if err != nil {
		return
	}
	var state struct {
		ContextRef   string `json:"context_ref"`
		ViewedTab    string `json:"viewed_tab"`
		Version      string `json:"version"`
		Generation   int64  `json:"generation"`
		Control      string `json:"control"`
		ControlEpoch int64  `json:"control_epoch"`
	}
	if json.Unmarshal(raw, &state) == nil && state.ContextRef == focus.ContextRef && state.ViewedTab == focus.TabID &&
		state.Version == focus.Version && state.Generation == focus.Generation && state.Control == "agent" && state.ControlEpoch == focus.ControlEpoch {
		// Keep the host-confirmed display identity closed even if a future
		// worker adds unrelated or private fields to its focus proposal.
		if value, err := json.Marshal(focus); err == nil {
			fields["focus"] = value
		}
	}
}
