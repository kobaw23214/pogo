package webservice

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func startStorageFixture(t *testing.T, r *DockerRunner, rec Record) Record {
	t.Helper()
	instance, err := r.Start(context.Background(), Launch{Record: rec, Archive: projectTar(t, "app.py", "print('fixture')", tar.TypeReg)})
	if err != nil {
		t.Fatal(err)
	}
	rec.Instance = instance
	return rec
}

func waitStorageStop(t *testing.T, r *DockerRunner, rec Record, code string) RuntimeStatus {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		status, err := r.Inspect(context.Background(), rec)
		if err != nil {
			t.Fatal(err)
		}
		if !status.Exists && status.LastError != nil && status.LastError.Code == code {
			return status
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("storage stop not confirmed")
	return RuntimeStatus{}
}

func TestDockerStorageRuntimeStopsWithoutDeletingData(t *testing.T) {
	r, f := newFakeDocker(t)
	r.opts.RuntimeBytesLimit = 100
	rec := startStorageFixture(t, r, runnerRecord())
	name, _, work, _, data := r.names(rec)
	f.mu.Lock()
	f.storageSizes[work] = 60
	f.storageTmp[name] = 60
	f.storageSizes[data] = 12
	f.mu.Unlock()
	if err := r.ReconcileStorage(context.Background()); err != nil {
		t.Fatal(err)
	}
	status := waitStorageStop(t, r, rec, "STORAGE_LIMIT_EXCEEDED")
	if status.Storage == nil || status.Storage.RuntimeBytes != 120 || status.Storage.DataBytes != 12 {
		t.Fatalf("lost storage snapshot: %+v", status)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.volumes[data]; !ok {
		t.Fatal("data volume was removed")
	}
	if f.stopGrace[name] != "1" {
		t.Fatalf("storage grace: %q", f.stopGrace[name])
	}
	config := f.lastMeterCreate["HostConfig"].(map[string]any)
	if config["NetworkMode"] != "none" || config["ReadonlyRootfs"] != true || len(config["CapDrop"].([]any)) != 1 {
		t.Fatalf("unsafe meter: %+v", config)
	}
	for _, v := range config["Mounts"].([]any) {
		mount := v.(map[string]any)
		if mount["ReadOnly"] != true || mount["Type"] != "bind" || mount["BindOptions"].(map[string]any)["CreateMountpoint"] != false || mount["BindOptions"].(map[string]any)["Propagation"] != "rslave" {
			t.Fatal("meter has writable source")
		}
	}
}

func TestDockerStorageOwnerCountsStoppedSlotsAndDeduplicatesGenerations(t *testing.T) {
	r, f := newFakeDocker(t)
	r.opts.DataBytesLimit = 100
	r.opts.OwnerDataBytesLimit = 100
	a := startStorageFixture(t, r, runnerRecord())
	_, _, _, _, aData := r.names(a)
	f.mu.Lock()
	f.storageSizes[aData] = 75
	f.mu.Unlock()
	if err := r.Stop(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	a.Generation++
	a.Instance = Instance{}
	a = startStorageFixture(t, r, a)
	if err := r.Stop(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	b := runnerRecord()
	b.ServiceID = "other-service"
	b = startStorageFixture(t, r, b)
	_, _, _, _, bData := r.names(b)
	f.mu.Lock()
	f.storageSizes[bData] = 40
	f.mu.Unlock()
	if err := r.ReconcileStorage(context.Background()); err != nil {
		t.Fatal(err)
	}
	status := waitStorageStop(t, r, b, "STORAGE_LIMIT_EXCEEDED")
	if status.Storage == nil || status.Storage.OwnerDataBytes != 115 {
		t.Fatalf("generations counted twice or stopped slot omitted: %+v", status.Storage)
	}
	c := runnerRecord()
	c.ServiceID = "new-service"
	_, err := r.Start(context.Background(), Launch{Record: c, Archive: projectTar(t, "app.py", "pass", tar.TypeReg)})
	requireCode(t, err, "STORAGE_LIMIT_EXCEEDED")
	if err := r.DeleteData(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	_ = startStorageFixture(t, r, c)
}

func TestDockerStorageDataLimitAndRunnerReloadRetainCause(t *testing.T) {
	r, f := newFakeDocker(t)
	r.opts.DataBytesLimit = 100
	rec := startStorageFixture(t, r, runnerRecord())
	_, _, _, _, data := r.names(rec)
	f.mu.Lock()
	f.storageSizes[data] = 101
	f.mu.Unlock()
	if err := r.ReconcileStorage(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitStorageStop(t, r, rec, "STORAGE_LIMIT_EXCEEDED")
	other, err := NewDockerRunner(r.opts)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	status, err := other.Inspect(context.Background(), rec)
	if err != nil || status.Exists || status.LastError == nil || status.LastError.Code != "STORAGE_LIMIT_EXCEEDED" {
		t.Fatalf("reloaded cause lost: %+v %v", status, err)
	}
	_, err = other.Start(context.Background(), Launch{Record: Record{ServiceID: "new", Owner: rec.Owner, Session: rec.Session, Generation: 1, Installation: rec.Installation, SpecDigest: "spec", LeaseExpiresAt: rec.LeaseExpiresAt, Spec: rec.Spec}, Archive: projectTar(t, "app.py", "pass", tar.TypeReg)})
	requireCode(t, err, "STORAGE_LIMIT_EXCEEDED")
}

func TestDockerStorageCountsDeletedOpenFilesInRuntimeAndData(t *testing.T) {
	r, f := newFakeDocker(t)
	r.opts.RuntimeBytesLimit = 100
	r.opts.DataBytesLimit = 100
	rec := startStorageFixture(t, r, runnerRecord())
	name, _, work, _, data := r.names(rec)
	f.mu.Lock()
	f.storageSizes[work] = 20
	f.storageTmp[name] = 20
	f.storageSizes[data] = 30
	f.storageDeleted[name] = []storageDeletedFile{{Root: "workspace", Device: 1, Inode: 1, Bytes: 70}, {Root: "data", Device: 1, Inode: 2, Bytes: 80}}
	f.mu.Unlock()
	if err := r.ReconcileStorage(context.Background()); err != nil {
		t.Fatal(err)
	}
	status := waitStorageStop(t, r, rec, "STORAGE_LIMIT_EXCEEDED")
	if status.Storage == nil || status.Storage.RuntimeBytes != 110 || status.Storage.DataBytes != 110 || status.Storage.OwnerDataBytes != 110 {
		t.Fatalf("unlinked files disappeared from accounting: %+v", status.Storage)
	}
}

func TestDockerStorageDeduplicatesSharedDeletedDataAcrossGenerations(t *testing.T) {
	r, f := newFakeDocker(t)
	r.opts.DataBytesLimit = 100
	r.opts.OwnerDataBytesLimit = 100
	a := startStorageFixture(t, r, runnerRecord())
	aName, _, _, _, data := r.names(a)
	f.mu.Lock()
	f.storageSizes[data] = 20
	f.storageDeleted[aName] = []storageDeletedFile{{Root: "data", Device: 1, Inode: 4, Bytes: 50}}
	f.mu.Unlock()
	b := a
	b.Generation++
	b.Instance = Instance{}
	b = startStorageFixture(t, r, b)
	bName, _, _, _, _ := r.names(b)
	f.mu.Lock()
	f.storageDeleted[bName] = []storageDeletedFile{{Root: "data", Device: 1, Inode: 4, Bytes: 60}}
	f.mu.Unlock()
	if err := r.ReconcileStorage(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, rec := range []Record{a, b} {
		status, err := r.Inspect(context.Background(), rec)
		if err != nil || !status.Running || status.LastError != nil || status.Storage == nil || status.Storage.DataBytes != 80 || status.Storage.OwnerDataBytes != 80 {
			t.Fatalf("same retained inode charged twice: %+v %v", status, err)
		}
	}
}

func TestDockerStorageSnapshotWriteFailureStillStopsTheWriter(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("test needs ordinary filesystem permission enforcement")
	}
	r, f := newFakeDocker(t)
	rec := startStorageFixture(t, r, runnerRecord())
	name, _, _, _, data := r.names(rec)
	r.opts.RuntimeBytesLimit = 100
	f.mu.Lock()
	f.storageTmp[name] = 101
	f.mu.Unlock()
	if err := os.Chmod(r.opts.StateDir, 0500); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(r.opts.StateDir, 0700)
	if err := r.ReconcileStorage(context.Background()); err == nil {
		t.Fatal("snapshot write failure hidden")
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		running := f.containers[name].Running
		_, retained := f.volumes[data]
		f.mu.Unlock()
		if !running {
			if !retained {
				t.Fatal("data was deleted")
			}
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("writer remained running after control storage write failure")
}

func TestDockerStorageMeasurementFailureWarnsAndRecovers(t *testing.T) {
	for _, failure := range []string{"unreadable", "runtime-exec"} {
		t.Run(failure, func(t *testing.T) {
			r, f := newFakeDocker(t)
			rec := startStorageFixture(t, r, runnerRecord())
			name, _, work, _, _ := r.names(rec)
			if err := r.ReconcileStorage(context.Background()); err != nil {
				t.Fatal(err)
			}
			before, err := r.Inspect(context.Background(), rec)
			if err != nil || before.Storage == nil {
				t.Fatalf("initial storage unavailable: %+v %v", before, err)
			}
			restore := func() {}
			if failure == "unreadable" {
				f.mu.Lock()
				f.meterExit = 2
				f.mu.Unlock()
			} else {
				restore = failStorageRuntimeExec(r, rec)
			}
			for attempt := 0; attempt < 3; attempt++ {
				if err := r.ReconcileStorage(context.Background()); err == nil {
					t.Fatal("measurement failure hidden")
				}
				status, err := r.Inspect(context.Background(), rec)
				if err != nil || !status.Running || status.LastError != nil || !storageWarning(status.StorageWarning) || status.Storage == nil || !status.Storage.CheckedAt.Equal(before.Storage.CheckedAt) {
					t.Fatalf("temporary failure stopped writer or replaced valid usage: %+v %v", status, err)
				}
			}
			restore()
			f.mu.Lock()
			f.meterExit = 0
			f.storageTmp[name] = 10
			f.storageSizes[work] = 10
			f.mu.Unlock()
			if err := r.ReconcileStorage(context.Background()); err != nil {
				t.Fatal(err)
			}
			status, err := r.Inspect(context.Background(), rec)
			if err != nil || !status.Running || status.LastError != nil || status.StorageWarning != nil || status.Storage.RuntimeBytes != 20 {
				t.Fatalf("successful retry failed to recover warning: %+v %v", status, err)
			}
		})
	}
}

func TestDockerStorageMeterIsIndependentOfLifecycleLock(t *testing.T) {
	r, f := newFakeDocker(t)
	r.opts.RuntimeBytesLimit = 10
	rec := startStorageFixture(t, r, runnerRecord())
	name, _, _, _, _ := r.names(rec)
	f.mu.Lock()
	f.storageTmp[name] = 11
	f.mu.Unlock()
	r.mu.Lock()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := r.ReconcileStorage(ctx)
	status, inspectErr := r.Inspect(ctx, rec)
	r.mu.Unlock()
	if err != nil || inspectErr != nil || ctx.Err() != nil || status.LastError == nil || status.LastError.Code != "STORAGE_LIMIT_EXCEEDED" {
		t.Fatalf("lifecycle lock starved the measurement: %+v %v %v", status, err, inspectErr)
	}
	waitStorageStop(t, r, rec, "STORAGE_LIMIT_EXCEEDED")
}

func TestDockerStorageMeterCancellationAndForeignVolumeWarnWithoutStopping(t *testing.T) {
	t.Run("cancel", func(t *testing.T) {
		r, f := newFakeDocker(t)
		rec := startStorageFixture(t, r, runnerRecord())
		f.mu.Lock()
		f.blockMeterWait = true
		f.removeMeterOnStop = true
		f.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		defer cancel()
		_ = r.ReconcileStorage(ctx)
		status, err := r.Inspect(context.Background(), rec)
		if err != nil || !status.Running || status.LastError != nil || !storageWarning(status.StorageWarning) {
			t.Fatalf("cancelled scan stopped website: %+v %v", status, err)
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, c := range f.containers {
			if c.Labels["pogo.web.role"] == "storage-meter" {
				t.Fatal("cancelled measurement leaked its helper")
			}
		}
	})
	t.Run("foreign", func(t *testing.T) {
		r, f := newFakeDocker(t)
		rec := startStorageFixture(t, r, runnerRecord())
		_, _, _, _, data := r.names(rec)
		f.mu.Lock()
		f.volumes[data]["pogo.web.owner"] = "foreign"
		f.mu.Unlock()
		_ = r.ReconcileStorage(context.Background())
		status, err := r.Inspect(context.Background(), rec)
		if err != nil || !status.Running || status.LastError != nil || !storageWarning(status.StorageWarning) {
			t.Fatalf("foreign volume check stopped website: %+v %v", status, err)
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, value := range f.lastMeterCreate["HostConfig"].(map[string]any)["Mounts"].([]any) {
			if strings.Contains(value.(map[string]any)["Source"].(string), data) {
				t.Fatal("foreign volume was mounted for measurement")
			}
		}
	})
}

func TestPythonStorageMeterDoesNotFollowLinksOrDoubleCountHardlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix fd traversal")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("Python interpreter unavailable")
	}
	dir, err := os.MkdirTemp("", "pogo-storage-meter-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	owned := filepath.Join(dir, "owned")
	if err := os.Mkdir(owned, 0700); err != nil {
		t.Fatal(err)
	}
	if os.Getuid() == 0 {
		if err := os.Chown(dir, 1000, 1000); err != nil {
			t.Fatal(err)
		}
		if err := os.Chown(owned, 1000, 1000); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(owned, "data"), []byte(strings.Repeat("x", 8192)), 0600); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(dir, "secret")
	if err := os.WriteFile(external, []byte(strings.Repeat("s", 1<<20)), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(owned, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(owned, "data"), filepath.Join(owned, "hardlink")); err != nil {
		t.Fatal(err)
	}
	config := meterConfig([]string{owned})
	command := exec.Command(python, "-I", "-c", pythonStorageMeter, config)
	data, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	values, err := meterValues(data, 1)
	if err != nil || values[0] < 8192 || values[0] >= 64<<10 {
		t.Fatalf("symlink followed or hardlink counted twice: %s %v", data, err)
	}
	bad, _ := json.Marshal(map[string]any{"paths": []string{owned}, "timeout_ms": 2000, "max_entries": 1})
	if data, err := exec.Command(python, "-I", "-c", pythonStorageMeter, string(bad)).Output(); err == nil || !strings.Contains(string(data), "STORAGE_CHECK_UNAVAILABLE") {
		t.Fatalf("entry budget silently undercounted: %s %v", data, err)
	}
	private := filepath.Join(owned, "private")
	if err := os.Mkdir(private, 0700); err != nil {
		t.Fatal(err)
	}
	if os.Getuid() == 0 {
		if err := os.Chown(private, 1000, 1000); err != nil {
			t.Fatal(err)
		}
	}
	if data, err := exec.Command(python, "-I", "-c", pythonStorageMeter, config).Output(); err != nil {
		t.Fatalf("normal private directory could not be measured: %s %v", data, err)
	}
	if err := os.Chmod(private, 0000); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(private, 0700)
	if data, err := exec.Command(python, "-I", "-c", pythonStorageMeter, config).Output(); err == nil || !strings.Contains(string(data), "STORAGE_CHECK_UNAVAILABLE") {
		t.Fatalf("unreadable tree silently undercounted: %s %v", data, err)
	}
}

func TestStorageLimitsCannotDisableOrExceedHostDefaults(t *testing.T) {
	for _, value := range []int64{-1, defaultDataBytesLimit + 1} {
		o := DockerRunnerOptions{DataBytesLimit: value}
		requireCode(t, normalizeStorageLimits(&o), "INVALID_RUNNER_CONFIG")
	}
	var o DockerRunnerOptions
	if err := normalizeStorageLimits(&o); err != nil || o.RuntimeBytesLimit != defaultRuntimeBytesLimit || o.DataBytesLimit != defaultDataBytesLimit || o.OwnerDataBytesLimit != defaultOwnerDataBytesLimit {
		t.Fatalf("invalid defaults %+v %v", o, err)
	}
}

// Linux omits map_files from /proc/PID/task/TID. Model that documented proc
// layout and both sides of an ENOENT race; a persistent managed mapping must
// never become a successful zero-byte sample merely because opening it failed.
func TestPythonStorageMeterRequiresDeletedMappingToActuallyDisappear(t *testing.T) {
	python := "python3"
	if runtime.GOOS == "windows" {
		python = "python"
	}
	if _, err := exec.LookPath(python); err != nil {
		t.Skip("Python interpreter unavailable")
	}
	dir := t.TempDir()
	if os.Getuid() == 0 {
		if err := os.Chown(dir, 1000, 1000); err != nil {
			t.Fatal(err)
		}
	}
	for _, tt := range []struct {
		name, source string
		errno        int
		disappeared  bool
		fail         bool
	}{
		{"persistent-enoent", "/data/file (deleted)", 2, false, true},
		{"vanished-enoent", "/data/file (deleted)", 2, true, false},
		{"permission-denied", "/data/file (deleted)", 1, false, true},
		{"unowned-memfd", "/memfd:memory (deleted)", 1, false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fixture, _ := json.Marshal(map[string]any{"source": tt.source, "errno": tt.errno, "disappeared": tt.disappeared})
			prefix := `
import builtins, io, types
fixture = __import__('json').loads(sys.argv[2])
os.O_PATH = getattr(os, 'O_PATH', 0)
original_open, original_scandir, original_osopen = builtins.open, os.scandir, os.open
maps_read = 0
class Entries:
    def __init__(self, names): self.items = [types.SimpleNamespace(name=n) for n in names]
    def __enter__(self): return iter(self.items)
    def __exit__(self, *args): pass
def scandir(path):
    if path == '/proc': return Entries(['12345'])
    if path == '/proc/12345/task': return Entries(['12346'])
    if path in ('/proc/12346/fd', '/proc/12345/task/12346/fd'): return Entries([])
    return original_scandir(path)
def file_open(path, *args, **kwargs):
    global maps_read
    if path == '/proc/self/mountinfo': return io.StringIO('1 0 0:1 / /workspace rw - x x rw\n2 0 0:1 / /data rw - x x rw\n3 0 0:1 / /tmp rw - x x rw\n')
    if path == '/proc/12345/status': return io.StringIO('Uid:\t1000\t1000\t1000\t1000\n')
    if path in ('/proc/12346/maps', '/proc/12345/task/12346/maps'):
        maps_read += 1
        return io.StringIO('' if maps_read > 1 and fixture['disappeared'] else '1000-2000 rw-s 00000000 00:01 77 ' + fixture['source'] + '\n')
    return original_open(path, *args, **kwargs)
def descriptor_open(path, *args, **kwargs):
    if isinstance(path, str) and '/map_files/' in path:
        print('MAP_FILES_PATH:' + path, file=sys.stderr)
        raise OSError(fixture['errno'], 'fixture mapping unavailable')
    return original_osopen(path, *args, **kwargs)
os.scandir, builtins.open, os.open = scandir, file_open, descriptor_open
os.readlink = lambda path: 'mnt:[1234]'
`
			// The fixture only substitutes proc. The production scanner still
			// traverses the actual empty directory and handles its own errors.
			config, _ := json.Marshal(map[string]any{"paths": []string{dir}, "include_deleted": true, "max_entries": 200000, "timeout_ms": 2500})
			command := exec.Command(python, "-I", "-c", "import sys, os\n"+prefix+pythonStorageMeter, string(config), string(fixture))
			var stderr bytes.Buffer
			command.Stderr = &stderr
			data, err := command.Output()
			if (err != nil) != tt.fail {
				t.Fatalf("unexpected mapping result: %s %v %s", data, err, stderr.String())
			}
			if tt.source != "/memfd:memory (deleted)" {
				if !strings.Contains(stderr.String(), "MAP_FILES_PATH:/proc/12346/map_files/1000-2000") || strings.Contains(stderr.String(), "/task/") {
					t.Fatalf("mapping used missing task subdirectory: %s", stderr.String())
				}
			} else if stderr.Len() != 0 {
				t.Fatalf("unowned memfd required inspection: %s", stderr.String())
			}
			if tt.fail && !strings.Contains(string(data), "STORAGE_CHECK_UNAVAILABLE") {
				t.Fatalf("unverifiable mapping was not rejected: %s", data)
			}
		})
	}
}
