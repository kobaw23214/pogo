package web

import (
	"context"
	"errors"
	"fmt"

	"github.com/yosukeno/pogo/identity"
)

// This is the same trusted identity used by retained subagent records and the
// Web session's result registry. Keep creation, resume and deletion together:
// neither a transcript's workspace nor a request-supplied path is an owner.
func sessionResultOwnerKey(owner *identity.Scope) string {
	if owner == nil {
		return "single-user"
	}
	return fmt.Sprintf("%q/%q/%q/%q", owner.AppID, owner.TenantID, owner.SubjectID, owner.ProjectID)
}

func sessionResultScope(id string, owner *identity.Scope) string {
	return "web:" + sessionResultOwnerKey(owner) + ":" + id
}

// Called while the Manager's deletion reservation is held, before closing a
// live session or changing Office/catalog/transcript state. Cold sessions use
// the ownership registry directly, without constructing an Agent. Tombstoned
// rows retain their owner and remain valid for an internal retry.
func (m *Manager) sessionResultOwnerForDelete(id string, live *Session) (*identity.Scope, error) {
	if m.cfg.Catalog == nil {
		if live != nil && live.owner != nil {
			owner := *live.owner
			return &owner, nil
		}
		return nil, nil
	}
	record, err := m.cfg.Catalog.OwnerOf(context.Background(), id)
	if err != nil {
		return nil, fmt.Errorf("resolve session owner before Spill cleanup: %w", err)
	}
	if !record.Scope.Valid() {
		return nil, errors.New("session owner is invalid; Spill cleanup was not started")
	}
	if live != nil && (live.owner == nil || *live.owner != record.Scope) {
		return nil, errors.New("session owner does not match the active registry; Spill cleanup was not started")
	}
	owner := record.Scope
	return &owner, nil
}
