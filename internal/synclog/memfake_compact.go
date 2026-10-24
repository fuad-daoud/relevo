package synclog

import "fmt"

// PullRange returns one origin's entries with after < seq <= upto.
func (m *MemTransport) PullRange(origin string, after, upto int) ([]Entry, error) {
	m.log.mu.Lock()
	defer m.log.mu.Unlock()

	var out []Entry
	for _, e := range m.log.entries {
		if e.Origin == origin && e.Seq > after && e.Seq <= upto {
			out = append(out, e)
		}
	}
	return out, nil
}

// CompactOwn compacts this handle's own origin.
func (m *MemTransport) CompactOwn(floor int) (CompactResult, error) {
	dropped, err := m.Compact(m.origin, floor)
	if err != nil {
		return CompactResult{}, err
	}
	// The worker pushes the compaction unconditionally once it commits, so a
	// completed compact counts a transfer whatever it dropped.
	m.pushes.Add(1)
	m.log.mu.Lock()
	defer m.log.mu.Unlock()
	return CompactResult{Dropped: dropped, Floor: m.log.floors[m.origin]}, nil
}

// Compact drops origin's superseded entries up to floor and raises the origin's
// recorded floor to it, under one lock so no reader sees one without the other.
// The floor only rises; a lower one drops what it covers and records nothing.
//
// A floor past the origin's head is refused, and one that falls inside a batch
// is moved down to the end of the batch before it, as the worker does.
func (m *MemTransport) Compact(origin string, floor int) (int, error) {
	l := m.log
	l.mu.Lock()
	defer l.mu.Unlock()

	if floor < 0 || floor > l.latestSeq(origin) {
		return 0, fmt.Errorf("synclog: compact %s to %d: past the log's head: %w", origin, floor, ErrInvalid)
	}
	var own []Entry
	aligned := floor
	for _, e := range l.entries {
		if e.Origin == origin {
			if e.Batch <= floor && e.Seq > floor {
				aligned = min(aligned, e.Batch-1)
			}
			own = append(own, e)
		}
	}
	floor = aligned
	drop := compactDrops(own, floor)
	kept := l.entries[:0:0]
	for _, e := range l.entries {
		if e.Origin == origin && drop[e.Seq] {
			continue
		}
		kept = append(kept, e)
	}
	l.entries = kept
	if l.floors == nil {
		l.floors = make(map[string]int)
	}
	l.floors[origin] = max(l.floors[origin], floor)
	return len(drop), nil
}
