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
// connection's rollback and discard finished -- as an abandoned statement.
// Every registration is kept: two clients can abandon statements at once, and
// dropping the second would let its runaway escape the refusal and the reap as
// soon as the first clears. A registration clears itself if finish closes
// first, so a statement that ends before the grace is never reaped.
func (s *Server) noteAbandoned(finish <-chan struct{}) {
	s.reapMu.Lock()
	if s.reapPending == nil {
		s.reapPending = make(map[<-chan struct{}]struct{})
	}
	s.reapPending[finish] = struct{}{}
	s.reapMu.Unlock()

	go s.awaitReapGrace(finish)
}

// awaitReapGrace waits out the grace on one registration and fires the reap
// when that statement still has not finished.
func (s *Server) awaitReapGrace(finish <-chan struct{}) {
	select {
	case <-finish:
		s.clearAbandoned(finish)
		return
	case <-time.After(reapGrace):
	}
	s.fireReap()
}

// fireReap calls the hook at most once. One re-exec ends every abandoned
// statement, so the first registration to outlive the grace is enough; a
// later one does not ask again.
func (s *Server) fireReap() {
	s.reapMu.Lock()
	if s.reapFired || len(s.reapPending) == 0 {
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

// clearAbandoned drops one registration when its finish closes: the statement
// finished after all, so it no longer refuses ad-hoc reads and no hook fires
// for it.
func (s *Server) clearAbandoned(finish <-chan struct{}) {
	s.reapMu.Lock()
	delete(s.reapPending, finish)
	s.reapMu.Unlock()
}

// reaping reports whether at least one abandoned statement is registered now,
// which is what refuses a request on an ad-hoc connection.
func (s *Server) reaping() bool {
	s.reapMu.Lock()
	defer s.reapMu.Unlock()
	return len(s.reapPending) > 0
}
