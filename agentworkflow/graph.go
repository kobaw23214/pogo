package agentworkflow

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

const GraphInput = "__input"
const GraphOutput = "__output"

// Graph is opt-in. A nil graph retains the original ordered-step contract.
type Graph struct {
	Version int    `json:"version"`
	Edges   []Edge `json:"edges"`
}
type Edge struct {
	Source string `json:"source"`
	Target string `json:"target"`
	Branch string `json:"branch,omitempty"`
}
type Condition struct {
	Source   string `json:"source"`
	Operator string `json:"operator"`
	Value    string `json:"value,omitempty"`
}
type GraphAnalysis struct {
	Order      []string
	Incoming   map[string][]Edge
	Outgoing   map[string][]Edge
	Dominators map[string]map[string]bool
	// Guaranteed contains ancestors that are active whenever this node is
	// active. Unlike a path dominator, it includes independently fanned-out
	// work that a fan-in must wait for, but excludes optional condition arms.
	Guaranteed map[string]map[string]bool
}

// AnalyzeGraph validates graph structure without reading or mutating any Run.
// Order contains real steps only, in a stable topological order. Dominators
// include the node itself; callers must exclude self-references.
func AnalyzeGraph(d Definition) (*GraphAnalysis, []Diagnostic) {
	a := &GraphAnalysis{Incoming: map[string][]Edge{}, Outgoing: map[string][]Edge{}, Dominators: map[string]map[string]bool{}, Guaranteed: map[string]map[string]bool{}}
	diagnostics := []Diagnostic{}
	add := func(code, id, field, text string) {
		diagnostics = append(diagnostics, Diagnostic{Code: code, Severity: "error", StepID: id, FieldPath: field, Message: text})
	}
	if d.Graph == nil || d.Graph.Version != 1 {
		add("invalid_graph_version", "", "graph.version", "图流程仅支持version=1")
		return a, diagnostics
	}
	if len(d.Steps) < 1 || len(d.Steps) > MaxSteps || len(d.Graph.Edges) < 2 || len(d.Graph.Edges) > MaxSteps*3 {
		add("invalid_graph_size", "", "graph.edges", fmt.Sprintf("图流程需要1–%d个节点及数量受限的连接", MaxSteps))
		return a, diagnostics
	}
	nodes := map[string]Step{GraphInput: {ID: GraphInput, Kind: "input"}, GraphOutput: {ID: GraphOutput, Kind: "output"}}
	ids := []string{GraphInput}
	for _, step := range d.Steps {
		if _, exists := nodes[step.ID]; exists || !ValidID(step.ID) {
			add("invalid_step_id", step.ID, "steps", "节点ID必须唯一且为有效稳定标识符")
			continue
		}
		nodes[step.ID] = step
		ids = append(ids, step.ID)
	}
	ids = append(ids, GraphOutput)
	seen := map[string]bool{}
	for i, edge := range d.Graph.Edges {
		field := fmt.Sprintf("graph.edges.%d", i)
		source, sourceOK := nodes[edge.Source]
		_, targetOK := nodes[edge.Target]
		if !sourceOK || !targetOK || edge.Source == edge.Target || edge.Target == GraphInput || edge.Source == GraphOutput {
			add("invalid_edge", edge.Source, field, "连接端点必须存在，不能自连或反向连接流程边界")
			continue
		}
		if source.Kind == "condition" {
			if edge.Branch != "true" && edge.Branch != "false" {
				add("invalid_branch", source.ID, field, "条件连接必须标明true或false")
			}
		} else if edge.Branch != "" {
			add("unexpected_branch", source.ID, field, "只有条件节点的连接可以带分支标签")
		}
		key := edge.Source + "\x00" + edge.Target + "\x00" + edge.Branch
		if seen[key] {
			add("duplicate_edge", edge.Source, field, "连接不能重复")
			continue
		}
		seen[key] = true
		a.Incoming[edge.Target] = append(a.Incoming[edge.Target], edge)
		a.Outgoing[edge.Source] = append(a.Outgoing[edge.Source], edge)
	}
	for _, id := range ids {
		step := nodes[id]
		in, out := len(a.Incoming[id]), len(a.Outgoing[id])
		valid := false
		switch step.Kind {
		case "input":
			valid = in == 0 && out >= 1
		case "output":
			valid = in >= 1 && out == 0
		case "agent", "confirm", "code", "iteration", "loop":
			valid = in >= 1 && out >= 1
		case "condition":
			valid = in >= 1 && out >= 2
			branches := map[string]int{}
			for _, e := range a.Outgoing[id] {
				branches[e.Branch]++
			}
			valid = valid && branches["true"] >= 1 && branches["false"] >= 1
		case "parallel":
			valid = in >= 1 && out >= 2
		case "join":
			valid = in >= 2 && out >= 1
		}
		if !valid {
			add("invalid_node_degree", id, "graph.edges", "普通节点至少一入一出；开始至少一出口，输出至少一入口；条件true/false分支各至少一出口，并行至少两出口，汇聚至少两入口")
		}
	}
	indegree := map[string]int{}
	for _, id := range ids {
		indegree[id] = len(a.Incoming[id])
	}
	queue := []string{}
	for _, id := range ids {
		if indegree[id] == 0 {
			queue = append(queue, id)
		}
	}
	allOrder := []string{}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		allOrder = append(allOrder, id)
		for _, edge := range a.Outgoing[id] {
			indegree[edge.Target]--
			if indegree[edge.Target] == 0 {
				queue = append(queue, edge.Target)
			}
		}
	}
	if len(allOrder) != len(ids) {
		add("graph_cycle", "", "graph.edges", "图流程不允许循环")
	}
	reachable := func(start string, reverse bool) map[string]bool {
		found := map[string]bool{start: true}
		pending := []string{start}
		for len(pending) > 0 {
			id := pending[0]
			pending = pending[1:]
			edges := a.Outgoing[id]
			if reverse {
				edges = a.Incoming[id]
			}
			for _, edge := range edges {
				next := edge.Target
				if reverse {
					next = edge.Source
				}
				if !found[next] {
					found[next] = true
					pending = append(pending, next)
				}
			}
		}
		return found
	}
	fromInput, toOutput := reachable(GraphInput, false), reachable(GraphOutput, true)
	for _, id := range ids {
		if !fromInput[id] || !toOutput[id] {
			add("unreachable_node", id, "graph.edges", "每个节点必须能从开始到达，并能连接到输出")
		}
	}
	if len(diagnostics) > 0 {
		return a, diagnostics
	}
	for _, id := range allOrder {
		if id != GraphInput && id != GraphOutput {
			a.Order = append(a.Order, id)
		}
		common := map[string]bool{}
		for i, edge := range a.Incoming[id] {
			predecessors := a.Dominators[edge.Source]
			if i == 0 {
				for key := range predecessors {
					common[key] = true
				}
			} else {
				for key := range common {
					if !predecessors[key] {
						delete(common, key)
					}
				}
			}
		}
		common[id] = true
		a.Dominators[id] = common
	}
	// A target executes only after all its incoming paths have resolved. Values
	// from an unconditional sibling are therefore safe, even though that sibling
	// is not a classic path dominator. Condition arms remain optional unless every
	// activating path to the target requires the same branch decision.
	branchFacts := map[string]map[string]string{}
	for _, id := range allOrder {
		common := map[string]string{}
		for i, edge := range a.Incoming[id] {
			facts := map[string]string{}
			for key, value := range branchFacts[edge.Source] {
				facts[key] = value
			}
			if nodes[edge.Source].Kind == "condition" {
				facts[edge.Source] = edge.Branch
			}
			if i == 0 {
				common = facts
			} else {
				for key, value := range common {
					if facts[key] != value {
						delete(common, key)
					}
				}
			}
		}
		branchFacts[id] = common
	}
	for _, target := range allOrder {
		ancestors := map[string]bool{target: true}
		pending := []string{target}
		for len(pending) > 0 {
			id := pending[0]
			pending = pending[1:]
			for _, edge := range a.Incoming[id] {
				if !ancestors[edge.Source] {
					ancestors[edge.Source] = true
					pending = append(pending, edge.Source)
				}
			}
		}
		guaranteed := map[string]bool{}
		for id := range a.Dominators[target] {
			guaranteed[id] = true
		}
		for _, id := range allOrder {
			if !guaranteed[id] {
				continue
			}
			for _, edge := range a.Outgoing[id] {
				if ancestors[edge.Target] && (nodes[id].Kind != "condition" || branchFacts[target][id] == edge.Branch) {
					guaranteed[edge.Target] = true
				}
			}
		}
		a.Guaranteed[target] = guaranteed
	}
	return a, diagnostics
}

// GuaranteedPredecessors includes completed unconditional fan-in ancestors.
// Optional condition arms are excluded; a join's actual branch-result summary
// remains available when a caller needs a value from whichever arm ran.
func GuaranteedPredecessors(d Definition, stepID string) []string {
	result := []string{}
	if d.Graph == nil {
		for _, step := range d.Steps {
			if step.ID == stepID {
				return result
			}
			result = append(result, step.ID)
		}
		return nil
	}
	a, ds := AnalyzeGraph(d)
	if len(ds) != 0 {
		return nil
	}
	for _, id := range a.Order {
		if id != stepID && a.Guaranteed[stepID][id] {
			result = append(result, id)
		}
	}
	return result
}

func validateCondition(step Step, inputs, guaranteed map[string]bool) []Diagnostic {
	ds := []Diagnostic{}
	add := func(code, field, message string) {
		ds = append(ds, Diagnostic{Code: code, Severity: "error", StepID: step.ID, FieldPath: "condition." + field, Message: message})
	}
	c := step.Condition
	if c == nil {
		add("missing_condition", "source", "条件节点需要声明数据来源与判断规则")
		return ds
	}
	parts, err := parseDataReference(c.Source)
	if err != nil {
		add("invalid_condition_source", "source", "条件来源需要输入参数或保证已完成的上游节点数据引用")
	} else if parts.input != "" && !inputs[parts.input] {
		add("unknown_input", "source", "条件引用了未声明的输入")
	} else if parts.step != "" && (parts.step == step.ID || !guaranteed[parts.step]) {
		add("unknown_step", "source", "条件只能读取保证已完成的上游节点")
	}
	if !validText(c.Value, 16384) {
		add("invalid_condition_value", "value", "条件比较值过长或包含无效字符")
	}
	switch c.Operator {
	case "equals", "not_equals", "contains", "not_contains":
	case "is_empty", "not_empty":
		if c.Value != "" {
			add("unexpected_condition_value", "value", "空值判断不接受比较值")
		}
	case "gt", "gte", "lt", "lte":
		if _, err := finiteNumber(c.Value); err != nil {
			add("invalid_condition_number", "value", "数值条件需要有限数值")
		}
	default:
		add("invalid_condition_operator", "operator", "不支持此条件判断运算符")
	}
	return ds
}

var decimalNumber = regexp.MustCompile(`^[+-]?(?:\d+(?:\.\d*)?|\.\d+)(?:[eE][+-]?\d+)?$`)

func finiteNumber(value string) (float64, error) {
	value = strings.TrimSpace(value)
	// Match the editor's decimal grammar before ParseFloat, which also accepts
	// Go-specific hexadecimal floats and underscore digit separators.
	if !decimalNumber.MatchString(value) {
		return 0, fmt.Errorf("%w: condition needs a finite decimal number", ErrInvalid)
	}
	n, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
		return 0, fmt.Errorf("%w: condition needs a finite number", ErrInvalid)
	}
	return n, nil
}

// EvaluateCondition only compares a fixed data reference with a literal.
// It does not run expressions, models, scripts, or nested template values.
func EvaluateCondition(step Step, inputs map[string]string, results []StepRun) (bool, error) {
	c := step.Condition
	if step.Kind != "condition" || c == nil {
		return false, fmt.Errorf("%w: invalid condition", ErrInvalid)
	}
	if _, err := parseDataReference(c.Source); err != nil {
		return false, fmt.Errorf("%w: invalid condition", ErrInvalid)
	}
	value, err := Render("{{ "+c.Source+" }}", inputs, results)
	if err != nil {
		return false, err
	}
	switch c.Operator {
	case "equals":
		return value == c.Value, nil
	case "not_equals":
		return value != c.Value, nil
	case "contains":
		return strings.Contains(value, c.Value), nil
	case "not_contains":
		return !strings.Contains(value, c.Value), nil
	case "is_empty":
		return strings.TrimSpace(value) == "", nil
	case "not_empty":
		return strings.TrimSpace(value) != "", nil
	case "gt", "gte", "lt", "lte":
		a, err := finiteNumber(value)
		if err != nil {
			return false, err
		}
		b, err := finiteNumber(c.Value)
		if err != nil {
			return false, err
		}
		switch c.Operator {
		case "gt":
			return a > b, nil
		case "gte":
			return a >= b, nil
		case "lt":
			return a < b, nil
		default:
			return a <= b, nil
		}
	default:
		return false, fmt.Errorf("%w: unsupported condition operator", ErrInvalid)
	}
}

type GraphPlan struct {
	Ready   []string
	Skipped []string
	Done    bool
}

// PlanGraph rebuilds the next frontier from durable step receipts. A join
// waits for every incoming path to resolve, including unselected/skipped ones.
// Failure is never treated as an inactive successful branch.
func PlanGraph(d Definition, steps []StepRun) (GraphPlan, error) {
	p := GraphPlan{Ready: []string{}, Skipped: []string{}}
	a, ds := AnalyzeGraph(d)
	if len(ds) > 0 {
		return p, fmt.Errorf("%w: %s", ErrInvalid, ds[0].Message)
	}
	states := map[string]StepRun{}
	nodes := map[string]Step{}
	for _, step := range d.Steps {
		nodes[step.ID] = step
		states[step.ID] = StepRun{StepID: step.ID, Status: Queued}
	}
	for _, result := range steps {
		if _, ok := nodes[result.StepID]; !ok {
			return p, fmt.Errorf("%w: unknown step result", ErrInvalid)
		}
		states[result.StepID] = result
		if result.Status == Failed || result.Status == Cancelled || result.Status == Interrupted {
			return p, fmt.Errorf("%w: unsuccessful graph step %s", ErrConflict, result.StepID)
		}
		if result.Status == Completed && nodes[result.StepID].Kind == "condition" && result.Branch != "true" && result.Branch != "false" {
			return p, fmt.Errorf("%w: completed condition has no decision", ErrConflict)
		}
	}
	ready := func(id string) (resolved, active bool) {
		resolved = true
		for _, edge := range a.Incoming[id] {
			if edge.Source == GraphInput {
				active = true
				continue
			}
			source := states[edge.Source]
			if source.Status != Completed && source.Status != Skipped {
				resolved = false
				continue
			}
			if source.Status == Completed && (edge.Branch == "" || edge.Branch == source.Branch) {
				active = true
			}
		}
		return
	}
	for _, id := range a.Order {
		state := states[id]
		if state.Status != Queued && state.Status != "" {
			continue
		}
		resolved, active := ready(id)
		if !resolved {
			continue
		}
		if active {
			p.Ready = append(p.Ready, id)
		} else {
			p.Skipped = append(p.Skipped, id)
			states[id] = StepRun{StepID: id, Status: Skipped}
		}
	}
	resolved, active := ready(GraphOutput)
	p.Done = resolved && active && len(p.Ready) == 0
	if p.Done {
		for _, state := range states {
			if state.Status != Completed && state.Status != Skipped {
				p.Done = false
				break
			}
		}
	}
	return p, nil
}
