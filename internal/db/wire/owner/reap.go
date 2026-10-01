//go:build unix

package owner

import "time"

// defaultReapGrace is how long the owner lets an abandoned statement's rollback
// and discard run before it asks the daemon to reap by re-exec. The engine
// cannot interrupt the statement, so it keeps a core, a read transaction and
// two goroutines for its whole run; the grace is the compromise between
// reaping an ordinary slow rollback and letting a runaway statement hold a core
// forever.
const defaultReapGrace = 30 * time.Second

// reapGrace is the grace in force, a var so a test can shrink it the way it
// shrinks the client's handshake budget. It is read when a statement is
// registered, so a change affects later registrations only.
var reapGrace = defaultReapGrace

// SetReapGrace sets the grace a later abandoned statement is reaped after. A
// non-positive d restores defaultReapGrace.
func SetReapGrace(d time.Duration) {
	if d <= 0 {
		d = defaultReapGrace
	}
	reapGrace = d
}

// noteAbandoned registers finish -- the channel closed when a pinned
// connection's rollback and discard finished -- as the abandoned statement. At
// most one statement is tracked: a registration made while one is live is
// dropped, so a second client teardown cannot replace the statement the owner
// is already ending. The registration clears itself if finish closes first, so
// a statement that ends before the grace is never reaped.
func (s *Server) noteAbandoned(finish <-chan struct{}) {
	s.reapMu.Lock()
	if s.reapPending != nil {
		s.reapMu.Unlock()
		return
	}
	s.reapPending = finish
	s.reapFired = false
	s.reapMu.Unlock()

	go s.awaitReapGrace(finish)
}

// awaitReapGrace waits out the grace on one registration and fires the reap
// when the statement still has not finished.
func (s *Server) awaitReapGrace(finish <-chan struct{}) {
	select {
	case <-finish:
		s.clearAbandoned(finish)
		return
	case <-time.After(reapGrace):
	}
	s.fireReap(finish)
}

// fireReap calls the hook once for one registration. A registration that
// already fired, or that a racing clear replaced, is not fired again, so the
// daemon re-execs at most once per abandoned statement.
func (s *Server) fireReap(finish <-chan struct{}) {
	s.reapMu.Lock()
	if s.reapPending != finish || s.reapFired {
		s.reapMu.Unlock()
		return
	}
	s.reapFired = true
	hook := s.OnAbandoned
	s.reapMu.Unlock()
	if hook != nil {
		hook()
	}
}

// clearAbandoned drops the registration when finish closes: the statement
// finished after all, so ad-hoc reads resume and no hook fires.
func (s *Server) clearAbandoned(finish <-chan struct{}) {
	s.reapMu.Lock()
	if s.reapPending == finish {
		s.reapPending = nil
		s.reapFired = false
	}
	s.reapMu.Unlock()
}

// reaping reports whether an abandoned statement is registered now, which is
// what refuses a request on an ad-hoc connection.
func (s *Server) reaping() bool {
	s.reapMu.Lock()
	defer s.reapMu.Unlock()
	return s.reapPending != nil
}
