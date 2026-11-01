package web

// Activity is a bounded display projection of the existing operation ledger.
// It never drives editor changes, performs another read, or retains document
// snapshots. Actor identity comes from the host entry point, not tool args.
import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const officeActivityLimit = 20
const officeActivityChangeLimit = 6
const officeActivityTextLimit = 160

type officeActivityActor string

const (
	officeActivityAgent officeActivityActor = "agent"
	officeActivityUser  officeActivityActor = "user"
)

type officeActivityTarget struct {
	Kind  string `json:"kind"`
	Sheet string `json:"sheet,omitempty"`
	Range string `json:"range,omitempty"`
}

type officeActivityChange struct {
	Address string `json:"address,omitempty"`
	Before  string `json:"before"`
	After   string `json:"after"`
}

type officeActivity struct {
	Sequence    uint64                 `json:"sequence"`
	OperationID string                 `json:"operationId"`
	Operation   string                 `json:"operation"`
	Actor       officeActivityActor    `json:"actor"`
	Phase       string                 `json:"phase"`
	Status      string                 `json:"status"`
	Target      officeActivityTarget   `json:"target"`
	QueuedAt    string                 `json:"queuedAt"`
	StartedAt   string                 `json:"startedAt,omitempty"`
	FinishedAt  string                 `json:"finishedAt,omitempty"`
	Changes     []officeActivityChange `json:"changes,omitempty"`
	ChangeCount *int                   `json:"changeCount,omitempty"`
	Truncated   bool                   `json:"truncated,omitempty"`
	Verified    bool                   `json:"verified,omitempty"`
	Code        string                 `json:"code,omitempty"`
	Message     string                 `json:"message,omitempty"`
	Revision    uint64                 `json:"revision,omitempty"`
	// A dirty event from a human or another command invalidates the narrow
	// write-followed-by-read association. This epoch is never sent to the UI.
	dirtyEpoch        uint64
	verificationOf    string
	verificationEpoch uint64
}

type officeActivityView struct {
	Version uint64           `json:"version"`
	Items   []officeActivity `json:"items"`
}

func officeActivityText(text string, limit int) (string, bool) {
	if utf8.RuneCountInString(text) <= limit {
		return strings.Clone(text), false
	}
	end, count := 0, 0
	for index := range text {
		if count == limit {
			end = index
			break
		}
		count++
	}
	return strings.Clone(text[:end]) + "…", true
}

func (d *officeDocumentSession) activityViewLocked() officeActivityView {
	items := make([]officeActivity, len(d.activity))
	copy(items, d.activity)
	for i := range items {
		items[i].Changes = append([]officeActivityChange(nil), items[i].Changes...)
		if items[i].ChangeCount != nil {
			count := *items[i].ChangeCount
			items[i].ChangeCount = &count
		}
	}
	return officeActivityView{Version: d.activityVersion, Items: items}
}

func officeActivityTargetFor(editor, name string, args json.RawMessage) officeActivityTarget {
	target := officeActivityTarget{Kind: editor}
	if name == "sheet.read" || name == "sheet.apply" || strings.HasPrefix(name, "history.") {
		var body struct {
			Sheet string `json:"sheet"`
			Range string `json:"range"`
		}
		if json.Unmarshal(args, &body) == nil && officeTextUnits(body.Sheet) <= 128 {
			if _, _, err := officeRangeSize(body.Range); err != nil {
				return target
			}
			target.Sheet, target.Range = body.Sheet, body.Range
		}
	}
	return target
}

func officeActivityPhase(name string) string {
	switch name {
	case "word.replace", "sheet.apply":
		return "writing"
	case "host.export":
		return "saving"
	case "history.undo", "history.redo":
		return "history"
	default:
		return "reading"
	}
}

func (d *officeDocumentSession) addActivityLocked(op *officeOperation, actor officeActivityActor) {
	if actor != officeActivityUser {
		actor = officeActivityAgent
	}
	d.activityVersion++
	op.activitySequence = d.activityVersion
	item := officeActivity{Sequence: op.activitySequence, OperationID: op.id, Operation: op.name,
		Actor: actor, Phase: officeActivityPhase(op.name), Status: "queued", Target: officeActivityTargetFor(d.editor, op.name, op.args),
		QueuedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	if op.name == "sheet.read" && len(d.activity) > 0 {
		// No guessed cross-sheet or full-document verification: only the most
		// recent confirmed edit at exactly this live read scope is associated.
		for _, previous := range d.activity {
			if previous.Phase == "writing" || previous.Phase == "history" {
				if previous.Operation == "sheet.apply" && previous.Status == "succeeded" && previous.Verified &&
					previous.Target == item.Target && previous.dirtyEpoch == d.dirtyEpoch {
					item.Phase = "verifying"
					item.verificationOf = previous.OperationID
					item.verificationEpoch = d.dirtyEpoch
				}
				break
			}
		}
	}
	if len(d.activity) >= officeActivityLimit {
		// Keep in-flight actions even when invalid requests flood the editor.
		for i := len(d.activity) - 1; i >= 0; i-- {
			if d.activity[i].Status != "queued" && d.activity[i].Status != "running" {
				d.activity = append(d.activity[:i], d.activity[i+1:]...)
				break
			}
		}
	}
	d.activity = append([]officeActivity{item}, d.activity...)
	if len(d.activity) > officeActivityLimit {
		d.activity = d.activity[:officeActivityLimit]
	}
}

func (d *officeDocumentSession) activityLocked(op *officeOperation) *officeActivity {
	for i := range d.activity {
		if d.activity[i].Sequence == op.activitySequence {
			return &d.activity[i]
		}
	}
	return nil
}

func (d *officeDocumentSession) startActivityLocked(op *officeOperation) {
	if item := d.activityLocked(op); item != nil && item.Status == "queued" {
		item.Status, item.StartedAt = "running", time.Now().UTC().Format(time.RFC3339Nano)
		d.activityVersion++
	}
}

func (d *officeDocumentSession) rejectActivity(id, name string, args json.RawMessage, actor officeActivityActor, err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return
	}
	if !officeOperationID.MatchString(id) {
		id = officeNewID()
	}
	switch name {
	case "capabilities", "word.read", "word.replace", "sheet.list", "sheet.read", "selection.read", "sheet.apply", "history.undo", "history.redo", "host.export":
	default:
		name = "command"
	}
	// Invalid retries cannot overwrite a previously admitted operation's UI.
	if d.operations[id] != nil {
		return
	}
	op := &officeOperation{id: id, name: name, args: args}
	d.addActivityLocked(op, actor)
	var typed *officeError
	code, message := "OFFICE_ERROR", "Office 操作未发送到编辑器。"
	if errors.As(err, &typed) {
		code, message = typed.code, typed.message
	}
	d.finishActivityLocked(op, json.RawMessage(`{"ok":false}`), code, message)
}

func (d *officeDocumentSession) finishActivityLocked(op *officeOperation, encoded json.RawMessage, code, message string) {
	item := d.activityLocked(op)
	if item == nil {
		return
	}
	var envelope struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
		Error  struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(encoded, &envelope) != nil {
		envelope.OK = false
		code = "OUTCOME_UNKNOWN"
	}
	item.FinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
	item.Status = "rejected"
	if envelope.OK {
		item.Status = "succeeded"
		if op.name == "host.export" {
			item.Verified, item.Revision = op.committedRevision > 0, op.committedRevision
		}
		if op.name == "word.replace" || op.name == "sheet.apply" {
			officeActivityMutation(item, op, envelope.Result)
		}
		if item.Phase == "verifying" {
			item.Verified = d.verifyActivityReadLocked(item, envelope.Result)
			if !item.Verified {
				item.Phase = "reading"
			}
		}
		item.dirtyEpoch = d.dirtyEpoch
	} else {
		if code == "" {
			code, message = envelope.Error.Code, envelope.Error.Message
		}
		if code == "OUTCOME_UNKNOWN" || code == "EDITOR_OUTCOME_UNKNOWN" || (op.dispatched && code == "CONVERSATION_CLOSED") {
			item.Status = "unknown"
		}
		item.Code, _ = officeActivityText(code, 128)
		// Never leak a bridge/file credential through a display receipt.
		if strings.Contains(message, d.bridgeToken) && d.bridgeToken != "" || strings.Contains(message, d.fileToken) && d.fileToken != "" {
			message = "编辑器操作失败，请检查当前文档状态。"
		}
		item.Message, _ = officeActivityText(message, 240)
	}
	d.activityVersion++
}

func (d *officeDocumentSession) verifyActivityReadLocked(item *officeActivity, result json.RawMessage) bool {
	// A snapshot may have been read before a human dirty event or native
	// history action, but its bridge receipt can arrive afterwards. Recheck
	// the association at completion, under the same document mutex as state.
	if item.verificationEpoch != d.dirtyEpoch {
		return false
	}
	applicable := false
	for _, candidate := range d.activity {
		if candidate.Phase == "writing" || candidate.Phase == "history" {
			applicable = candidate.OperationID == item.verificationOf && candidate.Operation == "sheet.apply" &&
				candidate.Status == "succeeded" && candidate.Verified && candidate.dirtyEpoch == item.verificationEpoch
			break
		}
	}
	if !applicable {
		return false
	}
	previous := d.operations[item.verificationOf]
	if previous == nil || !previous.finished || previous.name != "sheet.apply" {
		return false
	}
	var receipt struct {
		Result struct {
			After json.RawMessage `json:"after"`
		} `json:"result"`
	}
	var current, after struct {
		Kind   string              `json:"kind"`
		Sheet  string              `json:"sheet"`
		Range  string              `json:"range"`
		Inputs [][]json.RawMessage `json:"inputs"`
	}
	if json.Unmarshal(previous.result, &receipt) != nil || json.Unmarshal(receipt.Result.After, &after) != nil || json.Unmarshal(result, &current) != nil ||
		current.Kind != "cell" || after.Kind != "cell" || current.Sheet != item.Target.Sheet || current.Range != item.Target.Range || after.Sheet != current.Sheet || after.Range != current.Range {
		return false
	}
	_, _, rows, cols, ok := officeActivityRange(item.Target.Range)
	if !ok || len(current.Inputs) != rows || len(after.Inputs) != rows {
		return false
	}
	for r := 0; r < rows; r++ {
		if len(current.Inputs[r]) != cols || len(after.Inputs[r]) != cols {
			return false
		}
		for c := 0; c < cols; c++ {
			observed, observedOK := officeActivityScalar(current.Inputs[r][c])
			expected, expectedOK := officeActivityScalar(after.Inputs[r][c])
			if !observedOK || !expectedOK || observed != expected {
				return false
			}
		}
	}
	return true
}

func officeActivityMutation(item *officeActivity, op *officeOperation, result json.RawMessage) {
	var receipt struct {
		Before json.RawMessage `json:"before"`
		After  json.RawMessage `json:"after"`
	}
	if json.Unmarshal(result, &receipt) != nil {
		return
	}
	if op.name == "word.replace" {
		var before, after struct {
			Kind string  `json:"kind"`
			Text *string `json:"text"`
		}
		var args officeWordReplaceArgs
		if json.Unmarshal(receipt.Before, &before) != nil || json.Unmarshal(receipt.After, &after) != nil || json.Unmarshal(op.args, &args) != nil ||
			before.Kind != "word" || after.Kind != "word" || before.Text == nil || after.Text == nil || args.ExpectedText == nil || *before.Text != *args.ExpectedText || args.Search == "" || strings.Count(*before.Text, args.Search) != 1 ||
			strings.Replace(*before.Text, args.Search, args.Replacement, 1) != *after.Text {
			return
		}
		index := strings.Index(*before.Text, args.Search)
		old, oldCut := officeActivityText((*before.Text)[index:index+len(args.Search)], officeActivityTextLimit)
		updated, newCut := officeActivityText((*after.Text)[index:index+len(args.Replacement)], officeActivityTextLimit)
		count := 0
		if *before.Text != *after.Text {
			count = 1
			item.Changes = []officeActivityChange{{Before: old, After: updated}}
		}
		item.ChangeCount, item.Verified, item.Truncated = &count, true, oldCut || newCut
		return
	}
	var before, after struct {
		Kind   string              `json:"kind"`
		Sheet  string              `json:"sheet"`
		Range  string              `json:"range"`
		Inputs [][]json.RawMessage `json:"inputs"`
	}
	var args struct {
		Inputs         [][]json.RawMessage `json:"inputs"`
		ExpectedInputs [][]json.RawMessage `json:"expectedInputs"`
	}
	if json.Unmarshal(receipt.Before, &before) != nil || json.Unmarshal(receipt.After, &after) != nil || json.Unmarshal(op.args, &args) != nil ||
		before.Kind != "cell" || after.Kind != "cell" || before.Sheet != item.Target.Sheet || after.Sheet != item.Target.Sheet || before.Range != item.Target.Range || after.Range != item.Target.Range {
		return
	}
	row, col, rows, cols, ok := officeActivityRange(item.Target.Range)
	if !ok || len(before.Inputs) != rows || len(after.Inputs) != rows || len(args.Inputs) != rows || len(args.ExpectedInputs) != rows {
		return
	}
	count := 0
	changes := []officeActivityChange{}
	truncated := false
	for r := 0; r < rows; r++ {
		if len(before.Inputs[r]) != cols || len(after.Inputs[r]) != cols || len(args.Inputs[r]) != cols || len(args.ExpectedInputs[r]) != cols {
			return
		}
		for c := 0; c < cols; c++ {
			old, okOld := officeActivityScalar(before.Inputs[r][c])
			updated, okNew := officeActivityScalar(after.Inputs[r][c])
			expected, okExpected := officeActivityScalar(args.Inputs[r][c])
			original, okOriginal := officeActivityScalar(args.ExpectedInputs[r][c])
			if !okOld || !okNew || !okExpected || !okOriginal || old != original || updated != expected {
				return
			}
			if old == updated {
				continue
			}
			count++
			if len(changes) < officeActivityChangeLimit {
				old, oldCut := officeActivityText(old, officeActivityTextLimit)
				updated, newCut := officeActivityText(updated, officeActivityTextLimit)
				changes = append(changes, officeActivityChange{Address: officeActivityCell(row+r, col+c), Before: old, After: updated})
				truncated = truncated || oldCut || newCut
			}
		}
	}
	item.ChangeCount, item.Changes, item.Verified, item.Truncated = &count, changes, true, truncated || count > len(changes)
}

func officeActivityScalar(raw json.RawMessage) (string, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return "", false
	}
	switch typed := value.(type) {
	case string:
		return typed, officeTextUnits(typed) <= 4000
	case json.Number:
		value, err := typed.Float64()
		if err != nil || math.IsInf(value, 0) || math.IsNaN(value) {
			return "", false
		}
		return strconv.FormatFloat(value, 'g', -1, 64), true
	case bool:
		return strconv.FormatBool(typed), true
	}
	return "", false
}

func officeActivityRange(value string) (row, col, rows, cols int, ok bool) {
	parts := strings.Split(value, ":")
	if len(parts) < 1 || len(parts) > 2 {
		return
	}
	parse := func(cell string) (int, int, bool) {
		i, column := 0, 0
		for i < len(cell) && cell[i] >= 'A' && cell[i] <= 'Z' {
			column = column*26 + int(cell[i]-'A'+1)
			i++
		}
		line, err := strconv.Atoi(cell[i:])
		return line, column, err == nil && i > 0 && line > 0 && line <= 1048576 && column <= 16384
	}
	row, col, ok = parse(parts[0])
	if !ok {
		return
	}
	lastRow, lastCol := row, col
	if len(parts) == 2 {
		lastRow, lastCol, ok = parse(parts[1])
		if !ok {
			return
		}
	}
	rows, cols = lastRow-row+1, lastCol-col+1
	ok = rows > 0 && cols > 0 && rows*cols <= 400
	return
}

func officeActivityCell(row, col int) string {
	letters := ""
	for col > 0 {
		col--
		letters = string(rune('A'+col%26)) + letters
		col /= 26
	}
	return letters + strconv.Itoa(row)
}
