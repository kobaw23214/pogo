package skillruntime

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// PurgeSessionData removes only a conversation's private pkg inputs and
// artifacts. Callers must first revoke/delete the conversation, with no calls
// running. OpenRoot prevents parent symlinks from escaping the pkg store.
func (s *Service) PurgeSessionData(sessionID string) error {
	if sessionID == "" {
		return errors.New("session ID is required")
	}
	root, err := os.OpenRoot(s.Root)
	if err != nil {
		return err
	}
	defer root.Close()
	key := strings.TrimPrefix(bytesDigest([]byte(sessionID)), "sha256:")
	for _, base := range []string{"session-files", "session-artifacts"} {
		if err := root.RemoveAll(filepath.Join(base, key)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}
