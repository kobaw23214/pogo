package web

import "errors"

// deliverUserInputAnswer uses the existing receipt ledger to admit an answer
// once. Active runs receive it at a tool boundary; idle sessions start a new
// turn. A recovered queued answer is explicitly activated rather than treated
// as an ordinary draft that needs another click.
func (s *Session) deliverUserInputAnswer(request PendingUserInput, answers map[string][]string) (InboxItem, error) {
	id := userInputInboxID(request.RequestID)
	s.mu.Lock()
	if err := s.loadInboxLocked(); err != nil {
		s.mu.Unlock()
		return InboxItem{}, err
	}
	index := s.inboxIndexLocked(id)
	var item InboxItem
	if index >= 0 {
		item = s.inbox[index]
	}
	s.mu.Unlock()
	if index >= 0 {
		switch item.Status {
		case "delivered", "steering":
			return item, nil
		case "withdrawn":
			return item, errors.New("the answer was withdrawn before delivery")
		case "queued":
			return s.activateInput(id, item.Revision, "", false)
		}
	}
	return s.ReceiveParts(id, userInputAnswerText(request, answers), nil, "", "steer", "")
}
