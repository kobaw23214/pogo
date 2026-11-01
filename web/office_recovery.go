package web

// Recovery is an instance-bound control protocol. It never replays a mutation,
// changes the original caller's immutable receipt, or reloads a dirty canvas.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"sort"
	"time"
)

type officeRecoveryView struct {
	State        string                    `json:"state"`
	Revision     uint64                    `json:"revision"`
	OperationIDs []string                  `json:"operationIds"`
	ExportIDs    []string                  `json:"exportIds"`
	NeedsRead    bool                      `json:"needsRead,omitempty"`
	ReadTarget   *officeRecoveryReadTarget `json:"readTarget,omitempty"`
}

type officeRecoveryReadTarget struct {
	Operation string          `json:"operation"`
	Args      json.RawMessage `json:"args"`
}

func officeReadOnly(name string) bool {
	return name == "capabilities" || name == "word.read" || name == "sheet.read" || name == "sheet.list" || name == "selection.read"
}

func officeMarkUnknownLocked(d *officeDocumentSession, op *officeOperation) {
	if op.unknown {
		return
	}
	op.unknown = true
	if officeMutation(op.name) {
		// A possibly executed write may have changed the canvas even when its
		// confirmation was lost. Protect it as dirty without claiming success.
		d.dirtyEpoch++
		op.unknownDirtyEpoch = d.dirtyEpoch
		if !d.saving {
			d.saveState = "dirty"
		}
	}
	d.blocked = true
	d.recoveryRevision++
	d.syncRevision++
	d.notifyRecoveryLocked()
}

func (d *officeDocumentSession) notifyRecoveryLocked() {
	if d.recoveryChanged != nil {
		close(d.recoveryChanged)
	}
	d.recoveryChanged = make(chan struct{})
}

func (d *officeDocumentSession) recoveryViewLocked() *officeRecoveryView {
	if !d.blocked && d.recoveryRevision == 0 {
		return nil
	}
	v := &officeRecoveryView{State: "ready", Revision: d.recoveryRevision, OperationIDs: []string{}, ExportIDs: []string{}, NeedsRead: d.recoveryNeedsRead, ReadTarget: d.recoveryReadTarget}
	if d.blocked {
		v.State = "checking"
	}
	seen := map[*officeOperation]bool{}
	for _, op := range d.operations {
		if seen[op] || !op.unknown || op.reconciled {
			continue
		}
		seen[op] = true
		if op.name == "host.export" {
			v.ExportIDs = append(v.ExportIDs, op.id)
		} else {
			v.OperationIDs = append(v.OperationIDs, op.id)
		}
	}
	sort.Strings(v.OperationIDs)
	sort.Strings(v.ExportIDs)
	return v
}

// A batch may wait for an already-running instance recovery, rather than
// treating it as a permanent loss of Office. Waiting owns no file/service lock.
func (o *officeService) waitRecovery(ctx context.Context, d *officeDocumentSession) error {
	budget := o.cfg.commandTimeout
	if budget <= 0 || budget > 15*time.Second {
		budget = 15 * time.Second
	}
	timer := time.NewTimer(budget)
	defer timer.Stop()
	for {
		d.mu.Lock()
		if d.closed {
			d.mu.Unlock()
			return officeFail(409, "SESSION_CLOSED", "编辑会话已关闭。")
		}
		if !d.blocked {
			d.mu.Unlock()
			return nil
		}
		v := d.recoveryViewLocked()
		if len(v.OperationIDs)+len(v.ExportIDs) == 0 && !v.NeedsRead {
			d.mu.Unlock()
			return officeFail(409, "SESSION_BLOCKED", "当前文档需要确认先前操作结果。")
		}
		if d.recoveryChanged == nil {
			d.recoveryChanged = make(chan struct{})
		}
		changed := d.recoveryChanged
		d.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return officeFail(409, "RECOVERY_IN_PROGRESS", "正在核对编辑器的实际结果；本次未重复执行，请等待恢复后继续。")
		}
	}
}

// Filled only by the native runtime's settled ledger and the view's matching
// export completion. A timeout alone is never a proof of completion.
type officeRecoveryReceipt struct {
	InstanceID       string `json:"instanceId"`
	RecoveryRevision uint64 `json:"recoveryRevision"`
	BarrierID        string `json:"barrierId"`
	Settled          bool   `json:"settled"`
	Operations       []struct {
		OperationID string          `json:"operationId"`
		Operation   string          `json:"operation"`
		OK          *bool           `json:"ok"`
		Result      json.RawMessage `json:"result,omitempty"`
		Error       struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error,omitempty"`
	} `json:"operations"`
	Exports []struct {
		OperationID string `json:"operationId"`
		Settled     bool   `json:"settled"`
	} `json:"exports,omitempty"`
}

func (s *Server) handleOfficeReconcile(w http.ResponseWriter, r *http.Request, d *officeDocumentSession) {
	if r.Method != http.MethodPost {
		officeHTTPError(w, officeFail(405, "METHOD_NOT_ALLOWED", "恢复确认仅支持 POST。"))
		return
	}
	var receipt officeRecoveryReceipt
	if err := officeReadBody(r, &receipt); err != nil {
		officeHTTPError(w, err)
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := s.bridgeIdentity(d, receipt.InstanceID); err != nil {
		officeHTTPError(w, err)
		return
	}
	if err := s.office.reconcileLocked(d, receipt); err != nil {
		officeHTTPError(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true, "session": d.viewLocked()})
}

func (o *officeService) reconcileLocked(d *officeDocumentSession, receipt officeRecoveryReceipt) error {
	if !receipt.Settled || !officeOperationID.MatchString(receipt.BarrierID) || receipt.RecoveryRevision != d.recoveryRevision || !d.blocked {
		return officeFail(409, "RECOVERY_UNVERIFIED", "编辑器的实际执行尚未完成核对；不会重复之前的修改。")
	}
	v := d.recoveryViewLocked()
	if len(v.OperationIDs)+len(v.ExportIDs) == 0 || len(receipt.Operations) != len(v.OperationIDs) || len(receipt.Exports) != len(v.ExportIDs) {
		return officeFail(409, "RECOVERY_UNVERIFIED", "恢复回执未覆盖当前文档的全部未知操作。")
	}
	// Validate the entire barrier before altering any document state. The
	// authenticated instance is the same executor that owns these native IDs.
	known := map[*officeOperation]json.RawMessage{}
	uncertain := map[*officeOperation]bool{}
	additional := 0
	for _, item := range receipt.Operations {
		op := d.operations[item.OperationID]
		if op == nil || !op.dispatched || !op.finished || !op.unknown || op.reconciled || !officeMutation(op.name) || item.Operation != op.name || item.OK == nil || known[op] != nil {
			return officeFail(409, "RECOVERY_UNVERIFIED", "恢复回执不属于本实例已经发送的未知修改。")
		}
		var outcome any
		if *item.OK {
			clean, err := officeSanitizeResult(item.Result)
			if err != nil || bytes.Contains(clean, []byte(d.bridgeToken)) || bytes.Contains(clean, []byte(d.fileToken)) {
				return officeFail(400, "INVALID_RECEIPT", "恢复结果不正确或含有编辑器凭据。")
			}
			outcome = map[string]any{"ok": true, "operationId": op.id, "result": clean}
		} else {
			if item.Error.Code == "" || len(item.Error.Code) > 128 || len(item.Error.Message) > 4096 || bytes.Contains([]byte(item.Error.Code+item.Error.Message), []byte(d.bridgeToken)) || bytes.Contains([]byte(item.Error.Code+item.Error.Message), []byte(d.fileToken)) {
				return officeFail(400, "INVALID_RECEIPT", "恢复错误回执不正确。")
			}
			uncertain[op] = item.Error.Code == "OUTCOME_UNKNOWN" || item.Error.Code == "EDITOR_OUTCOME_UNKNOWN"
			outcome = map[string]any{"ok": false, "operationId": op.id, "error": item.Error}
		}
		encoded, err := json.Marshal(outcome)
		additional += len(encoded)
		if err != nil || len(encoded) > officeMaxJSON || d.retainedBytes+additional > officeMaxRetainedBytes+officeFinalSaveBytes {
			return officeFail(409, "RESPONSE_LIMIT", "恢复回执超过当前文档记录上限。")
		}
		known[op] = encoded
	}
	for _, item := range receipt.Exports {
		op := d.operations[item.OperationID]
		if op == nil || !op.dispatched || !op.finished || !op.unknown || op.reconciled || op.name != "host.export" || !item.Settled || d.exportBusy || known[op] != nil {
			return officeFail(409, "RECOVERY_UNVERIFIED", "导出或写回仍在处理中，不能确认恢复。")
		}
		known[op] = json.RawMessage(`{"ok":false,"settled":true,"saved":false}`)
	}
	for _, id := range append(v.OperationIDs, v.ExportIDs...) {
		if known[d.operations[id]] == nil {
			return officeFail(409, "RECOVERY_UNVERIFIED", "恢复回执缺少当前文档的未知操作。")
		}
	}
	for op, outcome := range known {
		op.reconciled = true
		op.reconciliationResult = outcome
		d.retainedBytes += len(outcome)
		if officeMutation(op.name) {
			var verified struct {
				OK bool `json:"ok"`
			}
			_ = json.Unmarshal(outcome, &verified)
			if verified.OK {
				op.epoch = op.unknownDirtyEpoch
			}
			if uncertain[op] {
				d.recoveryNeedsRead = true
				d.recoveryReadTarget = officeRecoveryTargetLocked(d, op)
			}
		}
	}
	d.recoveryRevision++
	d.syncRevision++
	if !d.recoveryNeedsRead {
		d.blocked = false
		d.lastError = ""
	}
	d.notifyRecoveryLocked()
	return nil
}

func officeRecoveryTargetLocked(d *officeDocumentSession, unknown *officeOperation) *officeRecoveryReadTarget {
	if d.editor == "word" {
		return &officeRecoveryReadTarget{Operation: "word.read", Args: json.RawMessage(`{}`)}
	}
	extract := func(op *officeOperation) *officeRecoveryReadTarget {
		var target struct {
			Sheet string `json:"sheet"`
			Range string `json:"range"`
		}
		if json.Unmarshal(op.args, &target) != nil || target.Sheet == "" {
			return nil
		}
		if _, _, err := officeRangeSize(target.Range); err != nil {
			return nil
		}
		args, _ := json.Marshal(target)
		return &officeRecoveryReadTarget{Operation: "sheet.read", Args: args}
	}
	if target := extract(unknown); target != nil {
		return target
	}
	for i := len(d.queue) - 1; i >= 0; i-- {
		if target := extract(d.queue[i]); target != nil {
			return target
		}
	}
	return &officeRecoveryReadTarget{Operation: "sheet.list", Args: json.RawMessage(`{}`)}
}

func officeConfirmRecoveryReadLocked(d *officeDocumentSession, op *officeOperation, result json.RawMessage) {
	if !d.recoveryNeedsRead || op.recoveryReadRevision != d.recoveryRevision || !op.finished || !officeOperationSucceeded(op) || (op.name != "word.read" && op.name != "sheet.read" && op.name != "sheet.list") {
		return
	}
	for _, previous := range d.operations {
		if previous.unknown && !previous.reconciled {
			return
		}
	}
	if op.name == "sheet.list" {
		var meta struct {
			Kind   string   `json:"kind"`
			Sheets []string `json:"sheets"`
		}
		if json.Unmarshal(result, &meta) != nil || meta.Kind != "cell" || len(meta.Sheets) == 0 || len(meta.Sheets) > 4096 || meta.Sheets[0] == "" || officeTextUnits(meta.Sheets[0]) > 128 {
			return
		}
		args, _ := json.Marshal(map[string]any{"sheet": meta.Sheets[0], "range": "A1"})
		d.recoveryReadTarget = &officeRecoveryReadTarget{Operation: "sheet.read", Args: args}
		d.syncRevision++
		d.notifyRecoveryLocked()
		return
	}
	if op.name == "word.read" {
		var snapshot struct {
			Kind string  `json:"kind"`
			Text *string `json:"text"`
		}
		if json.Unmarshal(result, &snapshot) != nil || snapshot.Kind != "word" || snapshot.Text == nil || officeTextUnits(*snapshot.Text) > 200000 {
			return
		}
	} else {
		var target struct {
			Sheet string `json:"sheet"`
			Range string `json:"range"`
		}
		var snapshot struct {
			Kind   string              `json:"kind"`
			Sheet  string              `json:"sheet"`
			Range  string              `json:"range"`
			Inputs [][]json.RawMessage `json:"inputs"`
		}
		rows, cols, err := officeRangeSize(func() string { _ = json.Unmarshal(op.args, &target); return target.Range }())
		if err != nil || json.Unmarshal(result, &snapshot) != nil || snapshot.Kind != "cell" || snapshot.Sheet != target.Sheet || snapshot.Range != target.Range || !officeMatrix(snapshot.Inputs, rows, cols) {
			return
		}
	}
	d.recoveryNeedsRead = false
	d.recoveryReadTarget = nil
	d.blocked = false
	d.lastError = ""
	d.recoveryRevision++
	d.syncRevision++
	d.notifyRecoveryLocked()
}
