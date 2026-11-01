package session

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yosukeno/pogo/llm"
)

// findInfo locates one session's Info in a listing, by id or by path.
func findInfo(t *testing.T, list []Info, idOrPath string) Info {
	t.Helper()
	for _, info := range list {
		if info.ID == idOrPath || info.Path == idOrPath {
			return info
		}
	}
	t.Fatalf("session %s missing from listing", idOrPath)
	return Info{}
}

// newStoreIn creates a session with the given user messages in an explicit
// directory — the index tests need List/Search to see the same directory the
// store writes to, which newStore's own TempDir cannot offer.
func newStoreIn(t *testing.T, dir string, texts ...string) *Store {
	t.Helper()
	s, err := Create(dir, "/work", "k3", "triage")
	if err != nil {
		t.Fatal(err)
	}
	for _, txt := range texts {
		if err := s.Append(llm.UserText(txt)); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

// The index is on by default, so every test that touches a Store or List in a
// temp directory is already exercising it. The explicit tests below pin the
// properties that exist for the index's sake: parity with the scan, healing,
// search (including the Chinese-substring case the trigram tokenizer exists
// for), and the off switch.

func TestIndexedListMatchesScan(t *testing.T) {
	dir := t.TempDir()
	s := newStoreIn(t, dir, "分析 emotet 样本", "第二个问题")
	if err := s.Append(llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockText, Text: "这个家族主要通过垃圾邮件传播"}}}); err != nil {
		t.Fatal(err)
	}

	got, err := List(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("listed %d sessions, want 1", len(got))
	}
	info := got[0]
	if info.Messages != 2 {
		t.Errorf("Messages = %d, want 2", info.Messages)
	}
	if !strings.Contains(info.Title, "emotet") {
		t.Errorf("Title = %q, want the first user message", info.Title)
	}

	// The same listing must come out of a forced scan, byte for byte: two
	// paths that can disagree about a session silently pick favourites in
	// every UI built on them.
	scan, err := scanList(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(scan) != 1 || !reflect.DeepEqual(scan[0], info) {
		t.Errorf("scan and indexed listings disagree: %+v vs %+v", scan, info)
	}
}

// scanList is List with the index benched, the control group.
func scanList(dir string) ([]Info, error) {
	t, off := indexEnvPath()
	if t != "" || off {
		panic("scanList requires the default index path")
	}
	entries, err := listDirEntries(dir)
	if err != nil {
		return nil, err
	}
	var out []Info
	for _, e := range entries {
		sum, _, err := loadSummary(filepath.Join(dir, e.name))
		if err != nil {
			continue
		}
		out = append(out, sum.info)
	}
	sortInfos(out)
	return out, nil
}

func TestListHealsIndexDrift(t *testing.T) {
	dir := t.TempDir()
	s := newStoreIn(t, dir, "first question")
	first := findInfo(t, mustList(t, dir), s.Path())

	// Simulate every way the index can end up behind the directory: a file it
	// never saw, and a file that changed under it.
	other, err := Create(dir, "/work", "k3")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.Path(), append(mustRead(t, s.Path()), []byte("\n")...), 0o600); err != nil {
		t.Fatal(err)
	}

	healed := mustList(t, dir)
	if len(healed) != 2 {
		t.Fatalf("listed %d sessions after drift, want 2", len(healed))
	}
	again := findInfo(t, healed, first.ID)
	if again.Updated == first.Updated {
		t.Error("re-summarised session kept the stale row's timestamp")
	}

	// And a file that disappears leaves no row behind.
	if err := os.Remove(other.Path()); err != nil {
		t.Fatal(err)
	}
	final := mustList(t, dir)
	if len(final) != 1 || final[0].ID != first.ID {
		t.Errorf("deleted session still listed: %+v", final)
	}
}

func TestSearchFindsChineseAndLatinSubstrings(t *testing.T) {
	dir := t.TempDir()
	a := newStoreIn(t, dir, "分析 emotet 样本的持久化机制")
	if err := a.Append(llm.Message{Role: llm.RoleAssistant, Content: []llm.Block{{Type: llm.BlockText, Text: "这个家族主要通过垃圾邮件传播"}}}); err != nil {
		t.Fatal(err)
	}
	b := newStoreIn(t, dir, "nothing about malware here")
	_ = b

	// Chinese substring spanning the trigram boundary: it appears only inside
	// a's assistant message, not in any title.
	hits, err := Search(dir, "垃圾邮件", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Path != a.Path() {
		t.Fatalf("Chinese body search: got %d hits, want exactly session a", len(hits))
	}
	if len(hits[0].Blocks) == 0 || !strings.Contains(hits[0].Blocks[0].Text, "垃圾邮件") {
		t.Fatalf("blocks should quote the match, got %+v", hits[0].Blocks)
	}

	hits, err = Search(dir, "emotet", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || !strings.Contains(hits[0].Title, "emotet") {
		t.Fatalf("Latin search: got %+v", hits)
	}

	// Two runes are under the trigram floor; LIKE over title and text keeps
	// them findable rather than silently empty.
	hits, err = Search(dir, "样本", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("short-query search: got %d hits, want 1", len(hits))
	}

	// A short query that appears only in a body — never in a title — must
	// still find its session, with a block quoting the match.
	hits, err = Search(dir, "垃圾", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Path != a.Path() {
		t.Fatalf("short body search: got %d hits, want exactly session a", len(hits))
	}
	if len(hits[0].Blocks) == 0 || !strings.Contains(hits[0].Blocks[0].Text, "垃圾") {
		t.Fatalf("short-query blocks should quote the match, got %+v", hits[0].Blocks)
	}

	// A hit's anchor must be a verbatim substring of the conversation — the
	// web UI locates the message by it.
	sum, _, err := loadSummary(a.Path())
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hits {
		for _, b := range h.Blocks {
			if !strings.Contains(sum.text, b.Anchor) {
				t.Fatalf("anchor %q not found in session text", b.Anchor)
			}
		}
	}

	if hits, err = Search(dir, "完全不存在的内容", 10); err != nil || len(hits) != 0 {
		t.Fatalf("no-match search: got %d hits, err %v", len(hits), err)
	}
}

// The block grammar: matches sharing one display window are one place,
// matches further apart quote as separate blocks, and every anchor is a
// verbatim cut of the message it quotes.
func TestMatchBlocksGroupsNearSplitsFar(t *testing.T) {
	// One message, three matches: the first sits alone, the last two close
	// together. Window 160 + context 30: the lone match and the pair are more
	// than a window apart, so the answer is two blocks — the pair sharing one.
	text := "Alpha 先单独出现。" + strings.Repeat("中", 300) + "末尾 Alpha 与另一个 Alpha 相邻"
	blocks := matchBlocks(text, []string{"alpha"}, 4)
	if len(blocks) != 2 {
		t.Fatalf("got %d blocks, want 2: %+v", len(blocks), blocks)
	}
	if !strings.Contains(blocks[0].Text, "Alpha") || strings.Contains(blocks[0].Text, "相邻") {
		t.Fatalf("first block should quote the lone match only: %q", blocks[0].Text)
	}
	if !strings.Contains(blocks[1].Text, "末尾") || !strings.Contains(blocks[1].Text, "相邻") {
		t.Fatalf("second block should quote the pair together: %q", blocks[1].Text)
	}
	// Anchors are verbatim substrings — the UI's locate-by- Includes contract.
	for _, b := range blocks {
		if !strings.Contains(text, b.Anchor) {
			t.Fatalf("anchor %q is not a substring of the source", b.Anchor)
		}
	}

	// A lone match quotes one window with ellipses on both cut sides.
	one := matchBlocks(strings.Repeat("甲", 100)+"孤独命中"+strings.Repeat("乙", 300), []string{"命中"}, 4)
	if len(one) != 1 || !strings.HasPrefix(one[0].Text, "…") || !strings.HasSuffix(one[0].Text, "…") {
		t.Fatalf("single block: %+v", one)
	}

	// No match, no blocks; and the cap holds when every block is its own.
	if got := matchBlocks(text, []string{"不存在"}, 4); got != nil {
		t.Fatalf("no-match: got %+v", got)
	}
	many := strings.Repeat("词 "+strings.Repeat("隔", 200), 6)
	if got := matchBlocks(many, []string{"词"}, 4); len(got) != 4 {
		t.Fatalf("cap: got %d blocks, want 4", len(got))
	}

	// Case-insensitive, and folding that changes the rune count (İ) yields no
	// blocks rather than a drifting anchor.
	if got := matchBlocks("QUOKKA facts", []string{"quokka"}, 4); len(got) != 1 {
		t.Fatalf("case fold: got %+v", got)
	}
	if got := matchBlocks("İstanbul", []string{"i̇s"}, 4); got != nil {
		t.Fatalf("rune-count-shifting fold must bail, got %+v", got)
	}
}

func TestSearchWorksWithoutIndex(t *testing.T) {
	// Off before anything touches the directory: the index file must never
	// come into existence, which is the point of the switch.
	t.Setenv("POGO_SESSION_INDEX", "off")
	dir := t.TempDir()
	a := newStoreIn(t, dir, "quarantine flow question")

	hits, err := Search(dir, "quarantine", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Fatalf("scan search: got %d hits, want 1", len(hits))
	}
	if len(hits[0].Blocks) == 0 || !strings.Contains(hits[0].Blocks[0].Text, "quarantine") {
		t.Fatalf("scan blocks should quote the match, got %+v", hits[0].Blocks)
	}

	// List stays correct (and index-file-free) with the index off.
	list, err := List(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Path != a.Path() {
		t.Fatalf("off-index list: %+v", list)
	}
	if _, err := os.Stat(filepath.Join(dir, IndexName)); !os.IsNotExist(err) {
		t.Error("index file created while index disabled")
	}
}

func TestAppendKeepsWorkingWhenIndexIsUnwritable(t *testing.T) {
	dir := t.TempDir()
	// A path whose parent does not exist: every index open fails, which is
	// the "index broken" case in miniature.
	t.Setenv("POGO_SESSION_INDEX", filepath.Join(dir, "no", "such", "dir", "index.db"))

	s := newStoreIn(t, dir, "still persists", "without an index")
	if err := s.Append(llm.UserText("third")); err != nil {
		t.Fatalf("append failed because of the index: %v", err)
	}
	list, err := List(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Messages != 3 {
		t.Fatalf("scan listing after indexless appends: %+v", list)
	}
}

func TestSidebarEditsReachTheIndex(t *testing.T) {
	dir := t.TempDir()
	s := newStoreIn(t, dir, "original title")

	pin := true
	if err := s.AppendMeta(&Meta{Pinned: &pin}); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendMeta(&Meta{Title: strPtr("renamed")}); err != nil {
		t.Fatal(err)
	}

	// The listing must come from the index row touchIndex just wrote, without
	// a re-summarise having had to run: mtime and size match, so the row is
	// trusted. If the in-memory summary were wrong, this is where it shows.
	info := findInfo(t, mustList(t, dir), s.Path())
	if !info.Pinned || info.Title != "renamed" {
		t.Errorf("index row missed sidebar edits: pinned=%v title=%q", info.Pinned, info.Title)
	}

	hits, err := Search(dir, "renamed", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Title != "renamed" {
		t.Errorf("search misses custom title: %+v", hits)
	}
}

func TestRebuildIndexRecreatesEverything(t *testing.T) {
	dir := t.TempDir()
	a := newStoreIn(t, dir, "rebuild me")
	// Seed the index, then corrupt the file past recognition.
	if _, err := List(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, IndexName), []byte("this is not a database"), 0o600); err != nil {
		t.Fatal(err)
	}

	n, err := RebuildIndex(dir)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("RebuildIndex indexed %d sessions, want 1", n)
	}
	list := mustList(t, dir)
	if len(list) != 1 || list[0].Path != a.Path() {
		t.Errorf("listing after rebuild: %+v", list)
	}
	hits, err := Search(dir, "rebuild", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 {
		t.Errorf("search after rebuild: %+v", hits)
	}
}

func TestIndexSurvivesConcurrentStores(t *testing.T) {
	dir := t.TempDir()
	done := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func(i int) {
			s, err := Create(dir, "/work", "k3")
			if err != nil {
				done <- err
				return
			}
			for j := 0; j < 20; j++ {
				if err := s.Append(llm.UserText(strings.Repeat("x", i+1))); err != nil {
					done <- err
					return
				}
			}
			done <- nil
		}(i)
	}
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatalf("concurrent append: %v", err)
		}
	}
	list := mustList(t, dir)
	if len(list) != 2 {
		t.Fatalf("listed %d sessions after concurrent writes, want 2", len(list))
	}
	for _, info := range list {
		if info.Messages != 20 {
			t.Errorf("session %s counted %d messages, want 20", info.ID, info.Messages)
		}
	}
}

func mustList(t *testing.T, dir string) []Info {
	t.Helper()
	list, err := List(dir)
	if err != nil {
		t.Fatal(err)
	}
	return list
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func strPtr(s string) *string { return &s }
