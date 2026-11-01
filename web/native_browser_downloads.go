package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/yosukeno/pogo/prefs"
)

const nativeDownloadLimit = int64(512 << 20)
const nativeDownloadTotalLimit = int64(2 << 30)
const nativeDownloadHistory = 128
const nativeDownloadImportTimeout = 5 * time.Minute

var nativeDownloadID = regexp.MustCompile(`^[a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}$`)

type nativeBrowserDownload struct {
	ID            string `json:"id"`
	Filename      string `json:"filename"`
	State         string `json:"state"`
	Path          string `json:"path,omitempty"`
	TabID         string `json:"tab_id,omitempty"`
	ContextRef    string `json:"context_ref,omitempty"`
	ReceivedBytes int64  `json:"received_bytes"`
	TotalBytes    int64  `json:"total_bytes"`
	Size          int64  `json:"size,omitempty"`
	ActualBytes   int64  `json:"actual_bytes,omitempty"`
	LimitBytes    int64  `json:"limit_bytes,omitempty"`
	Error         string `json:"error,omitempty"`
	ErrorCode     string `json:"error_code,omitempty"`
	CreatedAt     string `json:"created_at"`
}

type nativeBrowserDownloadList struct {
	Downloads      []nativeBrowserDownload    `json:"downloads"`
	Wait           *nativeBrowserDownloadWait `json:"wait,omitempty"`
	Supported      bool                       `json:"supported"`
	Initialized    bool                       `json:"initialized"`
	RefreshPending bool                       `json:"refresh_pending"`
	ObservedAt     string                     `json:"observed_at,omitempty"`
	MaxFileBytes   int64                      `json:"max_file_bytes"`
	MaxTotalBytes  int64                      `json:"max_total_bytes"`
	RejectedCount  int64                      `json:"rejected_count"`
	LastRejection  string                     `json:"last_rejection,omitempty"`
}

type nativeBrowserDownloadStore struct {
	browser           *nativeBrowser
	session           *Session
	policy            func() (map[string]bool, int64)
	ctx               context.Context
	cancel            context.CancelFunc
	work              sync.Mutex // serialises imports and Close; never the browser action/input lock
	wg                sync.WaitGroup
	wake              chan struct{}
	changed           chan struct{} // broadcast store updates to bounded metadata waits
	mu                sync.Mutex
	started           bool
	stopped           bool
	unsupported       bool
	records           []nativeBrowserDownload
	rejected          int64
	lastRejection     string
	maxBytes          int64
	maxTotalBytes     int64
	initialized       bool
	refreshPending    bool
	refreshGeneration uint64
	observedAt        string
	landed            map[string]AttachmentMeta // import lock only; repair a failed ledger write without replacing bytes
}

func (m *Manager) browserDownloadPolicy() (map[string]bool, int64) {
	m.mu.Lock()
	filename := m.browserDownloadPrefsPath
	m.mu.Unlock()
	if filename == "" {
		filename = prefs.Path()
	}
	p := prefs.Default()
	if loaded, err := prefs.Load(filename); err == nil {
		p = loaded
	}
	// Browser downloads have their own streaming/storage budget. The upload
	// preference controls attachments, never Chromium downloads.
	return effectiveAttachExts(p.AttachmentExts), nativeDownloadLimit
}

func (b *nativeBrowser) bindDownloads(session *Session, policy func() (map[string]bool, int64)) {
	ctx, cancel := context.WithCancel(context.Background())
	b.downloads = &nativeBrowserDownloadStore{browser: b, session: session, policy: policy, ctx: ctx, cancel: cancel, maxBytes: nativeDownloadLimit, maxTotalBytes: nativeDownloadTotalLimit, wake: make(chan struct{}, 1), landed: make(map[string]AttachmentMeta)}
}

func (d *nativeBrowserDownloadStore) start() {
	d.mu.Lock()
	if d.stopped {
		d.mu.Unlock()
		return
	}
	if d.started {
		// ensure calls start again after a confirmed context recreation. A 404
		// from the old context must not permanently disable the new one.
		d.unsupported = false
		d.mu.Unlock()
		return
	}
	d.started = true
	d.wg.Add(1)
	d.mu.Unlock()
	go func() {
		defer d.wg.Done()
		ticker := time.NewTicker(1500 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-d.ctx.Done():
				return
			case <-ticker.C:
			case <-d.wake:
			}
			d.mu.Lock()
			unsupported := d.unsupported
			d.mu.Unlock()
			if !unsupported {
				_, _ = d.sync(d.ctx)
			}
		}
	}()
}

func (d *nativeBrowserDownloadStore) stop() {
	d.mu.Lock()
	d.stopped = true // excludes a late wg.Add before we begin waiting
	d.cancel()
	d.notifyChangedLocked()
	d.mu.Unlock()
	// Any request already waiting for the worker observes cancellation. Wait
	// until its last filesystem/ledger/SSE operation ends before Close returns.
	d.work.Lock()
	d.work.Unlock()
	d.wg.Wait()
}

func (d *nativeBrowserDownloadStore) snapshot() nativeBrowserDownloadList {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.snapshotLocked()
}

// snapshotLocked returns owned metadata only. Callers must hold d.mu.
func (d *nativeBrowserDownloadStore) snapshotLocked() nativeBrowserDownloadList {
	items := append([]nativeBrowserDownload{}, d.records...)
	return nativeBrowserDownloadList{Downloads: items, Supported: !d.unsupported, Initialized: d.initialized, RefreshPending: d.refreshPending, ObservedAt: d.observedAt, MaxFileBytes: d.maxBytes, MaxTotalBytes: d.maxTotalBytes, RejectedCount: d.rejected, LastRejection: d.lastRejection}
}

func (d *nativeBrowserDownloadStore) remember(item nativeBrowserDownload) {
	d.mu.Lock()
	defer d.mu.Unlock()
	defer d.notifyChangedLocked()
	for i := range d.records {
		if d.records[i].ID == item.ID {
			d.records[i] = item
			return
		}
	}
	d.records = append(d.records, item)
	if len(d.records) > nativeDownloadHistory {
		d.records = d.records[len(d.records)-nativeDownloadHistory:]
	}
}

func (b *nativeBrowser) Downloads(ctx context.Context) (nativeBrowserDownloadList, error) {
	b.mu.Lock()
	created, closed, profileErr := b.created, b.closed, b.profileError
	b.mu.Unlock()
	if b.downloads == nil {
		return nativeBrowserDownloadList{Downloads: []nativeBrowserDownload{}, Supported: false, MaxFileBytes: nativeDownloadLimit, MaxTotalBytes: nativeDownloadTotalLimit}, nil
	}
	if profileErr != nil {
		return b.downloads.snapshot(), profileErr
	}
	if closed {
		return b.downloads.snapshot(), errors.New("browser session is closed")
	}
	// Merely listing downloads never creates a browser context or navigates.
	if !created {
		return b.downloads.snapshot(), nil
	}
	// The single background importer owns long content transfers. Status reads
	// stay quick even on a slow disk/network and merely coalesce a fresh poll.
	b.downloads.mu.Lock()
	started := b.downloads.started
	if started {
		b.downloads.refreshGeneration++
		b.downloads.refreshPending = true
	}
	b.downloads.mu.Unlock()
	if started {
		select {
		case b.downloads.wake <- struct{}{}:
		default:
		}
		return b.downloads.snapshot(), nil
	}
	return b.downloads.sync(ctx)
}

func (b *nativeBrowser) addDownloadState(state map[string]json.RawMessage) {
	if d := b.downloads; d != nil && state != nil {
		snapshot := d.snapshot()
		state["downloads"], _ = json.Marshal(snapshot.Downloads)
		state["downloads_supported"], _ = json.Marshal(snapshot.Supported)
		state["downloads_initialized"], _ = json.Marshal(snapshot.Initialized)
		state["downloads_refresh_pending"], _ = json.Marshal(snapshot.RefreshPending)
		state["downloads_observed_at"], _ = json.Marshal(snapshot.ObservedAt)
		state["downloads_max_file_bytes"], _ = json.Marshal(snapshot.MaxFileBytes)
		state["downloads_max_total_bytes"], _ = json.Marshal(snapshot.MaxTotalBytes)
		state["downloads_rejected_count"], _ = json.Marshal(snapshot.RejectedCount)
		state["downloads_last_rejection"], _ = json.Marshal(snapshot.LastRejection)
	}
}

func (s *Server) handleBrowserDownloads(w http.ResponseWriter, r *http.Request) {
	session, ok := s.nativeBrowserSession(w, r)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	result, err := session.nativeBrowser.Downloads(r.Context())
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (d *nativeBrowserDownloadStore) sync(parent context.Context) (nativeBrowserDownloadList, error) {
	ctx, cancel := context.WithTimeout(parent, nativeDownloadImportTimeout)
	stop := context.AfterFunc(d.ctx, cancel)
	defer func() { stop(); cancel() }()
	if !d.work.TryLock() {
		return d.snapshot(), nil
	}
	defer d.work.Unlock()
	lastPublished, _ := json.Marshal(d.snapshot())
	publish := func() {
		result := d.snapshot()
		after, _ := json.Marshal(result)
		if !bytes.Equal(lastPublished, after) {
			d.session.hub.Publish(Event{Type: EvBrowserDownloads, BrowserDownloads: &result})
			lastPublished = after
		}
	}
	defer publish()
	d.mu.Lock()
	generation := d.refreshGeneration
	d.refreshPending = true
	d.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return d.snapshot(), err
	}
	if err := d.ctx.Err(); err != nil {
		return d.snapshot(), err
	}
	pollCtx, pollCancel := context.WithTimeout(ctx, 30*time.Second)
	raw, err := d.browser.request(pollCtx, http.MethodGet, "/downloads", nil)
	pollCancel()
	if err != nil {
		var remote *nativeBrowserHTTPError
		if errors.As(err, &remote) && remote.status == http.StatusNotFound {
			d.mu.Lock()
			d.unsupported = true
			d.refreshPending = false
			d.notifyChangedLocked()
			d.mu.Unlock()
			return d.snapshot(), nil
		}
		return d.snapshot(), err
	}
	var list struct {
		Downloads     []nativeBrowserDownload `json:"downloads"`
		RejectedCount int64                   `json:"rejected_count"`
		LastRejection json.RawMessage         `json:"last_rejection"`
		MaxFileBytes  *int64                  `json:"max_file_bytes"`
		MaxTotalBytes *int64                  `json:"max_total_bytes"`
	}
	if json.Unmarshal(raw, &list) != nil || list.Downloads == nil || len(list.Downloads) > nativeDownloadHistory {
		return d.snapshot(), errors.New("browser download list is invalid")
	}
	allowed, limit := d.policy()
	if limit <= 0 {
		limit = nativeDownloadLimit
	}
	limit = min(limit, nativeDownloadLimit)
	totalLimit := nativeDownloadTotalLimit
	if list.MaxFileBytes != nil {
		if *list.MaxFileBytes <= 0 {
			return d.snapshot(), errors.New("invalid browser download file limit")
		}
		limit = min(limit, *list.MaxFileBytes)
	}
	if list.MaxTotalBytes != nil {
		if *list.MaxTotalBytes <= 0 {
			return d.snapshot(), errors.New("invalid browser download storage limit")
		}
		totalLimit = min(totalLimit, *list.MaxTotalBytes)
	}
	d.mu.Lock()
	d.unsupported = false
	d.maxBytes, d.maxTotalBytes = limit, totalLimit
	if list.RejectedCount > d.rejected {
		d.rejected = list.RejectedCount
		d.lastRejection = "浏览器拒绝了超过限制或无法接收的下载。"
	}
	d.mu.Unlock()
	prior := d.snapshot()
	known := make(map[string]nativeBrowserDownload, len(prior.Downloads))
	for _, item := range prior.Downloads {
		known[item.ID] = item
	}
	// Publish one complete metadata observation before any large content copy.
	// A status query queued during this poll retains its own pending generation.
	for _, remote := range list.Downloads {
		item, ok := cleanBrowserDownload(remote, limit, totalLimit)
		previous := known[item.ID]
		if ok && previous.State != "saved" && !permanentDownloadRejection(previous.ErrorCode) {
			d.remember(item)
		}
	}
	d.mu.Lock()
	d.initialized, d.observedAt = true, time.Now().UTC().Format(time.RFC3339Nano)
	d.refreshPending = d.refreshGeneration != generation
	d.notifyChangedLocked()
	d.mu.Unlock()
	publish()
	for _, remote := range list.Downloads {
		if ctx.Err() != nil {
			return d.snapshot(), ctx.Err()
		}
		item, ok := cleanBrowserDownload(remote, limit, totalLimit)
		if !ok {
			continue
		}
		if previous, exists := known[item.ID]; exists && (previous.State == "saved" || permanentDownloadRejection(previous.ErrorCode)) {
			_ = d.ack(ctx, item.ID)
			continue
		}
		if item.State == "completed" {
			meta, err := d.importFile(ctx, item, allowed, limit)
			if err != nil {
				if ctx.Err() != nil {
					return d.snapshot(), ctx.Err()
				}
				item.State, item.ErrorCode, item.Error = "failed", "IMPORT_FAILED", "文件已下载，但未能导入本会话；请重试。"
				var rejected *browserDownloadRejected
				if errors.As(err, &rejected) {
					item.ErrorCode, item.Error = rejected.code, rejected.Error()
					item.ActualBytes, item.LimitBytes = rejected.actual, rejected.limit
				}
			} else {
				item.State, item.Path, item.Size, item.Filename = "saved", meta.Path, meta.Size, meta.Name
				item.Error, item.ErrorCode = "", ""
				item.ActualBytes, item.LimitBytes = 0, 0
			}
		}
		d.remember(item)
		publish()
		if item.State == "saved" || permanentDownloadRejection(item.ErrorCode) || remote.State == "failed" {
			_ = d.ack(ctx, item.ID)
		}
	}
	return d.snapshot(), nil
}

func cleanBrowserDownload(item nativeBrowserDownload, limit, totalLimit int64) (nativeBrowserDownload, bool) {
	if !nativeDownloadID.MatchString(item.ID) || len(item.Filename) > 512 || (item.State != "in_progress" && item.State != "completed" && item.State != "failed") {
		return nativeBrowserDownload{}, false
	}
	item.Filename = sanitizeName(path.Base(strings.ReplaceAll(item.Filename, "\\", "/")))
	code := item.ErrorCode
	item.Path, item.Error, item.ErrorCode = "", "", ""
	item.Size, item.ActualBytes, item.LimitBytes = max(0, item.Size), max(0, item.ActualBytes), max(0, item.LimitBytes)
	if item.State == "failed" {
		item.ErrorCode = cleanDownloadFailureCode(code)
		if item.ErrorCode == "DOWNLOAD_TOO_LARGE" || item.ErrorCode == "DOWNLOAD_TOTAL_LIMIT" {
			ceiling := limit
			if item.ErrorCode == "DOWNLOAD_TOTAL_LIMIT" {
				ceiling = totalLimit
			}
			if item.LimitBytes == 0 || item.LimitBytes > ceiling {
				item.LimitBytes = ceiling
			}
			item.ActualBytes = max(item.ActualBytes, item.Size, item.ReceivedBytes, item.TotalBytes)
		} else {
			item.ActualBytes, item.LimitBytes = 0, 0
		}
		item.Error = downloadFailureMessage(item.ErrorCode, item.ActualBytes, item.LimitBytes)
	} else {
		item.ActualBytes, item.LimitBytes = 0, 0
	}
	if !nativeTabID.MatchString(item.TabID) {
		item.TabID = ""
	}
	if !validNativeContextRef(item.ContextRef) {
		item.ContextRef = ""
	}
	item.ReceivedBytes = max(0, min(item.ReceivedBytes, nativeDownloadLimit))
	item.TotalBytes = max(0, item.TotalBytes)
	if _, err := time.Parse(time.RFC3339Nano, item.CreatedAt); err != nil {
		item.CreatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	}
	return item, true
}

func (d *nativeBrowserDownloadStore) ack(ctx context.Context, id string) error {
	_, err := d.browser.request(ctx, http.MethodDelete, "/downloads/"+url.PathEscape(id), nil)
	return err
}

type browserDownloadRejected struct {
	code          string
	actual, limit int64
}

func (e *browserDownloadRejected) Error() string {
	return downloadFailureMessage(e.code, e.actual, e.limit)
}

func (d *nativeBrowserDownloadStore) importFile(ctx context.Context, item nativeBrowserDownload, allowed map[string]bool, limit int64) (AttachmentMeta, error) {
	if landed, ok := d.landed[item.ID]; ok {
		root, err := os.OpenRoot(d.session.cwd)
		if err != nil {
			return AttachmentMeta{}, err
		}
		defer root.Close()
		info, err := root.Lstat(landed.Path)
		if err != nil || !info.Mode().IsRegular() {
			return AttachmentMeta{}, errors.New("download destination changed before attachment registration")
		}
		landed.Size = info.Size()
		if err := ctx.Err(); err != nil {
			return AttachmentMeta{}, err
		}
		if err := d.registerFile(root, landed); err != nil {
			return AttachmentMeta{}, err
		}
		delete(d.landed, item.ID)
		return landed, nil
	}
	if len(d.landed) >= nativeDownloadHistory {
		return AttachmentMeta{}, errors.New("too many downloads awaiting attachment registration")
	}
	if max(item.Size, item.TotalBytes) > limit {
		return AttachmentMeta{}, &browserDownloadRejected{code: "DOWNLOAD_TOO_LARGE", actual: max(item.Size, item.TotalBytes), limit: limit}
	}
	// Chromium owns the original request/cookies; import never re-fetches it.
	endpoint := d.browser.config.url + "/sessions/" + url.PathEscape(d.browser.id) + "/downloads/" + url.PathEscape(item.ID) + "/content"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return AttachmentMeta{}, err
	}
	req.Header.Set("Authorization", "Bearer "+d.browser.config.token)
	response, err := d.browser.config.client.Do(req)
	if err != nil {
		return AttachmentMeta{}, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound || response.StatusCode == http.StatusGone {
		return AttachmentMeta{}, &browserDownloadRejected{code: "DOWNLOAD_FILE_UNAVAILABLE"}
	}
	if response.StatusCode != http.StatusOK {
		return AttachmentMeta{}, errors.New("browser download content is unavailable")
	}
	if response.ContentLength > limit {
		return AttachmentMeta{}, &browserDownloadRejected{code: "DOWNLOAD_TOO_LARGE", actual: response.ContentLength, limit: limit}
	}
	if item.Size > 0 && response.ContentLength >= 0 && item.Size != response.ContentLength {
		return AttachmentMeta{}, &browserDownloadRejected{code: "DOWNLOAD_SIZE_MISMATCH"}
	}
	if err := ctx.Err(); err != nil {
		return AttachmentMeta{}, err
	}
	root, err := os.OpenRoot(d.session.cwd)
	if err != nil {
		return AttachmentMeta{}, err
	}
	defer root.Close()
	namespace := sha256.Sum256([]byte("pogo-browser-download-v1:" + d.browser.id))
	dir := path.Join(UploadsDir, "browser", hex.EncodeToString(namespace[:12]), item.ID)
	if err := root.MkdirAll(dir, 0755); err != nil {
		return AttachmentMeta{}, err
	}
	temporary := path.Join(dir, ".download-"+randomID(16))
	file, err := root.OpenFile(temporary, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return AttachmentMeta{}, err
	}
	defer root.Remove(temporary)
	defer file.Close()
	// A fixed copy buffer and bounded file inspection keep memory independent
	// of the response length. One extra byte detects unknown-size overflow.
	n, err := io.CopyBuffer(file, io.LimitReader(response.Body, limit+1), make([]byte, 64<<10))
	if err != nil {
		if ctx.Err() == nil && errors.Is(err, io.ErrUnexpectedEOF) {
			return AttachmentMeta{}, &browserDownloadRejected{code: "DOWNLOAD_SIZE_MISMATCH"}
		}
		return AttachmentMeta{}, err
	}
	if n > limit {
		return AttachmentMeta{}, &browserDownloadRejected{code: "DOWNLOAD_TOO_LARGE", actual: n, limit: limit}
	}
	if n == 0 {
		return AttachmentMeta{}, &browserDownloadRejected{code: "DOWNLOAD_EMPTY"}
	}
	if item.Size > 0 && n != item.Size || response.ContentLength >= 0 && n != response.ContentLength {
		return AttachmentMeta{}, &browserDownloadRejected{code: "DOWNLOAD_SIZE_MISMATCH"}
	}
	if err := ctx.Err(); err != nil {
		return AttachmentMeta{}, err
	}
	meta, base, ext, err := classifyBrowserDownloadFile(item.Filename, file, n, allowed)
	if err != nil {
		return AttachmentMeta{}, err
	}
	meta.Name, meta.Path = base+ext, path.Join(dir, base+ext)
	// Existing imported bytes may have been edited by the user or Office.
	if info, err := root.Lstat(meta.Path); err == nil {
		if !info.Mode().IsRegular() || !browserDownloadInLedger(root, d.session.ID, meta.Path) {
			return AttachmentMeta{}, errors.New("download destination already exists")
		}
		meta.Size = info.Size()
		return meta, nil
	} else if !os.IsNotExist(err) {
		return AttachmentMeta{}, err
	}
	if err := file.Sync(); err != nil {
		return AttachmentMeta{}, err
	}
	if err := file.Close(); err != nil {
		return AttachmentMeta{}, err
	}
	if err := ctx.Err(); err != nil {
		return AttachmentMeta{}, err
	}
	// Atomic same-filesystem publication refuses any existing final filename.
	if err := root.Link(temporary, meta.Path); err != nil {
		return AttachmentMeta{}, err
	}
	d.landed[item.ID] = meta
	if err := d.registerFile(root, meta); err != nil {
		return AttachmentMeta{}, err
	}
	delete(d.landed, item.ID)
	return meta, nil
}

func (d *nativeBrowserDownloadStore) registerFile(root *os.Root, meta AttachmentMeta) error {
	if err := browserDownloadLedger(root, d.session.ID, meta.Path); err != nil {
		return err
	}
	if err := d.session.registerManagedOfficeAttachment(meta.Path); err != nil {
		return err
	}
	d.session.hub.Publish(Event{Type: EvAttachmentUploaded, Attachment: &meta})
	d.session.touch()
	return nil
}

func browserDownloadLedgerName(sid string) string {
	return path.Join(UploadsDir, attachCacheDir, "sessions", sid+".json")
}
func browserDownloadInLedger(root *os.Root, sid, rel string) bool {
	uploadLedgerMu.Lock()
	defer uploadLedgerMu.Unlock()
	raw, err := root.ReadFile(browserDownloadLedgerName(sid))
	if err != nil {
		return false
	}
	var paths []string
	if json.Unmarshal(raw, &paths) != nil {
		return false
	}
	for _, item := range paths {
		if item == rel {
			return true
		}
	}
	return false
}
func browserDownloadLedger(root *os.Root, sid, rel string) error {
	uploadLedgerMu.Lock()
	defer uploadLedgerMu.Unlock()
	name := browserDownloadLedgerName(sid)
	var paths []string
	if raw, err := root.ReadFile(name); err == nil {
		if json.Unmarshal(raw, &paths) != nil {
			return errors.New("attachment ledger is invalid")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	for _, item := range paths {
		if item == rel {
			return nil
		}
	}
	if err := root.MkdirAll(path.Dir(name), 0755); err != nil {
		return err
	}
	raw, _ := json.Marshal(append(paths, rel))
	temporary := name + ".download-" + randomID(16)
	file, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer root.Remove(temporary)
	if _, err = file.Write(raw); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return root.Rename(temporary, name)
}
