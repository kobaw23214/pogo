//go:build !unix

package webgateway

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSocketOwnedByProcessUnsupportedPlatformFailsClosed(t *testing.T) {
	info, err := os.Stat(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	err = socketOwnedByProcess(info)
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("unexpected platform result: %v", err)
	}
	if err := privateSocketParent(filepath.Join(t.TempDir(), "admin.sock")); err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("admin management was not disabled: %v", err)
	}
}
