package tools

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
)

const (
	ResultDefaultRetentionDays = 30
	ResultMaxRetentionDays     = 90
	ResultReadLease            = time.Hour
	resultMetadataDirectory    = ".metadata"
	resultLifecycleLockName    = ".result-lifecycle-lock"
	resultMetadataMaxBytes     = 16 << 10
)

type resultCatalogSource struct {
	Title      string `json:"title,omitempty"`
	Locator    string `json:"locator,omitempty"`
	ObservedAt string `json:"observed_at,omitempty"`
}

// This mutable sidecar is deliberately excluded from the observation's hash.
// Dates concern local storage; they do not assert source freshness or coverage.
type resultLifecycleMetadata struct {
	Version         int                   `json:"version"`
	ResultID        string                `json:"result_id"`
	CatalogKnown    bool                  `json:"catalog_known"`
	Tool            string                `json:"tool"`
	Kind            string                `json:"kind"`
	TextBytes       int64                 `json:"text_bytes"`
	TextComplete    bool                  `json:"text_complete"`
	IsError         bool                  `json:"is_error"`
	CreatedAt       time.Time             `json:"created_at"`
	UpdatedAt       time.Time             `json:"updated_at"`
	LastAccessAt    time.Time             `json:"last_access_at,omitempty"`
	LeaseUntil      time.Time             `json:"lease_until,omitempty"`
	RetainedUntil   time.Time             `json:"retained_until,omitempty"`
	OriginAmbiguous bool                  `json:"origin_ambiguous,omitempty"`
	Sources         []resultCatalogSource `json:"sources,omitempty"`
}

// ResultRetentionDetails is host lifecycle metadata, never webpage evidence.
// Release removes explicit retention; it neither deletes the original nor
// revokes a still-active one-hour read lease.
type ResultRetentionDetails struct {
	ResultID      string `json:"result_id"`
	View          string `json:"view"`
	Retained      bool   `json:"retained"`
	RetainedUntil string `json:"retained_until,omitempty"`
	LeaseUntil    string `json:"lease_until,omitempty"`
	DefaultDays   int    `json:"default_days"`
	MaxDays       int    `json:"max_days"`
	Note          string `json:"note"`
}

func openResultStoreRoot(dir string) (*os.Root, error) {
	before, err := os.Lstat(dir)
	if err != nil || !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("tool result directory is unavailable or is a symlink")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	opened, statErr := root.Stat(".")
	after, afterErr := os.Lstat(dir)
	if statErr != nil || afterErr != nil || after.Mode()&os.ModeSymlink != 0 || !os.SameFile(before, opened) || !os.SameFile(before, after) {
		root.Close()
		return nil, errors.New("tool result directory changed while opening")
	}
	return root, nil
}

func openResultMetadata(root *os.Root, create bool) (*os.Root, error) {
	if create {
		if err := root.Mkdir(resultMetadataDirectory, 0700); err != nil && !os.IsExist(err) {
			return nil, err
		}
	}
	before, err := root.Lstat(resultMetadataDirectory)
	if err != nil {
		return nil, err
	}
	if !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("result metadata directory is not a regular directory")
	}
	metadata, err := root.OpenRoot(resultMetadataDirectory)
	if err != nil {
		return nil, err
	}
	opened, statErr := metadata.Stat(".")
	after, afterErr := root.Lstat(resultMetadataDirectory)
	if statErr != nil || afterErr != nil || after.Mode()&os.ModeSymlink != 0 || !os.SameFile(before, opened) || !os.SameFile(before, after) {
		metadata.Close()
		return nil, errors.New("result metadata directory changed while opening")
	}
	return metadata, nil
}

func readResultLifecycle(ctx context.Context, root *os.Root, id string) (resultLifecycleMetadata, error) {
	var metadata resultLifecycleMetadata
	dir, err := openResultMetadata(root, false)
	if err != nil {
		return metadata, err
	}
	defer dir.Close()
	name := id + ".json"
	before, err := dir.Lstat(name)
	if err != nil {
		return metadata, err
	}
	if !before.Mode().IsRegular() || before.Size() > resultMetadataMaxBytes {
		return metadata, errors.New("result lifecycle metadata is not a bounded regular file")
	}
	f, err := scopedOpen(dir, name, false)
	if err != nil {
		return metadata, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return metadata, errors.New("result lifecycle metadata changed while opening")
	}
	raw, err := readResultBytes(ctx, f, resultMetadataMaxBytes)
	if err != nil {
		return metadata, err
	}
	if json.Unmarshal(raw, &metadata) != nil || metadata.Version != 1 || metadata.ResultID != id || len(metadata.Sources) > 4 {
		return resultLifecycleMetadata{}, errors.New("result lifecycle metadata is invalid")
	}
	return metadata, nil
}

func writeResultLifecycle(ctx context.Context, root *os.Root, metadata resultLifecycleMetadata) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	raw, err := json.Marshal(metadata)
	if err != nil || len(raw) > resultMetadataMaxBytes {
		return errors.New("result lifecycle metadata exceeds its storage limit")
	}
	dir, err := openResultMetadata(root, true)
	if err != nil {
		return err
	}
	defer dir.Close()
	temporary := ".partial-meta-" + rand.Text()
	f, err := dir.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer dir.Remove(temporary)
	n, writeErr := f.Write(raw)
	if writeErr == nil && n != len(raw) {
		writeErr = errors.New("incomplete lifecycle metadata write")
	}
	if writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if err := errors.Join(writeErr, closeErr, ctx.Err()); err != nil {
		return err
	}
	return dir.Rename(temporary, metadata.ResultID+".json")
}

func metadataForResult(ctx context.Context, root *os.Root, id string, record StoredToolResult, now time.Time) (resultLifecycleMetadata, error) {
	metadata, err := readResultLifecycle(ctx, root, id)
	if err != nil && !os.IsNotExist(err) {
		return metadata, err
	}
	if metadata.Version == 0 {
		created := now
		if info, statErr := root.Lstat("result-" + id[1:] + ".log"); statErr == nil {
			created = info.ModTime()
		}
		metadata = resultLifecycleMetadata{Version: 1, ResultID: id, CreatedAt: created.UTC()}
	}
	metadata.Tool = resultUTF8Prefix(record.Tool, 256)
	metadata.CatalogKnown = true
	metadata.Kind = resultTextKind(record.Tool, record.Text)
	if len(record.Structured) > 0 {
		metadata.Kind = "json"
	}
	metadata.TextBytes, metadata.TextComplete, metadata.IsError = int64(len(record.Text)), record.TextComplete, record.IsError
	metadata.UpdatedAt = now.UTC()
	return metadata, nil
}

func noteResultSaved(ctx context.Context, root *os.Root, id string, record StoredToolResult, now time.Time) error {
	metadata, err := metadataForResult(ctx, root, id, record, now)
	if err != nil {
		return err
	}
	metadata.LastAccessAt, metadata.LeaseUntil = now.UTC(), now.Add(ResultReadLease).UTC()
	return writeResultLifecycle(ctx, root, metadata)
}

func resultRetentionDetails(metadata resultLifecycleMetadata, view string, now time.Time) ResultRetentionDetails {
	return ResultRetentionDetails{
		ResultID: metadata.ResultID, View: view, Retained: metadata.RetainedUntil.After(now),
		RetainedUntil: resultTimeString(metadata.RetainedUntil), LeaseUntil: resultTimeString(metadata.LeaseUntil),
		DefaultDays: ResultDefaultRetentionDays, MaxDays: ResultMaxRetentionDays,
		Note: "Local lifecycle metadata only. Explicit retention expires; it does not refresh the original source or change its contents. Release leaves any active one-hour read lease in place. Expired/unavailable results cannot be recovered by retention and must not cause an automatic replay of the original action.",
	}
}

func resultTimeString(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func (s *ResultStore) Retain(ctx context.Context, id string, days int) (ResultRetentionDetails, error) {
	if days == 0 {
		days = ResultDefaultRetentionDays
	}
	if days < 1 || days > ResultMaxRetentionDays {
		return ResultRetentionDetails{}, fmt.Errorf("retention_days must be between 1 and %d (default %d); permanent retention is not supported", ResultMaxRetentionDays, ResultDefaultRetentionDays)
	}
	return s.updateRetention(ctx, id, days, "retain")
}

func (s *ResultStore) Release(ctx context.Context, id string) (ResultRetentionDetails, error) {
	return s.updateRetention(ctx, id, 0, "release")
}

func (s *ResultStore) updateRetention(ctx context.Context, id string, days int, view string) (ResultRetentionDetails, error) {
	if !validResultID(id) {
		return ResultRetentionDetails{}, errors.New("result_id must be an existing host-assigned ID in this session")
	}
	if s == nil || s.initErr != nil || s.dir == "" {
		return ResultRetentionDetails{}, errors.New("tool result storage is unavailable")
	}
	root, err := openResultStoreRoot(s.dir)
	if err != nil {
		return ResultRetentionDetails{}, errors.New("result is unavailable in this session or has expired; retention cannot restore it")
	}
	defer root.Close()
	unlock, err := lockResultLifecycle(ctx, root)
	if err != nil {
		return ResultRetentionDetails{}, err
	}
	defer unlock()
	record, err := loadStoredResult(ctx, root, id)
	if err != nil {
		return ResultRetentionDetails{}, err
	}
	now := time.Now()
	metadata, err := metadataForResult(ctx, root, id, record, now)
	if err != nil {
		return ResultRetentionDetails{}, err
	}
	metadata.RetainedUntil = time.Time{}
	if days > 0 {
		metadata.RetainedUntil = now.Add(time.Duration(days) * 24 * time.Hour).UTC()
	}
	if err := writeResultLifecycle(ctx, root, metadata); err != nil {
		return ResultRetentionDetails{}, err
	}
	return resultRetentionDetails(metadata, view, now), nil
}

// NoteResultSource accepts only a host-selected ID and already trusted source
// registration. It neither parses tool prose nor grants access to another scope.
func (r *Registry) NoteResultSource(id, title, locator, observedAt string) error {
	if r == nil || r.results == nil {
		return errors.New("tool result storage is unavailable")
	}
	return r.results.ObserveSource(context.Background(), id, title, locator, observedAt)
}

// ProtectResultReferences is called only with IDs extracted from trusted host
// messages/checkpoints, not free-form model text. It protects a bounded active
// working set without reading every stored body. The caller should throttle
// refreshes (for example once per ten minutes), never touch an entire history.
// Missing results remain missing; partial success is reported explicitly.
func (r *Registry) ProtectResultReferences(ctx context.Context, ids []string) error {
	if r == nil || r.results == nil {
		return errors.New("tool result storage is unavailable")
	}
	return r.results.protectReferences(ctx, ids)
}

func (s *ResultStore) protectReferences(ctx context.Context, ids []string) error {
	if len(ids) > 1024 {
		return errors.New("active result protection accepts at most 1024 trusted IDs per refresh")
	}
	if len(ids) == 0 {
		return ctx.Err()
	}
	if s == nil || s.initErr != nil || s.dir == "" {
		return errors.New("tool result storage is unavailable")
	}
	root, err := openResultStoreRoot(s.dir)
	if err != nil {
		return errors.New("referenced stored results are unavailable in this session or have expired; protection cannot restore them")
	}
	defer root.Close()
	unlock, err := lockResultLifecycle(ctx, root)
	if err != nil {
		return err
	}
	defer unlock()
	seen := map[string]bool{}
	protected, missing, unavailable := 0, 0, 0
	now := time.Now()
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return err
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		if !validResultID(id) {
			unavailable++
			continue
		}
		name := "result-" + id[1:] + ".log"
		before, statErr := root.Lstat(name)
		if os.IsNotExist(statErr) {
			missing++
			continue
		}
		if statErr != nil || !before.Mode().IsRegular() || before.Size() > maxStoredResultBytes {
			unavailable++
			continue
		}
		file, openErr := scopedOpen(root, name, false)
		if openErr != nil {
			unavailable++
			continue
		}
		opened, openedErr := file.Stat()
		_ = file.Close()
		if openedErr != nil || !os.SameFile(before, opened) || opened.Size() > maxStoredResultBytes {
			unavailable++
			continue
		}
		metadata, metaErr := readResultLifecycle(ctx, root, id)
		if os.IsNotExist(metaErr) {
			metadata = resultLifecycleMetadata{Version: 1, ResultID: id, CreatedAt: opened.ModTime().UTC()}
		} else if metaErr != nil {
			unavailable++
			continue
		}
		metadata.UpdatedAt, metadata.LastAccessAt, metadata.LeaseUntil = now.UTC(), now.UTC(), now.Add(ResultReadLease).UTC()
		if writeResultLifecycle(ctx, root, metadata) != nil {
			unavailable++
			continue
		}
		protected++
	}
	if missing != 0 || unavailable != 0 {
		return fmt.Errorf("active result protection: protected=%d, missing_or_expired=%d, unavailable=%d; missing records were not restored", protected, missing, unavailable)
	}
	return nil
}

func (s *ResultStore) ObserveSource(ctx context.Context, id, title, locator, observedAt string) error {
	if !validResultID(id) || s == nil || s.initErr != nil || s.dir == "" {
		return errors.New("invalid or unavailable stored result")
	}
	if observedAt != "" {
		stamp, err := time.Parse(time.RFC3339Nano, observedAt)
		if err != nil {
			return errors.New("source observation time must be RFC3339")
		}
		observedAt = stamp.UTC().Format(time.RFC3339Nano)
	}
	source := resultCatalogSource{Title: resultUTF8Prefix(strings.TrimSpace(title), 512), Locator: resultUTF8Prefix(strings.TrimSpace(locator), 2048), ObservedAt: observedAt}
	root, err := openResultStoreRoot(s.dir)
	if err != nil {
		return err
	}
	defer root.Close()
	unlock, err := lockResultLifecycle(ctx, root)
	if err != nil {
		return err
	}
	defer unlock()
	record, err := loadStoredResult(ctx, root, id)
	if err != nil {
		return err
	}
	metadata, err := metadataForResult(ctx, root, id, record, time.Now())
	if err != nil {
		return err
	}
	for _, existing := range metadata.Sources {
		if existing == source {
			return nil
		}
		if existing.Locator != "" && source.Locator != "" && existing.Locator != source.Locator {
			metadata.OriginAmbiguous = true
		}
	}
	if len(metadata.Sources) < 4 {
		metadata.Sources = append(metadata.Sources, source)
	} else {
		metadata.OriginAmbiguous = true
	}
	return writeResultLifecycle(ctx, root, metadata)
}

func storedResultIDFromFilename(name string) string {
	if !strings.HasPrefix(name, "result-") || !strings.HasSuffix(name, ".log") {
		return ""
	}
	id := "R" + strings.TrimSuffix(strings.TrimPrefix(name, "result-"), ".log")
	if !validResultID(id) {
		return ""
	}
	return id
}

func protectedResultLifecycle(metadata resultLifecycleMetadata, now time.Time) bool {
	// These bounds also keep malformed/future-dated sidecars from manufacturing
	// an indefinite pin. Failed parsing is handled by the sweeper as a retry.
	if metadata.UpdatedAt.IsZero() || metadata.UpdatedAt.After(now.Add(5*time.Minute)) {
		return false
	}
	retained := metadata.RetainedUntil.After(now) && !metadata.RetainedUntil.After(metadata.UpdatedAt.Add(ResultMaxRetentionDays*24*time.Hour))
	leased := metadata.LeaseUntil.After(now) && !metadata.LeaseUntil.After(metadata.UpdatedAt.Add(ResultReadLease))
	return retained || leased
}
