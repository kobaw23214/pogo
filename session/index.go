// Package session's derived index: one SQLite file beside the transcripts.
//
// The JSONL files are the source of truth and stay exactly as they are; this
// index exists so a listing does not parse every transcript and a search does
// not grep them. It is a cache in the strict sense — every row is derived, any
// row can be rebuilt from its file, and deleting the database file loses
// nothing but time. Two properties follow, and both are load-bearing:
//
//   - Self-healing. List compares the directory against the index and
//     re-summarises whatever differs, so an index left stale by a crash, a
//     hand-copied file, or a version change converges on the next read. Claude
//     Code's sessions-index.json showed the alternative: a list that trusts a
//     stopped-updating index makes sessions invisible while their files sit
//     intact on disk.
//   - Never in the write path's way. touchIndex runs after the record is
//     safely appended and swallows its own failures; a caching layer that
//     could fail a transcript write is not a cache.
//
// Why SQLite rather than a JSON sidecar: the search needs an inverted index
// (FTS5 with the trigram tokenizer, which is what makes Chinese prose and
// error codes findable as substrings), the listing needs a keyed row per
// session, and both want crash-safe partial updates. A flat file buys none of
// that. Goose and opencode reached the same conclusion and moved their whole
// session stores into SQLite; keeping the transcripts as files takes the
// low-risk half of that migration and none of its rewrite.
package session

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	_ "modernc.org/sqlite"
)

// IndexName is the index file, placed inside the session directory so
// POGO_SESSION_DIR relocates transcripts and index together (the malagent
// deployment mounts that directory as one volume).
const IndexName = "index.db"

// maxIndexTextRunes caps the searchable text kept per session. The first
// quarter-million characters answer "which session was this"; what follows in
// a runaway transcript is mostly tool traffic the text already excludes, and
// an unbounded index would grow past the transcripts it serves.
const maxIndexTextRunes = 256 << 10

// indexEnvPath resolves POGO_SESSION_INDEX: "" means the default location,
// "off" disables the index entirely, anything else is a file path. The escape
// hatch exists because the index is an optimisation — a user for whom it
// misbehaves should be able to turn it off without a rebuild.
func indexEnvPath() (path string, off bool) {
	switch v := os.Getenv("POGO_SESSION_INDEX"); {
	case strings.EqualFold(v, "off"):
		return "", true
	case v == "":
		return "", false
	default:
		return v, false
	}
}

// withIndex runs fn against a handle for dir's index and reports whether it
// ran. Handles are short-lived on purpose: SQLite open is cheap, a handle that
// outlives its operation holds the file open (which on Windows keeps the
// directory undeletable), and a clean close checkpoints the WAL away. Callers
// treat false as "fall back to the scan paths", never as failure.
func withIndex(dir string, fn func(*sql.DB) error) bool {
	path, off := indexEnvPath()
	if off || dir == "" {
		return false
	}
	if path == "" {
		path = filepath.Join(dir, IndexName)
	}
	// ToSlash: a Windows path inside a file: URI must not carry backslashes.
	// The DSN's shorthand params mirror mattn/go-sqlite3's keys; _txlock makes
	// every database/sql transaction take the write lock up front, which is
	// what keeps two coexisting pogo processes (terminal and web share the
	// default directory) from deadlocking on a deferred transaction's lock
	// upgrade.
	dsn := "file:" + filepath.ToSlash(path) +
		"?_journal_mode=WAL&_synchronous=NORMAL&_busy_timeout=5000&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return false
	}
	defer db.Close()
	if err := initSchema(db); err != nil {
		return false
	}
	return fn(db) == nil
}

const indexSchemaVersion = 2

// initSchema brings a fresh or existing index to indexSchemaVersion. Any
// version mismatch — older build or newer one — means this file's rows are
// shaped for a schema that is not the current one; since every row is
// derived, the honest move is to drop and let the next reads repopulate
// rather than guess at migrating a cache.
func initSchema(db *sql.DB) error {
	var version int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if version == indexSchemaVersion {
		return nil
	}
	if version != 0 {
		if _, err := db.Exec(`DROP TABLE IF EXISTS sessions_fts; DROP TABLE IF EXISTS sessions;`); err != nil {
			return err
		}
	}
	_, err := db.Exec(`
CREATE TABLE IF NOT EXISTS sessions (
	id       INTEGER PRIMARY KEY,
	sid      TEXT NOT NULL UNIQUE,
	path     TEXT NOT NULL,
	title    TEXT NOT NULL DEFAULT '',
	pinned   INTEGER NOT NULL DEFAULT 0,
	cwd      TEXT NOT NULL DEFAULT '',
	model    TEXT NOT NULL DEFAULT '',
	skills   TEXT NOT NULL DEFAULT '[]',
	messages INTEGER NOT NULL DEFAULT 0,
	created  INTEGER NOT NULL DEFAULT 0,
	updated  INTEGER NOT NULL DEFAULT 0,
	mtime    INTEGER NOT NULL DEFAULT 0,
	size     INTEGER NOT NULL DEFAULT 0,
	text     TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS sessions_updated ON sessions(updated DESC);
CREATE VIRTUAL TABLE IF NOT EXISTS sessions_fts USING fts5(
	title, text,
	tokenize='trigram',
	content='', contentless_delete=1
);
PRAGMA user_version = ` + fmt.Sprint(indexSchemaVersion) + `;`)
	return err
}

// touchIndex brings one session's row current after an append landed. It
// recomputes the summary from memory rather than tracking deltas: the walk it
// needs is the same one a disk re-summarise would do, the records are already
// in RAM, and correctness that depends on no delta bookkeeping stays correct
// after forks and rewinds change what "current" means.
func (s *Store) touchIndex() {
	st, err := os.Stat(s.path)
	if err != nil {
		return
	}
	sum := summariseRecords(s.path, s.records, s.head, s.parent, st.ModTime().UnixMilli())
	// A failure here leaves a stale row that heals on the next List; the
	// append itself already landed.
	withIndex(filepath.Dir(s.path), func(db *sql.DB) error {
		return writeSession(db, sum, st.Size())
	})
}

// writeSession upserts one summary and swaps its searchable text in a single
// transaction.
func writeSession(db *sql.DB, sum summary, size int64) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := upsertRow(tx, sum, size); err != nil {
		return err
	}
	if err := replaceFTS(tx, sum); err != nil {
		return err
	}
	return tx.Commit()
}

func upsertRow(tx *sql.Tx, sum summary, size int64) error {
	skills, err := json.Marshal(sum.info.Skills)
	if err != nil {
		skills = []byte("[]")
	}
	_, err = tx.Exec(`INSERT INTO sessions
		(sid, path, title, pinned, cwd, model, skills, messages, created, updated, mtime, size, text)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(sid) DO UPDATE SET
		path=excluded.path, title=excluded.title, pinned=excluded.pinned,
		cwd=excluded.cwd, model=excluded.model, skills=excluded.skills,
		messages=excluded.messages, created=excluded.created,
		updated=excluded.updated, mtime=excluded.mtime, size=excluded.size,
		text=excluded.text`,
		sum.info.ID, sum.info.Path, sum.info.Title, b2i(sum.info.Pinned),
		sum.info.Cwd, sum.info.Model, string(skills), sum.info.Messages,
		sum.info.Created, sum.info.Updated, sum.info.Updated, size, sum.text)
	return err
}

// replaceFTS swaps a session's searchable text. The table is contentless, so
// the old row is removed by rowid and a fresh one inserted — no triggers,
// because every writer already runs inside this one transaction.
func replaceFTS(tx *sql.Tx, sum summary) error {
	var id int64
	if err := tx.QueryRow(`SELECT id FROM sessions WHERE sid = ?`, sum.info.ID).Scan(&id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM sessions_fts WHERE rowid = ?`, id); err != nil {
		return err
	}
	_, err := tx.Exec(`INSERT INTO sessions_fts(rowid, title, text) VALUES (?,?,?)`,
		id, sum.info.Title, sum.text)
	return err
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// fileEntry is a transcript as the directory sees it: the two facts whose
// change means the index's row for it is out of date.
type fileEntry struct {
	name  string
	mtime int64 // milliseconds
	size  int64
}

// listDirEntries lists the directory's transcript files with their stats —
// the shared input of both List paths.
func listDirEntries(dir string) ([]fileEntry, error) {
	des, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []fileEntry
	for _, e := range des {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, fileEntry{name: e.Name(), mtime: info.ModTime().UnixMilli(), size: info.Size()})
	}
	return out, nil
}

// loadSummary summarises one transcript from disk, reporting the size the
// index records alongside it.
func loadSummary(path string) (summary, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return summary{}, 0, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return summary{}, 0, err
	}
	sum, err := parseSummary(f, path, st.ModTime().UnixMilli())
	return sum, st.Size(), err
}

// parseSummary streams a transcript file and walks its live branch. Bad lines
// are skipped exactly as Open and the old summarise skipped them; a scanner
// error is returned so a listing can leave a mid-read file out rather than
// show a half-parsed summary for it.
func parseSummary(f *os.File, path string, modTime int64) (summary, error) {
	var records []Record
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var r Record
		if err := json.Unmarshal([]byte(line), &r); err != nil || r.ID == "" {
			continue
		}
		records = append(records, r)
	}
	if err := sc.Err(); err != nil {
		return summary{}, err
	}
	records, _ = committedRecords(records, nil)
	tip := ""
	if n := len(records); n > 0 {
		tip = records[n-1].ID
	}
	return summariseRecords(path, records, tip, nil, modTime), nil
}

// listIndexed serves List from the index, healing it on the way through.
//
// The rule is one comparison per file: a row whose mtime and size match the
// directory's view of the file is trusted, and anything else — new file,
// appended file, file the index never saw, row whose file is gone — is
// re-summarised from disk or dropped. That is the whole staleness story: an
// index that cannot drift undetected cannot pull a Claude Code and hide
// sessions behind a stuck cache.
func listIndexed(db *sql.DB, dir string, entries []fileEntry) ([]Info, error) {
	rows, err := db.Query(`SELECT sid, path, title, pinned, cwd, model, skills, messages, created, updated, mtime, size FROM sessions`)
	if err != nil {
		return nil, err
	}
	type cached struct {
		info  Info
		mtime int64
		size  int64
	}
	have := map[string]cached{}
	for rows.Next() {
		var c cached
		var pinned int
		var skills string
		if err := rows.Scan(&c.info.ID, &c.info.Path, &c.info.Title, &pinned, &c.info.Cwd,
			&c.info.Model, &skills, &c.info.Messages, &c.info.Created, &c.info.Updated,
			&c.mtime, &c.size); err != nil {
			rows.Close()
			return nil, err
		}
		c.info.Pinned = pinned != 0
		_ = json.Unmarshal([]byte(skills), &c.info.Skills)
		have[c.info.ID] = c
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	var out []Info
	var refresh []summary
	var sizes []int64
	var stale []string // sids whose files are gone
	seen := make(map[string]bool, len(entries))
	for _, e := range entries {
		sid := strings.TrimSuffix(e.name, ".jsonl")
		seen[sid] = true
		if c, ok := have[sid]; ok && c.mtime == e.mtime && c.size == e.size {
			out = append(out, c.info)
			continue
		}
		sum, size, err := loadSummary(filepath.Join(dir, e.name))
		if err != nil {
			continue // one unreadable file never hid the others; keep that
		}
		refresh = append(refresh, sum)
		sizes = append(sizes, size)
		out = append(out, sum.info)
	}
	for sid := range have {
		if !seen[sid] {
			stale = append(stale, sid)
		}
	}
	if len(refresh) > 0 || len(stale) > 0 {
		if err := writeHeals(db, refresh, sizes, stale); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func writeHeals(db *sql.DB, refresh []summary, sizes []int64, stale []string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for i, sum := range refresh {
		if err := upsertRow(tx, sum, sizes[i]); err != nil {
			return err
		}
		if err := replaceFTS(tx, sum); err != nil {
			return err
		}
	}
	for _, sid := range stale {
		var id int64
		if err := tx.QueryRow(`SELECT id FROM sessions WHERE sid = ?`, sid).Scan(&id); err != nil {
			if err == sql.ErrNoRows {
				continue
			}
			return err
		}
		if _, err := tx.Exec(`DELETE FROM sessions_fts WHERE rowid = ?`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(`DELETE FROM sessions WHERE sid = ?`, sid); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// Block is one quoted stretch of conversation around a cluster of matches:
// hits close enough to share one display window merge into a single block,
// distant ones quote separately. Text is the display form (whitespace
// collapsed, ellipses marking the cut); Anchor is the same window raw,
// byte-for-byte out of the message, so the web UI can find the message that
// holds it without any id plumbing between transcript and stream.
type Block struct {
	Text   string `json:"text"`
	Anchor string `json:"anchor"`
}

// maxHitBlocks caps how many quoted blocks one hit carries — the sidebar row
// answers "where in this session", not "every occurrence".
const maxHitBlocks = 4

// Hit is a search result: the session row plus how well it matched. bm25
// scores are negative with the best match nearest zero. Blocks quote the
// conversation around each cluster of matches — empty when only the title
// matched — so a result row can show *what* matched, not just *which*
// session did.
type Hit struct {
	Info
	Score  float64 `json:"score"`
	Blocks []Block `json:"blocks,omitempty"`
}

// Search finds sessions whose recorded conversation matches query, best match
// first. Without a usable index it degrades to a slow scan — search is rare
// enough that correctness everywhere beats fast only sometimes.
func Search(dir, query string, limit int) ([]Hit, error) {
	query = strings.TrimSpace(query)
	if limit <= 0 {
		limit = 20
	}
	if dir == "" || query == "" {
		return nil, nil
	}
	var hits []Hit
	if withIndex(dir, func(db *sql.DB) (err error) {
		hits, err = searchIndexed(db, dir, query, limit)
		return err
	}) {
		return hits, nil
	}
	// A failed indexed search is not a failed search; scan instead.
	return searchScan(dir, query, limit)
}

// searchIndexed answers from the index. Queries too short for the trigram
// tokenizer fall back to LIKE over title and text — slower than MATCH, but it
// keeps "样本" (two runes) findable rather than silently empty, in bodies as
// well as titles.
func searchIndexed(db *sql.DB, dir, query string, limit int) ([]Hit, error) {
	terms := strings.Fields(query)
	scan := func(rows *sql.Rows, qerr error) ([]Hit, error) {
		if qerr != nil {
			return nil, qerr
		}
		defer rows.Close()
		var out []Hit
		for rows.Next() {
			var h Hit
			var pinned int
			if err := rows.Scan(&h.ID, &h.Path, &h.Title, &pinned, &h.Cwd, &h.Model,
				&h.Messages, &h.Created, &h.Updated, &h.Score); err != nil {
				return nil, err
			}
			h.Pinned = pinned != 0
			h.Blocks = blocksFor(dir, h.ID, terms, maxHitBlocks)
			out = append(out, h)
		}
		return out, rows.Err()
	}
	if utf8.RuneCountInString(query) < 3 {
		like := "%" + likeEscape(query) + "%"
		return scan(db.Query(`SELECT sid, path, title, pinned, cwd, model, messages, created, updated, 0.0
			FROM sessions WHERE title LIKE ? ESCAPE '\' OR text LIKE ? ESCAPE '\'
			ORDER BY updated DESC LIMIT ?`, like, like, limit))
	}
	// Phrase-quote each whitespace-separated term so user input can never be
	// parsed as FTS5 syntax — a bare NOT is a query error, and a bare *
	// silently matches everything.
	quoted := make([]string, len(terms))
	for i, t := range terms {
		quoted[i] = `"` + strings.ReplaceAll(t, `"`, `""`) + `"`
	}
	return scan(db.Query(`SELECT s.sid, s.path, s.title, s.pinned, s.cwd, s.model, s.messages, s.created, s.updated, rank
		FROM sessions_fts f JOIN sessions s ON s.id = f.rowid
		WHERE sessions_fts MATCH ?
		ORDER BY rank LIMIT ?`, strings.Join(quoted, " "), limit))
}

// matchBlocks quotes the stretches of one message's text where terms hit.
// Occurrences that fit inside a single window merge into one block — that is
// the sidebar's "one place" — while an occurrence past the window's end
// starts the next block. At most max blocks, nil when nothing matches.
func matchBlocks(text string, terms []string, max int) []Block {
	const before, window, tail = 30, 160, 12 // runes of context, block size, match overrun
	if max <= 0 {
		return nil
	}
	orig := []rune(text)
	low := strings.ToLower(text)
	if utf8.RuneCountInString(low) != len(orig) {
		// Case-folding changed the rune count (a handful of codepoints do
		// that): offsets computed on the lowered text would drift, and a
		// drifting anchor locates nothing.
		return nil
	}
	type span struct{ s, e int }
	var spans []span
	for _, term := range terms {
		lt := strings.ToLower(term)
		if lt == "" {
			continue
		}
		tn := utf8.RuneCountInString(lt)
		for off := 0; off < len(low); {
			i := strings.Index(low[off:], lt)
			if i < 0 {
				break
			}
			s := utf8.RuneCountInString(low[:off+i])
			spans = append(spans, span{s, s + tn})
			off += i + len(lt)
		}
	}
	if len(spans) == 0 {
		return nil
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].s < spans[j].s })

	// Group into fixed windows: a match whose start falls inside the open
	// block's window joins it, anything later starts the next block. The
	// window is the membership test; the display end still grows past it to
	// keep the last match whole.
	type block struct{ start, end int }
	var groups []block
	for _, sp := range spans {
		if n := len(groups); n > 0 && sp.s < groups[n-1].start+window {
			if e := sp.e + tail; e > groups[n-1].end {
				groups[n-1].end = e
			}
			continue
		}
		start := sp.s - before
		if start < 0 {
			start = 0
		}
		groups = append(groups, block{start: start, end: start + window})
	}
	var out []Block
	for _, g := range groups {
		if len(out) >= max {
			break
		}
		if g.end > len(orig) {
			g.end = len(orig)
		}
		anchor := string(orig[g.start:g.end])
		disp := strings.Join(strings.Fields(anchor), " ")
		if g.start > 0 {
			disp = "…" + disp
		}
		if g.end < len(orig) {
			disp += "…"
		}
		out = append(out, Block{Text: disp, Anchor: anchor})
	}
	return out
}

// blocksFor quotes a session's matches message by message, so each block can
// lead back to where it was said. The texts come from a fresh read of the
// transcript — the index rows stay small, and the quote can never disagree
// with the file that served the hit.
func blocksFor(dir, sid string, terms []string, max int) []Block {
	sum, _, err := loadSummary(filepath.Join(dir, sid+".jsonl"))
	if err != nil {
		return nil
	}
	var out []Block
	for _, text := range sum.texts {
		out = append(out, matchBlocks(text, terms, max-len(out))...)
		if len(out) >= max {
			break
		}
	}
	return out
}

// likeEscape neutralises LIKE's two metacharacters; % and _ in a query are
// content, not wildcards.
func likeEscape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `%`, `\%`)
	return strings.ReplaceAll(s, `_`, `\_`)
}

// searchScan is the no-index search: parse each transcript and substring-match
// the same texts the index would hold.
func searchScan(dir, query string, limit int) ([]Hit, error) {
	entries, err := listDirEntries(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	lq := strings.ToLower(query)
	terms := strings.Fields(query)
	var out []Hit
	for _, e := range entries {
		if len(out) >= limit {
			break
		}
		sum, _, err := loadSummary(filepath.Join(dir, e.name))
		if err != nil {
			continue
		}
		if strings.Contains(strings.ToLower(sum.info.Title), lq) ||
			strings.Contains(strings.ToLower(sum.text), lq) {
			var blocks []Block
			for _, text := range sum.texts {
				blocks = append(blocks, matchBlocks(text, terms, maxHitBlocks-len(blocks))...)
				if len(blocks) >= maxHitBlocks {
					break
				}
			}
			out = append(out, Hit{Info: sum.info, Blocks: blocks})
		}
	}
	return out, nil
}

// RebuildIndex throws the index away and rebuilds it from the transcripts,
// reporting how many sessions it indexed. The CLI surface for "the cache is
// wrong in a way healing cannot see" — which for a derived store is always
// also "delete it", the recovery every cache owes its users.
func RebuildIndex(dir string) (int, error) {
	path, off := indexEnvPath()
	if off {
		return 0, fmt.Errorf("session index disabled (POGO_SESSION_INDEX=off)")
	}
	if path == "" {
		path = filepath.Join(dir, IndexName)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(path + suffix); err != nil && !os.IsNotExist(err) {
			return 0, err
		}
	}

	entries, err := listDirEntries(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	if !withIndex(dir, func(db *sql.DB) error {
		_, err := listIndexed(db, dir, entries)
		return err
	}) {
		return 0, fmt.Errorf("could not build session index at %s", path)
	}
	return len(entries), nil
}

// sortInfos is List's ordering, shared by the indexed and scanned paths so
// the two cannot disagree about what "newest" means: pinned sessions first,
// then newest path first (filenames begin with a UTC timestamp, so lexical
// order is chronological).
func sortInfos(out []Info) {
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Pinned != out[j].Pinned {
			return out[i].Pinned
		}
		return out[i].Path > out[j].Path
	})
}
