package webservice

import (
	"archive/tar"
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// Hold an actual meter request after volume registration; Stop must account for
// this in-flight reader, rather than relying on the Docker volume DELETE race.
func holdStorageMeter(t *testing.T, r *DockerRunner, f *fakeDocker, rec Record, work string) (<-chan error, func()) {
	t.Helper()
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	result := make(chan error, 1)
	f.mu.Lock()
	f.meterWaitEntered = entered
	f.meterWaitRelease = release
	f.mu.Unlock()
	go func() {
		defer close(finished)
		_, err := r.measureVolumes(ctx, rec, []storageMount{{work, "/meter/0", r.labels(rec, true)}})
		result <- err
	}()
	t.Cleanup(func() {
		unblock()
		cancel()
		select {
		case <-finished:
		case <-time.After(time.Second):
			t.Error("meter did not finish cleanup")
		}
	})
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("meter did not register")
	}
	f.mu.Lock()
	f.meterWaitRelease = nil
	f.mu.Unlock()
	return result, unblock
}

func waitRuntimeStorageRetired(t *testing.T, r *DockerRunner, work string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if r.runtimeStorageRetired(work) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("stop never fenced runtime volumes")
}

func TestDockerStopDrainsMetersAndFencesOnlyRetiredGenerationVolumes(t *testing.T) {
	r, f := newFakeDocker(t)
	rec := startStorageFixture(t, r, runnerRecord())
	name, _, work, ctl, data := r.names(rec)
	f.mu.Lock()
	f.storageSizes[work] = 64
	f.storageSizes[data] = 17
	f.mu.Unlock()
	meter, release := holdStorageMeter(t, r, f, rec, work)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	stopped := make(chan error, 1)
	go func() { stopped <- r.Stop(ctx, rec) }()
	waitRuntimeStorageRetired(t, r, work)
	select {
	case err := <-stopped:
		t.Fatalf("stop failed to drain the meter: %v", err)
	default:
	}
	f.mu.Lock()
	_, runtimeExists := f.containers[name]
	_, workExists := f.volumes[work]
	f.mu.Unlock()
	if runtimeExists || !workExists {
		t.Fatal("writer must be absent while its measured volume is retained")
	}
	if !r.runtimeStorageRetired(ctl) {
		t.Fatal("control volume not fenced")
	}
	// A stale owner scan can still contain old workspace mounts, but must
	// continue measuring retained data instead of dropping the whole pass.
	values, err := r.measureVolumes(ctx, rec, []storageMount{{work, "/meter/0", r.labels(rec, true)}, {data, "/meter/1", r.labels(rec, false)}})
	if err != nil || len(values) != 2 || values[0] != 0 || values[1] != 17 {
		t.Fatalf("stale scan values=%v err=%v", values, err)
	}
	f.mu.Lock()
	mounts := f.lastMeterCreate["HostConfig"].(map[string]any)["Mounts"].([]any)
	f.mu.Unlock()
	if len(mounts) != 1 || mounts[0].(map[string]any)["Target"] != "/meter/1" {
		t.Fatal("stale workspace entered a new meter")
	}
	release()
	if err := <-meter; err != nil {
		t.Fatalf("existing meter failed: %v", err)
	}
	if err := <-stopped; err != nil {
		t.Fatalf("stop failed after meter drained: %v", err)
	}
	f.mu.Lock()
	_, workExists = f.volumes[work]
	_, dataExists := f.volumes[data]
	before := f.meterCreates
	f.mu.Unlock()
	if workExists || !dataExists {
		t.Fatal("stop cleanup or retained data contract changed")
	}
	values, err = r.measureVolumes(ctx, rec, []storageMount{{work, "/meter/0", r.labels(rec, true)}})
	f.mu.Lock()
	after := f.meterCreates
	f.mu.Unlock()
	if err != nil || len(values) != 1 || values[0] != 0 || before != after {
		t.Fatal("fully retired scan allocated a helper")
	}
	next := rec
	next.Generation++
	next.Instance = Instance{}
	next = startStorageFixture(t, r, next)
	_, _, newWork, _, _ := r.names(next)
	f.mu.Lock()
	f.storageSizes[newWork] = 33
	f.mu.Unlock()
	// Meter anchor can be an old retained generation while its mounts include
	// a current generation: the fence belongs to volume names, not the anchor.
	values, err = r.measureVolumes(ctx, rec, []storageMount{{newWork, "/meter/0", r.labels(next, true)}, {data, "/meter/1", r.labels(rec, false)}})
	if err != nil || len(values) != 2 || values[0] != 33 || values[1] != 17 {
		t.Fatalf("old generation fenced new data: %v %v", values, err)
	}
}

func TestDockerStopMeterDrainCancellationKeepsVolumesAndCanRetry(t *testing.T) {
	r, f := newFakeDocker(t)
	rec := startStorageFixture(t, r, runnerRecord())
	_, _, work, _, data := r.names(rec)
	meter, release := holdStorageMeter(t, r, f, rec, work)
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan error, 1)
	go func() { stopped <- r.Stop(ctx, rec) }()
	waitRuntimeStorageRetired(t, r, work)
	cancel()
	select {
	case err := <-stopped:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("meter drain ignored cancellation")
	}
	f.mu.Lock()
	_, workExists := f.volumes[work]
	_, dataExists := f.volumes[data]
	f.mu.Unlock()
	if !workExists || !dataExists {
		t.Fatal("cancelled stop deleted a measured volume")
	}
	// The durable launch can still be active until cleanup completes. It
	// must not allow a writer to be recreated behind the retirement fence.
	_, err := r.Start(context.Background(), Launch{Record: rec, Archive: projectTar(t, "app.py", "pass", tar.TypeReg)})
	requireCode(t, err, "SERVICE_EXITED")
	release()
	if err := <-meter; err != nil {
		t.Fatal(err)
	}
	retry, cancelRetry := context.WithTimeout(context.Background(), time.Second)
	defer cancelRetry()
	if err := r.Stop(retry, rec); err != nil {
		t.Fatalf("cleanup retry failed: %v", err)
	}
}

func TestDockerStopBeforeWriterConfirmationDoesNotRetireMeters(t *testing.T) {
	r, f := newFakeDocker(t)
	rec := startStorageFixture(t, r, runnerRecord())
	name, _, work, _, _ := r.names(rec)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := r.Stop(ctx, rec); err == nil {
		t.Fatal("cancelled stop was confirmed")
	}
	f.mu.Lock()
	running := f.containers[name].Running
	f.storageSizes[work] = 42
	f.mu.Unlock()
	if !running || r.runtimeStorageRetired(work) {
		t.Fatal("live writer lost storage monitoring")
	}
	values, err := r.measureVolumes(context.Background(), rec, []storageMount{{work, "/meter/0", r.labels(rec, true)}})
	if err != nil || len(values) != 1 || values[0] != 42 {
		t.Fatalf("live writer not measured: %v %v", values, err)
	}
}

func TestDockerStopMeterDrainRespectsDeadline(t *testing.T) {
	r, f := newFakeDocker(t)
	rec := startStorageFixture(t, r, runnerRecord())
	_, _, work, _, _ := r.names(rec)
	meter, release := holdStorageMeter(t, r, f, rec, work)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := r.Stop(ctx, rec); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stop ignored drain deadline: %v", err)
	}
	f.mu.Lock()
	_, exists := f.volumes[work]
	f.mu.Unlock()
	if !exists {
		t.Fatal("deadline removed a measured volume")
	}
	release()
	if err := <-meter; err != nil {
		t.Fatal(err)
	}
}
