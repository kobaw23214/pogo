package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
)

// Metadata views never grant access to another store or describe source bodies.
// Retention is local session bookkeeping, like todo state, not a source edit.
func (t *ToolResultRead) executeResultMetadata(ctx context.Context, raw json.RawMessage, args toolResultReadArgs) (Result, error) {
	duplicate, err := jsonHasDuplicateObjectKeys(raw)
	if err != nil || duplicate {
		return Result{}, fmt.Errorf("tool_result metadata arguments must be unambiguous JSON without duplicate keys")
	}
	allowed := []string{"view", "offset", "limit", "tool", "filter"}
	if args.View != "list" {
		allowed = []string{"view", "result_id"}
		if args.View == "retain" {
			allowed = append(allowed, "days")
		}
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return Result{}, fmt.Errorf("tool_result metadata requires a JSON object")
	}
	for _, name := range sortedKeys(fields) {
		if !contains(allowed, name) || bytes.Equal(bytes.TrimSpace(fields[name]), []byte("null")) {
			return Result{}, fmt.Errorf("view=%s does not accept null or unsupported field %q", args.View, resultUTF8Prefix(name, 64))
		}
	}
	if t.Store == nil {
		return Result{}, fmt.Errorf("tool result store is unavailable for this session")
	}
	if args.View == "list" {
		limit := 20
		if args.Limit != nil {
			limit = *args.Limit
		}
		if args.Offset < 0 || limit < 1 || limit > 100 || len(args.Tool) > 256 || len(args.Filter) > 512 {
			return Result{}, fmt.Errorf("list requires offset>=0, limit=1..100, tool<=256 bytes and filter<=512 bytes")
		}
		page, err := t.Store.List(ctx, ResultCatalogOptions{Tool: args.Tool, Filter: args.Filter, Offset: args.Offset, Limit: limit})
		if err != nil {
			return Result{}, err
		}
		return resultMetadataJSON(page, page)
	}
	if !validResultID(args.ResultID) {
		return Result{}, fmt.Errorf("view=%s requires a host-assigned result_id from this session", args.View)
	}
	var receipt ResultRetentionDetails
	if args.View == "retain" {
		days := 0
		if args.Days != nil {
			days = *args.Days
			if days < 1 || days > 90 {
				return Result{}, fmt.Errorf("retain days must be 1..90; omit days for the 30-day default")
			}
		}
		receipt, err = t.Store.Retain(ctx, args.ResultID, days)
	} else {
		receipt, err = t.Store.Release(ctx, args.ResultID)
	}
	if err != nil {
		return Result{}, err
	}
	return resultMetadataJSON(receipt, receipt)
}

func resultMetadataJSON(value, details any) (Result, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return Result{}, fmt.Errorf("tool result metadata could not be encoded")
	}
	var envelope map[string]any
	if json.Unmarshal(data, &envelope) == nil {
		envelope["metadata_only"] = true
		if _, catalog := details.(ResultCatalogDetails); catalog {
			envelope["view"] = "list"
		}
		data, _ = json.Marshal(envelope)
	}
	if len(data) > resultReadOutputMax {
		return Result{}, fmt.Errorf("tool result metadata exceeds page budget; request a smaller list limit")
	}
	return Result{Text: string(data), Details: details}, nil
}
