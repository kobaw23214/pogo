package webservice

import (
	"context"
	"strings"
	"time"
)

func deletionPending() *Error {
	return &Error{Code: "SERVICE_DELETE_PENDING", Message: "website deletion is pending; refresh or retry deleting the same website", Retryable: true}
}

// DeleteService is an owner-management API, deliberately absent from the Agent
// tool actions. A durable intent fences all older operations before cleanup.
// Request cancellation stops waiting, not the accepted background deletion.
func (m *Manager) DeleteService(ctx context.Context, scope Scope, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if scope.Owner == "" || scope.Session == "" || strings.ContainsRune(scope.Owner+scope.Session, 0) || !operationName.MatchString(id) {
		return Fail("INVALID_ARGUMENT", "a trusted conversation scope and service ID are required")
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return Fail("FEATURE_DISABLED", "service manager is closed")
	}
	r, exists := m.disk.Records[id]
	if !exists {
		m.mu.Unlock()
		return nil // A repeated DELETE has no additional effects.
	}
	if r.Owner != scope.Owner || r.Session != scope.Session || m.disk.Deleted[scopeKey(scope)] || m.disk.DeletedOwners[scope.Owner] {
		m.mu.Unlock()
		return Fail("NOT_FOUND", "service not found")
	}
	if !m.disk.DeletingServices[id] {
		r.State, r.Phase, r.TerminalTarget = "stopping", "deleting", "stopped"
		r.ControlID = "delete:" + id
		r.Revision++
		r.UpdatedAt = m.now()
		r.LastError = nil
		if err := m.commitLocked(func(d *diskState) {
			d.DeletingServices[id] = true
			d.Records[id] = r
			delete(d.PolicyRestarts, id)
			for key, op := range d.Operations {
				if op.ServiceID == id && (op.Status == "accepted" || op.Status == "running") {
					op.Status, op.Error = "cancelled", &Error{Code: "OPERATION_CANCELLED", Message: "website deletion superseded this operation"}
					d.Operations[key] = op
				}
			}
		}); err != nil {
			m.mu.Unlock()
			return err
		}
	}
	if cancel := m.launchCancel[id]; cancel != nil {
		cancel()
	}
	for key, grant := range m.grants {
		if grant.ServiceID == id {
			delete(m.grants, key)
		}
	}
	done := make(chan error, 1)
	m.queueLocked(id, func() { done <- m.attemptServiceDeletion(id) })
	m.mu.Unlock()
	m.notify(r)
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return deletionPending()
	case <-m.ctx.Done():
		return deletionPending()
	}
}

// Called with this service's queue lock held. Failed cleanup keeps both its
// record and the intent so maintenance (including after a restart) retries it.
func (m *Manager) attemptServiceDeletion(id string) error {
	r, exists := m.current(id)
	if !exists {
		return nil
	}
	ctx, cancel := context.WithTimeout(m.ctx, 30*time.Second)
	defer cancel()
	if err := m.deleteRecord(ctx, r); err != nil {
		pending := deletionPending()
		m.mu.Lock()
		current, ok := m.disk.Records[id]
		if ok && m.disk.DeletingServices[id] {
			current.State, current.Phase = "stopping", "deleting"
			current.LastError = pending
			current.Revision++
			current.UpdatedAt = m.now()
			_ = m.commitLocked(func(d *diskState) { d.Records[id] = current })
		}
		m.mu.Unlock()
		if ok {
			m.notify(current)
		}
		return pending
	}
	return nil
}
