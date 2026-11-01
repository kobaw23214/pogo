package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"

	"github.com/yosukeno/pogo/agent"
	"github.com/yosukeno/pogo/tools"
)

const groupedTaskPrompt = `请分析 get_records 返回并保存的 rows，仅筛选 status="active"。
1. 按 region 分组，计算各组记录数和 amount_cents 总和；按总和降序、相同总和时 region 升序，返回前3组。
2. 在同一批 active 记录中，按 amount_cents 降序、相同金额时 id 升序，返回前5条的 id 和 amount_cents。
使用 tool_result 的 group_by、aggregate、sort 和 select 完成精确计算；每次 query 的 limit 不超过5，需要时沿 next_offset 续页。统计和排序必须扫描完整保存数组，不能从预览样本计算。只获取一次原数据，不要读取夹具文件。
最终仅返回严格JSON（可代码围栏，整数不用引号，不增加字段）：
{"top_regions":[{"region":"区域","matched_count":整数,"amount_cents_sum":整数}],"top_records":[{"id":整数,"amount_cents":整数}],"scope":"saved_observation","sources":["[1](#cite:Sx)"]}
scope表示仅依据本次保存观察，不代表远端全量；来源引用应覆盖实际使用的分组统计与排序行结果。`

type groupedRegion struct {
	Region string `json:"region"`
	Count  int64  `json:"matched_count"`
	Sum    int64  `json:"amount_cents_sum"`
}

type groupedAnswer struct {
	Regions []groupedRegion `json:"top_regions"`
	Records []pair          `json:"top_records"`
	Scope   string          `json:"scope"`
	Sources []string        `json:"sources"`
}

type groupedOracle struct {
	Matched    int64           `json:"matched_count"`
	GroupCount int             `json:"group_count"`
	Regions    []groupedRegion `json:"top_regions"`
	Records    []pair          `json:"top_records"`
	Scope      string          `json:"scope"`
}

type groupedSort struct {
	Pointer   string `json:"pointer"`
	Direction string `json:"direction"`
}

var groupedRegionSort = []groupedSort{{"/aggregate/value", "desc"}, {"/keys/0/value", "asc"}}
var groupedRecordSort = []groupedSort{{"/amount_cents", "desc"}, {"/id", "asc"}}

type groupedQueryArgs struct {
	ResultID string `json:"result_id"`
	View     string `json:"view"`
	Pointer  string `json:"pointer"`
	Offset   int    `json:"offset"`
	Limit    *int   `json:"limit"`
	Where    []struct {
		Pointer string          `json:"pointer"`
		Op      string          `json:"op"`
		Value   json.RawMessage `json:"value"`
	} `json:"where"`
	Select    []string      `json:"select"`
	GroupBy   []string      `json:"group_by"`
	Sort      []groupedSort `json:"sort"`
	Aggregate *struct {
		Op      string `json:"op"`
		Pointer string `json:"pointer"`
	} `json:"aggregate"`
	IncludeRows *bool `json:"include_rows"`
}

type groupedQueryPage struct {
	View               string            `json:"view"`
	ResultID           string            `json:"result_id"`
	Pointer            string            `json:"pointer"`
	Offset             *int              `json:"offset"`
	NextOffset         *int              `json:"next_offset"`
	OriginalIsError    *bool             `json:"original_is_error"`
	StoredTextComplete *bool             `json:"stored_text_complete"`
	ScanComplete       bool              `json:"scan_complete"`
	Scanned            int               `json:"scanned_rows"`
	StoredCount        int               `json:"stored_array_item_count"`
	Matched            int64             `json:"matched_rows"`
	Groups             bool              `json:"groups_included"`
	GroupsDisplayed    *bool             `json:"groups_displayed"`
	Rows               bool              `json:"rows_included"`
	PageDisplayed      bool              `json:"page_displayed"`
	TotalGroups        int               `json:"total_groups"`
	GroupBy            []string          `json:"group_by"`
	Sort               []groupedSort     `json:"sort"`
	Items              []json.RawMessage `json:"items"`
	Shown              int               `json:"shown_items"`
	Omitted            bool              `json:"omitted"`
	BudgetOmitted      bool              `json:"model_budget_omitted"`
	Complete           bool              `json:"complete"`
}

type groupedEvidence struct {
	OriginalResultID     string   `json:"original_result_id"`
	InitialReferenceOnly bool     `json:"initial_reference_only"`
	InitialViews         int      `json:"initial_actual_views"`
	InitialSourceLabels  []string `json:"initial_source_labels"`
	GroupRanks           []int    `json:"delivered_group_ranks"`
	RecordRanks          []int    `json:"delivered_record_ranks"`
	GroupSourceLabels    []string `json:"group_source_labels"`
	RecordSourceLabels   []string `json:"record_source_labels"`
	SuccessfulRequests   int      `json:"successful_actual_requests"`
	RejectedViews        int      `json:"rejected_actual_views"`
	Groups               bool     `json:"top_three_groups_evidence"`
	Records              bool     `json:"top_five_projection_evidence"`
	CitationsSupported   bool     `json:"citations_bound_to_actual_views"`
	Passed               bool     `json:"passed"`
}

func groupedExpected(f fixture) groupedOracle {
	out := groupedOracle{Scope: "saved_observation", Regions: []groupedRegion{}, Records: []pair{}}
	groups := map[string]groupedRegion{}
	for _, row := range f.Rows {
		if row.Status != "active" {
			continue
		}
		out.Matched++
		g := groups[row.Region]
		g.Region = row.Region
		g.Count++
		g.Sum += row.AmountCents
		groups[row.Region] = g
		out.Records = append(out.Records, pair{row.ID, row.AmountCents})
	}
	for _, group := range groups {
		out.Regions = append(out.Regions, group)
	}
	sort.Slice(out.Regions, func(i, j int) bool {
		if out.Regions[i].Sum != out.Regions[j].Sum {
			return out.Regions[i].Sum > out.Regions[j].Sum
		}
		return out.Regions[i].Region < out.Regions[j].Region
	})
	out.GroupCount = len(out.Regions)
	sort.Slice(out.Records, func(i, j int) bool {
		if out.Records[i].AmountCents != out.Records[j].AmountCents {
			return out.Records[i].AmountCents > out.Records[j].AmountCents
		}
		return out.Records[i].ID < out.Records[j].ID
	})
	out.Regions = out.Regions[:min(3, len(out.Regions))]
	out.Records = out.Records[:min(5, len(out.Records))]
	return out
}

// Retain only this synthetic scenario's bounded actual model-request JSON.
// Its hash is checked again before a page is allowed to support acceptance.
func groupedMeasuredBody(text string) json.RawMessage {
	body := agent.StripCiteHeader(text)
	if len(body) > 40*1024 || !json.Valid([]byte(body)) {
		return nil
	}
	return append(json.RawMessage(nil), body...)
}

// encoding/json accepts duplicate keys and case-insensitive struct keys.
// This scenario's strict contract must not let a later value hide a bad scope.
func groupedUniqueKeys(raw []byte) bool {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if semanticJSONUniqueValue(decoder, 0) != nil {
		return false
	}
	_, err := decoder.Token()
	return err == io.EOF
}

func groupedExactFields(raw json.RawMessage, names ...string) bool {
	var fields map[string]json.RawMessage
	if !groupedDecode(raw, &fields, false) || len(fields) != len(names) {
		return false
	}
	for _, name := range names {
		if _, exists := fields[name]; !exists {
			return false
		}
	}
	return true
}

func groupedDecode(raw []byte, dst any, strict bool) bool {
	if !groupedUniqueKeys(raw) {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if strict {
		decoder.DisallowUnknownFields()
	}
	if decoder.Decode(dst) != nil {
		return false
	}
	var trailing any
	return decoder.Decode(&trailing) == io.EOF
}

func groupedParseAnswer(text string) (groupedAnswer, error) {
	var out groupedAnswer
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "```") {
		first := strings.IndexByte(text, '\n')
		if first < 0 || !strings.HasSuffix(text, "```") {
			return out, fmt.Errorf("invalid JSON fence")
		}
		text = strings.TrimSpace(text[first+1 : len(text)-3])
	}
	if !groupedDecode([]byte(text), &out, true) {
		return out, fmt.Errorf("answer must be one strict grouped JSON object")
	}
	var fields map[string]json.RawMessage
	if !groupedDecode([]byte(text), &fields, false) || !groupedExactFields(json.RawMessage(text), "top_regions", "top_records", "scope", "sources") || len(out.Regions) != 3 || len(out.Records) != 5 || len(out.Sources) == 0 {
		return out, fmt.Errorf("answer fields/cardinality are incomplete")
	}
	for name, required := range map[string][]string{"top_regions": {"region", "matched_count", "amount_cents_sum"}, "top_records": {"id", "amount_cents"}} {
		var items []json.RawMessage
		if !groupedDecode(fields[name], &items, false) {
			return out, fmt.Errorf("invalid answer rows")
		}
		for _, item := range items {
			if !groupedExactFields(item, required...) {
				return out, fmt.Errorf("answer row fields must match exactly")
			}
		}
	}
	return out, nil
}

func groupedInitialReference(raw json.RawMessage, id string) bool {
	var value map[string]any
	if !groupedDecode(raw, &value, false) || value["result_id"] != id || value["delivery_mode"] != "reference" || value["preview_only"] != true || value["is_error"] != false || value["original_is_error"] != false || value["stored_text_complete"] != true {
		return false
	}
	allowed := map[string]bool{}
	for _, name := range []string{"result_id", "is_error", "original_is_error", "preview_only", "stored_text_complete", "delivery_mode", "kind", "coverage", "read_more", "original_tool", "text_bytes", "structured_bytes", "native_bytes", "json_directory", "reader_usage", "observation_only"} {
		allowed[name] = true
	}
	for key := range value {
		if !allowed[key] {
			return false
		}
	}
	// The real reference builder supplies metadata directories only. Reject
	// any recursively injected source value/sample, including within metadata.
	var metadata func(any) bool
	metadata = func(v any) bool {
		switch x := v.(type) {
		case map[string]any:
			for key, child := range x {
				switch key {
				case "value", "items", "items_sample", "rows", "text_preview", "text_fragment", "observation_shape_and_samples":
					return false
				}
				if !metadata(child) {
					return false
				}
			}
		case []any:
			for _, child := range x {
				if !metadata(child) {
					return false
				}
			}
		}
		return true
	}
	return metadata(value)
}

func groupedFilter(args groupedQueryArgs) bool {
	active := false
	for _, condition := range args.Where {
		if condition.Pointer != "/status" {
			return false
		}
		if condition.Op == "exists" && (len(condition.Value) == 0 || bytes.Equal(bytes.TrimSpace(condition.Value), []byte("true"))) {
			continue
		}
		var value string
		if condition.Op != "eq" || json.Unmarshal(condition.Value, &value) != nil || value != "active" {
			return false
		}
		active = true
	}
	return active
}

func groupedPageScope(page groupedQueryPage, args groupedQueryArgs, id string, f fixture, want groupedOracle) bool {
	if page.View != "query" || page.ResultID != id || page.Pointer != "/rows" || args.ResultID != id || args.View != "query" || args.Pointer != "/rows" || !groupedFilter(args) || args.Offset < 0 || args.Limit == nil || *args.Limit < 1 || *args.Limit > 5 ||
		page.Offset == nil || *page.Offset != args.Offset || !page.ScanComplete || page.Scanned != len(f.Rows) || page.StoredCount != len(f.Rows) || page.Matched != want.Matched || page.OriginalIsError == nil || *page.OriginalIsError || page.StoredTextComplete == nil || !*page.StoredTextComplete ||
		!page.PageDisplayed || page.Omitted || page.BudgetOmitted || len(page.Items) == 0 || len(page.Items) > 5 || len(page.Items) > *args.Limit || page.Shown != len(page.Items) || !reflect.DeepEqual(page.Sort, args.Sort) {
		return false
	}
	total := int(want.Matched)
	if len(args.GroupBy) > 0 {
		total = want.GroupCount
	}
	end := args.Offset + len(page.Items)
	if end > total {
		return false
	}
	if page.NextOffset != nil {
		return *page.NextOffset == end && end < total && !page.Complete
	}
	return end == total && page.Complete
}

func groupedValidateGroup(raw json.RawMessage, rank int, want groupedOracle) bool {
	var item struct {
		Index    *int   `json:"group_index"`
		Original *int   `json:"original_index"`
		Pointer  string `json:"pointer_suffix"`
		Count    int64  `json:"count"`
		Keys     []struct {
			Pointer string `json:"pointer"`
			Present bool   `json:"present"`
			Value   string `json:"value"`
		} `json:"keys"`
		Aggregate struct {
			Op      string `json:"op"`
			Pointer string `json:"pointer"`
			Matched int64  `json:"matched_rows"`
			Numeric int64  `json:"numeric_count"`
			Missing int64  `json:"missing_count"`
			Null    int64  `json:"null_count"`
			Other   int64  `json:"non_numeric_count"`
			Value   string `json:"value"`
			Scope   string `json:"scope"`
		} `json:"aggregate"`
	}
	if !groupedDecode(raw, &item, false) || rank < 0 || rank >= len(want.Regions) {
		return false
	}
	g := want.Regions[rank]
	return item.Index != nil && *item.Index == rank && item.Original == nil && item.Pointer == "" && len(item.Keys) == 1 && item.Keys[0].Pointer == "/region" && item.Keys[0].Present && item.Keys[0].Value == g.Region && item.Count == g.Count &&
		item.Aggregate.Op == "sum" && item.Aggregate.Pointer == "/amount_cents" && item.Aggregate.Matched == g.Count && item.Aggregate.Numeric == g.Count && item.Aggregate.Missing == 0 && item.Aggregate.Null == 0 && item.Aggregate.Other == 0 && item.Aggregate.Scope != "" && exactRationalInteger(item.Aggregate.Value, g.Sum)
}

func groupedValidateRecord(raw json.RawMessage, rank int, f fixture, want groupedOracle) bool {
	var item struct {
		Index      *int   `json:"original_index"`
		Pointer    string `json:"pointer_suffix"`
		GroupIndex *int   `json:"group_index"`
		Omitted    bool   `json:"omitted_value"`
		Fields     map[string]struct {
			Present bool            `json:"present"`
			Value   json.RawMessage `json:"value"`
		} `json:"fields"`
	}
	if !groupedDecode(raw, &item, false) || rank < 0 || rank >= len(want.Records) {
		return false
	}
	row := want.Records[rank]
	index := row.ID - 1
	if index < 0 || index >= len(f.Rows) {
		return false
	}
	id, amount := item.Fields["/id"], item.Fields["/amount_cents"]
	return item.Index != nil && *item.Index == index && item.Pointer == fmt.Sprintf("/%d", index) && item.GroupIndex == nil && !item.Omitted && id.Present && amount.Present && exactJSONInteger(id.Value, int64(row.ID)) && exactJSONInteger(amount.Value, row.AmountCents)
}

func groupedNativeRead(call toolCallReport, args groupedQueryArgs, id string, want groupedOracle) bool {
	raw, err := json.Marshal(call.ReadDetails)
	var details tools.ToolResultReadDetails
	if err != nil || !groupedDecode(raw, &details, false) {
		return false
	}
	total := int(want.Matched)
	if len(args.GroupBy) > 0 {
		total = want.GroupCount
	}
	return details.ResultID == id && details.OriginalTool == "mcp_eval_get_records" && details.View == "query" && details.Pointer == "/rows" && details.Offset == args.Offset && details.Total == total && details.ScanComplete && details.TextComplete && !details.OriginalIsError && !details.Omitted
}

func groupedSourceHeader(view toolView, call toolCallReport) bool {
	return call.SourceLabel != "" && strings.HasPrefix(view.SourceHeader, "[cite:"+call.SourceLabel+" | ")
}

func groupedCheckEvidence(calls []toolCallReport, requests []requestReport, id string, f fixture, sources []string) groupedEvidence {
	trace := groupedEvidence{OriginalResultID: id, InitialReferenceOnly: true, InitialSourceLabels: []string{}, GroupRanks: []int{}, RecordRanks: []int{}, GroupSourceLabels: []string{}, RecordSourceLabels: []string{}}
	want := groupedExpected(f)
	byID := map[string]toolCallReport{}
	for _, call := range calls {
		byID[call.ID] = call
	}
	groups, records := map[int]bool{}, map[int]bool{}
	gLabels, rLabels, iLabels := map[string]bool{}, map[string]bool{}, map[string]bool{}
	seen := map[string]bool{}
	for _, request := range requests {
		// Failed/aborted requests cannot claim delivery or reserve a dedup key.
		if request.ErrorClass != "" || request.StopReason == "" || request.StopReason == "aborted" {
			continue
		}
		trace.SuccessfulRequests++
		for _, view := range request.ToolViews {
			call, known := byID[view.CallID]
			if !known || call.IsError {
				continue
			}
			key := view.CallID + ":" + view.BodySHA256
			if seen[key] {
				continue
			}
			seen[key] = true
			if len(view.GroupedBody) == 0 || digest(view.GroupedBody) != view.BodySHA256 || !groupedSourceHeader(view, call) {
				if call.Name == "mcp_eval_get_records" {
					trace.InitialViews++
					trace.InitialReferenceOnly = false
				}
				trace.RejectedViews++
				continue
			}
			if call.Name == "mcp_eval_get_records" {
				trace.InitialViews++
				valid := groupedInitialReference(view.GroupedBody, id)
				trace.InitialReferenceOnly = trace.InitialReferenceOnly && valid
				if valid {
					iLabels[call.SourceLabel] = true
				} else {
					trace.RejectedViews++
				}
				continue
			}
			if call.Name != tools.ToolResultReadName {
				continue
			}
			var args groupedQueryArgs
			var page groupedQueryPage
			if !groupedDecode(call.Args, &args, false) || !groupedDecode(view.GroupedBody, &page, false) || !groupedPageScope(page, args, id, f, want) {
				trace.RejectedViews++
				continue
			}
			if !groupedNativeRead(call, args, id, want) {
				trace.RejectedViews++
				continue
			}
			if len(args.GroupBy) > 0 {
				if len(args.Select) > 0 || args.IncludeRows != nil && *args.IncludeRows || !reflect.DeepEqual(args.GroupBy, []string{"/region"}) || args.Aggregate == nil || args.Aggregate.Op != "sum" || args.Aggregate.Pointer != "/amount_cents" || !reflect.DeepEqual(args.Sort, groupedRegionSort) || !page.Groups || page.Rows || page.GroupsDisplayed != nil && !*page.GroupsDisplayed || page.TotalGroups != want.GroupCount || !reflect.DeepEqual(page.GroupBy, args.GroupBy) {
					trace.RejectedViews++
					continue
				}
				for position, item := range page.Items {
					rank := args.Offset + position
					if rank < len(want.Regions) && groupedValidateGroup(item, rank, want) {
						groups[rank] = true
						gLabels[call.SourceLabel] = true
					}
				}
			} else {
				if args.Aggregate != nil || args.IncludeRows != nil && !*args.IncludeRows || !reflect.DeepEqual(args.Sort, groupedRecordSort) || page.Groups || !page.Rows || !sameGroupedProjection(args.Select) {
					trace.RejectedViews++
					continue
				}
				for position, item := range page.Items {
					rank := args.Offset + position
					if rank < len(want.Records) && groupedValidateRecord(item, rank, f, want) {
						records[rank] = true
						rLabels[call.SourceLabel] = true
					}
				}
			}
		}
	}
	for rank := range groups {
		trace.GroupRanks = append(trace.GroupRanks, rank)
	}
	sort.Ints(trace.GroupRanks)
	for rank := range records {
		trace.RecordRanks = append(trace.RecordRanks, rank)
	}
	sort.Ints(trace.RecordRanks)
	for label := range gLabels {
		trace.GroupSourceLabels = append(trace.GroupSourceLabels, label)
	}
	sort.Strings(trace.GroupSourceLabels)
	for label := range rLabels {
		trace.RecordSourceLabels = append(trace.RecordSourceLabels, label)
	}
	sort.Strings(trace.RecordSourceLabels)
	for label := range iLabels {
		trace.InitialSourceLabels = append(trace.InitialSourceLabels, label)
	}
	sort.Strings(trace.InitialSourceLabels)
	trace.Groups = len(groups) == 3
	trace.Records = len(records) == 5
	trace.InitialReferenceOnly = trace.InitialReferenceOnly && trace.InitialViews > 0 && len(iLabels) > 0
	allKnown, hasGroup, hasRecords := len(sources) > 0, false, false
	for _, citation := range sources {
		match := anchorPattern.FindStringSubmatch(citation)
		if len(match) != 3 || match[1] != "1" {
			allKnown = false
			continue
		}
		label := match[2]
		// A separately validated original reference may remain as provenance,
		// but it never satisfies either computed-group or sorted-row support.
		allKnown = allKnown && (gLabels[label] || rLabels[label] || iLabels[label])
		hasGroup = hasGroup || gLabels[label]
		hasRecords = hasRecords || rLabels[label]
	}
	trace.CitationsSupported = allKnown && hasGroup && hasRecords
	trace.Passed = sourceResultIDForEval(id) && trace.InitialReferenceOnly && trace.Groups && trace.Records && trace.CitationsSupported
	return trace
}

func sourceResultIDForEval(id string) bool {
	return len(id) == 65 && resultIDPattern.FindString(id) == id
}
func sameGroupedProjection(fields []string) bool {
	if len(fields) != 2 {
		return false
	}
	return fields[0] == "/id" && fields[1] == "/amount_cents" || fields[1] == "/id" && fields[0] == "/amount_cents"
}

func (scenario evalScenario) finishGrouped(report *runReport, id string, labels map[string]bool) {
	parsed, err := groupedParseAnswer(report.FinalAnswer)
	want := groupedExpected(scenario.records)
	if err == nil {
		report.GroupedAnswer = &parsed
		report.FinalFormatCorrect, report.FactChecksRun = true, true
		report.NumericCorrect = reflect.DeepEqual(parsed.Regions, want.Regions) && reflect.DeepEqual(parsed.Records, want.Records)
		report.CoverageCorrect = parsed.Scope == want.Scope
		report.CitationLabelsKnown, report.CitationFormatCorrect = citationChecks(parsed.Sources, labels)
		report.CitationsKnown = report.CitationLabelsKnown && report.CitationFormatCorrect
	}
	trace := groupedCheckEvidence(report.ToolCalls, report.Requests, id, scenario.records, parsed.Sources)
	report.GroupedEvidence = &trace
	report.Passed = report.EndReason == string(agent.EndCompleted) && report.ErrorClass == "" && report.FinalFormatCorrect && report.NumericCorrect && report.CoverageCorrect && report.CitationsKnown && trace.Passed && report.MCPCalls == 1 && report.FileToolCalls == 0 && report.Recovery.Complete && report.Recovery.MatchesSource
}
