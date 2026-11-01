package tools

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"syscall"

	"github.com/yosukeno/pogo/diff"
)

// These tools retain the ordinary schemas and UI payloads, but never execute
// the base implementation's path-based I/O. The caller owns the scope roots
// and their parents; commands may mutate descendants, never those parents.
// Root handles confine every descendant lookup, including concurrent symlink
// replacement by a terminal. Base Roots, WriteRoots, spill paths and Journal
// are deliberately not inherited. Journal needs a handle-based API first.
type rootScopedFileTool struct {
	Tool
	spec               commandScopeSpec
	err                error
	protectionManifest string
}

const maxScopedFileBytes = 8 << 20

func scopedFileTool(base Tool, scope *CommandScope) Tool {
	if base == nil || scope == nil {
		return base
	}
	switch base.Name() {
	case "read", "ls", "find", "grep", "write", "edit":
	default:
		return base
	}
	spec, err := normalizedCommandScope(scope, "")
	if runtime.GOOS == "js" || runtime.GOOS == "plan9" {
		err = errors.New("this platform cannot anchor scoped file operations to directory handles")
	}
	manifest := scope.ProtectionManifest
	if manifest == "" {
		switch typed := base.(type) {
		case *Write:
			manifest = typed.ProtectionManifest
		case *Edit:
			manifest = typed.ProtectionManifest
		}
	}
	return &rootScopedFileTool{Tool: base, spec: spec, err: err, protectionManifest: manifest}
}

func (t *rootScopedFileTool) Schema() map[string]any {
	if provider, ok := t.Tool.(SchemaProvider); ok {
		return provider.Schema()
	}
	return t.Tool.InputSchema()
}

func (t *rootScopedFileTool) ValidateArgs(raw json.RawMessage) error {
	return ValidateArgs(t.Tool, raw)
}

func (t *rootScopedFileTool) Execute(ctx context.Context, raw json.RawMessage) (Result, error) {
	if t.err != nil {
		return Result{}, t.err
	}
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if err := t.ValidateArgs(raw); err != nil {
		return Result{}, err
	}
	switch t.Name() {
	case "read":
		return t.read(ctx, raw)
	case "ls":
		return t.list(ctx, raw)
	case "find", "grep":
		return t.search(ctx, raw)
	case "write", "edit":
		return t.mutate(ctx, raw)
	}
	return Result{}, errors.New("unsupported scoped file tool")
}

type scopedFilePath struct {
	root, rel string
}

// Selection is lexical only. All filesystem resolution happens in os.Root,
// never by validating a pathname and subsequently opening it through os.Open.
func (t *rootScopedFileTool) selectPath(requested string, write bool) (scopedFilePath, error) {
	if requested == "" || len(requested) > 4096 || strings.ContainsRune(requested, 0) {
		return scopedFilePath{}, errors.New("a valid file path is required")
	}
	abs := requested
	if !filepath.IsAbs(requested) {
		if !filepath.IsLocal(requested) {
			return scopedFilePath{}, errors.New("relative path must stay inside the session workspace")
		}
		abs = filepath.Join(t.spec.cwd, requested)
	}
	abs = filepath.Clean(abs)
	for index, root := range append([]string{t.spec.cwd}, t.spec.readRoots...) {
		if !scopeWithin(root, abs) {
			continue
		}
		if write && index != 0 {
			return scopedFilePath{}, errors.New("shared skill directories are read-only")
		}
		rel, err := filepath.Rel(root, abs)
		if err != nil || (rel != "." && !filepath.IsLocal(rel)) {
			return scopedFilePath{}, errors.New("invalid scoped path")
		}
		return scopedFilePath{root: root, rel: rel}, nil
	}
	return scopedFilePath{}, errors.New("path is outside this session and its explicit read-only skills")
}

func scopedOpen(root *os.Root, rel string, directory bool) (*os.File, error) {
	// Nonblocking open prevents a file swapped to a FIFO from hanging before
	// the descriptor's own Stat can reject it. Never stat a pathname then rely
	// on that result to establish the type of a subsequently opened descriptor.
	f, err := root.OpenFile(rel, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil || (directory && !info.IsDir()) || (!directory && !info.Mode().IsRegular()) {
		f.Close()
		return nil, errors.New("scoped file operation requires a regular file or the requested directory")
	}
	return f, nil
}

func scopedReadBytes(ctx context.Context, root *os.Root, rel string) ([]byte, os.FileMode, error) {
	f, err := scopedOpen(root, rel, false)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, 0, err
	}
	data, err := io.ReadAll(io.LimitReader(&scopeContextReader{ctx: ctx, reader: f}, maxScopedFileBytes+1))
	if err != nil {
		return nil, 0, err
	}
	if len(data) > maxScopedFileBytes {
		return nil, 0, errors.New("file exceeds the scoped file tool's 8 MiB limit; inspect it with scoped bash")
	}
	return data, info.Mode().Perm(), nil
}

type scopeContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *scopeContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

func (t *rootScopedFileTool) readPath(ctx context.Context, requested string) ([]byte, error) {
	p, err := t.selectPath(requested, false)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(p.root)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	data, _, err := scopedReadBytes(ctx, root, p.rel)
	return data, err
}

func (t *rootScopedFileTool) read(ctx context.Context, raw json.RawMessage) (Result, error) {
	var args readArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return Result{}, err
	}
	targets, err := args.targets()
	if err != nil {
		return Result{}, err
	}
	if len(targets) > 64 {
		return Result{}, errors.New("at most 64 files may be read in one scoped call")
	}
	if len(targets) > 1 {
		var out strings.Builder
		details := ReadManyDetails{}
		failures := 0
		for i, requested := range targets {
			if err := ctx.Err(); err != nil {
				return Result{}, err
			}
			if i != 0 {
				out.WriteString("\n\n")
			}
			fmt.Fprintf(&out, "==> %s <==\n", requested)
			fd := ReadFileDetails{Path: requested}
			data, err := t.readPath(ctx, requested)
			if err != nil {
				fd.Error = err.Error()
				failures++
				fmt.Fprintf(&out, "[error: %s]", err)
			} else {
				tr := TruncateHeadLimit(string(data), max(1, MaxLines/len(targets)), max(1, MaxBytes/len(targets)))
				fd.TotalLines, fd.ShownLines, fd.Truncated, fd.TruncatedBy = tr.TotalLines, tr.OutputLines, tr.Truncated, tr.By
				fd.BodyOffset = out.Len()
				out.WriteString(tr.Content)
				fd.BodyLength = out.Len() - fd.BodyOffset
				if tr.Truncated {
					fmt.Fprintf(&out, "\n[Truncated; continue with path=%s offset=%d.]", requested, tr.OutputLines+1)
				}
			}
			details.Files = append(details.Files, fd)
		}
		if failures == len(targets) {
			return Result{Details: details}, fmt.Errorf("none of the requested files could be read:\n%s", out.String())
		}
		return Result{Text: out.String(), Details: details}, nil
	}
	args.Path = targets[0]
	data, err := t.readPath(ctx, args.Path)
	if err != nil {
		return Result{}, err
	}
	lines := strings.Split(string(data), "\n")
	start := 0
	if args.Offset > 0 {
		start = args.Offset - 1
	}
	if start >= len(lines) {
		return Result{}, fmt.Errorf("offset %d is beyond end of file (%d lines total)", args.Offset, len(lines))
	}
	end := len(lines)
	if args.Limit > 0 && args.Limit < end-start {
		end = start + args.Limit
	}
	tr := TruncateHead(strings.Join(lines[start:end], "\n"))
	text := tr.Content
	if tr.Truncated || end < len(lines) {
		text += fmt.Sprintf("\n\n[Showing lines %d-%d of %d. Use offset=%d to continue.]", start+1, start+tr.OutputLines, len(lines), start+tr.OutputLines+1)
	}
	return Result{Text: text, Details: ReadDetails{Path: args.Path, Offset: args.Offset, Limit: args.Limit,
		TotalLines: len(lines), ShownLines: tr.OutputLines, FirstLine: start + 1, Truncated: tr.Truncated, TruncatedBy: tr.By}}, nil
}

func scopedDirectoryEntries(ctx context.Context, root *os.Root, rel string) ([]os.DirEntry, bool, error) {
	f, err := scopedOpen(root, rel, true)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	entries, err := f.ReadDir(maxWalkEntries + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, false, err
	}
	cut := len(entries) > maxWalkEntries
	if cut {
		entries = entries[:maxWalkEntries]
	}
	sort.Slice(entries, func(i, j int) bool {
		a, b := strings.ToLower(entries[i].Name()), strings.ToLower(entries[j].Name())
		if a == b {
			return entries[i].Name() < entries[j].Name()
		}
		return a < b
	})
	return entries, cut, nil
}

func (t *rootScopedFileTool) list(ctx context.Context, raw json.RawMessage) (Result, error) {
	var args lsArgs
	if err := json.Unmarshal(raw, &args); err != nil {
		return Result{}, err
	}
	if args.Path == "" {
		args.Path = "."
	}
	p, err := t.selectPath(args.Path, false)
	if err != nil {
		return Result{}, err
	}
	root, err := os.OpenRoot(p.root)
	if err != nil {
		return Result{}, err
	}
	defer root.Close()
	entries, cut, err := scopedDirectoryEntries(ctx, root, p.rel)
	if err != nil {
		return Result{}, err
	}
	limit := args.Limit
	if limit <= 0 {
		limit = DefaultLsLimit
	}
	limit = min(limit, maxWalkEntries)
	details := LsDetails{Path: args.Path, Total: len(entries), EntryLimited: cut || len(entries) > limit}
	var names []string
	for _, entry := range entries[:min(limit, len(entries))] {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		name := entry.Name()
		if entry.IsDir() {
			name += "/"
			details.Dirs++
		} else {
			details.Files++
		}
		names = append(names, name)
	}
	if len(names) == 0 {
		return Result{Text: "(empty directory)", Details: details}, nil
	}
	tr := TruncateHead(strings.Join(names, "\n"))
	details.Entries, details.Truncated = tr.OutputLines, tr.Truncated || details.EntryLimited
	text := tr.Content
	if details.Truncated {
		text += "\n\n[Listing truncated; narrow the directory or increase limit.]"
	}
	return Result{Text: text, Details: details}, nil
}

// Each lookup remains relative to the retained root handle. Directory symlinks
// are skipped, and even a directory replaced after Lstat cannot escape Root.
func scopedWalk(ctx context.Context, root *os.Root, rel string, visit func(string, fs.FileInfo) (bool, error)) (int, bool, error) {
	stack := []string{rel}
	visited, cut := 0, false
	for len(stack) > 0 {
		if err := ctx.Err(); err != nil {
			return visited, cut, err
		}
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		info, err := root.Lstat(current)
		if err != nil {
			return visited, cut, err
		}
		visited++
		if visited > maxWalkEntries {
			return visited - 1, true, nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			if current == rel {
				return visited, cut, errors.New("search root must not be a symbolic link")
			}
			continue
		}
		if info.IsDir() {
			if current != rel && prunedDirs[info.Name()] {
				continue
			}
			entries, limited, err := scopedDirectoryEntries(ctx, root, current)
			if err != nil {
				return visited, cut, err
			}
			cut = cut || limited
			if len(stack)+len(entries) > maxWalkEntries {
				return visited, true, nil
			}
			for i := len(entries) - 1; i >= 0; i-- {
				if entries[i].Type()&os.ModeSymlink == 0 {
					stack = append(stack, filepath.Join(current, entries[i].Name()))
				}
			}
			continue
		}
		if !info.Mode().IsRegular() {
			continue
		}
		keep, err := visit(current, info)
		if err != nil || !keep {
			return visited, cut, err
		}
	}
	return visited, cut, nil
}

func (t *rootScopedFileTool) search(ctx context.Context, raw json.RawMessage) (Result, error) {
	var args grepArgs // find shares pattern/path/limit and ignores the absent include
	if err := json.Unmarshal(raw, &args); err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(args.Pattern) == "" {
		return Result{}, errors.New("pattern must not be empty")
	}
	isGrep := t.Name() == "grep"
	var expression *regexp.Regexp
	var err error
	if isGrep {
		expression, err = regexp.Compile(args.Pattern)
	} else {
		_, err = path.Match(filepath.ToSlash(args.Pattern), "probe")
	}
	if err != nil {
		return Result{}, fmt.Errorf("invalid pattern: %w", err)
	}
	if args.Include != "" {
		if _, err := path.Match(filepath.ToSlash(args.Include), "probe"); err != nil {
			return Result{}, err
		}
	}
	requested := args.Path
	if requested == "" {
		requested = "."
	}
	p, err := t.selectPath(requested, false)
	if err != nil {
		return Result{}, err
	}
	root, err := os.OpenRoot(p.root)
	if err != nil {
		return Result{}, err
	}
	defer root.Close()
	limit := args.Limit
	if limit <= 0 {
		if isGrep {
			limit = DefaultGrepLimit
		} else {
			limit = DefaultFindLimit
		}
	}
	limit = min(limit, maxWalkEntries)
	var hits []string
	files, binaryFiles := 0, 0
	limited := false
	visited, cut, err := scopedWalk(ctx, root, p.rel, func(rel string, info fs.FileInfo) (bool, error) {
		shown := rel
		if p.root != t.spec.cwd {
			shown = filepath.Join(p.root, rel)
		}
		if !isGrep {
			subject := info.Name()
			if strings.Contains(filepath.ToSlash(args.Pattern), "/") {
				subject, _ = filepath.Rel(p.rel, rel)
			}
			if matched, _ := path.Match(filepath.ToSlash(args.Pattern), filepath.ToSlash(subject)); matched {
				hits = append(hits, shown)
			}
		} else {
			if args.Include != "" {
				if matched, _ := path.Match(filepath.ToSlash(args.Include), info.Name()); !matched {
					return true, nil
				}
			}
			if info.Size() > maxGrepFileBytes {
				return true, nil
			}
			data, _, err := scopedReadBytes(ctx, root, rel)
			if err != nil {
				return false, err
			}
			if bytes.IndexByte(data[:min(1024, len(data))], 0) >= 0 {
				binaryFiles++
				return true, nil
			}
			scanner := bufio.NewScanner(bytes.NewReader(data))
			scanner.Buffer(make([]byte, 4096), maxGrepFileBytes+1)
			matchedFile := false
			for line := 1; scanner.Scan(); line++ {
				if err := ctx.Err(); err != nil {
					return false, err
				}
				if expression.MatchString(scanner.Text()) {
					matchedFile = true
					hits = append(hits, fmt.Sprintf("%s:%d:%s", shown, line, clipLine(scanner.Text())))
					if len(hits) >= limit {
						break
					}
				}
			}
			if err := scanner.Err(); err != nil {
				return false, err
			}
			if matchedFile {
				files++
			}
		}
		limited = len(hits) >= limit
		return !limited, nil
	})
	if err != nil {
		return Result{}, err
	}
	if !isGrep {
		sort.Strings(hits)
	}
	tr := TruncateHead(strings.Join(hits, "\n"))
	text := tr.Content
	if len(hits) == 0 {
		text = fmt.Sprintf("No matches for %q (searched %d entries)", args.Pattern, visited)
	}
	if notes := searchNotes(limited, cut, tr.Truncated, limit, "matches"); notes != "" {
		text += "\n\n" + notes
	}
	if isGrep {
		return Result{Text: text, Details: GrepDetails{Pattern: args.Pattern, Path: args.Path, Include: args.Include,
			Matches: len(hits), Files: files, Scanned: visited, SkippedBinary: binaryFiles, Truncated: limited || cut || tr.Truncated, LimitHit: limited}}, nil
	}
	return Result{Text: text, Details: FindDetails{Pattern: args.Pattern, Path: args.Path, Matches: len(hits), Scanned: visited,
		Truncated: limited || cut || tr.Truncated, LimitHit: limited}}, nil
}

func (t *rootScopedFileTool) mutate(ctx context.Context, raw json.RawMessage) (Result, error) {
	var write writeArgs
	var edit editArgs
	isEdit := t.Name() == "edit"
	if isEdit {
		if err := json.Unmarshal(raw, &edit); err != nil {
			return Result{}, err
		}
		if edit.OldText != nil && edit.NewText != nil {
			edit.Edits = append(edit.Edits, editOp{OldText: *edit.OldText, NewText: *edit.NewText})
		}
		write.Path = edit.Path
	} else if err := json.Unmarshal(raw, &write); err != nil {
		return Result{}, err
	}
	if len(write.Content) > maxScopedFileBytes {
		return Result{}, errors.New("scoped write exceeds 8 MiB")
	}
	p, err := t.selectPath(write.Path, true)
	if err != nil {
		return Result{}, err
	}
	if p.rel == "." {
		return Result{}, errors.New("file path must not be the workspace itself")
	}
	// Use a lexical lock key: withFileLock calls canonical(), which would put
	// filesystem pathname resolution outside Root back into this boundary.
	key := filepath.Join(p.root, p.rel)
	lock := fileMutations.acquire(key)
	defer fileMutations.release(key, lock)
	if t.spec.readOnlyWorkspace {
		return Result{}, errors.New("this workspace is read-only")
	}
	if err := checkManagedDocumentMutation(key, t.protectionManifest); err != nil {
		return Result{}, err
	}
	root, err := os.OpenRoot(p.root)
	if err != nil {
		return Result{}, err
	}
	defer root.Close()
	parentName := filepath.Dir(p.rel)
	if !isEdit {
		if err := root.MkdirAll(parentName, 0700); err != nil {
			return Result{}, err
		}
	}
	parent, err := root.OpenRoot(parentName)
	if err != nil {
		return Result{}, err
	}
	defer parent.Close()
	name := filepath.Base(p.rel)
	info, err := parent.Lstat(name)
	created := errors.Is(err, os.ErrNotExist)
	if err != nil && !created {
		return Result{}, err
	}
	if !created && !info.Mode().IsRegular() {
		return Result{}, errors.New("write/edit target must be a regular file, not a symbolic link or special file")
	}
	if isEdit && created {
		return Result{}, os.ErrNotExist
	}
	var previous []byte
	mode := os.FileMode(0600)
	if !created {
		previous, mode, err = scopedReadBytes(ctx, parent, name)
		if err != nil {
			return Result{}, err
		}
	}
	content := write.Content
	var details any
	var text string
	if isEdit {
		var result EditDetails
		content, result, err = scopedReplace(string(previous), edit)
		if err != nil {
			return Result{}, err
		}
		details = result
		text = fmt.Sprintf("Successfully replaced %d block(s) in %s", len(edit.Edits), edit.Path)
	} else {
		old := string(previous)
		body, _ := diff.Display(old, content, diff.DefaultContext)
		display, patch, tooBig := capDetailsDiff(body, diff.Unified(write.Path, old, content, diff.DefaultContext))
		added, removed := diff.Stat(old, content)
		details = WriteDetails{Path: write.Path, Bytes: len(content), Created: created, Diff: display, Patch: patch, TooBig: tooBig, Added: added, Removed: removed}
		text = fmt.Sprintf("Successfully wrote %d bytes to %s", len(content), write.Path)
	}
	if len(content) > maxScopedFileBytes {
		return Result{}, errors.New("scoped replacement exceeds 8 MiB")
	}
	if err := checkManagedDocumentMutation(key, t.protectionManifest); err != nil {
		return Result{}, err
	}
	if err := scopedAtomicWrite(ctx, parent, name, []byte(content), mode); err != nil {
		return Result{}, err
	}
	return Result{Text: text, Details: details}, nil
}

func scopedAtomicWrite(ctx context.Context, parent *os.Root, name string, data []byte, mode os.FileMode) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	temporary := ".pogo-scoped-" + rand.Text()
	f, err := parent.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode.Perm())
	if err != nil {
		return err
	}
	defer parent.Remove(temporary)
	_, writeErr := f.Write(data)
	closeErr := f.Close()
	if err := errors.Join(writeErr, closeErr, ctx.Err()); err != nil {
		return err
	}
	// Rename replaces a late symlink instead of following it, and the parent
	// handle remains anchored even if a terminal renames that directory.
	return parent.Rename(temporary, name)
}

// Matching is pure text manipulation; preserve the original edit tool's BOM,
// CRLF, unique-match and non-overlap rules without its pathname-based writes.
func scopedReplace(original string, args editArgs) (string, EditDetails, error) {
	bom := ""
	if strings.HasPrefix(original, "\ufeff") {
		bom, original = "\ufeff", strings.TrimPrefix(original, "\ufeff")
	}
	crlf := strings.Contains(original, "\r\n")
	content := strings.ReplaceAll(original, "\r\n", "\n")
	type replacement struct {
		start, end int
		text       string
	}
	var spans []replacement
	for i, op := range args.Edits {
		old := strings.ReplaceAll(op.OldText, "\r\n", "\n")
		if old == "" {
			return "", EditDetails{}, fmt.Errorf("edits[%d].oldText must not be empty", i)
		}
		count := strings.Count(content, old)
		if count != 1 {
			return "", EditDetails{}, fmt.Errorf("edits[%d].oldText matches %d places; read the file and provide a unique match", i, count)
		}
		start := strings.Index(content, old)
		spans = append(spans, replacement{start, start + len(old), strings.ReplaceAll(op.NewText, "\r\n", "\n")})
	}
	if len(spans) == 0 {
		return "", EditDetails{}, errors.New("edits must contain at least one replacement")
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].start < spans[j].start })
	for i := 1; i < len(spans); i++ {
		if spans[i].start < spans[i-1].end {
			return "", EditDetails{}, errors.New("edits overlap; merge them into one replacement")
		}
	}
	out := content
	for i := len(spans) - 1; i >= 0; i-- {
		out = out[:spans[i].start] + spans[i].text + out[spans[i].end:]
	}
	body, first := diff.Display(content, out, diff.DefaultContext)
	display, patch, tooBig := capDetailsDiff(body, diff.Unified(args.Path, content, out, diff.DefaultContext))
	added, removed := diff.Stat(content, out)
	details := EditDetails{Path: args.Path, Edits: len(spans), Diff: display, Patch: patch, FirstChangedLine: first, Added: added, Removed: removed, TooBig: tooBig}
	if crlf {
		out = strings.ReplaceAll(out, "\n", "\r\n")
	}
	return bom + out, details, nil
}
