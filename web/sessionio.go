package web

// Session import/export: the sidebar's portable backup format.
//
// An export is the session's JSONL records wrapped in a small envelope; an
// import validates each record and writes a fresh file under the session dir.
// Records are copied verbatim — ids, parent links, stats — because the file
// format already encodes everything, and re-serialising through an
// intermediate type is a second place for fields to be dropped. The wrapper
// exists only to carry the envelope fields and to make a hand-edited file
// self-describing.

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/yosukeno/pogo/session"
)

// sessionExportKind is the envelope marker. An import requires it so a random
// JSON file cannot half-apply as a transcript.
const sessionExportKind = "pogo-session"

// exportedSession is the wire shape of one exported conversation.
type exportedSession struct {
	Kind    string `json:"kind"`
	Version int    `json:"version"`
	Title   string `json:"title,omitempty"`
	Pinned  *bool  `json:"pinned,omitempty"`
	Model   string `json:"model,omitempty"`
	Cwd     string `json:"cwd,omitempty"`
	// Records are the transcript's JSONL lines, unparsed. The import side
	// validates each into a session.Record before writing anything.
	Records []json.RawMessage `json:"records"`
}

// importLimits: a transcript is bounded by what a session can honestly hold;
// a file past these is not a conversation anyone had.
const (
	importMaxBody     = 64 << 20
	importMaxRecords  = 100_000
	importMaxSessions = 200
)

// handleSessionExport streams one session as the portable JSON.
func (s *Server) handleSessionExport(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("sid")
	if !validID(id) || !s.ownsSession(r, id) {
		writeErr(w, http.StatusNotFound, ErrNotFound)
		return
	}
	path := s.mgr.transcriptPath(id)
	f, err := os.Open(path)
	if err != nil {
		writeErr(w, http.StatusNotFound, ErrNotFound)
		return
	}
	defer f.Close()

	out := exportedSession{Kind: sessionExportKind, Version: 1, Records: []json.RawMessage{}}
	// The envelope's title/pinned/cwd/model come from the listing's summary —
	// the same merge rules the sidebar reads, not a second implementation.
	if list, err := s.mgr.List(); err == nil {
		for _, info := range list {
			if info.ID == id {
				out.Title, out.Pinned, out.Model, out.Cwd = info.Title, &info.Pinned, info.Model, info.Cwd
				break
			}
		}
	}

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		out.Records = append(out.Records, json.RawMessage(line))
	}
	if err := sc.Err(); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", exportFilename(id, out.Title, ".json")))
	_ = json.NewEncoder(w).Encode(out)
}

// exportFilename turns a session into a filesystem-safe download name. The id
// alone is safe but unreadable; the title alone can be empty or hostile —
// keep the printable prefix, fall back to the id.
func exportFilename(id, title, ext string) string {
	slug := downloadSlug(title)
	if slug == "" {
		slug = id
	}
	return "session-" + slug + ext
}

var slugKeep = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

func downloadSlug(s string) string {
	s = strings.TrimSpace(s)
	s = slugKeep.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-.")
	if len(s) > 60 {
		s = s[:60]
	}
	return s
}

// handleSessionImport creates sessions from exported JSON. Accepts one
// exported session or {"sessions": [...]} for bulk; returns what landed.
func (s *Server) handleSessionImport(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, importMaxBody))
	if err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("reading body: %w", err))
		return
	}
	// The Markdown branch: the reading copy (export.md) coming back through
	// the same endpoint under a dedicated field — {"markdown": "…", ...
	// "workspace": "…"}. The sidebar's "import into this project" sends this;
	// the envelope probes below then never see it.
	var mdProbe struct {
		Markdown  string  `json:"markdown"`
		Workspace *string `json:"workspace"`
	}
	if err := decodeLenientJSON(body, &mdProbe); err == nil && mdProbe.Markdown != "" {
		s.handleSessionImportMarkdown(w, r, mdProbe.Markdown, mdProbe.Workspace)
		return
	}
	var batch struct {
		Sessions []exportedSession `json:"sessions"`
	}
	if err := decodeLenientJSON(body, &batch); err != nil || len(batch.Sessions) == 0 {
		var one exportedSession
		if err := decodeLenientJSON(body, &one); err != nil || one.Kind != sessionExportKind || len(one.Records) == 0 {
			writeErr(w, http.StatusBadRequest, errors.New(
				"not a pogo session export: expected {kind: \"pogo-session\", records: [...]}, from this server's export or sample"))
			return
		}
		batch.Sessions = []exportedSession{one}
	}
	if len(batch.Sessions) > importMaxSessions {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("more than %d sessions in one import", importMaxSessions))
		return
	}

	type result struct {
		Title string `json:"title"`
		ID    string `json:"id"`
	}
	var imported []result
	var skipped []string
	for i, ex := range batch.Sessions {
		if ex.Kind != sessionExportKind || len(ex.Records) == 0 {
			skipped = append(skipped, fmt.Sprintf("#%d: not a pogo session export", i+1))
			continue
		}
		if len(ex.Records) > importMaxRecords {
			skipped = append(skipped, fmt.Sprintf("#%d: more than %d records", i+1, importMaxRecords))
			continue
		}
		records := make([]session.Record, 0, len(ex.Records))
		bad := 0
		for _, raw := range ex.Records {
			var rec session.Record
			// A record must parse and carry the two fields the replay and the
			// chain walk depend on. Anything else is damage on a file nobody
			// is forced to trust: imports are copies, and the user can open
			// the JSON to see what failed.
			if err := decodeLenientJSON(raw, &rec); err != nil || rec.ID == "" ||
				(rec.Type != "meta" && rec.Type != "message" && rec.Type != "stats" && rec.Type != "fork") {
				bad++
				continue
			}
			stripImportedResultGrants(&rec)
			records = append(records, rec)
		}
		if bad > 0 {
			skipped = append(skipped, fmt.Sprintf("#%d: %d unreadable record(s) dropped", i+1, bad))
		}
		if len(records) == 0 {
			skipped = append(skipped, fmt.Sprintf("#%d: no valid records", i+1))
			continue
		}
		store, err := session.WriteImported(s.mgr.sessionDir(), records)
		if err != nil {
			skipped = append(skipped, fmt.Sprintf("#%d: %v", i+1, err))
			continue
		}
		// An import lands in the importer's session space: the records may
		// have travelled through an export, but the session they become
		// belongs to whoever uploaded it, exactly like one they typed.
		id := sessionIDOf(store)
		if scope, ok := s.ownerScope(r); ok && !s.mgr.claimImported(scope, id, ex.Title) {
			skipped = append(skipped, fmt.Sprintf("#%d: ownership could not be recorded", i+1))
			continue
		}
		imported = append(imported, result{Title: ex.Title, ID: id})
	}
	if len(imported) == 0 {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("nothing imported: %s", strings.Join(skipped, "; ")))
		return
	}
	if s.opts.Logger != nil {
		s.opts.Logger.Printf("imported %d session(s) from upload", len(imported))
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"imported":    len(imported),
		"session_ids": imported,
		"skipped":     skipped,
	})
}

// A portable transcript preserves what was said, not host-issued access grants.
// In particular an uploaded full_output_path must not become an allowlisted
// legacy file the next time the imported session constructs its tools.
func stripImportedResultGrants(rec *session.Record) {
	// Managed background roles and wake/creation receipts are local host
	// provenance. A portable transcript is an ordinary imported conversation,
	// not another managed worker or proof that this import created a watcher.
	if rec.Meta != nil {
		rec.Meta.Sentinel = nil
		rec.Meta.BackgroundTask = nil
		rec.Meta.AgentWorkflow = nil
	}
	if rec.Message == nil {
		return
	}
	rec.Message.WakeID = ""
	// Compaction checkpoints are local host provenance, not portable evidence or
	// storage grants. An uploaded transcript must not restore that host ledger.
	rec.Message.SourceState = nil
	for i := range rec.Message.Content {
		block := &rec.Message.Content[i]
		block.ResultID = ""
		if len(block.Details) > 0 {
			var details map[string]json.RawMessage
			if json.Unmarshal(block.Details, &details) == nil && details != nil {
				var kind string
				_ = json.Unmarshal(details["type"], &kind)
				if kind == "watch" || kind == "background_task" {
					block.Details = nil
				} else {
					for key := range details {
						if strings.EqualFold(key, "full_output_path") || strings.EqualFold(key, "result_id") {
							delete(details, key)
						}
					}
					block.Details, _ = json.Marshal(details)
				}
			}
		}
		if len(block.Source) > 0 {
			var source map[string]json.RawMessage
			if json.Unmarshal(block.Source, &source) == nil && source != nil {
				for key := range source {
					if strings.EqualFold(key, "full_path") || strings.EqualFold(key, "saved_result") {
						delete(source, key)
					}
				}
				block.Source, _ = json.Marshal(source)
			}
		}
	}
}

// handleSessionSample serves a small, importable example of the export
// format: a meta record, one user turn and one assistant turn, so the shape
// is concrete rather than described.
func (s *Server) handleSessionSample(w http.ResponseWriter, r *http.Request) {
	pinned := false
	sample := exportedSession{
		Kind:    sessionExportKind,
		Version: 1,
		Title:   "示例会话",
		Pinned:  &pinned,
		Model:   "",
		Cwd:     "",
		Records: []json.RawMessage{
			json.RawMessage(`{"id":"sample0001","type":"meta","time":0,"meta":{"cwd":"","model":"","skills":[]}}`),
			json.RawMessage(`{"id":"sample0002","parentId":"sample0001","type":"message","time":0,"message":{"role":"user","content":[{"type":"text","text":"用一句话介绍这个会话文件格式"}]}}`),
			json.RawMessage(`{"id":"sample0003","parentId":"sample0002","type":"message","time":0,"message":{"role":"assistant","content":[{"type":"text","text":"这是一个 pogo 会话导出样例：records 数组里的每一条对应会话文件(JSONL)里的一行，type 为 meta/message/stats/fork。导入时会写入为新会话，原会话不受影响。"}]}}`),
		},
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="session.sample.json"`)
	_ = json.NewEncoder(w).Encode(sample)
}

// sessionDir is the manager's transcript directory, for WriteImported.
func (m *Manager) sessionDir() string { return m.cfg.SessionDir }

// sessionIDOf derives the sidebar's session id from the store's file name,
// the same trim newSession applies.
func sessionIDOf(store *session.Store) string {
	return strings.TrimSuffix(filepath.Base(store.Path()), ".jsonl")
}
