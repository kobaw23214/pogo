package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"strconv"
	"strings"
)

const (
	resultQueryMaxRows         = 100000
	resultQueryMaxConditions   = 16
	resultQueryMaxSelect       = 32
	resultQueryMaxNumberDigits = 256
	resultQueryMaxExponent     = 512
	resultQueryMaxDepth        = 64
)

type resultQueryCondition struct {
	Pointer string          `json:"pointer"`
	Op      string          `json:"op"`
	Value   json.RawMessage `json:"value"`
}

type resultQueryAggregate struct {
	Op      string `json:"op"`
	Pointer string `json:"pointer"`
}

type resultQueryPredicate struct {
	pointer string
	tokens  []string
	op      string
	value   any
}

type resultQueryProjection struct {
	pointer string
	tokens  []string
}

type resultQueryStatistics struct {
	matched, numeric, missing, null, nonNumeric int
	sum, minimum, maximum                       *big.Rat
}

func resultQueryWhereSchema() map[string]any {
	condition := object([]string{"pointer", "op"}, map[string]any{
		"pointer": prop("string", "Row RFC 6901 pointer; empty selects the row; null invalid."),
		"op":      map[string]any{"type": "string", "enum": []string{"eq", "ne", "lt", "lte", "gt", "gte", "contains", "exists"}},
		"value":   map[string]any{"description": "Required except exists (boolean, default true); null valid for eq/ne. Exact numbers: <=256 mantissa digits, |exponent|<=512."},
	})
	condition["additionalProperties"] = false
	return map[string]any{
		"type": "array", "maxItems": resultQueryMaxConditions,
		"description": "Up to 16 ordered AND filters. exists tests presence (null is present); use before optional-field comparisons. eq needs equal JSON types; ne permits differing types. Ordered comparisons need numbers or strings. contains tests substring or exact array member. Type errors fail; no coercion.",
		"items":       condition,
	}
}

func resultQueryAggregateSchema() map[string]any {
	schema := object([]string{"op"}, map[string]any{
		"op":      map[string]any{"type": "string", "enum": []string{"count", "sum", "min", "max", "avg"}, "description": "All matching saved rows or each full group, before pagination. count counts rows; numeric ops use JSON numbers and report missing/null/non-numeric counts. Exact integer/rational string; null when no numbers."},
		"pointer": prop("string", "Row RFC 6901 pointer; count requires omitted/empty."),
	})
	schema["additionalProperties"] = false
	return schema
}

// Query requests are intentionally stricter than legacy tool arguments. In a
// local analysis request, silently ignoring sort/group_by changes its meaning.
// Validate exact key spelling and null/presence before the typed decoder, whose
// normal case-insensitive matching and null-to-zero behavior are too lenient.
func resultDecodeQueryArgs(raw json.RawMessage) (toolResultReadArgs, error) {
	var args toolResultReadArgs
	duplicate, err := jsonHasDuplicateObjectKeys(raw)
	if err != nil || duplicate {
		return args, fmt.Errorf("query arguments must be unambiguous JSON without duplicate keys")
	}
	fields, err := resultQueryObject(raw, "arguments", []string{"result_id", "view", "pointer", "offset", "limit", "where", "select", "aggregate", "include_rows", "sort", "group_by"}, []string{"result_id", "view"})
	if err != nil {
		return args, err
	}
	for _, name := range []string{"pointer", "offset", "limit", "where", "select", "aggregate", "include_rows", "sort", "group_by"} {
		if value, present := fields[name]; present && bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return args, fmt.Errorf("query field %q cannot be null; omit optional fields, or use pointer \"\" for the saved root array", name)
		}
	}
	if value, present := fields["where"]; present {
		var conditions []json.RawMessage
		if json.Unmarshal(value, &conditions) != nil {
			return args, fmt.Errorf("query where must be an array of condition objects")
		}
		if len(conditions) > resultQueryMaxConditions {
			return args, fmt.Errorf("query where accepts at most %d conditions", resultQueryMaxConditions)
		}
		for index, condition := range conditions {
			conditionFields, err := resultQueryObject(condition, fmt.Sprintf("where[%d]", index), []string{"pointer", "op", "value"}, []string{"pointer", "op"})
			if err != nil {
				return args, err
			}
			var operation string
			if json.Unmarshal(conditionFields["op"], &operation) != nil {
				return args, fmt.Errorf("query where[%d].op must be a string", index)
			}
			if _, present := conditionFields["value"]; !present && operation != "exists" {
				return args, fmt.Errorf("query where[%d] requires value for %q; supply an explicit JSON value, including null, or use op=exists to test presence", index, resultUTF8Prefix(operation, 32))
			}
		}
	}
	if value, present := fields["select"]; present {
		var selected []json.RawMessage
		if json.Unmarshal(value, &selected) != nil {
			return args, fmt.Errorf("query select must be an array of RFC 6901 strings")
		}
		if len(selected) > resultQueryMaxSelect {
			return args, fmt.Errorf("query select accepts at most %d field pointers", resultQueryMaxSelect)
		}
		for index, pointer := range selected {
			if bytes.Equal(bytes.TrimSpace(pointer), []byte("null")) {
				return args, fmt.Errorf("query select[%d] cannot be null; use \"\" for the whole row or an explicit field pointer", index)
			}
		}
	}
	if value, present := fields["aggregate"]; present {
		aggregate, err := resultQueryObject(value, "aggregate", []string{"op", "pointer"}, []string{"op"})
		if err != nil {
			return args, err
		}
		if pointer, present := aggregate["pointer"]; present && bytes.Equal(bytes.TrimSpace(pointer), []byte("null")) {
			return args, fmt.Errorf("query aggregate.pointer cannot be null; omit it or use \"\" for the whole row")
		}
	}
	if err := resultValidateQueryOrderFields(fields); err != nil {
		return args, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&args); err != nil {
		return args, fmt.Errorf("invalid query arguments: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return args, fmt.Errorf("query accepts exactly one JSON argument object")
	}
	if err := resultValidateQueryOrderArgs(args, fields); err != nil {
		return args, err
	}
	if !resultQueryHasPage(args) {
		for _, name := range []string{"offset", "limit", "select"} {
			if _, explicit := fields[name]; explicit {
				return args, fmt.Errorf("query field %q requires include_rows=true; aggregate queries omit row details by default. Remove offset/limit/select for an aggregate-only view, or explicitly enable rows", name)
			}
		}
	}
	return args, nil
}

func resultQueryIncludesRows(args toolResultReadArgs) bool {
	if args.IncludeRows != nil {
		return *args.IncludeRows
	}
	return args.Aggregate == nil
}

func resultQueryObject(raw json.RawMessage, location string, allowed, required []string) (map[string]json.RawMessage, error) {
	if len(bytes.TrimSpace(raw)) == 0 || bytes.TrimSpace(raw)[0] != '{' {
		return nil, fmt.Errorf("query %s must be an object", location)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, fmt.Errorf("query %s must be a JSON object", location)
	}
	for _, name := range sortedKeys(fields) {
		if !contains(allowed, name) {
			return nil, fmt.Errorf("query %s has unsupported field %q; supported fields: %s. Only the declared bounded query operations are supported; arbitrary code is not supported", location, resultUTF8Prefix(name, 64), strings.Join(allowed, ", "))
		}
	}
	for _, name := range required {
		value, present := fields[name]
		if !present || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, fmt.Errorf("query %s requires non-null field %q; a row pointer may be \"\" but must be explicit", location, name)
		}
	}
	return fields, nil
}

// This operates on immutable observed JSON only. It never executes code, loads
// a native protocol/media value or makes another request to the original tool.
func readResultQuery(ctx context.Context, record StoredToolResult, args toolResultReadArgs, details ToolResultReadDetails) (Result, error) {
	if len(args.Where) > resultQueryMaxConditions || len(args.Select) > resultQueryMaxSelect {
		return Result{}, fmt.Errorf("query accepts at most %d conditions and %d selected fields", resultQueryMaxConditions, resultQueryMaxSelect)
	}
	limit := 20
	if args.Limit != nil {
		limit = *args.Limit
	}
	if limit > resultReadArrayMax {
		return Result{}, fmt.Errorf("query limit must not exceed %d matching rows", resultReadArrayMax)
	}
	predicates, err := resultCompilePredicates(ctx, args.Where)
	if err != nil {
		return Result{}, err
	}
	projections := make([]resultQueryProjection, 0, len(args.Select))
	seen := make(map[string]bool)
	for _, pointer := range args.Select {
		tokens, err := resultQueryPointer(pointer)
		if err != nil {
			return Result{}, fmt.Errorf("query select: %w", err)
		}
		if seen[pointer] {
			return Result{}, fmt.Errorf("query select contains a duplicate pointer")
		}
		seen[pointer] = true
		projections = append(projections, resultQueryProjection{pointer, tokens})
	}
	var aggregateTokens []string
	if args.Aggregate != nil {
		switch args.Aggregate.Op {
		case "count":
			if args.Aggregate.Pointer != "" {
				return Result{}, fmt.Errorf("query aggregate count counts matching records and requires an empty pointer")
			}
		case "sum", "min", "max", "avg":
		default:
			return Result{}, fmt.Errorf("query aggregate op must be count, sum, min, max or avg")
		}
		aggregateTokens, err = resultQueryPointer(args.Aggregate.Pointer)
		if err != nil {
			return Result{}, fmt.Errorf("query aggregate: %w", err)
		}
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	root, ok := resultJSONValue(record)
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if !ok {
		return Result{}, fmt.Errorf("query requires valid saved structured/text JSON; native protocol data is not queried; use summary or text")
	}
	value, err := resultResolvePointer(root, args.Pointer)
	if err != nil {
		return Result{}, err
	}
	rows, ok := value.([]any)
	if !ok {
		return Result{}, fmt.Errorf("query pointer must select a saved JSON array")
	}
	// Reject before scanning: a partial scan must never appear to be a complete
	// count/aggregate. Narrow the upstream tool query rather than this local page.
	if len(rows) > resultQueryMaxRows {
		return Result{}, fmt.Errorf("selected saved array has %d rows, exceeding query scan limit %d; narrow the query at the original server/tool and save a smaller result; no partial statistics were computed", len(rows), resultQueryMaxRows)
	}
	if len(args.Sort) > 0 || len(args.GroupBy) > 0 {
		return readResultOrderedQuery(ctx, record, args, details, rows, limit, predicates, projections, aggregateTokens)
	}
	stats := resultQueryStatistics{}
	includeRows := resultQueryIncludesRows(args)
	indices := make([]int, 0, limit)
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
		if includeRows && stats.matched >= args.Offset && len(indices) < limit {
			indices = append(indices, index)
		}
		stats.matched++
		if args.Aggregate != nil && args.Aggregate.Op != "count" {
			if err := stats.add(row, aggregateTokens); err != nil {
				return Result{}, fmt.Errorf("query aggregate at original array index %d: %w; no partial statistics returned", index, err)
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if args.Offset > stats.matched {
		return Result{}, fmt.Errorf("query offset exceeds matching saved row count %d", stats.matched)
	}
	envelope := resultReadEnvelope(record, details)
	envelope["coverage"] = "Query computed over every row of the selected saved JSON array only. It does not establish remote/source completeness; original_is_error and stored_text_complete still apply. Numeric aggregates use only valid JSON numbers, with missing/null/non-numeric counts separate. With rows_included=true complete concerns delivery of matching rows; with rows_included=false complete concerns completion of the requested count/aggregate view. Neither establishes source coverage."
	envelope["stored_array_item_count"], envelope["scanned_rows"], envelope["matched_rows"] = len(rows), len(rows), stats.matched
	envelope["scan_complete"] = true
	envelope["rows_included"], envelope["page_displayed"] = includeRows, includeRows
	if args.Aggregate != nil {
		envelope["aggregate"] = stats.output(*args.Aggregate)
	}
	if !includeRows {
		// Deliberately omit items entirely: an undisplayed row page is not an
		// empty data set. The requested aggregate/count view is complete.
		details.Total, details.Complete = stats.matched, true
		envelope["complete"] = true
		return boundedToolResultRead(envelope, details)
	}
	envelope["range_unit"] = "matching rows; original indices remain in each item"
	baseJSON, err := json.Marshal(envelope)
	if err != nil {
		return Result{}, fmt.Errorf("encode query metadata: %w", err)
	}
	budget := min(resultReadJSONBudget, resultReadOutputMax-len(baseJSON)-2048)
	if budget < 1024 {
		return Result{}, fmt.Errorf("query metadata exceeds inline budget; use a shorter array pointer")
	}
	items := make([]any, 0, len(indices))
	next := args.Offset
	omitted := false
	for _, index := range indices {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		item := resultQueryRow(rows[index], index, projections)
		encoded, err := json.Marshal(item)
		if err != nil {
			return Result{}, fmt.Errorf("encode query row at original index %d: %w", index, err)
		}
		if len(encoded)+1 > budget {
			if len(items) > 0 {
				break
			}
			// A single oversized row gets an explicit receipt. Its original
			// position remains readable via json, and pagination still advances.
			item = map[string]any{
				"original_index": index, "pointer_suffix": "/" + strconv.Itoa(index),
				"omitted_value": true,
				"read_more":     "Use view=json and append pointer_suffix to this query's pointer. Select a child field or use byte ranges for strings. Query offset/next_offset count matching rows, not original indices.",
			}
			encoded, _ = json.Marshal(item)
			omitted = true
		}
		items = append(items, item)
		budget -= len(encoded) + 1
		next++
	}
	details.Total, details.Omitted = stats.matched, omitted
	details.Complete = next == stats.matched && !omitted
	if next < stats.matched {
		details.NextOffset = &next
		envelope["next_offset"] = next
	}
	envelope["items"], envelope["shown_items"] = items, len(items)
	envelope["complete"], envelope["omitted"] = details.Complete, omitted
	return boundedToolResultRead(envelope, details)
}

func resultCompilePredicates(ctx context.Context, conditions []resultQueryCondition) ([]resultQueryPredicate, error) {
	predicates := make([]resultQueryPredicate, 0, len(conditions))
	for index, condition := range conditions {
		tokens, err := resultQueryPointer(condition.Pointer)
		if err != nil {
			return nil, fmt.Errorf("query condition %d pointer: %w", index, err)
		}
		switch condition.Op {
		case "eq", "ne", "lt", "lte", "gt", "gte", "contains", "exists":
		default:
			return nil, fmt.Errorf("query condition %d has unsupported op", index)
		}
		var value any = true
		if len(condition.Value) == 0 {
			if condition.Op != "exists" {
				return nil, fmt.Errorf("query condition %d requires value (JSON null must be explicit)", index)
			}
		} else {
			if len(condition.Value) > 8192 {
				return nil, fmt.Errorf("query condition %d value exceeds 8192 bytes", index)
			}
			var valid bool
			value, valid = resultDecodeJSON(condition.Value)
			if !valid {
				return nil, fmt.Errorf("query condition %d value must be unambiguous JSON", index)
			}
		}
		if condition.Op == "exists" {
			if _, ok := value.(bool); !ok {
				return nil, fmt.Errorf("query condition %d exists value must be boolean", index)
			}
		}
		switch condition.Op {
		case "lt", "lte", "gt", "gte":
			switch value.(type) {
			case string, json.Number:
			default:
				return nil, fmt.Errorf("query condition %d ordered comparison value must be a string or JSON number", index)
			}
		}
		if err := resultQueryValidateValue(ctx, value, 0); err != nil {
			return nil, fmt.Errorf("query condition %d value: %w", index, err)
		}
		predicates = append(predicates, resultQueryPredicate{condition.Pointer, tokens, condition.Op, value})
	}
	return predicates, nil
}

func resultQueryPointer(pointer string) ([]string, error) {
	if len(pointer) > 4096 {
		return nil, fmt.Errorf("RFC 6901 pointer exceeds 4096 bytes")
	}
	if pointer == "" {
		return nil, nil
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, fmt.Errorf("RFC 6901 pointer must be empty or start with /")
	}
	tokens := strings.Split(pointer[1:], "/")
	if len(tokens) > resultQueryMaxDepth {
		return nil, fmt.Errorf("query pointer exceeds %d levels", resultQueryMaxDepth)
	}
	for index, token := range tokens {
		var decoded strings.Builder
		for offset := 0; offset < len(token); offset++ {
			if token[offset] != '~' {
				decoded.WriteByte(token[offset])
				continue
			}
			offset++
			if offset >= len(token) || token[offset] != '0' && token[offset] != '1' {
				return nil, fmt.Errorf("invalid RFC 6901 escape; use ~0 and ~1")
			}
			if token[offset] == '0' {
				decoded.WriteByte('~')
			} else {
				decoded.WriteByte('/')
			}
		}
		tokens[index] = decoded.String()
	}
	return tokens, nil
}

func resultQueryLookup(value any, tokens []string) (any, bool) {
	for _, token := range tokens {
		switch typed := value.(type) {
		case map[string]any:
			var present bool
			value, present = typed[token]
			if !present {
				return nil, false
			}
		case []any:
			index, err := strconv.Atoi(token)
			if err != nil || index < 0 || index >= len(typed) || strconv.Itoa(index) != token {
				return nil, false
			}
			value = typed[index]
		default:
			return nil, false
		}
	}
	return value, true
}

func resultQueryMatch(ctx context.Context, row any, predicate resultQueryPredicate) (bool, error) {
	value, present := resultQueryLookup(row, predicate.tokens)
	if predicate.op == "exists" {
		return present == predicate.value.(bool), nil
	}
	if !present {
		return false, fmt.Errorf("field %q is missing; put an exists condition before its comparison", resultUTF8Prefix(predicate.pointer, 128))
	}
	if predicate.op == "eq" || predicate.op == "ne" {
		if resultJSONType(value) != resultJSONType(predicate.value) && predicate.op == "eq" {
			return false, fmt.Errorf("eq requires matching JSON types (%s versus %s)", resultJSONType(value), resultJSONType(predicate.value))
		}
		equal, err := resultQueryEqual(ctx, value, predicate.value, 0)
		if predicate.op == "ne" {
			return !equal, err
		}
		return equal, err
	}
	if predicate.op == "contains" {
		switch typed := value.(type) {
		case string:
			needle, ok := predicate.value.(string)
			if !ok {
				return false, fmt.Errorf("string contains requires a string value")
			}
			return strings.Contains(typed, needle), nil
		case []any:
			for _, item := range typed {
				equal, err := resultQueryEqual(ctx, item, predicate.value, 0)
				if err != nil || equal {
					return equal, err
				}
			}
			return false, nil
		default:
			return false, fmt.Errorf("contains requires a saved string or array, got %s", resultJSONType(value))
		}
	}
	var comparison int
	switch typed := value.(type) {
	case json.Number:
		other, ok := predicate.value.(json.Number)
		if !ok {
			return false, fmt.Errorf("ordered numeric comparison requires a JSON number value")
		}
		left, err := resultQueryNumber(typed)
		if err != nil {
			return false, err
		}
		right, err := resultQueryNumber(other)
		if err != nil {
			return false, err
		}
		comparison = left.Cmp(right)
	case string:
		other, ok := predicate.value.(string)
		if !ok {
			return false, fmt.Errorf("ordered string comparison requires a string value")
		}
		comparison = strings.Compare(typed, other)
	default:
		return false, fmt.Errorf("ordered comparison requires numbers or strings, got %s", resultJSONType(value))
	}
	switch predicate.op {
	case "lt":
		return comparison < 0, nil
	case "lte":
		return comparison <= 0, nil
	case "gt":
		return comparison > 0, nil
	default: // gte was validated before the scan.
		return comparison >= 0, nil
	}
}

func resultQueryEqual(ctx context.Context, left, right any, depth int) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if depth > resultQueryMaxDepth {
		return false, fmt.Errorf("comparison exceeds %d nested levels", resultQueryMaxDepth)
	}
	if resultJSONType(left) != resultJSONType(right) {
		return false, nil
	}
	switch typed := left.(type) {
	case nil:
		return true, nil
	case bool:
		return typed == right.(bool), nil
	case string:
		return typed == right.(string), nil
	case json.Number:
		a, err := resultQueryNumber(typed)
		if err != nil {
			return false, err
		}
		b, err := resultQueryNumber(right.(json.Number))
		if err != nil {
			return false, err
		}
		return a.Cmp(b) == 0, nil
	case []any:
		other := right.([]any)
		if len(typed) != len(other) {
			return false, nil
		}
		for index, item := range typed {
			equal, err := resultQueryEqual(ctx, item, other[index], depth+1)
			if err != nil || !equal {
				return equal, err
			}
		}
		return true, nil
	case map[string]any:
		other := right.(map[string]any)
		if len(typed) != len(other) {
			return false, nil
		}
		for _, key := range resultJSONKeys(typed) {
			item := typed[key]
			otherItem, present := other[key]
			if !present {
				return false, nil
			}
			equal, err := resultQueryEqual(ctx, item, otherItem, depth+1)
			if err != nil || !equal {
				return equal, err
			}
		}
		return true, nil
	default:
		return false, fmt.Errorf("unsupported saved JSON value")
	}
}

func resultQueryValidateValue(ctx context.Context, value any, depth int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if depth > resultQueryMaxDepth {
		return fmt.Errorf("value exceeds %d nested levels", resultQueryMaxDepth)
	}
	switch typed := value.(type) {
	case json.Number:
		_, err := resultQueryNumber(typed)
		return err
	case []any:
		for _, item := range typed {
			if err := resultQueryValidateValue(ctx, item, depth+1); err != nil {
				return err
			}
		}
	case map[string]any:
		for _, item := range typed {
			if err := resultQueryValidateValue(ctx, item, depth+1); err != nil {
				return err
			}
		}
	}
	return nil
}

// JSON numbers are finite decimal rationals. Bound their syntax before asking
// math/big to allocate powers of ten; never round through a float64.
func resultQueryNumber(number json.Number) (*big.Rat, error) {
	text := number.String()
	if len(text) > resultQueryMaxNumberDigits+16 {
		return nil, fmt.Errorf("numeric value exceeds the query precision limit of %d mantissa digits", resultQueryMaxNumberDigits)
	}
	mantissa := text
	if exponentIndex := strings.IndexAny(text, "eE"); exponentIndex >= 0 {
		mantissa = text[:exponentIndex]
		exponent, err := strconv.Atoi(text[exponentIndex+1:])
		if err != nil || exponent < -resultQueryMaxExponent || exponent > resultQueryMaxExponent {
			return nil, fmt.Errorf("numeric exponent magnitude exceeds query limit %d", resultQueryMaxExponent)
		}
	}
	digits := 0
	for _, character := range mantissa {
		if character >= '0' && character <= '9' {
			digits++
		}
	}
	if digits > resultQueryMaxNumberDigits {
		return nil, fmt.Errorf("numeric value exceeds the query precision limit of %d mantissa digits", resultQueryMaxNumberDigits)
	}
	value, ok := new(big.Rat).SetString(text)
	if !ok {
		return nil, fmt.Errorf("saved value is not a supported exact JSON number")
	}
	return value, nil
}

func resultQueryRow(row any, index int, projections []resultQueryProjection) map[string]any {
	item := map[string]any{"original_index": index, "pointer_suffix": "/" + strconv.Itoa(index)}
	if len(projections) == 0 {
		item["value"] = row
		return item
	}
	fields := make(map[string]any, len(projections))
	for _, projection := range projections {
		value, present := resultQueryLookup(row, projection.tokens)
		field := map[string]any{"present": present}
		if present {
			field["value"] = value
		}
		fields[projection.pointer] = field
	}
	item["fields"] = fields
	return item
}

func (stats *resultQueryStatistics) add(row any, tokens []string) error {
	value, present := resultQueryLookup(row, tokens)
	if !present {
		stats.missing++
		return nil
	}
	if value == nil {
		stats.null++
		return nil
	}
	number, ok := value.(json.Number)
	if !ok {
		stats.nonNumeric++
		return nil
	}
	numeric, err := resultQueryNumber(number)
	if err != nil {
		return err
	}
	stats.numeric++
	if stats.sum == nil {
		stats.sum = new(big.Rat).Set(numeric)
		stats.minimum = new(big.Rat).Set(numeric)
		stats.maximum = new(big.Rat).Set(numeric)
	} else {
		stats.sum.Add(stats.sum, numeric)
		if numeric.Cmp(stats.minimum) < 0 {
			stats.minimum.Set(numeric)
		}
		if numeric.Cmp(stats.maximum) > 0 {
			stats.maximum.Set(numeric)
		}
	}
	return nil
}

func (stats *resultQueryStatistics) output(aggregate resultQueryAggregate) map[string]any {
	output := map[string]any{
		"op": aggregate.Op, "pointer": aggregate.Pointer, "matched_rows": stats.matched,
		"scope":    "all matching rows in the selected saved array; independent of page offset/limit",
		"encoding": "exact rational string: integer or numerator/denominator; null if no numeric values",
		"value":    nil,
	}
	if aggregate.Op == "count" {
		output["value"] = strconv.Itoa(stats.matched)
		output["encoding"] = "exact integer string; count is matching records, not non-null fields"
		return output
	}
	output["numeric_count"], output["missing_count"], output["null_count"], output["non_numeric_count"] = stats.numeric, stats.missing, stats.null, stats.nonNumeric
	if stats.numeric == 0 {
		return output
	}
	switch aggregate.Op {
	case "sum":
		output["value"] = stats.sum.RatString()
	case "min":
		output["value"] = stats.minimum.RatString()
	case "max":
		output["value"] = stats.maximum.RatString()
	case "avg":
		average := new(big.Rat).Quo(stats.sum, new(big.Rat).SetInt64(int64(stats.numeric)))
		output["value"] = average.RatString()
		output["sum"] = stats.sum.RatString()
	}
	return output
}
