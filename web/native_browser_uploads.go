package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/yosukeno/pogo/tools"
)

const nativeUploadFileLimit = int64(256 << 20)
const nativeUploadTotalLimit = int64(512 << 20)

type nativeUploadCacheEntry struct {
	context  string
	id       string
	info     os.FileInfo
	stagedAt time.Time
}

// This identity is internal Go-to-worker authority, never a model argument.
// Large file transfer must not silently retarget an upload after a handoff or
// navigation, even when the person subsequently returns the page to the Agent.
type nativeUploadBinding struct {
	Context         string `json:"context_ref"`
	Tab             string `json:"tab_id"`
	Generation      int64  `json:"generation"`
	ControlEpoch    int64  `json:"control_epoch"`
	DocumentVersion int64  `json:"document_version"`
}

func (b *nativeBrowser) uploadBinding(ctx context.Context) (nativeUploadBinding, error) {
	state, err := b.request(ctx, http.MethodGet, "/state", nil)
	if err != nil {
		return nativeUploadBinding{}, err
	}
	var ownership struct {
		Control         string `json:"control"`
		Context         string `json:"context_ref"`
		Tab             string `json:"active_tab"`
		Generation      int64  `json:"generation"`
		ControlEpoch    int64  `json:"control_epoch"`
		DocumentVersion int64  `json:"document_version"`
	}
	if json.Unmarshal(state, &ownership) != nil || ownership.Control != "agent" {
		return nativeUploadBinding{}, errors.New("browser upload: selected page is human-controlled; inspect tabs and explicitly claim the task page first")
	}
	if !validNativeContextRef(ownership.Context) || !nativeTabID.MatchString(ownership.Tab) || ownership.Generation < 1 || ownership.ControlEpoch < 0 || ownership.DocumentVersion < 1 {
		return nativeUploadBinding{}, errors.New("browser upload: current page identity is unavailable; inspect the browser before uploading")
	}
	return nativeUploadBinding{ownership.Context, ownership.Tab, ownership.Generation, ownership.ControlEpoch, ownership.DocumentVersion}, nil
}

// Files are streamed by the trusted host, never embedded in model messages or
// exposed via a second client port. The worker sees only session-scoped IDs.
func (b *nativeBrowser) prepareUpload(ctx context.Context, op map[string]json.RawMessage) (err error) {
	if b.uploadRoot == "" {
		return errors.New("browser upload: conversation workspace is unavailable")
	}
	var paths []string
	if json.Unmarshal(op["files"], &paths) != nil || len(paths) < 1 || len(paths) > 8 {
		return errors.New("browser upload: files must contain 1–8 workspace paths")
	}
	root, err := os.OpenRoot(b.uploadRoot)
	if err != nil {
		return fmt.Errorf("browser upload: open workspace: %w", err)
	}
	defer root.Close()
	type source struct {
		file *os.File
		info os.FileInfo
		name string
		path string
	}
	sources := make([]source, 0, len(paths))
	defer func() {
		for _, item := range sources {
			item.file.Close()
		}
	}()
	var total int64
	// Validate every source before staging any bytes. OpenRoot also confines
	// symlink resolution during open, eliminating a path-check/open escape.
	for _, path := range paths {
		absolute, failure := tools.Resolve(b.uploadRoot, path)
		if failure != nil {
			return fmt.Errorf("browser upload: file is outside this conversation workspace")
		}
		relative, failure := filepath.Rel(b.uploadRoot, absolute)
		if failure != nil {
			return errors.New("browser upload: invalid workspace path")
		}
		file, failure := root.Open(relative)
		if failure != nil {
			return fmt.Errorf("browser upload: cannot read %q: %w", filepath.Base(path), failure)
		}
		info, failure := file.Stat()
		if failure != nil || !info.Mode().IsRegular() {
			file.Close()
			return errors.New("browser upload: source must be a regular file")
		}
		if info.Size() == 0 {
			file.Close()
			return errors.New("browser upload: empty files cannot be uploaded")
		}
		if info.Size() > nativeUploadFileLimit || total+info.Size() > nativeUploadTotalLimit {
			file.Close()
			return errors.New("browser upload: exceeds 256 MiB per file or 512 MiB total")
		}
		total += info.Size()
		sources = append(sources, source{file, info, filepath.Base(absolute), absolute})
	}
	// Avoid transmitting files while the selected page belongs to a person.
	ownership, err := b.uploadBinding(ctx)
	if err != nil {
		return err
	}
	ids := make([]string, 0, len(sources))
	staged := make([]string, 0, len(sources))
	if b.uploadCache == nil {
		b.uploadCache = make(map[string]nativeUploadCacheEntry)
	}
	for path, entry := range b.uploadCache {
		if entry.context != ownership.Context || time.Since(entry.stagedAt) > 25*time.Minute {
			delete(b.uploadCache, path)
		}
	}
	defer func() {
		if err == nil {
			return
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, id := range staged {
			// A failed preparation can delete its just-staged bytes. Never retain
			// a cache ID that now refers to a deleted or uncertain staging file.
			for path, entry := range b.uploadCache {
				if entry.id == id {
					delete(b.uploadCache, path)
				}
			}
			_, _ = b.request(cleanup, http.MethodDelete, "/uploads/"+url.PathEscape(id), nil)
		}
	}()
	for _, item := range sources {
		if cached, okay := b.uploadCache[item.path]; okay && ownership.Context != "" && cached.context == ownership.Context && os.SameFile(cached.info, item.info) && cached.info.Size() == item.info.Size() && cached.info.ModTime().Equal(item.info.ModTime()) {
			ids = append(ids, cached.id)
			continue
		}
		endpoint := b.config.url + "/sessions/" + url.PathEscape(b.id) + "/uploads"
		request, failure := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, io.LimitReader(item.file, item.info.Size()))
		if failure != nil {
			return failure
		}
		request.ContentLength = item.info.Size()
		request.Header.Set("Authorization", "Bearer "+b.config.token)
		request.Header.Set("Content-Type", "application/octet-stream")
		request.Header.Set("X-Upload-Filename", strings.ReplaceAll(url.QueryEscape(item.name), "+", "%20"))
		response, failure := b.config.client.Do(request)
		if failure != nil {
			return fmt.Errorf("browser upload: file transfer did not complete: %w", failure)
		}
		data, readErr := io.ReadAll(io.LimitReader(response.Body, 32<<10))
		response.Body.Close()
		if readErr != nil {
			return readErr
		}
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return fmt.Errorf("browser upload: browser staging returned HTTP %d", response.StatusCode)
		}
		var receipt struct {
			OK   bool `json:"ok"`
			File struct {
				ID   string `json:"id"`
				Size int64  `json:"size"`
			} `json:"file"`
		}
		if json.Unmarshal(data, &receipt) != nil || !receipt.OK || !nativeDownloadID.MatchString(receipt.File.ID) || receipt.File.Size != item.info.Size() {
			return errors.New("browser upload: invalid staging receipt")
		}
		ids = append(ids, receipt.File.ID)
		staged = append(staged, receipt.File.ID)
		after, failure := item.file.Stat()
		if failure != nil || after.Size() != item.info.Size() || !after.ModTime().Equal(item.info.ModTime()) {
			return errors.New("browser upload: source changed during transfer; read the current file before uploading")
		}
		if ownership.Context != "" {
			b.uploadCache[item.path] = nativeUploadCacheEntry{ownership.Context, receipt.File.ID, item.info, time.Now()}
		}
	}
	// Check every opened source, including cache hits. Replacement by rename
	// changes the pathname identity without changing the already-open descriptor.
	for _, item := range sources {
		after, failure := item.file.Stat()
		relative, relativeErr := filepath.Rel(b.uploadRoot, item.path)
		current, currentErr := root.Stat(relative)
		if failure != nil || relativeErr != nil || currentErr != nil || !os.SameFile(item.info, current) || !current.Mode().IsRegular() || after.Size() != item.info.Size() || !after.ModTime().Equal(item.info.ModTime()) || current.Size() != item.info.Size() || !current.ModTime().Equal(item.info.ModTime()) {
			return errors.New("browser upload: source changed during preparation; read the current file before uploading")
		}
	}
	current, failure := b.uploadBinding(ctx)
	if failure != nil {
		return failure
	}
	if current != ownership {
		return errors.New("browser upload: page, document or control changed during file transfer; inspect the current page before uploading")
	}
	op["upload_binding"], err = json.Marshal(ownership)
	if err != nil {
		return err
	}
	op["files"], err = json.Marshal(ids)
	return err
}
