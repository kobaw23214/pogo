package tools

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/yosukeno/pogo/mcpobserve"
)

const (
	ShellTasksName      = "shell_tasks"
	ShellOutputName     = "shell_output"
	ShellStopName       = "shell_stop"
	maxShellJobs        = 4
	maxShellRecordBytes = 128 << 10
	maxShellTailBytes   = 8 << 10
)

var (
	ErrShellJobNotFound  = errors.New("unknown shell task in this session")
	ErrShellJobConflict  = errors.New("shell task state conflicts with this request")
	ErrShellStopTimeout  = errors.New("shell task stop or close timed out")
	shellShutdownTimeout = 5 * time.Second
)

// ShellJobRecord is a host-owned observation. Task identifiers never grant
// access outside the runner's private owner/session directory.
type ShellJobRecord struct {
	TaskID           string `json:"task_id"`
	Command          string `json:"command"`
	Workdir          string `json:"workdir"`
	Description      string `json:"description,omitempty"`
	Status           string `json:"status"`
	StartedAt        int64  `json:"started_at"`
	EndedAt          int64  `json:"ended_at,omitempty"`
	ExitCode         *int   `json:"exit_code,omitempty"`
	Error            string `json:"error,omitempty"`
	OutputTail       string `json:"output_tail"`
	OutputBytes      int64  `json:"output_bytes"`
	OutputTruncated  bool   `json:"output_truncated"`
	Revision         int64  `json:"revision"`
	Background       bool   `json:"background"`
	PersistenceError string `json:"persistence_error,omitempty"`
}

type ShellJobOutput struct {
	TaskID     string `json:"task_id"`
	Output     string `json:"output"`
	Offset     int64  `json:"offset"`
	NextOffset int64  `json:"next_offset"`
	TotalBytes int64  `json:"total_bytes"`
	Truncated  bool   `json:"truncated"`
	Status     string `json:"status"`
	EOF        bool   `json:"eof"`
}

// Hooks are serialized per runner, occur only for background jobs, and run
// outside its state lock. A completion is durable before OnComplete is called.
type ShellJobHooks struct {
	OnStart    func(ShellJobRecord)
	OnOutput   func(ShellJobRecord)
	OnComplete func(ShellJobRecord)
}

type ShellJobOptions struct {
	Scheduler *ShellScheduler
	Owner     string
	Session   string
	Retention ShellRetentionPolicy
}

type ShellRetentionPolicy struct {
	MaxTasks int
	MaxBytes int64
	MaxAge   time.Duration
}

type shellJobDisk struct {
	ShellJobRecord
	Version             int  `json:"version"`
	NotificationPending bool `json:"notification_pending,omitempty"`
}

type shellJob struct {
	disk       shellJobDisk
	log        *os.File
	cancel     context.CancelFunc
	done       chan struct{}
	result     Result
	err        error
	stopReason string
	writeErr   error
	lastUpdate time.Time
	savedBytes int64
	observer   func(Partial)
	observerMu sync.Mutex
	tail       []byte
}

// ShellJobRunner owns processes, bounded logs and durable completion receipts.
// Its context belongs to the session, never to the turn that dispatches a job.
type ShellJobRunner struct {
	ctx            context.Context
	cancel         context.CancelFunc
	root           *os.Root
	lease          *os.File
	hooks          ShellJobHooks
	retention      ShellRetentionPolicy
	scheduler      *ShellScheduler
	owner          string
	session        string
	mu             sync.Mutex
	jobs           map[string]*shellJob
	closed         bool
	wg             sync.WaitGroup
	hookMu         sync.Mutex
	closeDone      chan struct{}
	closeStoreOnce sync.Once
}

func NewShellJobRunner(ctx context.Context, storeDir string, hooks ShellJobHooks, options ...ShellJobOptions) (*ShellJobRunner, error) {
	if strings.TrimSpace(storeDir) == "" {
		return nil, errors.New("shell task storage is unavailable")
	}
	if err := os.MkdirAll(storeDir, 0700); err != nil {
		return nil, err
	}
	root, err := openResultStoreRoot(storeDir)
	if err != nil {
		return nil, err
	}
	lease, err := trySubagentContextLease(filepath.Join(storeDir, ".runner.lock"))
	if err != nil {
		root.Close()
		return nil, fmt.Errorf("shell task storage is already in use or cannot be locked: %w", err)
	}
	jobCtx, cancel := context.WithCancel(ctx)
	scheduler := NewShellScheduler(ShellSchedulerConfig{})
	retention := ShellRetentionPolicy{MaxTasks: 64, MaxBytes: 256 << 20, MaxAge: 30 * 24 * time.Hour}
	owner, session := "", ""
	if len(options) > 0 {
		if options[0].Scheduler != nil {
			scheduler = options[0].Scheduler
		}
		owner, session = options[0].Owner, options[0].Session
		if options[0].Retention.MaxTasks > 0 {
			retention.MaxTasks = options[0].Retention.MaxTasks
		}
		if options[0].Retention.MaxBytes > 0 {
			retention.MaxBytes = options[0].Retention.MaxBytes
		}
		if options[0].Retention.MaxAge > 0 {
			retention.MaxAge = options[0].Retention.MaxAge
		}
	}
	r := &ShellJobRunner{ctx: jobCtx, cancel: cancel, root: root, lease: lease, hooks: hooks, retention: retention, scheduler: scheduler, owner: owner, session: session, jobs: make(map[string]*shellJob), closeDone: make(chan struct{})}
	if err := r.recover(); err != nil {
		cancel()
		lease.Close()
		root.Close()
		return nil, err
	}
	return r, nil
}

func validShellJobID(id string) bool {
	if len(id) != len("shell_")+32 || !strings.HasPrefix(id, "shell_") || id != strings.ToLower(id) {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(id, "shell_"))
	return err == nil
}

func (r *ShellJobRunner) recover() error {
	dir, err := r.root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(-1)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		id := strings.TrimSuffix(entry.Name(), ".json")
		if !strings.HasSuffix(entry.Name(), ".json") || !validShellJobID(id) {
			continue
		}
		f, err := scopedOpen(r.root, entry.Name(), false)
		if err != nil {
			return err
		}
		data, err := io.ReadAll(io.LimitReader(f, maxShellRecordBytes+1))
		f.Close()
		var disk shellJobDisk
		if err != nil || len(data) > maxShellRecordBytes || json.Unmarshal(data, &disk) != nil || disk.Version != 1 || disk.TaskID != id {
			return errors.New("invalid shell task record")
		}
		if disk.OutputBytes < 0 || disk.OutputBytes > MaxCommandOutputBytes || disk.Revision < 1 || len(disk.OutputTail) > maxShellTailBytes*3 {
			return errors.New("invalid shell task output metadata")
		}
		if disk.Status == "running" || disk.Status == "queued" {
			// Rebuild observations from the bounded regular log, including output
			// produced since the last periodic metadata flush. No process is adopted.
			if log, openErr := scopedOpen(r.root, id+".log", false); openErr == nil {
				info, statErr := log.Stat()
				if statErr != nil || info.Size() > MaxCommandOutputBytes {
					log.Close()
					return errors.New("invalid shell task log")
				}
				disk.OutputBytes = info.Size()
				start := max(int64(0), disk.OutputBytes-int64(maxShellTailBytes))
				tail := make([]byte, disk.OutputBytes-start)
				_, readErr := log.ReadAt(tail, start)
				log.Close()
				if readErr != nil && readErr != io.EOF {
					return readErr
				}
				disk.OutputTail = shellOutputTail(tail, true)
				disk.OutputTruncated = start > 0
			}
			disk.Status = "interrupted"
			disk.Error = "The previous host stopped before completion was recorded. Inspect existing output and effects before retrying; the command has not been rerun."
			disk.EndedAt = time.Now().UnixMilli()
			disk.Revision++
			disk.NotificationPending = disk.Background
			if err := r.save(disk); err != nil {
				return err
			}
		}
		done := make(chan struct{})
		close(done)
		r.jobs[id] = &shellJob{disk: disk, done: done}
	}
	return r.prune(time.Now())
}

func (r *ShellJobRunner) prune(now time.Time) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.pruneLocked(now)
}

func (r *ShellJobRunner) pruneLocked(now time.Time) error {
	type candidate struct {
		id    string
		ended int64
		bytes int64
	}
	items := []candidate{}
	var total int64
	for id, job := range r.jobs {
		if job.disk.Status == "running" || job.disk.Status == "queued" || job.disk.NotificationPending || job.disk.EndedAt <= 0 {
			continue
		}
		items = append(items, candidate{id: id, ended: job.disk.EndedAt, bytes: job.disk.OutputBytes})
		total += job.disk.OutputBytes
	}
	sort.Slice(items, func(i, j int) bool {
		return items[i].ended < items[j].ended || (items[i].ended == items[j].ended && items[i].id < items[j].id)
	})
	remove := func(id string) error {
		if err := r.root.Remove(id + ".json"); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := r.root.Remove(id + ".log"); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		delete(r.jobs, id)
		return nil
	}
	remaining := items[:0]
	for _, item := range items {
		if r.retention.MaxAge > 0 && now.Sub(time.UnixMilli(item.ended)) > r.retention.MaxAge {
			if err := remove(item.id); err != nil {
				return err
			}
			total -= item.bytes
			continue
		}
		remaining = append(remaining, item)
	}
	for r.retention.MaxTasks > 0 && len(remaining) > r.retention.MaxTasks {
		if err := remove(remaining[0].id); err != nil {
			return err
		}
		total -= remaining[0].bytes
		remaining = remaining[1:]
	}
	for r.retention.MaxBytes > 0 && total > r.retention.MaxBytes && len(remaining) > 0 {
		if err := remove(remaining[0].id); err != nil {
			return err
		}
		total -= remaining[0].bytes
		remaining = remaining[1:]
	}
	return nil
}

func (r *ShellJobRunner) save(disk shellJobDisk) error {
	data, err := json.Marshal(disk)
	if err != nil || len(data) > maxShellRecordBytes {
		return errors.New("shell task record exceeds the storage limit")
	}
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return err
	}
	name := ".partial-" + hex.EncodeToString(nonce[:])
	f, err := r.root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer r.root.Remove(name)
	n, err := f.Write(data)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	if err == nil {
		err = f.Sync()
	}
	cerr := f.Close()
	if err != nil {
		return err
	}
	if cerr != nil {
		return cerr
	}
	if err = r.root.Rename(name, disk.TaskID+".json"); err != nil {
		return err
	}
	dir, err := r.root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func cloneShellRecord(record ShellJobRecord) ShellJobRecord {
	if record.ExitCode != nil {
		code := *record.ExitCode
		record.ExitCode = &code
	}
	return record
}

func shellOutputTail(raw []byte, terminal bool) string {
	for len(raw) > 0 && !utf8.RuneStart(raw[0]) {
		raw = raw[1:]
	}
	if !terminal {
		for pos := 0; pos < len(raw); {
			if !utf8.FullRune(raw[pos:]) {
				raw = raw[:pos]
				break
			}
			_, size := utf8.DecodeRune(raw[pos:])
			pos += size
		}
	}
	return strings.ToValidUTF8(string(raw), "\uFFFD")
}

func (j *shellJob) detachObserver() { j.observerMu.Lock(); j.observer = nil; j.observerMu.Unlock() }

func (r *ShellJobRunner) List(activeOnly bool) []ShellJobRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]ShellJobRecord, 0, len(r.jobs))
	for _, job := range r.jobs {
		if !activeOnly || job.disk.Status == "running" || job.disk.Status == "queued" {
			out = append(out, cloneShellRecord(job.disk.ShellJobRecord))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt < out[j].StartedAt })
	return out
}

func (r *ShellJobRunner) Get(id string) (ShellJobRecord, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	job, ok := r.jobs[id]
	if !validShellJobID(id) || !ok {
		return ShellJobRecord{}, ErrShellJobNotFound
	}
	return cloneShellRecord(job.disk.ShellJobRecord), nil
}

func (r *ShellJobRunner) InFlight() int { r.mu.Lock(); defer r.mu.Unlock(); return r.inFlightLocked() }
func (r *ShellJobRunner) inFlightLocked() int {
	n := 0
	for _, j := range r.jobs {
		if j.disk.Status == "running" {
			n++
		}
	}
	return n
}

func (r *ShellJobRunner) Output(id string, offset int64, limit int) (ShellJobOutput, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	job, ok := r.jobs[id]
	if !validShellJobID(id) || !ok {
		return ShellJobOutput{}, ErrShellJobNotFound
	}
	if r.closed {
		return ShellJobOutput{}, errors.New("shell task runner is closed")
	}
	if offset < 0 {
		return ShellJobOutput{}, errors.New("offset must be nonnegative")
	}
	if limit <= 0 {
		limit = 16 << 10
	}
	if limit > MaxBytes {
		limit = MaxBytes
	}
	f, err := scopedOpen(r.root, id+".log", false)
	if err != nil {
		return ShellJobOutput{}, errors.New("shell task output is unavailable")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return ShellJobOutput{}, err
	}
	total := info.Size()
	if total > MaxCommandOutputBytes {
		return ShellJobOutput{}, errors.New("shell task output exceeds its storage limit")
	}
	if offset > total {
		offset = total
	}
	if offset > 0 && offset < total {
		start := max(int64(0), offset-utf8.UTFMax+1)
		window := make([]byte, min(int64(2*utf8.UTFMax), total-start))
		n, readErr := f.ReadAt(window, start)
		if readErr != nil && readErr != io.EOF {
			return ShellJobOutput{}, readErr
		}
		window = window[:n]
		boundary := int(offset - start)
		for pos := 0; pos < boundary; pos++ {
			_, size := utf8.DecodeRune(window[pos:])
			if size > 1 && pos+size > boundary {
				return ShellJobOutput{}, errors.New("offset is inside a UTF-8 character; use the returned next_offset")
			}
		}
	}
	buf := make([]byte, min(int64(limit+utf8.UTFMax-1), total-offset))
	n, err := f.ReadAt(buf, offset)
	if err != nil && err != io.EOF {
		return ShellJobOutput{}, err
	}
	buf = buf[:n]
	end := min(limit, n)
	for pos := 0; pos < end; {
		if !utf8.FullRune(buf[pos:]) {
			if job.disk.Status != "running" && offset+int64(n) == total {
				pos++
				continue
			}
			end = pos
			break
		}
		_, size := utf8.DecodeRune(buf[pos:])
		if pos+size > end {
			if pos == 0 {
				end = pos + size
			} else {
				end = pos
			}
			break
		}
		pos += size
	}
	buf = buf[:end]
	return ShellJobOutput{TaskID: id, Output: strings.ToValidUTF8(string(buf), "\uFFFD"), Offset: offset, NextOffset: offset + int64(end), TotalBytes: total, Truncated: offset+int64(end) < total, Status: job.disk.Status, EOF: offset+int64(end) >= total && job.disk.Status != "running"}, nil
}

func (r *ShellJobRunner) Stop(id, reason string) error {
	return r.StopContext(context.Background(), id, reason)
}

func (r *ShellJobRunner) StopContext(ctx context.Context, id, reason string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.Lock()
	job, ok := r.jobs[id]
	if !validShellJobID(id) || !ok {
		r.mu.Unlock()
		return ErrShellJobNotFound
	}
	if job.disk.Status != "running" && job.disk.Status != "queued" {
		r.mu.Unlock()
		return nil
	}
	if reason == "" {
		reason = "Stopped by shell_stop"
	}
	job.stopReason = boundResultText(reason, 2048, 16)
	job.cancel()
	r.mu.Unlock()
	timer := time.NewTimer(shellShutdownTimeout)
	defer timer.Stop()
	select {
	case <-job.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ErrShellStopTimeout
	}
}

func (r *ShellJobRunner) Close() {
	_ = r.CloseContext(context.Background())
}

func (r *ShellJobRunner) CloseContext(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		select {
		case <-r.closeDone:
			return nil
		default:
		}
	} else {
		r.closed = true
		r.cancel()
		r.mu.Unlock()
	}
	done := make(chan struct{})
	go func() {
		r.wg.Wait()
		close(done)
	}()
	timer := time.NewTimer(shellShutdownTimeout)
	defer timer.Stop()
	select {
	case <-done:
		r.closeStoreOnce.Do(func() {
			_ = r.root.Close()
			_ = r.lease.Close()
			close(r.closeDone)
		})
		return nil
	case <-ctx.Done():
		go func() {
			<-done
			r.closeStoreOnce.Do(func() {
				_ = r.root.Close()
				_ = r.lease.Close()
				close(r.closeDone)
			})
		}()
		return ctx.Err()
	case <-timer.C:
		go func() {
			<-done
			r.closeStoreOnce.Do(func() {
				_ = r.root.Close()
				_ = r.lease.Close()
				close(r.closeDone)
			})
		}()
		return ErrShellStopTimeout
	}
}

func (r *ShellJobRunner) PendingNotifications() []ShellJobRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := []ShellJobRecord{}
	for _, j := range r.jobs {
		if j.disk.NotificationPending && j.disk.Background {
			out = append(out, cloneShellRecord(j.disk.ShellJobRecord))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].EndedAt < out[j].EndedAt })
	return out
}
func (r *ShellJobRunner) AckNotification(id string, revision int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	j, ok := r.jobs[id]
	if !ok || !validShellJobID(id) {
		return ErrShellJobNotFound
	}
	if j.disk.Revision != revision {
		return fmt.Errorf("%w: notification revision changed", ErrShellJobConflict)
	}
	if !j.disk.NotificationPending {
		return nil
	}
	next := j.disk
	next.NotificationPending = false
	if err := r.save(next); err != nil {
		return err
	}
	j.disk = next
	return r.pruneLocked(time.Now())
}

func (r *ShellJobRunner) hook(fn func(ShellJobRecord), record ShellJobRecord) {
	if fn != nil {
		fn(cloneShellRecord(record))
	}
}

// dispatch starts exactly one process; yielding changes only ownership of its
// observation, never the command, process or hard execution deadline.
func (r *ShellJobRunner) dispatch(ctx context.Context, t *Bash, args bashArgs, onPartial func(Partial)) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	r.mu.Lock()
	if r.closed || r.ctx.Err() != nil {
		r.mu.Unlock()
		return Result{}, errors.New("shell task runner is closed")
	}
	if r.inFlightLocked() >= maxShellJobs {
		r.mu.Unlock()
		return Result{}, fmt.Errorf("at most %d shell tasks may run concurrently; wait for an existing task", maxShellJobs)
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		r.mu.Unlock()
		return Result{}, err
	}
	id := "shell_" + hex.EncodeToString(nonce[:])
	log, err := r.root.OpenFile(id+".log", os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		r.mu.Unlock()
		return Result{}, err
	}
	jobCtx, cancel := context.WithCancel(mcpobserve.WithMetadata(r.ctx, mcpobserve.MetadataFrom(ctx)))
	job := &shellJob{disk: shellJobDisk{Version: 1, ShellJobRecord: ShellJobRecord{TaskID: id, Command: args.Command, Workdir: args.Workdir, Description: boundResultText(args.Description, 512, 4), Status: "queued", StartedAt: time.Now().UnixMilli(), Revision: 1, Background: args.RunInBackground}}, log: log, cancel: cancel, done: make(chan struct{}), observer: onPartial}
	if args.RunInBackground {
		job.observer = nil
	}
	if err := r.save(job.disk); err != nil {
		cancel()
		log.Close()
		r.root.Remove(id + ".log")
		r.mu.Unlock()
		return Result{}, err
	}
	r.jobs[id] = job
	r.wg.Add(1)
	record := job.disk.ShellJobRecord
	r.mu.Unlock()
	go r.start(jobCtx, t, args, job)
	if args.RunInBackground {
		return shellJobReceipt(record), nil
	}
	timer := time.NewTimer(time.Duration(args.YieldAfter * float64(time.Second)))
	defer timer.Stop()
	select {
	case <-job.done:
		job.detachObserver()
		r.mu.Lock()
		res, err := job.result, job.err
		r.mu.Unlock()
		return res, err
	case <-ctx.Done():
		job.detachObserver()
		r.mu.Lock()
		job.stopReason = "Command aborted before background handoff"
		job.cancel()
		r.mu.Unlock()
		<-job.done
		r.mu.Lock()
		res, err := job.result, job.err
		r.mu.Unlock()
		return res, err
	case <-timer.C:
		job.detachObserver()
		r.hookMu.Lock()
		r.mu.Lock()
		if job.disk.Status != "running" {
			res, err := job.result, job.err
			r.mu.Unlock()
			r.hookMu.Unlock()
			return res, err
		}
		if cancelErr := ctx.Err(); cancelErr != nil {
			job.stopReason = "Command aborted before background handoff"
			job.cancel()
			r.mu.Unlock()
			r.hookMu.Unlock()
			<-job.done
			r.mu.Lock()
			res, runErr := job.result, job.err
			r.mu.Unlock()
			return res, errors.Join(cancelErr, runErr)
		}
		next := job.disk
		next.Background = true
		next.Revision++
		if err := r.save(next); err != nil {
			job.stopReason = "Background handoff could not be saved"
			job.cancel()
			r.mu.Unlock()
			r.hookMu.Unlock()
			<-job.done
			r.mu.Lock()
			res, runErr := job.result, job.err
			r.mu.Unlock()
			return res, errors.Join(err, runErr)
		}
		job.disk = next
		record := next.ShellJobRecord
		r.mu.Unlock()
		r.hook(r.hooks.OnStart, record)
		r.hookMu.Unlock()
		return shellJobReceipt(record), nil
	}
}

func (r *ShellJobRunner) start(ctx context.Context, t *Bash, args bashArgs, job *shellJob) {
	release, err := r.scheduler.acquire(ctx, r.owner, r.session)
	if err != nil {
		r.startFailed(ctx, job, err)
		return
	}
	defer release()
	r.hookMu.Lock()
	if ctx.Err() != nil {
		r.hookMu.Unlock()
		r.startFailed(ctx, job, ctx.Err())
		return
	}
	r.mu.Lock()
	next := job.disk
	next.Status = "running"
	next.Revision++
	if err := r.save(next); err != nil {
		r.mu.Unlock()
		r.hookMu.Unlock()
		r.startFailed(ctx, job, err)
		return
	}
	job.disk = next
	record := next.ShellJobRecord
	r.mu.Unlock()
	r.hookMu.Unlock()
	if record.Background {
		r.hook(r.hooks.OnStart, record)
	}
	r.run(ctx, t, args, job)
}

func (r *ShellJobRunner) startFailed(ctx context.Context, job *shellJob, cause error) {
	defer r.wg.Done()
	defer job.cancel()
	r.hookMu.Lock()
	defer r.hookMu.Unlock()
	r.mu.Lock()
	if job.log != nil {
		_ = job.log.Sync()
		_ = job.log.Close()
		job.log = nil
	}
	next := job.disk
	next.Status = "failed"
	if ctx.Err() != nil || job.stopReason != "" {
		next.Status = "cancelled"
	}
	next.EndedAt = time.Now().UnixMilli()
	next.Revision++
	next.Error = boundResultText(cause.Error(), 2048, 16)
	if job.stopReason != "" {
		next.Error = job.stopReason
	}
	next.NotificationPending = next.Background
	job.err = cause
	if err := r.save(next); err != nil {
		next.Status = "failed"
		next.NotificationPending = false
		next.PersistenceError = err.Error()
		job.err = errors.Join(cause, err)
	}
	job.disk = next
	close(job.done)
	record := next.ShellJobRecord
	r.mu.Unlock()
	if record.Background {
		r.hook(r.hooks.OnComplete, record)
	}
}

func shellJobReceipt(record ShellJobRecord) Result {
	data, _ := json.Marshal(record)
	state := "running in the background"
	if record.Status == "queued" {
		state = "queued for execution"
	}
	text := fmt.Sprintf("Shell task %s is %s. This is an execution receipt, not a successful command result. Continue independent work; completion will be reported automatically. Use shell_output to inspect it or shell_stop to cancel it.", record.TaskID, state)
	return Result{Text: text, Details: BashDetails{Command: record.Command, Workdir: record.Workdir, TaskID: record.TaskID, Background: true, Status: record.Status}, Payload: &ResultPayload{Text: text, Structured: data, Format: "json"}}
}

func (r *ShellJobRunner) run(ctx context.Context, t *Bash, args bashArgs, job *shellJob) {
	defer r.wg.Done()
	defer job.cancel()
	res, err := t.executeCommand(ctx, args, func(p Partial) {
		job.observerMu.Lock()
		defer job.observerMu.Unlock()
		if job.observer != nil {
			job.observer(p)
		}
	}, &shellJobWriter{runner: r, job: job})
	job.detachObserver()
	r.hookMu.Lock()
	defer r.hookMu.Unlock()
	r.mu.Lock()
	if syncErr := job.log.Sync(); syncErr != nil && job.writeErr == nil {
		job.writeErr = syncErr
	}
	if closeErr := job.log.Close(); closeErr != nil && job.writeErr == nil {
		job.writeErr = closeErr
	}
	job.log = nil
	job.result, job.err = res, err
	next := job.disk
	next.OutputTail = shellOutputTail(job.tail, true)
	next.EndedAt = time.Now().UnixMilli()
	next.Revision++
	next.Status = "completed"
	if d, ok := res.Details.(BashDetails); ok {
		code := d.ExitCode
		next.ExitCode = &code
		if d.TimedOut {
			next.Status = "timed_out"
		}
	}
	if err != nil && next.Status != "timed_out" {
		next.Status = "failed"
	}
	if ctx.Err() != nil {
		next.Status = "cancelled"
	}
	if job.stopReason != "" {
		next.Status = "cancelled"
		next.Error = job.stopReason
	} else if err != nil {
		next.Error = boundResultText(err.Error(), 2048, 16)
	}
	if job.writeErr != nil {
		next.Status = "failed"
		next.Error = "Shell output could not be saved: " + job.writeErr.Error()
		job.err = errors.Join(err, job.writeErr)
	}
	next.NotificationPending = next.Background
	saveErr := r.save(next)
	if saveErr != nil {
		next.Status = "failed"
		next.NotificationPending = false
		next.PersistenceError = saveErr.Error()
		next.Error = boundResultText(next.Error+"; completion persistence failed: "+saveErr.Error(), 2048, 16)
		job.err = errors.Join(job.err, saveErr)
	}
	job.disk = next
	close(job.done)
	record := next.ShellJobRecord
	r.mu.Unlock()
	if record.Background {
		r.hook(r.hooks.OnComplete, record)
	}
}

type shellJobWriter struct {
	runner *ShellJobRunner
	job    *shellJob
}

func (w *shellJobWriter) Write(p []byte) (int, error) {
	r, j := w.runner, w.job
	r.hookMu.Lock()
	defer r.hookMu.Unlock()
	r.mu.Lock()
	remaining := MaxCommandOutputBytes - j.disk.OutputBytes
	part := p
	if int64(len(part)) > remaining {
		part = part[:max(remaining, 0)]
	}
	n, err := j.log.Write(part)
	j.disk.OutputBytes += int64(n)
	j.tail = append(j.tail, part[:n]...)
	if len(j.tail) > maxShellTailBytes {
		j.tail = append([]byte(nil), j.tail[len(j.tail)-maxShellTailBytes:]...)
	}
	j.disk.OutputTail = shellOutputTail(j.tail, false)
	j.disk.OutputTruncated = j.disk.OutputBytes > int64(len(j.tail))
	if err == nil && n != len(part) {
		err = io.ErrShortWrite
	}
	if err == nil && len(part) != len(p) {
		err = ErrCommandOutputLimit
		j.disk.OutputTruncated = true
	}
	if err != nil {
		j.writeErr = err
		j.cancel()
	}
	announce := j.disk.Background && (j.savedBytes == 0 || j.disk.OutputBytes-j.savedBytes >= 256<<10 || time.Since(j.lastUpdate) >= time.Second)
	if announce {
		j.disk.Revision++
		if saveErr := r.save(j.disk); saveErr != nil {
			j.writeErr = saveErr
			j.cancel()
			err = saveErr
		}
		j.savedBytes = j.disk.OutputBytes
		j.lastUpdate = time.Now()
	}
	record := j.disk.ShellJobRecord
	r.mu.Unlock()
	if announce {
		r.hook(r.hooks.OnOutput, record)
	}
	return n, err
}

func hostShellJobName(name string) bool {
	return name == ShellTasksName || name == ShellOutputName || name == ShellStopName
}
