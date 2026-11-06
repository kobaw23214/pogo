package webservice

import (
	"archive/tar"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

const runnerNodeTestImage = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

func enableFakeNode(r *DockerRunner, f *fakeDocker) {
	r.opts.NodeImage = runnerNodeTestImage
	f.mu.Lock()
	defer f.mu.Unlock()
	f.imageLabels = map[string]map[string]string{runnerNodeTestImage: {
		"io.pogo.web.profile": "node-web", "io.pogo.web.node": r.opts.NodePath, "io.pogo.web.python": r.opts.NodePythonPath,
	}}
}

func TestDockerRunnerProfilesOnlyAdvertiseConfiguredReviewedNode(t *testing.T) {
	r, f := newFakeDocker(t)
	ctx := context.Background()
	profiles, err := r.Profiles(ctx)
	if err != nil || strings.Join(profiles, ",") != "python-web" {
		t.Fatal(profiles, err)
	}
	r.opts.NodeImage = runnerNodeTestImage
	profiles, err = r.Profiles(ctx)
	if err != nil || strings.Join(profiles, ",") != "python-web" {
		t.Fatal("unreviewed Node image advertised", profiles, err)
	}
	enableFakeNode(r, f)
	profiles, err = r.Profiles(ctx)
	if err != nil || strings.Join(profiles, ",") != "python-web,node-web" {
		t.Fatal(profiles, err)
	}
}

func TestDockerRunnerNodeUsesConfiguredPortResourcesAndTrustedSupervisor(t *testing.T) {
	r, f := newFakeDocker(t)
	enableFakeNode(r, f)
	rec := runnerRecord()
	rec.Spec.Profile, rec.Spec.StartArgv = "node-web", []string{"node", "app.js", "{{HOST}}", "{{PORT}}"}
	rec.Runtime = RuntimeConfig{InternalPort: 9234, CPUs: 1.5, MemoryMiB: 768}
	instance, err := r.Start(context.Background(), Launch{Record: rec, Archive: projectTar(t, "app.js", "require('node:http')", tar.TypeReg)})
	if err != nil {
		t.Fatal(err)
	}
	if instance.Upstream != "172.29.0.2:9234" {
		t.Fatal(instance)
	}
	f.mu.Lock()
	body := f.lastCreate
	f.mu.Unlock()
	if body["Image"] != runnerNodeTestImage {
		t.Fatal("wrong runtime image", body["Image"])
	}
	entry := body["Entrypoint"].([]any)
	if entry[0] != r.opts.NodePythonPath || entry[3] != pythonSupervisor {
		t.Fatal("Node bypassed trusted lease/network supervisor")
	}
	host := body["HostConfig"].(map[string]any)
	if host["Memory"] != float64(768<<20) || host["NanoCpus"] != float64(1_500_000_000) || host["PortBindings"] != nil {
		t.Fatal("configured resources or shared-gateway isolation missing", host)
	}
	name, _, _, _, _ := r.names(rec)
	var config struct{ Argv []string }
	if err := json.Unmarshal(f.containers[name].Files["/run/pogo-control"]["config.json"], &config); err != nil {
		t.Fatal(err)
	}
	if strings.Join(config.Argv, "|") != r.opts.NodePath+"|app.js|0.0.0.0|9234" {
		t.Fatal(config.Argv)
	}
	rec.Instance = instance
	rec.LeaseExpiresAt = time.Now().Add(48 * time.Hour)
	if err := r.Renew(context.Background(), rec); err != nil {
		t.Fatal("longer Node lease failed", err)
	}
}

func TestDockerRunnerRejectsUnconfiguredNodeAndInvalidRuntime(t *testing.T) {
	for _, tc := range []struct {
		profile string
		runtime RuntimeConfig
	}{
		{"node-web", RuntimeConfig{}},
		{"python-web", RuntimeConfig{InternalPort: 80, CPUs: 1, MemoryMiB: 512}},
		{"python-web", RuntimeConfig{InternalPort: 8080, CPUs: 65, MemoryMiB: 512}},
		{"python-web", RuntimeConfig{InternalPort: 8080, CPUs: 1, MemoryMiB: 65537}},
	} {
		t.Run(tc.profile, func(t *testing.T) {
			r, f := newFakeDocker(t)
			rec := runnerRecord()
			rec.Spec.Profile, rec.Runtime = tc.profile, tc.runtime
			_, err := r.Start(context.Background(), Launch{Record: rec, Archive: projectTar(t, "app.js", "", tar.TypeReg)})
			if err == nil || f.creates != 0 || f.initCreates != 0 {
				t.Fatal("unavailable/invalid profile created runtime", err)
			}
		})
	}
}

func TestNodeArchiveAllowsESMMetadataButRejectsDependencyInstallation(t *testing.T) {
	for _, tc := range []struct {
		manifest string
		valid    bool
	}{
		{`{"type":"module","scripts":{"start":"node app.js"}}`, true},
		{`{"dependencies":{}}`, true},
		{`{"dependencies":{"express":"*"}}`, false},
		{`{"devDependencies":{"vite":"*"}}`, false},
		{`{"optionalDependencies":{"pkg":"*"}}`, false},
		{`{"dependencies":[]}`, false},
		{`invalid-json`, false},
	} {
		err := validateNodeArchive(projectTar(t, "package.json", tc.manifest, tar.TypeReg))
		if (err == nil) != tc.valid {
			t.Fatal(tc.manifest, err)
		}
	}
}
