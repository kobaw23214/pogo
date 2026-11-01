package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/sentinelsdk"
	"github.com/yosukeno/pogo/tools"
)

// Exact SDK 1 bytes sealed by the preceding host, not a stand-in library.
//
//go:embed testdata/pogo_watch_v1.py
var sentinelSDKV1Source []byte

func TestSentinelSDKV1BundleRemainsPinnedAndPrepareNeverOverwritesIt(t *testing.T) {
	const originalHash = "fc0f77768749e51cc7d0799f98da7e01c36427c4e6738ea4fa7f0a69a69ba8f5"
	digest := sha256.Sum256(sentinelSDKV1Source)
	if hex.EncodeToString(digest[:]) != originalHash || bytes.Equal(sentinelSDKV1Source, sentinelsdk.Source()) {
		t.Fatal("SDK 1 compatibility fixture changed or no longer distinguishes helper versions")
	}
	dir := sealedBundleFixture(t)
	manifestPath := filepath.Join(dir, ".pogo-watch-manifest.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var bundle sentinelBundle
	if err := json.Unmarshal(raw, &bundle); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, sentinelsdk.FileName), sentinelSDKV1Source, 0600); err != nil {
		t.Fatal(err)
	}
	bundle.Files[sentinelsdk.FileName], bundle.SDKVersion = originalHash, "1"
	raw, _ = json.Marshal(bundle)
	if err := os.WriteFile(manifestPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := verifySentinelBundle(dir); err != nil {
		t.Fatal("real previously sealed SDK 1 bundle no longer verifies", err)
	}
	if after, err := os.ReadFile(filepath.Join(dir, sentinelsdk.FileName)); err != nil || !bytes.Equal(after, sentinelSDKV1Source) {
		t.Fatal("verification upgraded a pinned SDK", err)
	}

	h, capture := newSentinelHarness(t, nil)
	s := sentinelTestSession(t, h)
	prepared := sentinelSDKPrepare(t, h, s, "watchers/legacy/main.py", "poll")
	entryPath, sdkPath := filepath.Join(s.cwd, prepared.ScriptPath), filepath.Join(s.cwd, prepared.SDKPath)
	entry, err := os.ReadFile(entryPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sdkPath, sentinelSDKV1Source, 0600); err != nil {
		t.Fatal(err)
	}
	b := h.mgr.sentinelBackend(s).(*sentinelToolBackend)
	_, err = b.prepareSDK(context.Background(), tools.WatchRequest{ScriptPath: prepared.ScriptPath, Mode: "poll"})
	if err == nil || !strings.Contains(err.Error(), "版本不一致") || !strings.Contains(err.Error(), "已确认进度") || !strings.Contains(err.Error(), "不会覆盖") {
		t.Fatalf("old valid SDK was misdiagnosed or implicitly upgraded: %v", err)
	}
	if after, err := os.ReadFile(sdkPath); err != nil || !bytes.Equal(after, sentinelSDKV1Source) {
		t.Fatal("prepare overwrote SDK 1", err)
	}
	if after, err := os.ReadFile(entryPath); err != nil || !bytes.Equal(after, entry) {
		t.Fatal("prepare changed the legacy business entry", err)
	}
	sentinelSDKAssertNoWatching(t, h, capture)
}

func TestSentinelSDKSavedBundleKeepsItsOwnPinnedVersion(t *testing.T) {
	dir := sealedBundleFixture(t)
	manifestPath := filepath.Join(dir, ".pogo-watch-manifest.json")
	raw, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var bundle sentinelBundle
	if err := json.Unmarshal(raw, &bundle); err != nil {
		t.Fatal(err)
	}
	legacy := []byte("# A library sealed by an earlier host version.\n")
	if err := os.WriteFile(filepath.Join(dir, sentinelsdk.FileName), legacy, 0600); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(legacy)
	bundle.Files[sentinelsdk.FileName] = hex.EncodeToString(digest[:])
	bundle.SDKVersion = "previous-host-version"
	raw, _ = json.Marshal(bundle)
	if err := os.WriteFile(manifestPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := verifySentinelBundle(dir); err != nil {
		t.Fatal("a saved version must not be invalidated by today's embedded SDK", err)
	}
	if err := os.WriteFile(filepath.Join(dir, sentinelsdk.FileName), []byte("# altered after sealing\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := verifySentinelBundle(dir); err == nil {
		t.Fatal("a saved old SDK may not bypass its original manifest integrity check")
	}
}
