package engine

import "sync"

// ProgressGuard holds per-key in-progress flag and message queue.
// It is used to avoid blocking on the process mutex when the handler is already
// busy: call TryQueue before locking; if it returns true, the message was queued
// and the caller should return an "in progress" response to the user.
// Safe for use by CoreHandler (key=userID) and Engine (key=sessionID).
type ProgressGuard struct {
	mu    sync.RWMutex
	state map[string]*progressState
}

type progressState struct {
	InProgress bool
	Queue      []string
}

// NewProgressGuard returns a new ProgressGuard.
func NewProgressGuard() *ProgressGuard {
	return &ProgressGuard{state: make(map[string]*progressState)}
}

// TryQueue queues the message for the key and returns true if the key is already
// in progress (caller should return without blocking). Returns false if the key
// is not in progress (caller should proceed with processing).
func (p *ProgressGuard) TryQueue(key, message string) (queued bool) {
	p.mu.RLock()
	s := p.state[key]
	inProg := s != nil && s.InProgress
	p.mu.RUnlock()
	if !inProg {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.state[key] == nil {
		p.state[key] = &progressState{}
	}
	p.state[key].Queue = append(p.state[key].Queue, message)
	return true
}

// SetInProgress sets the in-progress flag for the key. Call when starting/ending
// processing while holding the process mutex.
func (p *ProgressGuard) SetInProgress(key string, inProgress bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.state[key] == nil {
		p.state[key] = &progressState{}
	}
	p.state[key].InProgress = inProgress
}

// DrainQueue returns and clears the queue for the key. Caller should process
// each message. Must be called while holding the process mutex.
func (p *ProgressGuard) DrainQueue(key string) []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.state[key]
	if s == nil || len(s.Queue) == 0 {
		return nil
	}
	out := s.Queue
	s.Queue = nil
	return out
}
