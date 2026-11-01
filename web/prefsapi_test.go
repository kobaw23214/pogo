package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/yosukeno/pogo/prefs"
)

// The settings dialog's storage section talks to these three endpoints. The
// sweep behaviour itself is covered in spillsweep_test.go; here the contract
// is the HTTP shape.

func prefsTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	mgr := sweepManager(t, 0)
	srv, err := NewServer(mgr, ServerOptions{Token: "test-token"})
	if err != nil {
		t.Fatal(err)
	}
	front := httptest.NewServer(srv.Handler())
	t.Cleanup(front.Close)
	return front
}

func prefsRequest(t *testing.T, front *httptest.Server, method, path, body string) (int, map[string]any) {
	t.Helper()
	var rd *strings.Reader = strings.NewReader(body)
	req, err := http.NewRequest(method, front.URL+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer test-token")
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestPrefsEndpointsRoundTrip(t *testing.T) {
	dir := sweepEnv(t)
	agedSpill(t, dir, "bash-aaaaaa.log", 64, time.Now())
	front := prefsTestServer(t)

	// GET: defaults (no file written yet) plus the directory's current cost.
	code, got := prefsRequest(t, front, "GET", "/api/prefs", "")
	if code != http.StatusOK {
		t.Fatalf("GET /api/prefs: %d", code)
	}
	p, _ := got["prefs"].(map[string]any)
	if p["spill_retention_days"] != 7.0 {
		t.Errorf("retention = %v, want the 7-day default", p["spill_retention_days"])
	}
	spill, _ := got["spill"].(map[string]any)
	if spill["files"] != 1.0 || spill["bytes"] != 64.0 {
		t.Errorf("spill usage = %v, want 1 file of 64 bytes", spill)
	}
	if spill["dir"] != dir {
		t.Errorf("spill dir = %v, want %s", spill["dir"], dir)
	}

	// PUT persists; the sweeper and -spill-prune read the same file.
	code, _ = prefsRequest(t, front, "PUT", "/api/prefs", `{"spill_retention_days": 30}`)
	if code != http.StatusOK {
		t.Fatalf("PUT /api/prefs: %d", code)
	}
	loaded, err := readPrefsFile()
	if err != nil || loaded != 30 {
		t.Errorf("prefs on disk = %d, %v; want 30", loaded, err)
	}

	// A value Validate rejects never reaches the disk.
	code, _ = prefsRequest(t, front, "PUT", "/api/prefs", `{"spill_retention_days": -1}`)
	if code != http.StatusBadRequest {
		t.Errorf("PUT with -1 days: %d, want 400", code)
	}
	if loaded, _ = readPrefsFile(); loaded != 30 {
		t.Errorf("the rejected PUT overwrote the file: %d", loaded)
	}
}

func TestPruneSpillEndpointRunsUnderCurrentRetention(t *testing.T) {
	dir := sweepEnv(t)
	writePrefs(t, 7)
	now := time.Now()
	old := agedSpill(t, dir, "bash-old0.log", 10, now.Add(-10*24*time.Hour))
	fresh := agedSpill(t, dir, "bash-new0.log", 10, now)
	front := prefsTestServer(t)

	code, got := prefsRequest(t, front, "POST", "/api/prefs/prune-spill", "")
	if code != http.StatusOK {
		t.Fatalf("POST prune-spill: %d (%v)", code, got)
	}
	if got["removed"] != 1.0 || got["bytes"] != 10.0 {
		t.Errorf("result = %v, want one removal of 10 bytes", got)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Error("the old spill survived the manual prune")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Error("the fresh spill was pruned")
	}

	// The manual pass counts as the last sweep the GET view reports — the
	// dialog refreshes from GET after the button returns.
	_, got = prefsRequest(t, front, "GET", "/api/prefs", "")
	sw, _ := got["last_sweep"].(map[string]any)
	if sw == nil || sw["removed"] != 1.0 {
		t.Errorf("last_sweep = %v, want the manual prune recorded", sw)
	}
}

func TestPruneSpillEndpointRefusesWhenDisabledOrBroken(t *testing.T) {
	sweepEnv(t)
	writePrefs(t, 0)
	front := prefsTestServer(t)

	// 0 means disabled: pruning with a zero window would delete everything,
	// so the button answers with a readable refusal instead.
	code, got := prefsRequest(t, front, "POST", "/api/prefs/prune-spill", "")
	if code != http.StatusBadRequest {
		t.Errorf("prune with retention 0: %d, want 400 (%v)", code, got)
	}

	if err := os.WriteFile(prefs.Path(), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _ = prefsRequest(t, front, "POST", "/api/prefs/prune-spill", "")
	if code == http.StatusOK {
		t.Error("prune under broken prefs: 200, want a refusal — never guess a retention")
	}
}

func readPrefsFile() (int, error) {
	data, err := os.ReadFile(prefs.Path())
	if err != nil {
		return 0, err
	}
	var v struct {
		Days int `json:"spill_retention_days"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		return 0, err
	}
	return v.Days, nil
}
