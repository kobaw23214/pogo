package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func resultTestMetadata(t *testing.T, store *ResultStore, id string) resultLifecycleMetadata {
	t.Helper()
	root, err := openResultStoreRoot(store.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	metadata, err := readResultLifecycle(context.Background(), root, id)
	if err != nil {
		t.Fatal(err)
	}
	return metadata
}

func TestResultCatalogMetadataFilteringSourcesAndScope(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	_, store := newSessionResultStorage(base, "owner-one:conversation")
	ref, err := store.Save(StoredToolResult{Tool: "mcp_report", Text: "secret body that must not appear in the directory", TextComplete: true})
	if err != nil {
		t.Fatal(err)
	}
	stamp := "2026-10-21T00:00:00Z"
	if err := store.ObserveSource(ctx, ref.ID, "季度报告", "https://example.test/reports/1", stamp); err != nil {
		t.Fatal(err)
	}
	before := resultTestMetadata(t, store, ref.ID)
	page, err := store.List(ctx, ResultCatalogOptions{Tool: "mcp_report", Filter: "reports"})
	if err != nil || len(page.Items) != 1 || page.Items[0].ResultID != ref.ID || page.Items[0].Title != "季度报告" || page.Items[0].ObservedAt != stamp {
		t.Fatalf("source directory: %+v, %v", page, err)
	}
	raw, _ := json.Marshal(page)
	if strings.Contains(string(raw), "secret body") {
		t.Fatal("catalog leaked stored body")
	}
	after := resultTestMetadata(t, store, ref.ID)
	if !before.LeaseUntil.Equal(after.LeaseUntil) || !before.LastAccessAt.Equal(after.LastAccessAt) {
		t.Fatal("listing renewed a result lease")
	}
	if err := store.ObserveSource(ctx, ref.ID, "相同内容的另一来源", "https://other.test/report", stamp); err != nil {
		t.Fatal(err)
	}
	page, err = store.List(ctx, ResultCatalogOptions{})
	if err != nil || len(page.Items) != 1 || !page.Items[0].OriginAmbiguous || page.Items[0].Locator != "" {
		t.Fatalf("ambiguous content falsely assigned one origin: %+v, %v", page, err)
	}
	_, other := newSessionResultStorage(base, "owner-two:conversation")
	otherPage, err := other.List(ctx, ResultCatalogOptions{})
	if err != nil || len(otherPage.Items) != 0 {
		t.Fatalf("catalog crossed owner/session scope: %+v, %v", otherPage, err)
	}
	if _, err := other.Retain(ctx, ref.ID, 30); err == nil {
		t.Fatal("retention granted access to another scope")
	}
}

func TestResultCatalogByteBoundContinuationDoesNotSkip(t *testing.T) {
	ctx := context.Background()
	store := newResultStore(filepath.Join(t.TempDir(), "results"))
	want := map[string]bool{}
	for i := range 18 {
		ref, err := store.Save(StoredToolResult{Tool: "action", Text: fmt.Sprintf("body-%d", i), TextComplete: true})
		if err != nil {
			t.Fatal(err)
		}
		want[ref.ID] = true
		if err := store.ObserveSource(ctx, ref.ID, strings.Repeat("题", 170), "https://example.test/"+strings.Repeat("p", 2000), ""); err != nil {
			t.Fatal(err)
		}
	}
	seen, offset, pages := map[string]bool{}, 0, 0
	for {
		page, err := store.List(ctx, ResultCatalogOptions{Offset: offset, Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(page)
		if len(raw) > resultCatalogPageMaxBytes {
			t.Fatalf("oversized catalog response: %d", len(raw))
		}
		for _, item := range page.Items {
			if seen[item.ResultID] || !want[item.ResultID] || item.CatalogOffset < offset {
				t.Fatalf("invalid/repeated directory entry: %+v", item)
			}
			seen[item.ResultID] = true
		}
		pages++
		if page.NextOffset == nil {
			if !page.ScanComplete {
				t.Fatalf("unexpected directory gap: %+v", page)
			}
			break
		}
		if *page.NextOffset <= offset || pages > 20 {
			t.Fatalf("directory continuation did not progress: %+v", page)
		}
		offset = *page.NextOffset
	}
	if len(seen) != len(want) || pages < 2 {
		t.Fatalf("byte-capped pagination skipped entries: got %d/%d in %d pages", len(seen), len(want), pages)
	}
}

func TestResultCatalogBackfillsLegacyMetadataWithoutLease(t *testing.T) {
	store := newResultStore(filepath.Join(t.TempDir(), "results"))
	ref, err := store.Save(StoredToolResult{Tool: "action", Text: "old-format record", TextComplete: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(store.dir, resultMetadataDirectory, ref.ID+".json")); err != nil {
		t.Fatal(err)
	}
	page, err := store.List(context.Background(), ResultCatalogOptions{})
	if err != nil || len(page.Items) != 1 || page.Items[0].ResultID != ref.ID {
		t.Fatalf("legacy directory recovery: %+v, %v", page, err)
	}
	metadata := resultTestMetadata(t, store, ref.ID)
	if !metadata.LeaseUntil.IsZero() || !metadata.LastAccessAt.IsZero() {
		t.Fatal("directory recovery automatically activated an old result")
	}
}

func TestResultCatalogLegacyBackfillBudgetContinuesWithoutGaps(t *testing.T) {
	ctx := context.Background()
	store := newResultStore(filepath.Join(t.TempDir(), "results"))
	want := map[string]bool{}
	for i := range 19 {
		ref, err := store.Save(StoredToolResult{Tool: "old_action", Text: fmt.Sprintf("legacy-observation-%d", i), TextComplete: true})
		if err != nil {
			t.Fatal(err)
		}
		want[ref.ID] = true
		if err := os.Remove(filepath.Join(store.dir, resultMetadataDirectory, ref.ID+".json")); err != nil {
			t.Fatal(err)
		}
	}
	seen, offset, pages := map[string]bool{}, 0, 0
	for {
		// Use the default page size: more old records remain than the
		// eight-record per-page metadata recovery allowance.
		page, err := store.List(ctx, ResultCatalogOptions{Offset: offset})
		if err != nil {
			t.Fatal(err)
		}
		if page.Skipped != 0 {
			t.Fatalf("temporary recovery budget was presented as a gap: %+v", page)
		}
		for _, item := range page.Items {
			if !want[item.ResultID] || seen[item.ResultID] {
				t.Fatalf("unknown or repeated legacy result: %+v", item)
			}
			seen[item.ResultID] = true
			if item.LeaseUntil != "" {
				t.Fatal("legacy catalog backfill granted an activity lease")
			}
		}
		pages++
		if page.NextOffset == nil {
			if !page.ScanComplete {
				t.Fatalf("legacy backfill stopped with no usable continuation: %+v", page)
			}
			break
		}
		if *page.NextOffset <= offset || pages > 5 {
			t.Fatalf("legacy backfill made no forward progress: %+v", page)
		}
		offset = *page.NextOffset
	}
	if len(seen) != len(want) || pages < 3 {
		t.Fatalf("legacy backfill lost results: got %d/%d in %d pages", len(seen), len(want), pages)
	}
}

func TestResultRetentionLeaseExpiryReleaseAndIntegrity(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	_, store := newSessionResultStorage(base, "owner:conversation")
	ref, err := store.Save(StoredToolResult{Tool: "action", Text: "retain me", TextComplete: true})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	path := filepath.Join(store.dir, "result-"+ref.ID[1:]+".log")
	old := now.Add(-10 * 24 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	pruned, err := PruneSpill(base, 7*24*time.Hour, now)
	if err != nil || pruned.Removed != 0 {
		t.Fatalf("active result lease was ignored: %+v, %v", pruned, err)
	}
	retained, err := store.Retain(ctx, ref.ID, 0)
	if err != nil || !retained.Retained || retained.DefaultDays != 30 || retained.MaxDays != 90 || retained.RetainedUntil == "" {
		t.Fatalf("default explicit retention: %+v, %v", retained, err)
	}
	if _, err := store.Retain(ctx, ref.ID, 91); err == nil {
		t.Fatal("unbounded retention accepted")
	}
	pruned, err = PruneSpill(base, 7*24*time.Hour, now.Add(2*time.Hour))
	if err != nil || pruned.Removed != 0 {
		t.Fatalf("explicit retention ignored after short lease: %+v, %v", pruned, err)
	}
	released, err := store.Release(ctx, ref.ID)
	if err != nil || released.Retained || released.RetainedUntil != "" || released.LeaseUntil == "" {
		t.Fatalf("release changed active lease or retained original: %+v, %v", released, err)
	}
	pruned, err = PruneSpill(base, 7*24*time.Hour, now.Add(2*time.Hour))
	if err != nil || pruned.Removed != 1 {
		t.Fatalf("released and expired observation survived: %+v, %v", pruned, err)
	}
	if _, err := store.Load(ctx, ref.ID); err == nil {
		t.Fatal("expiry silently reconstructed an original action")
	}
	if _, err := store.Retain(ctx, ref.ID, 1); err == nil {
		t.Fatal("retention revived a deleted record")
	}
	if _, err := os.Stat(filepath.Join(store.dir, resultMetadataDirectory, ref.ID+".json")); !os.IsNotExist(err) {
		t.Fatal("deleted observation kept its lifecycle sidecar")
	}
}

func TestResultLoadRenewsOnlyRequestedResult(t *testing.T) {
	ctx := context.Background()
	store := newResultStore(filepath.Join(t.TempDir(), "results"))
	refs := make([]*ResultReference, 2)
	for i := range refs {
		var err error
		refs[i], err = store.Save(StoredToolResult{Tool: "action", Text: fmt.Sprintf("body-%d", i), TextComplete: true})
		if err != nil {
			t.Fatal(err)
		}
	}
	root, err := openResultStoreRoot(store.dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, ref := range refs {
		metadata := resultTestMetadata(t, store, ref.ID)
		metadata.LeaseUntil = time.Now().Add(-time.Hour)
		if err := writeResultLifecycle(ctx, root, metadata); err != nil {
			t.Fatal(err)
		}
	}
	root.Close()
	if _, err := store.Load(ctx, refs[0].ID); err != nil {
		t.Fatal(err)
	}
	if !resultTestMetadata(t, store, refs[0].ID).LeaseUntil.After(time.Now()) || resultTestMetadata(t, store, refs[1].ID).LeaseUntil.After(time.Now()) {
		t.Fatal("read lease renewed the wrong set of historical results")
	}
}

func TestResultCatalogAndRetentionRejectSymlinkMetadata(t *testing.T) {
	ctx := context.Background()
	store := newResultStore(filepath.Join(t.TempDir(), "results"))
	ref, err := store.Save(StoredToolResult{Tool: "action", Text: "original", TextComplete: true})
	if err != nil {
		t.Fatal(err)
	}
	metadataPath := filepath.Join(store.dir, resultMetadataDirectory, ref.ID+".json")
	outside := filepath.Join(t.TempDir(), "outside.json")
	if err := os.Rename(metadataPath, outside); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, metadataPath); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Retain(ctx, ref.ID, 1); err == nil {
		t.Fatal("retention followed symlink metadata")
	}
	page, err := store.List(ctx, ResultCatalogOptions{})
	if err != nil || len(page.Items) != 0 || page.Skipped != 1 || page.ScanComplete {
		t.Fatalf("symlink metadata reported as a valid complete catalog: %+v, %v", page, err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatal("external metadata was changed/deleted")
	}
}

func TestResultTrustedReferencesProtectOnlyExistingRequestedIDs(t *testing.T) {
	ctx := context.Background()
	base := t.TempDir()
	_, store := newSessionResultStorage(base, "owner:conversation")
	ref, err := store.Save(StoredToolResult{Tool: "action", Text: "checkpoint reference", TextComplete: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(store.dir, resultMetadataDirectory, ref.ID+".json")); err != nil {
		t.Fatal(err)
	}
	missing := "R" + strings.Repeat("0", 64)
	if err := store.protectReferences(ctx, []string{ref.ID, missing, "../untrusted"}); err == nil || !strings.Contains(err.Error(), "protected=1, missing_or_expired=1, unavailable=1") {
		t.Fatalf("reference protection hid a recovery gap: %v", err)
	}
	metadata := resultTestMetadata(t, store, ref.ID)
	if metadata.CatalogKnown || !metadata.LeaseUntil.After(time.Now()) {
		t.Fatal("minimal checkpoint lease invented catalog evidence or failed to protect")
	}
	if _, err := os.Stat(filepath.Join(store.dir, "result-"+missing[1:]+".log")); !os.IsNotExist(err) {
		t.Fatal("checkpoint protection revived a missing observation")
	}
	page, err := store.List(ctx, ResultCatalogOptions{})
	if err != nil || len(page.Items) != 1 || page.Items[0].Tool != "action" {
		t.Fatalf("minimal lease prevented catalog recovery: %+v, %v", page, err)
	}
	if err := store.protectReferences(ctx, make([]string, 1025)); err == nil {
		t.Fatal("unbounded checkpoint reference set accepted")
	}
}

func TestResultLifecycleLockBlocksSweepAndHonorsCancellation(t *testing.T) {
	base := t.TempDir()
	_, store := newSessionResultStorage(base, "owner:conversation")
	ref, err := store.Save(StoredToolResult{Tool: "action", Text: "locked", TextComplete: true})
	if err != nil {
		t.Fatal(err)
	}
	root, err := openResultStoreRoot(store.dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	unlock, err := lockResultLifecycle(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Load(ctx, ref.ID); err == nil {
		t.Fatal("canceled read waited on lifecycle lock")
	}
	now := time.Now().Add(2 * time.Hour)
	old := now.Add(-10 * 24 * time.Hour)
	path := filepath.Join(store.dir, "result-"+ref.ID[1:]+".log")
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	pruned, err := PruneSpill(base, 7*24*time.Hour, now)
	if err != nil || pruned.Removed != 0 {
		t.Fatalf("sweeper removed a process-locked observation: %+v, %v", pruned, err)
	}
}
