// Package session persists a conversation as append-only JSONL.
//
// Records form a tree via ParentID rather than a flat list. A linear chat is
// just a tree with no branches, but keeping the parent link from day one is what
// makes "rewind to an earlier point and try again" a read of a different leaf
// instead of a rewrite of the file. A fork is itself a record (type "fork"),
// so a reload replays the branch the fork chose rather than whatever happens
// to be the file's last line.
package session

import (
	"bufio"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/yosukeno/pogo/llm"
)

type Record struct {
	ID       string       `json:"id"`
	ParentID string       `json:"parentId,omitempty"`
	Type     string       `json:"type"` // "meta" | "message" | "stats" | "fork"
	Time     int64        `json:"time"`
	Message  *llm.Message `json:"message,omitempty"`
	Meta     *Meta        `json:"meta,omitempty"`
	Stats    *Stats       `json:"stats,omitempty"`
	// Batch members stay ordinary records so existing exports remain readable.
	// A reader commits them only when all contiguous members are present.
	BatchID    string `json:"batchId,omitempty"`
	BatchIndex int    `json:"batchIndex,omitempty"`
	BatchSize  int    `json:"batchSize,omitempty"`
}

type Meta struct {
	Cwd   string `json:"cwd"`
	Model string `json:"model"`
	// Runtime is an execution-boundary marker, not an authority for configuration.
	// Agent Studio resolves its immutable binding from a separate server registry.
	Runtime string `json:"runtime,omitempty"`
	// Sentinel is written only in the creation record of a hosted background
	// conversation. It preserves its special role even before the watch's
	// retained-session pointer is committed, without changing its runtime.
	Sentinel *SentinelIdentity `json:"sentinel,omitempty"`
	// BackgroundTask is host-issued provenance for a scheduled execution,
	// written before the conversation is published. It grants no authority.
	BackgroundTask *BackgroundTaskIdentity `json:"background_task,omitempty"`
	AgentWorkflow  *AgentWorkflowIdentity  `json:"agent_workflow,omitempty"`
	// Skills are the skill names in effect when the session was created.
	//
	// Recorded because they change behaviour without appearing anywhere in the
	// messages: the same transcript replayed with a different skill directory is a
	// different run, and without this there is no way to tell after the fact.
	Skills []string `json:"skills,omitempty"`
	// Pinned and Title are sidebar edits. They arrive in a meta record appended
	// long after creation rather than by rewriting the first one — the file is
	// append-only — so they are pointers: "this record says nothing about it"
	// must stay distinct from "set to the zero value", and a reader merges by
	// taking the last non-nil.
	Pinned *bool   `json:"pinned,omitempty"`
	Title  *string `json:"title,omitempty"`
}

type SentinelIdentity struct {
	WatchID         string `json:"watch_id"`
	ParentSessionID string `json:"parent_session_id,omitempty"`
	AgentID         string `json:"agent_id"`
}

type BackgroundTaskIdentity struct {
	Source   string `json:"source"`
	TaskID   string `json:"task_id"`
	SourceID string `json:"source_id"`
	AgentID  string `json:"agent_id"`
	RunID    string `json:"run_id,omitempty"`
	OwnerID  string `json:"owner_id"`
}

// AgentWorkflowIdentity is immutable host provenance, never imported authority.
type AgentWorkflowIdentity struct {
	OwnerID         string `json:"owner_id"`
	AgentID         string `json:"agent_id"`
	WorkflowID      string `json:"workflow_id"`
	RunID           string `json:"run_id"`
	SourceSessionID string `json:"source_session_id,omitempty"`
}

func CreateAgentWorkflowWebConversation(dir, cwd, model string, identity AgentWorkflowIdentity, skills ...string) (*Store, error) {
	return createWithMeta(dir, &Meta{Cwd: cwd, Model: model, Skills: skills, Runtime: "agent-studio-web-v1", AgentWorkflow: &identity}, true)
}

// Stats records per-turn performance metrics.
type Stats struct {
	// DelegationRunID makes a background child's incremental charge idempotent.
	// It is committed in the same fsynced record as the usage, not a separate
	// receipt file whose crash window could lose or double-count spending.
	DelegationRunID string `json:"delegation_run_id,omitempty"`
	// Token usage for this turn
	Usage *UsageStats `json:"usage,omitempty"`
	// Delegated is the part of Usage that subagents spent, absent when nothing was
	// delegated. A subset of Usage, never a sibling: adding the two would count the
	// same tokens twice, which is exactly the mistake this field exists to make
	// unnecessary — before it, the only way to attribute a delegation's cost was to
	// find each child's transcript and analyse it separately.
	Delegated *UsageStats `json:"delegated,omitempty"`
	// Tool execution statistics for this turn
	Tools *ToolStats `json:"tools,omitempty"`
	// Retry information for this turn
	Retry *RetryStats `json:"retry,omitempty"`
	// Approval gate information for this turn
	Approval *ApprovalStats `json:"approval,omitempty"`
	// Composition estimates what the prompt was made of when this record was
	// written. Unlike every other field here it is a snapshot rather than a delta,
	// so the newest one is the answer and summing them means nothing — see
	// Composition's own doc comment, which says so at length because the
	// surrounding convention is the opposite.
	Composition *Composition `json:"composition,omitempty"`
}

// UsageStats records token usage for one turn.
type UsageStats struct {
	Input     int64 `json:"input"`
	Output    int64 `json:"output"`
	CacheRead int64 `json:"cache_read,omitempty"`
	Reasoning int64 `json:"reasoning,omitempty"`
}

// ToolStats records tool execution metrics for one turn.
type ToolStats struct {
	CallCount int            `json:"call_count"`          // Number of tool calls in this turn
	Duration  int64          `json:"duration"`            // Total duration in milliseconds
	ExecTime  []ToolExecTime `json:"exec_time,omitempty"` // Individual tool execution times
}

// ToolExecTime records execution time for a single tool call.
type ToolExecTime struct {
	Name     string `json:"name"`
	Duration int64  `json:"duration"` // Duration in milliseconds
	IsError  bool   `json:"is_error,omitempty"`
}

// RetryStats records retry information for one turn.
type RetryStats struct {
	Count  int    `json:"count"`            // Number of retries
	Reason string `json:"reason,omitempty"` // Last retry reason
}

// ApprovalStats records approval gate metrics for one turn.
type ApprovalStats struct {
	WaitTime  int64 `json:"wait_time"`  // Total wait time in milliseconds
	AskCount  int   `json:"ask_count"`  // Number of approval requests
	DenyCount int   `json:"deny_count"` // Number of denied requests
}

// Store appends records to one JSONL file.
type Store struct {
	path string
	// head is the id of the last written record, i.e. the current leaf.
	head    string
	records []Record
	meta    *Meta
	// parent overrides a record's own ParentID. It holds an entry only where
	// damage broke the chain and Open stitched it back together; see repairChain.
	parent map[string]string
	diags  []string
	// A failed rollback makes the on-disk outcome uncertain. Refuse further
	// appends until Open reconciles it instead of duplicating a possibly saved turn.
	writeErr  error
	writeFile func(*os.File, []byte) error // per-store fault injection; nil in production
}

// Create starts a new session file under dir. skills is recorded as metadata; see
// Meta.Skills for why.
func Create(dir, cwd, model string, skills ...string) (*Store, error) {
	return create(dir, cwd, model, false, skills...)
}

// CreateMultiOwner starts a session in a directory whose ids must stay unique
// across many owners. The suffix is a full 128-bit nonce and the name is
// claimed exclusively before the first record is written, so a collision with
// another owner's session is an error rather than a silently shared file. The
// index still tracks the file: it is a cache of this same directory, never an
// ownership authority.
func CreateMultiOwner(dir, cwd, model string, skills ...string) (*Store, error) {
	return create(dir, cwd, model, true, skills...)
}

// create's nonce flag selects the collision-resistant exclusive name; both
// variants stay indexed.
func create(dir, cwd, model string, nonce bool, skills ...string) (*Store, error) {
	return createWithMeta(dir, &Meta{Cwd: cwd, Model: model, Skills: skills}, nonce)
}

// CreateAgentConversation marks its execution boundary in the first record, so
// losing a separate platform binding can never restore it as a coding session.
func CreateAgentConversation(dir, cwd, model string) (*Store, error) {
	return createWithMeta(dir, &Meta{Cwd: cwd, Model: model, Runtime: "agent-studio-v1"}, true)
}

// CreateAgentWebConversation uses the normal Pogo Web execution profile while
// pinning its Agent identity in the first record. A missing binding must not
// silently turn it into an unconfigured general-purpose session on recovery.
func CreateAgentWebConversation(dir, cwd, model string, skills ...string) (*Store, error) {
	return createWithMeta(dir, &Meta{Cwd: cwd, Model: model, Skills: skills, Runtime: "agent-studio-web-v1"}, true)
}

func CreateSentinelWebConversation(dir, cwd, model string, identity SentinelIdentity, skills ...string) (*Store, error) {
	return createWithMeta(dir, &Meta{Cwd: cwd, Model: model, Skills: skills, Runtime: "agent-studio-web-v1", Sentinel: &identity}, true)
}

func CreateBackgroundTaskWebConversation(dir, cwd, model string, identity BackgroundTaskIdentity, skills ...string) (*Store, error) {
	return createWithMeta(dir, &Meta{Cwd: cwd, Model: model, Skills: skills, Runtime: "agent-studio-web-v1", BackgroundTask: &identity}, true)
}

func createWithMeta(dir string, meta *Meta, nonce bool) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	name := fmt.Sprintf("%s-%s.jsonl", time.Now().UTC().Format("20060102T150405Z"), randomID(4))
	if nonce {
		var buf [16]byte
		if _, err := rand.Read(buf[:]); err != nil {
			return nil, err
		}
		name = fmt.Sprintf("%s-%s.jsonl", time.Now().UTC().Format("20060102T150405Z"), hex.EncodeToString(buf[:]))
		f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return nil, err
		}
		if err = f.Close(); err != nil {
			return nil, err
		}
	}
	s := &Store{path: filepath.Join(dir, name)}
	return s, s.append(Record{Type: "meta", Meta: meta})
}

// WriteImported writes validated records to a new session file under dir and
// returns the opened store. The web layer's import path: records arrive from a
// portable export, keep their ids and parent links verbatim, and get a fresh
// timestamped filename so an import never overwrites the session it came from.
//
// The caller has already validated each record; this function's own failure
// modes are the filesystem's. O_EXCL plus the random suffix covers two imports
// landing in the same second.
func WriteImported(dir string, records []Record) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	var f *os.File
	var path string
	for attempt := 0; attempt < 3; attempt++ {
		name := fmt.Sprintf("%s-%s.jsonl", time.Now().UTC().Format("20060102T150405Z"), randomID(4))
		path = filepath.Join(dir, name)
		var err error
		f, err = os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			break
		}
		if !os.IsExist(err) {
			return nil, err
		}
		f = nil
	}
	if f == nil {
		return nil, fmt.Errorf("could not allocate a session file name under %s", dir)
	}
	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)
	for _, r := range records {
		if err := enc.Encode(r); err != nil {
			f.Close()
			os.Remove(path)
			return nil, err
		}
	}
	if err := w.Flush(); err != nil {
		f.Close()
		os.Remove(path)
		return nil, err
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return nil, err
	}
	return Open(path)
}

// Open loads an existing session file.
//
// An unreadable line never fails the load — the transcript is usually the only
// copy, so recovering what is there beats refusing to start. But it is not
// ignored either: see Diagnostics, and repairChain for how the surviving records
// are kept reachable.
func Open(path string) (*Store, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return readRecords(f, path)
}

// Read inspects a reader using the same committed-record and branch semantics as
// Open. It has no backing path and cannot append to the source transcript.
func Read(reader io.Reader) (*Store, error) { return readRecords(reader, "") }

func readRecords(reader io.Reader, path string) (*Store, error) {
	s := &Store{path: path}
	sc := bufio.NewScanner(reader)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	// Unreadable lines are collected by line number rather than acted on where
	// they are found, because what one means depends on whether anything follows
	// it: the last line is a half-written record, an earlier one is damage.
	var bad []int
	lineNo, lastNonEmpty := 0, 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		lastNonEmpty = lineNo
		var r Record
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			bad = append(bad, lineNo)
			continue
		}
		if r.ID == "" {
			// Without an id the record cannot be linked to, so it is damage of
			// the same kind even though it parsed.
			bad = append(bad, lineNo)
			continue
		}
		s.records = append(s.records, r)
		s.head = r.ID
		if r.Type == "meta" && r.Meta != nil && s.meta == nil {
			s.meta = r.Meta
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}

	if n := len(bad); n > 0 && bad[n-1] == lastNonEmpty {
		// A half-written final line is the ordinary result of the process being
		// killed mid-append. Nothing follows it, so nothing is lost but the record
		// that never finished.
		bad = bad[:n-1]
	}
	if len(bad) > 0 {
		s.diags = append(s.diags, fmt.Sprintf(
			"%d unreadable record(s) at line %s: this file is damaged, not just truncated",
			len(bad), joinInts(bad)))
	}
	s.records, _ = committedRecords(s.records, &s.diags)
	s.head = ""
	if len(s.records) > 0 {
		s.head = s.records[len(s.records)-1].ID
	}
	s.repairChain()
	_, interrupted := recoverToolResults(s.rawTimedMessages(""))
	if interrupted > 0 {
		s.diags = append(s.diags, fmt.Sprintf("recovered %d interrupted tool call(s) with unknown outcome; tools were not rerun", interrupted))
	}
	return s, nil
}

// repairChain reconnects records whose parent did not survive.
//
// This is what stands between a single damaged line and losing every message
// before it. Replay walks up ParentID from the leaf, so one missing link used to
// end the walk there and silently drop the whole earlier history — the records
// were still on disk, just unreachable.
//
// The repair is only sound where file order is chain order, which is to say in a
// file with no branches. A branched file gets a diagnostic instead: guessing
// which branch a record belonged to could splice two conversations together, and
// a wrong transcript is worse than a short one.
func (s *Store) repairChain() {
	present := make(map[string]bool, len(s.records))
	children := make(map[string]int, len(s.records))
	for _, r := range s.records {
		present[r.ID] = true
	}
	for _, r := range s.records {
		if r.ParentID != "" {
			children[r.ParentID]++
		}
	}
	branched := false
	for _, n := range children {
		if n > 1 {
			branched = true
			break
		}
	}

	var broken, repaired int
	for i, r := range s.records {
		if r.ParentID == "" || present[r.ParentID] {
			continue
		}
		broken++
		if branched {
			continue
		}
		if s.parent == nil {
			s.parent = make(map[string]string)
		}
		// The previous surviving record is where this one rejoins. When there is
		// no previous record the lost parent was the root, so this record becomes
		// the root rather than a dead end.
		if i > 0 {
			s.parent[r.ID] = s.records[i-1].ID
			repaired++
		} else {
			s.parent[r.ID] = ""
		}
	}

	switch {
	case broken == 0:
	case branched:
		s.diags = append(s.diags, fmt.Sprintf(
			"%d record(s) lost their parent and this file has branches, so history before the damage is not replayed",
			broken))
	case repaired > 0:
		s.diags = append(s.diags, fmt.Sprintf(
			"reconnected %d broken link(s) using file order to keep the earlier history reachable", repaired))
	}
}

// Diagnostics reports damage found while loading. Empty for a healthy file.
//
// These are surfaced rather than returned as an error because none of them stops
// the session from being used, and a user who is told "4 records were
// unreadable" can act on it, while one who is told nothing cannot.
func (s *Store) Diagnostics() []string { return s.diags }

// Meta is the metadata recorded when the session was created. Nil for a file
// with no readable meta record.
func (s *Store) Meta() *Meta { return s.meta }

func joinInts(ns []int) string {
	parts := make([]string, len(ns))
	for i, n := range ns {
		parts[i] = fmt.Sprint(n)
	}
	return strings.Join(parts, ", ")
}

// Latest returns the most recently modified session file in dir.
func Latest(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl") {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return "", fmt.Errorf("no sessions in %s", dir)
	}
	// Filenames are UTC timestamps, so lexical order is chronological.
	sort.Strings(names)
	return filepath.Join(dir, names[len(names)-1]), nil
}

func (s *Store) Path() string { return s.path }

// Head is the current leaf id, usable as a fork point.
func (s *Store) Head() string { return s.head }

// Append writes a message as a child of the current head.
func (s *Store) Append(m llm.Message) error {
	return s.append(Record{Type: "message", Message: &m})
}

// AppendStats records metrics for the turns just written.
//
// This existed as a type and a parser long before it existed as a writer: the Stats
// struct here, the matching record in package analyze, and the whole "Token Usage"
// section of `-analyze-session` were all complete, and nothing ever produced one. So
// every session reported costing zero tokens — which reads as a fact rather than as
// the absence of one, and made a subagent's transcript unable to answer the only
// question anyone asks of it afterwards, which is what the delegation cost.
//
// What a transcript records is the *whole* cost of that run, delegation included.
// A parent's counters already have its subagents' tokens folded in — that is what
// makes -token-budget cover delegated work — so a parent's transcript answers "what
// did this session cost, all in", and each subagent's own transcript is the
// breakdown. Measured on one live run: parent 14635 in, child 9291 of them.
//
// So these numbers must not be added across transcripts. Stated here because the
// arithmetic looks additive and is not, and because a cost report that silently
// counts the same tokens twice is worse than no report at all. The link between the
// two is SubagentDetails.Session.
func (s *Store) AppendStats(st Stats) error {
	return s.append(Record{Type: "stats", Stats: &st})
}

// AppendDelegatedStatsOnce commits one background run's incremental usage.
// Like other Store writes the caller serializes access. All branches count:
// rewinding or compacting must not make an already billed run billable again.
// A zero-usage receipt is still recorded so duplicate completions stay silent.
func (s *Store) AppendDelegatedStatsOnce(runID string, usage UsageStats) (bool, error) {
	if strings.TrimSpace(runID) == "" {
		return false, fmt.Errorf("delegation run id is required")
	}
	if usage.Input < 0 || usage.Output < 0 || usage.CacheRead < 0 || usage.Reasoning < 0 {
		return false, fmt.Errorf("delegation run usage must be an increment")
	}
	for _, r := range s.records {
		if r.Stats != nil && r.Stats.DelegationRunID == runID {
			return false, nil
		}
	}
	err := s.AppendStats(Stats{DelegationRunID: runID, Usage: &usage, Delegated: &usage})
	return err == nil, err
}

// AppendMeta records sidebar edits (pin state, custom title) as an appended meta
// record. The creation record is never rewritten: the file stays append-only,
// and a reader merges by taking the last non-nil value — see Meta.Pinned.
func (s *Store) AppendMeta(m *Meta) error {
	return s.append(Record{Type: "meta", Meta: m})
}

// AppendAll writes messages in order.
func (s *Store) AppendAll(ms []llm.Message) error {
	records := make([]Record, len(ms))
	for i := range ms {
		m := ms[i]
		records[i] = Record{Type: "message", Message: &m}
	}
	return s.appendRecords(records, s.head)
}

// Timed pairs a message with when its record was written, for UIs that show
// send times.
type Timed struct {
	Message llm.Message
	Time    int64
}

// Messages replays the branch ending at leaf. An empty leaf means the head.
func (s *Store) Messages(leaf string) []llm.Message {
	timed := s.TimedMessages(leaf)
	out := make([]llm.Message, 0, len(timed))
	for _, t := range timed {
		out = append(out, t.Message)
	}
	return out
}

// BranchRecords is TimedMessages without the message-only filter: every record
// on the branch ending at leaf (an empty leaf means the head), oldest first —
// meta, messages, stats and fork markers alike. The web layer's Markdown
// export walks this so the file it serves is what a replay would show,
// per-turn stats included, without re-deriving the fork rules a second time.
func (s *Store) BranchRecords(leaf string) []Record { return s.chain(leaf) }

// TimedMessages is Messages with each record's timestamp attached.
func (s *Store) TimedMessages(leaf string) []Timed {
	out, _ := recoverToolResults(s.rawTimedMessages(leaf))
	return out
}

func (s *Store) rawTimedMessages(leaf string) []Timed {
	var out []Timed
	for _, r := range s.chain(leaf) {
		if r.Type == "message" && r.Message != nil {
			out = append(out, Timed{Message: *r.Message, Time: r.Time})
		}
	}
	return out
}

// UsageTotals sums every usage delta in the file — the session's all-in cost,
// delegation included (see AppendStats for why nothing is added across
// transcripts). A reopened session seeds its counters from this, so a restart does
// not zero the running total.
//
// Every record, deliberately, and not just the ones on the live branch. This used
// to walk the current chain, which meant a rewind or a compaction made the
// abandoned branch's spending disappear: Fork("") drops everything reachable, so a
// compacted session reopened from disk reported having cost nothing. The provider
// charged for those tokens whichever branch is live now, and two mechanisms depend
// on that being true — -token-budget and -cost-budget are ceilings, and a ceiling
// that forgets what was spent every time the conversation is reorganised is not one.
//
// It also puts this in agreement with package analyze, which has always read every
// record linearly. Two things that count one session's cost should not disagree
// about it.
func (s *Store) UsageTotals() (usage, delegated llm.Usage) {
	for _, r := range s.records {
		if r.Type != "stats" || r.Stats == nil {
			continue
		}
		if u := r.Stats.Usage; u != nil {
			usage.Input += u.Input
			usage.Output += u.Output
			usage.CacheRead += u.CacheRead
			usage.Reasoning += u.Reasoning
		}
		if d := r.Stats.Delegated; d != nil {
			delegated.Input += d.Input
			delegated.Output += d.Output
			delegated.CacheRead += d.CacheRead
			delegated.Reasoning += d.Reasoning
		}
	}
	return usage, delegated
}

// chain walks the branch ending at leaf (an empty leaf means the head),
// oldest record first.
func (s *Store) chain(leaf string) []Record {
	if leaf == "" {
		leaf = s.head
	}
	byID := make(map[string]Record, len(s.records))
	for _, r := range s.records {
		byID[r.ID] = r
	}

	var chain []Record
	// Links always point backwards in file order, so a cycle cannot arise from
	// anything this package writes. The guard is for a hand-edited file: reading
	// one should not hang.
	seen := make(map[string]bool, len(s.records))
	for id := leaf; id != "" && !seen[id]; {
		seen[id] = true
		r, ok := byID[id]
		if !ok {
			break
		}
		chain = append(chain, r)
		if p, ok := s.parent[r.ID]; ok {
			id = p // a link Open repaired
		} else {
			id = r.ParentID
		}
	}
	// Walking up yields newest first.
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain
}

// Fork points subsequent appends at an earlier record, branching the tree.
// The fork is itself recorded — a marker hanging off the fork point — because
// the head is otherwise just "the last line of the file": without the marker,
// a reload between the fork and the next append would replay the abandoned
// branch as if the rewind had never happened. An empty recordID points at the
// root: everything currently reachable is abandoned, and the marker starts a
// fresh chain.
func (s *Store) Fork(recordID string) error {
	if recordID != "" {
		found := false
		for _, r := range s.records {
			if r.ID == recordID {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("record %s not found in %s", recordID, s.path)
		}
	}
	return s.appendRecords([]Record{{Type: "fork"}}, recordID)
}

// ForkWithMessages commits a replacement branch as one unit. A crash while
// writing the summary leaves the previous branch live, never an empty fork.
func (s *Store) ForkWithMessages(recordID string, ms []llm.Message) error {
	if recordID != "" {
		found := false
		for _, r := range s.records {
			if r.ID == recordID {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("record %s not found in %s", recordID, s.path)
		}
	}
	records := make([]Record, 1, len(ms)+1)
	records[0] = Record{Type: "fork"}
	for i := range ms {
		m := ms[i]
		records = append(records, Record{Type: "message", Message: &m})
	}
	return s.appendRecords(records, recordID)
}

// RewindPoint finds the fork point that removes the kth visible user message
// (1-based) and everything that follows it. Visible means the message carries
// something other than tool results — the same filter the web Hub's Seed
// applies when it decides which records reach the timeline, which is what
// makes the ordinal the UI counts match the records counted here. The
// returned id is the record to Fork to: the target's parent (repaired link
// if Open stitched one), possibly "" when the target hangs off the root.
func (s *Store) RewindPoint(k int) (string, bool) {
	if k < 1 {
		return "", false
	}
	n := 0
	for _, r := range s.chain("") {
		if r.Type != "message" || r.Message == nil || r.Message.Role != llm.RoleUser {
			continue
		}
		if !visible(r.Message) {
			continue
		}
		n++
		if n == k {
			if p, ok := s.parent[r.ID]; ok {
				return p, true
			}
			return r.ParentID, true
		}
	}
	return "", false
}

// ExchangeMessages resolves the kth visible user message (1-based — the same
// counting rule as RewindPoint, which is what lets a caller holding the web
// Hub's ordinal line the two up) to the exchange it opens, and returns every
// message of that exchange with its timestamp: the question, the turns that
// answered it (thinking, tool calls, tool-result carriers) and any harness
// notices and steering that landed mid-run. The span closes at the next
// non-steering send; a
// harness notice rides the exchange it interrupted (the web timeline folds it
// in as a notice, not a boundary), and tool-result carriers were never visible
// to begin with. This is the projection "share this answer" publishes, so the
// public page can render the exchange the way the session view did — work
// included.
func (s *Store) ExchangeMessages(k int) ([]Timed, bool) {
	if k < 1 {
		return nil, false
	}
	recs := s.chain("")
	n := 0
	start := -1
	for i, r := range recs {
		if r.Type != "message" || r.Message == nil || r.Message.Role != llm.RoleUser || !visible(r.Message) {
			continue
		}
		n++
		if n == k {
			start = i
			break
		}
	}
	if start < 0 {
		return nil, false
	}
	out := []Timed{{Message: *recs[start].Message, Time: recs[start].Time}}
	for _, r := range recs[start+1:] {
		if r.Type != "message" || r.Message == nil {
			continue
		}
		if r.Message.Role == llm.RoleUser && visible(r.Message) && !r.Message.Steering && !IsHarnessNotice(r.Message.Text()) {
			break
		}
		out = append(out, Timed{Message: *r.Message, Time: r.Time})
	}
	return out, true
}

// ExchangeSpan is ExchangeMessages flattened to text: the question and every
// assistant text that answered it, joined in turn order.
func (s *Store) ExchangeSpan(k int) (question, answer string, ok bool) {
	msgs, ok := s.ExchangeMessages(k)
	if !ok {
		return "", "", false
	}
	question = msgs[0].Message.Text()
	var parts []string
	for _, m := range msgs[1:] {
		if m.Message.Role != llm.RoleAssistant {
			continue
		}
		if t := strings.TrimSpace(m.Message.Text()); t != "" {
			parts = append(parts, t)
		}
	}
	return question, strings.Join(parts, "\n\n"), true
}

// visible reports whether a message carries anything other than tool results.
// A tool-result-only user message never appears on a timeline, so it must not
// count toward a rewind ordinal either.
func visible(m *llm.Message) bool {
	for _, b := range m.Content {
		if b.Type != llm.BlockToolResult {
			return true
		}
	}
	return false
}

// harnessNotices are the prefixes a harness-generated message wears so no
// reader mistakes it for something the user typed. Background task
// completions wrap themselves in <task-notification>; the todo-finish
// reminder and the soft-cap checkpoint are prefixed "[pogo] " — the same
// marker the web timeline renders as a notice rather than a bubble.
var harnessNotices = []string{"<task-notification>", "[pogo] "}

// IsHarnessNotice reports whether a user message's text was injected by the
// harness rather than typed: real text, but not a send.
func IsHarnessNotice(text string) bool {
	for _, p := range harnessNotices {
		if strings.HasPrefix(text, p) {
			return true
		}
	}
	return false
}

func (s *Store) append(r Record) error {
	return s.appendRecords([]Record{r}, s.head)
}

func (s *Store) appendRecords(records []Record, parent string) error {
	if len(records) == 0 {
		return nil
	}
	if s.writeErr != nil {
		return s.writeErr
	}
	batchID := ""
	if len(records) > 1 {
		batchID = randomID(8)
	}
	var payload []byte
	for i := range records {
		r := &records[i]
		r.ID, r.ParentID, r.Time = randomID(8), parent, time.Now().UnixMilli()
		parent = r.ID
		if batchID != "" {
			r.BatchID, r.BatchIndex, r.BatchSize = batchID, i, len(records)
		}
		line, err := json.Marshal(r)
		if err != nil {
			return err
		}
		payload = append(payload, line...)
		payload = append(payload, '\n')
	}
	// O_RDWR rather than O_WRONLY so the last byte can be checked; O_APPEND still
	// forces every write to the end.
	f, err := os.OpenFile(s.path, os.O_APPEND|os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	write := s.writeFile
	if write == nil {
		write = writeLines
	}
	err = write(f, payload)
	if err == nil {
		err = f.Sync()
	}
	// Close can be where a deferred write finally fails, so its error counts too.
	// Reporting success for a record that never landed is how a transcript and
	// the agent's memory drift apart.
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		// The write may have returned a short count or even failed after all
		// bytes landed. Restore the pre-append length before allowing a retry.
		rf, rollbackErr := os.OpenFile(s.path, os.O_RDWR, 0o600)
		if rollbackErr == nil {
			rollbackErr = rf.Truncate(st.Size())
			if rollbackErr == nil {
				rollbackErr = rf.Sync()
			}
			if cerr := rf.Close(); rollbackErr == nil {
				rollbackErr = cerr
			}
		}
		if rollbackErr != nil {
			s.writeErr = fmt.Errorf("append outcome uncertain; reopen session before retrying: %w (rollback: %v)", err, rollbackErr)
			return s.writeErr
		}
		return err
	}
	s.records = append(s.records, records...)
	s.head = parent
	// The first meta record is the creation record; same rule as Open's parse,
	// so a just-created Store answers Meta() like a reloaded one.
	for _, r := range records {
		if r.Type == "meta" && r.Meta != nil && s.meta == nil {
			s.meta = r.Meta
		}
	}
	// The transcript is already safely on disk, so this is the moment the
	// derived index can be brought current — and the only moment: every write
	// funnels through append, so one hook covers create, message, stats,
	// sidebar edits and forks alike. Best-effort by contract; see touchIndex.
	s.touchIndex()
	return nil
}

// writeLine appends one record, first closing off any unterminated line.
//
// A file whose previous write was cut short has no closing newline. Appending
// straight onto it fuses the leftover fragment and this record into a single
// unparseable line, which destroys a good record on top of the damaged one — and
// that second loss is what breaks the parent chain and takes the earlier history
// with it.
func writeLine(f *os.File, line []byte) error {
	return writeLines(f, append(line, '\n'))
}

func writeLines(f *os.File, lines []byte) error {
	st, err := f.Stat()
	if err != nil {
		return err
	}
	if st.Size() > 0 {
		last := make([]byte, 1)
		if _, err := f.ReadAt(last, st.Size()-1); err != nil {
			return err
		}
		if last[0] != '\n' {
			if _, err := f.Write([]byte{'\n'}); err != nil {
				return err
			}
		}
	}
	n, err := f.Write(lines)
	if err == nil && n != len(lines) {
		err = io.ErrShortWrite
	}
	return err
}

// DefaultDir is where sessions live when no path is given.
func DefaultDir() string {
	if d := os.Getenv("POGO_SESSION_DIR"); d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".pogo", "sessions")
}

// SubagentDirName is the subdirectory under the session dir where a delegated
// run writes its own transcript.
//
// Physical separation beats a metadata flag here for one reason: List already
// skips directories, so a transcript under subagents/ never reaches the
// sidebar, the terminal's session picker, or Latest's resume candidate — with
// no filtering code anywhere. A flag in the file's meta would need every one
// of those readers to remember to check it. Claude Code made the same choice
// (~/.claude/projects/<p>/<s>/subagents/), and the bug reports when its
// cloud mirror forgot to exclude them are the evidence the exclusion matters.
const SubagentDirName = "subagents"

func randomID(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// Info summarises a session file without loading its whole history. The Web UI's
// session sidebar is built from these.
type Info struct {
	Path string `json:"path"`
	ID   string `json:"id"`
	// Title is the first user message, trimmed, or the custom name a rename
	// recorded. Empty for a session that was created but never used.
	Title string `json:"title"`
	// Pinned sessions sort above the rest in List. Set by a later meta record;
	// see Meta.Pinned.
	Pinned bool   `json:"pinned,omitempty"`
	Cwd    string `json:"cwd,omitempty"`
	Model  string `json:"model,omitempty"`
	// Skills were in effect when the session was created. Exposed because they
	// change how the session behaves without appearing in any message, so a
	// listing that omits them cannot explain why two sessions differ.
	Skills []string `json:"skills,omitempty"`
	// Messages counts only user-role messages on the live branch — the
	// sidebar's "N 条用户消息". Assistant replies are excluded so the number
	// reads as "how many times I wrote", not the transcript length.
	Messages int   `json:"messages"`
	Created  int64 `json:"created,omitempty"`
	Updated  int64 `json:"updated,omitempty"`
}

const titleMaxRunes = 80

// Title collapses a prompt to a single readable line, the sidebar's derived
// title shape. Exported so the live overlay (web.Manager.LiveTitles) derives
// exactly what a later disk read will, rather than flashing a differently
// truncated title for the same prompt.
func Title(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > titleMaxRunes {
		return string(r[:titleMaxRunes]) + "…"
	}
	return s
}

// AttachmentTitle names a textless send from what it carried: the composer's
// first layer is what a scanning eye wants — a folder chip collapses to its
// root directory, a loose file to its own name, and several first layers
// join into one line. Exported so the live overlay derives the same line a
// later disk read will, the same deal as Title.
func AttachmentTitle(m llm.Message) string {
	var names []string
	seen := map[string]bool{}
	add := func(ref, name string) {
		if n := firstLayerName(ref, name); n != "" && !seen[n] {
			seen[n] = true
			names = append(names, n)
		}
	}
	for _, b := range m.Content {
		switch b.Type {
		case llm.BlockImage:
			add(b.ImageRef, b.ImageName)
		case llm.BlockFile:
			add(b.FilePath, b.FileName)
		}
	}
	return strings.Join(names, ", ")
}

// UserInputTitle is the readable title source of an authored ordered message.
// It does not rewrite message text: observations remain complete for the model
// and history, while their wrapper/metadata never become sidebar title prose.
// Old transcripts without a layout keep their original title derivation.
func UserInputTitle(m llm.Message) string {
	hasShortcut := false
	for _, block := range m.Content {
		hasShortcut = hasShortcut || block.InputShortcut != nil || block.InputOffice != nil
	}
	if m.InputLayout == "" && !hasShortcut {
		if text := m.Text(); text != "" {
			return text
		}
		return AttachmentTitle(m)
	}
	var title strings.Builder
	for index := 0; index < len(m.Content); index++ {
		block := m.Content[index]
		switch block.Type {
		case llm.BlockText:
			if block.InputShortcut != nil {
				title.WriteString("〔" + block.InputShortcut.Title + "〕")
				index += min(max(block.InputShortcut.BlockCount, 0), len(m.Content)-index-1)
				continue
			}
			if block.InputOffice != nil {
				title.WriteString("〔" + block.InputOffice.Title + "〕")
				continue
			}
			if !block.InputElement {
				title.WriteString(block.Text)
				continue
			}
			label := "页面元素"
			for _, line := range strings.Split(block.Text, "\n") {
				if value, found := strings.CutPrefix(line, "element: "); found && strings.TrimSpace(value) != "" {
					label = strings.Join(strings.Fields(html.UnescapeString(value)), " ")
					if runes := []rune(label); len(runes) > 32 {
						label = string(runes[:32]) + "…"
					}
					break
				}
			}
			title.WriteString("〔" + label + "〕")
		case llm.BlockFile, llm.BlockImage:
			name, ref := block.FileName, block.FilePath
			if block.Type == llm.BlockImage {
				name, ref = block.ImageName, block.ImageRef
			}
			if name == "" {
				name = ref
			}
			if name != "" {
				title.WriteString("〔" + filepath.Base(strings.ReplaceAll(name, "\\", "/")) + "〕")
			}
		}
	}
	return title.String()
}

// firstLayerName reduces "uploads/<root>/<...>/file" to "<root>" and
// "uploads/file" to "file": the layer the composer showed as chips. A blank
// or malformed ref falls back to the block's own name.
func firstLayerName(ref, name string) string {
	rel := strings.TrimPrefix(strings.ReplaceAll(ref, "\\", "/"), "uploads/")
	seg := strings.Split(rel, "/")
	if len(seg) >= 2 && seg[0] != "" {
		return seg[0]
	}
	if seg[len(seg)-1] != "" {
		return seg[len(seg)-1]
	}
	return name
}

// List summarises every session in dir, newest first.
//
// The index answers first and the directory arbitrates: each transcript is
// compared against its indexed row, and only what differs is read (see
// listIndexed). A corrupt, locked, or disabled index falls back to scanning
// every file — the listing may get slow, never wrong or empty.
func List(dir string) ([]Info, error) {
	entries, err := listDirEntries(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Info
	indexed := false
	withIndex(dir, func(db *sql.DB) (err error) {
		out, err = listIndexed(db, dir, entries)
		if err == nil {
			indexed = true
		}
		return err
	})
	if !indexed {
		// A corrupt, locked, or disabled index must not hide the sessions.
		out = nil
		for _, e := range entries {
			sum, _, err := loadSummary(filepath.Join(dir, e.name))
			if err != nil {
				continue
			}
			out = append(out, sum.info)
		}
	}
	sortInfos(out)
	return out, nil
}

// summary is what a summarising walk produces: the sidebar Info plus the
// session's searchable text. The text rides along because the index wants
// both from one pass, and a second walker recomputing what the first already
// walked is a second place for the two to disagree. texts is the same content
// before concatenation — one entry per message, same chronological window —
// so search can quote a match back to the message it lives in.
type summary struct {
	info  Info
	text  string
	texts []string
}

// summariseRecords walks the branch ending at tip (the live branch when tip is
// the file's last record) and derives the sidebar summary from it.
//
// parent is the repair map Open stitches damaged chains with, nil for a walk
// over freshly parsed records — see Store.append's index hook for why both
// callers exist. The walk runs newest→oldest: for merged fields (pin, rename)
// the FIRST value met wins, matching the last-non-nil merge of a forward
// read; for the derived title and the creation meta the LAST one standing
// wins, since the oldest record on the branch owns them.
func summariseRecords(path string, records []Record, tip string, parent map[string]string, modTime int64) summary {
	info := Info{
		Path:    path,
		ID:      strings.TrimSuffix(filepath.Base(path), ".jsonl"),
		Updated: modTime,
	}

	// The map holds indexes into records; a duplicate id leaves the last one
	// in, which is what the previous map-of-values build did too.
	pos := make(map[string]int, len(records))
	for i, r := range records {
		pos[r.ID] = i
	}

	var texts []string // newest→oldest; reversed once the walk is done
	customTitle := ""
	derivedTitle := ""
	pinnedSet := false
	for id := tip; id != ""; {
		i, ok := pos[id]
		if !ok {
			// A missing parent ends the walk: history before the damage is
			// unreachable, which is exactly what Open replays too.
			break
		}
		r := records[i]
		if p, ok := parent[r.ID]; ok {
			id = p
		} else {
			id = r.ParentID
		}
		switch {
		case r.Type == "meta" && r.Meta != nil:
			// Overwritten all the way up, so the oldest meta on the branch —
			// the creation record — is what remains.
			info.Cwd, info.Model, info.Created = r.Meta.Cwd, r.Meta.Model, r.Time
			info.Skills = r.Meta.Skills
			if r.Meta.Pinned != nil && !pinnedSet {
				info.Pinned, pinnedSet = *r.Meta.Pinned, true
			}
			if r.Meta.Title != nil && customTitle == "" {
				customTitle = *r.Meta.Title
			}
		case r.Type == "message" && r.Message != nil:
			if r.Message.Role == llm.RoleUser {
				text := r.Message.Text()
				// The sidebar's "N 条用户消息" reads as "how many times I
				// wrote". Harness content wears the user role because that
				// is the one legal injection point: each turn's tool results
				// ride one user message (visible() filters those, the same
				// rule the rewind ordinals and the timeline apply), and
				// background tasks and the todo-finish reminder mark their
				// notices. None of it is a send.
				if IsHarnessNotice(text) || !visible(r.Message) {
					break
				}
				info.Messages++
				if text != "" {
					texts = append(texts, text)
					derivedTitle = UserInputTitle(*r.Message)
				} else if at := UserInputTitle(*r.Message); at != "" {
					// A send that was only files still said something: the
					// sidebar shows the first layer's names rather than an
					// empty-session row for a transcript with real content.
					derivedTitle = at
					texts = append(texts, at)
				}
			} else if r.Message.Role == llm.RoleAssistant && r.Message.Text() != "" {
				// Only what the model said out loud reaches the searchable
				// text. Tool traffic and thinking are noise to a "which
				// session discussed X" query and dwarf the prose in size.
				texts = append(texts, r.Message.Text())
			}
		}
	}
	// An empty custom title means the rename was reverted; the derived one
	// comes back.
	if customTitle != "" {
		info.Title = customTitle
	} else if derivedTitle != "" {
		info.Title = Title(derivedTitle)
	}

	// Chronological order, so the index's text is stable across rebuilds
	// regardless of which direction the walk happened to run in. The cap
	// applies to the same window in both forms: a text that never reached the
	// concatenation never reaches the segments either.
	s := summary{info: info}
	var b strings.Builder
	for i := len(texts) - 1; i >= 0; i-- {
		if b.Len() >= maxIndexTextRunes {
			break
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(texts[i])
		s.texts = append(s.texts, texts[i])
	}
	s.text = truncateRunes(b.String(), maxIndexTextRunes)
	return s
}

// truncateRunes cuts s to at most n runes without splitting one.
func truncateRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// emptyGrace is how long a message-less session must have sat before
// CleanEmpty may remove it. A younger file may belong to a process that just
// created it — terminal and web modes share DefaultDir — and deleting it
// would cost that session its meta record when the first message lands.
const emptyGrace = time.Minute

// CleanEmpty removes session files that hold no messages and reports how many
// were removed. The web UI writes the file when the session is created, not
// when the first prompt is sent, so every abandoned "new session" click
// accumulates in the sidebar as an unnamed empty row; sweeping them at
// startup keeps the listing to sessions that actually happened.
//
// Removal is deliberately conservative, because the file is the only copy:
// a pinned session stays (someone marked it on purpose), a file with an
// unreadable line stays (damage cannot prove emptiness), and a file younger
// than emptyGrace stays (its first message may be on its way).
func CleanEmpty(dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	removed := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		empty, pinned, err := provablyEmpty(path)
		if err != nil || !empty || pinned {
			continue
		}
		if info, err := e.Info(); err != nil || time.Since(info.ModTime()) < emptyGrace {
			continue
		}
		if err := os.Remove(path); err == nil {
			removed++
		}
	}
	return removed, nil
}

// provablyEmpty reports whether a session file parses cleanly and holds no
// message records, plus whether it is pinned — the two reasons CleanEmpty
// spares an otherwise empty file.
func provablyEmpty(path string) (empty, pinned bool, err error) {
	f, err := os.Open(path)
	if err != nil {
		return false, false, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var r Record
		if json.Unmarshal([]byte(line), &r) != nil {
			// An unreadable line may be a message; damage is not emptiness.
			return false, false, nil
		}
		switch {
		case r.Type == "message" && r.Message != nil:
			return false, false, nil
		case r.Type == "meta" && r.Meta != nil && r.Meta.Pinned != nil:
			pinned = *r.Meta.Pinned
		}
	}
	if sc.Err() != nil {
		return false, false, nil
	}
	return true, pinned, nil
}

// Recorded is how much of an agent's running totals is already on disk. Callers keep
// one and hand it to UsageDelta, which advances it.
//
// A struct rather than two loose variables because the two have to move together: a
// caller that advanced one and forgot the other would silently attribute the next
// turn's own spending to delegation, or the reverse.
type Recorded struct {
	Usage     llm.Usage
	Delegated llm.Usage
}

// UsageDelta is the usage accrued since the last recorded point, advancing that
// point as a side effect. It reports false when nothing has accrued, so a flush
// that wrote only user input does not leave a row of zeroes in the transcript.
//
// Shared by the terminal and the web server because both persist the same way — in
// bulk, when a turn settles — and a second copy of "subtract the last total" is a
// second place for the sign to be wrong.
func UsageDelta(rec *Recorded, total, delegated llm.Usage) (Stats, bool) {
	d := diff(total, rec.Usage)
	dd := diff(delegated, rec.Delegated)
	rec.Usage, rec.Delegated = total, delegated
	if zero(d) {
		return Stats{}, false
	}
	st := Stats{Usage: usageOf(d)}
	// Omitted rather than written as zeroes when nothing was delegated, which is most
	// turns: a field that is always present says nothing by being present.
	if !zero(dd) {
		st.Delegated = usageOf(dd)
	}
	return st, true
}

func diff(now, then llm.Usage) llm.Usage {
	return llm.Usage{
		Input:     now.Input - then.Input,
		Output:    now.Output - then.Output,
		CacheRead: now.CacheRead - then.CacheRead,
		Reasoning: now.Reasoning - then.Reasoning,
	}
}

func zero(u llm.Usage) bool {
	return u.Input == 0 && u.Output == 0 && u.CacheRead == 0 && u.Reasoning == 0
}

func usageOf(u llm.Usage) *UsageStats {
	return &UsageStats{
		Input: u.Input, Output: u.Output, CacheRead: u.CacheRead, Reasoning: u.Reasoning,
	}
}
