// Uploads lifecycle: a session's uploaded files belong to the conversation
// that uploaded them, so deleting the conversation deletes them, subject to
// workspace sharing. Another session rooted at the same cwd
// may reference the same uploads/ path (attachments are sent by path), and
// deleting the file out from under it strands its transcript. Deletion is
// therefore decided per file: it goes only when no surviving transcript in
// the same workspace mentions it.
//
// Information-library materializations are workspace resources and survive
// conversation deletion entirely. An unsent composer or separately executed
// agent can already hold a borrowed path; users remove these copies explicitly
// through the workspace file controls instead of conversation cleanup.
//
// Attribution is two-channel because the transcript alone under-counts. An
// upload the user removed from the composer before sending never became a
// message block, yet its bytes are on disk and the session meant them — so
// the upload endpoint keeps a per-session ledger under
// uploads/.cache/sessions/, and the transcript's image/file blocks union in
// to cover sessions written before the ledger existed (and imports).
package web

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/yosukeno/pogo/information"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/session"
)

// pruneEmptyUploadDirs drops directory levels under uploads/ that a removal
// just emptied — folder uploads leave a tree behind, and the tree should not
// outlive its last file. The upward walk stops at the first level that still
// holds anything (os.Remove on a non-empty dir fails), never removes
// uploads/ itself, and stays inside it: a dir that is not under the root is
// a bug somewhere else, not something to act on.
func pruneEmptyUploadDirs(uploadsRoot, dir string) {
	for dir != uploadsRoot && withinRoot(uploadsRoot, dir) {
		if os.Remove(dir) != nil {
			return
		}
		dir = filepath.Dir(dir)
	}
}

// uploadLedgerMu serialises the ledger's read-modify-write: two uploads into
// one session can land concurrently, and the file has one writer at a time.
var uploadLedgerMu sync.Mutex

// uploadLedgerPath is one small JSON file per session: the deduped list of
// the uploads/ paths it uploaded.
func uploadLedgerPath(cwd, sid string) string {
	return filepath.Join(cwd, UploadsDir, attachCacheDir, "sessions", sid+".json")
}

// noteSessionUpload records an upload in the session's ledger. Best-effort
// like the metadata cache beside it: the transcript still covers every sent
// attachment, so a lost ledger line only ever strands an unsent file.
func noteSessionUpload(cwd, sid, rel string) {
	uploadLedgerMu.Lock()
	defer uploadLedgerMu.Unlock()
	p := uploadLedgerPath(cwd, sid)
	var list []string
	if raw, err := os.ReadFile(p); err == nil {
		_ = json.Unmarshal(raw, &list)
	}
	for _, have := range list {
		if have == rel {
			return
		}
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return
	}
	if raw, err := json.Marshal(append(list, rel)); err == nil {
		_ = os.WriteFile(p, raw, 0o644)
	}
}

// sessionUploadLedger reads the session's ledger; a missing or corrupt one
// just means the transcript is the only witness.
func sessionUploadLedger(cwd, sid string) []string {
	raw, err := os.ReadFile(uploadLedgerPath(cwd, sid))
	if err != nil {
		return nil
	}
	var list []string
	if json.Unmarshal(raw, &list) != nil {
		return nil
	}
	return list
}

// sessionUploadRefs scans one transcript for the two facts Delete needs
// before the file goes away: the workspace the session ran in, and the
// uploads/ paths its message blocks carry.
func sessionUploadRefs(path string) (refs []string, cwd string) {
	f, err := os.Open(path)
	if err != nil {
		return nil, ""
	}
	defer f.Close()
	seen := map[string]bool{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var rec session.Record
		if json.Unmarshal(line, &rec) != nil {
			continue
		}
		if rec.Meta != nil && rec.Meta.Cwd != "" {
			cwd = rec.Meta.Cwd
		}
		if rec.Message == nil {
			continue
		}
		for _, b := range rec.Message.Content {
			for _, im := range b.Images {
				if im.Type == llm.BlockImage && isUploadRef(im.ImageRef) && !seen[im.ImageRef] {
					seen[im.ImageRef] = true
					refs = append(refs, im.ImageRef)
				}
			}
			var rel string
			switch b.Type {
			case llm.BlockImage:
				rel = b.ImageRef
			case llm.BlockFile:
				rel = b.FilePath
			}
			if isUploadRef(rel) && !seen[rel] {
				seen[rel] = true
				refs = append(refs, rel)
			}
		}
	}
	// Pending messages have not reached the transcript yet. They hold the
	// same file references while queued, including after a process restart.
	if data, err := os.ReadFile(inboxPath(path)); err == nil && len(data) <= maxInboxFile {
		var pending inboxFile
		if json.Unmarshal(data, &pending) == nil {
			for _, item := range pending.Items {
				if item.Status != "queued" && item.Status != "steering" {
					continue
				}
				for _, part := range item.Parts {
					if part.Type == "file" && isUploadRef(part.Path) && !seen[part.Path] {
						seen[part.Path] = true
						refs = append(refs, part.Path)
					}
				}
			}
		}
	}
	return refs, cwd
}

// isUploadRef reports a workspace-relative path inside uploads/ proper — the
// cache beside the files is the store's own bookkeeping, never an attachment.
func isUploadRef(rel string) bool {
	rest, ok := strings.CutPrefix(rel, UploadsDir+"/")
	return ok && rest != "" && rest != attachCacheDir && !strings.HasPrefix(rest, attachCacheDir+"/")
}

// uploadRefsElsewhere reports which candidates surviving transcripts in the
// same workspace still mention. The mention test is a raw substring over the
// JSONL: it catches message blocks, prose and tool calls with one check, and
// its false positives all err toward keeping the file — the safe side of a
// delete. The cwd filter is what keeps a same-named path in another
// workspace (a different uploads/ entirely) from shielding the file.
func uploadRefsElsewhere(sessionDir, selfID, cwd string, candidates []string) map[string]bool {
	refs := map[string]bool{}
	if len(candidates) == 0 {
		return refs
	}
	for _, dir := range []string{sessionDir, filepath.Join(sessionDir, session.SubagentDirName)} {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if len(refs) == len(candidates) {
				return refs
			}
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
				continue
			}
			if strings.TrimSuffix(e.Name(), ".jsonl") == selfID {
				continue
			}
			data, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil || transcriptCwd(data) != cwd {
				continue
			}
			// Conservatively include the durable pending receipt body as well;
			// the transcript still establishes workspace identity.
			if pending, err := os.ReadFile(inboxPath(filepath.Join(dir, e.Name()))); err == nil && len(pending) <= maxInboxFile {
				data = append(data, pending...)
			}
			for _, rel := range candidates {
				if !refs[rel] && bytes.Contains(data, []byte(rel)) {
					refs[rel] = true
				}
			}
		}
	}
	return refs
}

// transcriptCwd reads just the workspace a transcript ran in, from its meta
// record — everything uploadRefsElsewhere needs before deciding a file is
// worth a full scan.
func transcriptCwd(data []byte) string {
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var rec session.Record
		if json.Unmarshal(line, &rec) != nil {
			continue
		}
		if rec.Type == "meta" && rec.Meta != nil {
			return rec.Meta.Cwd
		}
	}
	return ""
}

// gcSessionUploads finishes Delete's job in the workspace: the session's
// uploads go with it, except library materializations and the ones a surviving
// session in the same workspace still references; regenerable caches are
// swept, and the session's ledger retires. Everything here is best-effort —
// the conversation is already deleted, and a stubborn file must not bring
// the delete down with it.
func (m *Manager) gcSessionUploads(sid, cwd string, refs []string) {
	if cwd == "" {
		return
	}
	candidates := append([]string(nil), refs...)
	candidates = append(candidates, sessionUploadLedger(cwd, sid)...)
	referenced := uploadRefsElsewhere(m.cfg.SessionDir, sid, cwd, candidates)
	removed := false
	seen := map[string]bool{}
	for _, rel := range candidates {
		// Library materializations are workspace resources, not session-owned
		// uploads. Another composer/subagent can already hold a borrowed path
		// without having sent a message; never implicitly delete that copy.
		if information.IsMaterializedAssetPath(rel) {
			continue
		}
		if seen[rel] || referenced[rel] {
			continue
		}
		seen[rel] = true
		abs, err := attachAbs(cwd, rel)
		if err != nil {
			continue
		}
		// Already-gone counts toward the sweep: the file is missing either
		// way, and its cache entry is an orphan now.
		if err := os.Remove(abs); err == nil || os.IsNotExist(err) {
			removed = true
			pruneEmptyUploadDirs(filepath.Join(cwd, UploadsDir), filepath.Dir(abs))
		}
	}
	if removed {
		sweepUploadCache(cwd)
	}
	_ = os.Remove(uploadLedgerPath(cwd, sid))
}

// A rewind no longer sweeps the uploads the abandoned branch referenced:
// attachments are message-bound, not workspace state, and the withdrawn
// draft rides back to the composer as path chips reusing the same bytes (a
// re-send uploads nothing). Their cleanup is the session's own delete —
// gcSessionUploads, with the same elsewhere test — so nothing leaks past
// the session's lifetime.

// uploadCacheName is the shape of a regenerable cache entry: the content
// hash plus .json (upload metadata) or .txt (vision description). The
// sessions/ ledger directory fails the test, as it must.
var uploadCacheName = regexp.MustCompile(`^[0-9a-f]{64}\.(json|txt)$`)

// sweepUploadCache drops cache entries whose content no longer exists under
// uploads/. Both kinds are fully regenerable — metadata is recomputed at
// upload, descriptions at the next ask — so an orphan entry is the only kind
// this may remove, and removing it can never break anything.
func sweepUploadCache(cwd string) {
	root := filepath.Join(cwd, UploadsDir)
	cacheDir := filepath.Join(root, attachCacheDir)
	live := map[string]bool{}
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p == cacheDir {
				return fs.SkipDir
			}
			return nil
		}
		if sum, err := hashFileContent(p); err == nil {
			live[sum] = true
		}
		return nil
	})
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !uploadCacheName.MatchString(e.Name()) {
			continue
		}
		if !live[e.Name()[:64]] {
			_ = os.Remove(filepath.Join(cacheDir, e.Name()))
		}
	}
}

// hashFileContent is attachmentHash for an absolute path: the content key
// the metadata and description caches share.
func hashFileContent(abs string) (string, error) {
	f, err := os.Open(abs)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
