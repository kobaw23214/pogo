package web

// The inbox is a receipt ledger for the existing steering path. It is not a
// second agent scheduler: delivery still happens at the loop's tool boundary,
// and queued work starts only through Session.startInput after a successful run.
import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/yosukeno/pogo/inputref"
	"github.com/yosukeno/pogo/llm"
	"github.com/yosukeno/pogo/mcpobserve"
	"github.com/yosukeno/pogo/tools"
)

const maxInboxPending = 64
const maxInboxText = 64 << 10
const maxInboxFile = 32 << 20

var errInboxConflict = errors.New("消息状态已改变，请刷新后重试")
var errInboxInvalid = errors.New("无效的待处理消息")

type InboxItem struct {
	ID          string             `json:"id"`
	Text        string             `json:"text"`
	Parts       []MessageInputPart `json:"parts,omitempty"`
	InputLayout string             `json:"input_layout,omitempty"`
	Mode        string             `json:"mode"`
	Status      string             `json:"status"`
	Revision    int                `json:"revision"`
	CreatedAt   int64              `json:"created_at"`
	UpdatedAt   int64              `json:"updated_at"`
	RunID       string             `json:"run_id,omitempty"`
	MessageID   string             `json:"message_id,omitempty"`
	Note        string             `json:"note,omitempty"`
}

type inboxFile struct {
	Version int               `json:"version"`
	Items   []InboxItem       `json:"items"`
	Hashes  map[string]string `json:"request_hashes"`
}

func inboxPath(transcript string) string { return transcript + ".inbox.json" }

func validInboxID(id string) bool {
	if len(id) < 1 || len(id) > 96 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func validInboxText(text string) bool {
	return strings.TrimSpace(text) != "" && len(text) <= maxInboxText && slashCommand(text) == "" && !strings.HasPrefix(strings.TrimSpace(text), "/skill:")
}

func (s *Session) inboxIndexLocked(id string) int {
	for i := range s.inbox {
		if s.inbox[i].ID == id {
			return i
		}
	}
	return -1
}

func (s *Session) loadInboxLocked() error {
	if s.inboxLoaded {
		return s.inboxErr
	}
	s.inboxLoaded = true
	s.inboxHashes = map[string]string{}
	path := inboxPath(s.store.Path())
	st, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		s.hub.setInbox(nil)
		return nil
	}
	if err != nil {
		s.inboxErr = err
		return err
	}
	if !st.Mode().IsRegular() || st.Size() > maxInboxFile {
		s.inboxErr = errors.New("待处理消息记录无法读取")
		return s.inboxErr
	}
	f, err := os.Open(path)
	if err != nil {
		s.inboxErr = err
		return err
	}
	defer f.Close()
	var saved inboxFile
	err = json.NewDecoder(io.LimitReader(f, maxInboxFile+1)).Decode(&saved)
	if err != nil || saved.Version != 1 {
		s.inboxErr = errors.New("待处理消息记录无效，原文件已保留")
		return s.inboxErr
	}
	seen := map[string]bool{}
	for _, item := range saved.Items {
		pending := item.Status == "queued" || item.Status == "steering"
		if !validInboxID(item.ID) || seen[item.ID] || (pending && !validInboxContent(item.Text, item.Parts, item.InputLayout)) || item.Revision < 1 || (item.Mode != "queued" && item.Mode != "steer") ||
			(item.Status != "queued" && item.Status != "steering" && item.Status != "delivered" && item.Status != "withdrawn") {
			s.inboxErr = errors.New("待处理消息记录无效，原文件已保留")
			return s.inboxErr
		}
		seen[item.ID] = true
	}
	s.inbox, s.inboxHashes = saved.Items, saved.Hashes
	if s.inboxHashes == nil {
		s.inboxHashes = map[string]string{}
	}
	for i := range s.inbox {
		if s.inbox[i].Status == "delivered" || s.inbox[i].Status == "withdrawn" {
			// Keep only the receipt for finished input. The transcript owns its
			// delivered body; the original digest still deduplicates exact retries.
			s.inbox[i].Text = ""
			s.inbox[i].Parts = nil
		}
		if s.inbox[i].Status == "queued" {
			s.inbox[i].Mode = "steer"
			s.inbox[i].Note = "连接恢复，消息尚未处理，请点击发送。"
		}
	}
	// A persisted input identity proves delivery even if the process ended before
	// writing its receipt. Uncommitted steering returns to a visible queue, never
	// starts itself merely because somebody opens the conversation again.
	s.reconcileInboxLocked(s.store.Messages(""), "连接恢复，消息尚未处理，请点击发送。")
	s.hub.setInbox(s.inbox)
	return s.inboxErr
}

func (s *Session) saveInboxLocked() error {
	data, err := json.Marshal(inboxFile{Version: 1, Items: s.inbox, Hashes: s.inboxHashes})
	if err != nil {
		return err
	}
	if len(data) > maxInboxFile {
		return errors.New("待处理消息记录已达到容量上限")
	}
	path := inboxPath(s.store.Path())
	f, err := os.CreateTemp(filepath.Dir(path), ".inbox-*.tmp")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}

func (s *Session) publishInboxLocked(item InboxItem) {
	s.hub.Publish(Event{Type: EvInboxUpdated, Item: &item})
}

func inboxDigest(text, mode string) string {
	h := sha256.Sum256([]byte(mode + "\x00" + text))
	return hex.EncodeToString(h[:])
}

// ReceiveInput durably accepts a client-generated id before acknowledging it.
// Retrying the same request returns its latest receipt without re-enqueuing.
func (s *Session) ReceiveInput(id, text, mode, expectedRun string) (InboxItem, error) {
	return s.ReceiveParts(id, text, nil, "", mode, expectedRun)
}

func validInboxContent(text string, parts []MessageInputPart, layout string) bool {
	if parts == nil {
		return validInboxText(text)
	}
	for _, part := range parts {
		if part.Type == "text" && strings.HasPrefix(strings.TrimSpace(part.Text), "/skill:") {
			return false
		}
	}
	text, _, err := normalizeMessageInput(text, nil, parts, layout)
	return err == nil && !strings.HasPrefix(strings.TrimSpace(text), "/skill:")
}

func inboxPartsDigest(text, mode, layout string, parts []MessageInputPart) string {
	if parts == nil {
		return inboxDigest(text, mode)
	}
	data, _ := json.Marshal(struct {
		Text, Mode, Layout string
		Parts              []MessageInputPart
	}{text, mode, layout, parts})
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func (s *Session) ReceiveParts(id, text string, parts []MessageInputPart, layout, mode, expectedRun string) (InboxItem, error) {
	var err error
	text, _, err = normalizeMessageInput(text, nil, parts, layout)
	if err != nil {
		return InboxItem{}, errInboxInvalid
	}
	if err := s.checkInputSkillAccess(parts); err != nil {
		return InboxItem{}, fmt.Errorf("%w: %v", errInboxInvalid, err)
	}
	if !validInboxID(id) || !validInboxContent(text, parts, layout) || (mode != "queued" && mode != "steer") {
		return InboxItem{}, errInboxInvalid
	}
	s.mu.Lock()
	if err := s.loadInboxLocked(); err != nil {
		s.mu.Unlock()
		return InboxItem{}, err
	}
	if s.closing {
		s.mu.Unlock()
		return InboxItem{}, ErrNotFound
	}
	digest := inboxPartsDigest(text, mode, layout, parts)
	if i := s.inboxIndexLocked(id); i >= 0 {
		item := s.inbox[i]
		same := s.inboxHashes[id] == digest
		s.mu.Unlock()
		if !same {
			return InboxItem{}, errInboxConflict
		}
		return item, nil
	}
	if err := s.validateInputFiles(parts); err != nil {
		s.mu.Unlock()
		return InboxItem{}, fmt.Errorf("%w: %v", errInboxInvalid, err)
	}
	pending := 0
	for _, item := range s.inbox {
		if item.Status == "queued" || item.Status == "steering" {
			pending++
		}
	}
	if pending >= maxInboxPending {
		s.mu.Unlock()
		return InboxItem{}, errors.New("待处理消息过多，请先处理或撤回已有消息")
	}
	item := InboxItem{ID: id, Text: text, Parts: inputref.Clone(parts), InputLayout: layout, Mode: mode, Status: "queued", Revision: 1, CreatedAt: nowMS(), UpdatedAt: nowMS()}
	s.inbox = append(s.inbox, item)
	s.inboxHashes[id] = digest
	if err := s.saveInboxLocked(); err != nil {
		s.inbox = s.inbox[:len(s.inbox)-1]
		delete(s.inboxHashes, id)
		s.mu.Unlock()
		return InboxItem{}, err
	}
	s.publishInboxLocked(item)
	idle := s.active == nil && !s.compacting && s.inboxStarting == ""
	s.mu.Unlock()
	if mode == "steer" {
		return s.activateInput(id, item.Revision, expectedRun, false)
	}
	if idle {
		s.startNextQueuedInput()
		s.mu.Lock()
		item = s.inbox[s.inboxIndexLocked(id)]
		s.mu.Unlock()
		return item, nil
	}
	return item, nil
}

func (s *Session) EditInput(id, text string, revision int, withdraw bool) (InboxItem, error) {
	return s.EditParts(id, text, nil, "", revision, withdraw)
}

func (s *Session) EditParts(id, text string, parts []MessageInputPart, layout string, revision int, withdraw bool) (InboxItem, error) {
	if !withdraw {
		var err error
		text, _, err = normalizeMessageInput(text, nil, parts, layout)
		if err != nil {
			return InboxItem{}, errInboxInvalid
		}
		if err := s.checkInputSkillAccess(parts); err != nil {
			return InboxItem{}, fmt.Errorf("%w: %v", errInboxInvalid, err)
		}
		if err := s.validateInputFiles(parts); err != nil {
			return InboxItem{}, fmt.Errorf("%w: %v", errInboxInvalid, err)
		}
	}
	if !validInboxID(id) || revision < 1 || (!withdraw && !validInboxContent(text, parts, layout)) {
		return InboxItem{}, errInboxInvalid
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.loadInboxLocked(); err != nil {
		return InboxItem{}, err
	}
	i := s.inboxIndexLocked(id)
	if i < 0 {
		return InboxItem{}, ErrNotFound
	}
	old := s.inbox[i]
	if s.closing || old.Status != "queued" || old.Revision != revision {
		return InboxItem{}, errInboxConflict
	}
	item := old
	if withdraw {
		item.Status = "withdrawn"
		item.Text = ""
		item.Parts = nil
	} else {
		item.Text = text
		item.Parts = inputref.Clone(parts)
		item.InputLayout = layout
	}
	item.Revision++
	item.UpdatedAt = nowMS()
	item.Note = ""
	s.inbox[i] = item
	if err := s.saveInboxLocked(); err != nil {
		s.inbox[i] = old
		return InboxItem{}, err
	}
	s.publishInboxLocked(item)
	return item, nil
}

func (s *Session) activateInput(id string, revision int, expectedRun string, queuedOnly bool) (InboxItem, error) {
	s.mu.Lock()
	if err := s.loadInboxLocked(); err != nil {
		s.mu.Unlock()
		return InboxItem{}, err
	}
	i := s.inboxIndexLocked(id)
	if i < 0 {
		s.mu.Unlock()
		return InboxItem{}, ErrNotFound
	}
	old := s.inbox[i]
	// File preparation may upload to a bound Skill runtime; do it without the
	// session lock, then compare the receipt revision before accepting delivery.
	hadActive := s.active != nil
	s.mu.Unlock()
	_, paths, prepErr := normalizeMessageInput(old.Text, nil, old.Parts, old.InputLayout)
	var content []llm.Block
	var handles []tools.ImageHandle
	armBrowser := false
	var skillBlocks map[int]llm.Block
	if prepErr == nil && hadActive {
		skillBlocks, _, armBrowser, prepErr = s.prepareInputSkills(old.Parts)
	}
	if prepErr == nil && hadActive {
		_, _, byPath, imageHandles, err := s.prepareInputAttachments(paths, old.Parts)
		prepErr = err
		handles = imageHandles
		if old.Parts != nil {
			content = orderedInputBlocks(old.Parts, byPath, skillBlocks)
		} else {
			content = llm.UserText(old.Text).Content
		}
	}
	s.mu.Lock()
	i = s.inboxIndexLocked(id)
	if i < 0 {
		s.mu.Unlock()
		return InboxItem{}, ErrNotFound
	}
	if prepErr != nil {
		s.mu.Unlock()
		return InboxItem{}, prepErr
	}
	if s.inbox[i].Revision != old.Revision {
		s.mu.Unlock()
		return InboxItem{}, errInboxConflict
	}
	// An ordinary /messages request may have occupied the idle slot while we
	// released the lock. No active-run content was prepared in that case; let
	// the durable receipt be retried instead of delivering an empty message.
	if !hadActive && s.active != nil {
		s.mu.Unlock()
		return InboxItem{}, errInboxConflict
	}
	if s.closing || s.compacting || s.inboxStarting != "" || old.Status != "queued" || old.Revision != revision || (queuedOnly && s.active != nil) {
		s.mu.Unlock()
		return InboxItem{}, errInboxConflict
	}
	if expectedRun != "" && s.active != nil && s.active.id != expectedRun {
		s.mu.Unlock()
		return InboxItem{}, errInboxConflict
	}
	item := old
	item.Status = "steering"
	item.Note = ""
	item.Revision++
	item.UpdatedAt = nowMS()
	if !queuedOnly {
		item.Mode = "steer"
	}
	if s.active != nil {
		item.RunID = s.active.id
	}
	s.inbox[i] = item
	if err := s.saveInboxLocked(); err != nil {
		s.inbox[i] = old
		s.mu.Unlock()
		return InboxItem{}, err
	}
	s.publishInboxLocked(item)
	if s.active != nil {
		m := llm.Message{Role: llm.RoleUser, Content: content, InputLayout: item.InputLayout}
		s.images = append(s.images, handles...)
		m.InboxID, m.RunID, m.Steering = item.ID, s.active.id, true
		wasArmed := s.Browser().Armed()
		if armBrowser {
			s.Browser().Arm()
		}
		accepted := true
		if s.active.preparing {
			s.active.steering = append(s.active.steering, m)
		} else {
			accepted = s.agent.SteerMessage(m)
		}
		if !accepted {
			if armBrowser && !wasArmed {
				s.Browser().Disarm()
			}
			item.Status = "queued"
			item.Note = "当前执行正在结束，请点击发送。"
			item.Revision++
			item.UpdatedAt = nowMS()
			s.inbox[i] = item
			if err := s.saveInboxLocked(); err != nil {
				s.inboxErr = err
			}
			s.publishInboxLocked(item)
		}
		s.mu.Unlock()
		return item, nil
	}
	// Reserve the idle slot before releasing the receipt lock. Two simultaneous
	// sends must not start the second accepted item before the first.
	s.inboxStarting = id
	s.mu.Unlock()
	_, err := s.startOrderedInput(item.Text, nil, item.Parts, item.InputLayout, "", mcpobserve.Metadata{}, id)
	if err != nil {
		s.mu.Lock()
		if s.inboxStarting == id {
			s.inboxStarting = ""
		}
		if i := s.inboxIndexLocked(id); i >= 0 && s.inbox[i].Status == "steering" {
			item = s.inbox[i]
			item.Status = "queued"
			item.Note = "尚未开始执行，请点击发送重试。"
			item.Revision++
			item.UpdatedAt = nowMS()
			s.inbox[i] = item
			if saveErr := s.saveInboxLocked(); saveErr != nil {
				s.inboxErr = saveErr
			}
			s.publishInboxLocked(item)
		}
		s.mu.Unlock()
		return item, nil // acceptance is durable even when starting the run fails
	}
	s.mu.Lock()
	item = s.inbox[s.inboxIndexLocked(id)]
	s.mu.Unlock()
	return item, nil
}

func (s *Session) markInboxDelivered(m llm.Message) error {
	if isShellNotificationID(m.InboxID) {
		return s.ackShellNotification(m.InboxID)
	}
	if isUserInputInboxID(m.InboxID) && s.userInput != nil {
		if err := s.userInput.markDelivered(strings.TrimPrefix(m.InboxID, userInputInboxPrefix)); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.inboxIndexLocked(m.InboxID)
	if i < 0 || s.inbox[i].Status == "delivered" {
		return nil
	}
	item := s.inbox[i]
	item.Status = "delivered"
	item.Text = ""
	item.Parts = nil
	item.RunID = m.RunID
	item.MessageID = s.hub.inboxMessageID(m.InboxID)
	item.Note = ""
	item.Revision++
	item.UpdatedAt = nowMS()
	s.inbox[i] = item
	// The transcript already contains InboxID. Stop before another model call if
	// its receipt cannot be committed: this also keeps later compaction from
	// hiding the only durable identity before recovery reconciles it.
	if err := s.saveInboxLocked(); err != nil {
		s.hub.Publish(Event{Type: EvError, Error: "消息已送达，但收件状态保存失败：" + err.Error()})
		s.publishInboxLocked(item)
		return err
	}
	s.publishInboxLocked(item)
	return nil
}

func (s *Session) reconcileInboxLocked(messages []llm.Message, note string) {
	if !s.inboxLoaded {
		return
	}
	committed := map[string]llm.Message{}
	for _, m := range messages {
		if m.InboxID != "" {
			committed[m.InboxID] = m
		}
	}
	changed := false
	for i := range s.inbox {
		item := s.inbox[i]
		if item.Status != "steering" {
			continue
		}
		if m, ok := committed[item.ID]; ok {
			item.Status = "delivered"
			item.Text = ""
			item.Parts = nil
			item.RunID = m.RunID
			item.MessageID = s.hub.inboxMessageID(item.ID)
			item.Note = ""
		} else {
			item.Status = "queued"
			item.RunID = ""
			item.Note = note
			// Do not auto-drain a receipt recovered after an interruption. A user
			// must explicitly activate it, even if its original mode was queued.
			item.Mode = "steer"
		}
		item.Revision++
		item.UpdatedAt = nowMS()
		s.inbox[i] = item
		changed = true
		s.publishInboxLocked(item)
	}
	if changed {
		if err := s.saveInboxLocked(); err != nil {
			s.inboxErr = fmt.Errorf("保存收件状态失败: %w", err)
		}
	}
}

func (s *Session) startNextQueuedInput() {
	s.mu.Lock()
	if s.closing || s.compacting || s.active != nil || s.inboxStarting != "" || s.inboxErr != nil {
		s.mu.Unlock()
		return
	}
	var next *InboxItem
	for _, item := range s.inbox {
		if item.Status == "queued" && item.Mode == "queued" {
			cp := item
			next = &cp
			break
		}
	}
	s.mu.Unlock()
	if next != nil {
		_, _ = s.activateInput(next.ID, next.Revision, "", true)
	}
}

func (s *Session) holdQueuedInputs(note string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	for i := range s.inbox {
		if s.inbox[i].Status == "queued" && s.inbox[i].Mode == "queued" {
			s.inbox[i].Mode = "steer"
			s.inbox[i].Note = note
			s.inbox[i].Revision++
			s.inbox[i].UpdatedAt = nowMS()
			s.publishInboxLocked(s.inbox[i])
			changed = true
		}
	}
	if changed {
		if err := s.saveInboxLocked(); err != nil {
			s.inboxErr = err
		}
	}
}

func (s *Session) cancelStartingInputLocked() bool {
	id := s.inboxStarting
	s.inboxStarting = ""
	changed := false
	for i := range s.inbox {
		if s.inbox[i].ID == id || s.inbox[i].Status == "queued" {
			s.inbox[i].Status = "queued"
			s.inbox[i].Mode = "steer"
			s.inbox[i].RunID = ""
			s.inbox[i].Note = "执行已停止，消息需要点击发送。"
			s.inbox[i].Revision++
			s.inbox[i].UpdatedAt = nowMS()
			s.publishInboxLocked(s.inbox[i])
			changed = true
		}
	}
	if changed {
		if err := s.saveInboxLocked(); err != nil {
			s.inboxErr = err
		}
	}
	return changed
}
