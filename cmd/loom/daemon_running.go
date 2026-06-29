package main

import "sync"

// runningSet tracks the schedule ids with a fire in flight, so the daemon's
// overlap policy can skip or serialize them. It is the daemon's only shared
// mutable state across the scan loop and the goroutines launched by execute, so
// it carries its own mutex rather than leaning on the daemon's.
type runningSet struct {
	mu sync.Mutex
	m  map[string]bool
}

func newRunningSet() *runningSet {
	return &runningSet{m: map[string]bool{}}
}

// active reports whether id currently has a fire in flight.
func (s *runningSet) active(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.m[id]
}

// mark records id as having a fire in flight.
func (s *runningSet) mark(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[id] = true
}

// clear records id as no longer in flight.
func (s *runningSet) clear(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, id)
}
