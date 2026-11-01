package tools

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// This is a local observation store, not a cache of actions to replay. IDs are
// content-addressed within a host-selected scope. Loading an ID never executes
// the original tool, opens a model-selected filename or contacts an MCP server.
const maxStoredResultBytes = 80 << 20

type ResultReference struct {
	ID        string `json:"result_id"`
	Tool      string `json:"tool"`
	Kind      string `json:"kind"`
	TextBytes int64  `json:"text_bytes"`
	Complete  bool   `json:"text_complete"`
}

type StoredToolResult struct {
	Version      int             `json:"version"`
	ID           string          `json:"result_id,omitempty"`
	Tool         string          `json:"tool"`
	IsError      bool            `json:"is_error,omitempty"`
	Error        string          `json:"error,omitempty"`
	Text         string          `json:"text"`
	Structured   json.RawMessage `json:"structured,omitempty"`
	Native       json.RawMessage `json:"native,omitempty"`
	TextComplete bool            `json:"text_complete"`
}

type ResultStore struct {
	dir     string
	initErr error
}

func newResultStore(dir string) *ResultStore { return &ResultStore{dir: dir} }

// resultSpillDir scopes new host sessions without invalidating legacy paths in
// old transcripts. The caller supplies identity, never the model's arguments.
func resultSpillDir(base, scope string) string {
	if scope == "" {
		return base
	}
	sum := sha256.Sum256([]byte(scope))
	return filepath.Join(base, "scopes", hex.EncodeToString(sum[:]))
}

func newSessionResultStorage(base, scope string) (string, *ResultStore) {
	// Embedders without a durable session identity still get a private namespace
	// for this registry. Web and CLI pass their stable transcript identity so
	// resuming the session resolves the same result IDs after a process restart.
	if scope == "" {
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			// Fail closed, including legacy spill writers: a failed namespace
			// allocation must not fall back to the shared readable directory.
			return filepath.Join(os.DevNull, "pogo-results"), &ResultStore{initErr: errors.New("cannot allocate tool result scope")}
		}
		scope = "ephemeral:" + hex.EncodeToString(nonce[:])
	}
	dir := resultSpillDir(base, scope)
	return dir, newResultStore(filepath.Join(dir, "results"))
}

func validResultID(id string) bool {
	if len(id) != 65 || id[0] != 'R' {
		return false
	}
	for _, c := range id[1:] {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func (s *ResultStore) Save(record StoredToolResult) (*ResultReference, error) {
	if s == nil || s.initErr != nil || s.dir == "" {
		return nil, errors.New("tool result storage is unavailable")
	}
	if len(record.Text)+len(record.Structured)+len(record.Native) > maxStoredResultBytes {
		return nil, errors.New("tool result exceeds the storage limit")
	}
	record.Version, record.ID = 1, ""
	data, err := json.Marshal(record)
	if err != nil || len(data) > maxStoredResultBytes {
		return nil, errors.New("tool result could not be encoded within the storage limit")
	}
	sum := sha256.Sum256(data)
	id := "R" + hex.EncodeToString(sum[:])
	if err := os.MkdirAll(s.dir, 0700); err != nil {
		return nil, errors.New("tool result storage could not be created")
	}
	root, err := openResultStoreRoot(s.dir)
	if err != nil {
		return nil, errors.New("tool result storage could not be opened")
	}
	defer root.Close()
	unlock, err := lockResultLifecycle(context.Background(), root)
	if err != nil {
		return nil, errors.New("tool result lifecycle storage could not be locked")
	}
	defer unlock()
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, errors.New("tool result could not be saved")
	}
	tmp := ".partial-" + hex.EncodeToString(nonce[:])
	f, err := root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, errors.New("tool result could not be saved")
	}
	defer root.Remove(tmp)
	n, werr := f.Write(data)
	if werr == nil && n != len(data) {
		werr = io.ErrShortWrite
	}
	if werr == nil {
		werr = f.Sync()
	}
	cerr := f.Close()
	if werr != nil || cerr != nil {
		return nil, errors.New("tool result could not be saved completely")
	}
	if err := root.Rename(tmp, "result-"+id[1:]+".log"); err != nil {
		return nil, errors.New("tool result could not be committed")
	}
	// Lifecycle/catalog information is separate from the content-addressed record.
	// A failed sidecar write must not pretend that a recovery handle was saved
	// with the promised active-read protection.
	if err := noteResultSaved(context.Background(), root, id, record, time.Now()); err != nil {
		return nil, errors.New("tool result was committed but lifecycle metadata could not be saved")
	}
	return &ResultReference{ID: id, Tool: record.Tool, TextBytes: int64(len(record.Text)), Complete: record.TextComplete}, nil
}

func (s *ResultStore) Load(ctx context.Context, id string) (StoredToolResult, error) {
	var record StoredToolResult
	if !validResultID(id) {
		return record, errors.New("invalid result_id; use the exact R-prefixed ID supplied by a tool result")
	}
	if err := ctx.Err(); err != nil {
		return record, err
	}
	if s == nil || s.initErr != nil || s.dir == "" {
		return record, errors.New("tool result storage is unavailable")
	}
	root, err := openResultStoreRoot(s.dir)
	if err != nil {
		return record, errors.New("result is unavailable in this session or has expired; do not repeat the original action automatically")
	}
	defer root.Close()
	unlock, err := lockResultLifecycle(ctx, root)
	if err != nil {
		if ctx.Err() != nil {
			return record, ctx.Err()
		}
		return record, errors.New("tool result lifecycle storage could not be locked")
	}
	defer unlock()
	record, err = loadStoredResult(ctx, root, id)
	if err != nil {
		return record, err
	}
	if err := noteResultSaved(ctx, root, id, record, time.Now()); err != nil {
		if ctx.Err() != nil {
			return StoredToolResult{}, ctx.Err()
		}
		return StoredToolResult{}, errors.New("stored result was verified but its temporary read protection could not be saved")
	}
	return record, nil
}

// loadStoredResult is the integrity check shared by reads, catalog recovery and
// explicit retention. Its caller holds the lifecycle lock. It never renews a
// lease by itself, so listing a directory cannot keep every result alive.
func loadStoredResult(ctx context.Context, root *os.Root, id string) (StoredToolResult, error) {
	var record StoredToolResult
	name := "result-" + id[1:] + ".log"
	before, err := root.Lstat(name)
	if err != nil || !before.Mode().IsRegular() {
		return record, errors.New("result is unavailable in this session or has expired; do not repeat the original action automatically")
	}
	f, err := scopedOpen(root, name, false)
	if err != nil {
		return record, errors.New("result is unavailable in this session or has expired; do not repeat the original action automatically")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || !os.SameFile(before, info) || info.Size() > maxStoredResultBytes {
		return record, errors.New("stored tool result is not a bounded regular file")
	}
	data, err := readResultBytes(ctx, f, maxStoredResultBytes)
	if err != nil {
		return record, err
	}
	sum := sha256.Sum256(data)
	if "R"+hex.EncodeToString(sum[:]) != id || json.Unmarshal(data, &record) != nil || record.Version != 1 {
		return StoredToolResult{}, errors.New("stored tool result failed its integrity check")
	}
	if err := ctx.Err(); err != nil {
		return StoredToolResult{}, err
	}
	record.ID = id
	return record, nil
}

func readResultBytes(ctx context.Context, r io.Reader, limit int64) ([]byte, error) {
	var out strings.Builder
	buf := make([]byte, 32<<10)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n, err := r.Read(buf)
		if contextErr := ctx.Err(); contextErr != nil {
			return nil, contextErr
		}
		if int64(out.Len()+n) > limit {
			return nil, errors.New("tool result exceeds the read limit")
		}
		out.Write(buf[:n])
		if err == io.EOF {
			return []byte(out.String()), nil
		}
		if err != nil {
			return nil, errors.New("tool result could not be read completely")
		}
	}
}
