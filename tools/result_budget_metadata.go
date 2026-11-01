package tools

import "encoding/json"

// A catalog page is navigation metadata, not a stored observation. It must not
// be converted to an unavailable result or acquire a fabricated recovery ID.
func budgetResultMetadata(in ModelResultBudgetRequest, limit int) (ModelResultBudgetView, bool) {
	var args struct {
		View   string `json:"view"`
		Offset int    `json:"offset"`
		Limit  *int   `json:"limit"`
	}
	if in.IsError || json.Unmarshal(in.Args, &args) != nil || args.View != "list" && args.View != "retain" && args.View != "release" {
		return ModelResultBudgetView{}, false
	}
	view := ModelResultBudgetView{Text: in.Text, ResultID: in.ResultID}
	if len(in.Text) <= limit || args.View != "list" {
		// Retain/release are bounded host receipts. Preserve their outcome even
		// at an identity-floor overrun; never suggest repeating a retention edit.
		return view, true
	}
	value, ok := resultDecodeJSON([]byte(in.Text))
	host, object := value.(map[string]any)
	if !ok || !object {
		return view, true
	}
	items, ok := host["items"].([]any)
	if !ok {
		return view, true
	}
	var original map[string]any
	if json.Unmarshal(in.Args, &original) != nil {
		return view, true
	}
	makePage := func(n int) string {
		page := make(map[string]any, len(host)+3)
		for key, value := range host {
			page[key] = value
		}
		page["items"], page["metadata_only"], page["page_displayed"] = items[:n], true, true
		page["model_budget_page"] = true
		if n < len(items) {
			item, ok := items[n-1].(map[string]any)
			cursor, valid := budgetReadInteger(item["catalog_offset"])
			if !ok || !valid {
				return ""
			}
			page["next_offset"], page["scan_complete"] = cursor+1, false
			page["scanned"] = cursor + 1 - args.Offset
		}
		return resultBudgetJSON(page)
	}
	for n := len(items); n > 0; n-- {
		if encoded := makePage(n); encoded != "" && len(encoded) <= limit {
			view.Text, view.Reduced, view.Recoverable = encoded, true, true
			return view, true
		}
	}
	page := map[string]any{
		"view": "list", "metadata_only": true, "page_displayed": false,
		"model_budget_omitted": true, "offset": args.Offset,
		"read_more": "No catalog entry was displayed. Keep this offset and filters, use a smaller limit or more context space. Do not advance the catalog cursor.",
	}
	if args.Limit == nil || *args.Limit > 1 {
		original["offset"], original["limit"] = args.Offset, 1
		page["next_request"] = original
	}
	view.Text, view.Reduced, view.Recoverable = resultBudgetJSON(page), true, true
	return view, true
}
