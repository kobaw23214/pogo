package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
	"unicode/utf8"
)

func budgetReadRowPage(in ModelResultBudgetRequest, limit int, request resultBudgetRequest, host map[string]any, items []any, delivery *ModelResultDelivery) ModelResultBudgetView {
	offset, valid := budgetReadInteger(host["offset"])
	if !valid || offset != request.args.Offset {
		return budgetReadNoContent(in, request, host, delivery, limit)
	}
	grouped := request.args.View == "query" && len(request.args.GroupBy) > 0
	if request.args.View == "query" && !budgetReadQueryPageMetadata(request, host, grouped) {
		return budgetReadNoContent(in, request, host, delivery, limit)
	}
	real := 0
	lastOriginal := -1
	seenOriginal := make(map[int]bool)
	for _, item := range items {
		receipt, object := item.(map[string]any)
		if request.args.View == "search" && (!object || !budgetReadSearchItem(request, host, receipt, offset+real)) {
			return budgetReadNoContent(in, request, host, delivery, limit)
		}
		if request.args.View == "query" {
			if grouped {
				if !object || !budgetReadQueryGroupItem(request, host, receipt, offset+real) {
					return budgetReadNoContent(in, request, host, delivery, limit)
				}
			} else {
				index, ok := budgetReadInteger(receipt["original_index"])
				if !object || !ok || seenOriginal[index] || len(request.args.Sort) == 0 && index <= lastOriginal || receipt["pointer_suffix"] != "/"+strconv.Itoa(index) {
					return budgetReadNoContent(in, request, host, delivery, limit)
				}
				seenOriginal[index], lastOriginal = true, index
			}
		}
		if object && host["omitted"] == true && receipt["omitted_value"] == true {
			break // never skip a missing original row because the host advanced
		}
		real++
	}
	empty := len(items) == 0 && host["complete"] == true && host["omitted"] != true
	if request.args.View == "search" && empty && host["scan_complete"] == true {
		return budgetReadEmptySearch(in, request, host, delivery, limit)
	}
	if len(in.Text) <= limit && real == len(items) {
		delivery.Delivered = real > 0 || empty
		delivery.Undeliverable = !delivery.Delivered
		delivery.StatisticsDelivered = host["scan_complete"] == true && (host["aggregate"] != nil || grouped && real > 0)
		if next, ok := budgetReadInteger(host["next_offset"]); ok && next > offset {
			delivery.NextRequest = budgetReadNext(request, next, max(1, real))
		}
		return ModelResultBudgetView{Text: in.Text, ResultID: in.ResultID, Delivery: delivery}
	}
	base := budgetReadCompactMetadata(host)
	makePage := func(n int) string {
		base["items"], base["shown_items"] = items[:n], n
		base["page_displayed"], base["omitted"] = n > 0 || empty, false
		base["complete"] = n == len(items) && host["complete"] == true && real == len(items)
		delete(base, "next_offset")
		if n > 0 && (n < len(items) || host["next_offset"] != nil || host["complete"] != true) {
			base["next_offset"] = offset + n
		}
		return resultBudgetJSON(base)
	}
	low, high := 0, real
	for low < high {
		middle := low + (high-low+1)/2
		if len(makePage(middle)) <= limit {
			low = middle
		} else {
			high = middle - 1
		}
	}
	if low > 0 || empty && len(makePage(0)) <= limit {
		delivery.Delivered = true
		delivery.StatisticsDelivered = base["scan_complete"] == true && (base["aggregate"] != nil || grouped && low > 0)
		encoded := makePage(low)
		if next, ok := base["next_offset"].(int); ok {
			remaining := max(1, len(items)-low)
			delivery.NextRequest = budgetReadNext(request, next, remaining)
		}
		return ModelResultBudgetView{Text: encoded, ResultID: in.ResultID, Reduced: true, Recoverable: true, Delivery: delivery}
	}
	view := budgetReadNoContent(in, request, host, delivery, limit)
	if grouped {
		// A group is computed data, not an original JSON array row. Keep
		// aggregate/group/sort semantics and never append group_index to pointer.
		if request.args.Limit == nil || *request.args.Limit > 1 {
			delivery.NextRequest = budgetReadNext(request, offset, 1)
		}
	} else if request.args.Aggregate != nil && delivery.StatisticsDelivered {
		call := budgetReadCopyCall(request)
		delete(call, "aggregate")
		call["include_rows"], call["offset"], call["limit"] = true, offset, 1
		delivery.NextRequest, _ = json.Marshal(call)
	} else if len(items) > 0 && request.args.View == "search" {
		if item, ok := items[0].(map[string]any); ok {
			delivery.NextRequest = budgetReadSearchNext(request, item)
		}
	} else if len(items) > 0 {
		index := offset
		if item, ok := items[0].(map[string]any); ok && request.args.View == "query" {
			if actual, valid := budgetReadInteger(item["original_index"]); valid {
				index = actual
			}
		}
		// Select the original row, not the next row after an omission receipt.
		call := map[string]any{"result_id": request.args.ResultID, "view": request.args.View, "pointer": request.args.Pointer + "/" + strconv.Itoa(index)}
		if request.args.View == "query" {
			call["view"] = "json"
		}
		delivery.NextRequest, _ = json.Marshal(call)
	}
	return budgetReadPublishNext(view, limit)
}

// Validate query page identity before treating item ordinals as delivered.
// Sorted source rows and computed groups have different index domains.
func budgetReadQueryPageMetadata(request resultBudgetRequest, host map[string]any, grouped bool) bool {
	if len(request.args.Sort) > 0 {
		// Decode the typed declaration before comparing: struct field order
		// differs from canonical map key order despite identical JSON values.
		expected, valid := resultDecodeJSON([]byte(resultBudgetJSON(request.args.Sort)))
		if !valid || resultBudgetJSON(host["sort"]) != resultBudgetJSON(expected) {
			return false
		}
	}
	if !grouped {
		return host["groups_included"] != true && host["rows_included"] == true
	}
	total, totalOK := budgetReadInteger(host["total_groups"])
	matched, matchedOK := budgetReadInteger(host["matched_rows"])
	return totalOK && matchedOK && total <= matched && total <= resultQueryMaxGroups &&
		host["groups_included"] == true && host["rows_included"] == false && host["scan_complete"] == true &&
		resultBudgetJSON(host["group_by"]) == resultBudgetJSON(request.args.GroupBy)
}

func budgetReadQueryGroupItem(request resultBudgetRequest, host, item map[string]any, ordinal int) bool {
	index, indexOK := budgetReadInteger(item["group_index"])
	total, totalOK := budgetReadInteger(host["total_groups"])
	count, countOK := budgetReadInteger(item["count"])
	matched, matchedOK := budgetReadInteger(host["matched_rows"])
	keys, keysOK := item["keys"].([]any)
	aggregate, aggregateOK := item["aggregate"].(map[string]any)
	_, originalIndex := item["original_index"]
	_, originalPointer := item["pointer_suffix"]
	if !indexOK || index != ordinal || !totalOK || index >= total || !countOK || count <= 0 || !matchedOK || count > matched ||
		!keysOK || len(keys) != len(request.args.GroupBy) || !aggregateOK || request.args.Aggregate == nil || originalIndex || originalPointer {
		return false
	}
	for position, key := range keys {
		field, ok := key.(map[string]any)
		if !ok || field["pointer"] != request.args.GroupBy[position] {
			return false
		}
		present, valid := field["present"].(bool)
		value, exists := field["value"]
		if !valid || present != exists {
			return false
		}
		if _, err := resultQueryOrderScalar(value, present); err != nil {
			return false
		}
	}
	groupMatched, valid := budgetReadInteger(aggregate["matched_rows"])
	if !valid || groupMatched != count || aggregate["op"] != request.args.Aggregate.Op || aggregate["pointer"] != request.args.Aggregate.Pointer {
		return false
	}
	value, hasValue := aggregate["value"]
	if !hasValue {
		return false
	}
	if request.args.Aggregate.Op == "count" {
		return value == strconv.Itoa(count)
	}
	numeric, numericOK := budgetReadInteger(aggregate["numeric_count"])
	missing, missingOK := budgetReadInteger(aggregate["missing_count"])
	null, nullOK := budgetReadInteger(aggregate["null_count"])
	nonNumeric, nonNumericOK := budgetReadInteger(aggregate["non_numeric_count"])
	if !numericOK || !missingOK || !nullOK || !nonNumericOK || numeric > count || missing > count || null > count || nonNumeric > count || numeric+missing+null+nonNumeric != count {
		return false
	}
	_, exactString := value.(string)
	return numeric == 0 && value == nil || numeric > 0 && exactString
}

func budgetReadNoContent(in ModelResultBudgetRequest, request resultBudgetRequest, host map[string]any, delivery *ModelResultDelivery, limit int) ModelResultBudgetView {
	delivery.Undeliverable = true
	delivery.Delivered, delivery.StatisticsDelivered = false, false
	minimal := map[string]any{
		"result_id": request.args.ResultID, "view": request.args.View, "pointer": request.args.Pointer,
		"offset": request.args.Offset, "complete": false, "page_displayed": false,
		"model_budget_page": true, "model_budget_omitted": true,
		"original_is_error": host["original_is_error"], "stored_text_complete": host["stored_text_complete"],
		"coverage":  "Selected saved observation only; source completeness unknown.",
		"read_more": "No requested content fits. Select narrower fields/range or make more context space; do not repeat the same request at this budget.",
	}
	grouped := request.args.View == "query" && len(request.args.GroupBy) > 0
	if grouped {
		minimal["groups_included"], minimal["rows_included"] = true, false
		minimal["groups_displayed"], minimal["statistics_displayed"] = false, false
		minimal["coverage"] = "Computed group statistics were not delivered; scan completion does not deliver groups or their underlying source rows."
		minimal["read_more"] = "No computed group fits. Retry with limit=1 if the previous limit was larger, or make more context space/use shorter group keys. Keep group_by, aggregate, sort and offset; never read a group_index as an original JSON row."
	} else if request.args.View == "query" && !resultQueryHasPage(request.args) {
		minimal["read_more"] = "Statistics omitted. Do not add limit. Retry with more context space or a narrower aggregate."
	}
	if grouped && host["scan_complete"] == true {
		for _, key := range []string{"total_groups", "scan_complete", "matched_rows", "scanned_rows", "stored_array_item_count"} {
			if value, exists := host[key]; exists {
				minimal[key] = value
			}
		}
		minimal["scan_statistics_displayed"] = true
		for _, key := range []string{"group_by", "sort"} {
			if value, exists := host[key]; exists {
				resultBudgetAdd(minimal, key, value, max(limit, 1024))
			}
		}
	} else if host["view"] == "query" && host["scan_complete"] == true {
		compact := budgetReadCompactMetadata(host)
		for _, key := range []string{"aggregate", "scan_complete", "matched_rows", "scanned_rows", "stored_array_item_count"} {
			if value, exists := compact[key]; exists {
				minimal[key] = value
			}
		}
		minimal["aggregate_displayed"] = host["aggregate"] != nil
		minimal["statistics_displayed"] = true
		if len(resultBudgetJSON(minimal)) <= limit {
			delivery.StatisticsDelivered = true
		} else {
			for _, key := range []string{"aggregate", "scan_complete", "matched_rows", "scanned_rows", "stored_array_item_count", "statistics_displayed"} {
				delete(minimal, key)
			}
			minimal["aggregate_displayed"] = false
		}
	}
	if host["view"] == "search" && host["scan_complete"] == true {
		for _, key := range []string{"scan_complete", "total_matches", "stored_text_bytes", "scanned_text_bytes"} {
			if value, exists := host[key]; exists {
				minimal[key] = value
			}
		}
		minimal["read_more"] = "Search completed; no hit context fit. Narrow context_lines or use the exact saved-text byte range."
		// Scan completion is not page delivery. Do not replace absent counts
		// with zero when even this bounded scan receipt cannot fit.
		if len(resultBudgetJSON(minimal)) > max(limit, 1024) {
			for _, key := range []string{"scan_complete", "total_matches", "stored_text_bytes", "scanned_text_bytes"} {
				delete(minimal, key)
			}
		}
	}
	return ModelResultBudgetView{Text: resultBudgetJSON(minimal), ResultID: in.ResultID, Reduced: true, Recoverable: true, Delivery: delivery}
}

func budgetReadPublishNext(view ModelResultBudgetView, limit int) ModelResultBudgetView {
	if view.Delivery == nil || len(view.Delivery.NextRequest) == 0 {
		return view
	}
	var envelope map[string]any
	if json.Unmarshal([]byte(view.Text), &envelope) != nil {
		return view
	}
	// At zero delivery, a concrete selected-row pointer is more useful than
	// repeated prose. Never add this overhead to a normally delivered page.
	delete(envelope, "read_more")
	envelope["next_request"] = view.Delivery.NextRequest
	encoded := resultBudgetJSON(envelope)
	if len(encoded) <= limit {
		view.Text = encoded
	} else if !view.Delivery.StatisticsDelivered && len(view.Delivery.NextRequest) <= 512 && len(encoded) <= max(limit, 1024) {
		// A small exact selected-row pointer may share the identity floor.
		// Never extend that floor for filters/projections or displace stats.
		var next map[string]any
		if json.Unmarshal(view.Delivery.NextRequest, &next) == nil {
			_, textOffset := next["offset"]
			_, textLimit := next["limit"]
			searchContext, search := next["context_lines"].(float64)
			pointer := len(next) == 3 && next["result_id"] != nil && next["view"] != nil && next["pointer"] != nil
			textRange := len(next) == 4 && next["view"] == "text" && next["result_id"] != nil && textOffset && textLimit
			literalContext := len(next) == 6 && next["view"] == "search" && next["result_id"] != nil && next["pattern"] != nil && search && searchContext == 0
			if pointer || textRange || literalContext {
				view.Text = encoded
			}
		}
	}
	return view
}

func budgetReadSearchItem(request resultBudgetRequest, host, item map[string]any, index int) bool {
	matchIndex, indexOK := budgetReadInteger(item["match_index"])
	start, startOK := budgetReadInteger(item["match_start"])
	end, endOK := budgetReadInteger(item["match_end"])
	contextStart, contextStartOK := budgetReadInteger(item["context_start"])
	contextEnd, contextEndOK := budgetReadInteger(item["context_end"])
	line, lineOK := budgetReadInteger(item["line_number"])
	totalBytes, totalOK := budgetReadInteger(host["stored_text_bytes"])
	text, textOK := item["text"].(string)
	return indexOK && matchIndex == index && startOK && endOK && contextStartOK && contextEndOK && lineOK && line > 0 && totalOK &&
		contextStart <= start && start < end && end <= contextEnd && contextEnd <= totalBytes && textOK && utf8.ValidString(text) &&
		len(text) == contextEnd-contextStart && len(text) <= resultSearchSnippetMax && end-start == len(request.args.Pattern) &&
		text[start-contextStart:end-contextStart] == request.args.Pattern
}

func budgetReadSearchNext(request resultBudgetRequest, item map[string]any) json.RawMessage {
	if request.args.ContextLines == nil || *request.args.ContextLines > 0 {
		call := budgetReadCopyCall(request)
		call["context_lines"], call["limit"] = 0, 1
		next, _ := json.Marshal(call)
		return next
	}
	start, startOK := budgetReadInteger(item["match_start"])
	end, endOK := budgetReadInteger(item["match_end"])
	if !startOK || !endOK || end <= start {
		return nil
	}
	// A search offset counts matches. A recovery text offset counts original
	// UTF-8 bytes; never append a JSON pointer or advance past an unseen hit.
	next, _ := json.Marshal(map[string]any{"result_id": request.args.ResultID, "view": "text", "offset": start, "limit": min(resultReadTextMax, max(end-start, 256))})
	return next
}

func budgetReadEmptySearch(in ModelResultBudgetRequest, request resultBudgetRequest, host map[string]any, delivery *ModelResultDelivery, limit int) ModelResultBudgetView {
	delivery.Delivered, delivery.Undeliverable = true, false
	if len(in.Text) <= limit {
		return ModelResultBudgetView{Text: in.Text, ResultID: in.ResultID, Delivery: delivery}
	}
	// The host fully scanned this immutable text. An empty terminal page is
	// useful evidence even if mandatory status metadata exceeds a tiny budget.
	minimal := map[string]any{
		"result_id": request.args.ResultID, "view": "search", "offset": request.args.Offset,
		"items": []any{}, "shown_items": 0, "complete": true, "page_displayed": true,
		"scan_complete": true, "total_matches": host["total_matches"],
		"original_is_error": host["original_is_error"], "stored_text_complete": host["stored_text_complete"],
		"coverage": "Saved text only; source completeness unknown.", "model_budget_page": true,
	}
	if originalError, ok := host["original_error"].(string); ok && strings.TrimSpace(originalError) != "" {
		minimal["original_error"] = resultBudgetStringPrefix(originalError, 128)
	}
	return ModelResultBudgetView{Text: resultBudgetJSON(minimal), ResultID: in.ResultID, Reduced: true, Recoverable: true, Delivery: delivery}
}

func budgetReadCopyCall(request resultBudgetRequest) map[string]any {
	call := make(map[string]any, len(request.call))
	for key, value := range request.call {
		call[key] = value
	}
	return call
}

func budgetReadStringPage(in ModelResultBudgetRequest, limit int, request resultBudgetRequest, host map[string]any, delivery *ModelResultDelivery) ModelResultBudgetView {
	body, _ := host["value_fragment"].(string)
	start, valid := budgetReadInteger(host["offset"])
	if !valid {
		return budgetReadNoContent(in, request, host, delivery, limit)
	}
	if len(in.Text) <= limit {
		delivery.Delivered = len(body) > 0 || host["complete"] == true
		delivery.Undeliverable = !delivery.Delivered
		if next, ok := budgetReadInteger(host["next_offset"]); ok && next > start {
			delivery.NextRequest = budgetReadNext(request, next, max(1, len(body)))
		}
		return ModelResultBudgetView{Text: in.Text, ResultID: in.ResultID, Delivery: delivery}
	}
	base := budgetReadCompactMetadata(host)
	_, stringFingerprints := host["page_sha256"].(string)
	makePage := func(bytes int) string {
		fragment := resultUTF8Prefix(body, bytes)
		end := start + len(fragment)
		base["value_fragment"], base["end_offset"] = fragment, end
		if stringFingerprints {
			// The model allocator may shorten the host's page again. Never
			// keep the original page digest beside a different fragment.
			digest := sha256.Sum256([]byte(fragment))
			base["page_sha256"], base["page_string_bytes"] = hex.EncodeToString(digest[:]), len(fragment)
		}
		base["fragment_only"] = start != 0 || len(fragment) < len(body) || host["fragment_only"] == true
		base["complete"] = len(fragment) == len(body) && host["complete"] == true
		base["page_displayed"] = len(fragment) > 0 || body == "" && host["complete"] == true
		delete(base, "next_offset")
		if len(fragment) < len(body) || host["next_offset"] != nil {
			base["next_offset"] = end
		}
		return resultBudgetJSON(base)
	}
	fit := func() int {
		low, high := 0, len(body)
		for low < high {
			n := low + (high-low+1)/2
			if len(makePage(n)) <= limit {
				low = n
			} else {
				high = n - 1
			}
		}
		return low
	}
	low := fit()
	if stringFingerprints && (resultUTF8Prefix(body, low) == "" && body != "" || len(makePage(low)) > limit) {
		// Fingerprints are optional metadata: keep an actual read page
		// useful at small budgets before falling back to an omitted receipt.
		for _, field := range []string{"stored_value_sha256", "page_sha256", "page_string_bytes", "hash_scope"} {
			delete(base, field)
		}
		stringFingerprints = false
		low = fit()
	}
	fragment := resultUTF8Prefix(body, low)
	if fragment == "" && body != "" || len(makePage(low)) > limit {
		return budgetReadNoContent(in, request, host, delivery, limit)
	}
	delivery.Delivered = true
	encoded := makePage(low)
	if next, ok := base["next_offset"].(int); ok {
		delivery.NextRequest = budgetReadNext(request, next, max(1, len(body)-len(fragment)))
	}
	return ModelResultBudgetView{Text: encoded, ResultID: in.ResultID, Reduced: true, Recoverable: true, Delivery: delivery}
}

func (r *Registry) budgetReadTextPage(ctx context.Context, in ModelResultBudgetRequest, limit int, request resultBudgetRequest) (ModelResultBudgetView, bool) {
	var details ToolResultReadDetails
	if json.Unmarshal(in.Details, &details) != nil || details.ResultID != request.args.ResultID || details.View != "text" {
		if r == nil || r.results == nil {
			return ModelResultBudgetView{}, false
		}
		record, err := r.results.Load(ctx, request.args.ResultID)
		if err != nil || request.args.Offset > len(record.Text) {
			return ModelResultBudgetView{}, false
		}
		n := resultReadTextDefault
		if request.args.Limit != nil {
			n = *request.args.Limit
		}
		start, end := resultTextRange(record.Text, request.args.Offset, n)
		details = ToolResultReadDetails{ResultID: record.ID, View: "text", Offset: start, Total: len(record.Text), Complete: end == len(record.Text), TextComplete: record.TextComplete, OriginalIsError: record.IsError, OriginalError: record.Error}
		if end < len(record.Text) {
			details.NextOffset = &end
		}
	}
	end := details.Total
	if details.NextOffset != nil {
		end = *details.NextOffset
	}
	bytes := end - details.Offset
	if bytes < 0 || bytes > len(in.Text) {
		return ModelResultBudgetView{}, false
	}
	body := in.Text[len(in.Text)-bytes:]
	if !utf8.ValidString(body) {
		return ModelResultBudgetView{}, false
	}
	delivery := &ModelResultDelivery{RequestKey: request.key, BudgetTokens: in.Tokens}
	if len(in.Text) <= limit {
		delivery.Delivered = body != "" || details.Complete
		if details.NextOffset != nil {
			delivery.NextRequest = budgetReadNext(request, *details.NextOffset, max(1, len(body)))
		}
		return ModelResultBudgetView{Text: in.Text, ResultID: in.ResultID, Delivery: delivery}, true
	}
	host := map[string]any{
		"result_id": request.args.ResultID, "view": "text", "pointer": "", "offset": details.Offset,
		"stored_string_bytes": details.Total, "value_fragment": body, "fragment_only": details.Offset > 0 || !details.Complete,
		"complete": details.Complete, "original_is_error": details.OriginalIsError, "original_error": details.OriginalError,
		"stored_text_complete": details.TextComplete, "range_unit": "UTF-8 bytes of stored text",
	}
	if details.NextOffset != nil {
		host["next_offset"] = json.Number(strconv.Itoa(*details.NextOffset))
	}
	host["offset"] = json.Number(strconv.Itoa(details.Offset))
	return budgetReadStringPage(in, limit, request, host, delivery), true
}
