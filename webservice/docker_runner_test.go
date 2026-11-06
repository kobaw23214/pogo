package webservice

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const runnerTestImage = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type fakeDockerRuntime struct {
	ID, Name, Network string
	Labels            map[string]string
	Running           bool
	Files             map[string]map[string][]byte
	MeterOutput       []byte
}
type fakeDocker struct {
	mu                      sync.Mutex
	containers              map[string]*fakeDockerRuntime
	networks                map[string]map[string]string
	networkIDs              map[string]string
	volumes                 map[string]map[string]string
	creates                 int
	lastCreate              map[string]any
	imageLabels             map[string]map[string]string
	lastInitCreate          map[string]any
	initCreates             int
	initStarts              int
	initExit                int
	blockInitWait           bool
	initWaitEntered         chan struct{}
	priority                int
	gatewayID               string
	gatewayNetworks         map[string]int
	gatewayConnects         int
	gatewayConnectErr       int
	gatewayRaceConnect      bool
	gatewayReplaceOnConnect bool
	lastGatewayConnect      map[string]any
	log                     string
	archiveDelay            time.Duration
	starts                  int
	storageSizes            map[string]int64
	storageTmp              map[string]int64
	storageDeleted          map[string][]storageDeletedFile
	execOutputs             map[string][]byte
	meterExit               int
	meterCreates            int
	lastMeterCreate         map[string]any
	blockMeterWait          bool
	meterWaitEntered        chan struct{}
	meterWaitRelease        <-chan struct{}
	removeMeterOnStop       bool
	stopGrace               map[string]string
}

func newFakeDocker(t *testing.T) (*DockerRunner, *fakeDocker) {
	t.Helper()
	f := &fakeDocker{containers: map[string]*fakeDockerRuntime{}, networks: map[string]map[string]string{}, volumes: map[string]map[string]string{}, storageSizes: map[string]int64{}, storageTmp: map[string]int64{}, storageDeleted: map[string][]storageDeletedFile{}, execOutputs: map[string][]byte{}, stopGrace: map[string]string{}, priority: 100, gatewayID: "gateway-id", gatewayNetworks: map[string]int{}, log: "log-one\nlog-two\n"}
	dir, err := os.MkdirTemp("", "pwr-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	socket := filepath.Join(dir, "d.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(f.serve)}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	r, err := NewDockerRunner(DockerRunnerOptions{SocketPath: socket, Image: runnerTestImage, GatewayContainer: "gateway", Prefix: "qa-web", Installation: "qa-install", StateDir: filepath.Join(dir, "state")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	return r, f
}

func stringMap(v any) map[string]string {
	out := map[string]string{}
	for k, x := range v.(map[string]any) {
		out[k] = x.(string)
	}
	return out
}
func (f *fakeDocker) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p := strings.TrimPrefix(r.URL.Path, "/v1.48")
	parts := strings.Split(strings.Trim(p, "/"), "/")
	write := func(v any) { w.Header().Set("Content-Type", "application/json"); _ = json.NewEncoder(w).Encode(v) }
	notFound := func() { w.WriteHeader(404); write(map[string]string{"message": "not found"}) }
	decode := func() map[string]any { var v map[string]any; _ = json.NewDecoder(r.Body).Decode(&v); return v }
	frame := func(data []byte) {
		header := make([]byte, 8)
		header[0] = 1
		binary.BigEndian.PutUint32(header[4:], uint32(len(data)))
		_, _ = w.Write(header)
		_, _ = w.Write(data)
	}
	if p == "/containers/json" {
		var filters map[string][]string
		_ = json.Unmarshal([]byte(r.URL.Query().Get("filters")), &filters)
		rows := []map[string]any{}
		for name, c := range f.containers {
			matched := true
			for _, filter := range filters["label"] {
				k, v, _ := strings.Cut(filter, "=")
				if c.Labels[k] != v {
					matched = false
				}
			}
			if matched {
				rows = append(rows, map[string]any{"Id": c.ID, "Names": []string{"/" + name}, "Labels": c.Labels})
			}
		}
		write(rows)
		return
	}
	if strings.HasPrefix(p, "/images/") {
		image := strings.TrimSuffix(strings.TrimPrefix(p, "/images/"), "/json")
		write(map[string]any{"Id": image, "Config": map[string]any{"Labels": f.imageLabels[image]}})
		return
	}
	if p == "/containers/gateway/json" {
		networks := map[string]any{"edge": map[string]any{"NetworkID": "edge-id", "GwPriority": f.priority}}
		for name, priority := range f.gatewayNetworks {
			networks[name] = map[string]any{"NetworkID": name + "-id", "GwPriority": priority}
		}
		write(map[string]any{"Id": f.gatewayID, "State": map[string]any{"Running": true}, "NetworkSettings": map[string]any{"Networks": networks}})
		return
	}
	if p == "/networks/edge-id" {
		write(map[string]any{"Internal": false})
		return
	}
	if p == "/networks/create" {
		v := decode()
		name := v["Name"].(string)
		f.networks[name] = stringMap(v["Labels"])
		write(map[string]string{"Id": name + "-id"})
		return
	}
	if len(parts) >= 2 && parts[0] == "networks" {
		name := parts[1]
		if _, ok := f.networks[name]; !ok && strings.HasSuffix(name, "-id") {
			name = strings.TrimSuffix(name, "-id")
		}
		labels, ok := f.networks[name]
		if !ok {
			notFound()
			return
		}
		if len(parts) == 3 {
			v := decode()
			if parts[2] == "connect" {
				f.gatewayConnects++
				f.lastGatewayConnect = v
				if f.gatewayConnectErr == 0 || f.gatewayRaceConnect {
					f.gatewayNetworks[name] = int(v["EndpointConfig"].(map[string]any)["GwPriority"].(float64))
				}
				if f.gatewayReplaceOnConnect {
					f.gatewayID += "-replaced"
				}
				if f.gatewayConnectErr != 0 {
					w.WriteHeader(f.gatewayConnectErr)
					write(map[string]any{"message": "connect conflict"})
					return
				}
			} else if parts[2] == "disconnect" {
				delete(f.gatewayNetworks, name)
			}
			write(map[string]any{})
			return
		}
		if r.Method == "DELETE" {
			delete(f.networks, name)
			delete(f.gatewayNetworks, name)
			w.WriteHeader(204)
			return
		}
		id := name + "-id"
		if f.networkIDs[name] != "" {
			id = f.networkIDs[name]
		}
		write(map[string]any{"Id": id, "Name": name, "Driver": "bridge", "Labels": labels, "Internal": true})
		return
	}
	if p == "/volumes/create" {
		v := decode()
		name := v["Name"].(string)
		f.volumes[name] = stringMap(v["Labels"])
		write(map[string]string{"Name": name})
		return
	}
	if len(parts) == 2 && parts[0] == "volumes" {
		name := parts[1]
		labels, ok := f.volumes[name]
		if !ok {
			notFound()
			return
		}
		if r.Method == "DELETE" {
			delete(f.volumes, name)
			w.WriteHeader(204)
			return
		}
		write(map[string]any{"Name": name, "Driver": "local", "Mountpoint": "/var/lib/docker/volumes/" + name + "/_data", "Labels": labels})
		return
	}
	if p == "/containers/create" {
		v := decode()
		name := r.URL.Query().Get("name")
		network := v["HostConfig"].(map[string]any)["NetworkMode"].(string)
		isMeter := stringMap(v["Labels"])["pogo.web.role"] == "storage-meter"
		if isMeter {
			f.meterCreates++
			f.lastMeterCreate = v
		} else if strings.HasSuffix(name, "-init") {
			f.initCreates++
			f.lastInitCreate = v
		} else {
			f.creates++
			f.lastCreate = v
		}
		f.containers[name] = &fakeDockerRuntime{ID: "id-" + name, Name: name, Network: network, Labels: stringMap(v["Labels"]), Files: map[string]map[string][]byte{}}
		if isMeter {
			values := []int64{}
			for _, mount := range v["HostConfig"].(map[string]any)["Mounts"].([]any) {
				source := mount.(map[string]any)["Source"].(string)
				if mount.(map[string]any)["Type"] == "bind" {
					source = filepath.Base(filepath.Dir(source))
				}
				values = append(values, f.storageSizes[source])
			}
			f.containers[name].MeterOutput, _ = json.Marshal(map[string]any{"bytes": values})
		}
		write(map[string]string{"Id": "id-" + name})
		return
	}
	if len(parts) >= 2 && parts[0] == "containers" {
		name := parts[1]
		c, ok := f.containers[name]
		if !ok {
			notFound()
			return
		}
		if len(parts) == 2 && r.Method == "DELETE" {
			delete(f.containers, name)
			w.WriteHeader(204)
			return
		}
		if len(parts) != 3 {
			notFound()
			return
		}
		switch parts[2] {
		case "json":
			write(map[string]any{"Id": c.ID, "Config": map[string]any{"Labels": c.Labels}, "State": map[string]any{"Running": c.Running, "Status": "running", "ExitCode": 0}, "NetworkSettings": map[string]any{"Networks": map[string]any{c.Network: map[string]any{"NetworkID": c.Network + "-id", "IPAddress": "172.29.0.2"}}}})
		case "archive":
			if r.URL.Query().Get("copyUIDGID") != "false" {
				http.Error(w, "preserving sanitized archive UID/GID is required", 400)
				return
			}
			if f.archiveDelay > 0 {
				time.Sleep(f.archiveDelay)
			}
			target := r.URL.Query().Get("path")
			if c.Files[target] == nil {
				c.Files[target] = map[string][]byte{}
			}
			tr := tar.NewReader(r.Body)
			for {
				h, err := tr.Next()
				if err == io.EOF {
					break
				}
				if err != nil {
					http.Error(w, err.Error(), 400)
					return
				}
				if h.Typeflag == tar.TypeReg {
					data, _ := io.ReadAll(tr)
					c.Files[target][h.Name] = data
				}
			}
			w.WriteHeader(200)
		case "start":
			if c.Labels["pogo.web.role"] == "storage-meter" {
				c.Running = f.blockMeterWait
			} else if strings.HasSuffix(name, "-init") {
				f.initStarts++
				c.Running = f.blockInitWait
			} else {
				f.starts++
				c.Running = true
			}
			w.WriteHeader(204)
		case "wait":
			if c.Labels["pogo.web.role"] == "storage-meter" {
				if f.meterWaitEntered != nil {
					close(f.meterWaitEntered)
					f.meterWaitEntered = nil
				}
				if release := f.meterWaitRelease; release != nil {
					f.mu.Unlock()
					select {
					case <-release:
					case <-r.Context().Done():
						f.mu.Lock()
						return
					}
					f.mu.Lock()
				}
				if f.blockMeterWait {
					f.mu.Unlock()
					<-r.Context().Done()
					f.mu.Lock()
					return
				}
				write(map[string]any{"StatusCode": f.meterExit})
				return
			}
			if f.blockInitWait {
				if f.initWaitEntered != nil {
					close(f.initWaitEntered)
					f.initWaitEntered = nil
				}
				f.mu.Unlock()
				<-r.Context().Done()
				f.mu.Lock()
				return
			}
			write(map[string]any{"StatusCode": f.initExit})
		case "stop":
			f.stopGrace[name] = r.URL.Query().Get("t")
			c.Running = false
			if c.Labels["pogo.web.role"] == "storage-meter" && f.removeMeterOnStop {
				delete(f.containers, name)
			}
			w.WriteHeader(204)
		case "logs":
			if c.Labels["pogo.web.role"] == "storage-meter" {
				frame(c.MeterOutput)
				return
			}
			header := make([]byte, 8)
			header[0] = 1
			binary.BigEndian.PutUint32(header[4:], uint32(len(f.log)))
			_, _ = w.Write(header)
			_, _ = io.WriteString(w, f.log)
		case "exec":
			command := decode()["Cmd"].([]any)
			if len(command) > 3 && command[3].(string) == pythonStorageMeter {
				id := "meter-exec-" + name
				f.execOutputs[id], _ = json.Marshal(map[string]any{"bytes": []int64{f.storageTmp[name]}, "deleted": f.storageDeleted[name]})
				write(map[string]string{"Id": id})
				return
			}
			c.Files["/run/pogo-control"]["lease.json"] = c.Files["/run/pogo-control"]["lease.next"]
			write(map[string]string{"Id": "exec-one"})
		default:
			notFound()
		}
		return
	}
	if strings.HasPrefix(p, "/exec/") {
		id := strings.Split(strings.TrimPrefix(p, "/exec/"), "/")[0]
		if strings.HasSuffix(p, "/json") {
			exit := 0
			if _, ok := f.execOutputs[id]; ok {
				exit = f.meterExit
			}
			write(map[string]any{"Running": false, "ExitCode": exit})
		} else {
			w.WriteHeader(200)
			if data, ok := f.execOutputs[id]; ok {
				frame(data)
			}
		}
		return
	}
	notFound()
}

func runnerRecord() Record {
	return Record{ServiceID: "service-one", Owner: "owner-one", Session: "session-one", Installation: "qa-install", SpecDigest: "spec-one", Generation: 1, State: "preparing", LeaseExpiresAt: time.Now().Add(time.Minute), Spec: Spec{Name: "app", Profile: "python-web", StartArgv: []string{"python", "app.py"}}}
}
func projectTar(t *testing.T, name, content string, kind byte) []byte {
	t.Helper()
	var b bytes.Buffer
	w := tar.NewWriter(&b)
	h := &tar.Header{Name: name, Typeflag: kind, Mode: 07777, Uid: 0, Gid: 0, Size: int64(len(content))}
	if kind != tar.TypeReg {
		h.Size = 0
		h.Linkname = "/etc/passwd"
	}
	if err := w.WriteHeader(h); err != nil {
		t.Fatal(err)
	}
	if kind == tar.TypeReg {
		_, _ = w.Write([]byte(content))
	}
	_ = w.Close()
	return b.Bytes()
}

func TestDockerRunnerOwnedLifecycleAndStoppedLog(t *testing.T) {
	r, f := newFakeDocker(t)
	ctx := context.Background()
	rec := runnerRecord()
	archive := projectTar(t, "app.py", "print('hello')", tar.TypeReg)
	instance, err := r.Start(ctx, Launch{Record: rec, Archive: archive})
	if err != nil {
		t.Fatal(err)
	}
	rec.Instance = instance
	if instance.ID == "" || instance.Upstream != "172.29.0.2:8080" || strings.Contains(instance.Upstream, "://") {
		t.Fatalf("invalid instance: %+v", instance)
	}
	again, err := r.Start(ctx, Launch{Record: rec, Archive: archive})
	if err != nil || again != instance || f.creates != 1 {
		t.Fatalf("duplicate start: %+v %v creates=%d", again, err, f.creates)
	}
	f.mu.Lock()
	create := f.lastCreate
	f.mu.Unlock()
	host := create["HostConfig"].(map[string]any)
	if host["ReadonlyRootfs"] != true || host["Memory"] != float64(512<<20) || host["PortBindings"] != nil || create["ExposedPorts"] != nil {
		t.Fatal("runtime resource/port isolation missing")
	}
	if host["NetworkMode"] == "host" || create["User"] != "0:0" || !strings.Contains(pythonSupervisor, "os.setuid(1000)") {
		t.Fatal("supervisor ownership contract missing")
	}
	initHost := f.lastInitCreate["HostConfig"].(map[string]any)
	initCaps := initHost["CapAdd"].([]any)
	runtimeCaps := host["CapAdd"].([]any)
	if initHost["NetworkMode"] != "none" || initHost["ReadonlyRootfs"] != true || len(initCaps) != 1 || initCaps[0] != "CHOWN" || initHost["PortBindings"] != nil || len(initHost["Mounts"].([]any)) != 2 {
		t.Fatal("volume initializer confinement missing")
	}
	for _, cap := range runtimeCaps {
		if cap == "CHOWN" {
			t.Fatal("application supervisor received CHOWN")
		}
	}
	if f.initCreates != 1 || f.initStarts != 1 {
		t.Fatal("volume initialization was not idempotent")
	}
	status, err := r.Inspect(ctx, rec)
	if err != nil || !status.Running {
		t.Fatal(status, err)
	}
	page, err := r.Logs(ctx, rec, "", 5)
	if err != nil || page.Text != "log-o" {
		t.Fatal(page, err)
	}
	page2, err := r.Logs(ctx, rec, page.NextCursor, 64)
	if err != nil || page2.Text != "ne\nlog-two\n" {
		t.Fatal(page2, err)
	}
	if _, err := r.Logs(ctx, rec, "aW52YWxpZA", 64); err == nil {
		t.Fatal("invalid cursor accepted")
	}
	rec.LeaseExpiresAt = time.Now().Add(90 * time.Minute)
	if err := r.Renew(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if err := r.Stop(ctx, rec); err != nil {
		t.Fatal(err)
	}
	status, err = r.Inspect(ctx, rec)
	if err != nil || status.Exists {
		t.Fatal("stopped runtime still exists", status, err)
	}
	page, err = r.Logs(ctx, rec, "", 64)
	if err != nil || !page.EOF || page.Text != f.log {
		t.Fatal("stopped log unavailable", page, err)
	}
	if err := r.Stop(ctx, rec); err != nil {
		t.Fatal("repeat stop", err)
	}
	_, _, _, _, data := r.names(rec)
	if len(f.networks) != 0 || len(f.containers) != 0 || len(f.volumes) != 1 || f.volumes[data] == nil {
		t.Fatalf("unexpected retained resources: networks=%d containers=%d volumes=%d", len(f.networks), len(f.containers), len(f.volumes))
	}
	if _, err := r.Start(ctx, Launch{Record: rec, Archive: archive}); err == nil {
		t.Fatal("completed generation executed twice")
	}
	if err := r.DeleteData(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if len(f.volumes) != 0 {
		t.Fatal("owned data not deleted")
	}
	entries, err := os.ReadDir(r.opts.StateDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("conversation deletion retained private runner records/logs: %v %v", entries, err)
	}
}

func TestDockerRunnerRejectsIdentityMismatchAndUnstableEdge(t *testing.T) {
	r, f := newFakeDocker(t)
	rec := runnerRecord()
	ctx := context.Background()
	archive := projectTar(t, "app.py", "print(1)", tar.TypeReg)
	instance, err := r.Start(ctx, Launch{Record: rec, Archive: archive})
	if err != nil {
		t.Fatal(err)
	}
	rec.Instance = instance
	wrong := rec
	wrong.SpecDigest = "different"
	if err := r.Stop(ctx, wrong); err == nil {
		t.Fatal("mismatched spec stopped runtime")
	}
	status, err := r.Inspect(ctx, rec)
	if err != nil || !status.Running {
		t.Fatal("identity failure changed real runtime", status, err)
	}
	f.mu.Lock()
	f.priority = 0
	f.mu.Unlock()
	if err := r.Preflight(ctx); err == nil {
		t.Fatal("unstable gateway default route accepted")
	}
}

func TestDockerRunnerRestoresRecreatedGatewayWithoutRestartingApplication(t *testing.T) {
	for _, via := range []string{"inspect", "reconcile", "start-replay"} {
		t.Run(via, func(t *testing.T) {
			r, f := newFakeDocker(t)
			rec := startStorageFixture(t, r, runnerRecord())
			_, network, _, _, _ := r.names(rec)
			f.mu.Lock()
			f.gatewayID = "replacement-gateway"
			f.gatewayNetworks = map[string]int{}
			connects, starts, creates := f.gatewayConnects, f.starts, f.creates
			f.mu.Unlock()
			switch via {
			case "inspect":
				state, err := r.Inspect(context.Background(), rec)
				if err != nil || !state.Exists || !state.Running || state.Instance.ID != rec.Instance.ID {
					t.Fatalf("live instance was not preserved: %+v %v", state, err)
				}
			case "reconcile":
				// A fresh runner over the same private launch metadata must also
				// repair a gateway replacement while main is unavailable.
				restarted, err := NewDockerRunner(r.opts)
				if err != nil {
					t.Fatal(err)
				}
				defer restarted.Close()
				if err := restarted.ReconcileOwned(context.Background()); err != nil {
					t.Fatal(err)
				}
			case "start-replay":
				if _, err := r.Start(context.Background(), Launch{Record: rec, Archive: projectTar(t, "app.py", "print(1)", tar.TypeReg)}); err != nil {
					t.Fatal(err)
				}
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			priority, attached := f.gatewayNetworks[network]
			if !attached || priority != 0 || f.priority != 100 || f.starts != starts || f.creates != creates || f.gatewayConnects != connects+1 || len(f.stopGrace) != 0 || f.lastGatewayConnect["Container"] != "replacement-gateway" {
				t.Fatalf("recovery changed route/application or targeted a name: %+v starts=%d creates=%d connects=%d", f.lastGatewayConnect, f.starts, f.creates, f.gatewayConnects)
			}
		})
	}
}

func TestDockerRunnerGatewayRecoveryRejectsForeignAndCompletedGenerations(t *testing.T) {
	for _, change := range []string{"foreign-runtime", "foreign-network", "same-name-new-network", "missing-launch", "foreign-launch", "persisted-runtime-id", "terminal", "expired", "storage-failure", "stopping", "unstable-edge"} {
		t.Run(change, func(t *testing.T) {
			r, f := newFakeDocker(t)
			rec := startStorageFixture(t, r, runnerRecord())
			name, network, _, _, _ := r.names(rec)
			f.mu.Lock()
			f.gatewayID = "replacement-gateway"
			f.gatewayNetworks = map[string]int{}
			connects := f.gatewayConnects
			switch change {
			case "foreign-runtime":
				f.containers[name].Labels["pogo.web.owner"] = "foreign"
			case "foreign-network":
				f.networks[network]["pogo.web.session"] = "foreign"
			case "same-name-new-network":
				f.networkIDs = map[string]string{network: "different-network-id"}
			case "unstable-edge":
				f.priority = 0
			}
			f.mu.Unlock()
			persisted := rec
			switch change {
			case "missing-launch":
				if err := os.Remove(r.launchPath(rec)); err != nil {
					t.Fatal(err)
				}
			case "foreign-launch":
				persisted.SpecDigest = "foreign-spec"
			case "persisted-runtime-id":
				persisted.Instance.ID = "different-runtime-id"
			case "terminal":
				persisted.State = "stopped"
			case "expired":
				persisted.LeaseExpiresAt = time.Now().Add(-time.Second)
			case "stopping":
				rec.State = "stopping"
			case "storage-failure":
				if err := r.writeStorageSnapshot(rec, &StorageUsage{CheckedAt: time.Now()}, &Error{Code: "STORAGE_LIMIT_EXCEEDED", Message: "confirmed excess"}); err != nil {
					t.Fatal(err)
				}
			}
			if change != "missing-launch" {
				if err := r.saveLaunch(persisted, true); err != nil {
					t.Fatal(err)
				}
			}
			_, _ = r.Inspect(context.Background(), rec)
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.gatewayConnects != connects || len(f.gatewayNetworks) != 0 || !f.containers[name].Running || len(f.stopGrace) != 0 {
				t.Fatal("untrusted/completed generation caused reconnect or application mutation")
			}
		})
	}
}

func TestDockerRunnerGatewayRecoveryConfirmsAlreadyConnectedRace(t *testing.T) {
	for _, code := range []int{0, 403, 409} {
		for _, connected := range []bool{false, true} {
			t.Run(fmt.Sprintf("status-%d-connected-%t", code, connected), func(t *testing.T) {
				r, f := newFakeDocker(t)
				rec := startStorageFixture(t, r, runnerRecord())
				f.mu.Lock()
				f.gatewayNetworks = map[string]int{}
				f.gatewayConnectErr, f.gatewayRaceConnect = code, connected
				// A successful connect whose name was replaced before readback
				// must also be rejected, even if an endpoint happens to exist.
				f.gatewayReplaceOnConnect = code == 0 && !connected
				f.mu.Unlock()
				state, err := r.Inspect(context.Background(), rec)
				if (err == nil) != connected || !state.Exists || !state.Running {
					t.Fatalf("unverified connect accepted or runtime hidden: %+v %v", state, err)
				}
			})
		}
	}
}

func TestDockerRunnerGatewayRecoveryWaitIsBounded(t *testing.T) {
	r, f := newFakeDocker(t)
	rec := startStorageFixture(t, r, runnerRecord())
	f.mu.Lock()
	f.gatewayNetworks = map[string]int{}
	connects := f.gatewayConnects
	f.mu.Unlock()
	r.mu.Lock()
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	_, err := r.Inspect(ctx, rec)
	r.mu.Unlock()
	if !errors.Is(err, context.DeadlineExceeded) || f.gatewayConnects != connects {
		t.Fatalf("blocked lifecycle lock ignored cancellation: %v", err)
	}
}

func TestRunnerArchiveRejectsEscapesSecretsLinksAndDependencies(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		kind       byte
	}{{"../escape", "x", tar.TypeReg}, {".env", "KEY=secret", tar.TypeReg}, {"link", "", tar.TypeSymlink}, {"requirements.txt", "flask", tar.TypeReg}} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := runtimeArchive(projectTar(t, tc.name, tc.body, tc.kind)); err == nil {
				t.Fatal("unsafe archive accepted")
			}
		})
	}
	data, err := runtimeArchive(projectTar(t, "app.py", "print(1)", tar.TypeReg))
	if err != nil {
		t.Fatal(err)
	}
	reader := tar.NewReader(bytes.NewReader(data))
	_, _ = reader.Next()
	header, err := reader.Next()
	if err != nil || header.Uid != 1000 || header.Gid != 1000 || header.Mode != 0644 {
		t.Fatal("untrusted ownership/mode preserved", header, err)
	}
}

func TestDockerRunnerExpiredIntentIsNeverStarted(t *testing.T) {
	r, f := newFakeDocker(t)
	rec := runnerRecord()
	rec.LeaseExpiresAt = time.Now().Add(-time.Second)
	_, err := r.Start(context.Background(), Launch{Record: rec, Archive: projectTar(t, "app.py", "print(1)", tar.TypeReg)})
	var typed *Error
	if !errors.As(err, &typed) || typed.Code != "LEASE_EXPIRED" || f.creates != 0 {
		t.Fatalf("expired intent side effects: %v %d", err, f.creates)
	}
}

func TestDockerRunnerPreparationCannotStartAfterLeaseExpiry(t *testing.T) {
	r, f := newFakeDocker(t)
	f.archiveDelay = 40 * time.Millisecond
	rec := runnerRecord()
	rec.LeaseExpiresAt = time.Now().Add(80 * time.Millisecond)
	_, err := r.Start(context.Background(), Launch{Record: rec, Archive: projectTar(t, "app.py", "print(1)", tar.TypeReg)})
	var typed *Error
	if !errors.As(err, &typed) || typed.Code != "LEASE_EXPIRED" || f.creates != 1 || f.starts != 0 {
		t.Fatalf("expired preparation launched code: %v creates=%d starts=%d", err, f.creates, f.starts)
	}
	if strings.Index(pythonSupervisor, "initial_deadline") > strings.Index(pythonSupervisor, "subprocess.Popen") {
		t.Fatal("supervisor launches before checking initial lease")
	}
}

func TestDockerRunnerHistoryGCLeavesOnlyReplayTombstone(t *testing.T) {
	r, _ := newFakeDocker(t)
	ctx := context.Background()
	rec := runnerRecord()
	archive := projectTar(t, "app.py", "print(1)", tar.TypeReg)
	instance, err := r.Start(ctx, Launch{Record: rec, Archive: archive})
	if err != nil {
		t.Fatal(err)
	}
	rec.Instance = instance
	if err := r.Stop(ctx, rec); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(r.launchPath(rec))
	if err != nil {
		t.Fatal(err)
	}
	var stopped Record
	if err := json.Unmarshal(b, &stopped); err != nil {
		t.Fatal(err)
	}
	stopped.UpdatedAt = time.Now().Add(-8 * 24 * time.Hour)
	if err := r.saveLaunch(stopped, true); err != nil {
		t.Fatal(err)
	}
	if err := r.ReconcileOwned(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(r.logPath(rec)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired log retained: %v", err)
	}
	b, err = os.ReadFile(r.launchPath(rec))
	if err != nil {
		t.Fatal(err)
	}
	stopped = Record{}
	if json.Unmarshal(b, &stopped) != nil || len(stopped.Spec.StartArgv) > 0 || stopped.Owner != rec.Owner {
		t.Fatal("GC lost identity or retained command payload")
	}
	if _, err := r.Start(ctx, Launch{Record: rec, Archive: archive}); err == nil {
		t.Fatal("expired historical generation replayed")
	}
}

func TestDockerRunnerInitializerFailurePreventsApplicationStart(t *testing.T) {
	r, f := newFakeDocker(t)
	f.initExit = 1
	rec := runnerRecord()
	_, err := r.Start(context.Background(), Launch{Record: rec, Archive: projectTar(t, "app.py", "print(1)", tar.TypeReg)})
	var typed *Error
	if !errors.As(err, &typed) || typed.Code != "RUNTIME_INIT_FAILED" || f.creates != 0 || f.starts != 0 || len(f.containers) != 0 {
		t.Fatalf("failed initializer launched app or leaked: %v %+v", err, f.containers)
	}
	if err := r.Stop(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
}

func TestDockerRunnerCancelledInitializerIsCleanedWithIndependentContext(t *testing.T) {
	r, f := newFakeDocker(t)
	f.blockInitWait = true
	entered := make(chan struct{})
	f.initWaitEntered = entered
	rec := runnerRecord()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := r.Start(ctx, Launch{Record: rec, Archive: projectTar(t, "app.py", "print(1)", tar.TypeReg)})
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("initializer did not enter wait")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled initializer succeeded")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled initializer did not settle")
	}
	f.mu.Lock()
	leaked := len(f.containers)
	created := f.creates
	f.mu.Unlock()
	if leaked != 0 || created != 0 {
		t.Fatalf("cancelled initializer leaked/started app: %d %d", leaked, created)
	}
	if err := r.Stop(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
}

func TestDockerRunnerRecoveryCleansOwnedInterruptedInitializer(t *testing.T) {
	r, f := newFakeDocker(t)
	rec := runnerRecord()
	if err := r.saveLaunch(rec, false); err != nil {
		t.Fatal(err)
	}
	name, _, _, _, _ := r.names(rec)
	name += "-init"
	labels := r.labels(rec, true)
	labels["pogo.web.role"] = "volume-init"
	f.mu.Lock()
	f.containers[name] = &fakeDockerRuntime{ID: "orphan-init", Name: name, Labels: labels, Running: true}
	f.mu.Unlock()
	if err := r.ReconcileOwned(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	remaining := len(f.containers)
	f.mu.Unlock()
	if remaining != 0 {
		t.Fatal("owned initializer remained after recovery")
	}
	labels["pogo.web.owner"] = "foreign"
	f.mu.Lock()
	f.containers[name] = &fakeDockerRuntime{ID: "foreign-init", Name: name, Labels: labels}
	f.mu.Unlock()
	if err := r.ReconcileOwned(context.Background()); err == nil {
		t.Fatal("foreign initializer was accepted")
	}
	f.mu.Lock()
	remaining = len(f.containers)
	f.mu.Unlock()
	if remaining != 1 {
		t.Fatal("foreign initializer was deleted")
	}
}
