package tools

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

// CommandScope is a trusted caller's per-session OS boundary. It must never be
// populated from model arguments. ReadRoots lists individual, operator-managed
// built-in skill directories, not arbitrary filesystem roots or user manifests.
// Network is opt-in. Interactive is only for a dedicated PTY owned by this
// session; it preserves that PTY's controlling session instead of detaching it.
type CommandScope struct {
	Cwd         string
	ReadRoots   []string
	Network     bool
	Interactive bool
	// ReadOnlyRoots are trusted data directories exposed without write access.
	// Unlike ReadRoots they are not skill bundles. A transform may expose the
	// source workspace read-only while its Cwd is a writable staging directory.
	ReadOnlyRoots []string
	// ReadOnlyWorkspace is used by explicitly read-only command executors.
	ReadOnlyWorkspace bool
	// ProtectedFiles are existing managed document paths inside Cwd. Their
	// ancestor directories are pinned before each file is mounted read-only, so
	// a command cannot replace the original path by renaming its parent.
	ProtectedFiles []string
	// ProtectionManifest is a host-owned JSON manifest. Its private host roots
	// are mounted read-only even in legacy workspaces containing session state.
	// It is read
	// afresh for each command and may be inherited by child agent processes;
	// model arguments must never select or replace it.
	ProtectionManifest string
	// ProtectedFilesFunc supplies live host registry facts without scanning the
	// workspace. It supplements, rather than replaces, manifest protection.
	ProtectedFilesFunc func() ([]string, error)
}

// EnvCommandProtectionManifest is set only by a trusted Web host when spawning
// its agent tree. It is not a grant: it adds read-only mounts to CommandScope.
const EnvCommandProtectionManifest = "POGO_COMMAND_PROTECTION_MANIFEST"

// CommandProtectionManifest is an atomic host-owned snapshot of managed paths.
// An empty paths array is valid; a missing or malformed manifest fails closed.
type CommandProtectionManifest struct {
	Schema       int      `json:"schema"`
	Paths        []string `json:"paths"`
	PrivateRoots []string `json:"private_roots,omitempty"`
}

var ErrScopedCommandUnavailable = errors.New("per-session command isolation requires Linux and working bubblewrap with private PID/proc namespaces")

type commandScopeSpec struct {
	cwd, workdir      string
	readRoots         []string
	readOnlyRoots     []string
	protectedFiles    []string
	protectedParents  []string
	privateRoots      []string
	readOnlyWorkspace bool
	network           bool
	interactive       bool
	env               []string
}

type scopeBind struct{ source, target string }

const scopedCommandPath = "/opt/venv/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"

func scopeWithin(root, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// Reject aliases instead of silently granting a different host directory. The
// sandbox mount tree then protects later symlinks created *inside* the workspace:
// their outside targets simply do not exist in that namespace.
func scopedCanonicalDir(value string) (string, error) {
	if value == "" || value != strings.TrimSpace(value) || !filepath.IsAbs(value) || strings.ContainsRune(value, 0) {
		return "", errors.New("command scope requires an absolute directory")
	}
	abs := filepath.Clean(value)
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", errors.New("command scope directory is unavailable")
	}
	if filepath.Clean(real) != abs {
		return "", errors.New("command scope directory must not traverse a symlink")
	}
	info, err := os.Stat(real)
	if err != nil || !info.IsDir() {
		return "", errors.New("command scope target must be a directory")
	}
	if filepath.Dir(real) == real {
		return "", errors.New("filesystem root cannot be a command scope")
	}
	return real, nil
}

func scopedReservedRoot(value string) bool {
	// These are Linux namespace destinations, so POSIX semantics are deliberate.
	p := filepath.ToSlash(value)
	for _, root := range []string{"/usr", "/bin", "/sbin", "/lib", "/lib64", "/etc", "/proc", "/dev", "/sys", "/opt/venv"} {
		if p == root || strings.HasPrefix(p, root+"/") {
			return true
		}
	}
	return p == "/home" || p == "/root" || p == "/tmp" || p == "/run" || p == "/var" || p == "/opt"
}

func normalizedCommandScope(scope *CommandScope, workdir string) (commandScopeSpec, error) {
	var out commandScopeSpec
	if scope == nil {
		return out, errors.New("command scope is required")
	}
	root, err := scopedCanonicalDir(scope.Cwd)
	if err != nil {
		return out, err
	}
	if scopedReservedRoot(root) {
		return out, errors.New("runtime or system directory cannot be a session workspace")
	}
	if home, err := os.UserHomeDir(); err == nil && filepath.Clean(home) == root {
		return out, errors.New("host home cannot be a session workspace")
	}
	dir := workdir
	if dir == "" {
		dir = root
	} else if !filepath.IsAbs(dir) {
		dir = filepath.Join(root, dir)
	}
	if !scopeWithin(root, filepath.Clean(dir)) {
		return out, errors.New("command workdir is outside the session workspace")
	}
	dir, err = scopedCanonicalDir(dir)
	if err != nil || !scopeWithin(root, dir) {
		return out, errors.New("command workdir must be a real directory inside the session workspace")
	}
	out = commandScopeSpec{cwd: root, workdir: dir, network: scope.Network, interactive: scope.Interactive}
	out.readOnlyWorkspace = scope.ReadOnlyWorkspace
	for _, given := range scope.ReadOnlyRoots {
		shared, err := scopedCanonicalDir(given)
		if err != nil || scopedReservedRoot(shared) {
			return out, errors.New("read-only data root must be a canonical data directory")
		}
		out.readOnlyRoots = append(out.readOnlyRoots, shared)
	}
	protected := append([]string(nil), scope.ProtectedFiles...)
	if scope.ProtectionManifest != "" {
		manifest, err := readCommandProtectionManifest(scope.ProtectionManifest)
		if err != nil {
			return out, err
		}
		protected = append(protected, manifest.Paths...)
		out.privateRoots, err = commandProtectionPrivateRoots(scope.ProtectionManifest, manifest)
		if err != nil {
			return out, err
		}
	}
	if scope.ProtectedFilesFunc != nil {
		paths, err := scope.ProtectedFilesFunc()
		if err != nil {
			return out, errors.New("managed document protection is temporarily unavailable")
		}
		protected = append(protected, paths...)
	}
	if err := normalizeProtectedFiles(&out, protected); err != nil {
		return out, err
	}
	if len(scope.ReadRoots) > 64 {
		return out, errors.New("too many shared built-in skill directories")
	}
	for _, given := range scope.ReadRoots {
		shared, err := scopedCanonicalDir(given)
		if err != nil {
			return out, errors.New("shared built-in skill directory must be canonical")
		}
		for _, component := range strings.Split(filepath.ToSlash(shared), "/") {
			if component == ".pogo" || component == "transcripts" || component == "workspaces" {
				return out, errors.New("private agent state cannot be a shared skill directory")
			}
		}
		if scopedReservedRoot(shared) || scopeWithin(shared, root) || scopeWithin(root, shared) {
			return out, errors.New("shared skill directory overlaps a private or system root")
		}
		marker, err := os.Lstat(filepath.Join(shared, "SKILL.md"))
		if err != nil || !marker.Mode().IsRegular() {
			return out, errors.New("shared read root must be an individual built-in skill directory")
		}
		for _, previous := range out.readRoots {
			if scopeWithin(previous, shared) || scopeWithin(shared, previous) {
				return out, errors.New("shared skill directories must not overlap")
			}
		}
		out.readRoots = append(out.readRoots, shared)
	}
	// Private roots add write protection to an existing grant; they are not
	// new read grants. In particular a global SessionDir from a manifest must
	// never expose other conversations to a private CommandScope.
	out.privateRoots = scopedVisiblePrivateRoots(out)
	parentSet := map[string]bool{}
	for _, parent := range out.protectedParents {
		parentSet[parent] = true
	}
	for _, private := range out.privateRoots {
		if scopeWithin(root, private) {
			for parent := filepath.Dir(private); parent != root && scopeWithin(root, parent); parent = filepath.Dir(parent) {
				parentSet[parent] = true
			}
		}
	}
	out.protectedParents = nil
	for parent := range parentSet {
		out.protectedParents = append(out.protectedParents, parent)
	}
	sortProtectionParents(out.protectedParents)
	return out, nil
}

func scopedVisiblePrivateRoots(spec commandScopeSpec) []string {
	visible := append(append([]string{spec.cwd}, spec.readRoots...), spec.readOnlyRoots...)
	seen := map[string]bool{}
	var roots []string
	for _, private := range spec.privateRoots {
		for _, granted := range visible {
			intersection := ""
			if scopeWithin(granted, private) {
				intersection = private
			} else if scopeWithin(private, granted) {
				// Only the authorized descendant is visible. Mounting its
				// global private ancestor would widen the session's reads.
				intersection = granted
			}
			if intersection != "" && !seen[intersection] {
				seen[intersection] = true
				roots = append(roots, intersection)
			}
		}
	}
	sortProtectionParents(roots)
	return roots
}

func readCommandProtectionManifest(filename string) (CommandProtectionManifest, error) {
	var manifest CommandProtectionManifest
	if _, blocked := invalidCommandProtectionManifests.Load(filepath.Clean(filename)); blocked {
		return manifest, errors.New("managed document protection is unavailable; retry document registration before executing tools")
	}
	if !filepath.IsAbs(filename) {
		return manifest, errors.New("managed document manifest must be an absolute host path")
	}
	real, err := filepath.EvalSymlinks(filename)
	if err != nil || filepath.Clean(real) != filepath.Clean(filename) {
		return manifest, errors.New("managed document manifest is unavailable")
	}
	f, err := os.Open(real)
	if err != nil {
		return manifest, errors.New("managed document manifest is unavailable")
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return manifest, errors.New("managed document manifest is invalid")
	}
	d := json.NewDecoder(io.LimitReader(f, (1<<20)+1))
	d.DisallowUnknownFields()
	if d.Decode(&manifest) != nil || manifest.Schema != 1 || d.Decode(new(any)) != io.EOF {
		return manifest, errors.New("managed document manifest is invalid")
	}
	return manifest, nil
}

func commandProtectionPrivateRoots(filename string, manifest CommandProtectionManifest) ([]string, error) {
	seen := map[string]bool{}
	var roots []string
	for _, given := range append(append([]string(nil), manifest.PrivateRoots...), filepath.Dir(filename)) {
		root, err := scopedCanonicalDir(given)
		if err != nil || scopedReservedRoot(root) {
			return nil, errors.New("managed document private root is unavailable or invalid")
		}
		if !seen[root] {
			seen[root] = true
			roots = append(roots, root)
		}
	}
	sortProtectionParents(roots)
	return roots, nil
}

func normalizeProtectedFiles(spec *commandScopeSpec, paths []string) error {
	seen, parents := map[string]bool{}, map[string]bool{}
	for _, given := range paths {
		if !filepath.IsAbs(given) || strings.ContainsRune(given, 0) {
			return errors.New("managed document path is invalid")
		}
		file := filepath.Clean(given)
		// A shared owner manifest may also contain a sibling agent workspace.
		// Such paths are already absent from this namespace and add no grant.
		if !scopeWithin(spec.cwd, file) || seen[file] {
			continue
		}
		real, err := filepath.EvalSymlinks(file)
		if errors.Is(err, os.ErrNotExist) {
			// Keep the host registration, but there is no inode to mount in
			// this command. A later command protects it if it reappears. File
			// tools still reject direct writes by its registered pathname.
			continue
		}
		if err != nil || filepath.Clean(real) != file {
			return errors.New("managed document path is unavailable or aliased")
		}
		info, err := os.Lstat(file)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !info.Mode().IsRegular() {
			return errors.New("managed document must be a regular file")
		}
		// An existing hard link would expose the same inode through an
		// unprotected name. Do not pretend a path mount protects that alias.
		stat := reflect.Indirect(reflect.ValueOf(info.Sys()))
		if stat.IsValid() && stat.Kind() == reflect.Struct {
			links := stat.FieldByName("Nlink")
			if links.IsValid() && links.CanUint() && links.Uint() > 1 {
				return errors.New("managed document has hard-link aliases; use docs_office transform on a standalone document instead of overwriting the original")
			}
		}
		seen[file] = true
		spec.protectedFiles = append(spec.protectedFiles, file)
		for parent := filepath.Dir(file); parent != spec.cwd && scopeWithin(spec.cwd, parent); parent = filepath.Dir(parent) {
			parents[parent] = true
		}
	}
	for parent := range parents {
		spec.protectedParents = append(spec.protectedParents, parent)
	}
	sort.Slice(spec.protectedParents, func(i, j int) bool {
		left, right := spec.protectedParents[i], spec.protectedParents[j]
		if len(left) == len(right) {
			return left < right
		}
		return len(left) < len(right)
	})
	sort.Strings(spec.protectedFiles)
	return nil
}

// Apply the same environment to the bwrap launcher and the inner command.
// PATH/HOME/XDG/TMP are fixed, so shell startup and Python cannot load global
// user settings or inherited injection variables. No process-global mutation.
func scopedEnvironment(env []string, cwd string) []string {
	values := map[string]string{
		"PATH": scopedCommandPath, "HOME": cwd, "TMPDIR": "/tmp", "TMP": "/tmp", "TEMP": "/tmp",
		"XDG_CONFIG_HOME": path.Join(filepath.ToSlash(cwd), ".config"),
		"XDG_CACHE_HOME":  path.Join(filepath.ToSlash(cwd), ".cache"),
		"XDG_DATA_HOME":   path.Join(filepath.ToSlash(cwd), ".local/share"),
		"XDG_STATE_HOME":  path.Join(filepath.ToSlash(cwd), ".local/state"),
		"XDG_RUNTIME_DIR": "/run/user", "LANG": "C.UTF-8", "TERM": "xterm-256color",
		"USER": "pogo", "LOGNAME": "pogo", "SHELL": "/bin/bash", "PYTHONDONTWRITEBYTECODE": "1",
	}
	for _, entry := range env {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || len(value) > 256 || strings.ContainsAny(value, "\x00\r\n") {
			continue
		}
		switch name {
		case "LANG", "LC_ALL", "LC_CTYPE", "TERM", "COLORTERM", "TZ":
			values[name] = value
		}
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]string, 0, len(values))
	for _, name := range names {
		out = append(out, name+"="+values[name])
	}
	return out
}

// Bubblewrap starts with an empty mount root; no bind of /, /home, /proc or
// parent workspace directories is ever added. Returned argv excludes bwrap.
func scopedBwrapArgs(spec commandScopeSpec, binds []scopeBind, argv []string) []string {
	a := []string{"--unshare-user", "--unshare-pid", "--unshare-ipc", "--unshare-uts", "--unshare-cgroup-try",
		"--die-with-parent", "--cap-drop", "ALL", "--clearenv"}
	if !spec.interactive {
		a = append(a, "--new-session")
	}
	if !spec.network {
		a = append(a, "--unshare-net")
	}
	a = append(a, "--dev", "/dev", "--proc", "/proc", "--tmpfs", "/tmp", "--tmpfs", "/run", "--dir", "/run/user")
	parents := func(target string) {
		var dirs []string
		for p := path.Dir(target); p != "/" && p != "."; p = path.Dir(p) {
			dirs = append(dirs, p)
		}
		for i := len(dirs) - 1; i >= 0; i-- {
			a = append(a, "--dir", dirs[i])
		}
	}
	for _, bind := range binds {
		parents(bind.target)
		a = append(a, "--ro-bind", bind.source, bind.target)
	}
	for _, shared := range spec.readRoots {
		parents(shared)
		a = append(a, "--ro-bind", shared, shared)
	}
	for _, shared := range spec.readOnlyRoots {
		parents(shared)
		a = append(a, "--ro-bind", shared, shared)
	}
	parents(spec.cwd)
	mode := "--bind"
	if spec.readOnlyWorkspace {
		mode = "--ro-bind"
	}
	a = append(a, mode, spec.cwd, spec.cwd)
	for _, parent := range spec.protectedParents {
		a = append(a, mode, parent, parent)
	}
	for _, file := range spec.protectedFiles {
		a = append(a, "--ro-bind", file, file)
	}
	for _, private := range scopedVisiblePrivateRoots(spec) {
		parents(private)
		a = append(a, "--ro-bind", private, private)
	}
	for _, entry := range scopedEnvironment(spec.env, spec.cwd) {
		name, value, _ := strings.Cut(entry, "=")
		a = append(a, "--setenv", name, value)
	}
	a = append(a, "--chdir", spec.workdir, "--remount-ro", "/", "--")
	return append(a, argv...)
}

func scopeError(err error) error { return fmt.Errorf("session command isolation: %w", err) }
