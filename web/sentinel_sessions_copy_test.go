package web

// Read-only compatibility acceptance for a private, consistent deployment
// snapshot. Layout for POGO_SENTINEL_SESSION_COPY is the session directory:
//
//   agent-studio.json
//   <creator-session-id>.jsonl
//   <worker-session-id>.jsonl
//   [other same-owner/Agent candidate transcripts]
//   sentinels/events.db  (consistent SQLite backup, no live WAL dependency)
//
// Set POGO_SENTINEL_SESSION_WATCH and POGO_SENTINEL_SESSION_EXPECT_PARENT.
// This test first copies these inputs again into t.TempDir, never opens the
// supplied database for writing, never starts a Manager/runtime loop and never
// loads a session/model or accesses a saved Linux workspace/script path.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/yosukeno/pogo/sentinel"
)

func TestSentinelSessionCopiedDeploymentCompatibility(t *testing.T) {
	input := os.Getenv("POGO_SENTINEL_SESSION_COPY")
	if input == "" {
		t.Skip("private copied-state acceptance is opt-in")
	}
	id, expected := os.Getenv("POGO_SENTINEL_SESSION_WATCH"), os.Getenv("POGO_SENTINEL_SESSION_EXPECT_PARENT")
	if !validID(id) || !validID(expected) {
		t.Fatal("set valid watch and expected creator session IDs")
	}
	dir := t.TempDir()
	saved := map[string][]byte{}
	files, err := os.ReadDir(input)
	if err != nil {
		t.Fatal(err)
	}
	copyFile := func(rel string) {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(input, rel))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, rel)), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, rel), data, 0600); err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(rel, ".jsonl") || rel == "agent-studio.json" {
			saved[rel] = data
		}
	}
	copyFile("agent-studio.json")
	copyFile("sentinels/events.db")
	for _, file := range files {
		if !file.IsDir() && strings.HasSuffix(file.Name(), ".jsonl") {
			copyFile(file.Name())
		}
	}
	var registry studioData
	if err := json.Unmarshal(saved["agent-studio.json"], &registry); err != nil {
		t.Fatal(err)
	}
	db, err := sentinel.Open(filepath.Join(dir, "sentinels/events.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	m := &Manager{cfg: Config{SessionDir: dir}, studio: &studioStore{data: registry}, sessions: map[string]*Session{}}
	m.sentinels = newSentinelRuntime(m)
	m.sentinels.store = db
	defer m.sentinels.cancel()
	before, err := db.SessionRecords()
	if err != nil {
		t.Fatal(err)
	}
	var watch *sentinel.Watch
	for i := range before {
		if before[i].Spec.ID == id {
			watch = &before[i]
		}
	}
	if watch == nil || watch.Spec.SessionID == "" {
		t.Fatal("copied watch/worker not found")
	}
	if parent := m.sentinelParentSession(*watch); parent != expected {
		t.Fatalf("creator mismatch: watch=%s expected=%s actual=%s", id, expected, parent)
	}
	if !m.nativeSentinelRegistration(expected, watch.Spec) {
		t.Fatal("expected creator lacks exact native creation evidence")
	}
	role, err := m.sentinelIdentity(watch.Spec.SessionID)
	if err != nil || role == nil || role.WatchID != id || role.ParentSessionID != expected {
		t.Fatalf("legacy worker role mismatch: %+v %v", role, err)
	}
	infos, err := m.List()
	if err != nil {
		t.Fatal(err)
	}
	for _, info := range infos {
		if info.ID == watch.Spec.SessionID {
			t.Fatal("copied legacy worker leaked into ordinary conversations")
		}
	}
	after, err := db.SessionRecords()
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("read projection changed native task/event state", err)
	}
	for rel, data := range saved {
		after, err := os.ReadFile(filepath.Join(dir, rel))
		if err != nil || !bytes.Equal(data, after) {
			t.Fatalf("read projection rewrote %s", rel)
		}
	}
	// Force a fresh manager/cache against the same retained data. No migration
	// metadata has been written to manufacture the successful second lookup.
	m.sentinelMarkers = sync.Map{}
	m.sentinelParents = sync.Map{}
	m.sentinelWakeSessions = sync.Map{}
	if parent := m.sentinelParentSession(*watch); parent != expected {
		t.Fatal("creator lookup depended on a warm cache")
	}
	t.Logf("copied compatibility passed: watch=%s parent=%s worker=%s ordinary=%d untouched_transcripts=%d", id, expected, watch.Spec.SessionID, len(infos), len(saved)-1)
}
