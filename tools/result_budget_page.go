package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
)

// ModelResultDelivery is host-only accounting for the view actually delivered
// to the model. When rows were requested, statistics alone are not Delivered.
// An explicit empty terminal page is a delivery, not a budget failure.
type ModelResultDelivery struct {
	RequestKey          string          `json:"request_key"`
	BudgetTokens        int64           `json:"budget_tokens"`
	Delivered           bool            `json:"delivered"`
	Undeliverable       bool            `json:"undeliverable"`
	StatisticsDelivered bool            `json:"statistics_delivered,omitempty"`
	NextRequest         json.RawMessage `json:"next_request,omitempty"`
}

type resultBudgetRequest struct {
	args toolResultReadArgs
	call map[string]any
	key  string
}

// ToolResultRequestKey normalizes host defaults without reading or executing a
// tool. A json/native limit has a type-dependent default; missing remains
// distinct from an explicit limit rather than guessing the selected value type.
func ToolResultRequestKey(raw json.RawMessage) (string, bool) {
	request, ok := resultBudgetNormalizeRequest(raw)
	return request.key, ok
}

func resultBudgetNormalizeRequest(raw json.RawMessage) (resultBudgetRequest, bool) {
	var out resultBudgetRequest
	duplicate, err := jsonHasDuplicateObjectKeys(raw)
	if err != nil || duplicate {
		return out, false
	}
	fields, err := resultQueryObject(raw, "arguments", []string{"result_id", "view", "pointer", "offset", "limit", "where", "select", "aggregate", "include_rows", "pattern", "context_lines", "sort", "group_by"}, []string{"result_id"})
	if err != nil {
		return out, false
	}
	for _, value := range fields {
		if strings.TrimSpace(string(value)) == "null" {
			return out, false
		}
	}
	var args toolResultReadArgs
	if json.Unmarshal(raw, &args) != nil || !validResultID(args.ResultID) || args.Offset < 0 || args.Limit != nil && (*args.Limit <= 0 || *args.Limit > resultReadTextMax) || len(args.Pointer) > 4096 {
		return out, false
	}
	if args.View == "" {
		args.View = "summary"
	}
	if args.View != "search" && resultHasSearchFields(raw) {
		return out, false
	}
	if _, err := resultQueryPointer(args.Pointer); err != nil {
		return out, false
	}
	if args.View != "query" && (args.Where != nil || args.Select != nil || args.Aggregate != nil || args.IncludeRows != nil || args.Sort != nil || args.GroupBy != nil) {
		return out, false
	}
	call := map[string]any{"result_id": args.ResultID, "view": args.View, "pointer": args.Pointer}
	switch args.View {
	case "search":
		args, err = resultDecodeSearchArgs(raw)
		if err != nil {
			return out, false
		}
		contextLines, limit := 1, 20
		if args.ContextLines != nil {
			contextLines = *args.ContextLines
		}
		if args.Limit != nil {
			limit = *args.Limit
		}
		call = map[string]any{"result_id": args.ResultID, "view": "search", "pattern": args.Pattern, "context_lines": contextLines, "offset": args.Offset, "limit": limit}
	case "summary":
		if args.Pointer != "" || args.Offset != 0 || args.Limit != nil {
			return out, false
		}
		delete(call, "pointer")
	case "text":
		if args.Pointer != "" {
			return out, false
		}
		delete(call, "pointer")
		call["offset"], call["limit"] = args.Offset, resultReadTextDefault
		if args.Limit != nil {
			call["limit"] = *args.Limit
		}
	case "json", "native":
		if args.View == "native" && args.Pointer == "" && (args.Offset != 0 || args.Limit != nil) {
			return out, false
		}
		call["offset"] = args.Offset
		if args.Limit != nil {
			call["limit"] = *args.Limit
		}
	case "query":
		args, err = resultDecodeQueryArgs(raw)
		if err != nil || args.Limit != nil && *args.Limit > resultReadArrayMax {
			return out, false
		}
		if _, err := resultCompilePredicates(context.Background(), args.Where); err != nil {
			return out, false
		}
		if _, err := resultCompileQuerySort(args.Sort, len(args.GroupBy) > 0); err != nil {
			return out, false
		}
		grouped := make(map[string]bool)
		for _, pointer := range args.GroupBy {
			if _, err := resultQueryPointer(pointer); err != nil || grouped[pointer] {
				return out, false
			}
			grouped[pointer] = true
		}
		selected := make(map[string]bool)
		for _, pointer := range args.Select {
			if _, err := resultQueryPointer(pointer); err != nil {
				return out, false
			}
			if selected[pointer] {
				return out, false
			}
			selected[pointer] = true
		}
		if args.Aggregate != nil {
			switch args.Aggregate.Op {
			case "count":
				if args.Aggregate.Pointer != "" {
					return out, false
				}
			case "sum", "min", "max", "avg":
			default:
				return out, false
			}
			if _, err := resultQueryPointer(args.Aggregate.Pointer); err != nil {
				return out, false
			}
			call["aggregate"] = args.Aggregate
		}
		include := resultQueryIncludesRows(args)
		call["include_rows"] = include
		if resultQueryHasPage(args) {
			call["offset"], call["limit"] = args.Offset, 20
			if args.Limit != nil {
				call["limit"] = *args.Limit
			}
		}
		if len(args.Sort) > 0 {
			call["sort"] = args.Sort
		}
		if len(args.GroupBy) > 0 {
			call["group_by"] = args.GroupBy
		}
		if len(args.Where) > 0 {
			conditions := make([]any, len(args.Where))
			for i, condition := range args.Where {
				value := condition.Value
				if len(value) == 0 && condition.Op == "exists" {
					value = json.RawMessage("true")
				}
				decoded, _ := resultDecodeJSON(value)
				conditions[i] = map[string]any{"pointer": condition.Pointer, "op": condition.Op, "value": decoded}
			}
			call["where"] = conditions
		}
		if len(args.Select) > 0 {
			call["select"] = args.Select
		}
	default:
		return out, false
	}
	encoded, err := json.Marshal(call)
	if err != nil {
		return out, false
	}
	sum := sha256.Sum256(encoded)
	return resultBudgetRequest{args: args, call: call, key: hex.EncodeToString(sum[:])}, true
}

func (r *Registry) budgetReadPage(ctx context.Context, in ModelResultBudgetRequest, limit int) (ModelResultBudgetView, bool) {
	request, ok := resultBudgetNormalizeRequest(in.Args)
	if !ok || in.IsError || ctx.Err() != nil {
		return ModelResultBudgetView{}, false
	}
	if request.args.View == "text" {
		return r.budgetReadTextPage(ctx, in, limit, request)
	}
	value, ok := resultDecodeJSON([]byte(in.Text))
	host, object := value.(map[string]any)
	if !ok || !object || host["result_id"] != request.args.ResultID || host["view"] != request.args.View || host["pointer"] != request.args.Pointer {
		return ModelResultBudgetView{}, false
	}
	delivery := &ModelResultDelivery{RequestKey: request.key, BudgetTokens: in.Tokens}
	view := ModelResultBudgetView{Text: in.Text, ResultID: in.ResultID, Delivery: delivery}
	if items, rows := host["items"].([]any); rows {
		return budgetReadRowPage(in, limit, request, host, items, delivery), true
	}
	if _, fragment := host["value_fragment"].(string); fragment {
		return budgetReadStringPage(in, limit, request, host, delivery), true
	}
	if request.args.View == "query" && !resultQueryHasPage(request.args) {
		compact := budgetReadCompactMetadata(host)
		delivery.StatisticsDelivered = host["scan_complete"] == true
		delivery.Delivered = delivery.StatisticsDelivered
		if len(in.Text) <= limit {
			return view, true
		}
		encoded := resultBudgetJSON(compact)
		if len(encoded) <= limit {
			view.Text, view.Reduced, view.Recoverable = encoded, true, true
			return view, true
		}
		return budgetReadNoContent(in, request, host, delivery, limit), true
	}
	if len(in.Text) <= limit {
		_, scalar := host["value"]
		delivery.Delivered = scalar || request.args.View == "summary" || request.args.View == "native" && request.args.Pointer == ""
		delivery.Undeliverable = !delivery.Delivered
		if !delivery.Delivered {
			delivery.NextRequest = budgetReadDeeperRequest(request, host)
		}
		return view, true
	}
	compact := budgetReadCompactMetadata(host)
	if _, scalar := host["value"]; scalar && len(resultBudgetJSON(compact)) <= limit {
		delivery.Delivered = true
		view.Text, view.Reduced, view.Recoverable = resultBudgetJSON(compact), true, true
		return view, true
	}
	view = budgetReadNoContent(in, request, host, delivery, limit)
	delivery.NextRequest = budgetReadDeeperRequest(request, host)
	return budgetReadPublishNext(view, limit), true
}

func budgetReadDeeperRequest(request resultBudgetRequest, host map[string]any) json.RawMessage {
	var key string
	if value, ok := host["value"].(map[string]any); ok {
		keys := resultJSONKeys(value)
		if len(keys) > 0 {
			key = keys[0]
		}
	} else if directory, ok := host["directory"].(map[string]any); ok {
		if fields, ok := directory["fields"].([]any); ok && len(fields) > 0 {
			if field, ok := fields[0].(map[string]any); ok {
				key, _ = field["name"].(string)
			}
		}
	}
	if key == "" || request.args.View != "json" && request.args.View != "native" {
		return nil
	}
	pointer := request.args.Pointer + "/" + strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1")
	if len(pointer) > 4096 {
		return nil
	}
	next, _ := json.Marshal(map[string]any{"result_id": request.args.ResultID, "view": request.args.View, "pointer": pointer})
	return next
}

func budgetReadCompactMetadata(host map[string]any) map[string]any {
	compact := make(map[string]any, len(host)+3)
	for key, value := range host {
		compact[key] = value
	}
	compact["model_budget_page"] = true
	delete(compact, "original_tool") // source identity remains bound by result_id
	compact["coverage"] = "Selected saved observation only; source completeness unknown. complete concerns delivered stored content."
	if host["view"] == "query" {
		compact["coverage"] = "All matching rows in the selected saved array only; source completeness unknown. complete concerns delivered rows/statistics."
		if host["groups_included"] == true {
			compact["coverage"] = "Computed groups over matching saved rows only; underlying source rows are not delivered. complete concerns delivered groups, never source completeness."
		}
		compact["aggregate_displayed"] = host["aggregate"] != nil
		compact["statistics_displayed"] = host["scan_complete"] == true
	} else if host["view"] == "search" {
		compact["coverage"] = "Literal hits in saved text only; source completeness unknown. complete concerns displayed matches."
	}
	if originalError, ok := compact["original_error"].(string); ok && len(originalError) > 128 {
		compact["original_error"] = resultBudgetStringPrefix(originalError, 128)
		compact["original_error_omitted"] = true
	}
	if aggregate, ok := host["aggregate"].(map[string]any); ok {
		copyAggregate := make(map[string]any, len(aggregate))
		for key, value := range aggregate {
			copyAggregate[key] = value
		}
		copyAggregate["scope"] = "all matching rows in selected saved array; independent of pagination"
		copyAggregate["encoding"] = "exact integer or rational string; null when no numeric values"
		compact["aggregate"] = copyAggregate
	}
	return compact
}

func budgetReadInteger(value any) (int, bool) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, false
	}
	n, err := strconv.Atoi(string(number))
	return n, err == nil && n >= 0
}

func budgetReadNext(request resultBudgetRequest, offset, limit int) json.RawMessage {
	call := make(map[string]any, len(request.call))
	for key, value := range request.call {
		call[key] = value
	}
	call["offset"], call["limit"] = offset, max(1, limit)
	encoded, _ := json.Marshal(call)
	return encoded
}
