package web

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"

	"github.com/yosukeno/pogo/identity"
)

// Private reusable files include every owner dimension and the Agent. They
// deliberately never use the older published/global starter namespace.
func (m *Manager) shortcutAssetNamespace(owner *identity.Scope, agentID string) (string, error) {
	scope, err := shortcutScopeID(owner, agentID)
	if err != nil || m.cfg.SessionDir == "" {
		return "", errors.New("资料附件存储不可用")
	}
	return filepath.Join(m.cfg.SessionDir, ".shortcut-assets", scope), nil
}

func inputPartPointers(parts []MessageInputPart) []*MessageInputPart {
	var out []*MessageInputPart
	var visit func([]MessageInputPart)
	visit = func(ps []MessageInputPart) {
		for i := range ps {
			out = append(out, &ps[i])
			visit(ps[i].Parts)
		}
	}
	visit(parts)
	return out
}

var errShortcutAssetInvalid = errors.New("资料附件不属于当前账号及 Agent，或已不可用")

func (m *Manager) validateShortcutAssets(owner *identity.Scope, agentID string, entries []userShortcut) error {
	var root *os.Root
	defer func() {
		if root != nil {
			_ = root.Close()
		}
	}()
	seen := map[string]bool{}
	for _, entry := range entries {
		for _, part := range flatInputParts(entry.Parts) {
			if part.Type != "file" || seen[part.Path] {
				continue
			}
			seen[part.Path] = true
			if root == nil {
				namespace, err := m.shortcutAssetNamespace(owner, agentID)
				if err != nil {
					return err
				}
				root, err = os.OpenRoot(namespace)
				if err != nil {
					return errShortcutAssetInvalid
				}
			}
			f, _, err := readStarterAsset(root, part.Path)
			if err != nil {
				return errShortcutAssetInvalid
			}
			f.Close()
		}
	}
	return nil
}

func (s *Server) handleShortcutAssetsSave(w http.ResponseWriter, r *http.Request) {
	s.savedInputAssets(w, r, false, true)
}
func (s *Server) handleShortcutAssetsResolve(w http.ResponseWriter, r *http.Request) {
	s.savedInputAssets(w, r, true, true)
}

// A private material embedded in a published starter must keep its files in
// the owner's private namespace, rather than publishing their bytes globally.
func inputPrivateFiles(parts []MessageInputPart) map[*MessageInputPart]bool {
	out := map[*MessageInputPart]bool{}
	var visit func([]MessageInputPart, bool)
	visit = func(ps []MessageInputPart, private bool) {
		for i := range ps {
			out[&ps[i]] = private
			visit(ps[i].Parts, private || ps[i].Type == "shortcut")
		}
	}
	visit(parts, false)
	return out
}
