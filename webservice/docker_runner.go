package webservice

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DockerRunnerOptions are exclusively administrator configuration. No image,
// Docker socket, gateway ID, mount or network option is accepted from tools.
type DockerRunnerOptions struct {
	SocketPath          string
	APIVersion          string
	Image               string
	PythonPath          string
	NodeImage           string
	NodePath            string
	NodePythonPath      string
	GatewayContainer    string
	Prefix              string
	Installation        string
	StateDir            string
	RuntimeBytesLimit   int64
	DataBytesLimit      int64
	OwnerDataBytesLimit int64
}

type DockerRunner struct {
	opts                DockerRunnerOptions
	http                *http.Client
	mu                  sync.Mutex
	storageMu           sync.Mutex
	storagePassMu       sync.Mutex
	storageStops        map[string]bool
	storageWarnings     map[string]*Error
	storageMeters       map[string]bool
	storageMeterVolumes map[string][]string
	storageVolumes      map[string]int
	storageRetired      map[string]bool
	storageChanged      chan struct{}
	storageWG           sync.WaitGroup
	storageClosed       bool
	storageContext      context.Context
	storageCancel       context.CancelFunc
}

var runnerID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)
var pinnedImage = regexp.MustCompile(`^(?:sha256:[a-f0-9]{64}|[^\s]+@sha256:[a-f0-9]{64})$`)

const managedLabel = "pogo.web.managed"
const maxRuntimeLog = 10 << 20

func NewDockerRunner(o DockerRunnerOptions) (*DockerRunner, error) {
	if err := normalizeStorageLimits(&o); err != nil {
		return nil, err
	}
	if o.SocketPath == "" {
		o.SocketPath = "/var/run/docker.sock"
	}
	if o.APIVersion == "" {
		o.APIVersion = "1.48"
	}
	if o.PythonPath == "" {
		o.PythonPath = "/opt/venv/bin/python3"
	}
	if o.NodePath == "" {
		o.NodePath = "/usr/local/bin/node"
	}
	if o.NodePythonPath == "" {
		o.NodePythonPath = "/usr/bin/python3"
	}
	if o.Prefix == "" {
		o.Prefix = "pogo-web"
	}
	if o.StateDir == "" {
		return nil, Fail("INVALID_RUNNER_CONFIG", "private runner state directory is required")
	}
	if err := os.MkdirAll(o.StateDir, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(o.StateDir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, Fail("INVALID_RUNNER_CONFIG", "runner state must be a private directory")
	}
	if err := os.Chmod(o.StateDir, 0700); err != nil {
		return nil, err
	}
	if !pinnedImage.MatchString(o.Image) || !runnerID.MatchString(o.Prefix) || !runnerID.MatchString(o.Installation) || o.GatewayContainer == "" || !strings.HasPrefix(o.PythonPath, "/") {
		return nil, Fail("INVALID_RUNNER_CONFIG", "pinned image, installation, gateway and absolute Python path are required")
	}
	if o.NodeImage != "" && (!pinnedImage.MatchString(o.NodeImage) || !path.IsAbs(o.NodePath) || !path.IsAbs(o.NodePythonPath)) {
		return nil, Fail("INVALID_RUNNER_CONFIG", "node-web requires a pinned image and absolute Node and supervisor Python paths")
	}
	if ok, _ := regexp.MatchString(`^1\.\d{2,3}$`, o.APIVersion); !ok {
		return nil, Fail("INVALID_RUNNER_CONFIG", "invalid Docker API version")
	}
	transport := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", o.SocketPath)
	}}
	storageContext, storageCancel := context.WithCancel(context.Background())
	return &DockerRunner{opts: o, http: &http.Client{Transport: transport, Timeout: 30 * time.Second}, storageStops: map[string]bool{}, storageWarnings: map[string]*Error{}, storageMeters: map[string]bool{}, storageMeterVolumes: map[string][]string{}, storageVolumes: map[string]int{}, storageRetired: map[string]bool{}, storageChanged: make(chan struct{}), storageContext: storageContext, storageCancel: storageCancel}, nil
}

type dockerError struct {
	Status  int
	Message string
}

func (e *dockerError) Error() string {
	return fmt.Sprintf("Docker API status %d: %s", e.Status, e.Message)
}
func statusIs(err error, status int) bool {
	var e *dockerError
	return errors.As(err, &e) && e.Status == status
}

func (r *DockerRunner) request(ctx context.Context, method, endpoint string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	res, err := r.raw(ctx, method, endpoint, reader, "application/json")
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if out != nil {
		return json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(out)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(res.Body, 1<<20))
	return nil
}

func (r *DockerRunner) raw(ctx context.Context, method, endpoint string, body io.Reader, content string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, "http://docker/v"+r.opts.APIVersion+endpoint, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", content)
	}
	res, err := r.http.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		defer res.Body.Close()
		var v struct{ Message string }
		_ = json.NewDecoder(io.LimitReader(res.Body, 16<<10)).Decode(&v)
		return nil, &dockerError{Status: res.StatusCode, Message: v.Message}
	}
	return res, nil
}

func (r *DockerRunner) Preflight(ctx context.Context) error {
	var image map[string]any
	if err := r.request(ctx, "GET", "/images/"+url.PathEscape(r.opts.Image)+"/json", nil, &image); err != nil {
		return Fail("PROFILE_UNAVAILABLE", "configured Python image is unavailable")
	}
	_, err := r.configuredGateway(ctx)
	return err
}

// Profiles is an authenticated control-plane capability query. An optional
// runtime is never advertised merely because a model knows its profile name.
func (r *DockerRunner) Profiles(ctx context.Context) ([]string, error) {
	if err := r.Preflight(ctx); err != nil {
		return nil, err
	}
	profiles := []string{"python-web"}
	if r.opts.NodeImage != "" {
		if err := r.nodeImageAvailable(ctx); err == nil {
			profiles = append(profiles, "node-web")
		}
	}
	return profiles, nil
}

func (r *DockerRunner) nodeImageAvailable(ctx context.Context) error {
	if r.opts.NodeImage == "" {
		return Fail("PROFILE_UNAVAILABLE", "node-web has not been configured by the administrator")
	}
	var image struct {
		Config struct{ Labels map[string]string }
	}
	if err := r.request(ctx, "GET", "/images/"+url.PathEscape(r.opts.NodeImage)+"/json", nil, &image); err != nil {
		return Fail("PROFILE_UNAVAILABLE", "configured Node.js image is unavailable")
	}
	labels := image.Config.Labels
	if labels["io.pogo.web.profile"] != "node-web" || labels["io.pogo.web.node"] != r.opts.NodePath || labels["io.pogo.web.python"] != r.opts.NodePythonPath {
		return Fail("PROFILE_UNAVAILABLE", "Node.js image must declare its reviewed node-web profile and interpreters")
	}
	return nil
}

func (r *DockerRunner) supervisorPython(rec Record) string {
	if rec.Spec.Profile == "node-web" {
		return r.opts.NodePythonPath
	}
	return r.opts.PythonPath
}

// The gateway is an administrator-configured container name/ID, never an
// application resource. Resolve its current ID so name replacement cannot
// redirect an in-flight connect to a different container.
func (r *DockerRunner) configuredGateway(ctx context.Context) (dockerContainer, error) {
	var gateway dockerContainer
	if err := r.request(ctx, "GET", "/containers/"+url.PathEscape(r.opts.GatewayContainer)+"/json", nil, &gateway); err != nil || gateway.ID == "" || !gateway.State.Running {
		return gateway, Fail("GATEWAY_UNAVAILABLE", "configured gateway container is not running")
	}
	for _, endpoint := range gateway.NetworkSettings.Networks {
		var network struct{ Internal bool }
		if endpoint.GwPriority > 0 && r.request(ctx, "GET", "/networks/"+url.PathEscape(endpoint.NetworkID), nil, &network) == nil && !network.Internal {
			return gateway, nil
		}
	}
	return gateway, Fail("GATEWAY_UNAVAILABLE", "gateway needs a normal edge network with positive gateway priority")
}

type dockerContainer struct {
	ID     string `json:"Id"`
	Config struct{ Labels map[string]string }
	State  struct {
		Running  bool
		Status   string
		ExitCode int
	}
	NetworkSettings struct {
		Networks map[string]struct {
			NetworkID, IPAddress string
			GwPriority           int
		}
	}
}

type dockerGenerationNetwork struct {
	ID       string `json:"Id"`
	Name     string
	Driver   string
	Internal bool
	Labels   map[string]string
}

func (r *DockerRunner) gatewayAttachment(ctx context.Context, rec Record, c dockerContainer) (dockerGenerationNetwork, dockerContainer, bool, error) {
	_, name, _, _, _ := r.names(rec)
	var network dockerGenerationNetwork
	if err := r.request(ctx, "GET", "/networks/"+url.PathEscape(name), nil, &network); err != nil {
		return network, dockerContainer{}, false, err
	}
	endpoint := c.NetworkSettings.Networks[name]
	if network.Name != name || network.ID == "" || network.Driver != "bridge" || !network.Internal || !matchLabels(network.Labels, r.labels(rec, true)) || endpoint.NetworkID != network.ID || rec.Instance.NetworkID != "" && rec.Instance.NetworkID != network.ID {
		return network, dockerContainer{}, false, Fail("PERMISSION_DENIED", "runtime network identity mismatch")
	}
	gateway, err := r.configuredGateway(ctx)
	if err != nil {
		return network, gateway, false, err
	}
	attached, exists := gateway.NetworkSettings.Networks[name]
	if exists && (attached.NetworkID != network.ID || attached.GwPriority != 0) {
		return network, gateway, false, Fail("PERMISSION_DENIED", "gateway application endpoint identity mismatch")
	}
	return network, gateway, exists, nil
}

// A recreated fixed-name gateway loses Docker's dynamically attached app
// networks. Repair only an existing generation; never create a network or
// restart application code. Caller holds the lifecycle mutex against Stop.
func (r *DockerRunner) restoreGatewayLocked(ctx context.Context, rec Record, c dockerContainer) error {
	saved, exists, err := r.savedLaunch(rec)
	if err != nil {
		return err
	}
	if !exists || !active(saved.State) || saved.State == "stopping" || !time.Now().Before(saved.LeaseExpiresAt) || storageFailure(saved.LastError) {
		return nil
	}
	snapshot, err := r.loadStorageSnapshot(saved)
	if err != nil {
		return err
	}
	if snapshot != nil && storageFailure(snapshot.Issue) {
		return nil
	}
	// Launch records saved before create may lack Instance. Pin the observed
	// runtime ID as well as its full generation labels on the final reread.
	if c.ID == "" || saved.Instance.ID != "" && saved.Instance.ID != c.ID {
		return Fail("PERMISSION_DENIED", "persisted runtime identity mismatch")
	}
	saved.Instance.ID = c.ID
	current, err := r.inspect(ctx, saved)
	if err != nil || !current.State.Running {
		return err
	}
	network, gateway, attached, err := r.gatewayAttachment(ctx, saved, current)
	if err != nil || attached {
		return err
	}
	err = r.request(ctx, "POST", "/networks/"+url.PathEscape(network.ID)+"/connect", map[string]any{"Container": gateway.ID, "EndpointConfig": map[string]any{"GwPriority": 0}}, nil)
	if err != nil && !statusIs(err, 403) && !statusIs(err, 409) {
		return err
	}
	_, after, attached, verifyErr := r.gatewayAttachment(ctx, saved, current)
	if verifyErr != nil {
		return verifyErr
	}
	if after.ID != gateway.ID || !attached {
		return Fail("GATEWAY_UNAVAILABLE", "gateway network recovery was not confirmed")
	}
	return nil
}

func (r *DockerRunner) restoreGateway(ctx context.Context, rec Record, c dockerContainer) error {
	if !c.State.Running || !active(rec.State) || rec.State == "stopping" {
		return nil
	}
	bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_, _, attached, err := r.gatewayAttachment(bounded, rec, c)
	if err != nil || attached {
		return err
	}
	// Healthy inspections take no lifecycle lock. Repair has its own bound,
	// including lock acquisition, so long preparation cannot block Inspect.
	for !r.mu.TryLock() {
		select {
		case <-bounded.Done():
			return bounded.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
	defer r.mu.Unlock()
	return r.restoreGatewayLocked(bounded, rec, c)
}

func digestID(s string) string { d := sha256.Sum256([]byte(s)); return hex.EncodeToString(d[:]) }
func (r *DockerRunner) names(rec Record) (container, network, workspace, control, data string) {
	base := r.opts.Prefix + "-" + digestID(rec.Installation + "\x00" + rec.Owner + "\x00" + rec.Session + "\x00" + rec.ServiceID)[:20]
	container = base + "-g" + strconv.Itoa(rec.Generation)
	return container, container + "-net", container + "-work", container + "-ctl", base + "-data"
}

func (r *DockerRunner) labels(rec Record, generation bool) map[string]string {
	m := map[string]string{managedLabel: "true", "pogo.web.installation": r.opts.Installation, "pogo.web.owner": digestID(rec.Owner), "pogo.web.session": digestID(rec.Session), "pogo.web.service": rec.ServiceID}
	if generation {
		m["pogo.web.generation"] = strconv.Itoa(rec.Generation)
		m["pogo.web.spec"] = rec.SpecDigest
		if rec.Runtime != (RuntimeConfig{}) {
			config, _ := json.Marshal(rec.Runtime)
			m["pogo.web.runtime"] = digestID(string(config))
		}
	}
	return m
}

func (r *DockerRunner) validate(rec Record) error {
	if rec.Installation != r.opts.Installation || !runnerID.MatchString(rec.ServiceID) || rec.Owner == "" || rec.Session == "" || rec.Generation < 1 || rec.SpecDigest == "" || rec.LeaseExpiresAt.IsZero() {
		return Fail("PERMISSION_DENIED", "invalid runner ownership or generation")
	}
	if _, err := rec.Runtime.Normalized(); err != nil {
		return Fail("INVALID_RUNNER_CONFIG", "invalid managed application runtime settings")
	}
	return nil
}

func matchLabels(have, want map[string]string) bool {
	for k, v := range want {
		if have[k] != v {
			return false
		}
	}
	return true
}
func (r *DockerRunner) inspect(ctx context.Context, rec Record) (dockerContainer, error) {
	if err := r.validate(rec); err != nil {
		return dockerContainer{}, err
	}
	name, _, _, _, _ := r.names(rec)
	var c dockerContainer
	if err := r.request(ctx, "GET", "/containers/"+name+"/json", nil, &c); err != nil {
		return c, err
	}
	if !matchLabels(c.Config.Labels, r.labels(rec, true)) || rec.Instance.ID != "" && rec.Instance.ID != c.ID {
		return c, Fail("PERMISSION_DENIED", "runtime identity does not match persisted service")
	}
	return c, nil
}

func (r *DockerRunner) ensureVolume(ctx context.Context, name string, labels map[string]string) error {
	var v struct{ Labels map[string]string }
	err := r.request(ctx, "GET", "/volumes/"+name, nil, &v)
	if err == nil {
		if !matchLabels(v.Labels, labels) {
			return Fail("PERMISSION_DENIED", "volume identity mismatch")
		}
		return nil
	}
	if !statusIs(err, 404) {
		return err
	}
	return r.request(ctx, "POST", "/volumes/create", map[string]any{"Name": name, "Labels": labels}, nil)
}

// Start is idempotent for the exact persisted service/generation/spec identity.
// Failures deliberately leave discoverable labelled resources for Stop/reconcile.
func (r *DockerRunner) Start(ctx context.Context, l Launch) (Instance, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec := l.Record
	if err := r.validate(rec); err != nil {
		return Instance{}, err
	}
	_, _, retiredWork, _, _ := r.names(rec)
	if r.runtimeStorageRetired(retiredWork) {
		return Instance{}, Fail("SERVICE_EXITED", "generation storage was retired; use a new generation")
	}
	if err := r.storageAdmissionLocked(ctx, rec.Owner); err != nil {
		return Instance{}, err
	}
	if err := r.saveLaunch(rec, false); err != nil {
		return Instance{}, err
	}
	if !time.Now().Before(rec.LeaseExpiresAt) {
		return Instance{}, Fail("LEASE_EXPIRED", "service lease has expired")
	}
	if len(rec.Spec.BuildArgv) > 0 || (rec.Spec.Profile != "python-web" && rec.Spec.Profile != "node-web") {
		return Instance{}, Fail("PROFILE_UNAVAILABLE", "managed Python/Node.js profiles do not install dependencies or run a build phase")
	}
	image, supervisorPython := r.opts.Image, r.opts.PythonPath
	if rec.Spec.Profile == "node-web" {
		if err := r.nodeImageAvailable(ctx); err != nil {
			return Instance{}, err
		}
		image, supervisorPython = r.opts.NodeImage, r.opts.NodePythonPath
	}
	settings, _ := rec.Runtime.Normalized() // validate above rejects invalid RPC input.
	argv, err := r.argvFor(rec.Spec.Profile, rec.Spec.StartArgv, settings.InternalPort)
	if err != nil {
		return Instance{}, err
	}
	archive, err := runtimeArchive(l.Archive)
	if err != nil {
		return Instance{}, err
	}
	if rec.Spec.Profile == "node-web" {
		if err := validateNodeArchive(archive); err != nil {
			return Instance{}, err
		}
	}
	if err := r.Preflight(ctx); err != nil {
		return Instance{}, err
	}
	name, network, work, ctl, data := r.names(rec)
	if c, err := r.inspect(ctx, rec); err == nil {
		if c.State.Running {
			if err := r.restoreGatewayLocked(ctx, rec, c); err != nil {
				return Instance{}, err
			}
			return r.instance(rec, c), nil
		}
		return Instance{}, Fail("SERVICE_EXITED", "existing generation is not running; use a new generation")
	} else if !statusIs(err, 404) {
		return Instance{}, err
	}
	labels := r.labels(rec, true)
	var n struct {
		ID     string `json:"Id"`
		Labels map[string]string
	}
	err = r.request(ctx, "GET", "/networks/"+network, nil, &n)
	if err == nil && !matchLabels(n.Labels, labels) {
		return Instance{}, Fail("PERMISSION_DENIED", "network identity mismatch")
	}
	if statusIs(err, 404) {
		err = r.request(ctx, "POST", "/networks/create", map[string]any{"Name": network, "Driver": "bridge", "Internal": true, "Labels": labels}, &n)
	}
	if err != nil {
		return Instance{}, err
	}
	for _, v := range []struct {
		name   string
		labels map[string]string
	}{{work, labels}, {ctl, labels}, {data, r.labels(rec, false)}} {
		if err := r.ensureVolume(ctx, v.name, v.labels); err != nil {
			return Instance{}, err
		}
	}
	if err := r.initializeVolumes(ctx, rec, work, data); err != nil {
		return Instance{}, err
	}
	// Connect only the trusted gateway; its normal edge remains higher priority.
	if err := r.request(ctx, "POST", "/networks/"+network+"/connect", map[string]any{"Container": r.opts.GatewayContainer, "EndpointConfig": map[string]any{"GwPriority": 0}}, nil); err != nil {
		var gateway dockerContainer
		if !statusIs(err, 403) || r.request(ctx, "GET", "/containers/"+url.PathEscape(r.opts.GatewayContainer)+"/json", nil, &gateway) != nil || gateway.NetworkSettings.Networks[network].NetworkID != n.ID {
			return Instance{}, err
		}
	}
	mount := func(source, target string) map[string]any {
		return map[string]any{"Type": "volume", "Source": source, "Target": target, "ReadOnly": false, "VolumeOptions": map[string]any{"NoCopy": true}}
	}
	body := map[string]any{
		"Image": image, "Entrypoint": []string{supervisorPython, "-I", "-c", pythonSupervisor}, "Cmd": []string{}, "User": "0:0", "WorkingDir": "/workspace", "Labels": labels,
		"Env":              []string{"HOST=0.0.0.0", "PORT=" + strconv.Itoa(settings.InternalPort), "HOME=/tmp", "PYTHONUNBUFFERED=1", "POGO_DATA_DIR=/data", "PATH=/opt/venv/bin:/usr/local/bin:/usr/bin:/bin"},
		"HostConfig":       map[string]any{"NetworkMode": network, "ReadonlyRootfs": true, "CapDrop": []string{"ALL"}, "CapAdd": []string{"SETUID", "SETGID"}, "SecurityOpt": []string{"no-new-privileges:true"}, "Memory": int64(settings.MemoryMiB) << 20, "NanoCpus": int64(settings.CPUs * 1_000_000_000), "PidsLimit": 128, "Mounts": []any{mount(work, "/workspace"), mount(ctl, "/run/pogo-control"), mount(data, "/data")}, "Tmpfs": map[string]string{"/tmp": "rw,nosuid,nodev,size=1g,mode=1777"}, "LogConfig": map[string]any{"Type": "json-file", "Config": map[string]string{"max-size": "10m", "max-file": "1"}}},
		"NetworkingConfig": map[string]any{"EndpointsConfig": map[string]any{network: map[string]any{"GwPriority": 0}}},
	}
	var created struct {
		ID string `json:"Id"`
	}
	if err := r.request(ctx, "POST", "/containers/create?name="+name, body, &created); err != nil {
		return Instance{}, err
	}
	if err := r.putArchive(ctx, name, "/workspace", archive); err != nil {
		return Instance{}, err
	}
	if err := r.putArchive(ctx, name, "/data", directoryArchive(1000)); err != nil {
		return Instance{}, err
	}
	config, _ := json.Marshal(map[string]any{"argv": argv})
	lease, _ := json.Marshal(map[string]any{"expires_at": rec.LeaseExpiresAt.UnixMilli()})
	if err := r.putArchive(ctx, name, "/run/pogo-control", controlArchive(map[string][]byte{"config.json": config, "lease.json": lease})); err != nil {
		return Instance{}, err
	}
	if !time.Now().Before(rec.LeaseExpiresAt) {
		return Instance{}, Fail("LEASE_EXPIRED", "service lease expired while preparing its runtime")
	}
	if err := r.request(ctx, "POST", "/containers/"+name+"/start", nil, nil); err != nil {
		return Instance{}, err
	}
	c, err := r.inspect(ctx, rec)
	if err != nil {
		return Instance{}, err
	}
	if !c.State.Running {
		return Instance{}, Fail("START_FAILED", "application supervisor exited before startup")
	}
	return r.instance(rec, c), nil
}

func (r *DockerRunner) argv(input []string) ([]string, error) {
	return r.argvFor("python-web", input, 8080)
}

func (r *DockerRunner) argvFor(profile string, input []string, port int) ([]string, error) {
	if len(input) == 0 || len(input) > 128 {
		return nil, Fail("START_FAILED", "start_argv is required")
	}
	result := append([]string(nil), input...)
	switch profile {
	case "python-web":
		if result[0] != "python" && result[0] != "python3" && result[0] != r.opts.PythonPath {
			return nil, Fail("PROFILE_UNAVAILABLE", "python-web requires the configured Python executable")
		}
		result[0] = r.opts.PythonPath
	case "node-web":
		if r.opts.NodeImage == "" || (result[0] != "node" && result[0] != r.opts.NodePath) {
			return nil, Fail("PROFILE_UNAVAILABLE", "node-web requires the configured Node.js executable")
		}
		result[0] = r.opts.NodePath
	default:
		return nil, Fail("PROFILE_UNAVAILABLE", "unsupported managed runtime profile")
	}
	for i, v := range result {
		if len(v) > 16384 || strings.ContainsRune(v, 0) {
			return nil, Fail("START_FAILED", "invalid argv token")
		}
		switch v {
		case "{{HOST}}":
			result[i] = "0.0.0.0"
		case "{{PORT}}":
			result[i] = strconv.Itoa(port)
		default:
			if strings.Contains(v, "{{") {
				return nil, Fail("START_FAILED", "unsupported argv template")
			}
		}
	}
	return result, nil
}

func (r *DockerRunner) instance(rec Record, c dockerContainer) Instance {
	_, network, _, _, _ := r.names(rec)
	endpoint := c.NetworkSettings.Networks[network]
	settings, _ := rec.Runtime.Normalized()
	return Instance{ID: c.ID, NetworkID: endpoint.NetworkID, Upstream: net.JoinHostPort(endpoint.IPAddress, strconv.Itoa(settings.InternalPort))}
}

func (r *DockerRunner) Inspect(ctx context.Context, rec Record) (RuntimeStatus, error) {
	if err := r.validate(rec); err != nil {
		return RuntimeStatus{}, err
	}
	snapshot, err := r.loadStorageSnapshot(rec)
	if err != nil {
		return RuntimeStatus{}, err
	}
	result := RuntimeStatus{}
	if snapshot != nil {
		result.Storage = snapshot.Usage
		result.LastError = snapshot.Issue
		result.StorageWarning = snapshot.Warning
	}
	name, _, _, _, _ := r.names(rec)
	r.storageMu.Lock()
	if warning := r.storageWarnings[name]; warning != nil {
		result.StorageWarning = warning
	}
	r.storageMu.Unlock()
	c, err := r.inspect(ctx, rec)
	if statusIs(err, 404) {
		return result, nil
	}
	if err != nil {
		return RuntimeStatus{}, err
	}
	result.Exists, result.Running, result.ExitCode, result.Instance = true, c.State.Running, c.State.ExitCode, r.instance(rec, c)
	if c.State.Running && snapshot != nil && time.Since(snapshot.CheckedAt) > storageSnapshotStaleAfter && !storageFailure(result.LastError) {
		result.StorageWarning = storageUnavailable()
		result.StorageWarning.Phase = "stale_snapshot"
	}
	if c.State.Running && snapshot == nil {
		if info, err := os.Stat(r.launchPath(rec)); err != nil || time.Since(info.ModTime()) > storageSnapshotStaleAfter {
			result.StorageWarning = storageUnavailable()
			result.StorageWarning.Phase = "missing_snapshot"
		}
	}
	if !storageFailure(result.LastError) {
		if err := r.restoreGateway(ctx, rec, c); err != nil {
			return result, err
		}
	}
	return result, nil
}

func (r *DockerRunner) ownedResource(ctx context.Context, endpoint string, labels map[string]string) error {
	var v struct{ Labels map[string]string }
	if err := r.request(ctx, "GET", endpoint, nil, &v); err != nil {
		return err
	}
	if !matchLabels(v.Labels, labels) {
		return Fail("PERMISSION_DENIED", "resource identity mismatch")
	}
	return nil
}

func (r *DockerRunner) Stop(ctx context.Context, rec Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stopLocked(ctx, rec)
}

func (r *DockerRunner) stopLocked(ctx context.Context, rec Record) error {
	if err := r.validate(rec); err != nil {
		return err
	}
	if snapshot, err := r.loadStorageSnapshot(rec); err != nil {
		return err
	} else if snapshot != nil {
		rec.Storage = snapshot.Usage
		if storageFailure(snapshot.Issue) {
			rec.LastError = snapshot.Issue
		}
	}
	if err := r.cleanupInitializer(ctx, rec); err != nil {
		return err
	}
	name, network, work, ctl, _ := r.names(rec)
	c, err := r.inspect(ctx, rec)
	if err != nil && !statusIs(err, 404) {
		return err
	}
	if err == nil {
		if c.State.Running {
			grace := "10"
			if storageFailure(rec.LastError) {
				grace = "1"
			}
			if err := r.request(ctx, "POST", "/containers/"+name+"/stop?t="+grace, nil, nil); err != nil && !statusIs(err, 304) {
				return err
			}
		}
		c, err = r.inspect(ctx, rec)
		if err != nil && !statusIs(err, 404) {
			return err
		}
		if err == nil && c.State.Running {
			return Fail("STOP_UNCONFIRMED", "runtime still running")
		}
		if err == nil {
			if log, logErr := r.readDockerLog(ctx, rec); logErr == nil {
				if err := r.saveStoppedLog(rec, log); err != nil {
					return err
				}
			} else {
				return logErr
			}
		}
		if err := r.request(ctx, "DELETE", "/containers/"+name+"?v=false", nil, nil); err != nil && !statusIs(err, 404) {
			return err
		}
	}
	// The writer is now confirmed absent. Fence only this generation's
	// disposable volumes, then let existing meters drain before deleting them.
	// Retained data and other generations remain measurable throughout.
	if err := r.retireRuntimeStorage(ctx, []string{work, ctl}); err != nil {
		return err
	}
	if err := r.ownedResource(ctx, "/networks/"+network, r.labels(rec, true)); err == nil {
		if err := r.request(ctx, "POST", "/networks/"+network+"/disconnect", map[string]any{"Container": r.opts.GatewayContainer, "Force": true}, nil); err != nil && !statusIs(err, 404) && !statusIs(err, 403) {
			return err
		}
		if err := r.request(ctx, "DELETE", "/networks/"+network, nil, nil); err != nil && !statusIs(err, 404) {
			return err
		}
	} else if !statusIs(err, 404) {
		return err
	}
	for _, v := range []string{work, ctl} {
		if err := r.deleteVolume(ctx, v, r.labels(rec, true)); err != nil {
			return err
		}
	}
	rec.State = "stopped"
	if storageFailure(rec.LastError) {
		rec.State = "failed"
	} else if !time.Now().Before(rec.LeaseExpiresAt) {
		rec.State = "expired"
	}
	rec.UpdatedAt = time.Now().UTC()
	return r.saveLaunch(rec, true)
}

func (r *DockerRunner) deleteVolume(ctx context.Context, name string, labels map[string]string) error {
	if err := r.ownedResource(ctx, "/volumes/"+name, labels); err != nil {
		if statusIs(err, 404) {
			return nil
		}
		return err
	}
	err := r.request(ctx, "DELETE", "/volumes/"+name, nil, nil)
	if statusIs(err, 404) {
		return nil
	}
	return err
}

func (r *DockerRunner) DeleteData(ctx context.Context, rec Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.validate(rec); err != nil {
		return err
	}
	_, _, _, _, data := r.names(rec)
	if r.storageVolumeBusy(data) {
		return Fail("STOP_UNCONFIRMED", "storage meter still uses the retained data volume")
	}
	if err := r.deleteVolume(ctx, data, r.labels(rec, false)); err != nil {
		return err
	}
	name, _, _, _, _ := r.names(rec)
	prefix := strings.TrimSuffix(name, "-g"+strconv.Itoa(rec.Generation)) + "-g"
	files, err := os.ReadDir(r.opts.StateDir)
	if err != nil {
		return err
	}
	for _, entry := range files {
		if !entry.Type().IsRegular() || !strings.HasPrefix(entry.Name(), prefix) {
			continue
		}
		p := filepath.Join(r.opts.StateDir, entry.Name())
		if strings.HasSuffix(entry.Name(), ".launch.json") {
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			var old Record
			err = json.NewDecoder(io.LimitReader(f, 16<<20)).Decode(&old)
			f.Close()
			if err != nil || r.validate(old) != nil || !matchLabels(r.labels(old, false), r.labels(rec, false)) || filepath.Base(r.launchPath(old)) != entry.Name() {
				return Fail("PERMISSION_DENIED", "runner metadata identity mismatch")
			}
		} else if strings.HasSuffix(entry.Name(), ".log.json") {
			f, err := os.Open(p)
			if err != nil {
				return err
			}
			var log stoppedLog
			err = json.NewDecoder(io.LimitReader(f, 15<<20)).Decode(&log)
			f.Close()
			if err != nil || !matchLabels(log.Labels, r.labels(rec, false)) {
				return Fail("PERMISSION_DENIED", "runner log identity mismatch")
			}
		} else if strings.HasSuffix(entry.Name(), ".storage.json") {
			var snapshot storageSnapshot
			data, err := os.ReadFile(p)
			if err != nil || json.Unmarshal(data, &snapshot) != nil || !matchLabels(snapshot.Labels, r.labels(rec, false)) {
				return Fail("PERMISSION_DENIED", "runner storage snapshot identity mismatch")
			}
		} else {
			continue
		}
		if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (r *DockerRunner) Renew(ctx context.Context, rec Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.validate(rec); err != nil {
		return err
	}
	if snapshot, err := r.loadStorageSnapshot(rec); err != nil {
		return err
	} else if snapshot != nil && storageFailure(snapshot.Issue) {
		return snapshot.Issue
	}
	if err := r.storageAdmissionLocked(ctx, rec.Owner); err != nil {
		return err
	}
	c, err := r.inspect(ctx, rec)
	if err != nil {
		return err
	}
	if !c.State.Running {
		return Fail("SERVICE_EXITED", "cannot renew stopped runtime")
	}
	now := time.Now()
	if !rec.LeaseExpiresAt.After(now) || rec.LeaseExpiresAt.After(now.Add(time.Duration(MaxLeaseSeconds)*time.Second)) {
		return Fail("LEASE_EXPIRED", "invalid runtime lease")
	}
	data, _ := json.Marshal(map[string]any{"expires_at": rec.LeaseExpiresAt.UnixMilli()})
	name, _, _, _, _ := r.names(rec)
	if err := r.putArchive(ctx, name, "/run/pogo-control", controlArchive(map[string][]byte{"lease.next": data})); err != nil {
		return err
	}
	if err := r.atomicLease(ctx, name, rec); err != nil {
		return err
	}
	return r.saveLaunch(rec, true)
}

func (r *DockerRunner) atomicLease(ctx context.Context, name string, rec Record) error {
	var created struct {
		ID string `json:"Id"`
	}
	command := []string{r.supervisorPython(rec), "-I", "-c", "import os; os.replace('/run/pogo-control/lease.next', '/run/pogo-control/lease.json')"}
	if err := r.request(ctx, "POST", "/containers/"+name+"/exec", map[string]any{"User": "0:0", "Cmd": command, "AttachStdout": true, "AttachStderr": true}, &created); err != nil {
		return err
	}
	if err := r.request(ctx, "POST", "/exec/"+created.ID+"/start", map[string]any{"Detach": false, "Tty": false}, nil); err != nil {
		return err
	}
	var state struct {
		Running  bool
		ExitCode int
	}
	if err := r.request(ctx, "GET", "/exec/"+created.ID+"/json", nil, &state); err != nil {
		return err
	}
	if state.Running || state.ExitCode != 0 {
		return Fail("LEASE_UPDATE_FAILED", "runtime lease update was not confirmed")
	}
	return nil
}

func (r *DockerRunner) putArchive(ctx context.Context, name, target string, data []byte) error {
	// false preserves the archive's sanitized UID/GID. Despite its name, true
	// invokes Moby's container-user copy override (User=0 here), which would
	// incorrectly make the application's workspace/data root-owned as well.
	// Workspace/data use UID 1000; supervisor control files stay root-owned.
	res, err := r.raw(ctx, "PUT", "/containers/"+name+"/archive?copyUIDGID=false&path="+url.QueryEscape(target), bytes.NewReader(data), "application/x-tar")
	if err != nil {
		return err
	}
	res.Body.Close()
	return nil
}

func directoryArchive(uid int) []byte               { return controlTar(uid, nil) }
func controlArchive(files map[string][]byte) []byte { return controlTar(0, files) }
func controlTar(uid int, files map[string][]byte) []byte {
	var out bytes.Buffer
	w := tar.NewWriter(&out)
	_ = w.WriteHeader(&tar.Header{Name: ".", Typeflag: tar.TypeDir, Mode: 0755, Uid: uid, Gid: uid})
	for name, data := range files {
		_ = w.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0444, Uid: 0, Gid: 0, Size: int64(len(data))})
		_, _ = w.Write(data)
	}
	_ = w.Close()
	return out.Bytes()
}

func runtimeArchive(input []byte) ([]byte, error) {
	if len(input) > 55<<20 {
		return nil, Fail("SNAPSHOT_LIMIT_EXCEEDED", "snapshot archive too large")
	}
	var out bytes.Buffer
	w := tar.NewWriter(&out)
	_ = w.WriteHeader(&tar.Header{Name: ".", Typeflag: tar.TypeDir, Mode: 0755, Uid: 1000, Gid: 1000})
	reader := tar.NewReader(bytes.NewReader(input))
	files := 0
	var total int64
	for {
		h, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, Fail("INVALID_PROJECT_ROOT", "invalid snapshot archive")
		}
		name := path.Clean(h.Name)
		if name == "." {
			continue
		}
		if name == ".." || strings.HasPrefix(name, "../") || path.IsAbs(name) || strings.Contains(name, "\\") {
			return nil, Fail("INVALID_PROJECT_ROOT", "unsafe snapshot path")
		}
		for _, part := range strings.Split(name, "/") {
			if part == ".git" || part == ".venv" || part == "node_modules" || part == ".env" || strings.HasPrefix(part, ".env.") {
				return nil, Fail("INVALID_PROJECT_ROOT", "private or generated snapshot path")
			}
		}
		if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA && h.Typeflag != tar.TypeDir {
			return nil, Fail("INVALID_PROJECT_ROOT", "snapshot may contain only regular files and directories")
		}
		if h.Size < 0 || h.Size > 50<<20 {
			return nil, Fail("SNAPSHOT_LIMIT_EXCEEDED", "snapshot file too large")
		}
		if path.Base(name) == "requirements.txt" && h.Size > 0 {
			return nil, Fail("DEPENDENCY_UNSUPPORTED", "dependency installation requires the controlled installer profile")
		}
		if h.Typeflag != tar.TypeDir {
			files++
			total += h.Size
		}
		if files > 5000 || total > 50<<20 {
			return nil, Fail("SNAPSHOT_LIMIT_EXCEEDED", "snapshot limit exceeded")
		}
		h.Name = name
		h.Uid = 1000
		h.Gid = 1000
		h.Uname = ""
		h.Gname = ""
		h.Mode = 0644
		if h.Typeflag == tar.TypeDir {
			h.Mode = 0755
		}
		h.PAXRecords = nil
		h.Xattrs = nil
		if err := w.WriteHeader(h); err != nil {
			return nil, err
		}
		if _, err := io.Copy(w, reader); err != nil {
			return nil, err
		}
	}
	if files == 0 {
		return nil, Fail("INVALID_PROJECT_ROOT", "project snapshot contains no files")
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func (r *DockerRunner) Logs(ctx context.Context, rec Record, cursor string, limit int) (LogPage, error) {
	if limit == 0 {
		limit = 16 << 10
	}
	if limit < 1 || limit > 64<<10 {
		return LogPage{}, Fail("INVALID_LOG_CURSOR", "invalid log page limit")
	}
	if err := r.validate(rec); err != nil {
		return LogPage{}, err
	}
	c, inspectErr := r.inspect(ctx, rec)
	var text []byte
	if statusIs(inspectErr, 404) {
		var err error
		text, err = r.loadStoppedLog(rec)
		if err != nil {
			return LogPage{}, err
		}
	} else if inspectErr != nil {
		return LogPage{}, inspectErr
	} else {
		var err error
		text, err = r.readDockerLog(ctx, rec)
		if err != nil {
			return LogPage{}, err
		}
	}
	truncated := len(text) >= maxRuntimeLog
	stamp := digestID(string(text[:min(64, len(text))]))[:16]
	scope := digestID(rec.ServiceID + ":" + strconv.Itoa(rec.Generation))[:16]
	offset := 0
	if cursor != "" {
		b, err := base64.RawURLEncoding.DecodeString(cursor)
		parts := strings.Split(string(b), ":")
		if err != nil || len(parts) != 3 || parts[0] != scope {
			return LogPage{}, Fail("INVALID_LOG_CURSOR", "cursor belongs to a different runtime")
		}
		offset, err = strconv.Atoi(parts[2])
		if err != nil || offset < 0 {
			return LogPage{}, Fail("INVALID_LOG_CURSOR", "invalid cursor")
		}
		if parts[1] != stamp || offset > len(text) {
			offset = 0
			truncated = true
		}
	}
	end := min(offset+limit, len(text))
	next := base64.RawURLEncoding.EncodeToString([]byte(scope + ":" + stamp + ":" + strconv.Itoa(end)))
	return LogPage{Generation: rec.Generation, FromCursor: cursor, NextCursor: next, Text: string(text[offset:end]), Truncated: truncated, EOF: !c.State.Running && end == len(text)}, nil
}

func (r *DockerRunner) readDockerLog(ctx context.Context, rec Record) ([]byte, error) {
	name, _, _, _, _ := r.names(rec)
	res, err := r.raw(ctx, "GET", "/containers/"+name+"/logs?stdout=1&stderr=1&tail=all", nil, "")
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 12<<20))
	if err != nil {
		return nil, err
	}
	var decoded bytes.Buffer
	for len(raw) > 0 {
		if len(raw) < 8 {
			return nil, Fail("LOG_UNAVAILABLE", "invalid Docker log stream")
		}
		n := int(binary.BigEndian.Uint32(raw[4:8]))
		if n < 0 || n > len(raw)-8 {
			return nil, Fail("LOG_UNAVAILABLE", "invalid Docker log frame")
		}
		decoded.Write(raw[8 : 8+n])
		raw = raw[8+n:]
	}
	text := decoded.Bytes()
	truncated := len(text) > maxRuntimeLog
	if truncated {
		text = text[len(text)-maxRuntimeLog:]
	}
	return append([]byte(nil), text...), nil
}

type stoppedLog struct {
	Labels  map[string]string `json:"labels"`
	SavedAt time.Time         `json:"saved_at"`
	Text    []byte            `json:"text"`
}

func (r *DockerRunner) logPath(rec Record) string {
	name, _, _, _, _ := r.names(rec)
	return filepath.Join(r.opts.StateDir, name+".log.json")
}
func (r *DockerRunner) saveStoppedLog(rec Record, text []byte) error {
	b, err := json.Marshal(stoppedLog{Labels: r.labels(rec, true), SavedAt: time.Now(), Text: text})
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(r.opts.StateDir, ".log-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
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
	return os.Rename(tmp, r.logPath(rec))
}
func (r *DockerRunner) loadStoppedLog(rec Record) ([]byte, error) {
	f, err := os.Open(r.logPath(rec))
	if err != nil {
		return nil, Fail("LOG_UNAVAILABLE", "runtime log is unavailable")
	}
	defer f.Close()
	var log stoppedLog
	if json.NewDecoder(io.LimitReader(f, 15<<20)).Decode(&log) != nil || !matchLabels(log.Labels, r.labels(rec, true)) || len(log.Text) > maxRuntimeLog {
		return nil, Fail("LOG_UNAVAILABLE", "runtime log metadata is invalid")
	}
	if time.Since(log.SavedAt) > 7*24*time.Hour {
		return nil, Fail("LOG_UNAVAILABLE", "runtime log has expired")
	}
	return log.Text, nil
}

func (r *DockerRunner) launchPath(rec Record) string {
	name, _, _, _, _ := r.names(rec)
	return filepath.Join(r.opts.StateDir, name+".launch.json")
}
func (r *DockerRunner) saveLaunch(rec Record, replace bool) error {
	if !replace {
		if b, err := os.ReadFile(r.launchPath(rec)); err == nil {
			var old Record
			if json.Unmarshal(b, &old) != nil || !matchLabels(r.labels(old, true), r.labels(rec, true)) {
				return Fail("PERMISSION_DENIED", "runner launch identity mismatch")
			}
			if old.State == "stopped" || old.State == "expired" || old.State == "failed" || old.State == "interrupted" {
				return Fail("SERVICE_EXITED", "a completed generation cannot be executed again")
			}
			return nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(r.opts.StateDir, ".launch-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
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
	return os.Rename(tmp, r.launchPath(rec))
}

// ReconcileOwned restores fixed-gateway membership and cleans expired resources
// only when full persisted identity matches Docker labels. It never restarts
// exited applications and does not
// infer ownership from container names. The runtime PID1 also enforces leases
// while this daemon is unavailable.
func (r *DockerRunner) ReconcileOwned(ctx context.Context) error {
	files, err := os.ReadDir(r.opts.StateDir)
	if err != nil {
		return err
	}
	var failures []error
	for _, f := range files {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !f.Type().IsRegular() || !strings.HasSuffix(f.Name(), ".launch.json") {
			continue
		}
		file, err := os.Open(filepath.Join(r.opts.StateDir, f.Name()))
		if err != nil {
			failures = append(failures, err)
			continue
		}
		var rec Record
		err = json.NewDecoder(io.LimitReader(file, 16<<20)).Decode(&rec)
		file.Close()
		if err != nil || r.validate(rec) != nil || filepath.Base(r.launchPath(rec)) != f.Name() {
			failures = append(failures, Fail("PERMISSION_DENIED", "invalid persisted runner identity"))
			continue
		}
		if rec.State == "stopped" || rec.State == "expired" || rec.State == "failed" || rec.State == "interrupted" {
			r.mu.Lock()
			var current Record
			file, err := os.Open(r.launchPath(rec))
			if err == nil {
				err = json.NewDecoder(io.LimitReader(file, 16<<20)).Decode(&current)
				file.Close()
			}
			terminal := current.State == "stopped" || current.State == "expired" || current.State == "failed" || current.State == "interrupted"
			if err == nil && terminal && r.validate(current) == nil && !current.UpdatedAt.IsZero() && time.Since(current.UpdatedAt) > 7*24*time.Hour {
				if err := os.Remove(r.logPath(current)); err != nil && !errors.Is(err, os.ErrNotExist) {
					failures = append(failures, err)
				}
				if len(current.Spec.StartArgv) > 0 || current.SourceDigest != "" {
					current.Spec = Spec{}
					current.SourceDigest = ""
					if err := r.saveLaunch(current, true); err != nil {
						failures = append(failures, err)
					}
				}
			}
			r.mu.Unlock()
			continue
		}
		r.mu.Lock()
		cleanupErr := r.cleanupInitializer(ctx, rec)
		r.mu.Unlock()
		if cleanupErr != nil {
			failures = append(failures, cleanupErr)
			continue
		}
		if !time.Now().Before(rec.LeaseExpiresAt) {
			// Renew may have committed while this scan waited. Decide expiry
			// again under the same mutex as Renew, not from a stale snapshot.
			r.mu.Lock()
			var current Record
			f, openErr := os.Open(r.launchPath(rec))
			if openErr == nil {
				openErr = json.NewDecoder(io.LimitReader(f, 16<<20)).Decode(&current)
				f.Close()
			}
			if openErr == nil && r.validate(current) == nil && matchLabels(r.labels(current, true), r.labels(rec, true)) && !time.Now().Before(current.LeaseExpiresAt) {
				if err := r.stopLocked(ctx, current); err != nil {
					failures = append(failures, err)
				}
			} else if openErr != nil && !errors.Is(openErr, os.ErrNotExist) {
				failures = append(failures, openErr)
			}
			r.mu.Unlock()
		} else if c, err := r.inspect(ctx, rec); err == nil {
			if err := r.restoreGateway(ctx, rec, c); err != nil {
				failures = append(failures, err)
			}
		} else if !statusIs(err, 404) {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

// Close releases idle Docker HTTP connections. Running services keep their
// root-owned, independently supervised lease; daemon shutdown is not stop-all.
func (r *DockerRunner) Close() error {
	r.storageMu.Lock()
	r.storageClosed = true
	r.storageCancel()
	r.storageMu.Unlock()
	r.storageWG.Wait()
	r.http.CloseIdleConnections()
	return nil
}

const pythonSupervisor = pythonNetworkGuard + `
import json, os, signal, subprocess, sys, time
with open('/run/pogo-control/config.json') as f:
    argv = json.load(f)['argv']
try:
    with open('/run/pogo-control/lease.json') as f:
        initial_deadline = int(json.load(f)['expires_at'])
except (OSError, ValueError, KeyError):
    sys.exit(124)
if time.time() * 1000 >= initial_deadline:
    sys.exit(124)
def drop():
    os.setgroups([])
    os.setgid(1000)
    os.setuid(1000)
    _deny_outbound()
child = subprocess.Popen(argv, cwd='/workspace', preexec_fn=drop, start_new_session=True)
stopping = False
def stop(signum=None, frame=None):
    global stopping
    stopping = True
signal.signal(signal.SIGTERM, stop)
signal.signal(signal.SIGINT, stop)
def signal_child(signum):
    # The trusted supervisor keeps real/saved root identity, but has no KILL
    # capability. Signal its permanently UID 1000 application as that user.
    previous = os.geteuid()
    try:
        os.seteuid(1000)
        os.killpg(child.pid, signum)
    except ProcessLookupError:
        pass
    finally:
        os.seteuid(previous)
while child.poll() is None and not stopping:
    try:
        with open('/run/pogo-control/lease.json') as f:
            deadline = int(json.load(f)['expires_at'])
        if time.time() * 1000 >= deadline:
            stopping = True
            break
    except (OSError, ValueError, KeyError):
        stopping = True
        break
    time.sleep(0.25)
if child.poll() is None:
    signal_child(signal.SIGTERM)
    try: child.wait(timeout=10)
    except subprocess.TimeoutExpired:
        signal_child(signal.SIGKILL)
        child.wait()
sys.exit(child.returncode if child.returncode is not None and child.returncode >= 0 else 1)
`
