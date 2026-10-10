package syncworker

// The byte totals. What is measured here is the difference between the engine's
// cumulative counters either side of one transfer, and the sum of those over a
// worker's life -- because the counters alone are not the answer: they describe
// the replica since it was written, including transfers a previous worker made.

import (
	"context"
	"errors"
	"testing"

	turso "turso.tech/database/tursogo"
)

// TestBytesDeltaCountsWhatOneTransferMoved pins the rule in both directions: a
// counter that rose gives the difference, and a counter that went down is a
// reset rather than a lost transfer, so the reading after it is counted whole.
//
// The mutation is returning zeros, which would make every reply report that the
// month cost nothing.
func TestBytesDeltaCountsWhatOneTransferMoved(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name             string
		before, after    turso.TursoSyncDbStats
		wantSent, wantRe int64
	}{
		{
			name:     "a push that sent bytes",
			before:   turso.TursoSyncDbStats{NetworkSentBytes: 100, NetworkReceivedBytes: 40},
			after:    turso.TursoSyncDbStats{NetworkSentBytes: 160, NetworkReceivedBytes: 40},
			wantSent: 60,
		},
		{
			name:   "a pull that received bytes",
			before: turso.TursoSyncDbStats{NetworkSentBytes: 100, NetworkReceivedBytes: 40},
			after:  turso.TursoSyncDbStats{NetworkSentBytes: 100, NetworkReceivedBytes: 900},
			wantRe: 860,
		},
		{
			name:   "a counter that went down is a reset, not a loss",
			before: turso.TursoSyncDbStats{NetworkSentBytes: 500, NetworkReceivedBytes: 500},
			after:  turso.TursoSyncDbStats{NetworkSentBytes: 30, NetworkReceivedBytes: 0},
			// The whole of the post-reset reading is what this worker moved;
			// subtracting the pre-reset total would report a negative.
			wantSent: 30,
		},
		{
			name: "a transfer that moved nothing",
			before: turso.TursoSyncDbStats{NetworkSentBytes: 100,
				NetworkReceivedBytes: 100},
			after: turso.TursoSyncDbStats{NetworkSentBytes: 100,
				NetworkReceivedBytes: 100},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sent, recv := bytesDelta(tc.before, tc.after)
			if sent != tc.wantSent || recv != tc.wantRe {
				t.Errorf("bytesDelta = %d/%d, want %d/%d", sent, recv, tc.wantSent, tc.wantRe)
			}
		})
	}
}

// TestDriverStatsDeltaCountsTursoBytes pins the accumulation above the rule: the
// backend adds each transfer's difference to a running total, driven through a
// fake syncDb standing in for the engine. This is the counter the daemon adds to
// what the month has cost, so a total that counted nothing would bill nothing.
func TestDriverStatsDeltaCountsTursoBytes(t *testing.T) {
	t.Parallel()
	fake := &fakeSyncDb{}
	driver := newTursoDriver("")
	driver.sdb = fake
	backend := &TursoBackend{driver: driver}

	// Each transfer raises the engine's counters by what it moved. The totals
	// are what the sum of the differences comes to, not the final reading: a
	// worker that booted against a replica which had already moved 10,000 bytes
	// has moved none of them itself.
	fake.reading = turso.TursoSyncDbStats{NetworkSentBytes: 10_000, NetworkReceivedBytes: 10_000}
	if err := backend.transfer(t.Context(), func(context.Context) error {
		fake.reading = turso.TursoSyncDbStats{NetworkSentBytes: 10_300, NetworkReceivedBytes: 10_000}
		return nil
	}); err != nil {
		t.Fatalf("push transfer: %v", err)
	}
	if err := backend.transfer(t.Context(), func(context.Context) error {
		fake.reading = turso.TursoSyncDbStats{NetworkSentBytes: 10_300, NetworkReceivedBytes: 10_900}
		return nil
	}); err != nil {
		t.Fatalf("pull transfer: %v", err)
	}

	if backend.bytes.TursoSent != 300 {
		t.Errorf("turso_sent = %d, want 300 across both transfers", backend.bytes.TursoSent)
	}
	if backend.bytes.TursoReceived != 900 {
		t.Errorf("turso_received = %d, want 900", backend.bytes.TursoReceived)
	}
}

// TestTransferCountsAcrossAStatsCallThatFails pins that a transfer whose
// measurement could not be taken contributes no number rather than a wrong one,
// and that counting resumes from the last reading that was actually observed.
//
// The two halves matter in opposite directions. A failed reading must not be
// taken for a zero counter -- that would subtract the whole running total on the
// next difference, or count a transfer that moved nothing as though it had moved
// everything since the replica was written. And a transfer nobody could measure
// must not be guessed at: with no anchor on either side there is no difference to
// compute, so the honest total leaves it out and the next successful reading
// starts from what it really saw.
func TestTransferCountsAcrossAStatsCallThatFails(t *testing.T) {
	t.Parallel()
	fake := &fakeSyncDb{reading: turso.TursoSyncDbStats{NetworkSentBytes: 100}}
	driver := newTursoDriver("")
	driver.sdb = fake
	backend := &TursoBackend{driver: driver}

	// The push moves 40 bytes, and every reading that would show it fails.
	fake.readingErr = errors.New("the engine could not be asked")
	if err := backend.transfer(t.Context(), func(context.Context) error {
		fake.reading = turso.TursoSyncDbStats{NetworkSentBytes: 140}
		return nil
	}); err != nil {
		t.Fatalf("a transfer whose measurement failed: %v", err)
	}
	if backend.bytes.TursoSent != 0 {
		t.Errorf("turso_sent = %d after an unmeasurable transfer, want 0", backend.bytes.TursoSent)
	}

	// Counting resumes at the next reading that succeeded: from 140 to 200 is
	// this transfer's 60, and not the 100 the replica has moved since it was
	// written.
	fake.readingErr = nil
	if err := backend.transfer(t.Context(), func(context.Context) error {
		fake.reading = turso.TursoSyncDbStats{NetworkSentBytes: 200}
		return nil
	}); err != nil {
		t.Fatalf("transfer: %v", err)
	}
	if backend.bytes.TursoSent != 60 {
		t.Errorf("turso_sent = %d, want 60: only what a reading on both ends saw", backend.bytes.TursoSent)
	}
}

// TestWorkerRefusesWrongProtocolVersion pins the handshake gate: a worker on
// version 2 refuses a daemon on any other number, naming both, because every
// answer after it would be in a vocabulary the daemon reads wrongly.
//
// The mutation is accepting version 1, which would let a version 1 daemon start
// a version 2 worker and discover the difference at the first put_blob, after
// the exporter had written entries pointing at objects nobody uploaded.
func TestWorkerRefusesWrongProtocolVersion(t *testing.T) {
	t.Parallel()
	backend := &fakeBackend{}
	for _, version := range []int{1, 3, 0} {
		s := &server{backend: backend}
		resp := s.dispatch(Request{
			ID: "1", Verb: VerbHello, Version: version,
			Origin: "origin-a", URL: "libsql://x", Token: "t",
		})
		if resp.OK {
			t.Errorf("version %d was accepted", version)
			continue
		}
		// Both numbers have to be in the message: the reader has to know which
		// side is the one to change.
		if !contains(resp.Error, "pipe version") {
			t.Errorf("version %d refusal %q does not name what arrived", version, resp.Error)
		}
	}
	if ProtocolVersion != 2 {
		t.Errorf("ProtocolVersion = %d, want 2", ProtocolVersion)
	}
}

// contains reports whether s holds sub, without pulling in strings for one call.
func contains(s, sub string) bool {
	if len(sub) > len(s) {
		return false
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
