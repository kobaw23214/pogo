//go:build unix

package web

import (
	"net/http"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestSpillBrowseRejectsFIFOWithoutBlocking(t *testing.T) {
	dir := sweepEnv(t)
	if err := syscall.Mkfifo(filepath.Join(dir, "fifo"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := prefsTestServer(t)
	server.Client().Timeout = 2 * time.Second
	response := spillBrowseRequest(t, server, "/api/prefs/spill/file?path=fifo", nil)
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("FIFO was not rejected: %d", response.StatusCode)
	}
	var page spillBrowseEntries
	spillBrowseRequest(t, server, "/api/prefs/spill/entries", &page)
	if len(page.Entries) != 0 || page.Skipped != 1 {
		t.Fatalf("FIFO appeared as a normal file: %+v", page)
	}
	if err := os.Symlink("/dev/zero", filepath.Join(dir, "device")); err != nil {
		t.Fatal(err)
	}
	response = spillBrowseRequest(t, server, "/api/prefs/spill/file?path=device", nil)
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("device symlink was not rejected: %d", response.StatusCode)
	}
}
