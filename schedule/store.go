package schedule

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/inputref"
	"hash/fnv"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// DirEnv relocates the schedule directory, for tests and for deployments that
// keep state elsewhere. Same shape as POGO_SESSION_DIR, POGO_MEMORY_DIR and
// POGO_SPILL_DIR.
const DirEnv = "POGO_SCHEDULE_DIR"

// Job kinds. A cron job carries a cron expression; an every job a fixed
// interval; an at job one wall-clock time and disables itself after it runs.
const (
	KindCron  = "cron"
	KindEvery = "every"
	KindAt    = "at"
)

// LastRunStatus values. Empty means the job has never run.
const (
	StatusOK      = "ok"
	StatusError   = "error"
	StatusSkipped = "skipped"
)

// Trigger values on a run record: how the run came to be.
const (
	TriggerScheduled = "scheduled"
	TriggerManual    = "manual"
)

// MaxConsecutiveFailures is how many runs in a row may fail before the job
// disables itself. An unattended job that always fails is a bill, not a
// schedule; ten is enough rope for a transient outage and not enough for a
// broken prompt to spend meaningfully.
const MaxConsecutiveFailures = 10

// DefaultDir is where schedule state lives when nothing is configured:
// jobs.json, the per-job run history, the logs the backends point at, and the
// lock files.
func DefaultDir() string {
	if d := strings.TrimSpace(os.Getenv(DirEnv)); d != "" {
		return d
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".pogo", "schedule")
	}
	return filepath.Join(os.TempDir(), "pogo", "schedule")
}

// Job is one scheduled task. The zero-value-safe fields are the ones a user
// leaves alone: legacy jobs inherit CLI defaults. Agent jobs resolve the current
// trusted Agent definition in the web runtime; they never use a CLI fallback.
type Job struct {
	ScheduleFrom *time.Time      `json:"schedule_from,omitempty"`
	Title        string          `json:"title,omitempty"`
	AgentID      string          `json:"agent_id,omitempty"`
	Owner        *identity.Scope `json:"owner,omitempty"`
	Parts        []inputref.Part `json:"parts,omitempty"`
	InputLayout  string          `json:"input_layout,omitempty"`
	Timezone     string          `json:"timezone,omitempty"`
	Revision     int64           `json:"revision,omitempty"`
	LastError    string          `json:"last_error,omitempty"`
	LastSummary  string          `json:"last_summary,omitempty"`
	ID           string          `json:"id"`
	Prompt       string          `json:"prompt"`
	Workdir      string          `json:"workdir"`
	// WorkflowID only identifies a retired platform-workflow job on disk.
	// Keep the marker and its hash representation so an old entry can never
	// become an ordinary prompt job. These entries are not exposed or executed.
	WorkflowID string `json:"workflow_id,omitempty"`
	Kind       string `json:"kind"` // KindCron, KindEvery, KindAt
	// Cron is the expression as the user wrote it, macros included. Parsed
	// fresh wherever it is needed; storing the text keeps jobs.json readable.
	Cron     string `json:"cron,omitempty"`
	EverySec int64  `json:"every_sec,omitempty"`
	// At is an RFC3339 timestamp for one-shot jobs.
	At            string    `json:"at,omitempty"`
	MaxTurns      int       `json:"max_turns,omitempty"`
	TurnLimitMode string    `json:"turn_limit_mode,omitempty"`
	ApprovalMode  string    `json:"approval_mode,omitempty"`
	BudgetUSD     float64   `json:"budget_usd,omitempty"`
	Model         string    `json:"model,omitempty"`
	Enabled       bool      `json:"enabled"`
	CreatedAt     time.Time `json:"created_at"`
	LastRunAt     time.Time `json:"last_run_at,omitempty"`
	// LastQueuedAt is the durable timer admission watermark. It is separate
	// from execution history: cancellation must consume only this occurrence,
	// and a crash after admission must not create another logical trigger.
	LastQueuedAt  time.Time `json:"last_queued_at,omitempty"`
	LastRunStatus string    `json:"last_run_status,omitempty"`
	// LastRunDurationSec is wall clock for the whole child run, not model time.
	LastRunDurationSec  float64 `json:"last_run_duration_sec,omitempty"`
	ConsecutiveFailures int     `json:"consecutive_failures,omitempty"`
	// SessionID is the session the last run wrote, empty until the first run —
	// the transcript handle for "what did it actually do".
	SessionID string `json:"session_id,omitempty"`
	// ExpiresAt is an RFC3339 deadline after which the job refuses to fire and
	// disables itself. Empty means never — the pre-hardening behaviour, kept
	// for jobs that existed before the field did. An unattended, self-refiring
	// agent with no sunset is a persistence mechanism, which is why the field
	// exists at all; the web API's dialog defaults it.
	ExpiresAt string `json:"expires_at,omitempty"`
	// DefHash is DefinitionHash's output recorded at creation time: the
	// creating process's statement of what the job is allowed to be. RunOnce
	// recomputes it and refuses to fire on a mismatch, so jobs.json edited out
	// of band — by another agent, or by anything that reached the store
	// directory — changes what fires into a visible refusal instead. Its trust
	// is exactly the store directory's writability: with the directory outside
	// the sandbox's writable set (the hardened deployment), a sandboxed
	// command can neither forge nor alter it. Empty means a legacy job, which
	// RunOnce blesses once by computing and persisting, so upgrades do not
	// break existing schedules.
	DefHash string `json:"def_hash,omitempty"`
}

// RunRecord is one line of a job's run history.
type RunRecord struct {
	Summary       string    `json:"summary,omitempty"`
	Model         string    `json:"model,omitempty"`
	AgentRevision int       `json:"agent_revision,omitempty"`
	TS            time.Time `json:"ts"`
	Trigger       string    `json:"trigger"` // TriggerScheduled or TriggerManual
	SessionID     string    `json:"session_id,omitempty"`
	Status        string    `json:"status"` // StatusOK, StatusError, StatusSkipped
	DurationSec   float64   `json:"duration_sec"`
	EndReason     string    `json:"end_reason,omitempty"`
	// CostUSD has no source today: the wire contract carries token usage, not
	// money, and inventing a number here would read back as metering. Kept for
	// the day the stream reports one; omitted until then.
	CostUSD float64 `json:"cost_usd,omitempty"`
	Error   string  `json:"error,omitempty"`
}

// NewID derives an 8-hex-digit job id from the job's content plus a
// timestamp, so two identical `add` invocations still get distinct ids.
func NewID(seed ...string) string {
	h := fnv.New32a()
	for _, p := range seed {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return fmt.Sprintf("%08x", h.Sum32())
}

// DefinitionHash is the tamper-evident seal over the fields that decide what a
// run does — everything a job file edit could turn into a different, still
// valid-looking job. State fields (Enabled, LastRun*, ConsecutiveFailures,
// SessionID, CreatedAt) are deliberately out: they change in the ordinary
// course of running, and sealing them would make the job refuse itself.
//
// JSON over a fixed struct rather than over the raw file bytes, so
// reformatting jobs.json by hand does not read as tampering.
func DefinitionHash(j Job) string {
	canon := struct {
		Prompt        string          `json:"prompt"`
		Workdir       string          `json:"workdir"`
		WorkflowID    string          `json:"workflow_id,omitempty"`
		Kind          string          `json:"kind"`
		Cron          string          `json:"cron,omitempty"`
		EverySec      int64           `json:"every_sec,omitempty"`
		At            string          `json:"at,omitempty"`
		MaxTurns      int             `json:"max_turns,omitempty"`
		BudgetUSD     float64         `json:"budget_usd,omitempty"`
		Model         string          `json:"model,omitempty"`
		ExpiresAt     string          `json:"expires_at,omitempty"`
		Title         string          `json:"title,omitempty"`
		AgentID       string          `json:"agent_id,omitempty"`
		Owner         *identity.Scope `json:"owner,omitempty"`
		Parts         []inputref.Part `json:"parts,omitempty"`
		InputLayout   string          `json:"input_layout,omitempty"`
		Timezone      string          `json:"timezone,omitempty"`
		ScheduleFrom  *time.Time      `json:"schedule_from,omitempty"`
		TurnLimitMode string          `json:"turn_limit_mode,omitempty"`
		ApprovalMode  string          `json:"approval_mode,omitempty"`
	}{j.Prompt, j.Workdir, j.WorkflowID, j.Kind, j.Cron, j.EverySec, j.At, j.MaxTurns, j.BudgetUSD, j.Model, j.ExpiresAt, j.Title, j.AgentID, j.Owner, j.Parts, j.InputLayout, j.Timezone, j.ScheduleFrom, j.TurnLimitMode, j.ApprovalMode}
	data, _ := json.Marshal(canon)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Store is the on-disk state under one schedule directory. A process-wide
// mutex serialises read-modify-write cycles within the process; between
// processes, writers are rare (add/remove/enable run interactively) and
// -schedule-exec serialises itself per job with the run lock.
type Store struct {
	dir string
	mu  *sync.Mutex
}

var storeMutexes sync.Map

func NewStore(dir string) *Store {
	key, err := filepath.Abs(dir)
	if err != nil {
		key = filepath.Clean(dir)
	}
	mu, _ := storeMutexes.LoadOrStore(key, &sync.Mutex{})
	return &Store{dir: dir, mu: mu.(*sync.Mutex)}
}

// Dir is the directory the store was opened on.
func (s *Store) Dir() string { return s.dir }

func (s *Store) jobsPath() string { return filepath.Join(s.dir, "jobs.json") }
func (s *Store) lockPath(id string) string {
	return filepath.Join(s.dir, "locks", id+".lock")
}
func (s *Store) runsPath(id string) string {
	return filepath.Join(s.dir, "runs", id+".jsonl")
}

// OutLogPath / ErrLogPath are the files the backends point StandardOutPath /
// StandardErrorPath (launchd), StandardOutput (systemd) and the crontab
// redirect at, and that the executor itself appends the child's output to.
func (s *Store) OutLogPath(id string) string {
	return filepath.Join(s.dir, "logs", id+".out.log")
}

func (s *Store) ErrLogPath(id string) string {
	return filepath.Join(s.dir, "logs", id+".err.log")
}

// Load reads jobs.json. A missing file is an empty list — that is the state
// before the first `add`, not an error. A corrupt file *is* an error: the
// alternative is silently treating "I have jobs" as "I have none", which is
// how a schedule quietly stops running.
func (s *Store) Load() ([]Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	jobs, err := s.loadLocked()
	if err != nil {
		return nil, err
	}
	supported := jobs[:0]
	for _, job := range jobs {
		if job.WorkflowID == "" {
			supported = append(supported, job)
		}
	}
	return supported, nil
}

func (s *Store) loadLocked() ([]Job, error) {
	raw, err := os.ReadFile(s.jobsPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var jobs []Job
	if err := json.Unmarshal(raw, &jobs); err != nil {
		return nil, fmt.Errorf("%s is corrupt: %w", s.jobsPath(), err)
	}
	return jobs, nil
}

// saveLocked writes jobs.json atomically (temp file plus rename, after
// config.Save), 0600: the file holds prompts, and prompts can hold anything.
func (s *Store) saveLocked(jobs []Job) error {
	data, err := json.MarshalIndent(jobs, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return writeFileAtomic(s.jobsPath(), data, 0o600)
}

// Add appends a job, refusing a duplicate id. The job is sealed here — the
// hash is (re)computed rather than taken from the caller, so the value on
// disk is always this process's statement of what it created, never something
// smuggled in alongside it.
func (s *Store) Add(j Job) error {
	if err := ValidateAgentRunOptions(j); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	jobs, err := s.loadLocked()
	if err != nil {
		return err
	}
	for _, existing := range jobs {
		if existing.ID == j.ID {
			return fmt.Errorf("a scheduled job named %q already exists", j.ID)
		}
	}
	// IDs are also the private history/log namespace. Reusing a deleted ID
	// would give a different account (or binding) its predecessor's records.
	if !JobNameShape.MatchString(j.ID) {
		return fmt.Errorf("invalid scheduled job id")
	}
	for _, retained := range []string{s.runsPath(j.ID), s.OutLogPath(j.ID), s.ErrLogPath(j.ID)} {
		if _, err := os.Lstat(retained); err == nil {
			return fmt.Errorf("scheduled job id %q already exists in retained history; choose a new name", j.ID)
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	if j.AgentID != "" && j.Revision == 0 {
		j.Revision = 1
	}
	j.DefHash = DefinitionHash(j)
	return s.saveLocked(append(jobs, j))
}

// Remove deletes a job by id. Run history and logs are kept on purpose: the
// job is gone, the record of what it did is not something a remove should
// erase.
func (s *Store) Remove(id string) error { return s.removeDefinition(id, true, nil) }

// removeUnregistered is AddJob's rollback only: a failed registration may
// remove its own untouched, never-run definition, never a subsequent edit.
func (s *Store) removeUnregistered(job Job) error {
	return s.removeDefinition(job.ID, false, &job)
}
func (s *Store) removeDefinition(id string, reserve bool, expected *Job) error {
	if !reserve {
		if !JobNameShape.MatchString(id) {
			return fmt.Errorf("invalid scheduled job id")
		}
		if err := os.MkdirAll(filepath.Join(s.dir, "locks"), 0700); err != nil {
			return err
		}
		f, err := os.OpenFile(s.lockPath(id), os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return err
		}
		defer f.Close()
		if err = lockFile(f.Fd()); err != nil {
			return ErrRunning
		}
		defer unlockFile(f.Fd())
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	jobs, err := s.loadLocked()
	if err != nil {
		return err
	}
	for i, j := range jobs {
		if j.ID != id {
			continue
		}
		if !JobNameShape.MatchString(id) {
			return fmt.Errorf("invalid scheduled job id")
		}
		if reserve {
			if err := s.reserveHistoryID(id); err != nil {
				return err
			}
		} else {
			revision := expected.Revision
			if expected.AgentID != "" && revision == 0 {
				revision = 1
			}
			if j.DefHash != DefinitionHash(*expected) || j.Revision != revision || j.Enabled != expected.Enabled || !j.CreatedAt.Equal(expected.CreatedAt) || !j.LastRunAt.IsZero() {
				return fmt.Errorf("job changed or ran during registration; retained for inspection")
			}
			for _, p := range []string{s.runsPath(id), s.OutLogPath(id), s.ErrLogPath(id)} {
				if _, e := os.Lstat(p); e == nil {
					return fmt.Errorf("job has execution records; retained for inspection")
				} else if !os.IsNotExist(e) {
					return e
				}
			}
		}
		return s.saveLocked(append(jobs[:i], jobs[i+1:]...))
	}
	return fmt.Errorf("no scheduled job %q", id)
}
func (s *Store) reserveHistoryID(id string) error {
	p := s.runsPath(id)
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if os.IsExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	syncErr := f.Sync()
	closeErr := f.Close()
	if syncErr != nil {
		return syncErr
	}
	if closeErr != nil {
		return closeErr
	}
	// Empty history reserves a namespace; it is deliberately not a fabricated run.
	dir, err := os.Open(filepath.Dir(p))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// Find returns the job with the given id.
func (s *Store) Find(id string) (Job, error) {
	jobs, err := s.Load()
	if err != nil {
		return Job{}, err
	}
	for _, j := range jobs {
		if j.ID == id {
			return j, nil
		}
	}
	return Job{}, fmt.Errorf("no scheduled job %q", id)
}

// Update applies fn to the named job and saves, all under the mutex.
func (s *Store) Update(id string, fn func(*Job)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	jobs, err := s.loadLocked()
	if err != nil {
		return err
	}
	for i := range jobs {
		if jobs[i].ID == id {
			if jobs[i].WorkflowID != "" {
				return ErrRetiredWorkflow
			}
			fn(&jobs[i])
			if jobs[i].WorkflowID != "" {
				return ErrRetiredWorkflow
			}
			return s.saveLocked(jobs)
		}
	}
	return fmt.Errorf("no scheduled job %q", id)
}

const (
	// maxRunHistoryBytes is when a job's history file gets truncated...
	maxRunHistoryBytes = 2 << 20 // 2 MB
	// ...down to its newest keepRunLines lines.
	keepRunLines = 2000
)

// AppendRun adds one record to runs/<id>.jsonl, truncating the file to its
// newest lines once it passes the size cap.
func (s *Store) AppendRun(id string, r RunRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := s.runsPath(id)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	werr := json.NewEncoder(f).Encode(r)
	cerr := f.Close()
	if werr != nil {
		return werr
	}
	if cerr != nil {
		return cerr
	}

	st, err := os.Stat(path)
	if err != nil || st.Size() <= maxRunHistoryBytes {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	lines := bytes.Split(bytes.TrimRight(data, "\n"), []byte("\n"))
	if len(lines) > keepRunLines {
		lines = lines[len(lines)-keepRunLines:]
	}
	return writeFileAtomic(path, append(bytes.Join(lines, []byte("\n")), '\n'), 0o600)
}

// Runs returns the newest n records of a job's history, oldest first. n <= 0
// means all of them. A missing history is an empty list.
func (s *Store) Runs(id string, n int) ([]RunRecord, error) {
	data, err := os.ReadFile(s.runsPath(id))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []RunRecord
	for _, line := range bytes.Split(data, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var r RunRecord
		if err := json.Unmarshal(line, &r); err != nil {
			// A torn last line (kill mid-append) is skipped; earlier damage
			// is reported, because it should not be there.
			if len(out) > 0 || len(bytes.Split(data, []byte("\n"))) > 1 {
				continue
			}
			return nil, fmt.Errorf("%s is corrupt: %w", s.runsPath(id), err)
		}
		out = append(out, r)
	}
	if n > 0 && len(out) > n {
		out = out[len(out)-n:]
	}
	return out, nil
}

// writeFileAtomic writes data to path via a temp file in the same directory
// plus rename, so a reader never sees a half-written file. Falls back to a
// direct write when the rename is refused (bind-mounted single file), like
// config.Save does.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err == nil {
		tmpName := tmp.Name()
		if _, werr := tmp.Write(data); werr == nil {
			if cerr := tmp.Close(); cerr == nil {
				if rerr := os.Rename(tmpName, path); rerr == nil {
					return nil
				}
			}
		}
		tmp.Close()
		os.Remove(tmpName)
	}
	out, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, perm)
	if err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if _, err := out.Write(data); err != nil {
		out.Close()
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return out.Close()
}

var ErrConflict = errors.New("scheduled job changed; reload before editing")
var ErrRunning = errors.New("scheduled job is running")

type DefinitionOptions struct {
	Enabled *bool
	Install func(Job, Job) error
}

// ReplaceDefinition edits a definition under both the run lease and the shared
// store mutex. Execution state is retained; owner and Agent cannot be reassigned.
func (s *Store) ReplaceDefinition(id string, revision int64, next Job, options ...DefinitionOptions) (Job, error) {
	if err := ValidateAgentRunOptions(next); err != nil {
		return Job{}, err
	}
	var option DefinitionOptions
	if len(options) > 0 {
		option = options[0]
	}
	if !JobNameShape.MatchString(id) {
		return Job{}, fmt.Errorf("invalid job id")
	}
	if err := os.MkdirAll(filepath.Join(s.dir, "locks"), 0700); err != nil {
		return Job{}, err
	}
	f, err := os.OpenFile(s.lockPath(id), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return Job{}, err
	}
	defer f.Close()
	if err := lockFile(f.Fd()); err != nil {
		return Job{}, ErrRunning
	}
	defer unlockFile(f.Fd())
	s.mu.Lock()
	defer s.mu.Unlock()
	jobs, err := s.loadLocked()
	if err != nil {
		return Job{}, err
	}
	for i, old := range jobs {
		if old.ID != id {
			continue
		}
		if old.Revision != revision {
			return Job{}, ErrConflict
		}
		if old.AgentID != next.AgentID || old.WorkflowID != next.WorkflowID || !sameOwner(old.Owner, next.Owner) {
			return Job{}, fmt.Errorf("job binding cannot change")
		}
		next.ID, next.CreatedAt, next.Enabled = old.ID, old.CreatedAt, old.Enabled
		if option.Enabled != nil {
			next.Enabled = *option.Enabled
		}
		next.Revision = old.Revision + 1
		if old.Kind == next.Kind && old.Cron == next.Cron && old.EverySec == next.EverySec && old.At == next.At && old.Timezone == next.Timezone {
			next.ScheduleFrom = old.ScheduleFrom
		}
		next.LastRunAt, next.LastRunStatus, next.LastRunDurationSec = old.LastRunAt, old.LastRunStatus, old.LastRunDurationSec
		next.ConsecutiveFailures, next.SessionID = old.ConsecutiveFailures, old.SessionID
		next.LastError, next.LastSummary = old.LastError, old.LastSummary
		next.DefHash = DefinitionHash(next)
		if option.Install != nil {
			if err := option.Install(old, next); err != nil {
				return Job{}, err
			}
		}
		jobs[i] = next
		if err := s.saveLocked(jobs); err != nil {
			if option.Install != nil {
				if restoreErr := option.Install(next, old); restoreErr != nil {
					return Job{}, errors.Join(err, fmt.Errorf("restore original trigger: %w", restoreErr))
				}
			}
			return Job{}, err
		}
		return next, nil
	}
	return Job{}, fmt.Errorf("no scheduled job %q", id)
}
func sameOwner(a, b *identity.Scope) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
