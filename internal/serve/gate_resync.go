package serve

import (
	"log/slog"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/burst"
)

// resyncGates resends every live head-ledger rate-limit gate to every
// gate-eligible node that has not heard it: a node verified after the gate
// was recorded hears it on the next tick, with no new traffic needed. Only
// rate limits travel -- a spawn failure says only this machine could not
// start a candidate, so it is never exported. Cleared and expired gates are
// never sent: absent from the live set, their heard rows are forgotten, not
// transmitted, so no clear hook is needed. A failed node is unmarked and
// retried next tick, and its failure only logs.
func (s *Server) resyncGates(now time.Time) {
	t := s.copyBurstTable()
	if t == nil {
		return
	}
	rows := s.gateFanoutTargets()

	l, err := availability.LoadLedger(s.gates)
	if err != nil {
		slog.Warn("burst: gate resync skipped, ledger unreadable", "err", err)
		return
	}
	var live []availability.Entry
	for _, e := range l.Prune(now).Entries {
		if e.Kind == availability.RateLimited {
			live = append(live, e)
		}
	}

	s.pruneHeard(t, live)
	if len(rows) == 0 || len(live) == 0 {
		return
	}
	cl, err := s.burstNodeClient(rows)
	if err != nil {
		slog.Warn("burst: gate resync skipped, client key unusable", "err", err)
		return
	}
	join := s.gateFanoutHook
	for _, row := range rows {
		for _, e := range live {
			if s.gateHeardBy(row.ID, e.Subject, e.Until) {
				continue
			}
			node, subject, until := row.ID, e.Subject, e.Until
			gate := targetGateUnavailable{token: e.Subject, reason: e.Note, until: e.Until}
			hook := func(err error) {
				if err == nil {
					s.markGateHeard(node, subject, until)
				}
				if join != nil {
					join(err)
				}
			}
			s.fanoutDetached(hook, func() error { return s.gateOnNode(cl, node, gate) })
		}
	}
}

// pruneHeard forgets what the pass cannot use: nodes the table no longer
// lists, and subjects no live gate holds. A cleared or expired gate is
// forgotten here, never transmitted.
func (s *Server) pruneHeard(t *burst.NodeTable, live []availability.Entry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.heard == nil {
		s.heard = make(map[string]map[string]time.Time)
		return
	}
	members := make(map[string]bool, len(t.Nodes))
	for _, r := range t.Nodes {
		members[r.ID] = true
	}
	subjects := make(map[string]bool, len(live))
	for _, e := range live {
		subjects[e.Subject] = true
	}
	for node, heard := range s.heard {
		if !members[node] {
			delete(s.heard, node)
			continue
		}
		for subject := range heard {
			if !subjects[subject] {
				delete(heard, subject)
			}
		}
		if len(heard) == 0 {
			delete(s.heard, node)
		}
	}
}

// gateHeardBy reports whether node already holds subject at until.
func (s *Server) gateHeardBy(node, subject string, until time.Time) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	have, ok := s.heard[node][subject]
	return ok && have.Equal(until)
}

// markGateHeard records that node's attempt landed, so the next pass skips
// it. Only success marks: a failed node retries next tick.
func (s *Server) markGateHeard(node, subject string, until time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.heard == nil {
		s.heard = make(map[string]map[string]time.Time)
	}
	m, ok := s.heard[node]
	if !ok {
		m = make(map[string]time.Time)
		s.heard[node] = m
	}
	m[subject] = until
}
