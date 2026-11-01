package web

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/yosukeno/pogo/tools"
)

const (
	maxUserInputStoreBytes = 4 << 20
	maxUserInputRecords    = 64
	maxAsyncUserInputs     = 16
	userInputInboxPrefix   = "user-input-answer-"
)

type asyncUserInputRecord struct {
	Request     PendingUserInput    `json:"request"`
	Status      string              `json:"status"`
	Answers     map[string][]string `json:"answers,omitempty"`
	InboxID     string              `json:"inbox_id,omitempty"`
	CreatedAt   int64               `json:"created_at"`
	AnsweredAt  int64               `json:"answered_at,omitempty"`
	DeliveredAt int64               `json:"delivered_at,omitempty"`
}

type userInputStoreFile struct {
	Version int                     `json:"version"`
	Records []*asyncUserInputRecord `json:"records"`
}

func userInputStorePath(transcript string) string { return transcript + ".user-inputs.json" }
func userInputInboxID(requestID string) string    { return userInputInboxPrefix + requestID }
func isUserInputInboxID(id string) bool           { return strings.HasPrefix(id, userInputInboxPrefix) }

func loadUserInputStore(path string) (map[string]*asyncUserInputRecord, error) {
	out := map[string]*asyncUserInputRecord{}
	if path == "" {
		return out, nil
	}
	st, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() || st.Size() > maxUserInputStoreBytes {
		return nil, errors.New("invalid structured-question store")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var saved userInputStoreFile
	if err := json.NewDecoder(io.LimitReader(file, maxUserInputStoreBytes+1)).Decode(&saved); err != nil {
		return nil, err
	}
	if saved.Version != 1 || len(saved.Records) > maxUserInputRecords {
		return nil, errors.New("unsupported structured-question store")
	}
	pending := 0
	for _, record := range saved.Records {
		if record == nil || !validInboxID(record.Request.RequestID) || record.Request.Revision != 1 || strings.TrimSpace(record.Request.CallID) == "" || !record.Request.Async || record.CreatedAt <= 0 {
			return nil, errors.New("invalid structured-question identity")
		}
		if _, exists := out[record.Request.RequestID]; exists {
			return nil, errors.New("duplicate structured-question identity")
		}
		questions, err := tools.NormalizeUserInputQuestions(record.Request.Questions)
		if err != nil {
			return nil, err
		}
		record.Request.Questions = questions
		record.Request.Deadline = 0
		switch record.Status {
		case "pending":
			pending++
		case "answered", "delivered":
			if record.AnsweredAt <= 0 || record.InboxID != userInputInboxID(record.Request.RequestID) {
				return nil, errors.New("invalid structured-question delivery identity")
			}
			answers, err := normalizeUserInputAnswers(questions, record.Answers)
			if err != nil {
				return nil, err
			}
			record.Answers = answers
		case "cancelled":
		default:
			return nil, errors.New("invalid structured-question status")
		}
		out[record.Request.RequestID] = record
	}
	if pending > maxAsyncUserInputs {
		return nil, errors.New("too many pending structured questions")
	}
	return out, nil
}

func saveUserInputStore(path string, records map[string]*asyncUserInputRecord) error {
	if path == "" {
		return nil
	}
	ordered := make([]*asyncUserInputRecord, 0, len(records))
	for _, record := range records {
		ordered = append(ordered, record)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].CreatedAt == ordered[j].CreatedAt {
			return ordered[i].Request.RequestID < ordered[j].Request.RequestID
		}
		return ordered[i].CreatedAt < ordered[j].CreatedAt
	})
	data, err := json.Marshal(userInputStoreFile{Version: 1, Records: ordered})
	if err != nil {
		return err
	}
	if len(data) > maxUserInputStoreBytes {
		return errors.New("structured-question store exceeds its size limit")
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".user-inputs-*.tmp")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if err := file.Chmod(0o600); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(data); err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(name, path)
}

func (u *WebUserInput) saveAsyncLocked() error {
	if u.storeErr != nil {
		return u.storeErr
	}
	return saveUserInputStore(u.storePath, u.records)
}

func (u *WebUserInput) pruneAsyncLocked() {
	if len(u.records) <= maxUserInputRecords {
		return
	}
	ids := make([]string, 0, len(u.records))
	for id, record := range u.records {
		if record.Status == "delivered" || record.Status == "cancelled" {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool {
		left, right := u.records[ids[i]], u.records[ids[j]]
		if left.AnsweredAt == right.AnsweredAt {
			return ids[i] < ids[j]
		}
		return left.AnsweredAt < right.AnsweredAt
	})
	for _, id := range ids {
		if len(u.records) <= maxUserInputRecords {
			break
		}
		delete(u.records, id)
		delete(u.completed, id)
	}
	if len(u.order) > maxUserInputRecords {
		u.order = append([]string(nil), u.order[len(u.order)-maxUserInputRecords:]...)
	}
}

func (u *WebUserInput) recoverAnswers() {
	u.mu.Lock()
	records := make([]*asyncUserInputRecord, 0, len(u.records))
	for _, record := range u.records {
		if record.Status == "answered" {
			records = append(records, record)
		}
	}
	u.mu.Unlock()
	for _, candidate := range records {
		u.mu.Lock()
		record := u.records[candidate.Request.RequestID]
		pending := record != nil && record.Status == "answered"
		u.mu.Unlock()
		if !pending {
			continue
		}
		if _, err := u.deliver(record); err != nil {
			u.hub.Publish(Event{Type: EvError, Error: "A saved asynchronous answer is still waiting for delivery: " + err.Error()})
		}
	}
}

func userInputAnswerText(request PendingUserInput, answers map[string][]string) string {
	payload := struct {
		Kind      string                    `json:"kind"`
		RequestID string                    `json:"request_id"`
		CallID    string                    `json:"call_id"`
		RunID     string                    `json:"run_id,omitempty"`
		Questions []tools.UserInputQuestion `json:"questions"`
		Answers   map[string][]string       `json:"answers"`
		Note      string                    `json:"note"`
	}{
		Kind: "user_input_answer", RequestID: request.RequestID, CallID: request.CallID, RunID: request.RunID,
		Questions: request.Questions, Answers: answers,
		Note: "This is the user's answer to the earlier optional question, not an execution approval. Use it in the current conversation; do not repeat the old tool call or old task.",
	}
	encoded, _ := json.Marshal(payload)
	return "<task-notification>" + string(encoded) + "</task-notification>"
}

func (u *WebUserInput) deliver(record *asyncUserInputRecord) (InboxItem, error) {
	if u.deliverAnswer == nil {
		return InboxItem{}, ErrUserInputUnavailable
	}
	item, err := u.deliverAnswer(record.Request, record.Answers)
	if err != nil {
		return item, err
	}
	if item.Status == "delivered" {
		if err := u.markDelivered(record.Request.RequestID); err != nil {
			return item, err
		}
	}
	return item, nil
}

func (u *WebUserInput) markDelivered(requestID string) error {
	u.mu.Lock()
	record, ok := u.records[requestID]
	if !ok || record.Status != "answered" {
		u.mu.Unlock()
		return nil
	}
	record.Status = "delivered"
	record.DeliveredAt = time.Now().UnixMilli()
	u.pruneAsyncLocked()
	err := u.saveAsyncLocked()
	u.mu.Unlock()
	return err
}

func orderedUserInputRecords(records map[string]*asyncUserInputRecord) []*asyncUserInputRecord {
	ordered := make([]*asyncUserInputRecord, 0, len(records))
	for _, record := range records {
		ordered = append(ordered, record)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].CreatedAt == ordered[j].CreatedAt {
			return ordered[i].Request.RequestID < ordered[j].Request.RequestID
		}
		return ordered[i].CreatedAt < ordered[j].CreatedAt
	})
	return ordered
}
