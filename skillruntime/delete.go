package skillruntime

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/yosukeno/pogo/skillruntime/core"
)

var ErrPackageNotFound = errors.New("Skill版本不存在，请刷新列表")
var ErrPackageBusy = errors.New("Skill仍有调用正在结束，请稍后重试")

type DeleteResult struct {
	Deleted        bool `json:"deleted"`
	CleanupPending bool `json:"cleanup_pending"`
}

// Delete requires the caller to serialize and check persistent Agent/session
// references. It serializes installation and active calls at the service gate.
func (s *Service) Delete(digest string) (DeleteResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	index := -1
	for i, entry := range s.installed {
		if entry.Ref.PackageDigest == digest {
			index = i
			break
		}
	}
	if index < 0 {
		return DeleteResult{}, ErrPackageNotFound
	}
	if len(s.active[digest]) > 0 {
		return DeleteResult{}, ErrPackageBusy
	}
	ref := s.installed[index].Ref
	if !core.IsDigest(digest) {
		return DeleteResult{}, errors.New("invalid package digest")
	}
	journal := filepath.Join(s.Root, ".delete-"+strings.TrimPrefix(digest, "sha256:")+".json")
	// The journal precedes the catalog commit. Recovery keeps the package if
	// the commit did not happen, and resumes cleanup only after unpublication.
	if err := atomicJSON(journal, ref); err != nil {
		return DeleteResult{}, err
	}
	next := append([]Installed{}, s.installed[:index]...)
	next = append(next, s.installed[index+1:]...)
	if err := atomicJSON(filepath.Join(s.Root, "catalog.json"), next); err != nil {
		_ = os.Remove(journal)
		return DeleteResult{}, err
	}
	s.installed = next
	s.catalogMu.Lock()
	defer s.catalogMu.Unlock()
	delete(s.metadata, ref)
	result := DeleteResult{Deleted: true}
	if err := s.Store.Remove(ref); err != nil {
		result.CleanupPending = true
		return result, nil
	}
	if err := os.Remove(journal); err != nil {
		result.CleanupPending = true
	}
	return result, nil
}

// recoverDeletes is called at startup and before installation under s.mu.
// Never run pkg uninstall scripts; only reclaim that version's own tree.
func (s *Service) recoverDeletes() error {
	entries, err := os.ReadDir(s.Root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".delete-") || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		path := filepath.Join(s.Root, entry.Name())
		if !entry.Type().IsRegular() {
			return errors.New("invalid Skill deletion journal")
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var ref core.PackageRef
		if err := json.Unmarshal(raw, &ref); err != nil {
			return err
		}
		if !core.IsDigest(ref.PackageDigest) || entry.Name() != ".delete-"+strings.TrimPrefix(ref.PackageDigest, "sha256:")+".json" {
			return errors.New("invalid Skill deletion journal reference")
		}
		keep := false
		for _, installed := range s.installed {
			if installed.Ref.ID == ref.ID && installed.Ref.Version == ref.Version {
				keep = true
				break
			}
		}
		if !keep {
			if err := s.Store.Remove(ref); err != nil {
				return fmt.Errorf("resume Skill deletion: %w", err)
			}
		}
		if err := os.Remove(path); err != nil {
			return err
		}
	}
	return nil
}
