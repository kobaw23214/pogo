package skills

import (
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// The built-in skills, compiled into the binary. They are framework
// capabilities stated as skills — the drawing contract for the web UI is the
// first one — so that a deployment gets them without mounting anything, and a
// skill on disk with the same name still outranks them.
//
//go:embed all:builtin
var builtinFS embed.FS

// MaterializeEmbedded copies the built-in skills into dst and returns the
// per-skill directories, ready for Options.Embedded.
//
// Skills are files on disk everywhere else in the pipeline: the model reads the
// body with the read tool, /skill: re-reads it per invocation, and the
// directories become read roots. Materializing once per process keeps all of
// that working unchanged rather than teaching every consumer about embed.FS.
func MaterializeEmbedded(dst string) ([]string, error) {
	entries, err := fs.ReadDir(builtinFS, "builtin")
	if err != nil {
		return nil, err
	}

	var dirs []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		// embed.FS paths are always slash-separated; filepath.Join would emit
		// a backslash on Windows and WalkDir would not find the entry.
		root := path.Join("builtin", name)
		target := filepath.Join(dst, name)
		err := fs.WalkDir(builtinFS, root, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel := strings.TrimPrefix(p, root+"/")
			if rel == "." {
				return os.MkdirAll(target, 0o755)
			}
			out := filepath.Join(target, rel)
			if d.IsDir() {
				return os.MkdirAll(out, 0o755)
			}
			data, err := fs.ReadFile(builtinFS, p)
			if err != nil {
				return err
			}
			// A skill's scripts/ entries are invoked by absolute path through
			// bash, the mal-* convention: they need the execute bit, which
			// go:embed does not carry. Everything else stays 0644.
			perm := os.FileMode(0o644)
			if path.Base(path.Dir(p)) == "scripts" {
				perm = 0o755
			}
			// Skip the write when the bytes already match. That makes the copy
			// idempotent, and — the reason it matters more than tidiness — it
			// lets a root-administered shared directory (/opt/skills) be reused
			// read-only by every user: identical content needs no write, so an
			// unprivileged process succeeds against a location it could not
			// have created instead of falling back to a private copy.
			if fi, rerr := os.Stat(out); rerr == nil && fi.Mode().Perm() == perm {
				if existing, rerr := os.ReadFile(out); rerr == nil && bytes.Equal(existing, data) {
					return nil
				}
			}
			// Temp-then-rename so a concurrently starting process never reads a
			// half-written file; the bytes are identical, so last writer wins is
			// not a race that matters.
			tmp := out + ".tmp"
			if err := os.WriteFile(tmp, data, perm); err != nil {
				return err
			}
			return os.Rename(tmp, out)
		})
		if err != nil {
			return nil, fmt.Errorf("materialize %s: %w", name, err)
		}
		dirs = append(dirs, target)
	}
	return dirs, nil
}
