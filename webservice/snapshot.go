package webservice

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

const maxSnapshotBytes = 50 << 20
const maxSnapshotFiles = 5000

func excludedSource(name string) bool {
	name = strings.ToLower(name)
	switch name {
	case ".git", ".hg", ".svn", ".pogo", ".ssh", ".aws", "node_modules", ".venv", "venv", "__pycache__", "providers.json", "users.json", "users.json.secret", "mcp.json", "id_rsa", "id_ed25519":
		return true
	}
	return name == ".env" || strings.HasPrefix(name, ".env.") || strings.HasSuffix(name, ".pem") || strings.HasSuffix(name, ".key")
}

func canonicalWorkspace(workspace, private string) (string, error) {
	if !filepath.IsAbs(workspace) {
		return "", Fail("INVALID_PROJECT_ROOT", "host workspace must be absolute")
	}
	workspace = filepath.Clean(workspace)
	if _, err := workspaceInfo(workspace); err != nil {
		return "", Fail("INVALID_PROJECT_ROOT", "workspace is unavailable")
	}
	within := func(parent, child string) bool {
		rel, err := filepath.Rel(parent, child)
		return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
	}
	if within(workspace, private) || within(private, workspace) {
		return "", Fail("INVALID_PROJECT_ROOT", "service control storage must be outside the workspace")
	}
	return workspace, nil
}

func workspaceInfo(path string) (os.FileInfo, error) {
	var result os.FileInfo
	for p := filepath.Clean(path); ; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil {
			return nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return nil, Fail("INVALID_PROJECT_ROOT", "workspace path must contain real directories")
		}
		if result == nil {
			result = info
		}
		if filepath.Dir(p) == p {
			return result, nil
		}
	}
}

func projectRelative(name string) (string, error) {
	if name == "" || filepath.IsAbs(name) || strings.ContainsAny(name, "\\\x00") {
		return "", Fail("INVALID_PROJECT_ROOT", "project_root must be a workspace-relative directory")
	}
	name = filepath.Clean(name)
	if name == ".." || strings.HasPrefix(name, ".."+string(filepath.Separator)) {
		return "", Fail("INVALID_PROJECT_ROOT", "project_root escapes the workspace")
	}
	for _, part := range strings.Split(filepath.ToSlash(name), "/") {
		if excludedSource(part) {
			return "", Fail("INVALID_PROJECT_ROOT", "private directories cannot be projects")
		}
	}
	return name, nil
}

func snapshot(workspace, project string) ([]byte, string, error) {
	return snapshotContext(context.Background(), workspace, project)
}

func snapshotContext(ctx context.Context, workspace, project string) ([]byte, string, error) {
	project, err := projectRelative(project)
	if err != nil {
		return nil, "", err
	}
	for attempt := 0; attempt < 2; attempt++ {
		archive, digest, err := snapshotOnce(ctx, workspace, project)
		if err == nil {
			return archive, digest, nil
		}
		if e, ok := err.(*Error); !ok || e.Code != "SOURCE_CHANGED" {
			return nil, "", err
		}
	}
	return nil, "", Fail("SOURCE_CHANGED", "project changed while taking its snapshot; finish edits and retry")
}

func snapshotOnce(ctx context.Context, workspace, project string) ([]byte, string, error) {
	before, err := workspaceInfo(workspace)
	if err != nil {
		return nil, "", err
	}
	w, err := os.OpenRoot(workspace)
	if err != nil {
		return nil, "", Fail("INVALID_PROJECT_ROOT", "workspace cannot be opened")
	}
	defer w.Close()
	opened, err := w.Stat(".")
	after, pathErr := workspaceInfo(workspace)
	if err != nil || pathErr != nil || !os.SameFile(before, opened) || !os.SameFile(before, after) {
		return nil, "", Fail("SOURCE_CHANGED", "workspace identity changed")
	}
	part := ""
	var projectInfo os.FileInfo
	for _, component := range strings.Split(filepath.ToSlash(project), "/") {
		part = filepath.Join(part, component)
		info, err := w.Lstat(part)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return nil, "", Fail("INVALID_PROJECT_ROOT", "project path must contain real directories")
		}
		projectInfo = info
	}
	r, err := w.OpenRoot(project)
	if err != nil {
		return nil, "", Fail("INVALID_PROJECT_ROOT", "project cannot be opened")
	}
	defer r.Close()
	pinnedProject, err := r.Stat(".")
	currentProject, pathErr := w.Lstat(project)
	if err != nil || pathErr != nil || currentProject.Mode()&os.ModeSymlink != 0 || !os.SameFile(projectInfo, pinnedProject) || !os.SameFile(projectInfo, currentProject) {
		return nil, "", Fail("SOURCE_CHANGED", "project directory identity changed")
	}
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	first, err := snapshotPass(ctx, r, writer)
	if closeErr := writer.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, "", err
	}
	second, err := snapshotPass(ctx, r, nil)
	if err != nil {
		return nil, "", err
	}
	if first != second {
		return nil, "", Fail("SOURCE_CHANGED", "project changed during snapshot")
	}
	after, err = workspaceInfo(workspace)
	if err != nil || !os.SameFile(before, after) {
		return nil, "", Fail("SOURCE_CHANGED", "workspace identity changed")
	}
	currentProject, pathErr = w.Lstat(project)
	if pathErr != nil || currentProject.Mode()&os.ModeSymlink != 0 || !os.SameFile(projectInfo, currentProject) {
		return nil, "", Fail("SOURCE_CHANGED", "project directory identity changed")
	}
	return archive.Bytes(), first, nil
}

func snapshotPass(ctx context.Context, root *os.Root, archive *tar.Writer) (string, error) {
	manifest := sha256.New()
	var total int64
	files := 0
	err := fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return Fail("SOURCE_CHANGED", "project entry changed during snapshot")
		}
		if name == "." {
			return nil
		}
		if excludedSource(entry.Name()) {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return Fail("SOURCE_CHANGED", "project entry disappeared")
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return Fail("INVALID_PROJECT_ROOT", "project snapshots do not follow symlinks")
		}
		if info.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return Fail("INVALID_PROJECT_ROOT", "project contains a non-regular file")
		}
		files++
		total += info.Size()
		if files > maxSnapshotFiles || total > maxSnapshotBytes {
			return Fail("SNAPSHOT_LIMIT_EXCEEDED", "project exceeds 5000 files or 50 MiB")
		}
		file, err := openSnapshotFile(root, name)
		if err != nil {
			return Fail("SOURCE_CHANGED", "project file changed before reading")
		}
		defer file.Close()
		actual, err := file.Stat()
		if err != nil || !actual.Mode().IsRegular() || !os.SameFile(info, actual) || actual.Size() != info.Size() {
			return Fail("SOURCE_CHANGED", "project file identity changed")
		}
		if archive != nil {
			if err := archive.WriteHeader(&tar.Header{Name: name, Mode: 0644, Size: info.Size(), Typeflag: tar.TypeReg}); err != nil {
				return err
			}
		}
		hash := sha256.New()
		var output io.Writer = hash
		if archive != nil {
			output = io.MultiWriter(hash, archive)
		}
		n, err := io.Copy(output, io.LimitReader(&snapshotReader{ctx, file}, info.Size()+1))
		if err != nil || n != info.Size() {
			return Fail("SOURCE_CHANGED", "project file changed while reading")
		}
		end, err := file.Stat()
		if err != nil || end.Size() != info.Size() || !end.ModTime().Equal(info.ModTime()) {
			return Fail("SOURCE_CHANGED", "project file changed while reading")
		}
		_, _ = fmt.Fprintf(manifest, "%s\x00%d\x00%s\n", name, n, hex.EncodeToString(hash.Sum(nil)))
		return nil
	})
	if err != nil {
		return "", err
	}
	if files == 0 {
		return "", Fail("INVALID_PROJECT_ROOT", "project has no included source files")
	}
	return hex.EncodeToString(manifest.Sum(nil)), nil
}

type snapshotReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *snapshotReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}
