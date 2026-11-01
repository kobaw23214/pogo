// Package workflow runs a defined DAG of agent nodes. A definition names
// nodes — each a Studio agent reference plus a task and conditional
// dependencies — and the Runner schedules them on a ready queue: independent
// nodes run in parallel, a node releases when one of its dependency edges
// fires, and a node whose edges all close is skipped, cascading downstream.
//
// The package is deliberately web-free: the platform layer (web/) supplies the
// NodeExecutor that actually runs a node and the EventSink that persists and
// broadcasts state, so this half stays testable without an HTTP server.
package workflow

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
)

// MaxNodes bounds one definition. A bound this size is already far past
// anything a person can read on one canvas.
const MaxNodes = 50

// MaxTaskLen bounds a node's brief, in runes.
const MaxTaskLen = 4000

// When conditions an edge: the upstream outcome that releases the dependent
// node. Empty is success — the v1 default.
const (
	WhenSuccess  = "success"
	WhenFailed   = "failed"
	WhenRejected = "rejected"
	WhenAlways   = "always"
)

func validWhen(w string) bool {
	switch w {
	case WhenSuccess, WhenFailed, WhenRejected, WhenAlways:
		return true
	}
	return false
}

// Dep is one incoming edge: depend on node, released when its outcome matches
// when. The wire form is dual, mirroring studio.go's starterEntry: a plain
// string (the v1 shape) reads as {node, success} and round-trips as a plain
// string; only an explicit non-success condition upgrades to the object
// shape, so untouched v1 data stays byte-identical.
type Dep struct {
	Node string `json:"node"`
	When string `json:"when,omitempty"`
}

func (d *Dep) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		var plain string
		if err := json.Unmarshal(b, &plain); err != nil {
			return err
		}
		d.Node, d.When = plain, WhenSuccess
		return nil
	}
	type alias Dep
	var a alias
	if err := json.Unmarshal(b, &a); err != nil {
		return err
	}
	*d = Dep(a)
	if d.When == "" {
		d.When = WhenSuccess
	}
	return nil
}

func (d Dep) MarshalJSON() ([]byte, error) {
	if d.When == "" || d.When == WhenSuccess {
		return json.Marshal(d.Node)
	}
	type alias Dep
	return json.Marshal(alias(d))
}

// when is the edge's effective condition: empty reads as success, so a Dep
// built by hand (not decoded, not normalized) schedules like its wire form.
func (d Dep) when() string {
	if d.When == "" {
		return WhenSuccess
	}
	return d.When
}

// Expect declares the one file a node is expected to leave behind in the
// run's shared workspace. Empty means the node produces text only.
type Expect struct {
	Path string `json:"path,omitempty"`
}

// FieldSpec is one form field a human node asks for. Type is "text" (the
// default) or "select" (Options required).
type FieldSpec struct {
	Key      string   `json:"key"`
	Label    string   `json:"label,omitempty"`
	Type     string   `json:"type,omitempty"`
	Options  []string `json:"options,omitempty"`
	Required bool     `json:"required,omitempty"`
}

// Candidate is one agent an orchestrator node may dispatch to, with the hint
// becoming the dispatch tool's description — it is what the model reads when
// choosing.
type Candidate struct {
	Agent string `json:"agent"`
	Hint  string `json:"hint,omitempty"`
}

// Orchestrate turns a node into a dispatcher: its agent gets one
// dispatch_to_<agent> tool per candidate on top of its narrowed set.
// MaxDispatches caps total dispatch calls (default 5, limit 20);
// MaxParallel bounds concurrent dispatches (default 1, limit 4).
type Orchestrate struct {
	Candidates    []Candidate `json:"candidates"`
	MaxDispatches int         `json:"max_dispatches,omitempty"`
	MaxParallel   int         `json:"max_parallel,omitempty"`
}

// Node kinds.
const (
	KindAgent = "agent"
	KindHuman = "human"
)

// ApprovalPolicy is optional so historical definitions keep their original
// bytes and authorization semantics. The host resolves accounts and freezes
// each new Run's assignment; the engine never treats inputs as authority.
type ApprovalPolicy struct {
	PreventSelfApproval bool     `json:"prevent_self_approval"`
	Assignment          string   `json:"assignment"`
	Approvers           []string `json:"approvers,omitempty"`
}

// Node is one step: an agent doing what, after which siblings on which
// conditions — or, kind permitting, a person reviewing a prompt. Tools and
// Model narrow the referenced agent's own definition for this step only;
// nil/absent inherits it.
type Node struct {
	ID     string  `json:"id"`
	Kind   string  `json:"kind,omitempty"`
	Agent  string  `json:"agent,omitempty"`
	Task   string  `json:"task,omitempty"`
	Expect *Expect `json:"expect,omitempty"`
	After  []Dep   `json:"after,omitempty"`
	// Tools narrows the agent's native-tool selection for this node, with the
	// same pointer semantics as the agent definition's own list.
	Tools    *[]string `json:"tools,omitempty"`
	Model    string    `json:"model,omitempty"`
	MaxTurns int       `json:"max_turns,omitempty"`
	// Approval makes an agent node's completion a draft: the node waits for a
	// person (waiting_human) and only an approve counts as success.
	Approval       bool            `json:"approval,omitempty"`
	ApprovalPolicy *ApprovalPolicy `json:"approval_policy,omitempty"`
	// Orchestrate arms the node's agent with dispatch tools.
	Orchestrate *Orchestrate `json:"orchestrate,omitempty"`
	// Human-node fields: Prompt is the rendered Markdown (same {{ }}
	// interpolation as task), Fields the form, Actions the decision buttons
	// (default [approve reject]), Timeout how long the wait may last
	// (default 72h), OnTimeout the settlement when it lapses (reject|fail,
	// default reject).
	Prompt    string      `json:"prompt,omitempty"`
	Fields    []FieldSpec `json:"fields,omitempty"`
	Actions   []string    `json:"actions,omitempty"`
	Timeout   string      `json:"timeout,omitempty"`
	OnTimeout string      `json:"on_timeout,omitempty"`
}

// EffectiveActions is a node's decision vocabulary, defaults applied.
func EffectiveActions(n Node) []string {
	if len(n.Actions) > 0 {
		return append([]string(nil), n.Actions...)
	}
	return []string{"approve", "reject"}
}

// EffectiveOnTimeout is a node's timeout settlement, default reject.
func EffectiveOnTimeout(n Node) string {
	if n.OnTimeout != "" {
		return n.OnTimeout
	}
	return "reject"
}

// Definition is the engine's view of a workflow: identity plus the node list.
// The web store carries the same fields, so a run's snapshot round-trips.
type Definition struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Revision    int    `json:"revision"`
	Nodes       []Node `json:"nodes"`
}

var nodeIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// validNodeID keeps a node id from becoming a path segment or a UI selector
// hazard. It is the session-id grammar, minus the length allowance.
func validNodeID(id string) bool {
	return id != "" && len(id) <= 64 && nodeIDPattern.MatchString(id) &&
		!strings.Contains(id, "..") && !strings.ContainsAny(id, `/\`)
}

// ValidArtifactPath confines an expected artifact to the run workspace:
// relative, canonical, no escaping.
func ValidArtifactPath(p string) bool {
	if p == "" || strings.Contains(p, `\`) || strings.HasPrefix(p, "/") {
		return false
	}
	if len(p) >= 2 && p[1] == ':' {
		return false
	}
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

// Normalize trims the free-text fields, defaults edge conditions to success
// and drops duplicate dependencies (first occurrence wins — a node that must
// react to two outcomes of one upstream uses when:always), so a stored
// definition and a just-decoded one validate identically.
func (d *Definition) Normalize() {
	d.Name = strings.TrimSpace(d.Name)
	d.Description = strings.TrimSpace(d.Description)
	for i := range d.Nodes {
		n := &d.Nodes[i]
		n.ID = strings.TrimSpace(n.ID)
		n.Agent = strings.TrimSpace(n.Agent)
		n.Task = strings.TrimSpace(n.Task)
		n.Model = strings.TrimSpace(n.Model)
		n.Prompt = strings.TrimSpace(n.Prompt)
		n.Timeout = strings.TrimSpace(n.Timeout)
		n.OnTimeout = strings.TrimSpace(n.OnTimeout)
		if n.ApprovalPolicy != nil {
			policy := *n.ApprovalPolicy
			policy.Assignment = strings.TrimSpace(policy.Assignment)
			if policy.Assignment == "" {
				policy.Assignment = "fixed"
			}
			policy.Approvers = nil
			seen := map[string]bool{}
			for _, account := range n.ApprovalPolicy.Approvers {
				account = strings.TrimSpace(account)
				if !seen[account] {
					seen[account] = true
					policy.Approvers = append(policy.Approvers, account)
				}
			}
			n.ApprovalPolicy = &policy
		}
		for j := range n.Actions {
			n.Actions[j] = strings.TrimSpace(n.Actions[j])
		}
		if n.Orchestrate != nil {
			for j := range n.Orchestrate.Candidates {
				c := &n.Orchestrate.Candidates[j]
				c.Agent = strings.TrimSpace(c.Agent)
				c.Hint = strings.TrimSpace(c.Hint)
			}
		}
		if n.Expect != nil {
			n.Expect.Path = strings.TrimSpace(n.Expect.Path)
			if n.Expect.Path == "" {
				n.Expect = nil
			}
		}
		seen := map[string]bool{}
		kept := n.After[:0]
		for _, dep := range n.After {
			dep.Node = strings.TrimSpace(dep.Node)
			if dep.When == "" {
				dep.When = WhenSuccess
			}
			if dep.Node == "" || seen[dep.Node] {
				continue
			}
			seen[dep.Node] = true
			kept = append(kept, dep)
		}
		n.After = kept
		if n.After == nil {
			n.After = []Dep{}
		}
	}
}

// Validate checks a runnable definition: non-empty, plus everything
// ValidateShape checks. Runs fail fast on an invalid definition and publish
// enforces it; a saved draft is allowed to be empty (ValidateShape only).
func (d Definition) Validate() error {
	if len(d.Nodes) == 0 {
		return errors.New("workflow has no nodes")
	}
	if err := d.ValidateShape(); err != nil {
		return err
	}
	for _, node := range d.Nodes {
		if p := node.ApprovalPolicy; p != nil && (p.Assignment == "fixed" || p.Assignment == "") && len(p.Approvers) == 0 {
			return fmt.Errorf("node %q: fixed approval policy requires at least one approver", node.ID)
		}
	}
	return nil
}

// ValidateShape checks the whole shape: node ids legal and unique, edge
// conditions and references resolvable, template references wired to real
// upstreams, the graph acyclic. References to agents are not checked here —
// those live behind the store's refs hook, which can see the live catalog.
func (d Definition) ValidateShape() error {
	if len(d.Nodes) > MaxNodes {
		return fmt.Errorf("workflow has %d nodes, the limit is %d", len(d.Nodes), MaxNodes)
	}
	ids := map[string]bool{}
	for _, n := range d.Nodes {
		if !validNodeID(n.ID) {
			return fmt.Errorf("node %q: invalid id", n.ID)
		}
		if ids[n.ID] {
			return fmt.Errorf("node %q: duplicate id", n.ID)
		}
		ids[n.ID] = true
		kind := n.Kind
		if kind == "" {
			kind = KindAgent
		}
		if kind != KindAgent && kind != KindHuman {
			return fmt.Errorf("node %q: invalid kind %q", n.ID, n.Kind)
		}
		if p := n.ApprovalPolicy; p != nil {
			if kind != KindHuman && !n.Approval {
				return fmt.Errorf("node %q: approval policy requires a human or approval node", n.ID)
			}
			if p.Assignment != "" && p.Assignment != "fixed" && p.Assignment != "business" {
				return fmt.Errorf("node %q: unsupported approval assignment", n.ID)
			}
			if len(p.Approvers) > 50 || p.Assignment == "business" && len(p.Approvers) > 0 {
				return fmt.Errorf("node %q: invalid approval candidates", n.ID)
			}
			for _, account := range p.Approvers {
				if account == "" || len(account) > 256 || strings.TrimSpace(account) != account || strings.IndexFunc(account, unicode.IsControl) >= 0 {
					return fmt.Errorf("node %q: invalid approver account", n.ID)
				}
			}
		}
		if kind == KindHuman {
			if err := validateHumanNode(n); err != nil {
				return err
			}
		} else {
			if n.Agent == "" {
				return fmt.Errorf("node %q: agent is required", n.ID)
			}
			if n.Task == "" {
				return fmt.Errorf("node %q: task is required", n.ID)
			}
			if len(n.Fields) > 0 {
				return fmt.Errorf("node %q: fields belong to human nodes", n.ID)
			}
			if !n.Approval && (len(n.Actions) > 0 || n.Timeout != "" || n.OnTimeout != "") {
				return fmt.Errorf("node %q: actions/timeout/on_timeout belong to human or approval nodes", n.ID)
			}
			if err := validateOrchestrate(n); err != nil {
				return err
			}
		}
		if err := validateHumanOptions(n); err != nil {
			return err
		}
		if len([]rune(n.Task)) > MaxTaskLen || len([]rune(n.Prompt)) > MaxTaskLen {
			return fmt.Errorf("node %q: task or prompt exceeds %d characters", n.ID, MaxTaskLen)
		}
		if n.MaxTurns < 0 {
			return fmt.Errorf("node %q: max_turns cannot be negative", n.ID)
		}
		if n.Expect != nil && !ValidArtifactPath(n.Expect.Path) {
			return fmt.Errorf("node %q: invalid expect path", n.ID)
		}
		for _, dep := range n.After {
			if dep.When != "" && !validWhen(dep.When) {
				return fmt.Errorf("node %q: invalid edge condition %q", n.ID, dep.When)
			}
			if dep.Node == n.ID {
				return fmt.Errorf("node %q: cannot depend on itself", n.ID)
			}
		}
	}
	for _, n := range d.Nodes {
		for _, dep := range n.After {
			if !ids[dep.Node] {
				return fmt.Errorf("node %q: unknown dependency %q", n.ID, dep.Node)
			}
		}
	}
	if _, err := d.TopoOrder(); err != nil {
		return err
	}
	return d.validateTemplates(ids)
}

// validateHumanNode checks the human-kind node shape: no agent knobs, a
// prompt to render, sane form fields.
func validateHumanNode(n Node) error {
	if n.Agent != "" || n.Expect != nil || n.Tools != nil || n.Model != "" || n.MaxTurns != 0 || n.Orchestrate != nil || n.Approval {
		return fmt.Errorf("node %q: human nodes take no agent, expect, tools, model, max_turns, orchestrate or approval", n.ID)
	}
	if n.Prompt == "" {
		return fmt.Errorf("node %q: prompt is required", n.ID)
	}
	if len(n.Fields) > 20 {
		return fmt.Errorf("node %q: fields exceed the limit of 20", n.ID)
	}
	seen := map[string]bool{}
	for _, f := range n.Fields {
		if f.Key == "" || len(f.Key) > 64 || strings.ContainsAny(f.Key, " \t\n/\\") {
			return fmt.Errorf("node %q: invalid field key %q", n.ID, f.Key)
		}
		if seen[f.Key] {
			return fmt.Errorf("node %q: duplicate field key %q", n.ID, f.Key)
		}
		seen[f.Key] = true
		if f.Type != "" && f.Type != "text" && f.Type != "select" {
			return fmt.Errorf("node %q: invalid field type %q", n.ID, f.Type)
		}
		if f.Type == "select" && len(f.Options) == 0 {
			return fmt.Errorf("node %q: select field %q needs options", n.ID, f.Key)
		}
		if len([]rune(f.Label)) > 100 {
			return fmt.Errorf("node %q: field label exceeds 100 characters", n.ID)
		}
		for _, o := range f.Options {
			if strings.TrimSpace(o) == "" || len([]rune(o)) > 200 {
				return fmt.Errorf("node %q: invalid option in field %q", n.ID, f.Key)
			}
		}
	}
	return nil
}

// validateHumanOptions checks the waiting knobs human and approval nodes
// share: action vocabulary and the timeout contract.
func validateHumanOptions(n Node) error {
	if len(n.Actions) > 8 {
		return fmt.Errorf("node %q: actions exceed the limit of 8", n.ID)
	}
	for _, a := range n.Actions {
		if a == "" || len(a) > 32 || strings.ContainsAny(a, " \t\n") {
			return fmt.Errorf("node %q: invalid action %q", n.ID, a)
		}
	}
	if n.Timeout != "" {
		d, err := time.ParseDuration(n.Timeout)
		if err != nil || d <= 0 || d > 720*time.Hour {
			return fmt.Errorf("node %q: invalid timeout %q", n.ID, n.Timeout)
		}
	}
	if n.OnTimeout != "" && n.OnTimeout != "fail" && n.OnTimeout != "reject" {
		return fmt.Errorf("node %q: invalid on_timeout %q", n.ID, n.OnTimeout)
	}
	return nil
}

// validateOrchestrate checks the dispatcher shape: a small candidate list,
// short hints, bounded budgets.
func validateOrchestrate(n Node) error {
	o := n.Orchestrate
	if o == nil {
		return nil
	}
	if len(o.Candidates) == 0 {
		return fmt.Errorf("node %q: orchestrate needs at least one candidate", n.ID)
	}
	if len(o.Candidates) > 8 {
		return fmt.Errorf("node %q: orchestrate allows at most 8 candidates", n.ID)
	}
	seen := map[string]bool{}
	for _, c := range o.Candidates {
		if c.Agent == "" {
			return fmt.Errorf("node %q: candidate agent is required", n.ID)
		}
		if seen[c.Agent] {
			return fmt.Errorf("node %q: duplicate candidate %q", n.ID, c.Agent)
		}
		seen[c.Agent] = true
		if len([]rune(c.Hint)) > 200 {
			return fmt.Errorf("node %q: candidate hint exceeds 200 characters", n.ID)
		}
	}
	if o.MaxDispatches < 0 || o.MaxDispatches > 20 {
		return fmt.Errorf("node %q: max_dispatches must be 0–20", n.ID)
	}
	if o.MaxParallel < 0 || o.MaxParallel > 4 {
		return fmt.Errorf("node %q: max_parallel must be 0–4", n.ID)
	}
	return nil
}

// closure is the set of nodes reachable upstream of id through after edges —
// the set a node's templates may reference.
func (d Definition) closure(id string) map[string]bool {
	byID := make(map[string][]Dep, len(d.Nodes))
	for _, n := range d.Nodes {
		byID[n.ID] = n.After
	}
	out := map[string]bool{}
	var walk func(string)
	walk = func(cur string) {
		for _, dep := range byID[cur] {
			if !out[dep.Node] {
				out[dep.Node] = true
				walk(dep.Node)
			}
		}
	}
	walk(id)
	return out
}

// TopoOrder returns node ids in an order where every dependency precedes its
// dependent — any edge kind counts; a cycle through any of them is a cycle.
// Among the nodes ready at any point the one earliest in the definition goes
// first. A cycle is an error naming one of its members.
func (d Definition) TopoOrder() ([]string, error) {
	index := make(map[string]int, len(d.Nodes))
	indeg := make(map[string]int, len(d.Nodes))
	for i, n := range d.Nodes {
		index[n.ID] = i
		indeg[n.ID] = 0
	}
	dependents := make(map[string][]string, len(d.Nodes))
	for _, n := range d.Nodes {
		for _, dep := range n.After {
			if _, known := indeg[dep.Node]; !known {
				return nil, fmt.Errorf("node %q: unknown dependency %q", n.ID, dep.Node)
			}
			indeg[n.ID]++
			dependents[dep.Node] = append(dependents[dep.Node], n.ID)
		}
	}
	ready := []string{}
	push := func(id string) {
		// Insert keeping definition order, so the earliest-defined ready node
		// is always at the head.
		at := len(ready)
		for at > 0 && index[ready[at-1]] > index[id] {
			at--
		}
		ready = append(ready, "")
		copy(ready[at+1:], ready[at:])
		ready[at] = id
	}
	for _, n := range d.Nodes {
		if indeg[n.ID] == 0 {
			push(n.ID)
		}
	}
	order := make([]string, 0, len(d.Nodes))
	for len(ready) > 0 {
		id := ready[0]
		ready = ready[1:]
		order = append(order, id)
		for _, next := range dependents[id] {
			indeg[next]--
			if indeg[next] == 0 {
				push(next)
			}
		}
	}
	if len(order) != len(d.Nodes) {
		for _, n := range d.Nodes {
			if indeg[n.ID] > 0 {
				return nil, fmt.Errorf("node %q is part of a dependency cycle", n.ID)
			}
		}
	}
	return order, nil
}

// node returns the definition's node with the given id.
func (d Definition) node(id string) Node {
	for _, n := range d.Nodes {
		if n.ID == id {
			return n
		}
	}
	return Node{ID: id}
}

// Node is the exported form of node, for platform code handling a record's
// definition snapshot.
func (d Definition) Node(id string) Node { return d.node(id) }
