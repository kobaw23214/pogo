package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"

	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/shortcuts"
)

type shortcutCreateReceipt struct {
	Digest     string `json:"digest"`
	Revision   int64  `json:"revision"`
	ShortcutID string `json:"shortcut_id"`
}

const shortcutToolMaxReceipts = 4096

var shortcutReceiptHash = regexp.MustCompile(`^[a-f0-9]{64}$`)
var shortcutToolRequestID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,160}$`)

func validShortcutCreateReceipts(receipts map[string]shortcutCreateReceipt, revision int64) bool {
	if len(receipts) > shortcutToolMaxReceipts {
		return false
	}
	for key, receipt := range receipts {
		if !shortcutReceiptHash.MatchString(key) || !shortcutReceiptHash.MatchString(receipt.Digest) || receipt.Revision < 1 || receipt.Revision > revision {
			return false
		}
		if !shortcutFolderID.MatchString(receipt.ShortcutID) {
			return false
		}
	}
	return true
}

func replayToolShortcut(doc shortcutDocument, receipt shortcutCreateReceipt, requestID string) (shortcuts.CreateResult, error) {
	for _, entry := range doc.Shortcuts {
		if entry.ID == receipt.ShortcutID {
			return shortcuts.CreateResult{Shortcut: shortcutEntry(entry), Revision: doc.Revision, RequestID: requestID, Reused: true}, nil
		}
	}
	return shortcuts.CreateResult{}, errors.New("this request already created a shortcut that was subsequently deleted; it will not be recreated; use a new request_id only if creating a new shortcut is intended")
}

func shortcutToolDigest(value any) string {
	raw, _ := json.Marshal(value)
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:])
}

func shortcutEntry(entry userShortcut) shortcuts.Entry {
	return shortcuts.Entry{ID: entry.ID, Title: entry.Title, Content: entry.Content, Icon: entry.Icon, Language: entry.Language, Parts: entry.Parts, InputLayout: entry.InputLayout, ParentID: entry.ParentID}
}

// appendToolShortcut updates only one root against the latest private document.
// The receipt and new root are one atomic write. A later UI CAS save preserves
// the receipt even when that root is edited or deleted.
func (m *Manager) appendToolShortcut(owner *identity.Scope, agentID, requestID, digest string, entry userShortcut) (shortcuts.CreateResult, error) {
	var result shortcuts.CreateResult
	if !shortcutToolRequestID.MatchString(requestID) || !shortcutReceiptHash.MatchString(digest) {
		return result, errors.New("invalid shortcut request_id")
	}
	p, err := shortcutPath(m.cfg.SessionDir, owner, agentID)
	if err != nil {
		return result, err
	}
	scope, _ := shortcutScopeID(owner, agentID)
	shortcutsMu.Lock()
	defer shortcutsMu.Unlock()
	doc, err := readShortcutDocument(p, scope, agentID)
	if err != nil {
		return result, err
	}
	key := shortcutToolDigest(requestID)
	if old, ok := doc.ToolCreates[key]; ok {
		if old.Digest != digest {
			return result, errors.New("request_id already used for different shortcut content; use a new request_id")
		}
		out, err := replayToolShortcut(doc, old, requestID)
		if err == nil && out.Shortcut.ParentID != entry.ParentID {
			return result, errors.New("the existing shortcut was moved; its current location and content were preserved; use a new request_id for a separate copy")
		}
		return out, err
	}
	if len(doc.ToolCreates) >= shortcutToolMaxReceipts {
		return result, errors.New("shortcut request history reached its safety limit; ask an administrator to archive this Agent's request history before creating more shortcuts; existing content is unchanged")
	}
	if doc.Revision >= shortcutsMaxRevision || len(doc.Shortcuts) >= shortcutsMaxCount {
		return result, errors.New("private shortcut storage limit reached")
	}
	entries, err := validateUserShortcuts(append(append([]userShortcut{}, doc.Shortcuts...), entry))
	if err != nil {
		return result, err
	}
	if _, err = validateShortcutFolders(doc.Folders, entries); err != nil {
		return result, err
	}
	if err = m.validateShortcutAssets(owner, agentID, []userShortcut{entry}); err != nil {
		return result, err
	}
	doc.Shortcuts = entries
	doc.Revision++
	if doc.ToolCreates == nil {
		doc.ToolCreates = map[string]shortcutCreateReceipt{}
	}
	doc.ToolCreates[key] = shortcutCreateReceipt{Digest: digest, Revision: doc.Revision, ShortcutID: entry.ID}
	var data bytes.Buffer
	enc := json.NewEncoder(&data)
	enc.SetEscapeHTML(false)
	if err = enc.Encode(doc); err != nil || data.Len() > shortcutsMaxBody {
		return result, errors.New("private shortcut storage limit reached")
	}
	if err = writePrivateFileAtomic(p, data.Bytes()); err != nil {
		return result, errors.New("shortcut save failed; previous version preserved")
	}
	return shortcuts.CreateResult{Shortcut: shortcutEntry(entry), Revision: doc.Revision, RequestID: requestID}, nil
}

func (m *Manager) toolShortcutReceipt(owner *identity.Scope, agentID, requestID, digest string) (shortcuts.CreateResult, bool, error) {
	p, err := shortcutPath(m.cfg.SessionDir, owner, agentID)
	if err != nil {
		return shortcuts.CreateResult{}, false, err
	}
	scope, _ := shortcutScopeID(owner, agentID)
	shortcutsMu.Lock()
	defer shortcutsMu.Unlock()
	doc, err := readShortcutDocument(p, scope, agentID)
	if err != nil {
		return shortcuts.CreateResult{}, false, err
	}
	old, ok := doc.ToolCreates[shortcutToolDigest(requestID)]
	if !ok {
		return shortcuts.CreateResult{}, false, nil
	}
	if old.Digest != digest {
		return shortcuts.CreateResult{}, true, errors.New("request_id already used for different shortcut content; use a new request_id")
	}
	result, err := replayToolShortcut(doc, old, requestID)
	return result, true, err
}
