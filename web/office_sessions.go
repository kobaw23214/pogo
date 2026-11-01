package web

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/yosukeno/pogo/identity"
	"github.com/yosukeno/pogo/tools"
)

func officeCheckPackage(data []byte, fileType string) error {
	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil || len(archive.File) > 4096 {
		return officeFail(400, "INVALID_DOCUMENT", "文件不是受支持的 Office 文档。")
	}
	required := "word/document.xml"
	if fileType == "xlsx" {
		required = "xl/workbook.xml"
	}
	found, contentTypes := false, false
	for _, entry := range archive.File {
		if entry.Name == required {
			found = true
		}
		if entry.Name == "[Content_Types].xml" {
			contentTypes = true
		}
	}
	if !found || !contentTypes {
		return officeFail(400, "INVALID_DOCUMENT", "文件不是与扩展名对应的 Office 文档。")
	}
	return nil
}

func officeReadFile(parent *os.Root, name string) ([]byte, os.FileInfo, error) {
	stat, err := parent.Lstat(name)
	if err != nil {
		return nil, nil, err
	}
	if !stat.Mode().IsRegular() {
		return nil, nil, officeFail(409, "FILE_IDENTITY_CHANGED", "文档必须是普通文件，不能是符号链接或特殊文件。")
	}
	f, err := parent.Open(name)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	if !info.Mode().IsRegular() || !os.SameFile(stat, info) {
		return nil, nil, officeFail(409, "FILE_IDENTITY_CHANGED", "文档身份已改变。")
	}
	if info.Size() > officeMaxBytes {
		return nil, nil, officeFail(413, "DOCUMENT_LIMIT", "当前 Office 文档不能超过 20 MiB。")
	}
	data, err := io.ReadAll(io.LimitReader(f, officeMaxBytes+1))
	if err != nil {
		return nil, nil, err
	}
	if len(data) > officeMaxBytes {
		return nil, nil, officeFail(413, "DOCUMENT_LIMIT", "当前 Office 文档不能超过 20 MiB。")
	}
	return data, info, nil
}

func (o *officeService) open(conversationID, cwd string, owner *identity.Scope, hub *Hub, absolute, panelPath string, journal tools.Journal) (*officeDocumentSession, error) {
	return o.openRole(conversationID, cwd, owner, hub, absolute, panelPath, journal, nil)
}

func (o *officeService) openRole(conversationID, cwd string, owner *identity.Scope, hub *Hub, absolute, panelPath string, journal tools.Journal, expectedRole *officeRole) (*officeDocumentSession, error) {
	return o.openRoleLease(conversationID, cwd, owner, hub, absolute, panelPath, journal, expectedRole, "")
}

func (o *officeService) openRoleLease(conversationID, cwd string, owner *identity.Scope, hub *Hub, absolute, panelPath string, journal tools.Journal, expectedRole *officeRole, lease string) (*officeDocumentSession, error) {
	if conversationID == "" || cwd == "" {
		return nil, officeFail(400, "NO_CONVERSATION", "请先选择当前会话。")
	}
	canonicalRoot, err := tools.Resolve(cwd, ".")
	if err != nil {
		return nil, officeFail(400, "PATH_REJECTED", "工作区无法安全解析。")
	}
	checked, err := tools.Resolve(canonicalRoot, absolute)
	if err != nil {
		return nil, officeFail(400, "PATH_REJECTED", "文档不在当前会话工作区。")
	}
	relative, err := filepath.Rel(canonicalRoot, checked)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(os.PathSeparator)) {
		return nil, officeFail(400, "PATH_REJECTED", "文档路径不正确。")
	}
	fileType := strings.ToLower(strings.TrimPrefix(filepath.Ext(checked), "."))
	if fileType != "docx" && fileType != "xlsx" {
		return nil, officeFail(400, "UNSUPPORTED_DOCUMENT", "Office 编辑目前支持 .docx 和 .xlsx。")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return nil, officeFail(503, "OFFICE_STOPPED", "Office 服务已停止。")
	}
	if o.editorJournalError {
		return nil, officeFail(503, "EDITOR_STORAGE_UNCONFIRMED", "编辑状态记录需要核对，文件仍可只读查看。")
	}
	if expectedRole != nil && (expectedRole.revoked.Load() || o.roles[conversationID] != expectedRole) {
		return nil, officeFail(409, "CONVERSATION_CLOSED", "当前会话的 Office 操作身份已撤销。")
	}
	if held := o.batchLeases[checked]; held != "" && held != lease {
		return nil, officeFail(409, "DOCUMENT_BATCH_ACTIVE", "此文档正在进行受控批处理，请等待更新完成。")
	}
	o.roleLocked(conversationID)
	if existing := o.sessions[o.active[checked]]; existing != nil {
		existing.mu.Lock()
		closed := existing.closed
		existing.mu.Unlock()
		if !closed {
			if existing.conversationID == conversationID && officeSameOwner(existing.owner, owner) && existing.cwd == cwd {
				return existing, nil
			}
			return nil, officeFail(409, "DOCUMENT_ALREADY_OPEN", "该文件已在另一个会话中打开，请先保存并关闭原编辑会话。")
		}
	}
	if len(o.active) >= 32 {
		return nil, officeFail(409, "EDITOR_LIMIT", "打开的 Office 文档已达当前服务上限。")
	}
	return o.createDocumentLocked(conversationID, cwd, owner, hub, checked, panelPath, journal, canonicalRoot, relative, fileType)
}

func (o *officeService) createDocumentLocked(conversationID, cwd string, owner *identity.Scope, hub *Hub, checked, panelPath string, journal tools.Journal, canonicalRoot, relative, fileType string) (*officeDocumentSession, error) {
	root, err := os.OpenRoot(canonicalRoot)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	parent, err := root.OpenRoot(filepath.Dir(relative))
	if err != nil {
		return nil, officeFail(400, "PATH_REJECTED", "文档目录无法安全打开。")
	}
	ok := false
	defer func() {
		if !ok {
			parent.Close()
		}
	}()
	data, info, err := officeReadFile(parent, filepath.Base(relative))
	if err != nil {
		return nil, err
	}
	if err := officeCheckPackage(data, fileType); err != nil {
		return nil, err
	}
	parentInfo, err := parent.Stat(".")
	if err != nil {
		return nil, err
	}
	o.generation++
	id := officeNewID()
	if panelPath == "" {
		panelPath = filepath.ToSlash(relative)
	}
	d := &officeDocumentSession{syncRevision: 1, id: id, documentID: randomID(24), conversationID: conversationID, cwd: cwd, absolute: checked, relative: relative, panelPath: filepath.ToSlash(panelPath), title: filepath.Base(checked), fileType: fileType, editor: "word", key: "pogo-" + id, bridgeToken: randomID(32), fileToken: randomID(32), owner: officeCopyOwner(owner), hub: hub, generation: o.generation, saveState: "saved", snapshot: data, baseHash: officeHash(data), diskVersion: officeHash(data), baseInfo: info, parentInfo: parentInfo, parent: parent, operations: map[string]*officeOperation{}, journal: journal}
	if fileType == "xlsx" {
		d.editor = "cell"
	}
	d.role = o.roleLocked(conversationID)
	d.bootEpoch = o.epochLocked()
	if err := o.registerCommandProtectedSession(d); err != nil {
		return nil, err
	}
	o.sessions[id] = d
	o.active[checked] = id
	if err := o.persistEditorLocked(d); err != nil {
		delete(o.sessions, id)
		delete(o.active, checked)
		return nil, err
	}
	ok = true
	return d, nil
}

func (o *officeService) list(conversationID string, owner *identity.Scope) []officeSessionView {
	o.mu.Lock()
	documents := []*officeDocumentSession{}
	for _, d := range o.sessions {
		if d.conversationID == conversationID && officeSameOwner(d.owner, owner) {
			documents = append(documents, d)
		}
	}
	o.mu.Unlock()
	result := []officeSessionView{}
	for _, d := range documents {
		// Registry membership is snapshotted before taking document locks;
		// a slow commit must not hold up other documents' registration.
		o.observeDisk(d)
		view := d.metadataView()
		if !view.Closed {
			result = append(result, view)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Generation < result[j].Generation })
	return result
}

func officeEmptyArgs(args json.RawMessage) bool {
	var obj map[string]json.RawMessage
	return json.Unmarshal(args, &obj) == nil && obj != nil && len(obj) == 0
}

type officeWordReplaceArgs struct {
	Search         string  `json:"search"`
	Replacement    string  `json:"replacement"`
	ExpectedText   *string `json:"expectedText,omitempty"`
	ExpectedReadID *string `json:"expectedReadId,omitempty"`
	ExpectedCount  int     `json:"expectedCount"`
}

func officeWordReplaceArguments(args json.RawMessage) (officeWordReplaceArgs, error) {
	// There is only one supported replacement cardinality. A missing optional
	// field can safely mean one; an explicit null/other value cannot.
	body := officeWordReplaceArgs{ExpectedCount: 1}
	var fields map[string]json.RawMessage
	if officeDecodeJSON(args, &body) != nil || json.Unmarshal(args, &fields) != nil {
		return body, officeFail(400, "INVALID_ARGUMENT", "word.replace 的参数必须放在 args 中，只接受 search、replacement、expectedReadId 或 expectedText，以及 expectedCount。")
	}
	_, hasText := fields["expectedText"]
	_, hasReadID := fields["expectedReadId"]
	if hasText == hasReadID {
		return body, officeFail(400, "INVALID_ARGUMENT", "先用 word.read 读取同一 document_session_id，再将成功回执顶部的 operationId 放入 args.expectedReadId；不能同时提供 expectedText。")
	}
	if hasReadID && (body.ExpectedReadID == nil || !officeOperationID.MatchString(*body.ExpectedReadID)) {
		return body, officeFail(400, "INVALID_ARGUMENT", "args.expectedReadId 必须是同一文档成功 word.read 回执顶部的 operationId，不能使用工具调用 ID。")
	}
	if hasText && (body.ExpectedText == nil || officeTextUnits(*body.ExpectedText) > 200000) {
		return body, officeFail(400, "INVALID_ARGUMENT", "args.expectedText 必须是 word.read 返回的完整原文，包括末尾换行；优先使用 args.expectedReadId。")
	}
	if body.Search == "" || officeTextUnits(body.Search) > 4000 {
		return body, officeFail(400, "INVALID_ARGUMENT", "args.search 必须是当前文档中唯一出现的非空原文片段，最多 4000 个 UTF-16 单位。")
	}
	replacement, hasReplacement := fields["replacement"]
	if !hasReplacement || bytes.Equal(bytes.TrimSpace(replacement), []byte("null")) || officeTextUnits(body.Replacement) > 4000 {
		return body, officeFail(400, "INVALID_ARGUMENT", "请明确提供 args.replacement 字符串（最多 4000 个 UTF-16 单位）；只有明确的空字符串才表示删除目标。")
	}
	if count, exists := fields["expectedCount"]; body.ExpectedCount != 1 || exists && bytes.Equal(bytes.TrimSpace(count), []byte("null")) {
		return body, officeFail(400, "INVALID_ARGUMENT", "args.expectedCount 只支持 1；每次只替换唯一目标，省略时固定为 1。")
	}
	if strings.ContainsFunc(body.Search+body.Replacement, func(r rune) bool { return r < 0x20 || r == '^' }) {
		return body, officeFail(400, "UNSUPPORTED_TEXT_SHAPE", "word.replace 当前只支持单段内唯一的纯文本片段；search/replacement 含换行、制表符、控制字符或 ^ 搜索序列，不能跨段替换。扩写现有多段内容时逐段替换，每次成功后重新 word.read，使用新回执和新操作 ID；此请求未执行。")
	}
	return body, nil
}

// JavaScript/ONLYOFFICE string bounds count UTF-16 code units, not UTF-8 bytes.
func officeTextUnits(text string) int {
	units := 0
	for _, r := range text {
		units++
		if r > 0xffff {
			units++
		}
	}
	return units
}

func officeValidateOperation(editor, name string, args json.RawMessage) error {
	if len(args) == 0 || len(args) > officeMaxJSON {
		return officeFail(400, "INVALID_ARGUMENT", "Office args 必须是有界对象。")
	}
	switch name {
	case "selection.read":
		if _, err := officeSelectionArguments(args); err != nil {
			return err
		}
	case "capabilities":
		if !officeEmptyArgs(args) {
			return officeFail(400, "INVALID_ARGUMENT", "该操作不接受额外参数。")
		}
	case "history.undo", "history.redo":
		if editor == "word" || officeEmptyArgs(args) {
			if !officeEmptyArgs(args) {
				return officeFail(400, "INVALID_ARGUMENT", "Word 历史操作不接受额外参数。")
			}
		} else {
			var body struct {
				Sheet string `json:"sheet"`
				Range string `json:"range"`
			}
			if officeDecodeJSON(args, &body) != nil || body.Sheet == "" || officeTextUnits(body.Sheet) > 128 {
				return officeFail(400, "INVALID_ARGUMENT", "Excel 历史观察范围参数不正确。")
			}
			if _, _, err := officeRangeSize(body.Range); err != nil {
				return err
			}
		}
	case "word.read":
		if editor != "word" {
			return officeFail(400, "WRONG_EDITOR", "当前 document_session_id 是 Excel，不能执行 word.read。请先 list，按 path/title 和 editor=word 选择目标文档。")
		}
		if !officeEmptyArgs(args) {
			return officeFail(400, "INVALID_ARGUMENT", "word.read 的 args 必须是空对象 {}。")
		}
	case "word.replace":
		if editor != "word" {
			return officeFail(400, "WRONG_EDITOR", "当前 document_session_id 是 Excel，不能执行 word.replace。请先 list，按 path/title 和 editor=word 选择目标文档。")
		}
		if _, err := officeWordReplaceArguments(args); err != nil {
			return err
		}
	case "sheet.list":
		if editor != "cell" {
			return officeFail(400, "WRONG_EDITOR", "当前 document_session_id 是 Word，不能列出工作表。请先 list，选择 editor=cell 的目标 Excel。")
		}
		if !officeEmptyArgs(args) {
			return officeFail(400, "INVALID_ARGUMENT", "sheet.list 的 args 必须是空对象 {}；返回当前编辑器的准确工作表名称。")
		}
	case "sheet.read", "sheet.apply":
		if editor != "cell" {
			return officeFail(400, "WRONG_EDITOR", "当前 document_session_id 是 Word，不能执行工作表操作。请先 list，按 path/title 和 editor=cell 选择目标 Excel。")
		}
		var body struct {
			Sheet          string              `json:"sheet"`
			Range          string              `json:"range"`
			ExpectedInputs [][]json.RawMessage `json:"expectedInputs,omitempty"`
			Inputs         [][]json.RawMessage `json:"inputs,omitempty"`
		}
		if officeDecodeJSON(args, &body) != nil || body.Sheet == "" || officeTextUnits(body.Sheet) > 128 {
			return officeFail(400, "INVALID_ARGUMENT", "工作表操作需要 args.sheet 的准确名称和 args.range 的有界 A1 范围；sheet.apply 还需 args.expectedInputs 与 args.inputs。")
		}
		rows, cols, err := officeRangeSize(body.Range)
		if err != nil {
			return err
		}
		if name == "sheet.read" {
			if body.ExpectedInputs != nil || body.Inputs != nil {
				return officeFail(400, "INVALID_ARGUMENT", "读取工作表不接受写入值。")
			}
		} else if !officeMatrix(body.ExpectedInputs, rows, cols) || !officeMatrix(body.Inputs, rows, cols) {
			return officeFail(400, "INVALID_ARGUMENT", "单元格输入必须为完整矩形，并提供原值条件。")
		}
	default:
		return officeFail(400, "UNSUPPORTED_OPERATION", "Office 桥只接受固定文档操作，不执行任意脚本。")
	}
	return nil
}

func officeRangeSize(value string) (int, int, error) {
	parts := strings.Split(value, ":")
	if len(parts) > 2 || len(parts) == 0 {
		return 0, 0, officeFail(400, "RANGE_LIMIT", "请使用有界 A1 范围。")
	}
	parse := func(cell string) (int, int, bool) {
		var col, row int
		at := 0
		for at < len(cell) && cell[at] >= 'A' && cell[at] <= 'Z' {
			col = col*26 + int(cell[at]-'A') + 1
			at++
		}
		if at == 0 || at > 3 || at == len(cell) || cell[at] == '0' {
			return 0, 0, false
		}
		for ; at < len(cell); at++ {
			if cell[at] < '0' || cell[at] > '9' {
				return 0, 0, false
			}
			row = row*10 + int(cell[at]-'0')
			if row > 1048576 {
				return 0, 0, false
			}
		}
		return row, col, row >= 1 && col >= 1 && col <= 16384
	}
	sr, sc, ok := parse(parts[0])
	if !ok {
		return 0, 0, officeFail(400, "RANGE_LIMIT", "请使用有界 A1 范围。")
	}
	er, ec := sr, sc
	if len(parts) == 2 {
		var valid bool
		er, ec, valid = parse(parts[1])
		if !valid {
			return 0, 0, officeFail(400, "RANGE_LIMIT", "请使用有界 A1 范围。")
		}
	}
	rows, cols := er-sr+1, ec-sc+1
	if rows < 1 || cols < 1 || rows*cols > 400 {
		return 0, 0, officeFail(400, "RANGE_LIMIT", "每次操作最多 400 个单元格。")
	}
	return rows, cols, nil
}

func officeMatrix(matrix [][]json.RawMessage, rows, cols int) bool {
	if len(matrix) != rows {
		return false
	}
	for _, row := range matrix {
		if len(row) != cols {
			return false
		}
		for _, raw := range row {
			var value any
			if json.Unmarshal(raw, &value) != nil {
				return false
			}
			switch v := value.(type) {
			case string:
				if officeTextUnits(v) > 4000 {
					return false
				}
			case float64, bool:
			default:
				return false
			}
		}
	}
	return true
}

func officeMutation(name string) bool {
	return name == "word.replace" || name == "sheet.apply" || name == "history.undo" || name == "history.redo"
}
func officeFingerprint(name string, args json.RawMessage) string {
	var decoded any
	_ = json.Unmarshal(args, &decoded)
	data, _ := json.Marshal(struct {
		Name string
		Args any
	}{name, decoded})
	return officeHash(data)
}

// Resolve a read receipt from the existing bounded, session-local ledger. The
// editor still receives the original full-text precondition and checks its
// current canvas atomically before making any change. No new snapshot cache or
// alternative mutation path is introduced.
func officeResolveReadReferenceLocked(d *officeDocumentSession, name string, args json.RawMessage) (json.RawMessage, error) {
	if name != "word.replace" {
		return args, nil
	}
	body, err := officeWordReplaceArguments(args)
	if err != nil {
		return nil, err
	}
	if body.ExpectedReadID == nil {
		return args, nil
	}
	id := *body.ExpectedReadID
	read := d.operations[id]
	var receipt struct {
		OK          bool   `json:"ok"`
		OperationID string `json:"operationId"`
		Result      struct {
			Kind string  `json:"kind"`
			Text *string `json:"text"`
		} `json:"result"`
	}
	if read == nil || read.name != "word.read" || !read.dispatched || !read.finished || len(read.result) > officeMaxJSON || json.Unmarshal(read.result, &receipt) != nil || !receipt.OK || receipt.OperationID != id || receipt.Result.Kind != "word" || receipt.Result.Text == nil || officeTextUnits(*receipt.Result.Text) > 200000 {
		return nil, officeFail(409, "INVALID_READ_REFERENCE", "请引用当前编辑会话中已成功完成的 Word 读取回执，再重试修改。")
	}
	body.ExpectedReadID = nil
	body.ExpectedText = receipt.Result.Text
	resolved, err := json.Marshal(body)
	if err != nil || len(resolved) > officeMaxJSON {
		return nil, officeFail(400, "INVALID_ARGUMENT", "Word 读取回执与替换文字超过单次操作上限。")
	}
	return resolved, nil
}

func (o *officeService) finishLocked(d *officeDocumentSession, op *officeOperation, result any) {
	if op.finished {
		return
	}
	encoded, err := json.Marshal(result)
	if err != nil || len(encoded) > officeMaxJSON {
		code, message := "RESPONSE_LIMIT", "编辑器回执超过单次操作上限。"
		if op.dispatched && (officeMutation(op.name) || op.name == "host.export") {
			officeMarkUnknownLocked(d, op)
			d.lastError = "操作已发出但回执超过上限，结果不能确认，不会自动重试。"
			code, message = "OUTCOME_UNKNOWN", d.lastError
		}
		encoded, _ = json.Marshal(officeErrorReply(officeFail(409, code, message)))
	}
	op.finished = true
	d.finishActivityLocked(op, encoded, "", "")
	if op.name == "host.export" {
		d.saving = false
		var outcome struct {
			OK bool `json:"ok"`
		}
		if json.Unmarshal(encoded, &outcome) == nil && outcome.OK {
			// Include the completed save receipt, rather than a stale queued
			// activity snapshot evaluated before the workspace commit finished.
			encoded, _ = json.Marshal(map[string]any{"ok": true, "session": d.viewLocked()})
		}
	}
	op.result = encoded
	d.reservedBytes -= op.resultReservation
	op.resultReservation = 0
	d.retainedBytes += len(op.result)
	if op.name == "host.export" {
		d.saving = false
		var outcome struct {
			OK bool `json:"ok"`
		}
		if json.Unmarshal(op.result, &outcome) != nil || !outcome.OK {
			d.saveState = "error"
		}
	}
	if op.timer != nil {
		op.timer.Stop()
	}
	if op.cancel != nil {
		op.cancel()
	}
	if err := o.persistEditorLocked(d); err != nil {
		op.result, _ = json.Marshal(officeErrorReply(err))
	}
	close(op.done)
	// A save is a barrier over the operations that were still pending when
	// admitted. A rejected/unknown predecessor must never become a successful
	// save merely because exporting the otherwise unchanged canvas worked.
	if barrier := d.exportIntent; barrier != nil && barrier != op && !barrier.dispatched && !barrier.finished {
		for _, predecessor := range barrier.prerequisites {
			if predecessor == op && !officeOperationSucceeded(op) {
				o.failOperationLocked(d, barrier, officeFail(409, "SAVE_PREDECESSOR_FAILED", "保存前的操作未能确认成功，当前画布仍保留；请核对后重新保存。"))
				break
			}
		}
	}
}

func officeOperationSucceeded(op *officeOperation) bool {
	var outcome struct {
		OK bool `json:"ok"`
	}
	return op.finished && json.Unmarshal(op.result, &outcome) == nil && outcome.OK
}

// Called under the document mutex immediately before the bridge dispatches.
// Dirty epochs belong to the exported canvas, rather than the earlier request
// that joined the queue. Human changes arriving after this point remain dirty.
func (o *officeService) prepareDispatchLocked(d *officeDocumentSession, op *officeOperation) error {
	if op.name == "selection.read" {
		return officeSelectionMatchesLocked(d, op.args)
	}
	if op.name != "host.export" {
		return nil
	}
	for _, predecessor := range op.prerequisites {
		if !officeOperationSucceeded(predecessor) {
			return officeFail(409, "SAVE_PREDECESSOR_FAILED", "保存前的操作未能确认成功，请核对当前画布后重新保存。")
		}
	}
	op.epoch = d.dirtyEpoch
	if op.timer != nil {
		op.timer.Stop()
	}
	// Queue waiting and exporting are separate bounded phases. Start the
	// actual export budget now, and fence a callback from the old queue timer.
	op.deadline = time.Now().Add(o.cfg.saveTimeout)
	deadline := op.deadline
	op.timer = time.AfterFunc(o.cfg.saveTimeout, func() { o.expireAt(d, op, deadline) })
	return nil
}

// Reserve the largest permitted response before admitting work. Completed
// saves share the normal ledger with commands; only one final save may use
// the extra slot, and no new work is admitted after that slot is used.
func officeReserveOperationLocked(d *officeDocumentSession, op *officeOperation, allowFinalSave bool) error {
	if d.finalSaveUsed {
		return officeFail(409, "OPERATION_LIMIT", "最终保存额度已使用，请核对保存状态后关闭并重新打开文档；未保存内容仍保留在画布中。")
	}
	required := len(op.args) + officeMaxJSON
	normal := len(d.operations) < officeMaxOperations && d.retainedBytes+d.reservedBytes+required <= officeMaxRetainedBytes
	if !normal {
		if !allowFinalSave || len(d.operations) >= officeMaxOperations+1 || d.retainedBytes+d.reservedBytes+required > officeMaxRetainedBytes+officeFinalSaveBytes {
			return officeFail(409, "OPERATION_LIMIT", "当前文档操作记录已达上限，请先保存并关闭，再重新打开。")
		}
		d.finalSaveUsed = true
	}
	op.resultReservation = officeMaxJSON
	d.reservedBytes += op.resultReservation
	d.retainedBytes += len(op.args)
	return nil
}

func (o *officeService) failOperationLocked(d *officeDocumentSession, op *officeOperation, err error) {
	o.finishLocked(d, op, officeErrorReply(err))
}

func (o *officeService) expire(d *officeDocumentSession, op *officeOperation) {
	d.mu.Lock()
	defer d.mu.Unlock()
	o.expireLocked(d, op)
}

func (o *officeService) expireAt(d *officeDocumentSession, op *officeOperation, deadline time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !op.deadline.Equal(deadline) {
		return
	}
	o.expireLocked(d, op)
}

func (o *officeService) expireLocked(d *officeDocumentSession, op *officeOperation) {
	if op.finished {
		return
	}
	if op.dispatched && !officeReadOnly(op.name) {
		officeMarkUnknownLocked(d, op)
		d.lastError = "上一步结果未知，不会重复执行。"
		o.failOperationLocked(d, op, officeFail(409, "OUTCOME_UNKNOWN", d.lastError))
	} else if op.dispatched {
		o.failOperationLocked(d, op, officeFail(409, "READ_TIMEOUT", "读取超时，未修改文档；连接恢复后可使用新操作 ID 重新读取。"))
	} else {
		o.failOperationLocked(d, op, officeFail(409, "NOT_DISPATCHED", "编辑器尚未接收操作，请检查连接后使用新的操作 ID。"))
	}
	if op.name == "host.export" {
		d.saving = false
		d.saveState = "error"
	}
}

func (o *officeService) wait(ctx context.Context, d *officeDocumentSession, op *officeOperation) (json.RawMessage, error) {
	select {
	case <-op.done:
		d.mu.Lock()
		result := append(json.RawMessage(nil), op.result...)
		if op.reconciled {
			var envelope map[string]any
			if json.Unmarshal(result, &envelope) == nil {
				envelope["reconciled"] = true
				envelope["recoveryResult"] = json.RawMessage(op.reconciliationResult)
				result, _ = json.Marshal(envelope)
			}
		}
		d.mu.Unlock()
		return result, nil
	case <-ctx.Done():
		d.mu.Lock()
		if op.name == "host.export" && op.saveShared {
			// This HTTP/model caller is only a waiter now. The document-owned
			// bounded deadline still protects the common export/commit.
			d.mu.Unlock()
			return nil, ctx.Err()
		}
		if !op.finished {
			if op.dispatched && !officeReadOnly(op.name) {
				officeMarkUnknownLocked(d, op)
				d.lastError = "操作已发出但结果未知，不会自动重试。"
				o.failOperationLocked(d, op, officeFail(409, "OUTCOME_UNKNOWN", d.lastError))
			} else if op.dispatched {
				o.failOperationLocked(d, op, officeFail(409, "READ_CANCELLED", "读取已取消，未修改文档；可使用新操作 ID 重新读取。"))
			} else {
				o.failOperationLocked(d, op, officeFail(409, "CANCELLED_NOT_DISPATCHED", "操作已取消，未发送到编辑器。"))
			}
			if op.name == "host.export" {
				d.saving = false
				d.saveState = "error"
			}
		}
		d.mu.Unlock()
		return nil, ctx.Err()
	}
}

func (o *officeService) command(ctx context.Context, d *officeDocumentSession, id, name string, args json.RawMessage) (json.RawMessage, error) {
	return o.commandAs(ctx, d, id, name, args, officeActivityAgent)
}

func (o *officeService) commandAs(ctx context.Context, d *officeDocumentSession, id, name string, args json.RawMessage, actor officeActivityActor) (reply json.RawMessage, err error) {
	if d.readOnly && name != "capabilities" && name != "selection.read" {
		return nil, officeFail(403, "READ_ONLY_DOCUMENT", "信息库文档以只读方式打开，只能读取当前选区。")
	}
	defer func() {
		if err != nil {
			d.rejectActivity(id, name, args, actor, err)
		}
	}()
	if !officeOperationID.MatchString(id) {
		return nil, officeFail(400, "INVALID_OPERATION_ID", "请提供 8 至 100 位字母、数字、下划线或短横线操作 ID。")
	}
	if err := officeValidateOperation(d.editor, name, args); err != nil {
		return nil, err
	}
	if name == "word.replace" {
		// Normalize before the request fingerprint and dispatch so omitted count
		// and explicit count=1 share exactly the same guarded request identity.
		body, _ := officeWordReplaceArguments(args)
		args, _ = json.Marshal(body)
	}
	fingerprint := officeFingerprint(name, args)
	o.observeDisk(d)
admit:
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil, officeFail(409, "SESSION_CLOSED", "编辑会话已关闭。")
	}
	if d.restarted || d.recoverySealed {
		d.mu.Unlock()
		return nil, officeFail(409, "EDITOR_FENCED", "原编辑连接已封存，请查看恢复副本或已保存版本。")
	}
	if op := d.operations[id]; op != nil {
		if op.fingerprint != fingerprint {
			d.mu.Unlock()
			return nil, officeFail(409, "OPERATION_ID_CONFLICT", "相同操作 ID 不能用于不同请求。")
		}
		d.mu.Unlock()
		return o.wait(ctx, d, op)
	}
	if d.freeze != nil || d.batchLease != "" {
		d.mu.Unlock()
		return nil, officeFail(409, "DOCUMENT_BATCH_ACTIVE", "文档正在切换版本，请等待完成。")
	}
	if d.externalChange && !officeReadOnly(name) {
		d.mu.Unlock()
		return nil, officeFail(409, "EXTERNAL_CHANGE", "磁盘文件已更新，请先核对并载入新版本。")
	}
	if d.replacementPending && !officeReadOnly(name) {
		d.mu.Unlock()
		return nil, officeFail(409, "RELOAD_UNVERIFIED", "新版本尚未完成画布验证，请稍候。")
	}
	if !d.ready {
		instanceBound := d.instanceID != ""
		d.mu.Unlock()
		if instanceBound {
			return nil, officeFail(409, "ENGINE_EXTENSION_REQUIRED", "编辑服务尚未加载已验证的 Pogo 文档历史扩展，Agent 操作暂不可用。")
		}
		return nil, officeFail(409, "EDITOR_NOT_READY", "请先在文件面板中打开当前文档，等待真实编辑器连接。")
	}
	if d.blocked && !officeReadOnly(name) {
		d.mu.Unlock()
		if err := o.waitRecovery(ctx, d); err != nil {
			return nil, err
		}
		goto admit
	}
	if d.saving && officeMutation(name) {
		d.mu.Unlock()
		return nil, officeFail(409, "SAVE_IN_PROGRESS", "当前文档正在保存。")
	}
	var cap officeCapabilities
	_ = json.Unmarshal(d.capabilities, &cap)
	if (name == "history.undo" && !cap.NativeUndo) || (name == "history.redo" && !cap.NativeRedo) || (name == "word.replace" && !cap.WordReplace) || (name == "sheet.apply" && !cap.SheetApply) || (name == "sheet.list" && !cap.SheetList) || (name == "selection.read" && !cap.SelectionRead) {
		d.mu.Unlock()
		return nil, officeFail(409, "UNSUPPORTED_CAPABILITY", "当前编辑器未确认支持这项 Agent 操作。")
	}
	if name == "selection.read" {
		if err := officeSelectionMatchesLocked(d, args); err != nil {
			d.mu.Unlock()
			return nil, err
		}
	}
	pending := 0
	for _, item := range d.queue {
		if !item.finished {
			pending++
		}
	}
	if pending >= 8 {
		d.mu.Unlock()
		return nil, officeFail(409, "OPERATION_PENDING", "当前文档已有待处理操作，请等待结果。")
	}
	args, err = officeResolveReadReferenceLocked(d, name, args)
	if err != nil {
		d.mu.Unlock()
		return nil, err
	}
	op := &officeOperation{id: id, name: name, args: append(json.RawMessage(nil), args...), fingerprint: fingerprint, done: make(chan struct{}), deadline: time.Now().Add(o.cfg.commandTimeout), requestContext: ctx}
	if err := officeReserveOperationLocked(d, op, false); err != nil {
		d.mu.Unlock()
		return nil, err
	}
	d.operations[id] = op
	d.queue = append(d.queue, op)
	d.addActivityLocked(op, actor)
	op.timer = time.AfterFunc(o.cfg.commandTimeout, func() { o.expire(d, op) })
	d.mu.Unlock()
	return o.wait(ctx, d, op)
}

func (o *officeService) save(ctx context.Context, d *officeDocumentSession, id string) (officeSessionView, error) {
	return o.saveAs(ctx, d, id, officeActivityAgent)
}

func (o *officeService) saveAs(ctx context.Context, d *officeDocumentSession, id string, actor officeActivityActor) (view officeSessionView, err error) {
	if d.readOnly {
		return officeSessionView{}, officeFail(403, "READ_ONLY_DOCUMENT", "信息库文档只读，不能保存或改写原文件。")
	}
	defer func() {
		if err != nil {
			d.rejectActivity(id, "host.export", json.RawMessage(`{}`), actor, err)
		}
	}()
	if id == "" {
		id = officeNewID()
	}
	if !officeOperationID.MatchString(id) {
		return officeSessionView{}, officeFail(400, "INVALID_OPERATION_ID", "保存操作 ID 不正确。")
	}
	if err := ctx.Err(); err != nil {
		return officeSessionView{}, err
	}
admit:
	d.mu.Lock()
	if d.restarted || d.recoverySealed {
		d.mu.Unlock()
		return officeSessionView{}, officeFail(409, "EDITOR_FENCED", "原编辑连接已封存，不能将未知画布标记为已保存。")
	}
	if previous := d.operations[id]; previous != nil {
		if previous.name != "host.export" {
			d.mu.Unlock()
			return officeSessionView{}, officeFail(409, "OPERATION_ID_CONFLICT", "操作 ID 已用于不同请求。")
		}
		if !previous.finished {
			previous.saveShared = true
			previous.requestContext = nil
		}
		d.mu.Unlock()
		return o.waitForSave(ctx, d, previous)
	}
	if d.blocked {
		d.mu.Unlock()
		if err := o.waitRecovery(ctx, d); err != nil {
			return officeSessionView{}, err
		}
		goto admit
	}
	if d.freeze != nil && (d.freeze.purpose != "batch" || d.freeze.phase != "freeze_requested") {
		d.mu.Unlock()
		return officeSessionView{}, officeFail(409, "DOCUMENT_BATCH_ACTIVE", "文档正在切换版本，不能保存旧画布。")
	}
	if d.replacementPending {
		d.mu.Unlock()
		return officeSessionView{}, officeFail(409, "RELOAD_UNVERIFIED", "新版本尚未完成画布验证。")
	}
	if d.closed || !d.ready || d.blocked {
		d.mu.Unlock()
		return officeSessionView{}, officeFail(409, "EDITOR_NOT_READY", "当前编辑会话不能确认保存，请保留画布并检查连接。")
	}
	if d.saving || d.exportBusy {
		previous := d.exportIntent
		if previous != nil && !previous.finished && (!previous.dispatched || d.dirtyEpoch == previous.epoch) {
			// Different request IDs for the same pending canvas share a single
			// export, but still consume bounded ledger identity space. There is
			// no second queue item, activity or retained response allocation.
			if d.finalSaveUsed || len(d.operations) >= officeMaxOperations || d.retainedBytes+d.reservedBytes+len(id) > officeMaxRetainedBytes {
				d.mu.Unlock()
				return officeSessionView{}, officeFail(409, "OPERATION_LIMIT", "当前文档操作记录已达上限，请等待本次保存完成。")
			}
			d.operations[id] = previous
			d.retainedBytes += len(id)
			previous.saveShared = true
			previous.requestContext = nil
			d.mu.Unlock()
			return o.waitForSave(ctx, d, previous)
		}
		d.mu.Unlock()
		return officeSessionView{}, officeFail(409, "SAVE_IN_PROGRESS", "当前文件正在保存；新增修改仍保留，请等本次保存完成后再次保存。")
	}
	prerequisites := []*officeOperation{}
	deadline := time.Now().Add(o.cfg.saveTimeout)
	for _, op := range d.queue {
		if !op.finished {
			prerequisites = append(prerequisites, op)
			// Bounded queue waiting gets the predecessor's remaining budget;
			// the export still has its configured budget once that work ends.
			if candidate := op.deadline.Add(o.cfg.saveTimeout); candidate.After(deadline) {
				deadline = candidate
			}
		}
	}
	if len(prerequisites) >= 8 {
		d.mu.Unlock()
		return officeSessionView{}, officeFail(409, "OPERATION_PENDING", "当前文档待处理操作已达上限，请等待结果后保存。")
	}
	args, _ := json.Marshal(map[string]any{"fileType": d.fileType})
	op := &officeOperation{id: id, name: "host.export", args: args, done: make(chan struct{}), prerequisites: prerequisites, fingerprint: officeFingerprint("host.export", args), deadline: deadline, requestContext: ctx}
	if err := officeReserveOperationLocked(d, op, true); err != nil {
		d.mu.Unlock()
		return officeSessionView{}, err
	}
	d.saving = true
	d.saveState = "saving"
	d.lastError = ""
	d.exportIntent = op
	d.operations[id] = op
	d.queue = append(d.queue, op)
	d.addActivityLocked(op, actor)
	op.timer = time.AfterFunc(time.Until(deadline), func() { o.expireAt(d, op, deadline) })
	d.mu.Unlock()
	return o.waitForSave(ctx, d, op)
}

func (o *officeService) waitForSave(ctx context.Context, d *officeDocumentSession, op *officeOperation) (officeSessionView, error) {
	result, err := o.wait(ctx, d, op)
	if err != nil {
		return officeSessionView{}, err
	}
	var envelope struct {
		OK      bool              `json:"ok"`
		Session officeSessionView `json:"session"`
		Error   struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(result, &envelope) != nil {
		return officeSessionView{}, officeFail(409, "SAVE_FAILED", "当前文件未能写回，编辑器内容仍保留。")
	}
	if !envelope.OK {
		if envelope.Error.Code != "" && envelope.Error.Message != "" {
			return officeSessionView{}, officeFail(409, envelope.Error.Code, envelope.Error.Message)
		}
		return officeSessionView{}, officeFail(409, "SAVE_FAILED", "当前文件未能写回，编辑器内容仍保留。")
	}
	return envelope.Session, nil
}

func (o *officeService) exportURL(value, browserOrigin string) (*url.URL, error) {
	u, err := url.Parse(value)
	if err != nil || u.User != nil || u.Fragment != "" || u.Opaque != "" {
		return nil, officeFail(403, "EXPORT_ORIGIN_REJECTED", "导出地址不属于配置的编辑服务。")
	}
	relative := u.Scheme == "" && u.Host == "" && strings.HasPrefix(u.Path, officeGatewayPrefix+"/")
	if !relative && (u.Host == "" || (u.Scheme != "http" && u.Scheme != "https")) {
		return nil, officeFail(403, "EXPORT_ORIGIN_REJECTED", "导出地址不属于配置的编辑服务。")
	}
	origin := u.Scheme + "://" + u.Host
	public := relative || (browserOrigin != "" && origin == browserOrigin && origin != o.cfg.serviceOrigin)
	if (relative && browserOrigin == "") || (!public && origin != o.cfg.serviceOrigin) {
		return nil, officeFail(403, "EXPORT_ORIGIN_REJECTED", "导出地址不属于当前编辑器来源。")
	}
	// The public Office engine lives under its own gateway namespace. Never
	// turn another Pogo API or a look-alike prefix into an internal fetch.
	// Internal engine URLs already have the upstream path and remain valid.
	if strings.ContainsAny(u.Path, "\\\x00\r\n") || strings.Contains(strings.ToLower(u.EscapedPath()), "%2f") || strings.Contains(strings.ToLower(u.EscapedPath()), "%5c") {
		return nil, officeFail(403, "EXPORT_ORIGIN_REJECTED", "导出地址路径不正确。")
	}
	for _, part := range strings.Split(u.Path, "/") {
		if part == "." || part == ".." {
			return nil, officeFail(403, "EXPORT_ORIGIN_REJECTED", "导出地址路径不正确。")
		}
	}
	if public {
		if !strings.HasPrefix(u.Path, officeGatewayPrefix+"/") {
			return nil, officeFail(403, "EXPORT_ORIGIN_REJECTED", "导出地址不属于编辑器网关。")
		}
		u.Path = strings.TrimPrefix(u.Path, officeGatewayPrefix)
		u.RawPath = ""
	}
	target, _ := url.Parse(o.cfg.serviceURL)
	u.Scheme, u.Host = target.Scheme, target.Host
	return u, nil
}

func (o *officeService) exportCurrent(ctx context.Context, d *officeDocumentSession, instance, id, fileType, address string) (err error) {
	d.mu.Lock()
	op := d.exportIntent
	if d.closed || !d.saving || d.exportBusy || instance != d.instanceID || op == nil || op.id != id || !op.dispatched || op.finished {
		d.mu.Unlock()
		return officeFail(409, "STALE_EXPORT", "导出回执不属于当前在途保存。")
	}
	if fileType != d.fileType {
		o.failOperationLocked(d, op, officeFail(400, "WRONG_FORMAT", "导出格式与当前文档不一致。"))
		d.mu.Unlock()
		return officeFail(400, "WRONG_FORMAT", "导出格式与当前文档不一致。")
	}
	// The viewer origin belongs to this exact instance. Never accept an
	// arbitrary callback origin or fetch a tunneled client localhost address.
	source, sourceErr := o.exportURL(address, d.browserOrigin)
	if sourceErr != nil {
		o.failOperationLocked(d, op, sourceErr)
		d.mu.Unlock()
		return sourceErr
	}
	// An engine origin is shared by many documents. An otherwise valid URL
	// must still refer to this canvas, never another account's cached export.
	if !officeExportDocumentPath(source.Path, d.key) {
		err := officeFail(403, "EXPORT_DOCUMENT_REJECTED", "导出文件不属于当前文档。")
		o.failOperationLocked(d, op, err)
		d.mu.Unlock()
		return err
	}
	fetchCtx, cancel := context.WithCancel(ctx)
	op.cancel = cancel
	op.fetchContext = fetchCtx
	d.exportBusy = true
	d.mu.Unlock()
	defer func() {
		cancel()
		d.mu.Lock()
		defer d.mu.Unlock()
		d.exportBusy = false
		if persistErr := o.persistEditorLocked(d); err == nil && persistErr != nil {
			err = persistErr
		}
	}()
	request, err := http.NewRequestWithContext(fetchCtx, http.MethodGet, source.String(), nil)
	if err == nil {
		var response *http.Response
		response, err = o.cfg.client.Do(request)
		if err == nil {
			defer response.Body.Close()
			if response.StatusCode != 200 {
				err = officeFail(502, "EXPORT_FETCH_FAILED", "无法获取当前编辑器导出的文件。")
			} else {
				var data []byte
				data, err = io.ReadAll(io.LimitReader(response.Body, officeMaxBytes+1))
				if err == nil && len(data) > officeMaxBytes {
					err = officeFail(413, "DOCUMENT_LIMIT", "导出文件超过 20 MiB。")
				}
				if err == nil {
					err = officeCheckPackage(data, d.fileType)
				}
				if err == nil {
					err = o.commitExport(d, op, instance, data)
				}
			}
		}
	}
	if err != nil {
		d.mu.Lock()
		if !op.finished {
			d.saving = false
			d.saveState = "error"
			d.lastError = "保存未能确认，当前画布内容仍保留。"
			o.failOperationLocked(d, op, err)
		}
		d.mu.Unlock()
	}
	return err
}

func officeExportDocumentPath(resourcePath, documentKey string) bool {
	parts := strings.Split(strings.TrimPrefix(resourcePath, "/"), "/")
	if len(parts) < 4 || parts[0] != "cache" || parts[1] != "files" {
		return false
	}
	index := 2
	if parts[index] == "data" {
		index++
	}
	return len(parts) > index+1 && officeEngineFileKeyMatches(parts[index], documentKey)
}

func (o *officeService) commitExport(d *officeDocumentSession, op *officeOperation, instance string, data []byte) error {
	return tools.WithFileMutationLock(d.absolute, func() error {
		o.mu.Lock()
		d.mu.Lock()
		defer d.mu.Unlock()
		if o.closed || d.closed || o.active[d.absolute] != d.id || d.exportIntent != op || op.finished || !d.saving || d.instanceID != instance || officeOperationExpired(op) {
			o.mu.Unlock()
			return officeFail(409, "STALE_EXPORT", "导出已过期，文件不会写回。")
		}
		// The document/file locks fence this commit. Avoid holding the service
		// registry mutex over filesystem and journal I/O for unrelated files.
		o.mu.Unlock()
		currentParent, err := os.Stat(filepath.Dir(d.absolute))
		if err != nil || !os.SameFile(currentParent, d.parentInfo) {
			return officeFail(409, "FILE_IDENTITY_CHANGED", "文档目录已移动或替换，保存不会覆盖。")
		}
		previous, info, err := officeReadFile(d.parent, filepath.Base(d.relative))
		if err != nil {
			return officeFail(409, "FILE_IDENTITY_CHANGED", "文档身份已改变，保存不会覆盖。")
		}
		if !os.SameFile(info, d.baseInfo) || officeHash(previous) != d.baseHash {
			return officeFail(409, "FILE_CHANGED", "文件已被其他操作修改，当前画布不会覆盖新文件。")
		}
		temp := ".pogo-office-" + randomID(16) + ".tmp"
		f, err := d.parent.OpenFile(temp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, info.Mode().Perm())
		if err != nil {
			return err
		}
		defer d.parent.Remove(temp)
		_, writeErr := f.Write(data)
		chmodErr := f.Chmod(info.Mode().Perm())
		syncErr := f.Sync()
		newInfo, statErr := f.Stat()
		closeErr := f.Close()
		if err := errors.Join(writeErr, chmodErr, syncErr, statErr, closeErr); err != nil {
			return err
		}
		versionScope := officeVersionScopeForDocument(d)
		versionID, err := o.beginVersion(versionScope, op.id, "", previous, data, info, newInfo, officeNativeVersionMetadata(d, op))
		if err != nil {
			return err
		}
		if op.finished || d.closed || officeOperationExpired(op) {
			return officeFail(409, "STALE_EXPORT", "保存已经过期。")
		}
		if d.journal != nil {
			d.journal.BeforeChange(d.absolute, previous, true)
		}
		// Journal I/O may block past a deadline while timer/cancellation
		// callbacks are waiting for this mutex. Inspect their original clocks
		// and contexts again rather than relying on callback scheduling.
		currentParent, err = os.Stat(filepath.Dir(d.absolute))
		if err != nil || !os.SameFile(currentParent, d.parentInfo) {
			return officeFail(409, "FILE_IDENTITY_CHANGED", "文档目录在保存提交期间移动或替换，文件不会写回。")
		}
		latest, latestInfo, err := officeReadFile(d.parent, filepath.Base(d.relative))
		if err != nil || !os.SameFile(latestInfo, d.baseInfo) || officeHash(latest) != d.baseHash {
			return officeFail(409, "FILE_CHANGED", "文件在保存提交期间改变，当前画布不会覆盖它。")
		}
		if op.finished || d.closed || officeOperationExpired(op) {
			return officeFail(409, "STALE_EXPORT", "保存已经过期，文件不会写回。")
		}
		if err := d.parent.Rename(temp, filepath.Base(d.relative)); err != nil {
			return err
		}
		// Both snapshots and the version intent are already durable. Confirm the
		// version only after the file's atomic publication; an interrupted marker
		// is recovered from the exact hash and inode, not a guessed tool result.
		if dir, openErr := d.parent.Open("."); openErr == nil {
			if syncErr := dir.Sync(); syncErr == nil {
				_ = o.finishVersion(versionScope, versionID)
			}
			dir.Close()
		}
		d.baseInfo = newInfo
		d.baseHash = officeHash(data)
		d.diskVersion, d.externalChange, d.versionError = d.baseHash, false, ""
		d.syncRevision++
		d.snapshot = append(d.snapshot[:0], data...)
		d.revision++
		op.committedRevision = d.revision
		d.savedAt = time.Now().UTC().Format(time.RFC3339Nano)
		d.lastError = ""
		if op.epoch > d.persistedEpoch {
			d.persistedEpoch = op.epoch
		}
		d.saveState = "saved"
		if d.dirtyEpoch > d.persistedEpoch {
			d.saveState = "dirty"
		}
		d.saving = false
		o.finishLocked(d, op, map[string]any{"ok": true, "session": d.viewLocked()})
		return nil
	})
}

func officeOperationExpired(op *officeOperation) bool {
	return time.Now().After(op.deadline) || (op.fetchContext != nil && op.fetchContext.Err() != nil) || (op.requestContext != nil && op.requestContext.Err() != nil)
}

func (o *officeService) closeDocument(d *officeDocumentSession) error {
	return o.closeDocumentInstance(d, "")
}

func (o *officeService) closeDocumentInstance(d *officeDocumentSession, instance string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	d.mu.Lock()
	defer d.mu.Unlock()
	if instance != "" && instance != d.instanceID {
		return officeFail(409, "EDITOR_INSTANCE_MISMATCH", "该编辑器实例不拥有当前文档。")
	}
	if d.closed {
		return nil
	}
	if d.readOnly {
		o.retireLocked(d)
		return nil
	}
	if err := officeCanCloseLocked(d); err != nil {
		return err
	}
	d.closed = true
	if err := o.persistEditorLocked(d); err != nil {
		d.closed = false
		return err
	}
	d.closed = false
	o.retireLocked(d)
	return nil
}

func officeCanCloseLocked(d *officeDocumentSession) error {
	pending := false
	for _, op := range d.queue {
		if !op.finished {
			pending = true
			break
		}
	}
	if d.dirtyEpoch > d.persistedEpoch || d.saving || d.exportBusy || pending || d.blocked || d.freeze != nil || d.batchLease != "" {
		return officeFail(409, "UNSAVED_DOCUMENT", "当前会话有未保存修改或未知操作，请先保存并核对。")
	}
	return nil
}

// Call with the service and document locks in that order. This never calls
// Manager: delete/eviction hooks may already hold its reservation mutex.
func (o *officeService) retireLocked(d *officeDocumentSession) {
	if d.viewTimer != nil {
		d.viewTimer.Stop()
		d.viewTimer = nil
	}
	if d.readOnly && !d.closed {
		for key, receipt := range o.viewReceipts {
			if receipt.document == d {
				receipt.expires = time.Now().Add(informationOfficeViewIdle)
				o.viewReceipts[key] = receipt
			}
		}
	}
	d.notifyRecoveryLocked()
	if d.closed {
		if o.active[d.absolute] == d.id {
			delete(o.active, d.absolute)
		}
		return
	}
	pendingMutation := false
	for _, op := range d.queue {
		if !op.finished && !officeReadOnly(op.name) {
			pendingMutation = true
			break
		}
	}
	if !o.closed && !d.retirementAuthorized && (d.dirtyEpoch > d.persistedEpoch || d.blocked || d.saving || d.exportBusy || pendingMutation) {
		// Manager eviction/shutdown and a lost browser are not user consent to
		// discard a canvas. Revoke its live privileges while retaining an orphan
		// in the active map, so another opener cannot silently replace it.
		d.restarted = true
		d.ready = false
		if o.gateway != nil {
			o.gateway.revokeDocument(d.key)
		}
		if err := o.persistEditorLocked(d); err != nil {
			o.editorJournalError = true
		}
		for _, op := range d.operations {
			if !op.finished {
				if op.dispatched && !officeReadOnly(op.name) {
					officeMarkUnknownLocked(d, op)
				}
				o.failOperationLocked(d, op, officeFail(409, "CONVERSATION_CLOSED", "原编辑连接已撤销，上一步结果需要恢复。"))
			}
		}
		return
	}
	d.closed = true
	if !o.closed {
		if err := o.persistEditorLocked(d); err != nil {
			o.editorJournalError = true
		} else {
			o.compactCleanEditorRecordLocked(d)
		}
	}
	if o.gateway != nil {
		o.gateway.revokeDocument(d.key)
	}
	d.ready = false
	d.releaseGrant = [32]byte{}
	for _, op := range d.operations {
		if !op.finished {
			o.failOperationLocked(d, op, officeFail(409, "CONVERSATION_CLOSED", "当前文档操作身份已撤销，请保留画布内容。"))
		}
	}
	if d.parent != nil {
		d.parent.Close()
	}
	d.snapshot = nil
	d.informationSnapshot.retire()
	d.informationSnapshot = nil
	d.operations = nil
	d.queue = nil
	d.exportIntent = nil
	d.capabilities = nil
	if !o.closed && !d.journalFrozen {
		o.compactCleanEditorRecordLocked(d)
	}
	if o.active[d.absolute] == d.id {
		delete(o.active, d.absolute)
	}
	o.tombstones = append(o.tombstones, d.id)
	for len(o.tombstones) > 64 {
		delete(o.sessions, o.tombstones[0])
		o.tombstones = o.tombstones[1:]
	}
}

func (o *officeService) beforeDeleteConversation(id string) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	documents := []*officeDocumentSession{}
	for _, d := range o.sessions {
		if d.conversationID == id {
			documents = append(documents, d)
		}
	}
	sort.Slice(documents, func(i, j int) bool { return documents[i].generation < documents[j].generation })
	for _, d := range documents {
		d.mu.Lock()
	}
	defer func() {
		for index := len(documents) - 1; index >= 0; index-- {
			documents[index].mu.Unlock()
		}
	}()
	for _, d := range documents {
		if !d.closed {
			if err := officeCanCloseLocked(d); err != nil {
				return err
			}
		}
	}
	if role := o.roles[id]; role != nil {
		role.revoked.Store(true)
		delete(o.roles, id)
	}
	for _, d := range documents {
		o.retireLocked(d)
	}
	return nil
}

func (o *officeService) revokeConversation(id string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if role := o.roles[id]; role != nil {
		role.revoked.Store(true)
		delete(o.roles, id)
	}
	documents := []*officeDocumentSession{}
	for _, d := range o.sessions {
		if d.conversationID == id {
			documents = append(documents, d)
		}
	}
	for _, d := range documents {
		d.mu.Lock()
		o.retireLocked(d)
		d.mu.Unlock()
	}
}

func (o *officeService) conversationActive(id string) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, d := range o.sessions {
		if d.conversationID == id {
			d.mu.Lock()
			active := !d.closed
			d.mu.Unlock()
			if active {
				return true
			}
		}
	}
	return false
}

func (o *officeService) close() {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return
	}
	o.checkpointEditorsBeforeShutdownLocked()
	o.closed = true
	for _, role := range o.roles {
		role.revoked.Store(true)
	}
	o.roles = nil
	documents := []*officeDocumentSession{}
	for _, d := range o.sessions {
		documents = append(documents, d)
	}
	for _, d := range documents {
		d.mu.Lock()
		o.retireLocked(d)
		d.mu.Unlock()
	}
}
