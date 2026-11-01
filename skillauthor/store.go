package skillauthor

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

type receipt struct {
	Payload  string `json:"payload"`
	SkillID  string `json:"skill_id"`
	Revision string `json:"revision"`
}
type scopeData struct {
	Format   int                   `json:"format"`
	Scope    string                `json:"scope"`
	Skills   map[string][]Snapshot `json:"skills"`
	Requests map[string]receipt    `json:"requests"`
}

type Store struct {
	root, scope string
	mu          *sync.Mutex
}

var storeLocks sync.Map

// New is lazy: it validates a host-issued scope and trusted storage path without
// creating directories. The scope must include authenticated owner and Agent ID.
func New(root, scope string) (*Store, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) == string(filepath.Separator) || !scopeRE.MatchString(scope) {
		return nil, ErrInvalid
	}
	root = filepath.Clean(root)
	if err := noSymlinks(root); err != nil {
		return nil, err
	}
	m, _ := storeLocks.LoadOrStore(root+"\x00"+scope, &sync.Mutex{})
	return &Store{root: root, scope: scope, mu: m.(*sync.Mutex)}, nil
}

func noSymlinks(p string) error {
	for {
		st, err := os.Lstat(p)
		if err == nil && st.Mode()&os.ModeSymlink != 0 {
			return invalid("private storage must not traverse symlinks")
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		parent := filepath.Dir(p)
		if parent == p {
			return nil
		}
		p = parent
	}
}
func (s *Store) open(create bool) (*os.Root, error) {
	if err := noSymlinks(s.root); err != nil {
		return nil, err
	}
	if create {
		if err := os.MkdirAll(s.root, 0700); err != nil {
			return nil, err
		}
	}
	if err := noSymlinks(s.root); err != nil {
		return nil, err
	}
	return os.OpenRoot(s.root)
}
func (s *Store) filename() string { return s.scope + ".json" }
func emptyData(scope string) scopeData {
	return scopeData{Format: 1, Scope: scope, Skills: map[string][]Snapshot{}, Requests: map[string]receipt{}}
}
func (s *Store) load(r *os.Root) (scopeData, error) {
	data := emptyData(s.scope)
	info, err := r.Lstat(s.filename())
	if errors.Is(err, os.ErrNotExist) {
		return data, nil
	}
	if err != nil {
		return data, err
	}
	if !info.Mode().IsRegular() || info.Size() > MaxScopeBytes {
		return data, invalid("private Skill store is not a bounded regular file")
	}
	f, err := r.Open(s.filename())
	if err != nil {
		return data, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !os.SameFile(st, info) {
		return data, invalid("private store changed during read")
	}
	decoder := json.NewDecoder(io.LimitReader(f, MaxScopeBytes+1))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&data); err != nil {
		return data, fmt.Errorf("%w: unreadable private Skill store", ErrInvalid)
	}
	if decoder.Decode(new(any)) != io.EOF {
		return data, invalid("private store has trailing data")
	}
	if err = validateData(data, s.scope); err != nil {
		return data, err
	}
	return data, nil
}
func cloneSnapshot(v Snapshot) Snapshot { v.Files = append([]File(nil), v.Files...); return v }
func (s *Store) List() ([]Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.open(false)
	if errors.Is(err, os.ErrNotExist) {
		return []Snapshot{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer r.Close()
	d, err := s.load(r)
	if err != nil {
		return nil, err
	}
	out := make([]Snapshot, 0, len(d.Skills))
	for _, versions := range d.Skills {
		out = append(out, cloneSnapshot(versions[len(versions)-1]))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
func (s *Store) Read(id, revision string) (Snapshot, error) {
	if !idRE.MatchString(id) || (revision != "" && !scopeRE.MatchString(revision)) {
		return Snapshot{}, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.open(false)
	if errors.Is(err, os.ErrNotExist) {
		return Snapshot{}, ErrNotFound
	}
	if err != nil {
		return Snapshot{}, err
	}
	defer r.Close()
	d, err := s.load(r)
	if err != nil {
		return Snapshot{}, err
	}
	return readSnapshot(d, id, revision)
}

// Versions returns oldest-first immutable snapshots. Hosts may omit Files when
// presenting this list; a version is never deleted or silently overwritten.
func (s *Store) Versions(id string) ([]Snapshot, error) {
	if !idRE.MatchString(id) {
		return nil, ErrInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.open(false)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	defer r.Close()
	d, err := s.load(r)
	if err != nil {
		return nil, err
	}
	versions := d.Skills[id]
	if len(versions) == 0 {
		return nil, ErrNotFound
	}
	out := make([]Snapshot, len(versions))
	for i, v := range versions {
		out[i] = cloneSnapshot(v)
	}
	return out, nil
}
func readSnapshot(d scopeData, id, revision string) (Snapshot, error) {
	versions := d.Skills[id]
	if len(versions) == 0 {
		return Snapshot{}, ErrNotFound
	}
	if revision == "" {
		return cloneSnapshot(versions[len(versions)-1]), nil
	}
	for _, v := range versions {
		if v.Revision == revision {
			return cloneSnapshot(v), nil
		}
	}
	return Snapshot{}, ErrNotFound
}
func digest(value any) string {
	raw, _ := json.Marshal(value)
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}
func revision(v Snapshot) string {
	return digest(struct {
		ID          string
		Name        string
		Description string
		Version     int
		Enabled     bool
		Files       []File
	}{v.ID, v.Name, v.Description, v.Version, v.Enabled, v.Files})
}

// Save atomically commits a complete bundle and an idempotency receipt. It never
// installs runtime code, projects tools, or chooses an owner/Agent itself.
func (s *Store) Save(ctx context.Context, in SaveRequest) (SaveResult, error) {
	var result SaveResult
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if !requestRE.MatchString(in.RequestID) || (in.SkillID != "" && !idRE.MatchString(in.SkillID)) || (in.SkillID == "" && in.ExpectedRevision != "") || (in.SkillID != "" && !scopeRE.MatchString(in.ExpectedRevision)) {
		return result, ErrInvalid
	}
	name, description, err := ValidateFiles(in.Files)
	if err != nil {
		return result, err
	}
	in.Files = append([]File(nil), in.Files...)
	sort.Slice(in.Files, func(i, j int) bool { return in.Files[i].Path < in.Files[j].Path })
	payload := digest(in)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err = ctx.Err(); err != nil {
		return result, err
	}
	r, err := s.open(true)
	if err != nil {
		return result, err
	}
	defer r.Close()
	d, err := s.load(r)
	if err != nil {
		return result, err
	}
	if old, ok := d.Requests[in.RequestID]; ok {
		if old.Payload != payload {
			return result, ErrRequestConflict
		}
		v, err := readSnapshot(d, old.SkillID, old.Revision)
		return SaveResult{Skill: v, RequestID: in.RequestID, Reused: true}, err
	}
	if len(d.Requests) >= MaxRequests {
		return result, ErrLimit
	}
	now := time.Now().UTC()
	next := Snapshot{Name: name, Description: description, Version: 1, Enabled: true, Files: in.Files, CreatedAt: now, UpdatedAt: now}
	reused := false
	if in.SkillID != "" {
		old, err := readSnapshot(d, in.SkillID, "")
		if err != nil {
			return result, err
		}
		if old.Revision != in.ExpectedRevision {
			return result, ErrConflict
		}
		if old.Name != name {
			return result, invalid("Skill name is immutable; create a separate Skill to rename")
		}
		next.ID = old.ID
		next.Version = old.Version + 1
		next.CreatedAt = old.CreatedAt
		next.Enabled = old.Enabled
		if in.Activate != nil {
			next.Enabled = *in.Activate
		}
		if next.Enabled == old.Enabled && digest(next.Files) == digest(old.Files) {
			next = old
			reused = true
		} else if len(d.Skills[old.ID]) >= MaxVersions {
			return result, ErrLimit
		}
	} else {
		if len(d.Skills) >= MaxSkills {
			return result, ErrLimit
		}
		for _, versions := range d.Skills {
			if versions[len(versions)-1].Name == name {
				return result, ErrNameConflict
			}
		}
		var random [16]byte
		if _, err = rand.Read(random[:]); err != nil {
			return result, err
		}
		next.ID = "as_" + hex.EncodeToString(random[:])
		if in.Activate != nil {
			next.Enabled = *in.Activate
		}
	}
	if !reused {
		next.Revision = revision(next)
		d.Skills[next.ID] = append(d.Skills[next.ID], next)
	}
	d.Requests[in.RequestID] = receipt{Payload: payload, SkillID: next.ID, Revision: next.Revision}
	if err = s.commit(ctx, r, d); err != nil {
		return result, err
	}
	return SaveResult{Skill: cloneSnapshot(next), RequestID: in.RequestID, Reused: reused}, nil
}
func (s *Store) commit(ctx context.Context, r *os.Root, d scopeData) error {
	raw, err := json.Marshal(d)
	if err != nil {
		return err
	}
	if len(raw) > MaxScopeBytes {
		return ErrLimit
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		return err
	}
	temp := ".skillauthor-" + hex.EncodeToString(random[:])
	f, err := r.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer r.Remove(temp)
	_, err = f.Write(raw)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if st, e := r.Lstat(s.filename()); e == nil && !st.Mode().IsRegular() {
		return ErrInvalid
	} else if e != nil && !errors.Is(e, os.ErrNotExist) {
		return e
	}
	if err = r.Rename(temp, s.filename()); err != nil {
		return err
	}
	if dir, e := r.Open("."); e == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}
func validateData(d scopeData, scope string) error {
	if d.Format != 1 || d.Scope != scope || d.Skills == nil || d.Requests == nil || len(d.Skills) > MaxSkills || len(d.Requests) > MaxRequests {
		return invalid("private store scope or format mismatch")
	}
	names := map[string]bool{}
	for id, versions := range d.Skills {
		if !idRE.MatchString(id) || len(versions) == 0 || len(versions) > MaxVersions {
			return ErrInvalid
		}
		lastName := ""
		var created time.Time
		for i, v := range versions {
			if v.ID != id || v.Version != i+1 || !scopeRE.MatchString(v.Revision) || v.Revision != revision(v) || v.CreatedAt.IsZero() || v.UpdatedAt.Before(v.CreatedAt) {
				return invalid("invalid Skill version")
			}
			name, description, err := ValidateFiles(v.Files)
			if err != nil || name != v.Name || description != v.Description {
				return invalid("invalid stored Skill bundle")
			}
			if i > 0 && (v.Name != lastName || !v.CreatedAt.Equal(created)) {
				return ErrInvalid
			}
			lastName = v.Name
			created = v.CreatedAt
		}
		if names[lastName] {
			return ErrNameConflict
		}
		names[lastName] = true
	}
	for request, r := range d.Requests {
		if !requestRE.MatchString(request) || !scopeRE.MatchString(r.Payload) {
			return ErrInvalid
		}
		if _, err := readSnapshot(d, r.SkillID, r.Revision); err != nil {
			return ErrInvalid
		}
	}
	return nil
}
