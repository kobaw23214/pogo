package tools

import (
	"context"
	"errors"
	"sync"
)

var ErrShellQueueFull = errors.New("shell task queue is full")

type ShellSchedulerConfig struct {
	Global  int
	User    int
	Session int
	Queue   int
}

type shellScheduleRequest struct {
	user, session string
	ready         chan struct{}
	granted       bool
}

type ShellScheduler struct {
	cfg      ShellSchedulerConfig
	mu       sync.Mutex
	running  int
	users    map[string]int
	sessions map[string]int
	queue    []*shellScheduleRequest
}

func NewShellScheduler(cfg ShellSchedulerConfig) *ShellScheduler {
	if cfg.Global <= 0 {
		cfg.Global = 16
	}
	if cfg.User <= 0 {
		cfg.User = 4
	}
	if cfg.Session <= 0 {
		cfg.Session = 4
	}
	if cfg.Queue == 0 {
		cfg.Queue = 64
	}
	if cfg.Queue < 0 {
		cfg.Queue = 0
	}
	return &ShellScheduler{cfg: cfg, users: map[string]int{}, sessions: map[string]int{}}
}

func (s *ShellScheduler) acquire(ctx context.Context, user, session string) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	key := user + "\x00" + session
	s.mu.Lock()
	s.scheduleLocked()
	req := &shellScheduleRequest{user: user, session: key, ready: make(chan struct{})}
	if len(s.queue) == 0 && s.availableLocked(req) {
		s.grantLocked(req)
		s.mu.Unlock()
		return s.releaseFunc(req), nil
	}
	if len(s.queue) >= s.cfg.Queue {
		s.mu.Unlock()
		return nil, ErrShellQueueFull
	}
	s.queue = append(s.queue, req)
	s.mu.Unlock()
	select {
	case <-req.ready:
		return s.releaseFunc(req), nil
	case <-ctx.Done():
		s.mu.Lock()
		granted := req.granted
		if !granted {
			for i, candidate := range s.queue {
				if candidate == req {
					s.queue = append(s.queue[:i], s.queue[i+1:]...)
					break
				}
			}
		}
		s.scheduleLocked()
		s.mu.Unlock()
		if granted {
			s.releaseFunc(req)()
		}
		return nil, ctx.Err()
	}
}

func (s *ShellScheduler) availableLocked(req *shellScheduleRequest) bool {
	return s.running < s.cfg.Global && s.users[req.user] < s.cfg.User && s.sessions[req.session] < s.cfg.Session
}

func (s *ShellScheduler) grantLocked(req *shellScheduleRequest) {
	s.running++
	s.users[req.user]++
	s.sessions[req.session]++
	req.granted = true
	close(req.ready)
}

func (s *ShellScheduler) scheduleLocked() {
	for i, req := range s.queue {
		if !s.availableLocked(req) {
			continue
		}
		s.queue = append(s.queue[:i], s.queue[i+1:]...)
		s.grantLocked(req)
		return
	}
}

func (s *ShellScheduler) releaseFunc(req *shellScheduleRequest) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			s.running--
			s.users[req.user]--
			if s.users[req.user] == 0 {
				delete(s.users, req.user)
			}
			s.sessions[req.session]--
			if s.sessions[req.session] == 0 {
				delete(s.sessions, req.session)
			}
			s.scheduleLocked()
			s.mu.Unlock()
		})
	}
}
