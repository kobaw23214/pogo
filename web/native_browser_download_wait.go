package web

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/yosukeno/pogo/tools"
)

// Wait receipts describe only the selected conversation download. Expiring a
// metadata wait never cancels the browser, download or background importer.
type nativeBrowserDownloadWait struct {
	DownloadID string `json:"download_id"`
	Outcome    string `json:"outcome"`
	ElapsedMS  int64  `json:"elapsed_ms"`
}

// notifyChangedLocked broadcasts without blocking an importer or holding an
// extra lock. A nil channel is valid before the first waiter subscribes.
func (d *nativeBrowserDownloadStore) notifyChangedLocked() {
	if d.changed != nil {
		close(d.changed)
		d.changed = make(chan struct{})
	}
}

// downloadWaitSnapshot atomically captures the selected metadata and its next
// update notification, so a terminal update cannot be lost before select.
func (d *nativeBrowserDownloadStore) downloadWaitSnapshot(id string) (nativeBrowserDownloadList, <-chan struct{}, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.changed == nil {
		d.changed = make(chan struct{})
	}
	result := d.snapshotLocked()
	result.Downloads = []nativeBrowserDownload{}
	for _, item := range d.records {
		if item.ID == id {
			result.Downloads = append(result.Downloads, item)
			break
		}
	}
	return result, d.changed, d.stopped
}

func downloadWaitOutcome(snapshot nativeBrowserDownloadList, stopped bool) string {
	if len(snapshot.Downloads) != 0 {
		item := snapshot.Downloads[0]
		switch item.State {
		case "saved":
			if strings.TrimSpace(item.Path) != "" {
				return "saved"
			}
		case "failed", "rejected", "canceled":
			return item.State
		}
	}
	if stopped {
		return "closed"
	}
	if !snapshot.Supported {
		return "unsupported"
	}
	if snapshot.Initialized && len(snapshot.Downloads) == 0 {
		// The bounded history can evict old IDs. Absence never means success,
		// nor does it establish that the website did not start a download.
		return "not_found"
	}
	return ""
}

func (b *nativeBrowser) waitDownload(ctx context.Context, query tools.BrowserDownloadQuery) (nativeBrowserDownloadList, error) {
	if err := ctx.Err(); err != nil {
		return nativeBrowserDownloadList{}, err
	}
	if !nativeDownloadID.MatchString(query.DownloadID) || query.Seconds <= 0 || query.Seconds > tools.MaxBrowserWaitSeconds {
		return nativeBrowserDownloadList{}, errors.New("invalid conversation download wait")
	}
	startedAt := time.Now()
	finish := func(snapshot nativeBrowserDownloadList, outcome string) (nativeBrowserDownloadList, error) {
		snapshot.Wait = &nativeBrowserDownloadWait{DownloadID: query.DownloadID, Outcome: outcome, ElapsedMS: time.Since(startedAt).Milliseconds()}
		return snapshot, nil
	}
	b.mu.Lock()
	created, closed, profileErr, store := b.created, b.closed, b.profileError, b.downloads
	b.mu.Unlock()
	if store == nil {
		return finish(nativeBrowserDownloadList{Downloads: []nativeBrowserDownload{}, Supported: false}, "unsupported")
	}
	if profileErr != nil {
		return nativeBrowserDownloadList{}, profileErr
	}
	snapshot, _, stopped := store.downloadWaitSnapshot(query.DownloadID)
	if closed || stopped {
		return finish(snapshot, "closed")
	}
	if outcome := downloadWaitOutcome(snapshot, false); outcome != "" {
		return finish(snapshot, outcome)
	}
	if !created {
		// Metadata lookup must never launch, recreate or navigate a browser.
		return finish(snapshot, "not_found")
	}

	// Start only the existing context's metadata observer if it was not started
	// yet. Request one coalesced refresh; later notifications are store-local.
	store.mu.Lock()
	started := store.started
	store.mu.Unlock()
	if !started {
		store.start()
	}
	store.mu.Lock()
	if !store.stopped {
		store.refreshGeneration++
		store.refreshPending = true
	}
	store.mu.Unlock()
	select {
	case store.wake <- struct{}{}:
	default:
	}

	deadline := time.NewTimer(time.Duration(query.Seconds * float64(time.Second)))
	defer deadline.Stop()
	for {
		if err := ctx.Err(); err != nil {
			return nativeBrowserDownloadList{}, err
		}
		snapshot, changed, stopped := store.downloadWaitSnapshot(query.DownloadID)
		if outcome := downloadWaitOutcome(snapshot, stopped); outcome != "" {
			return finish(snapshot, outcome)
		}
		select {
		case <-ctx.Done():
			return nativeBrowserDownloadList{}, ctx.Err()
		case <-store.ctx.Done():
			snapshot, _, _ := store.downloadWaitSnapshot(query.DownloadID)
			return finish(snapshot, "closed")
		case <-changed:
		case <-deadline.C:
			if err := ctx.Err(); err != nil {
				return nativeBrowserDownloadList{}, err
			}
			snapshot, _, stopped := store.downloadWaitSnapshot(query.DownloadID)
			if outcome := downloadWaitOutcome(snapshot, stopped); outcome != "" {
				return finish(snapshot, outcome)
			}
			return finish(snapshot, "timeout")
		}
	}
}
