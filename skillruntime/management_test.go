package skillruntime

import (
	"bytes"
	"context"
	"errors"
	"io"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/internal/testfs"
)

func TestManagementInstallPersistsWithoutExecutor(t *testing.T) {
	t.Setenv("PATH", "") // Management must not even locate a sandbox executable.
	s, err := NewManagementService(testfs.SealedTempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	// This test validates management without a process executor; keep its
	// synthetic amd64 package independent of the machine running the test.
	platform := detectPlatform("linux", "amd64", "")
	s.platform = &platform
	installed, _ := installSkillFixture(t, s)
	if s.Executor != nil || installed.RuntimeCount == nil || *installed.RuntimeCount != 1 || installed.AssetCount == nil || *installed.AssetCount != 0 || len(installed.Capabilities) != 1 || installed.PackageSizeBytes <= 0 || installed.InstalledAt == "" {
		t.Fatalf("incorrect offline installation metadata: %+v", installed)
	}
	plan, err := s.PlanSelection(Selection{SkillIDs: []string{PackageSkillID(installed.Ref)}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ValidateBinding(plan.Binding); err != nil {
		t.Fatal(err)
	}
	before := s.List()
	*installed.RuntimeCount = 99
	installed.Capabilities[0] = "caller mutation"
	if !reflect.DeepEqual(before, s.List()) {
		t.Fatal("returned metadata mutated the trusted catalog")
	}
	reopened, err := NewManagementService(s.Root)
	if err != nil || !reflect.DeepEqual(before, reopened.Catalog()) {
		t.Fatalf("catalog did not survive reload: %v", err)
	}
	if err := reopened.Revoke(before[0].Ref.PackageDigest); err != nil {
		t.Fatal(err)
	}
	again, err := NewManagementService(s.Root)
	if err != nil || !again.List()[0].Revoked {
		t.Fatalf("revocation did not persist: %v", err)
	}
	if _, err := NewService(context.Background(), t.TempDir()); err == nil {
		t.Fatal("explicit executor configuration silently degraded to management-only")
	}
}

func TestManagementExecutionFailsClosed(t *testing.T) {
	s, err := NewManagementService(testfs.SealedTempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	platform := detectPlatform("linux", "amd64", "")
	s.platform = &platform
	checks := []func() error{
		func() error { return s.Prepare(context.Background(), nil) },
		func() error { _, err := s.MakeLock(nil, "test"); return err },
		func() error { _, err := s.Session(nil, nil, "test"); return err },
		func() error {
			_, err := (&BoundSession{service: s}).Call(context.Background(), "anything", nil)
			return err
		},
	}
	for i, check := range checks {
		if err := check(); err == nil || !strings.Contains(err.Error(), "HOST_CAPABILITY_MISSING") {
			t.Fatalf("entrypoint %d: %v", i, err)
		}
	}
}

type interruptedUpload struct{}

func (interruptedUpload) Read([]byte) (int, error) { return 0, errors.New("client disconnected") }

func TestManagementInterruptedStreamPublishesNothing(t *testing.T) {
	s, err := NewManagementService(testfs.SealedTempDir(t))
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.Import(io.MultiReader(bytes.NewReader([]byte("partial ZIP bytes")), interruptedUpload{}))
	if err == nil || !strings.Contains(err.Error(), "client disconnected") {
		t.Fatal(err)
	}
	if len(s.List()) != 0 {
		t.Fatal("interrupted upload reached the catalog")
	}
	files, err := filepath.Glob(filepath.Join(s.Root, ".upload-*"))
	if err != nil || len(files) != 0 {
		t.Fatalf("upload staging leaked: %v, %v", files, err)
	}
}
