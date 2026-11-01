package information

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"strings"
)

const maxMaterializationCandidates = 128

// MaterializedAsset distinguishes a borrowed public file from this batch's new
// staged copy. Path is not published until Commit. Never delete Path on failure:
// Rollback owns only unpublished staging directories, never public copies.
type MaterializedAsset struct {
	Path    string
	Asset   Asset
	Created bool
}

type materializationCandidates struct {
	Version   int      `json:"version"`
	ScopeID   string   `json:"scope_id"`
	Workspace string   `json:"workspace"`
	SHA256    string   `json:"sha256"`
	Name      string   `json:"name"`
	Paths     []string `json:"paths"`
}
type pendingMaterialization struct {
	staging, destination string
	asset                Asset
	stagingInfo          os.FileInfo
}

// Materialization holds one scope/workspace lock until the complete reference
// resolves. The index is private to the source Store, never in the user's tree.
// Legacy paths must come from trusted same-owner persisted references, not a
// request body or a scan of otherwise unattributed workspace files.
// Each batch is used serially; independent batches coordinate across processes.
type Materialization struct {
	store      *Store
	workspace  string
	root       *os.Root
	rootInfo   os.FileInfo
	lockFile   *os.File
	unlock     func()
	reusable   bool
	closed     bool
	cacheDir   string
	legacy     []string
	selected   map[string]MaterializedAsset
	candidates map[string]materializationCandidates
	pending    []pendingMaterialization
}

func (s *Store) BeginMaterialization(workspace string, legacyPaths ...string) (*Materialization, error) {
	if !filepath.IsAbs(workspace) {
		return nil, ErrInvalid
	}
	if err := noSymlinkPath(workspace); err != nil {
		return nil, err
	}
	canonical, err := filepath.EvalSymlinks(filepath.Clean(workspace))
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(canonical)
	if err != nil {
		return nil, err
	}
	info, err := root.Stat(".")
	if err != nil {
		root.Close()
		return nil, err
	}
	digest := sha256.Sum256([]byte(canonical))
	key := hex.EncodeToString(digest[:])
	// Never wait for a process lock while holding the Store mutex: its current
	// owner may need that mutex to commit its index and release the process lock.
	lockFile, err := s.materializationLockFile(key)
	if err != nil {
		root.Close()
		return nil, err
	}
	unlock, reusable, err := lockMaterialization(lockFile)
	if err != nil {
		lockFile.Close()
		root.Close()
		return nil, err
	}
	m := &Materialization{store: s, workspace: canonical, root: root, rootInfo: info, lockFile: lockFile, unlock: unlock, reusable: reusable && materializationReuseSupported, cacheDir: "materializations/" + key, legacy: append([]string(nil), legacyPaths...), selected: map[string]MaterializedAsset{}, candidates: map[string]materializationCandidates{}}
	if err = m.workspaceCurrent(); err != nil {
		m.Rollback()
		return nil, err
	}
	for _, prefix := range []string{"uploads", "uploads/information"} {
		if err = mkdir(root, prefix); err != nil {
			m.Rollback()
			return nil, err
		}
	}
	return m, nil
}

func (s *Store) materializationLockFile(key string) (*os.File, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	root, err := os.OpenRoot(s.root)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if err = s.verifyScope(root); err != nil {
		return nil, err
	}
	if err = mkdir(root, "materializations/locks"); err != nil {
		return nil, err
	}
	p := "materializations/locks/" + key + ".lock"
	if st, e := root.Lstat(p); e == nil && (!st.Mode().IsRegular() || !singleLink(st)) {
		return nil, ErrInvalid
	} else if e != nil && !errors.Is(e, os.ErrNotExist) {
		return nil, e
	}
	// Elect a creator atomically, then let other processes open that same
	// inode. Never truncate or replace a live process's lock file.
	f, err := root.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if errors.Is(err, os.ErrExist) {
		f, err = root.OpenFile(p, os.O_RDWR, 0600)
	}
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	named, e := root.Lstat(p)
	if err != nil || e != nil || !st.Mode().IsRegular() || !singleLink(st) || !named.Mode().IsRegular() || !os.SameFile(st, named) {
		f.Close()
		return nil, ErrInvalid
	}
	return f, nil
}

func singleLink(info os.FileInfo) bool {
	stat := reflect.Indirect(reflect.ValueOf(info.Sys()))
	if !stat.IsValid() || stat.Kind() != reflect.Struct {
		return !materializationReuseSupported
	}
	links := stat.FieldByName("Nlink")
	if !links.IsValid() || !links.CanUint() {
		return !materializationReuseSupported
	}
	return links.Uint() == 1
}
func materializationKey(asset Asset) string {
	sum := sha256.Sum256([]byte(asset.SHA256 + "\x00" + asset.Name))
	return hex.EncodeToString(sum[:])
}
func materializationPath(p, name string) bool {
	parts := strings.Split(p, "/")
	return len(parts) == 4 && parts[0] == "uploads" && parts[1] == "information" && idShape.MatchString(parts[2]) && parts[3] == name && name == safeName(name)
}

// IsMaterializedAssetPath recognizes the workspace-level copy namespace. It is
// only a shape check, never an ownership or read/write permission grant. These
// copies may be shared by multiple conversations and survive session deletion;
// users can remove them explicitly through the normal workspace file controls.
func IsMaterializedAssetPath(p string) bool {
	return materializationPath(p, path.Base(p))
}
func (m *Materialization) workspaceCurrent() error {
	if m.closed {
		return ErrInvalid
	}
	if err := noSymlinkPath(m.workspace); err != nil {
		return err
	}
	info, err := os.Stat(m.workspace)
	if err != nil {
		return err
	}
	if !os.SameFile(m.rootInfo, info) {
		return ErrInvalid
	}
	return nil
}
func (m *Materialization) readCandidates(asset Asset) (materializationCandidates, error) {
	empty := materializationCandidates{Version: 1, ScopeID: m.store.scope, Workspace: m.workspace, SHA256: asset.SHA256, Name: asset.Name, Paths: []string{}}
	if !m.reusable {
		return empty, nil
	}
	m.store.mu.Lock()
	defer m.store.mu.Unlock()
	root, err := os.OpenRoot(m.store.root)
	if err != nil {
		return empty, err
	}
	defer root.Close()
	if err = m.store.verifyScope(root); err != nil {
		return empty, err
	}
	var value materializationCandidates
	err = decode(root, m.cacheDir+"/"+materializationKey(asset)+".json", maxEntryBytes, &value)
	if errors.Is(err, os.ErrNotExist) {
		return empty, nil
	}
	if err != nil {
		return empty, err
	}
	if value.Version != 1 || value.ScopeID != m.store.scope || value.Workspace != m.workspace || value.SHA256 != asset.SHA256 || value.Name != asset.Name || len(value.Paths) > maxMaterializationCandidates {
		return empty, ErrInvalid
	}
	for _, p := range value.Paths {
		if !materializationPath(p, asset.Name) {
			return empty, ErrInvalid
		}
	}
	return value, nil
}

// pristine opens without symlink ancestors, checks independent single-link
// regular bytes, and checks the pathname/inode again after the streamed hash.
// A changed/missing/unreadable candidate is simply not reused.
func (m *Materialization) pristine(p string, asset Asset, source os.FileInfo) bool {
	f, err := regular(m.root, p, MaxAssetBytes)
	if err != nil {
		return false
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil || before.Size() != asset.Size || before.Mode().Perm()&0022 != 0 || !singleLink(before) || source != nil && os.SameFile(before, source) {
		return false
	}
	sum := sha256.New()
	n, err := io.Copy(sum, io.LimitReader(f, asset.Size+1))
	if err != nil || n != asset.Size || hex.EncodeToString(sum.Sum(nil)) != asset.SHA256 {
		return false
	}
	after, err := f.Stat()
	if err != nil || after.Mode().Perm()&0022 != 0 || !singleLink(after) || !os.SameFile(before, after) || before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return false
	}
	// Recheck every ancestor, not just the opened file: an internal symlink is
	// still an alias even when os.Root correctly prevents escape from the root.
	prefix := ""
	for _, part := range strings.Split(p, "/") {
		prefix = path.Join(prefix, part)
		named, e := m.root.Lstat(prefix)
		if e != nil || named.Mode()&os.ModeSymlink != 0 {
			return false
		}
	}
	named, err := m.root.Lstat(p)
	return err == nil && named.Mode().IsRegular() && named.Mode().Perm()&0022 == 0 && singleLink(named) && os.SameFile(after, named)
}

func (m *Materialization) MaterializeAsset(handle string) (MaterializedAsset, error) {
	if err := m.workspaceCurrent(); err != nil {
		return MaterializedAsset{}, err
	}
	source, asset, err := m.store.AssetRead(handle)
	if err != nil {
		return MaterializedAsset{}, err
	}
	defer source.Close()
	sourceInfo, err := source.Stat()
	if err != nil {
		return MaterializedAsset{}, err
	}
	key := materializationKey(asset)
	if chosen, ok := m.selected[key]; ok {
		return chosen, nil
	}
	candidates, err := m.readCandidates(asset)
	if err != nil {
		return MaterializedAsset{}, err
	}
	if m.reusable {
		seen := map[string]bool{}
		for _, candidate := range append(append([]string(nil), candidates.Paths...), m.legacy...) {
			if seen[candidate] || !materializationPath(candidate, asset.Name) {
				continue
			}
			seen[candidate] = true
			if m.pristine(candidate, asset, sourceInfo) {
				result := MaterializedAsset{Path: candidate, Asset: asset}
				m.selected[key] = result
				m.candidates[key] = withCandidate(candidates, candidate)
				return result, nil
			}
		}
	}
	id, err := newID()
	if err != nil {
		return MaterializedAsset{}, err
	}
	staging := "uploads/information/.pending-" + id
	destination := "uploads/information/" + id
	if err = m.root.Mkdir(staging, 0700); err != nil {
		return MaterializedAsset{}, err
	}
	stagingInfo, err := m.root.Lstat(staging)
	if err != nil {
		return MaterializedAsset{}, err
	}
	pending := pendingMaterialization{staging: staging, destination: destination, asset: asset, stagingInfo: stagingInfo}
	m.pending = append(m.pending, pending)
	dst, err := m.root.OpenFile(staging+"/"+asset.Name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return MaterializedAsset{}, err
	}
	sum := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(dst, sum), io.LimitReader(source, asset.Size+1))
	if copyErr == nil && (n != asset.Size || hex.EncodeToString(sum.Sum(nil)) != asset.SHA256) {
		copyErr = ErrInvalid
	}
	if copyErr == nil {
		copyErr = dst.Sync()
	}
	closeErr := dst.Close()
	if copyErr != nil {
		return MaterializedAsset{}, copyErr
	}
	if closeErr != nil {
		return MaterializedAsset{}, closeErr
	}
	result := MaterializedAsset{Path: destination + "/" + asset.Name, Asset: asset, Created: true}
	m.selected[key] = result
	m.candidates[key] = withCandidate(candidates, result.Path)
	return result, nil
}
func withCandidate(value materializationCandidates, p string) materializationCandidates {
	paths := []string{p}
	for _, old := range value.Paths {
		if old != p && len(paths) < maxMaterializationCandidates {
			paths = append(paths, old)
		}
	}
	value.Paths = paths
	return value
}

// Commit first records candidates under the still-held private process lock,
// then atomically publishes complete random directories. Any failed publication
// leaves at most a stale cache candidate, which must pass full verification on
// its next use. Public paths are never rollback-owned, including partial commit.
func (m *Materialization) Commit() error {
	if err := m.workspaceCurrent(); err != nil {
		return err
	}
	for _, selected := range m.selected {
		if !selected.Created && !m.pristine(selected.Path, selected.Asset, nil) {
			return ErrInvalid
		}
	}
	for _, p := range m.pending {
		if !m.pristine(p.staging+"/"+p.asset.Name, p.asset, nil) {
			return ErrInvalid
		}
	}
	if m.reusable {
		m.store.mu.Lock()
		err := func() error {
			root, err := os.OpenRoot(m.store.root)
			if err != nil {
				return err
			}
			defer root.Close()
			if err = m.store.verifyScope(root); err != nil {
				return err
			}
			if err = mkdir(root, m.cacheDir); err != nil {
				return err
			}
			for key, value := range m.candidates {
				if err = atomicJSON(root, m.cacheDir+"/"+key+".json", value, false); err != nil {
					return err
				}
			}
			return nil
		}()
		m.store.mu.Unlock()
		if err != nil {
			return err
		}
	}
	for _, p := range m.pending {
		if _, err := m.root.Lstat(p.destination); !errors.Is(err, os.ErrNotExist) {
			if err != nil {
				return err
			}
			return ErrInvalid
		}
		// Linux/macOS use kernel no-replace publication, including an empty
		// destination-directory race. No source or workspace file is hard-linked.
		if err := publishMaterialization(m.root, p.staging, p.destination); err != nil {
			return err
		}
	}
	if !materializationReuseSupported {
		m.finish()
		return nil
	}
	if parent, err := m.root.Open("uploads/information"); err == nil {
		err = parent.Sync()
		parent.Close()
		if err != nil {
			return err
		}
	} else {
		return err
	}
	m.finish()
	return nil
}

// Rollback only removes our unpublished .pending directories. Never remove
// borrowed or already-published paths, even if Commit failed after a rename.
func (m *Materialization) Rollback() {
	if m == nil || m.closed {
		return
	}
	for _, p := range m.pending {
		if current, err := m.root.Lstat(p.staging); err == nil && current.IsDir() && current.Mode()&os.ModeSymlink == 0 && os.SameFile(current, p.stagingInfo) {
			_ = m.root.RemoveAll(p.staging)
		}
	}
	m.finish()
}
func (m *Materialization) finish() {
	if m.closed {
		return
	}
	m.closed = true
	m.root.Close()
	m.unlock()
	m.lockFile.Close()
}
