package tools

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"time"
)

const (
	resultCatalogDefaultLimit = 20
	resultCatalogMaxLimit     = 100
	resultCatalogMaxScan      = 4096
	resultCatalogMaxOffset    = 1_000_000
	resultCatalogBackfillMax  = 80 << 20
	resultCatalogPageMaxBytes = 24 << 10
)

type ResultCatalogOptions struct {
	Tool   string
	Filter string
	Offset int
	Limit  int
}

type ResultCatalogEntry struct {
	CatalogOffset   int    `json:"catalog_offset"`
	ResultID        string `json:"result_id"`
	Tool            string `json:"tool"`
	Kind            string `json:"kind"`
	TextBytes       int64  `json:"text_bytes"`
	Complete        bool   `json:"text_complete"`
	IsError         bool   `json:"original_is_error"`
	CreatedAt       string `json:"stored_at"`
	ObservedAt      string `json:"original_observed_at,omitempty"`
	Title           string `json:"title,omitempty"`
	Locator         string `json:"locator,omitempty"`
	OriginAmbiguous bool   `json:"origin_ambiguous,omitempty"`
	Retained        bool   `json:"retained"`
	RetainedUntil   string `json:"retained_until,omitempty"`
	LeaseUntil      string `json:"lease_until,omitempty"`
}

// Offset/NextOffset count directory entries scanned in this host-owned scope,
// not filtered matches. This lets a selective search advance under a scan cap.
// A catalog is a local point-in-time directory, not evidence of a complete
// remote dataset; concurrent insertions/removals can change directory positions.
type ResultCatalogPage struct {
	Items        []ResultCatalogEntry `json:"items"`
	Offset       int                  `json:"offset"`
	NextOffset   *int                 `json:"next_offset,omitempty"`
	Scanned      int                  `json:"scanned"`
	ScanComplete bool                 `json:"scan_complete"`
	Skipped      int                  `json:"skipped_unavailable_metadata"`
	Note         string               `json:"note"`
}

type ResultCatalogDetails = ResultCatalogPage

func catalogEntry(metadata resultLifecycleMetadata, now time.Time) ResultCatalogEntry {
	entry := ResultCatalogEntry{
		ResultID: metadata.ResultID, Tool: metadata.Tool, Kind: metadata.Kind, TextBytes: metadata.TextBytes,
		Complete: metadata.TextComplete, IsError: metadata.IsError, CreatedAt: resultTimeString(metadata.CreatedAt),
		OriginAmbiguous: metadata.OriginAmbiguous, Retained: metadata.RetainedUntil.After(now),
		RetainedUntil: resultTimeString(metadata.RetainedUntil), LeaseUntil: resultTimeString(metadata.LeaseUntil),
	}
	if len(metadata.Sources) == 1 && !metadata.OriginAmbiguous {
		entry.Title, entry.Locator, entry.ObservedAt = metadata.Sources[0].Title, metadata.Sources[0].Locator, metadata.Sources[0].ObservedAt
	} else if len(metadata.Sources) > 0 {
		// A title is only a discovery hint. Never attribute identical content to
		// one arbitrary locator when the host registered multiple origins.
		entry.Title = metadata.Sources[0].Title
	}
	return entry
}

func resultCatalogMatch(metadata resultLifecycleMetadata, options ResultCatalogOptions) bool {
	if options.Tool != "" && options.Tool != metadata.Tool {
		return false
	}
	needle := strings.ToLower(options.Filter)
	if needle == "" || strings.Contains(strings.ToLower(metadata.ResultID+"\n"+metadata.Tool), needle) {
		return true
	}
	for _, source := range metadata.Sources {
		if strings.Contains(strings.ToLower(source.Title+"\n"+source.Locator), needle) {
			return true
		}
	}
	return false
}

func (s *ResultStore) List(ctx context.Context, options ResultCatalogOptions) (ResultCatalogPage, error) {
	page := ResultCatalogPage{Items: []ResultCatalogEntry{}, Offset: options.Offset,
		Note: "Host storage directory metadata only, not webpage evidence. This listing neither reads result bodies into the model nor renews result leases. Offset/next_offset count scanned directory entries, not matching rows. Follow next_offset until scan_complete; skipped metadata is not proof of absence. Concurrent storage changes may alter positions. Stored times do not establish source freshness; actual result reads still verify content integrity."}
	if options.Limit == 0 {
		options.Limit = resultCatalogDefaultLimit
	}
	if options.Offset < 0 || options.Offset > resultCatalogMaxOffset || options.Limit < 1 || options.Limit > resultCatalogMaxLimit || len(options.Tool) > 256 || len(options.Filter) > 512 {
		return page, errors.New("catalog requires offset 0..1000000, limit 1..100, tool at most 256 bytes and filter at most 512 bytes")
	}
	if s == nil || s.initErr != nil || s.dir == "" {
		return page, errors.New("tool result storage is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return page, err
	}
	if _, err := os.Lstat(s.dir); os.IsNotExist(err) {
		page.ScanComplete = true
		return page, nil
	}
	root, err := openResultStoreRoot(s.dir)
	if err != nil {
		return page, err
	}
	defer root.Close()
	unlock, err := lockResultLifecycle(ctx, root)
	if err != nil {
		return page, err
	}
	defer unlock()
	listing, err := root.Open(".")
	if err != nil {
		return page, err
	}
	defer listing.Close()
	position, recoveredBytes, recoveries := 0, int64(0), 0
	now := time.Now()
	for {
		if err := ctx.Err(); err != nil {
			return page, err
		}
		entries, readErr := listing.ReadDir(128)
		for _, entry := range entries {
			position++
			if position <= options.Offset {
				continue
			}
			page.Scanned++
			name := entry.Name()
			id := storedResultIDFromFilename(name)
			if id != "" {
				info, statErr := root.Lstat(name)
				if statErr == nil && info.Mode().IsRegular() && info.Size() <= maxStoredResultBytes {
					metadata, metaErr := readResultLifecycle(ctx, root, id)
					needsRecovery := os.IsNotExist(metaErr) || metaErr == nil && !metadata.CatalogKnown
					if needsRecovery && (recoveries >= 8 || recoveredBytes+info.Size() > resultCatalogBackfillMax) {
						// A temporary recovery budget is not a permanent gap.
						// Leave this candidate unread so the next page can retry
						// it with a fresh per-page metadata recovery allowance.
						next := position - 1
						page.NextOffset = &next
						return page, nil
					}
					if needsRecovery {
						// Compatibility recovery does not grant an activity lease.
						recoveries++
						recoveredBytes += info.Size()
						var record StoredToolResult
						record, metaErr = loadStoredResult(ctx, root, id)
						if metaErr == nil {
							metadata, metaErr = metadataForResult(ctx, root, id, record, now)
							if metaErr == nil {
								metaErr = writeResultLifecycle(ctx, root, metadata)
							}
						}
					}
					if metaErr == nil && !metadata.CatalogKnown {
						metaErr = errors.New("catalog metadata has not been recovered within this listing's budget")
					}
					if metaErr != nil {
						page.Skipped++
					} else if resultCatalogMatch(metadata, options) {
						item := catalogEntry(metadata, now)
						item.CatalogOffset = position - 1
						candidate := page
						candidate.Items = append(append([]ResultCatalogEntry{}, page.Items...), item)
						encoded, encodeErr := json.Marshal(candidate)
						if encodeErr != nil || len(encoded) > resultCatalogPageMaxBytes-256 {
							// This candidate was inspected but not delivered. Resume
							// before it, rather than skipping an unread catalog item.
							next := position - 1
							page.NextOffset = &next
							return page, nil
						}
						page.Items = candidate.Items
					}
				} else {
					page.Skipped++
				}
			}
			if len(page.Items) >= options.Limit || page.Scanned >= resultCatalogMaxScan {
				next := position
				page.NextOffset = &next
				return page, nil
			}
		}
		if readErr == io.EOF {
			page.ScanComplete = page.Skipped == 0
			return page, nil
		}
		if readErr != nil {
			return page, readErr
		}
	}
}
