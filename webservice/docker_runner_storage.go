package webservice

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	defaultRuntimeBytesLimit   int64 = 1 << 30
	defaultDataBytesLimit      int64 = 512 << 20
	defaultOwnerDataBytesLimit int64 = 2 << 30
	storageScanTimeout               = 12 * time.Second
	storagePhaseTimeout              = 4 * time.Second
	storageSnapshotStaleAfter        = 2 * time.Minute
)

func normalizeStorageLimits(o *DockerRunnerOptions) error {
	for _, limit := range []struct {
		value   *int64
		maximum int64
	}{{&o.RuntimeBytesLimit, defaultRuntimeBytesLimit}, {&o.DataBytesLimit, defaultDataBytesLimit}, {&o.OwnerDataBytesLimit, defaultOwnerDataBytesLimit}} {
		if *limit.value == 0 {
			*limit.value = limit.maximum
		}
		if *limit.value < 1 || *limit.value > limit.maximum {
			return Fail("INVALID_RUNNER_CONFIG", "storage limits must be positive and cannot exceed the default host budgets")
		}
	}
	return nil
}

func storageFailure(e *Error) bool {
	return e != nil && e.Code == "STORAGE_LIMIT_EXCEEDED"
}

func storageWarning(e *Error) bool { return e != nil && e.Code == "STORAGE_CHECK_UNAVAILABLE" }

// Each input is a verified nonnegative byte count. Saturation preserves a
// lower bound that is already above every supported budget, without wrapping
// a confirmed excess into an unavailable-check warning.
func storageAdd(a, b int64) int64 {
	const maximum = int64(^uint64(0) >> 1)
	if a > maximum-b {
		return maximum
	}
	return a + b
}

func storageCheckError(phase string, err error) *Error {
	issue := storageUnavailable()
	issue.Phase = phase
	// Docker errors on this path describe the trusted storage operation, never
	// an application request body or authentication header.
	log.Printf("web storage check failed: phase=%s error=%v", phase, err)
	return issue
}

func storageUnavailable() *Error {
	return &Error{Code: "STORAGE_CHECK_UNAVAILABLE", Message: "managed storage usage could not be verified", Retryable: true}
}

func (r *DockerRunner) savedLaunch(rec Record) (Record, bool, error) {
	data, err := os.ReadFile(r.launchPath(rec))
	if errors.Is(err, os.ErrNotExist) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, err
	}
	var saved Record
	if len(data) > 16<<20 || json.Unmarshal(data, &saved) != nil || r.validate(saved) != nil || !matchLabels(r.labels(saved, true), r.labels(rec, true)) {
		return Record{}, false, Fail("PERMISSION_DENIED", "runner metadata identity mismatch")
	}
	return saved, true, nil
}

// Retained data is keyed by service, not generation. Old launch tombstones
// remain useful after their logs/spec have expired and must still count here.
func (r *DockerRunner) storageRecords(owner string) ([]Record, error) {
	entries, err := os.ReadDir(r.opts.StateDir)
	if err != nil {
		return nil, err
	}
	var records []Record
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".launch.json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(r.opts.StateDir, entry.Name()))
		if err != nil {
			return nil, err
		}
		var rec Record
		if len(data) > 16<<20 || json.Unmarshal(data, &rec) != nil || r.validate(rec) != nil || filepath.Base(r.launchPath(rec)) != entry.Name() {
			return nil, Fail("PERMISSION_DENIED", "invalid retained storage identity")
		}
		if owner == "" || rec.Owner == owner {
			records = append(records, rec)
		}
	}
	sort.Slice(records, func(i, j int) bool { return r.launchPath(records[i]) < r.launchPath(records[j]) })
	return records, nil
}

type storageMount struct {
	name, target string
	labels       map[string]string
}

// readMeterOutput accepts only Docker's bounded, non-TTY stdout stream. Neither
// runtime output nor application-provided JSON participates in this protocol.
func readMeterOutput(input io.Reader) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(input, 64<<10))
	if err != nil || len(raw) >= 64<<10 {
		return nil, storageUnavailable()
	}
	var stdout bytes.Buffer
	for len(raw) > 0 {
		if len(raw) < 8 || raw[0] != 1 || raw[1] != 0 || raw[2] != 0 || raw[3] != 0 {
			return nil, storageUnavailable()
		}
		n := int(binary.BigEndian.Uint32(raw[4:8]))
		if n > len(raw)-8 {
			return nil, storageUnavailable()
		}
		stdout.Write(raw[8 : 8+n])
		raw = raw[8+n:]
	}
	return stdout.Bytes(), nil
}

func meterValues(data []byte, count int) ([]int64, error) {
	var value struct {
		Bytes []int64 `json:"bytes"`
	}
	if json.Unmarshal(data, &value) != nil || len(value.Bytes) != count {
		return nil, storageUnavailable()
	}
	for _, size := range value.Bytes {
		if size < 0 {
			return nil, storageUnavailable()
		}
	}
	return value.Bytes, nil
}

func storageScannerException(data []byte) string {
	var detail struct {
		Exception string `json:"exception"`
	}
	if json.Unmarshal(data, &detail) == nil && regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,80}$`).MatchString(detail.Exception) {
		return detail.Exception
	}
	return "unknown"
}

func meterConfig(paths []string) string {
	data, _ := json.Marshal(map[string]any{"paths": paths, "timeout_ms": 2500, "max_entries": 200000})
	return string(data)
}

type storageDeletedFile struct {
	Root   string `json:"root"`
	Device uint64 `json:"device"`
	Inode  uint64 `json:"inode"`
	Bytes  int64  `json:"bytes"`
}
type runtimeStorageMeter struct {
	Bytes   []int64              `json:"bytes"`
	Deleted []storageDeletedFile `json:"deleted"`
}

func runtimeMeterConfig() string {
	data, _ := json.Marshal(map[string]any{"paths": []string{"/tmp"}, "timeout_ms": 2500, "max_entries": 200000, "include_deleted": true, "max_deleted": 256})
	return string(data)
}

func (r *DockerRunner) measureTmp(ctx context.Context, rec Record) (runtimeStorageMeter, error) {
	var result runtimeStorageMeter
	name, _, _, _, _ := r.names(rec)
	// Verify the full runtime identity immediately before the administrator exec.
	c, err := r.inspect(ctx, rec)
	if err != nil || !c.State.Running {
		if err == nil {
			err = errors.New("runtime is not running")
		}
		return result, storageCheckError("runtime_inspect", err)
	}
	var created struct {
		ID string `json:"Id"`
	}
	command := []string{r.supervisorPython(rec), "-I", "-c", pythonStorageMeter, runtimeMeterConfig()}
	if err := r.request(ctx, "POST", "/containers/"+name+"/exec", map[string]any{"User": "0:0", "Cmd": command, "AttachStdout": true, "AttachStderr": true}, &created); err != nil {
		return result, storageCheckError("runtime_exec_create", err)
	}
	body := bytes.NewBufferString(`{"Detach":false,"Tty":false}`)
	res, err := r.raw(ctx, "POST", "/exec/"+url.PathEscape(created.ID)+"/start", body, "application/json")
	if err != nil {
		return result, storageCheckError("runtime_exec_start", err)
	}
	data, err := readMeterOutput(res.Body)
	res.Body.Close()
	if err != nil {
		return result, storageCheckError("runtime_exec_output", err)
	}
	var state struct {
		Running  bool
		ExitCode int
	}
	if err := r.request(ctx, "GET", "/exec/"+url.PathEscape(created.ID)+"/json", nil, &state); err != nil || state.Running || state.ExitCode != 0 {
		if err == nil {
			err = fmt.Errorf("running=%t exit_code=%d scanner_exception=%s", state.Running, state.ExitCode, storageScannerException(data))
		}
		return result, storageCheckError("runtime_exec_exit", err)
	}
	values, err := meterValues(data, 1)
	if err != nil {
		return result, storageCheckError("runtime_exec_decode", err)
	}
	if json.Unmarshal(data, &result) != nil || len(result.Deleted) > 256 {
		return result, storageUnavailable()
	}
	result.Bytes = values
	for _, file := range result.Deleted {
		if file.Bytes < 0 || file.Inode == 0 || (file.Root != "data" && file.Root != "workspace" && file.Root != "tmp") {
			return runtimeStorageMeter{}, storageUnavailable()
		}
	}
	return result, nil
}

func (r *DockerRunner) storageMeterName(rec Record) string {
	name, _, _, _, _ := r.names(rec)
	return name + "-meter"
}

func (r *DockerRunner) cleanupStorageMeter(ctx context.Context, rec Record, name string) error {
	if !strings.HasPrefix(name, r.storageMeterName(rec)+"-") || !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(strings.TrimPrefix(name, r.storageMeterName(rec)+"-")) {
		return Fail("PERMISSION_DENIED", "invalid storage meter name")
	}
	var state dockerContainer
	err := r.request(ctx, "GET", "/containers/"+name+"/json", nil, &state)
	if statusIs(err, 404) {
		return nil
	}
	if err != nil {
		return err
	}
	labels := r.labels(rec, true)
	labels["pogo.web.role"] = "storage-meter"
	if !matchLabels(state.Config.Labels, labels) {
		return Fail("PERMISSION_DENIED", "storage meter identity mismatch")
	}
	if state.State.Running {
		if err := r.request(ctx, "POST", "/containers/"+name+"/stop?t=1", nil, nil); err != nil && !statusIs(err, 304) && !statusIs(err, 404) {
			return err
		}
		if err := r.request(ctx, "GET", "/containers/"+name+"/json", nil, &state); err != nil {
			if statusIs(err, 404) {
				return nil
			}
			return err
		}
		if state.State.Running {
			return Fail("STOP_UNCONFIRMED", "storage meter is still running")
		}
	}
	err = r.request(ctx, "DELETE", "/containers/"+name+"?v=false", nil, nil)
	if statusIs(err, 404) {
		return nil
	}
	return err
}

func (r *DockerRunner) measureVolumes(ctx context.Context, anchor Record, mounts []storageMount) (values []int64, resultErr error) {
	if len(mounts) == 0 {
		return nil, nil
	}
	if len(mounts) > 256 {
		return nil, storageUnavailable()
	}
	name := r.storageMeterName(anchor) + "-" + randomID("")
	originalCount := len(mounts)
	var selected []storageMount
	var indexes []int
	r.storageMu.Lock()
	for i, mount := range mounts {
		// Stop installs this fence only after the generation's writer is
		// confirmed absent. A stale scan may still contain its old workspace;
		// account it as zero without mounting or recreating a deleted path.
		if r.storageRetired[mount.name] {
			continue
		}
		indexes = append(indexes, i)
		selected = append(selected, mount)
		r.storageVolumes[mount.name]++
		r.storageMeterVolumes[name] = append(r.storageMeterVolumes[name], mount.name)
	}
	if len(selected) == 0 {
		r.storageMu.Unlock()
		return make([]int64, originalCount), nil
	}
	r.storageMeters[name] = true
	r.storageMu.Unlock()
	mounts = selected
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		cleanupErr := r.cleanupStorageMeter(cleanup, anchor, name)
		resultErr = errors.Join(resultErr, cleanupErr)
		r.storageMu.Lock()
		delete(r.storageMeters, name)
		if cleanupErr == nil {
			r.releaseStorageVolumesLocked(name)
		}
		r.storageMu.Unlock()
	}()
	labels := r.labels(anchor, true)
	labels["pogo.web.role"] = "storage-meter"
	paths := make([]string, 0, len(mounts))
	bindings := make([]any, 0, len(mounts))
	for _, mount := range mounts {
		var volume struct {
			Name, Driver, Mountpoint string
			Options                  map[string]string
			Labels                   map[string]string
		}
		if err := r.request(ctx, "GET", "/volumes/"+mount.name, nil, &volume); err != nil {
			return nil, storageCheckError("volume_inspect", err)
		}
		if volume.Name != mount.name || volume.Driver != "local" || len(volume.Options) > 0 || !matchLabels(volume.Labels, mount.labels) || !path.IsAbs(volume.Mountpoint) || path.Clean(volume.Mountpoint) != volume.Mountpoint || !strings.HasSuffix(volume.Mountpoint, "/"+mount.name+"/_data") {
			return nil, storageUnavailable()
		}
		paths = append(paths, mount.target)
		// Bind only the Engine-confirmed local volume directory. A named-volume
		// mount would silently recreate an unlabelled volume if a cancelled
		// create arrives after lifecycle cleanup. Missing bind sources fail.
		bindings = append(bindings, map[string]any{"Type": "bind", "Source": volume.Mountpoint, "Target": mount.target, "ReadOnly": true, "BindOptions": map[string]any{"CreateMountpoint": false, "NonRecursive": true, "Propagation": "rslave"}})
	}
	body := map[string]any{
		"Image": r.opts.Image, "Entrypoint": []string{r.opts.PythonPath, "-I", "-c", pythonStorageMeter, meterConfig(paths)}, "Cmd": []string{}, "User": "1000:1000", "WorkingDir": "/", "Labels": labels,
		"HostConfig": map[string]any{"NetworkMode": "none", "ReadonlyRootfs": true, "CapDrop": []string{"ALL"}, "SecurityOpt": []string{"no-new-privileges:true"}, "Memory": 128 << 20, "NanoCpus": 1_000_000_000, "PidsLimit": 16, "Mounts": bindings, "LogConfig": map[string]any{"Type": "json-file", "Config": map[string]string{"max-size": "64k", "max-file": "1"}}},
	}
	if err := r.request(ctx, "POST", "/containers/create?name="+name, body, nil); err != nil {
		return nil, storageCheckError("meter_create", err)
	}
	if err := r.request(ctx, "POST", "/containers/"+name+"/start", nil, nil); err != nil {
		return nil, storageCheckError("meter_start", err)
	}
	var state struct{ StatusCode int }
	if err := r.request(ctx, "POST", "/containers/"+name+"/wait?condition=not-running", nil, &state); err != nil {
		return nil, storageCheckError("meter_wait", err)
	}
	res, err := r.raw(ctx, "GET", "/containers/"+name+"/logs?stdout=1&stderr=1", nil, "")
	if err != nil {
		return nil, storageCheckError("meter_logs", err)
	}
	data, err := readMeterOutput(res.Body)
	res.Body.Close()
	if err != nil {
		return nil, err
	}
	if state.StatusCode != 0 {
		return nil, storageCheckError("meter_scan", fmt.Errorf("exit_code=%d scanner_exception=%s", state.StatusCode, storageScannerException(data)))
	}
	measured, err := meterValues(data, len(paths))
	if err != nil {
		return nil, err
	}
	values = make([]int64, originalCount)
	for i, value := range measured {
		values[indexes[i]] = value
	}
	return values, nil
}

type ownerStorage struct {
	records  []Record
	usage    map[string]*StorageUsage
	issues   map[string]*Error
	warnings map[string]*Error
	blocked  bool
}

// Admission excludes lifecycle changes; periodic measurements intentionally do
// not hold the lifecycle mutex, so a slow start/stop cannot starve the meter.
// Names and labels are revalidated; disappeared resources fail closed.
func (r *DockerRunner) measureOwnerLocked(ctx context.Context, records []Record) (*ownerStorage, error) {
	result := &ownerStorage{records: records, usage: map[string]*StorageUsage{}, issues: map[string]*Error{}, warnings: map[string]*Error{}}
	if len(records) == 0 {
		return result, nil
	}
	if !r.beginStorageTask() {
		return result, storageUnavailable()
	}
	defer r.storageWG.Done()
	bounded, cancel := context.WithTimeout(ctx, storageScanTimeout)
	defer cancel()
	stopCancellation := context.AfterFunc(r.storageContext, cancel)
	defer stopCancellation()
	dataNames := map[string]int{}
	workNames := map[string]int{}
	var mounts []storageMount
	var running []Record
	for _, rec := range records {
		name, _, work, _, data := r.names(rec)
		result.usage[name] = &StorageUsage{CheckedAt: time.Now().UTC()}
		if _, seen := dataNames[data]; !seen {
			if err := r.ownedResource(bounded, "/volumes/"+data, r.labels(rec, false)); err == nil {
				dataNames[data] = len(mounts)
				mounts = append(mounts, storageMount{data, fmt.Sprintf("/meter/%d", len(mounts)), r.labels(rec, false)})
			} else if !statusIs(err, 404) {
				result.warnings[name] = storageCheckError("data_volume_inspect", err)
			}
		}
		if active(rec.State) && !r.runtimeStorageRetired(work) {
			if err := r.ownedResource(bounded, "/volumes/"+work, r.labels(rec, true)); err == nil {
				workNames[work] = len(mounts)
				mounts = append(mounts, storageMount{work, fmt.Sprintf("/meter/%d", len(mounts)), r.labels(rec, true)})
			} else if !statusIs(err, 404) {
				result.warnings[name] = storageCheckError("workspace_volume_inspect", err)
			}
			c, err := r.inspect(bounded, rec)
			if err == nil && c.State.Running {
				running = append(running, rec)
			} else if err != nil && !statusIs(err, 404) {
				result.warnings[name] = storageCheckError("runtime_inspect", err)
			}
		}
	}
	var wait sync.WaitGroup
	var mu sync.Mutex
	deletedData := map[string]map[string]int64{}
	for _, rec := range running {
		wait.Add(1)
		go func(rec Record) {
			defer wait.Done()
			runtimeCtx, runtimeCancel := context.WithTimeout(bounded, storagePhaseTimeout)
			defer runtimeCancel()
			usage, err := r.measureTmp(runtimeCtx, rec)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				name, _, _, _, _ := r.names(rec)
				result.warnings[name] = storageCheckError("runtime_scan", err)
				return
			}
			name, _, _, _, _ := r.names(rec)
			result.usage[name].RuntimeBytes = usage.Bytes[0]
			_, _, _, _, data := r.names(rec)
			for _, file := range usage.Deleted {
				if file.Root == "data" {
					if deletedData[data] == nil {
						deletedData[data] = map[string]int64{}
					}
					key := fmt.Sprintf("%d:%d", file.Device, file.Inode)
					if file.Bytes > deletedData[data][key] {
						deletedData[data][key] = file.Bytes
					}
				} else {
					result.usage[name].RuntimeBytes = storageAdd(result.usage[name].RuntimeBytes, file.Bytes)
				}
			}
		}(rec)
	}
	wait.Wait()
	// Read unlinked descriptors before walking linked trees. Ordinary unlink
	// cannot make a previously unlinked inode reappear in a later tree walk.
	// A slow runtime must not consume the volume phase's entire budget and
	// make healthy sibling sites inherit its unavailable result.
	volumeCtx, volumeCancel := context.WithTimeout(bounded, storagePhaseTimeout)
	defer volumeCancel()
	values, volumeErr := r.measureVolumes(volumeCtx, records[0], mounts)
	if volumeErr != nil {
		return result, storageCheckError("volume_scan", volumeErr)
	}
	deletedBytes := map[string]int64{}
	for name, files := range deletedData {
		for _, size := range files {
			deletedBytes[name] = storageAdd(deletedBytes[name], size)
		}
	}
	var ownerBytes int64
	for data, index := range dataNames {
		values[index] = storageAdd(values[index], deletedBytes[data])
		ownerBytes = storageAdd(ownerBytes, values[index])
	}
	for _, rec := range records {
		name, _, work, _, data := r.names(rec)
		usage := result.usage[name]
		if index, ok := dataNames[data]; ok {
			usage.DataBytes = values[index]
		}
		usage.OwnerDataBytes = ownerBytes
		var workspaceBytes int64
		if index, ok := workNames[work]; ok {
			workspaceBytes = values[index]
		}
		// Successfully measured data is a confirmed lower bound even when a
		// runtime scan is incomplete. A warning must never hide known excess.
		if usage.RuntimeBytes > r.opts.RuntimeBytesLimit || workspaceBytes > r.opts.RuntimeBytesLimit || usage.DataBytes > r.opts.DataBytesLimit || ownerBytes > r.opts.OwnerDataBytesLimit {
			result.issues[name] = r.storageLimitIssue(usage, workspaceBytes)
			result.blocked = true
		}
		if result.warnings[name] != nil {
			delete(result.usage, name) // Keep that generation's last complete usage.
			continue
		}
		usage.RuntimeBytes = storageAdd(usage.RuntimeBytes, workspaceBytes)
		usage.CheckedAt = time.Now().UTC()
		if usage.RuntimeBytes > r.opts.RuntimeBytesLimit || usage.DataBytes > r.opts.DataBytesLimit || ownerBytes > r.opts.OwnerDataBytesLimit {
			result.issues[name] = r.storageLimitIssue(usage, workspaceBytes)
			result.blocked = true
		}
	}
	return result, nil
}

func (r *DockerRunner) storageLimitIssue(usage *StorageUsage, workspaceBytes int64) *Error {
	return &Error{Code: "STORAGE_LIMIT_EXCEEDED", Message: "managed storage budget exceeded; data has been retained", Phase: "storage", Details: map[string]any{
		"verified_data_bytes": usage.DataBytes, "verified_owner_data_bytes": usage.OwnerDataBytes,
		"verified_workspace_bytes": workspaceBytes, "verified_runtime_bytes": usage.RuntimeBytes, "runtime_bytes_limit": r.opts.RuntimeBytesLimit,
		"data_bytes_limit": r.opts.DataBytesLimit, "owner_data_bytes_limit": r.opts.OwnerDataBytesLimit,
	}}
}

func (r *DockerRunner) storageAdmissionLocked(ctx context.Context, owner string) error {
	records, err := r.storageRecords(owner)
	if err != nil {
		return storageUnavailable()
	}
	result, err := r.measureOwnerLocked(ctx, records)
	if err != nil {
		return err
	}
	if result.blocked {
		return Fail("STORAGE_LIMIT_EXCEEDED", "retained service storage exceeds the owner budget; clear owned data before allocating")
	}
	if len(result.warnings) != 0 {
		return storageUnavailable() // New allocations still require a complete check.
	}
	return nil
}

type storageSnapshot struct {
	Labels    map[string]string `json:"labels"`
	Usage     *StorageUsage     `json:"usage,omitempty"`
	Issue     *Error            `json:"issue,omitempty"`
	Warning   *Error            `json:"warning,omitempty"`
	CheckedAt time.Time         `json:"checked_at"`
}

// retireRuntimeStorage closes meter admission for already stopped writers.
// Registration and retirement use the same mutex, so a meter is either counted
// before the fence or cannot acquire the retired volume afterwards.
func (r *DockerRunner) retireRuntimeStorage(ctx context.Context, volumes []string) error {
	r.storageMu.Lock()
	for _, name := range volumes {
		r.storageRetired[name] = true
	}
	for {
		busy := false
		for _, name := range volumes {
			busy = busy || r.storageVolumes[name] > 0
		}
		if !busy {
			r.storageMu.Unlock()
			return ctx.Err()
		}
		changed := r.storageChanged
		r.storageMu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
		r.storageMu.Lock()
	}
}

func (r *DockerRunner) runtimeStorageRetired(name string) bool {
	r.storageMu.Lock()
	defer r.storageMu.Unlock()
	return r.storageRetired[name]
}

func (r *DockerRunner) storageVolumeBusy(name string) bool {
	r.storageMu.Lock()
	defer r.storageMu.Unlock()
	return r.storageVolumes[name] > 0
}
func (r *DockerRunner) releaseStorageVolumesLocked(name string) {
	for _, volume := range r.storageMeterVolumes[name] {
		r.storageVolumes[volume]--
		if r.storageVolumes[volume] == 0 {
			delete(r.storageVolumes, volume)
		}
	}
	delete(r.storageMeterVolumes, name)
	close(r.storageChanged)
	r.storageChanged = make(chan struct{})
}

func (r *DockerRunner) beginStorageTask() bool {
	r.storageMu.Lock()
	defer r.storageMu.Unlock()
	if r.storageClosed {
		return false
	}
	r.storageWG.Add(1)
	return true
}

func (r *DockerRunner) storagePath(rec Record) string {
	name, _, _, _, _ := r.names(rec)
	return filepath.Join(r.opts.StateDir, name+".storage.json")
}

func (r *DockerRunner) writeStorageSnapshot(rec Record, usage *StorageUsage, issue *Error) error {
	r.storageMu.Lock()
	defer r.storageMu.Unlock()
	previous, err := r.loadStorageSnapshot(rec)
	if err != nil {
		return err
	}
	if usage == nil && previous != nil {
		usage = previous.Usage
	}
	// A generation with confirmed excess cannot recover its access merely because its writer
	// has been stopped and its temporary workspace removed. A new generation is
	// required; data/owner admission is measured afresh before that allocation.
	if previous != nil && storageFailure(previous.Issue) {
		issue = previous.Issue
	}
	var warning *Error
	if storageWarning(issue) {
		warning, issue = issue, nil
		if previous != nil {
			usage = previous.Usage
		}
		if usage == nil {
			usage = rec.Storage
		}
	}
	data, err := json.Marshal(storageSnapshot{Labels: r.labels(rec, true), Usage: usage, Issue: issue, Warning: warning, CheckedAt: time.Now().UTC()})
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(r.opts.StateDir, ".storage-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(name, r.storagePath(rec)); err == nil {
		runtime, _, _, _, _ := r.names(rec)
		delete(r.storageWarnings, runtime)
	}
	return err
}

func (r *DockerRunner) loadStorageSnapshot(rec Record) (*storageSnapshot, error) {
	data, err := os.ReadFile(r.storagePath(rec))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var result storageSnapshot
	if len(data) > 64<<10 || json.Unmarshal(data, &result) != nil || !matchLabels(result.Labels, r.labels(rec, true)) {
		return nil, Fail("PERMISSION_DENIED", "storage snapshot identity mismatch")
	}
	// Older runners latched unavailable checks as fatal. They are observations,
	// not confirmed excess, and may recover after a successful measurement.
	if storageWarning(result.Issue) {
		result.Warning, result.Issue = result.Issue, nil
	}
	return &result, nil
}

// Queue enforcement separately from measurement. A long normal stop/start
// cannot prevent the next bounded pass or the Manager observing the cause.
func (r *DockerRunner) queueStorageStop(rec Record) {
	name, _, _, _, _ := r.names(rec)
	r.storageMu.Lock()
	if r.storageClosed || r.storageStops[name] {
		r.storageMu.Unlock()
		return
	}
	r.storageStops[name] = true
	r.storageWG.Add(1)
	r.storageMu.Unlock()
	go func() {
		defer r.storageWG.Done()
		defer func() { r.storageMu.Lock(); delete(r.storageStops, name); r.storageMu.Unlock() }()
		ctx, cancel := context.WithTimeout(r.storageContext, 15*time.Second)
		defer cancel()
		for !r.mu.TryLock() {
			select {
			case <-ctx.Done():
				return
			case <-time.After(10 * time.Millisecond):
			}
		}
		defer r.mu.Unlock()
		current, exists, err := r.savedLaunch(rec)
		if err != nil || !exists || !active(current.State) {
			return
		}
		snapshot, err := r.loadStorageSnapshot(current)
		if err != nil {
			return
		}
		if snapshot != nil && storageFailure(snapshot.Issue) {
			current.Storage = snapshot.Usage
			current.LastError = snapshot.Issue
		} else if storageFailure(rec.LastError) {
			current.Storage = rec.Storage
			current.LastError = rec.LastError
		} else {
			return
		}
		// A full control disk must not prevent stopping the confirmed writer.
		// Persistence failure still leaves cleanup unconfirmed; it never grants
		// permission to discard data or release the Manager's quota.
		_ = r.saveLaunch(current, true)
		_ = r.stopLocked(ctx, current)
	}()
}

func (r *DockerRunner) cleanupOrphanStorageMeters(ctx context.Context, records []Record) error {
	query, _ := json.Marshal(map[string][]string{"label": {managedLabel + "=true", "pogo.web.installation=" + r.opts.Installation, "pogo.web.role=storage-meter"}})
	var items []struct {
		Names  []string
		Labels map[string]string
	}
	if err := r.request(ctx, "GET", "/containers/json?all=1&filters="+url.QueryEscape(string(query)), nil, &items); err != nil {
		return err
	}
	known := map[string]Record{}
	for _, rec := range records {
		known[r.storageMeterName(rec)] = rec
	}
	for _, item := range items {
		if len(item.Names) != 1 {
			return Fail("PERMISSION_DENIED", "invalid storage meter identity")
		}
		name := strings.TrimPrefix(item.Names[0], "/")
		r.storageMu.Lock()
		inUse := r.storageMeters[name]
		r.storageMu.Unlock()
		if inUse {
			continue
		}
		index := strings.LastIndex(name, "-")
		if index < 0 {
			return Fail("PERMISSION_DENIED", "invalid storage meter identity")
		}
		rec, ok := known[name[:index]]
		if !ok || !matchLabels(item.Labels, r.labels(rec, true)) {
			return Fail("PERMISSION_DENIED", "unrecognized storage meter identity")
		}
		if err := r.cleanupStorageMeter(ctx, rec, name); err != nil {
			return err
		}
		r.storageMu.Lock()
		r.releaseStorageVolumesLocked(name)
		r.storageMu.Unlock()
	}
	return nil
}

// ReconcileStorage is an independent, bounded host pass. The CLI calls it at
// periodically, separately from lease cleanup. All active owners are
// measured concurrently; each includes every retained data slot for that owner.
func (r *DockerRunner) ReconcileStorage(ctx context.Context) error {
	if !r.storagePassMu.TryLock() {
		return nil
	}
	defer r.storagePassMu.Unlock()
	if !r.beginStorageTask() {
		return storageUnavailable()
	}
	defer r.storageWG.Done()
	records, err := r.storageRecords("")
	if err != nil {
		return err
	}
	bounded, cancel := context.WithTimeout(ctx, storageScanTimeout)
	defer cancel()
	stopCancellation := context.AfterFunc(r.storageContext, cancel)
	defer stopCancellation()
	if err := r.cleanupOrphanStorageMeters(bounded, records); err != nil {
		warning := storageCheckError("meter_cleanup", err)
		for _, rec := range records {
			if active(rec.State) {
				_ = r.writeStorageSnapshot(rec, nil, warning)
			}
		}
		return err
	}
	activeOwners := map[string]bool{}
	for _, rec := range records {
		if active(rec.State) {
			activeOwners[rec.Owner] = true
		}
	}
	owners := map[string][]Record{}
	for _, rec := range records {
		if activeOwners[rec.Owner] {
			owners[rec.Owner] = append(owners[rec.Owner], rec)
		}
	}
	var failures []error
	var wait sync.WaitGroup
	var mu sync.Mutex
	for _, owned := range owners {
		wait.Add(1)
		go func(owned []Record) {
			defer wait.Done()
			result, scanErr := r.measureOwnerLocked(bounded, owned)
			if scanErr != nil {
				storageCheckError("owner_scan", scanErr)
				mu.Lock()
				failures = append(failures, scanErr)
				mu.Unlock()
			}
			if len(result.warnings) > 0 {
				mu.Lock()
				failures = append(failures, storageUnavailable())
				mu.Unlock()
			}
			for _, rec := range owned {
				name, _, _, _, _ := r.names(rec)
				issue := result.issues[name]
				if issue == nil {
					issue = result.warnings[name]
				}
				if scanErr != nil {
					issue = storageUnavailable()
				}
				// Avoid failing a generation whose Start has not created a writer yet.
				if issue != nil && active(rec.State) {
					c, inspectErr := r.inspect(ctx, rec)
					if statusIs(inspectErr, 404) || inspectErr == nil && !c.State.Running {
						if saved, _, _ := r.savedLaunch(rec); !storageFailure(saved.LastError) {
							issue = nil
						}
					}
				}
				var usage *StorageUsage
				if scanErr == nil {
					usage = result.usage[name]
				}
				if err := r.writeStorageSnapshot(rec, usage, issue); err != nil {
					warning := storageCheckError("snapshot_write", err)
					r.storageMu.Lock()
					r.storageWarnings[name] = warning
					r.storageMu.Unlock()
					mu.Lock()
					failures = append(failures, err)
					mu.Unlock()
					if active(rec.State) && storageFailure(issue) {
						rec.LastError = issue
						rec.Storage = usage
						r.queueStorageStop(rec)
					}
					continue
				}
				if storageFailure(issue) && active(rec.State) {
					rec.LastError = issue
					rec.Storage = usage
					r.queueStorageStop(rec)
				}
			}
		}(owned)
	}
	wait.Wait()
	return errors.Join(failures...)
}

// The scanner executes only from the pinned, read-only interpreter image. It
// uses directory file descriptors and never follows a source symlink, including
// a directory replaced during traversal. Sparse files are conservatively
// charged by the larger of apparent and allocated bytes; hardlinks count once.
const pythonStorageMeter = `
import errno, json, os, stat, sys, time
try:
    cfg = json.loads(sys.argv[1])
    if os.getuid() == 0:
        # In the runtime, preserve real/saved root identity against application
        # ptrace/signals, but inspect files as their UID 1000 owner. Separate
        # network-none helpers already start as UID 1000 without capabilities.
        os.setgroups([])
        os.setegid(1000)
        os.seteuid(1000)
    deadline = time.monotonic() + min(2500, int(cfg['timeout_ms'])) / 1000
    remaining = min(200000, int(cfg['max_entries']))
    def charge(info):
        return max(info.st_size, getattr(info, 'st_blocks', 0) * 512)
    def check():
        global remaining
        remaining -= 1
        if remaining < 0 or time.monotonic() > deadline:
            raise RuntimeError('bounded scan incomplete')
    def scan(path):
        seen = set()
        def visit(fd, depth):
            check()
            if depth > 128:
                raise RuntimeError('tree too deep')
            info = os.fstat(fd)
            identity = (info.st_dev, info.st_ino)
            if identity in seen:
                return 0
            seen.add(identity)
            total = charge(info)
            with os.scandir(fd) as entries:
                for entry in entries:
                    check()
                    info = os.stat(entry.name, dir_fd=fd, follow_symlinks=False)
                    identity = (info.st_dev, info.st_ino)
                    if identity in seen:
                        continue
                    if stat.S_ISDIR(info.st_mode):
                        child = os.open(entry.name, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=fd)
                        try:
                            actual = os.fstat(child)
                            if (actual.st_dev, actual.st_ino) != identity:
                                raise RuntimeError('directory changed')
                            total += visit(child, depth + 1)
                        finally:
                            os.close(child)
                    else:
                        seen.add(identity)
                        total += charge(info)
            return total
        fd = os.open(path, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
        try:
            return visit(fd, 0)
        finally:
            os.close(fd)
    deleted = []
    def deleted_open_files():
        roots = {}
        with open('/proc/self/mountinfo') as stream:
            for line in stream:
                check()
                fields = line.split()
                if fields[4] in ('/workspace', '/data', '/tmp'):
                    roots[int(fields[0])] = fields[4][1:]
        if len(roots) != 3:
            raise RuntimeError('managed mounts unavailable')
        own_namespace = os.readlink('/proc/self/ns/mnt')
        seen = set()
        def missing(error):
            return isinstance(error, OSError) and error.errno in (errno.ENOENT, errno.ESRCH)
        def pinned_mount(fd):
            previous = os.geteuid()
            if os.getuid() == 0:
                os.seteuid(0)
            try:
                with open('/proc/self/fdinfo/' + str(fd)) as stream:
                    for line in stream:
                        if line.startswith('mnt_id:'):
                            return int(line.split(':', 1)[1].strip())
                raise RuntimeError('descriptor mount unavailable')
            finally:
                if os.getuid() == 0:
                    os.seteuid(previous)
        def account(fd, managed=False):
            info = os.fstat(fd)
            identity = (info.st_dev, info.st_ino)
            if not stat.S_ISREG(info.st_mode) or info.st_nlink != 0 or identity in seen:
                return
            root = roots.get(pinned_mount(fd))
            if root is None:
                if managed:
                    raise RuntimeError('managed mapping mount unavailable')
                return
            if len(deleted) >= min(256, int(cfg.get('max_deleted', 256))):
                raise RuntimeError('too many deleted files')
            seen.add(identity)
            deleted.append({'root': root, 'device': info.st_dev, 'inode': info.st_ino, 'bytes': charge(info)})
        def mapping_remains(task, address, identity):
            with open(task + '/maps') as stream:
                for line in stream:
                    check()
                    fields = line.split(None, 5)
                    if len(fields) >= 5 and fields[0] == address:
                        device = fields[3].split(':')
                        current = (os.makedev(int(device[0], 16), int(device[1], 16)), int(fields[4]))
                        if current == identity:
                            return True
            return False
        with os.scandir('/proc') as processes:
            for process in processes:
                check()
                if not process.name.isdecimal():
                    continue
                base = '/proc/' + process.name
                try:
                    app = False
                    with open(base + '/status') as stream:
                        for line in stream:
                            check()
                            if line.startswith('Uid:'):
                                app = int(line.split()[1]) == 1000
                                break
                    if not app:
                        continue
                    if os.readlink(base + '/ns/mnt') != own_namespace:
                        raise RuntimeError('application mount namespace changed')
                    with os.scandir(base + '/task') as threads:
                        for thread in threads:
                            check()
                            # Linux exposes map_files at /proc/TID, but not in
                            # /proc/PID/task/TID. Both paths expose maps/fd.
                            task = '/proc/' + thread.name
                            try:
                                with os.scandir(task + '/fd') as entries:
                                    for entry in entries:
                                        check()
                                        try:
                                            fd = os.open(task + '/fd/' + entry.name, os.O_PATH | os.O_CLOEXEC)
                                            try:
                                                account(fd)
                                            finally:
                                                os.close(fd)
                                        except OSError as error:
                                            if not missing(error):
                                                raise
                                with open(task + '/maps') as stream:
                                    for line in stream:
                                        check()
                                        fields = line.split(None, 5)
                                        if len(fields) < 6 or not fields[5].rstrip().endswith(' (deleted)'):
                                            continue
                                        source = fields[5]
                                        if not source.startswith(('/workspace/', '/data/', '/tmp/')):
                                            continue
                                        device = fields[3].split(':')
                                        identity = (os.makedev(int(device[0], 16), int(device[1], 16)), int(fields[4]))
                                        if identity in seen:
                                            continue
                                        try:
                                            fd = os.open(task + '/map_files/' + fields[0], os.O_PATH | os.O_CLOEXEC)
                                            try:
                                                account(fd, managed=True)
                                            finally:
                                                os.close(fd)
                                        except OSError as error:
                                            # A vanished mapping released its reference. A managed
                                            # deleted mapping we cannot inspect is not zero bytes.
                                            if not missing(error):
                                                raise
                                            if mapping_remains(task, fields[0], identity):
                                                raise RuntimeError('managed mapping remains unavailable')
                            except OSError as error:
                                if not missing(error):
                                    raise
                except OSError as error:
                    if not missing(error):
                        raise
    if cfg.get('include_deleted'):
        deleted_open_files()
    values = [scan(path) for path in cfg['paths']]
    print(json.dumps({'bytes': values, 'deleted': deleted}, separators=(',', ':')))
except Exception as error:
    print(json.dumps({'error': 'STORAGE_CHECK_UNAVAILABLE', 'exception': type(error).__name__}))
    sys.exit(2)
`
