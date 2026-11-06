package webservice

import (
	"context"
	"errors"
	"time"
)

const initializeVolumeRoots = "import os; os.chown('/workspace', 1000, 1000); os.chown('/data', 1000, 1000)"

// Docker's archive extraction preserves file ownership but skips the target
// root directory's metadata. A separate fixed helper initializes only the two
// owned mount roots. The application never receives CHOWN or executes as root.
func (r *DockerRunner) initializeVolumes(ctx context.Context, rec Record, work, data string) (resultErr error) {
	name, _, _, _, _ := r.names(rec)
	name += "-init"
	// A prior interrupted attempt is safe to clean only after exact label checks.
	if err := r.cleanupInitializer(ctx, rec); err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := r.cleanupInitializer(cleanup, rec); err != nil {
			resultErr = errors.Join(resultErr, err)
		}
	}()
	labels := r.labels(rec, true)
	labels["pogo.web.role"] = "volume-init"
	mount := func(source, target string) map[string]any {
		return map[string]any{"Type": "volume", "Source": source, "Target": target, "ReadOnly": false, "VolumeOptions": map[string]any{"NoCopy": true}}
	}
	body := map[string]any{
		"Image": r.opts.Image, "Entrypoint": []string{r.opts.PythonPath, "-I", "-c", initializeVolumeRoots}, "Cmd": []string{}, "User": "0:0", "WorkingDir": "/", "Labels": labels,
		"HostConfig": map[string]any{"NetworkMode": "none", "ReadonlyRootfs": true, "CapDrop": []string{"ALL"}, "CapAdd": []string{"CHOWN"}, "SecurityOpt": []string{"no-new-privileges:true"}, "Memory": 64 << 20, "NanoCpus": 1_000_000_000, "PidsLimit": 16, "Mounts": []any{mount(work, "/workspace"), mount(data, "/data")}, "LogConfig": map[string]any{"Type": "json-file", "Config": map[string]string{"max-size": "64k", "max-file": "1"}}},
	}
	bounded, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if err := r.request(bounded, "POST", "/containers/create?name="+name, body, nil); err != nil {
		return err
	}
	if !time.Now().Before(rec.LeaseExpiresAt) {
		return Fail("LEASE_EXPIRED", "service expired before volume initialization")
	}
	if err := r.request(bounded, "POST", "/containers/"+name+"/start", nil, nil); err != nil {
		return err
	}
	var result struct {
		StatusCode int
		Error      *struct{ Message string }
	}
	if err := r.request(bounded, "POST", "/containers/"+name+"/wait?condition=not-running", nil, &result); err != nil {
		return err
	}
	if result.StatusCode != 0 || result.Error != nil {
		return Fail("RUNTIME_INIT_FAILED", "owned volume initialization did not exit successfully")
	}
	var state dockerContainer
	if err := r.request(bounded, "GET", "/containers/"+name+"/json", nil, &state); err != nil {
		return err
	}
	if !matchLabels(state.Config.Labels, labels) || state.State.Running || state.State.ExitCode != 0 {
		return Fail("RUNTIME_INIT_FAILED", "owned volume initialization exit was not confirmed")
	}
	return nil
}

func (r *DockerRunner) cleanupInitializer(ctx context.Context, rec Record) error {
	name, _, _, _, _ := r.names(rec)
	name += "-init"
	var state dockerContainer
	err := r.request(ctx, "GET", "/containers/"+name+"/json", nil, &state)
	if statusIs(err, 404) {
		return nil
	}
	if err != nil {
		return err
	}
	labels := r.labels(rec, true)
	labels["pogo.web.role"] = "volume-init"
	if !matchLabels(state.Config.Labels, labels) {
		return Fail("PERMISSION_DENIED", "volume initializer identity mismatch")
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
			return Fail("STOP_UNCONFIRMED", "volume initializer is still running")
		}
	}
	err = r.request(ctx, "DELETE", "/containers/"+name+"?v=false", nil, nil)
	if statusIs(err, 404) {
		return nil
	}
	return err
}
