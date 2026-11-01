// Package information stores an owner's workspace-wide reference library.
// Metadata is a CAS tree; contents and assets are immutable snapshots outside
// agent workspaces. A reference never grants a tool or source-session right.
package information

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/inputref"
	"github.com/yosukeno/pogo/skills"
)

const MaxEntries = 10000
const MaxDepth = 32
const MaxRevision int64 = 1<<53 - 1
const MaxAssetBytes int64 = 512 << 20
const maxIndexBytes = 32 << 20
const maxEntryBytes = 2 << 20

var ErrConflict = errors.New("information library changed; reload before saving")
var ErrNameConflict = errors.New("a name already exists in this folder")
var ErrNotFound = errors.New("information entry does not exist")
var ErrNotEmpty = errors.New("folder is not empty; explicit recursive deletion is required")
var ErrInvalid = errors.New("invalid information library data")
var ErrTooLarge = errors.New("information file exceeds upload limit")

var idShape = regexp.MustCompile(`^inf_[a-f0-9]{32}$`)
var scopeShape = regexp.MustCompile(`^[a-f0-9]{64}$`)
var assetShape = regexp.MustCompile(`^information-assets/([a-f0-9]{64})/([^/]+)$`)

type Asset struct {
	Name   string `json:"name"`
	Mime   string `json:"mime"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
	Asset  string `json:"asset"`
}
type Shortcut struct {
	ID          string          `json:"id"`
	Title       string          `json:"title"`
	Content     string          `json:"content"`
	Parts       []inputref.Part `json:"parts,omitempty"`
	InputLayout string          `json:"input_layout,omitempty"`
	Icon        string          `json:"icon,omitempty"`
	Language    string          `json:"language,omitempty"`
}
type Entry struct {
	ID        string    `json:"id"`
	ParentID  string    `json:"parent_id"`
	Kind      string    `json:"kind"`
	Name      string    `json:"name"`
	Version   int64     `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
	File      *Asset    `json:"file,omitempty"`
	Shortcut  *Shortcut `json:"shortcut,omitempty"`
}
type TreeView struct {
	ScopeID  string  `json:"scope_id"`
	Revision int64   `json:"revision"`
	Entries  []Entry `json:"entries"`
	CanWrite bool    `json:"can_write"`
}
type CreateInput struct {
	ParentID string    `json:"parent_id"`
	Kind     string    `json:"kind"`
	Name     string    `json:"name"`
	File     *Asset    `json:"file,omitempty"`
	Shortcut *Shortcut `json:"shortcut,omitempty"`
}
type UpdateInput struct {
	Name     *string   `json:"name,omitempty"`
	ParentID *string   `json:"parent_id,omitempty"`
	Shortcut *Shortcut `json:"shortcut,omitempty"`
}
type SearchHit struct {
	Entry   Entry  `json:"entry"`
	Snippet string `json:"snippet,omitempty"`
}
type Store struct {
	root, scope string
	mu          *sync.Mutex
}

var locks sync.Map

// Namespace includes all authenticated identity dimensions and the trusted
// workspace. Same-project subjects deliberately do not share private data.
func Namespace(owner *identity.Scope, workspace string) (string, error) {
	if owner != nil && !owner.Valid() || !filepath.IsAbs(workspace) {
		return "", ErrInvalid
	}
	raw, _ := json.Marshal(struct {
		Version   string
		Owner     *identity.Scope
		Workspace string
	}{"pogo-information-v1", owner, filepath.Clean(workspace)})
	d := sha256.Sum256(raw)
	return hex.EncodeToString(d[:]), nil
}
func Open(root, scopeID string) (*Store, error) {
	if !scopeShape.MatchString(scopeID) || !filepath.IsAbs(root) {
		return nil, ErrInvalid
	}
	root = filepath.Clean(root)
	// The caller supplies the trusted private storage parent; never permit a
	// symlink namespace to alias another owner's directory.
	if err := noSymlinkPath(root); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	if err := noSymlinkPath(root); err != nil {
		return nil, err
	}
	m, _ := locks.LoadOrStore(root, &sync.Mutex{})
	s := &Store{root: root, scope: scopeID, mu: m.(*sync.Mutex)}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	for _, p := range []string{"entries", "assets", "tmp"} {
		if err := mkdir(r, p); err != nil {
			return nil, err
		}
	}
	if err := s.verifyScope(r); errors.Is(err, os.ErrNotExist) {
		// Refuse an existing foreign index before binding an older store.
		var old TreeView
		if e := decode(r, "index.json", maxIndexBytes, &old); e == nil && old.ScopeID != scopeID {
			return nil, ErrInvalid
		} else if e != nil && !errors.Is(e, os.ErrNotExist) {
			return nil, e
		}
		if err = atomicJSON(r, "scope.json", struct {
			ScopeID string `json:"scope_id"`
		}{scopeID}, true); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if _, err = s.readIndex(r); err != nil {
		return nil, err
	}
	return s, nil
}
func (s *Store) verifyScope(r *os.Root) error {
	var scope struct {
		ScopeID string `json:"scope_id"`
	}
	if err := decode(r, "scope.json", 4096, &scope); err != nil {
		return err
	}
	if scope.ScopeID != s.scope {
		return ErrInvalid
	}
	return nil
}
func (s *Store) Root() string    { return s.root }
func (s *Store) ScopeID() string { return s.scope }
func noSymlinkPath(p string) error {
	for p != "" {
		st, e := os.Lstat(p)
		if errors.Is(e, os.ErrNotExist) {
			q := filepath.Dir(p)
			if q == p {
				break
			}
			p = q
			continue
		}
		if e != nil {
			return e
		}
		if st.Mode()&os.ModeSymlink != 0 && p != "/var" && p != "/tmp" {
			return ErrInvalid
		}
		q := filepath.Dir(p)
		if q == p {
			break
		}
		p = q
	}
	return nil
}
func mkdir(r *os.Root, p string) error {
	prefix := ""
	for _, c := range strings.Split(filepath.ToSlash(p), "/") {
		if c == "" || c == "." || c == ".." {
			return ErrInvalid
		}
		prefix = filepath.Join(prefix, c)
		st, err := r.Lstat(prefix)
		if errors.Is(err, os.ErrNotExist) {
			// Another process may create the same directory after Lstat. Still
			// verify it below; EEXIST must not admit a file or symlink.
			if err = r.Mkdir(prefix, 0700); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
			st, err = r.Lstat(prefix)
		}
		if err != nil || !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
			return ErrInvalid
		}
	}
	return nil
}
func regular(r *os.Root, p string, limit int64) (*os.File, error) {
	parts := strings.Split(filepath.ToSlash(p), "/")
	prefix := ""
	for _, v := range parts {
		if v == "" || v == "." || v == ".." {
			return nil, ErrInvalid
		}
		prefix = filepath.Join(prefix, v)
		st, e := r.Lstat(prefix)
		if e != nil {
			return nil, e
		}
		if st.Mode()&os.ModeSymlink != 0 {
			return nil, ErrInvalid
		}
	}
	f, e := r.Open(p)
	if e != nil {
		return nil, e
	}
	st, e := f.Stat()
	if e != nil || !st.Mode().IsRegular() || st.Size() > limit {
		f.Close()
		return nil, ErrInvalid
	}
	return f, nil
}
func decode(r *os.Root, p string, limit int64, v any) error {
	f, e := regular(r, p, limit)
	if e != nil {
		return e
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, limit+1))
	d.DisallowUnknownFields()
	var extra any
	if d.Decode(v) != nil || d.Decode(&extra) != io.EOF {
		return ErrInvalid
	}
	return nil
}
func atomicJSON(r *os.Root, p string, v any, immutable bool) error {
	if err := mkdir(r, "tmp"); err != nil {
		return err
	}
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	limit := maxEntryBytes
	if p == "index.json" {
		limit = maxIndexBytes
	}
	if len(b) > limit {
		return ErrInvalid
	}
	token, e := newID()
	if e != nil {
		return e
	}
	tmp := "tmp/" + token
	f, e := r.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return e
	}
	defer r.Remove(tmp)
	if _, e = f.Write(b); e != nil {
		f.Close()
		return e
	}
	if e = f.Sync(); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	if immutable {
		if _, e = r.Lstat(p); e == nil {
			return ErrConflict
		} else if !errors.Is(e, os.ErrNotExist) {
			return e
		}
	}
	if err := r.Rename(tmp, p); err != nil {
		return err
	}
	if dir, err := r.Open(filepath.Dir(p)); err == nil {
		defer dir.Close()
		return dir.Sync()
	}
	return nil
}
func newID() (string, error) {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		return "", e
	}
	return "inf_" + hex.EncodeToString(b[:]), nil
}
func metadata(e Entry) Entry { e.Shortcut = nil; return e }
func (s *Store) readIndex(r *os.Root) (TreeView, error) {
	if err := s.verifyScope(r); err != nil {
		return TreeView{}, err
	}
	v := TreeView{ScopeID: s.scope, Entries: []Entry{}, CanWrite: true}
	if e := decode(r, "index.json", maxIndexBytes, &v); errors.Is(e, os.ErrNotExist) {
		return v, nil
	} else if e != nil {
		return TreeView{}, e
	}
	if v.ScopeID != s.scope || v.Revision < 1 || v.Revision > MaxRevision || !v.CanWrite || len(v.Entries) > MaxEntries {
		return TreeView{}, ErrInvalid
	}
	if e := validateTree(v.Entries); e != nil {
		return TreeView{}, e
	}
	return v, nil
}
func (s *Store) List() (TreeView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, e := os.OpenRoot(s.root)
	if e != nil {
		return TreeView{}, e
	}
	defer r.Close()
	return s.readIndex(r)
}
func entryPath(id string, version int64) string {
	return fmt.Sprintf("entries/%s/%d.json", id, version)
}
func (s *Store) get(r *os.Root, id string, version int64) (Entry, error) {
	if err := s.verifyScope(r); err != nil {
		return Entry{}, err
	}
	if !idShape.MatchString(id) || version < 0 || version > MaxRevision {
		return Entry{}, ErrInvalid
	}
	if version == 0 {
		v, e := s.readIndex(r)
		if e != nil {
			return Entry{}, e
		}
		for _, entry := range v.Entries {
			if entry.ID == id {
				version = entry.Version
				break
			}
		}
		if version == 0 {
			return Entry{}, ErrNotFound
		}
	}
	var e Entry
	if err := decode(r, entryPath(id, version), maxEntryBytes, &e); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Entry{}, ErrNotFound
		}
		return Entry{}, err
	}
	if e.ID != id || e.Version != version || validateEntry(e, false) != nil {
		return Entry{}, ErrInvalid
	}
	return e, nil
}

// Explicit historic versions remain available after a rename, move or delete.
func (s *Store) Get(id string, version int64) (Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, e := os.OpenRoot(s.root)
	if e != nil {
		return Entry{}, e
	}
	defer r.Close()
	return s.get(r, id, version)
}
func validName(v string) bool {
	if v == "" || strings.TrimSpace(v) != v || utf8.RuneCountInString(v) > 160 || !utf8.ValidString(v) || v == "." || v == ".." {
		return false
	}
	for _, c := range v {
		if unicode.IsControl(c) || c == '/' || c == '\\' {
			return false
		}
	}
	return true
}
func validateEntry(e Entry, meta bool) error {
	if !idShape.MatchString(e.ID) || e.ParentID != "" && !idShape.MatchString(e.ParentID) || !validName(e.Name) || e.Version < 1 || e.Version > MaxRevision || e.CreatedAt.IsZero() || e.UpdatedAt.IsZero() {
		return ErrInvalid
	}
	switch e.Kind {
	case "folder":
		if e.File != nil || e.Shortcut != nil {
			return ErrInvalid
		}
	case "file":
		if e.File == nil || e.Shortcut != nil || !validAsset(*e.File) {
			return ErrInvalid
		}
	case "shortcut":
		if e.File != nil || !meta && e.Shortcut == nil {
			return ErrInvalid
		}
		if e.Shortcut != nil {
			q := e.Shortcut
			p := inputref.Part{Type: "shortcut", ShortcutID: q.ID, Title: q.Title, Text: q.Content, Parts: q.Parts, Icon: q.Icon, Language: q.Language}
			if msg := skills.ValidateInputParts([]inputref.Part{p}, q.InputLayout, "information"); msg != "" {
				return fmt.Errorf("%w: %s", ErrInvalid, msg)
			}
		}
	default:
		return ErrInvalid
	}
	return nil
}
func validateTree(es []Entry) error {
	m := map[string]Entry{}
	names := map[string]bool{}
	for _, e := range es {
		if validateEntry(e, true) != nil || m[e.ID].ID != "" || e.Shortcut != nil {
			return ErrInvalid
		}
		key := e.ParentID + "\x00" + e.Name
		if names[key] {
			return ErrNameConflict
		}
		names[key] = true
		m[e.ID] = e
	}
	for _, e := range es {
		seen := map[string]bool{e.ID: true}
		p := e.ParentID
		depth := 0
		for p != "" {
			depth++
			if depth > MaxDepth || seen[p] {
				return ErrInvalid
			}
			seen[p] = true
			parent, ok := m[p]
			if !ok || parent.Kind != "folder" {
				return ErrInvalid
			}
			p = parent.ParentID
		}
	}
	return nil
}
func cloneEntry(e Entry) Entry {
	b, _ := json.Marshal(e)
	var q Entry
	_ = json.Unmarshal(b, &q)
	return q
}
func (s *Store) mutate(revision int64, fn func(*os.Root, *TreeView) (*Entry, error)) (TreeView, *Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, e := os.OpenRoot(s.root)
	if e != nil {
		return TreeView{}, nil, e
	}
	defer r.Close()
	v, e := s.readIndex(r)
	if e != nil {
		return v, nil, e
	}
	if revision < 0 || revision >= MaxRevision {
		return v, nil, ErrInvalid
	}
	if v.Revision != revision {
		return v, nil, ErrConflict
	}
	entry, e := fn(r, &v)
	if e != nil {
		return v, nil, e
	}
	if len(v.Entries) > MaxEntries {
		return v, nil, ErrInvalid
	}
	if e = validateTree(v.Entries); e != nil {
		return v, nil, e
	}
	v.Revision++
	if e = atomicJSON(r, "index.json", v, false); e != nil {
		return v, nil, e
	}
	return v, entry, nil
}
func (s *Store) writeEntry(r *os.Root, e Entry) error {
	if err := validateEntry(e, false); err != nil {
		return err
	}
	if e.File != nil {
		f, _, err := s.assetRead(r, e.File.Asset)
		if err != nil {
			return err
		}
		f.Close()
	}
	if e.Shortcut != nil {
		seen := map[string]bool{}
		for _, p := range inputref.Flatten(e.Shortcut.Parts) {
			if p.Type == "file" && !seen[p.Path] {
				seen[p.Path] = true
				f, _, err := s.assetRead(r, p.Path)
				if err != nil {
					return err
				}
				f.Close()
			}
		}
	}
	if err := mkdir(r, "entries/"+e.ID); err != nil {
		return err
	}
	return atomicJSON(r, entryPath(e.ID, e.Version), e, true)
}
func (s *Store) Create(revision int64, in CreateInput) (TreeView, *Entry, error) {
	return s.mutate(revision, func(r *os.Root, v *TreeView) (*Entry, error) {
		id, err := newID()
		if err != nil {
			return nil, err
		}
		now := time.Now().UTC()
		e := cloneEntry(Entry{ID: id, ParentID: in.ParentID, Kind: in.Kind, Name: in.Name, Version: 1, CreatedAt: now, UpdatedAt: now, File: in.File, Shortcut: in.Shortcut})
		if e.Kind == "shortcut" && e.Shortcut != nil {
			e.Shortcut.Title = e.Name
		}
		v.Entries = append(v.Entries, metadata(e))
		if err := validateTree(v.Entries); err != nil {
			return nil, err
		}
		if err := s.writeEntry(r, e); err != nil {
			return nil, err
		}
		return &e, nil
	})
}
func (s *Store) Update(revision int64, id string, in UpdateInput) (TreeView, *Entry, error) {
	return s.mutate(revision, func(r *os.Root, v *TreeView) (*Entry, error) {
		for i, old := range v.Entries {
			if old.ID != id {
				continue
			}
			e, err := s.get(r, id, old.Version)
			if err != nil {
				return nil, err
			}
			if in.Name != nil {
				e.Name = *in.Name
			}
			if in.ParentID != nil {
				e.ParentID = *in.ParentID
			}
			if in.Shortcut != nil {
				if e.Kind != "shortcut" {
					return nil, ErrInvalid
				}
				e.Shortcut = in.Shortcut
				if in.Name == nil {
					e.Name = in.Shortcut.Title
				}
			}
			e = cloneEntry(e)
			if e.Kind == "shortcut" && e.Shortcut != nil {
				e.Shortcut.Title = e.Name
			}
			e.Version++
			// A crash after writing a version but before publishing the index
			// may leave an orphan snapshot. Never replace it or wedge retries.
			for {
				if _, statErr := r.Lstat(entryPath(e.ID, e.Version)); errors.Is(statErr, os.ErrNotExist) {
					break
				} else if statErr != nil {
					return nil, statErr
				}
				e.Version++
				if e.Version > MaxRevision {
					return nil, ErrInvalid
				}
			}
			e.UpdatedAt = time.Now().UTC()
			v.Entries[i] = metadata(e)
			if err := validateTree(v.Entries); err != nil {
				return nil, err
			}
			if err := s.writeEntry(r, e); err != nil {
				return nil, err
			}
			return &e, nil
		}
		return nil, ErrNotFound
	})
}
func (s *Store) Delete(revision int64, id string, recursive bool) (TreeView, *Entry, error) {
	return s.mutate(revision, func(r *os.Root, v *TreeView) (*Entry, error) {
		if !idShape.MatchString(id) {
			return nil, ErrInvalid
		}
		remove := map[string]bool{id: true}
		found := false
		for _, e := range v.Entries {
			if e.ID == id {
				found = true
			}
		}
		if !found {
			return nil, ErrNotFound
		}
		for changed := true; changed; {
			changed = false
			for _, e := range v.Entries {
				if remove[e.ParentID] && !remove[e.ID] {
					if !recursive {
						return nil, ErrNotEmpty
					}
					remove[e.ID] = true
					changed = true
				}
			}
		}
		out := make([]Entry, 0, len(v.Entries))
		for _, e := range v.Entries {
			if !remove[e.ID] {
				out = append(out, e)
			}
		}
		v.Entries = out
		return nil, nil
	})
}

// Search prioritizes names. Content scanning has a single bounded budget;
// it is a useful small-library fallback, not an unbounded prompt expansion.
func (s *Store) Search(query string, limit int) ([]SearchHit, error) {
	query = strings.ToLower(strings.TrimSpace(query))
	if query == "" || len(query) > 512 {
		return nil, ErrInvalid
	}
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := os.OpenRoot(s.root)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	v, err := s.readIndex(r)
	if err != nil {
		return nil, err
	}
	hits := []SearchHit{}
	seen := map[string]bool{}
	sort.SliceStable(v.Entries, func(i, j int) bool { return v.Entries[i].Name < v.Entries[j].Name })
	for _, e := range v.Entries {
		if strings.Contains(strings.ToLower(e.Name), query) {
			hits = append(hits, SearchHit{Entry: e})
			seen[e.ID] = true
			if len(hits) >= limit {
				return hits, nil
			}
		}
	}
	budget, count := 2<<20, 0
	for _, e := range v.Entries {
		if seen[e.ID] || e.Kind != "shortcut" {
			continue
		}
		count++
		if count > 128 || budget <= 0 {
			break
		}
		full, err := s.get(r, e.ID, e.Version)
		if err != nil {
			return nil, err
		}
		q := full.Shortcut
		data := q.Content
		for _, p := range inputref.Flatten(q.Parts) {
			if p.Type == "text" || p.Type == "element" {
				data += "\n" + p.Text
			} else if p.Type == "office" && p.Office != nil {
				data += "\n" + p.Office.Text
			}
		}
		budget -= len(data)
		if strings.Contains(strings.ToLower(data), query) {
			runes := []rune(data)
			if len(runes) > 240 {
				runes = runes[:240]
			}
			hits = append(hits, SearchHit{Entry: e, Snippet: string(runes)})
			if len(hits) >= limit {
				break
			}
		}
	}
	return hits, nil
}
