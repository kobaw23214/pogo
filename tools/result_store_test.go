package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestResultStoreRoundTripIdentityPermissionsAndIntegrity(t *testing.T) {
	store := newResultStore(filepath.Join(t.TempDir(), "results"))
	original := StoredToolResult{
		Tool: "mcp_example", IsError: true, Error: "remote failed",
		Text: "完整文本\n" + strings.Repeat("tail\n", 100), TextComplete: false,
		Structured: json.RawMessage(`{"rows":[{"id":9007199254740993}]}`),
		Native:     json.RawMessage(`{"content":[{"type":"text","text":"native"}]}`),
	}
	ref, err := store.Save(original)
	if err != nil {
		t.Fatal(err)
	}
	if !validResultID(ref.ID) || ref.Complete || ref.TextBytes != int64(len(original.Text)) {
		t.Fatalf("invalid reference: %+v", ref)
	}
	loaded, err := store.Load(context.Background(), ref.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ID != ref.ID || loaded.Version != 1 || loaded.Text != original.Text || !loaded.IsError || loaded.TextComplete || loaded.Error != original.Error || string(loaded.Structured) != string(original.Structured) || string(loaded.Native) != string(original.Native) {
		t.Fatalf("round trip changed observation: %+v", loaded)
	}
	// Host version/ID fields cannot influence the content identity.
	original.Version, original.ID = 999, "model-chosen"
	again, err := store.Save(original)
	if err != nil || again.ID != ref.ID {
		t.Fatalf("identity changed: %+v, %v", again, err)
	}
	path := filepath.Join(store.dir, "result-"+ref.ID[1:]+".log")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("record permissions: %v, %v", info, err)
	}
	if err := os.WriteFile(path, []byte(`{"version":1,"text":"tampered"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(context.Background(), ref.ID); err == nil || !strings.Contains(err.Error(), "integrity") {
		t.Fatalf("tampered record accepted: %v", err)
	}
}

func TestResultStoreRejectsMalformedExpiredSymlinkAndWrongVersion(t *testing.T) {
	store := newResultStore(filepath.Join(t.TempDir(), "results"))
	for _, id := range []string{"", "../result.log", "R" + strings.Repeat("A", 64), "R" + strings.Repeat("0", 63)} {
		if _, err := store.Load(context.Background(), id); err == nil {
			t.Fatalf("accepted ID %q", id)
		}
	}
	missingID := "R" + strings.Repeat("0", 64)
	if _, err := store.Load(context.Background(), missingID); err == nil {
		t.Fatal("missing result accepted")
	}
	ref, err := store.Save(StoredToolResult{Tool: "action", Text: "original", TextComplete: true})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.dir, "result-"+ref.ID[1:]+".log")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.log")
	if err := os.WriteFile(outside, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(context.Background(), ref.ID); err == nil {
		t.Fatal("followed a result symlink outside the scope")
	}
	wrongVersion := []byte(`{"version":2,"tool":"action","text":"version two","text_complete":true}`)
	sum := sha256.Sum256(wrongVersion)
	wrongID := "R" + hex.EncodeToString(sum[:])
	if err := os.WriteFile(filepath.Join(store.dir, "result-"+wrongID[1:]+".log"), wrongVersion, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load(context.Background(), wrongID); err == nil {
		t.Fatal("accepted unsupported record version with a valid hash")
	}
}

func TestResultScopesResumeRefreshAndCrossSessionIsolation(t *testing.T) {
	base := t.TempDir()
	cwd := t.TempDir()
	newRegistry := func(scope string) *Registry {
		return New(Options{Cwd: cwd, SpillDir: base, ResultScope: scope, ReadOnly: true})
	}
	ownerA := newRegistry("web:owner-a:conversation-1")
	prepared := ownerA.PrepareResult(context.Background(), "example", Result{Text: "retained observation"}, nil)
	if !validResultID(prepared.ResultID) {
		t.Fatalf("missing prepared ID: %q", prepared.Text)
	}
	args, _ := json.Marshal(map[string]any{"result_id": prepared.ResultID, "view": "text"})
	for name, registry := range map[string]*Registry{
		"same session resumed": newRegistry("web:owner-a:conversation-1"),
		"MCP catalog refresh":  ownerA.WithMCP(nil),
	} {
		t.Run(name, func(t *testing.T) {
			tool, ok := registry.Get(ToolResultReadName)
			reader, native := tool.(*ToolResultRead)
			if !ok || !native || !reader.IsReadOnly() {
				t.Fatal("read-only core lost tool_result")
			}
			result, err := tool.Execute(context.Background(), args)
			if err != nil || !strings.Contains(result.Text, "retained observation") {
				t.Fatalf("cannot recover: %s, %v", result.Text, err)
			}
		})
	}
	for _, scope := range []string{"web:owner-b:conversation-1", "web:owner-a:conversation-2", ""} {
		registry := newRegistry(scope)
		tool, _ := registry.Get(ToolResultReadName)
		if _, err := tool.Execute(context.Background(), args); err == nil {
			t.Fatalf("ID crossed scope %q", scope)
		}
	}
	_, ephemeralA := newSessionResultStorage(base, "")
	_, ephemeralB := newSessionResultStorage(base, "")
	if ephemeralA.dir == ephemeralB.dir {
		t.Fatal("ownerless registries share storage")
	}
}

func TestResultStoreConcurrentCommitsHaveNoPartialRecords(t *testing.T) {
	store := newResultStore(filepath.Join(t.TempDir(), "results"))
	record := StoredToolResult{Tool: "action", Text: strings.Repeat("data\n", 4096), TextComplete: true}
	const writers = 12
	refs := make(chan string, writers)
	errors := make(chan error, writers)
	var wg sync.WaitGroup
	for range writers {
		wg.Go(func() {
			ref, err := store.Save(record)
			if err != nil {
				errors <- err
				return
			}
			loaded, err := store.Load(context.Background(), ref.ID)
			if err != nil {
				errors <- err
				return
			}
			if loaded.Text != record.Text {
				errors <- io.ErrUnexpectedEOF
				return
			}
			refs <- ref.ID
		})
	}
	wg.Wait()
	close(errors)
	close(refs)
	for err := range errors {
		t.Error(err)
	}
	first, count := "", 0
	for id := range refs {
		if first == "" {
			first = id
		}
		if id != first {
			t.Error("same observation produced distinct IDs")
		}
		count++
	}
	if count != writers {
		t.Fatalf("only %d of %d commits completed", count, writers)
	}
	entries, err := os.ReadDir(store.dir)
	resultFiles := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "result-") && strings.HasSuffix(entry.Name(), ".log") {
			resultFiles++
		} else if entry.Name() != resultMetadataDirectory && entry.Name() != resultLifecycleLockName {
			t.Fatalf("unexpected partial record: %s", entry.Name())
		}
	}
	if err != nil || resultFiles != 1 {
		t.Fatalf("partial or duplicate records: %v, %v", entries, err)
	}
}

func TestResultSpillCleanupVisitsManagedScopesOnly(t *testing.T) {
	base := t.TempDir()
	_, store := newSessionResultStorage(base, "web:owner:conversation")
	ref, err := store.Save(StoredToolResult{Tool: "action", Text: "old observation", TextComplete: true})
	if err != nil {
		t.Fatal(err)
	}
	// A newly saved record has a bounded activity lease. Advance the sweep's
	// injected clock beyond it while retaining the original mtime-age check.
	now := time.Now().Add(2 * time.Hour)
	old := now.Add(-10 * 24 * time.Hour)
	path := filepath.Join(store.dir, "result-"+ref.ID[1:]+".log")
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	unmanagedDir := filepath.Join(base, "user-files")
	if err := os.Mkdir(unmanagedDir, 0700); err != nil {
		t.Fatal(err)
	}
	unmanaged := writeAged(t, unmanagedDir, "keep.log", 20, old)
	files, bytes, err := SpillDiskUsage(base)
	if err != nil || files != 1 || bytes <= 0 {
		t.Fatalf("usage missed saved record: files=%d bytes=%d err=%v", files, bytes, err)
	}
	pruned, err := PruneSpill(base, 7*24*time.Hour, now)
	if err != nil || pruned.Removed != 1 || pruned.Bytes != bytes {
		t.Fatalf("prune = %+v, %v", pruned, err)
	}
	if _, err := os.Stat(unmanaged); err != nil {
		t.Fatalf("pruned unmanaged user file: %v", err)
	}
	if _, err := store.Load(context.Background(), ref.ID); err == nil {
		t.Fatal("expired ID still loads")
	}
}

type cancelResultReader struct{ cancel context.CancelFunc }

func (r cancelResultReader) Read(dst []byte) (int, error) {
	r.cancel()
	return copy(dst, "last bytes"), io.EOF
}

func TestResultStoreCancellationAndReadLimit(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err := readResultBytes(ctx, cancelResultReader{cancel}, 100); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation during final read returned success: %v", err)
	}
	if _, err := readResultBytes(context.Background(), strings.NewReader("12345"), 4); err == nil {
		t.Fatal("read exceeded byte cap")
	}
	store := newResultStore(t.TempDir())
	if _, err := store.Load(ctx, "R"+strings.Repeat("0", 64)); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled load: %v", err)
	}
}
