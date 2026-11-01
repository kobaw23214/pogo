package tools

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

var invalidCommandProtectionManifests sync.Map

// CheckCommandProtectionManifest lets a parent command gate reject a stale
// child process after host registration failed. It checks live host facts and
// never treats an older child-held manifest as authorization to keep writing.
func CheckCommandProtectionManifest(filename string) error {
	if filename == "" {
		return nil
	}
	manifest, err := readCommandProtectionManifest(filename)
	if err != nil {
		return err
	}
	_, err = commandProtectionPrivateRoots(filename, manifest)
	return err
}

// InvalidateCommandProtectionManifest fails the existing path closed, including
// callers that have already captured it. Truncation does not need free space;
// rename/removal is the fallback when the file itself is no longer writable.
// The process-local guard remains mandatory when storage rejects all mutation.
func InvalidateCommandProtectionManifest(filename string) error {
	filename = filepath.Clean(filename)
	invalidCommandProtectionManifests.Store(filename, struct{}{})
	file, err := os.OpenFile(filename, os.O_WRONLY|os.O_TRUNC, 0)
	if err == nil {
		err = file.Sync()
		closeErr := file.Close()
		if err == nil {
			err = closeErr
		}
		return err
	}
	if os.IsNotExist(err) {
		return nil
	}
	if renameErr := os.Rename(filename, filename+".blocked"); renameErr == nil {
		return nil
	}
	if removeErr := os.Remove(filename); removeErr == nil || os.IsNotExist(removeErr) {
		return nil
	}
	return err
}

// RestoreCommandProtectionManifest is called only after the host has durably
// rewritten the complete authoritative registry. Models cannot clear a guard.
func RestoreCommandProtectionManifest(filename string) {
	invalidCommandProtectionManifests.Delete(filepath.Clean(filename))
}

// WrapCommandWithProtection keeps the ordinary sandbox policy (including its
// network, runtime and additional writable roots), adding only the Web host's
// managed document protection. The manifest is trusted host state, never a tool
// argument. Callers without one retain the ordinary command behavior.
func WrapCommandWithProtection(cmd *exec.Cmd, manifest string) error {
	return wrapCommandWithProtection(cmd, manifest)
}

func inheritedCommandProtection(cmd *exec.Cmd) string {
	// A Web parent installs this in its child agent process, not the model's
	// shell text. Shell environment assignments run only after this wrapper.
	if cmd != nil && cmd.Env != nil {
		for _, entry := range cmd.Env {
			if value, ok := strings.CutPrefix(entry, EnvCommandProtectionManifest+"="); ok {
				return value
			}
		}
	}
	return os.Getenv(EnvCommandProtectionManifest)
}

type commandDocumentProtection struct {
	privateRoots []string
	files        []string
	parents      []string
}

func documentCommandProtection(cmd *exec.Cmd, manifest string) (commandDocumentProtection, error) {
	var result commandDocumentProtection
	if manifest == "" {
		return result, nil
	}
	if cmd == nil || cmd.Process != nil {
		return result, errors.New("managed document protection requires an unstarted command")
	}
	data, err := readCommandProtectionManifest(manifest)
	if err != nil {
		return result, err
	}
	// The ordinary sandbox exposes several writable roots, rather than only
	// workdir. Normalize every host-authorized path, then anchor only parents
	// already inside those writable grants. An ancestor of a grant stays ro.
	spec := commandScopeSpec{cwd: string(filepath.Separator)}
	if err = normalizeProtectedFiles(&spec, data.Paths); err != nil {
		return result, err
	}
	result.files, result.parents = spec.protectedFiles, spec.protectedParents
	result.privateRoots, err = commandProtectionPrivateRoots(manifest, data)
	if err != nil {
		return commandDocumentProtection{}, err
	}
	return result, nil
}

func (protection commandDocumentProtection) mountArgs(writable []string) []string {
	if len(protection.privateRoots) == 0 {
		return nil
	}
	parents := map[string]bool{}
	for _, parent := range protection.parents {
		parents[parent] = true
	}
	for _, private := range protection.privateRoots {
		for parent := filepath.Dir(private); filepath.Dir(parent) != parent; parent = filepath.Dir(parent) {
			parents[parent] = true
		}
	}
	var anchors []string
	for parent := range parents {
		for _, root := range writable {
			if scopeWithin(root, parent) {
				anchors = append(anchors, parent)
				break
			}
		}
	}
	// Parent-first self binds preserve all existing write grants. They pin
	// names; only the actual managed files/private manifest directory become ro.
	sortProtectionParents(anchors)
	args := []string{"--cap-drop", "ALL"}
	for _, parent := range anchors {
		args = append(args, "--bind", parent, parent)
	}
	for _, file := range protection.files {
		args = append(args, "--ro-bind", file, file)
	}
	for _, private := range protection.privateRoots {
		args = append(args, "--ro-bind", private, private)
	}
	return args
}

// checkManagedDocumentMutation guards host file tools with the same live
// registry as command mounts. It leaves new/unmanaged outputs available.
func checkManagedDocumentMutation(filename, manifestPath string) error {
	if manifestPath == "" {
		manifestPath = inheritedCommandProtection(nil)
	}
	if manifestPath == "" {
		return nil
	}
	manifest, err := readCommandProtectionManifest(manifestPath)
	if err != nil {
		return fmt.Errorf("managed document protection: %w", err)
	}
	privateRoots, err := commandProtectionPrivateRoots(manifestPath, manifest)
	if err != nil {
		return err
	}
	absolute := canonical(filepath.Clean(filename))
	for _, root := range privateRoots {
		if scopeWithin(root, absolute) {
			return errors.New("Pogo private document state is read-only to file tools")
		}
	}
	target, _ := os.Stat(absolute)
	for _, given := range manifest.Paths {
		if !filepath.IsAbs(given) {
			return errors.New("managed document manifest contains an invalid path")
		}
		managed := canonical(filepath.Clean(given))
		matched := absolute == managed
		if !matched && target != nil {
			if info, err := os.Stat(managed); err == nil {
				matched = os.SameFile(target, info)
			}
		}
		if matched {
			return errors.New("this Office document is managed; use docs_office live edits or action:transform instead of overwriting its original path")
		}
	}
	return nil
}

func sortProtectionParents(paths []string) {
	// Kept shared with strict CommandScope so both execution paths have the
	// same ancestor ordering without selecting a different sandbox policy.
	sort.Slice(paths, func(i, j int) bool {
		if len(paths[i]) == len(paths[j]) {
			return paths[i] < paths[j]
		}
		return len(paths[i]) < len(paths[j])
	})
}
