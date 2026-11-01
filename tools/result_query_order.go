package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"
	"strings"
)

const (
	resultQueryMaxSort       = 4
	resultQueryMaxGroupBy    = 4
	resultQueryMaxGroups     = 1000
	resultQueryMaxGroupBytes = 8192
)

type resultQuerySort struct {
	Pointer   string `json:"pointer"`
	Direction string `json:"direction"`
}

type resultQuerySortKey struct {
	pointer   string
	tokens    []string
	desc      bool
	exactText bool // Only host-computed aggregate rational strings use this.
}

// kind order is used only for deterministic group identity/order. Explicit
// sorting requires homogeneous non-null scalar types within each sort column.
// Null and missing remain distinct and always sort last, in that order.
type resultQueryOrderValue struct {
	kind    int // 0=missing, 1=null, 2=bool, 3=number, 4=string
	boolean bool
	number  *big.Rat
	text    string
}

type resultQueryOrderedRow struct {
	index  int
	values []resultQueryOrderValue
}

type resultQueryGroup struct {
	keys   []resultQueryOrderValue
	fields []any
	stats  resultQueryStatistics
	item   map[string]any
	order  []resultQueryOrderValue
}

func resultQuerySortSchema() map[string]any {
	item := object([]string{"pointer", "direction"}, map[string]any{
		"pointer":   prop("string", "Row pointer; grouped: /count, /aggregate/value, /keys/0/value. /aggregate/value and /aggregate/sum sort as exact rationals."),
		"direction": map[string]any{"type": "string", "enum": []string{"asc", "desc"}},
	})
	item["additionalProperties"] = false
	return map[string]any{
		"type": "array", "minItems": 1, "maxItems": resultQueryMaxSort, "items": item,
		"description": "Up to 4 stable keys before paging. Each key needs one scalar type; mixed non-null types/containers fail. Null then missing always last. Ties retain row order or group-key order. Rows must be included.",
	}
}

func resultQueryGroupBySchema() map[string]any {
	return map[string]any{
		"type": "array", "minItems": 1, "maxItems": resultQueryMaxGroupBy,
		"items":       prop("string", "Row scalar pointer; empty selects scalar row."),
		"description": "Up to 4 keys; missing/null distinct, equal numbers merge. Requires aggregate; no select/include_rows=true. Items: keys(present/value), count, aggregate, group_index. Scan all before group paging (default20/max200). Max1000 groups/8192 encoded key bytes; overflow fails. Default key order: bool,number,string,null,missing. offset counts groups, not source rows.",
	}
}

func resultQueryHasPage(args toolResultReadArgs) bool {
	return len(args.GroupBy) > 0 || resultQueryIncludesRows(args)
}

func resultValidateQueryOrderFields(fields map[string]json.RawMessage) error {
	if raw, present := fields["sort"]; present {
		var values []json.RawMessage
		if json.Unmarshal(raw, &values) != nil || len(values) < 1 || len(values) > resultQueryMaxSort {
			return fmt.Errorf("query sort must be an array of 1..%d sort objects", resultQueryMaxSort)
		}
		for index, value := range values {
			if _, err := resultQueryObject(value, fmt.Sprintf("sort[%d]", index), []string{"pointer", "direction"}, []string{"pointer", "direction"}); err != nil {
				return err
			}
		}
	}
	if raw, present := fields["group_by"]; present {
		var values []json.RawMessage
		if json.Unmarshal(raw, &values) != nil || len(values) < 1 || len(values) > resultQueryMaxGroupBy {
			return fmt.Errorf("query group_by must be an array of 1..%d RFC 6901 pointers", resultQueryMaxGroupBy)
		}
		for index, value := range values {
			if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
				return fmt.Errorf("query group_by[%d] cannot be null; use an explicit RFC 6901 pointer", index)
			}
		}
	}
	return nil
}

func resultValidateQueryOrderArgs(args toolResultReadArgs, fields map[string]json.RawMessage) error {
	if len(args.GroupBy) > 0 {
		if args.Aggregate == nil {
			return fmt.Errorf("query group_by requires aggregate; use aggregate.op=count for group counts")
		}
		if _, present := fields["select"]; present || args.IncludeRows != nil && *args.IncludeRows {
			return fmt.Errorf("grouped query returns groups, not original rows; select and include_rows=true are unavailable")
		}
	} else if len(args.Sort) > 0 && !resultQueryIncludesRows(args) {
		return fmt.Errorf("query sort requires include_rows=true unless group_by is supplied")
	}
	return nil
}

func resultCompileQuerySort(conditions []resultQuerySort, grouped bool) ([]resultQuerySortKey, error) {
	if len(conditions) > resultQueryMaxSort {
		return nil, fmt.Errorf("query sort accepts at most %d keys", resultQueryMaxSort)
	}
	keys := make([]resultQuerySortKey, 0, len(conditions))
	seen := make(map[string]bool)
	for index, condition := range conditions {
		tokens, err := resultQueryPointer(condition.Pointer)
		if err != nil {
			return nil, fmt.Errorf("query sort[%d]: %w", index, err)
		}
		if condition.Direction != "asc" && condition.Direction != "desc" {
			return nil, fmt.Errorf("query sort[%d].direction must be asc or desc", index)
		}
		if seen[condition.Pointer] {
			return nil, fmt.Errorf("query sort contains a duplicate pointer")
		}
		if grouped && (condition.Pointer == "/group_index" || strings.HasPrefix(condition.Pointer, "/group_index/")) {
			return nil, fmt.Errorf("group_index is assigned after sorting and cannot be a sort key")
		}
		seen[condition.Pointer] = true
		keys = append(keys, resultQuerySortKey{
			pointer: condition.Pointer, tokens: tokens, desc: condition.Direction == "desc",
			exactText: grouped && (condition.Pointer == "/aggregate/value" || condition.Pointer == "/aggregate/sum"),
		})
	}
	return keys, nil
}

func resultQueryOrderScalar(value any, present bool) (resultQueryOrderValue, error) {
	if !present {
		return resultQueryOrderValue{}, nil
	}
	switch typed := value.(type) {
	case nil:
		return resultQueryOrderValue{kind: 1}, nil
	case bool:
		return resultQueryOrderValue{kind: 2, boolean: typed}, nil
	case json.Number:
		number, err := resultQueryNumber(typed)
		return resultQueryOrderValue{kind: 3, number: number}, err
	case string:
		return resultQueryOrderValue{kind: 4, text: typed}, nil
	default:
		return resultQueryOrderValue{}, fmt.Errorf("sorting/grouping requires scalar values; got %s", resultJSONType(value))
	}
}

func resultQueryCompareOrder(left, right resultQueryOrderValue, descending bool) int {
	if left.kind < 2 || right.kind < 2 {
		// Null is after all real values; missing is after null regardless of direction.
		rank := func(kind int) int {
			if kind == 0 {
				return 2
			}
			if kind == 1 {
				return 1
			}
			return 0
		}
		return rank(left.kind) - rank(right.kind)
	}
	comparison := left.kind - right.kind
	if comparison == 0 {
		switch left.kind {
		case 2:
			if left.boolean != right.boolean {
				if left.boolean {
					comparison = 1
				} else {
					comparison = -1
				}
			}
		case 3:
			comparison = left.number.Cmp(right.number)
		case 4:
			comparison = strings.Compare(left.text, right.text)
		}
	}
	if descending {
		return -comparison
	}
	return comparison
}

func resultQuerySortValues(item any, keys []resultQuerySortKey, types []int) ([]resultQueryOrderValue, error) {
	values := make([]resultQueryOrderValue, len(keys))
	for index, key := range keys {
		value, present := resultQueryLookup(item, key.tokens)
		var scalar resultQueryOrderValue
		var err error
		if key.exactText && present && value != nil {
			text, ok := value.(string)
			if !ok {
				return nil, fmt.Errorf("host aggregate is not an exact rational string")
			}
			number, ok := new(big.Rat).SetString(text)
			if !ok {
				return nil, fmt.Errorf("host aggregate is not an exact rational number")
			}
			scalar = resultQueryOrderValue{kind: 3, number: number}
		} else {
			scalar, err = resultQueryOrderScalar(value, present)
			if err != nil {
				return nil, fmt.Errorf("sort field %q: %w", resultUTF8Prefix(key.pointer, 128), err)
			}
		}
		if scalar.kind >= 2 {
			if types[index] != 0 && types[index] != scalar.kind {
				return nil, fmt.Errorf("sort field %q has mixed non-null JSON types; filter to one type before sorting", resultUTF8Prefix(key.pointer, 128))
			}
			types[index] = scalar.kind
		}
		values[index] = scalar
	}
	return values, nil
}

// Bottom-up stable mergesort checks cancellation while comparing and copying.
// No comparator changes ordering mid-sort when a context is canceled.
func resultQueryStableOrder(ctx context.Context, order []int, compare func(int, int) int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	buffer := make([]int, len(order))
	for width := 1; width < len(order); width *= 2 {
		for start := 0; start < len(order); start += width * 2 {
			middle, end := min(start+width, len(order)), min(start+width*2, len(order))
			left, right := start, middle
			for at := start; at < end; at++ {
				if err := ctx.Err(); err != nil {
					return err
				}
				if right == end || left < middle && compare(order[left], order[right]) <= 0 {
					buffer[at], left = order[left], left+1
				} else {
					buffer[at], right = order[right], right+1
				}
			}
		}
		copy(order, buffer)
	}
	return ctx.Err()
}

func resultQueryCompareKeys(left, right []resultQueryOrderValue, keys []resultQuerySortKey) int {
	for index := range left {
		desc := len(keys) > 0 && keys[index].desc
		if comparison := resultQueryCompareOrder(left[index], right[index], desc); comparison != 0 {
			return comparison
		}
	}
	return 0
}

func resultQueryGroupKey(row any, pointers []string, tokens [][]string) (string, []resultQueryOrderValue, []any, error) {
	identity := make([]any, len(tokens))
	values := make([]resultQueryOrderValue, len(tokens))
	fields := make([]any, len(tokens))
	for index, token := range tokens {
		value, present := resultQueryLookup(row, token)
		scalar, err := resultQueryOrderScalar(value, present)
		if err != nil {
			return "", nil, nil, fmt.Errorf("group_by field %q: %w", resultUTF8Prefix(pointers[index], 128), err)
		}
		if scalar.kind == 4 && len(scalar.text) > resultQueryMaxGroupBytes {
			return "", nil, nil, fmt.Errorf("combined group key exceeds %d encoded bytes; no partial statistics returned", resultQueryMaxGroupBytes)
		}
		values[index] = scalar
		field := map[string]any{"pointer": pointers[index], "present": present}
		if present {
			field["value"] = value
		}
		fields[index] = field
		canonical := value
		if scalar.kind == 3 {
			canonical = scalar.number.RatString()
			// Keep one original exact JSON numeric spelling as the displayed
			// representative; identity/order use the normalized rational above.
		}
		identity[index] = []any{scalar.kind, canonical}
	}
	encoded, err := json.Marshal(identity)
	if err != nil {
		return "", nil, nil, err
	}
	if len(encoded) > resultQueryMaxGroupBytes {
		return "", nil, nil, fmt.Errorf("combined group key exceeds %d encoded bytes; no partial statistics returned", resultQueryMaxGroupBytes)
	}
	return string(encoded), values, fields, nil
}

func readResultOrderedQuery(ctx context.Context, record StoredToolResult, args toolResultReadArgs, details ToolResultReadDetails, rows []any, limit int, predicates []resultQueryPredicate, projections []resultQueryProjection, aggregateTokens []string) (Result, error) {
	grouped := len(args.GroupBy) > 0
	if grouped && (args.Aggregate == nil || args.Select != nil || args.IncludeRows != nil && *args.IncludeRows) {
		return Result{}, fmt.Errorf("grouped query requires aggregate and does not return select/include_rows=true source rows")
	}
	if len(args.GroupBy) > resultQueryMaxGroupBy {
		return Result{}, fmt.Errorf("query group_by accepts at most %d pointers", resultQueryMaxGroupBy)
	}
	sortKeys, err := resultCompileQuerySort(args.Sort, grouped)
	if err != nil {
		return Result{}, err
	}
	groupTokens := make([][]string, len(args.GroupBy))
	seen := make(map[string]bool)
	for index, pointer := range args.GroupBy {
		if seen[pointer] {
			return Result{}, fmt.Errorf("query group_by contains a duplicate pointer")
		}
		seen[pointer] = true
		groupTokens[index], err = resultQueryPointer(pointer)
		if err != nil {
			return Result{}, fmt.Errorf("query group_by: %w", err)
		}
	}
	stats := resultQueryStatistics{}
	orderedRows := make([]resultQueryOrderedRow, 0)
	groups := make([]resultQueryGroup, 0)
	groupIndex := make(map[string]int)
	sortTypes := make([]int, len(sortKeys))
	for index, row := range rows {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		matches := true
		for conditionIndex, predicate := range predicates {
			matches, err = resultQueryMatch(ctx, row, predicate)
			if err != nil {
				return Result{}, fmt.Errorf("query condition %d at original array index %d: %w; no partial result returned", conditionIndex, index, err)
			}
			if !matches {
				break
			}
		}
		if !matches {
			continue
		}
		stats.matched++
		if grouped {
			identity, keys, fields, err := resultQueryGroupKey(row, args.GroupBy, groupTokens)
			if err != nil {
				return Result{}, fmt.Errorf("query group at original array index %d: %w", index, err)
			}
			position, exists := groupIndex[identity]
			if !exists {
				if len(groups) == resultQueryMaxGroups {
					return Result{}, fmt.Errorf("query exceeds %d groups; narrow the saved selection; no partial statistics returned", resultQueryMaxGroups)
				}
				position = len(groups)
				groupIndex[identity] = position
				groups = append(groups, resultQueryGroup{keys: keys, fields: fields})
			}
			group := &groups[position]
			group.stats.matched++
			if args.Aggregate.Op != "count" {
				if err := group.stats.add(row, aggregateTokens); err != nil {
					return Result{}, fmt.Errorf("query group aggregate at original array index %d: %w; no partial statistics returned", index, err)
				}
			}
		} else {
			values, err := resultQuerySortValues(row, sortKeys, sortTypes)
			if err != nil {
				return Result{}, fmt.Errorf("query sort at original array index %d: %w; no partial result returned", index, err)
			}
			orderedRows = append(orderedRows, resultQueryOrderedRow{index, values})
			if args.Aggregate != nil && args.Aggregate.Op != "count" {
				if err := stats.add(row, aggregateTokens); err != nil {
					return Result{}, fmt.Errorf("query aggregate at original array index %d: %w; no partial statistics returned", index, err)
				}
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	total := len(orderedRows)
	if grouped {
		total = len(groups)
	}
	if args.Offset > total {
		return Result{}, fmt.Errorf("query offset exceeds available %s count %d", map[bool]string{true: "group", false: "matching saved row"}[grouped], total)
	}
	order := make([]int, total)
	for index := range order {
		order[index] = index
	}
	if grouped {
		// Establish key order first so aggregate-sort ties never depend on map
		// iteration or first occurrence in the source array.
		if err := resultQueryStableOrder(ctx, order, func(left, right int) int { return resultQueryCompareKeys(groups[left].keys, groups[right].keys, nil) }); err != nil {
			return Result{}, err
		}
		for index := range groups {
			if err := ctx.Err(); err != nil {
				return Result{}, err
			}
			aggregate := groups[index].stats.output(*args.Aggregate)
			aggregate["scope"] = "all matching saved rows with this exact group key; independent of group page offset/limit; not remote/source completeness"
			groups[index].item = map[string]any{"keys": groups[index].fields, "count": json.Number(strconv.Itoa(groups[index].stats.matched)), "aggregate": aggregate}
			groups[index].order, err = resultQuerySortValues(groups[index].item, sortKeys, sortTypes)
			if err != nil {
				return Result{}, fmt.Errorf("query group sort: %w; no partial statistics returned", err)
			}
		}
		if len(sortKeys) > 0 {
			if err := resultQueryStableOrder(ctx, order, func(left, right int) int {
				return resultQueryCompareKeys(groups[left].order, groups[right].order, sortKeys)
			}); err != nil {
				return Result{}, err
			}
		}
	} else {
		if err := resultQueryStableOrder(ctx, order, func(left, right int) int {
			return resultQueryCompareKeys(orderedRows[left].values, orderedRows[right].values, sortKeys)
		}); err != nil {
			return Result{}, err
		}
	}
	envelope := resultReadEnvelope(record, details)
	envelope["coverage"] = "Query computed over every row of the selected saved JSON array only. It does not establish remote/source completeness; original_is_error and stored_text_complete still apply. Sorted row pages contain only the matching rows actually returned. Group pages are computed statistics, not delivery of their underlying source rows. complete concerns this output page sequence, never remote/source coverage."
	envelope["stored_array_item_count"], envelope["scanned_rows"], envelope["matched_rows"] = len(rows), len(rows), stats.matched
	envelope["scan_complete"], envelope["page_displayed"] = true, true
	envelope["rows_included"], envelope["groups_included"] = !grouped, grouped
	if len(args.Sort) > 0 {
		envelope["sort"] = args.Sort
	}
	if grouped {
		envelope["group_by"], envelope["total_groups"] = args.GroupBy, total
		envelope["range_unit"] = "groups; group_index is the zero-based position after complete grouping and sorting, not an original source row index"
	} else {
		envelope["range_unit"] = "sorted matching rows; original indices remain in each item"
		if args.Aggregate != nil {
			envelope["aggregate"] = stats.output(*args.Aggregate)
		}
	}
	baseJSON, err := json.Marshal(envelope)
	if err != nil {
		return Result{}, fmt.Errorf("encode query metadata: %w", err)
	}
	budget := min(resultReadJSONBudget, resultReadOutputMax-len(baseJSON)-2048)
	if budget < 1024 {
		return Result{}, fmt.Errorf("query metadata exceeds inline budget; use shorter pointers")
	}
	items := make([]any, 0, min(limit, total-args.Offset))
	next, omitted := args.Offset, false
	for next < total && len(items) < limit {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		position := order[next]
		var item map[string]any
		if grouped {
			item = groups[position].item
			item["group_index"] = next
		} else {
			row := orderedRows[position]
			item = resultQueryRow(rows[row.index], row.index, projections)
		}
		encoded, err := json.Marshal(item)
		if err != nil {
			return Result{}, fmt.Errorf("encode ordered query item: %w", err)
		}
		if len(encoded)+1 > budget {
			if len(items) > 0 {
				break
			}
			if grouped {
				return Result{}, fmt.Errorf("one computed group cannot fit the inline budget; use shorter group pointers/keys; no partial group statistics returned")
			}
			row := orderedRows[position]
			item = map[string]any{"original_index": row.index, "pointer_suffix": "/" + strconv.Itoa(row.index), "omitted_value": true, "read_more": "Use view=json with the query array pointer plus pointer_suffix. Sorted query offset/next_offset count sorted matches, not original indices."}
			encoded, _ = json.Marshal(item)
			omitted = true
		}
		items = append(items, item)
		budget -= len(encoded) + 1
		next++
	}
	details.Total, details.Omitted, details.ScanComplete = total, omitted, true
	details.Complete = next == total && !omitted
	if next < total {
		details.NextOffset = &next
		envelope["next_offset"] = next
	}
	envelope["items"], envelope["shown_items"] = items, len(items)
	envelope["complete"], envelope["omitted"] = details.Complete, omitted
	return boundedToolResultRead(envelope, details)
}
