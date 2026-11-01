package web

// Workflow publishing: the draft in workflows.json is the only editable
// thing; a publish freezes it into an immutable snapshot under
// workflows/versions/<wfID>/<hash>.json and moves the pointer in
// workflows/published.json. Runs start from the pointer's snapshot, never
// from the draft — editing a draft therefore cannot move a published
// pipeline, and rollback is a pointer move because snapshots never change.
//
// version_id is the first 12 hex of the snapshot definition's sha256, taken
// over the same Marshal shape the store writes: republishing an unchanged
// draft lands on the same version, by construction.
import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/yosukeno/pogo/workflow"
)

// errWorkflowUnpublished is the run gate's answer when no pointer exists.
var errWorkflowUnpublished = errors.New("工作流尚未发布，请先发布后再运行")

// publishPointer is published.json's per-workflow entry: which snapshot runs,
// plus the release label the publish dialog gave it.
type publishPointer struct {
	VersionID   string `json:"version_id"`
	Name        string `json:"name"`
	Desc        string `json:"desc"`
	PublishedAt string `json:"published_at"`
}

// publishedData is the on-disk shape of workflows/published.json.
type publishedData struct {
	Version   int                       `json:"version"`
	Workflows map[string]publishPointer `json:"workflows"`
}

// canonicalWorkflow is the hashed shape: exactly what a version snapshot
// stores and a run starts from. Field order fixed, no maps — the hash is
// stable by construction.
type canonicalWorkflow struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Revision    int             `json:"revision"`
	Nodes       []workflow.Node `json:"nodes"`
}

// workflowVersion is one immutable snapshot envelope. The workflow field is
// the hashed content; the rest is metadata for the versions list.
type workflowVersion struct {
	VersionID   string            `json:"version_id"`
	Name        string            `json:"name"`
	Desc        string            `json:"desc"`
	PublishedAt string            `json:"published_at"`
	Workflow    canonicalWorkflow `json:"workflow"`
}

// workflowVersionView is one row of the versions endpoint.
type workflowVersionView struct {
	VersionID   string `json:"version_id"`
	Name        string `json:"name"`
	Desc        string `json:"desc"`
	PublishedAt string `json:"published_at"`
	Current     bool   `json:"current"`
}

func (s *workflowStore) versionPath(wid, hash string) string {
	return filepath.Join(s.versionsDir, wid, hash+".json")
}

// canonicalDraft normalizes the draft and returns its snapshot shape, the
// canonical bytes, and the version id they hash to.
func (s *workflowStore) canonicalDraft(w Workflow) (canonicalWorkflow, []byte, string, error) {
	def := w.definition()
	// Normalization trims/deduplicates nested slices and pointer fields. Freeze
	// them before touching them so RLock readers and immutable receipts never
	// share writable backing arrays with canonicalization.
	raw, err := json.Marshal(def)
	if err != nil {
		return canonicalWorkflow{}, nil, "", err
	}
	var frozen workflow.Definition
	if err := json.Unmarshal(raw, &frozen); err != nil {
		return canonicalWorkflow{}, nil, "", err
	}
	def = frozen
	def.Normalize()
	canon := canonicalWorkflow{Name: def.Name, Description: def.Description, Revision: w.Revision, Nodes: def.Nodes}
	b, err := json.MarshalIndent(canon, "", "  ")
	if err != nil {
		return canon, nil, "", err
	}
	sum := sha256.Sum256(b)
	return canon, b, hex.EncodeToString(sum[:])[:12], nil
}

// openPublished loads workflows/published.json with the registry's own
// guarantees: first boot seeds it, corrupt or foreign files refuse to boot.
func (s *workflowStore) openPublished(dir string) error {
	s.pubPath = filepath.Join(dir, "workflows", "published.json")
	s.versionsDir = filepath.Join(dir, "workflows", "versions")
	s.published = publishedData{Version: 1, Workflows: map[string]publishPointer{}}
	f, err := os.Open(s.pubPath)
	if errors.Is(err, os.ErrNotExist) {
		return s.savePublishedLocked(s.published)
	}
	if err != nil {
		return err
	}
	raw, readErr := io.ReadAll(io.LimitReader(f, (32<<20)+1))
	closeErr := f.Close()
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	if len(raw) > 32<<20 {
		return errors.New("workflow publish registry exceeds size limit")
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s.published); err != nil {
		return fmt.Errorf("workflow publish registry: %w", err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return errors.New("workflow publish registry has trailing data")
	}
	if s.published.Version != 1 || s.published.Workflows == nil {
		return errors.New("invalid workflow publish registry")
	}
	for id, ptr := range s.published.Workflows {
		if !validID(id) || ptr.VersionID == "" {
			return errors.New("invalid workflow publish pointer")
		}
	}
	return nil
}

// migratePublished gives every legacy ready workflow without a pointer its
// first snapshot, named "migrated" — the publish model replaces the ready
// gate, and a workflow that could run yesterday must still run today.
func (s *workflowStore) migratePublished() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, w := range s.data.Workflows {
		if w.Deleted || w.Status != "ready" {
			continue
		}
		if _, ok := s.published.Workflows[id]; ok {
			continue
		}
		if _, err := s.publishLocked(id, "migrated", ""); err != nil {
			return fmt.Errorf("workflow publish migration: %w", err)
		}
	}
	return nil
}

// savePublishedLocked is the stores' atomic-write pattern for the pointer
// file. Callers hold mu.
func (s *workflowStore) savePublishedLocked(next publishedData) error {
	b, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return err
	}
	return writeWorkflowFileAtomic(s.pubPath, b)
}

// writeWorkflowFileAtomic writes one publish-layer file: temp, 0600, rename.
func writeWorkflowFileAtomic(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".wfpub-*.tmp")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// published returns the pointer for one workflow.
func (s *workflowStore) pointer(id string) (publishPointer, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	ptr, ok := s.published.Workflows[id]
	return ptr, ok
}

// dirty reports whether the draft has moved past the published snapshot.
// No pointer is not dirty — it is simply unpublished, a different badge.
func (s *workflowStore) dirty(w Workflow) bool {
	ptr, ok := s.pointer(w.ID)
	if !ok {
		return false
	}
	_, _, hash, err := s.canonicalDraft(w)
	return err != nil || hash != ptr.VersionID
}

// publish freezes the current draft into a snapshot and moves the pointer.
// The shape validation is put's, but a publish additionally requires a
// runnable definition — an empty draft saves fine and publishes never.
var errWorkflowDraftChanged = errors.New("workflow draft changed after review")

func (s *workflowStore) publish(id, name, desc string) (publishPointer, error) {
	return s.publishExact(id, name, desc, 0, "")
}

// Exact publication binds review to one revision/hash. Legacy callers omit
// both expectations; they still validate and snapshot the same locked draft.
func (s *workflowStore) publishExact(id, name, desc string, expected int, expectedHash string) (publishPointer, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	w, ok := s.data.Workflows[id]
	if !ok || w.Deleted {
		return publishPointer{}, ErrNotFound
	}
	if expected != 0 && expected != w.Revision {
		return publishPointer{}, errWorkflowDraftChanged
	}
	_, _, hash, err := s.canonicalDraft(w)
	if err != nil {
		return publishPointer{}, err
	}
	if expectedHash != "" && expectedHash != hash {
		return publishPointer{}, errWorkflowDraftChanged
	}
	// Work on a private copy: normalization must not mutate a published record
	// or receipt sharing a slice with the registry.
	in := cloneAuthoringWorkflow(w).workflowInput
	if err := validateWorkflowInput(&in); err != nil {
		return publishPointer{}, err
	}
	if s.refs != nil {
		if err := s.refs(&in); err != nil {
			return publishPointer{}, err
		}
	}
	def := workflow.Definition{ID: w.ID, Name: in.Name, Revision: w.Revision, Nodes: in.Nodes}
	if err := def.Validate(); err != nil {
		return publishPointer{}, fmt.Errorf("无法发布：%w", err)
	}
	name, desc = strings.TrimSpace(name), strings.TrimSpace(desc)
	if utf8.RuneCountInString(name) > 60 || utf8.RuneCountInString(desc) > 240 {
		return publishPointer{}, errors.New("版本名称或描述超过长度限制")
	}
	return s.publishLocked(id, name, desc)
}

// publishLocked snapshots and points. Callers hold mu.
func (s *workflowStore) publishLocked(id, name, desc string) (publishPointer, error) {
	w, ok := s.data.Workflows[id]
	if !ok || w.Deleted {
		return publishPointer{}, ErrNotFound
	}
	canon, _, hash, err := s.canonicalDraft(w)
	if err != nil {
		return publishPointer{}, err
	}
	ptr := publishPointer{VersionID: hash, Name: name, Desc: desc, PublishedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	path := s.versionPath(id, hash)
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		env := workflowVersion{VersionID: hash, Name: name, Desc: desc, PublishedAt: ptr.PublishedAt, Workflow: canon}
		b, err := json.MarshalIndent(env, "", "  ")
		if err != nil {
			return publishPointer{}, err
		}
		if err := writeWorkflowFileAtomic(path, b); err != nil {
			return publishPointer{}, err
		}
	} else if err != nil {
		return publishPointer{}, err
	}
	next := publishedData{Version: 1, Workflows: maps.Clone(s.published.Workflows)}
	next.Workflows[id] = ptr
	if err := s.savePublishedLocked(next); err != nil {
		return publishPointer{}, err
	}
	s.published = next
	return ptr, nil
}

// rollback points at an older snapshot. The draft is untouched — pointer
// only, and safe precisely because snapshots are immutable.
func (s *workflowStore) rollback(id, versionID string) (publishPointer, error) {
	env, err := s.readVersion(id, versionID)
	if err != nil {
		return publishPointer{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ptr := publishPointer{VersionID: env.VersionID, Name: env.Name, Desc: env.Desc, PublishedAt: env.PublishedAt}
	next := publishedData{Version: 1, Workflows: maps.Clone(s.published.Workflows)}
	next.Workflows[id] = ptr
	if err := s.savePublishedLocked(next); err != nil {
		return publishPointer{}, err
	}
	s.published = next
	return ptr, nil
}

// readVersion loads one snapshot envelope, strictly.
func (s *workflowStore) readVersion(id, hash string) (workflowVersion, error) {
	var env workflowVersion
	if !validID(id) || !validID(hash) {
		return env, ErrNotFound
	}
	raw, err := os.ReadFile(s.versionPath(id, hash))
	if errors.Is(err, os.ErrNotExist) {
		return env, ErrNotFound
	}
	if err != nil {
		return env, err
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&env); err != nil {
		return env, fmt.Errorf("workflow version %s: %w", hash, err)
	}
	if env.VersionID != hash || len(env.Workflow.Nodes) == 0 {
		return env, fmt.Errorf("invalid workflow version %s", hash)
	}
	return env, nil
}

// versions lists one workflow's snapshots, newest first, the pointer's
// version marked current. Unreadable entries are skipped with a note — a
// listing must not die on one bad file.
func (s *workflowStore) versions(id string) ([]workflowVersionView, error) {
	entries, err := os.ReadDir(filepath.Join(s.versionsDir, id))
	if errors.Is(err, os.ErrNotExist) {
		return []workflowVersionView{}, nil
	}
	if err != nil {
		return nil, err
	}
	ptr, hasPtr := s.pointer(id)
	out := []workflowVersionView{}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		env, err := s.readVersion(id, strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			fmt.Fprintf(os.Stderr, "workflow %s version %s: %v\n", id, e.Name(), err)
			continue
		}
		out = append(out, workflowVersionView{
			VersionID: env.VersionID, Name: env.Name, Desc: env.Desc,
			PublishedAt: env.PublishedAt, Current: hasPtr && ptr.VersionID == env.VersionID,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].PublishedAt == out[j].PublishedAt {
			return out[i].VersionID < out[j].VersionID
		}
		return out[i].PublishedAt > out[j].PublishedAt
	})
	return out, nil
}

// publishedDefinition resolves the pointer to the snapshot a run starts
// from: the publish model's whole point — draft edits after the publish
// change nothing here.
func (s *workflowStore) publishedDefinition(id string) (workflow.Definition, error) {
	s.mu.RLock()
	ptr, ok := s.published.Workflows[id]
	s.mu.RUnlock()
	if !ok {
		return workflow.Definition{}, errWorkflowUnpublished
	}
	env, err := s.readVersion(id, ptr.VersionID)
	if err != nil {
		return workflow.Definition{}, fmt.Errorf("published snapshot unreadable: %w", err)
	}
	return workflow.Definition{
		ID: id, Name: env.Workflow.Name, Description: env.Workflow.Description,
		Revision: env.Workflow.Revision, Nodes: append([]workflow.Node(nil), env.Workflow.Nodes...),
	}, nil
}

// unpublishLocked drops the pointer (delete does this). The versions
// directory stays: snapshots are the audit trail of what actually ran.
// Callers hold mu.
func (s *workflowStore) unpublishLocked(id string) error {
	if _, ok := s.published.Workflows[id]; !ok {
		return nil
	}
	next := publishedData{Version: 1, Workflows: maps.Clone(s.published.Workflows)}
	delete(next.Workflows, id)
	if err := s.savePublishedLocked(next); err != nil {
		return err
	}
	s.published = next
	return nil
}
