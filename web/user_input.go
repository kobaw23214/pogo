package web

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/yosukeno/pogo/tools"
)

const DefaultUserInputTimeout = 30 * time.Minute

var (
	ErrUserInputNotFound    = errors.New("no pending question with this ID")
	ErrUserInputRevision    = errors.New("the question version is stale; reload the current question")
	ErrUserInputResolved    = errors.New("this question has already been resolved")
	ErrUserInputUnavailable = errors.New("structured questions are unavailable in this session")
)

type PendingUserInput struct {
	RequestID string                    `json:"request_id"`
	CallID    string                    `json:"call_id"`
	RunID     string                    `json:"run_id,omitempty"`
	Revision  int                       `json:"revision"`
	Questions []tools.UserInputQuestion `json:"questions"`
	Deadline  int64                     `json:"deadline"`
	Async     bool                      `json:"async"`
}

type UserInputResolution struct {
	RequestID string              `json:"request_id"`
	CallID    string              `json:"call_id,omitempty"`
	Status    string              `json:"status"`
	Reason    string              `json:"reason,omitempty"`
	Async     bool                `json:"async,omitempty"`
	Answers   map[string][]string `json:"answers,omitempty"`
}

type userInputOutcome struct {
	status  string
	answers map[string][]string
	reason  string
}

type pendingUserInput struct {
	request PendingUserInput
	decide  chan userInputOutcome
}

type resolvedUserInput struct {
	revision int
	outcome  userInputOutcome
}

// WebUserInput is a session-bound wait, not a policy decision. It survives
// browser reconnects through the Hub, while its channel retains the one tool
// invocation that may consume the answer.
type WebUserInput struct {
	hub     *Hub
	timeout time.Duration
	serial  chan struct{}

	mu            sync.Mutex
	pending       map[string]*pendingUserInput
	completed     map[string]resolvedUserInput
	records       map[string]*asyncUserInputRecord
	order         []string
	epoch         string
	n             int
	closed        bool
	storePath     string
	storeErr      error
	deliverAnswer func(PendingUserInput, map[string][]string) (InboxItem, error)
}

func NewWebUserInput(hub *Hub, timeout time.Duration) *WebUserInput {
	return newWebUserInput(hub, timeout, "", nil, nil, nil)
}

func NewWebUserInputWithStore(hub *Hub, timeout time.Duration, transcript string, deliver func(PendingUserInput, map[string][]string) (InboxItem, error)) *WebUserInput {
	records, err := loadUserInputStore(userInputStorePath(transcript))
	return newWebUserInput(hub, timeout, userInputStorePath(transcript), records, deliver, err)
}

func newWebUserInput(hub *Hub, timeout time.Duration, storePath string, records map[string]*asyncUserInputRecord, deliver func(PendingUserInput, map[string][]string) (InboxItem, error), loadErr error) *WebUserInput {
	if timeout <= 0 {
		timeout = DefaultUserInputTimeout
	}
	if records == nil {
		records = map[string]*asyncUserInputRecord{}
	}
	u := &WebUserInput{
		hub: hub, timeout: timeout, serial: make(chan struct{}, 1), storePath: storePath, storeErr: loadErr, deliverAnswer: deliver,
		pending: make(map[string]*pendingUserInput), completed: make(map[string]resolvedUserInput), records: records, epoch: rand.Text(), n: len(records),
	}
	for _, record := range orderedUserInputRecords(records) {
		u.order = append(u.order, record.Request.RequestID)
		switch record.Status {
		case "pending":
			u.pending[record.Request.RequestID] = &pendingUserInput{request: *clonePendingUserInputPtr(record.Request)}
			u.hub.Publish(Event{Type: EvUserInputRequest, UserInput: clonePendingUserInputPtr(record.Request)})
		case "answered", "delivered":
			u.completed[record.Request.RequestID] = resolvedUserInput{revision: record.Request.Revision, outcome: userInputOutcome{status: "answered", answers: cloneUserInputAnswers(record.Answers)}}
			u.hub.restoreUserInputAnswers(tools.UserInputDetails{RequestID: record.Request.RequestID, CallID: record.Request.CallID, Status: record.Status, Async: true, Questions: record.Request.Questions, Answers: record.Answers})
		case "cancelled":
			u.completed[record.Request.RequestID] = resolvedUserInput{revision: record.Request.Revision, outcome: userInputOutcome{status: "cancelled", reason: "The user chose not to answer these questions. Do not infer a preference or treat this as approval for another tool."}}
			u.hub.restoreUserInputAnswers(tools.UserInputDetails{RequestID: record.Request.RequestID, CallID: record.Request.CallID, Status: record.Status, Async: true, Questions: record.Request.Questions})
		}
	}
	return u
}

func (u *WebUserInput) Ask(ctx context.Context, input tools.UserInputRequest) (map[string][]string, error) {
	select {
	case u.serial <- struct{}{}:
		defer func() { <-u.serial }()
	case <-ctx.Done():
		return nil, errors.New("The question was cancelled before it was shown; do not infer a preference")
	}
	if err := ctx.Err(); err != nil {
		return nil, errors.New("The question was cancelled before it was shown; do not infer a preference")
	}
	if strings.TrimSpace(input.CallID) == "" {
		return nil, errors.New("a host-owned tool call ID is required")
	}
	questions, err := tools.NormalizeUserInputQuestions(input.Questions)
	if err != nil {
		return nil, err
	}
	u.mu.Lock()
	if u.closed {
		u.mu.Unlock()
		return nil, errors.New("The question was cancelled because this session closed; do not infer a preference")
	}
	u.n++
	p := &pendingUserInput{
		request: PendingUserInput{
			RequestID: fmt.Sprintf("q%s_%d", u.epoch, u.n), CallID: input.CallID, Revision: 1,
			Questions: questions, Deadline: time.Now().Add(u.timeout).UnixMilli(),
		},
		decide: make(chan userInputOutcome, 1),
	}
	u.pending[p.request.RequestID] = p
	u.mu.Unlock()
	u.hub.Publish(Event{Type: EvUserInputRequest, UserInput: clonePendingUserInputPtr(p.request)})
	timer := time.NewTimer(u.timeout)
	defer timer.Stop()
	select {
	case outcome := <-p.decide:
		if outcome.status != "answered" {
			return nil, errors.New(outcome.reason)
		}
		return cloneUserInputAnswers(outcome.answers), nil
	case <-timer.C:
		u.finish(p.request.RequestID, userInputOutcome{status: "timeout", reason: fmt.Sprintf("The user did not answer within %s. Do not select an option or infer a preference on their behalf.", u.timeout)})
		return nil, fmt.Errorf("the user did not answer within %s; do not infer a preference", u.timeout)
	case <-ctx.Done():
		u.finish(p.request.RequestID, userInputOutcome{status: "cancelled", reason: "The run was cancelled before the user answered. Do not infer a preference."})
		return nil, errors.New("the question was cancelled before the user answered; do not infer a preference")
	}
}

func (u *WebUserInput) AskAsync(ctx context.Context, input tools.UserInputRequest) (tools.UserInputTicket, error) {
	if err := ctx.Err(); err != nil {
		return tools.UserInputTicket{}, errors.New("the asynchronous question was cancelled before registration")
	}
	if strings.TrimSpace(input.CallID) == "" {
		return tools.UserInputTicket{}, errors.New("a host-owned tool call ID is required")
	}
	questions, err := tools.NormalizeUserInputQuestions(input.Questions)
	if err != nil {
		return tools.UserInputTicket{}, err
	}
	u.mu.Lock()
	if u.closed {
		u.mu.Unlock()
		return tools.UserInputTicket{}, errors.New("the session is closed")
	}
	if u.storeErr != nil {
		u.mu.Unlock()
		return tools.UserInputTicket{}, u.storeErr
	}
	pending := 0
	for _, record := range u.records {
		if record.Status == "pending" {
			pending++
		}
	}
	if pending >= maxAsyncUserInputs {
		u.mu.Unlock()
		return tools.UserInputTicket{}, errors.New("too many optional questions are waiting; answer or cancel one first")
	}
	u.pruneAsyncLocked()
	if len(u.records) >= maxUserInputRecords {
		u.mu.Unlock()
		return tools.UserInputTicket{}, errors.New("too many undelivered question answers; let the current conversation catch up")
	}
	u.n++
	now := time.Now()
	request := PendingUserInput{RequestID: fmt.Sprintf("q%s_%d", u.epoch, u.n), CallID: input.CallID, RunID: input.RunID, Revision: 1, Questions: questions, Async: true}
	record := &asyncUserInputRecord{Request: request, Status: "pending", CreatedAt: now.UnixMilli()}
	u.records[request.RequestID] = record
	u.pending[request.RequestID] = &pendingUserInput{request: request}
	if err := u.saveAsyncLocked(); err != nil {
		delete(u.records, request.RequestID)
		delete(u.pending, request.RequestID)
		u.mu.Unlock()
		return tools.UserInputTicket{}, err
	}
	u.mu.Unlock()
	u.hub.Publish(Event{Type: EvUserInputRequest, UserInput: clonePendingUserInputPtr(request)})
	return tools.UserInputTicket{RequestID: request.RequestID, CallID: request.CallID, RunID: request.RunID, Revision: 1, Status: "pending", Questions: questions, WaitingForUser: true}, nil
}

// Answer is idempotent for a byte-equivalent retry, but never lets a second
// answer overwrite the first or an old request revision resume a call.
func (u *WebUserInput) Answer(id string, revision int, answers map[string][]string) (bool, error) {
	if u.isAsync(id) {
		return u.answerAsync(id, revision, answers)
	}
	return u.decideRequest(id, revision, userInputOutcome{status: "answered", answers: answers})
}

func (u *WebUserInput) isAsync(id string) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	_, ok := u.records[id]
	return ok
}

func (u *WebUserInput) answerAsync(id string, revision int, answers map[string][]string) (bool, error) {
	u.mu.Lock()
	if revision < 1 {
		u.mu.Unlock()
		return false, ErrUserInputRevision
	}
	record, ok := u.records[id]
	if !ok {
		u.mu.Unlock()
		return false, ErrUserInputNotFound
	}
	if revision != record.Request.Revision {
		u.mu.Unlock()
		return false, ErrUserInputRevision
	}
	normalized, err := normalizeUserInputAnswers(record.Request.Questions, answers)
	if err != nil {
		u.mu.Unlock()
		return false, err
	}
	if record.Status != "pending" {
		if record.Status == "cancelled" || !sameUserInputOutcome(u.completed[id].outcome, userInputOutcome{status: "answered", answers: normalized}) {
			u.mu.Unlock()
			return false, ErrUserInputResolved
		}
		delivered := record.Status == "delivered"
		u.mu.Unlock()
		if !delivered {
			if _, err = u.deliver(record); err != nil {
				return true, err
			}
		}
		u.resolveAsyncAnswer(id, record, normalized)
		return true, nil
	}
	record.Status = "answered"
	record.Answers = normalized
	record.InboxID = userInputInboxID(id)
	record.AnsweredAt = time.Now().UnixMilli()
	u.completed[id] = resolvedUserInput{revision: revision, outcome: userInputOutcome{status: "answered", answers: cloneUserInputAnswers(normalized)}}
	if err := u.saveAsyncLocked(); err != nil {
		record.Status = "pending"
		record.Answers = nil
		record.InboxID = ""
		record.AnsweredAt = 0
		delete(u.completed, id)
		u.mu.Unlock()
		return false, err
	}
	u.mu.Unlock()
	if _, err := u.deliver(record); err != nil {
		return false, err
	}
	u.resolveAsyncAnswer(id, record, normalized)
	return false, nil
}

func (u *WebUserInput) resolveAsyncAnswer(id string, record *asyncUserInputRecord, answers map[string][]string) {
	u.mu.Lock()
	_, pending := u.pending[id]
	delete(u.pending, id)
	u.mu.Unlock()
	if pending {
		u.hub.Publish(Event{Type: EvUserInputResolved, Resolution: &UserInputResolution{RequestID: id, CallID: record.Request.CallID, Status: "answered", Async: true, Answers: cloneUserInputAnswers(answers)}})
	}
}

func (u *WebUserInput) Cancel(id string, revision int) (bool, error) {
	if u.isAsync(id) {
		return u.cancelAsync(id, revision)
	}
	return u.decideRequest(id, revision, userInputOutcome{
		status: "cancelled", reason: "The user chose not to answer these questions. Do not infer a preference or treat this as approval for another tool.",
	})
}

func (u *WebUserInput) cancelAsync(id string, revision int) (bool, error) {
	const reason = "The user chose not to answer these questions. Do not infer a preference or treat this as approval for another tool."
	u.mu.Lock()
	record, ok := u.records[id]
	if !ok {
		u.mu.Unlock()
		return false, ErrUserInputNotFound
	}
	if revision < 1 || revision != record.Request.Revision {
		u.mu.Unlock()
		return false, ErrUserInputRevision
	}
	if record.Status != "pending" {
		resolved, exists := u.completed[id]
		u.mu.Unlock()
		if exists && record.Status == "cancelled" && sameUserInputOutcome(resolved.outcome, userInputOutcome{status: "cancelled", reason: reason}) {
			return true, nil
		}
		return false, ErrUserInputResolved
	}
	record.Status = "cancelled"
	u.completed[id] = resolvedUserInput{revision: revision, outcome: userInputOutcome{status: "cancelled", reason: reason}}
	if err := u.saveAsyncLocked(); err != nil {
		record.Status = "pending"
		delete(u.completed, id)
		u.mu.Unlock()
		return false, err
	}
	delete(u.pending, id)
	u.pruneAsyncLocked()
	u.mu.Unlock()
	u.hub.Publish(Event{Type: EvUserInputResolved, Resolution: &UserInputResolution{RequestID: id, CallID: record.Request.CallID, Status: "cancelled", Reason: reason, Async: true}})
	return false, nil
}

func (u *WebUserInput) decideRequest(id string, revision int, outcome userInputOutcome) (bool, error) {
	u.mu.Lock()
	if revision < 1 {
		u.mu.Unlock()
		return false, ErrUserInputRevision
	}
	p, ok := u.pending[id]
	if !ok {
		resolved, exists := u.completed[id]
		u.mu.Unlock()
		if !exists {
			return false, ErrUserInputNotFound
		}
		if revision != resolved.revision {
			return false, ErrUserInputRevision
		}
		if !sameUserInputOutcome(resolved.outcome, outcome) {
			return false, ErrUserInputResolved
		}
		return true, nil
	}
	if revision != p.request.Revision {
		u.mu.Unlock()
		return false, ErrUserInputRevision
	}
	if outcome.status == "answered" {
		normalized, err := normalizeUserInputAnswers(p.request.Questions, outcome.answers)
		if err != nil {
			u.mu.Unlock()
			return false, err
		}
		outcome.answers = normalized
	}
	delete(u.pending, id)
	u.rememberLocked(id, resolvedUserInput{revision: revision, outcome: outcome})
	u.mu.Unlock()
	u.hub.Publish(Event{Type: EvUserInputResolved, Resolution: &UserInputResolution{RequestID: id, Status: outcome.status, Reason: outcome.reason}})
	p.decide <- outcome
	return false, nil
}

func normalizeUserInputAnswers(questions []tools.UserInputQuestion, answers map[string][]string) (map[string][]string, error) {
	if len(answers) != len(questions) {
		return nil, errors.New("answer every question exactly once, or choose not to answer")
	}
	out := make(map[string][]string, len(questions))
	for _, q := range questions {
		values, ok := answers[q.ID]
		if !ok || len(values) == 0 {
			return nil, fmt.Errorf("question %q needs a non-empty answer", q.ID)
		}
		if !q.MultiSelect && len(values) != 1 {
			return nil, fmt.Errorf("question %q accepts exactly one answer", q.ID)
		}
		if len(values) > tools.MaxUserInputOptions+1 {
			return nil, fmt.Errorf("question %q has too many answers", q.ID)
		}
		seen := make(map[string]bool, len(values))
		out[q.ID] = make([]string, len(values))
		for i, value := range values {
			value = strings.TrimSpace(value)
			if value == "" || utf8.RuneCountInString(value) > tools.MaxUserInputAnswerLength {
				return nil, fmt.Errorf("answers for question %q must contain 1-%d characters", q.ID, tools.MaxUserInputAnswerLength)
			}
			if seen[value] {
				return nil, fmt.Errorf("question %q has a duplicate answer", q.ID)
			}
			seen[value] = true
			out[q.ID][i] = value
		}
	}
	return out, nil
}

func (u *WebUserInput) finish(id string, outcome userInputOutcome) {
	u.mu.Lock()
	p, ok := u.pending[id]
	if !ok {
		u.mu.Unlock()
		return
	}
	delete(u.pending, id)
	u.rememberLocked(id, resolvedUserInput{revision: p.request.Revision, outcome: outcome})
	u.mu.Unlock()
	u.hub.Publish(Event{Type: EvUserInputResolved, Resolution: &UserInputResolution{RequestID: id, Status: outcome.status, Reason: outcome.reason}})
	if p.decide != nil {
		p.decide <- outcome
	}
}

func (u *WebUserInput) rememberLocked(id string, resolved resolvedUserInput) {
	u.completed[id] = resolved
	u.order = append(u.order, id)
	for len(u.order) > 64 {
		delete(u.completed, u.order[0])
		u.order = u.order[1:]
	}
}

func (u *WebUserInput) Close() {
	u.mu.Lock()
	if u.closed {
		u.mu.Unlock()
		return
	}
	u.closed = true
	ids := make([]string, 0, len(u.pending))
	for id, pending := range u.pending {
		if !pending.request.Async {
			ids = append(ids, id)
		}
	}
	u.mu.Unlock()
	for _, id := range ids {
		u.finish(id, userInputOutcome{status: "cancelled", reason: "The session closed before the user answered. Do not infer a preference."})
	}
}

func sameUserInputOutcome(a, b userInputOutcome) bool {
	if a.status != b.status || a.reason != b.reason {
		return false
	}
	left, leftErr := json.Marshal(a.answers)
	right, rightErr := json.Marshal(b.answers)
	return leftErr == nil && rightErr == nil && string(left) == string(right)
}

func clonePendingUserInputPtr(request PendingUserInput) *PendingUserInput {
	clone := request
	clone.Questions = make([]tools.UserInputQuestion, len(request.Questions))
	for i, q := range request.Questions {
		q.Options = append([]tools.UserInputOption(nil), q.Options...)
		clone.Questions[i] = q
	}
	return &clone
}

func cloneUserInputAnswers(answers map[string][]string) map[string][]string {
	clone := make(map[string][]string, len(answers))
	for id, values := range answers {
		clone[id] = append([]string(nil), values...)
	}
	return clone
}

func cloneUserInputDetails(details tools.UserInputDetails) tools.UserInputDetails {
	details.Questions = clonePendingUserInputPtr(PendingUserInput{Questions: details.Questions}).Questions
	details.Answers = cloneUserInputAnswers(details.Answers)
	return details
}
