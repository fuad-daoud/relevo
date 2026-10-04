package sync

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync/atomic"
	"testing"
	"time"
)

// runnerNow is a fixed clock, so a marker's timestamp is not what the test
// turns on.
var runnerNow = time.Date(2026, 10, 4, 10, 0, 0, 0, time.UTC)

// blackhole answers nothing until its context expires, which is what a network
// that accepts a connection and then goes silent looks like from here. It is
// the client a test needs to prove the bound is load-bearing.
type blackhole struct {
	calls atomic.Int64
	// block is how long a call pretends to work before answering. It is set far
	// past any timeout under test, so the context is always what ends the call.
	block time.Duration
}

func (b *blackhole) wait(ctx context.Context) error {
	b.calls.Add(1)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(b.block):
		return nil
	}
}

func (b *blackhole) Push(ctx context.Context) error           { return b.wait(ctx) }
func (b *blackhole) Pull(ctx context.Context) (bool, error)   { return false, b.wait(ctx) }
func (b *blackhole) Stats(ctx context.Context) (Stats, error) { return Stats{}, b.wait(ctx) }
func (b *blackhole) Checkpoint(ctx context.Context) error     { return b.wait(ctx) }

// newRunner wires a runner over a local file and the given client, with the
// sync-enabled marker already written so the triggers would take it.
func newRunner(t *testing.T, client SyncClient) (*Runner, Local) {
	t.Helper()
	_, _, local := openSplit(t)
	putMarker(t, local, KeyEnabled, `true`)
	return &Runner{Client: client, Local: local, Now: func() time.Time { return runnerNow }}, local
}

// TestSyncPushThenPullOrder pins the one ordering decision both triggers share:
// push first, then pull, then read the stats off the back of it. Pulling first
// would give every unpushed local change to roll back and replay.
func TestSyncPushThenPullOrder(t *testing.T) {
	t.Parallel()

	f := &Fake{}
	r, _ := newRunner(t, f)

	out := r.SyncOnce(t.Context())
	if out.Err != nil {
		t.Fatalf("SyncOnce: %v", out.Err)
	}

	want := []string{"push", "pull", "stats"}
	if len(f.Calls) != len(want) {
		t.Fatalf("calls = %v, want %v", f.Calls, want)
	}
	for i, name := range want {
		if f.Calls[i] != name {
			t.Fatalf("calls = %v, want %v", f.Calls, want)
		}
	}
}

// TestSyncRetryAfterFailure pins that a failure is a marker and nothing more: no
// latch holds the machine down, so the next attempt runs in full and clears the
// marker it found.
func TestSyncRetryAfterFailure(t *testing.T) {
	t.Parallel()

	boom := errors.New("the network is gone")
	f := &Fake{PushErr: boom}
	r, local := newRunner(t, f)

	if out := r.SyncOnce(t.Context()); out.Err == nil {
		t.Fatal("the first attempt reported no error")
	}
	token, err := StatusToken(local)
	if err != nil {
		t.Fatalf("StatusToken: %v", err)
	}
	if token != TokenBehind {
		t.Errorf("token after a failed attempt = %q, want %q", token, TokenBehind)
	}

	// The next attempt gets a working remote and a real backlog to record.
	f.PushErr = nil
	f.Reported = Stats{CdcOperations: 12, Revision: "rev-2"}
	out := r.SyncOnce(t.Context())
	if out.Err != nil {
		t.Fatalf("the second attempt: %v", out.Err)
	}
	if token, err = StatusToken(local); err != nil {
		t.Fatalf("StatusToken: %v", err)
	} else if token != TokenOK {
		t.Errorf("token after a recovered attempt = %q, want %q", token, TokenOK)
	}
	if got := readStats(t, local); got != f.Reported {
		t.Errorf("stored stats = %+v, want %+v", got, f.Reported)
	}
}

// TestSyncMarkersOnEveryOutcome is the whole marker contract as one table: the
// exact token each outcome leaves behind, read back the way the statusline
// reads it.
func TestSyncMarkersOnEveryOutcome(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		// enabled is what the local marker says, which is what makes a machine
		// off rather than merely unconfigured.
		enabled bool
		// build returns the client for the row.
		build func() SyncClient
		want  string
	}{
		{
			name:    "an empty backlog after a good attempt is ok",
			enabled: true,
			build:   func() SyncClient { return &Fake{} },
			want:    TokenOK,
		},
		{
			name:    "a backlog past the threshold is behind",
			enabled: true,
			build: func() SyncClient {
				return &Fake{Reported: Stats{CdcOperations: BacklogThreshold + 1}}
			},
			want: TokenBehind,
		},
		{
			name:    "a transport failure is behind",
			enabled: true,
			build:   func() SyncClient { return &Fake{PushErr: errors.New("dial tcp: no route")} },
			want:    TokenBehind,
		},
		{
			name:    "a refused token needs a human, so it is an error",
			enabled: true,
			build:   func() SyncClient { return &Fake{PushErr: ErrAuthRefused} },
			want:    TokenErr,
		},
		{
			// Sync turned off is the only thing that reads as off. A machine
			// with no client is a misconfiguration, and reads as behind rather
			// than as an intentional choice.
			name:    "a machine with sync turned off is off",
			enabled: false,
			build:   func() SyncClient { return &Fake{} },
			want:    TokenOff,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, _, local := openSplit(t)
			putMarker(t, local, KeyEnabled, strconv.FormatBool(tc.enabled))
			r := &Runner{
				Client: tc.build(),
				Local:  local,
				Now:    func() time.Time { return runnerNow },
			}

			r.SyncOnce(t.Context())

			got, err := StatusToken(local)
			if err != nil {
				t.Fatalf("StatusToken: %v", err)
			}
			if got != tc.want {
				t.Errorf("token = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestSyncAuthRefusalClearsOnTheNextGoodAttempt pins that the attention marker
// is not a latch: it names an unblockable failure, so an attempt that succeeds
// has to take it back down.
func TestSyncAuthRefusalClearsOnTheNextGoodAttempt(t *testing.T) {
	t.Parallel()

	f := &Fake{PushErr: ErrAuthRefused}
	r, local := newRunner(t, f)
	r.SyncOnce(t.Context())

	if _, ok, err := local.KVGet(KeyAttention); err != nil {
		t.Fatalf("KVGet(attention): %v", err)
	} else if !ok {
		t.Fatal("a refused token wrote no attention marker")
	}

	f.PushErr = nil
	r.SyncOnce(t.Context())

	if _, ok, err := local.KVGet(KeyAttention); err != nil {
		t.Fatalf("KVGet(attention): %v", err)
	} else if ok {
		t.Error("the attention marker survived a good attempt")
	}
}

// TestSyncOnceIsBounded pins the bound itself: a remote that accepts a
// connection and then goes silent costs one timeout and leaves a marker saying
// so, rather than hanging the caller that started it.
func TestSyncOnceIsBounded(t *testing.T) {
	t.Parallel()

	b := &blackhole{block: time.Minute}
	_, _, local := openSplit(t)
	putMarker(t, local, KeyEnabled, `true`)
	r := &Runner{Client: b, Local: local, Timeout: 50 * time.Millisecond}

	start := time.Now()
	out := r.SyncOnce(t.Context())
	took := time.Since(start)

	if out.Err == nil {
		t.Error("a blackholed remote produced no error")
	}
	if out.OK {
		t.Error("a blackholed remote produced a good outcome")
	}
	// Generous next to the 50ms bound, so a loaded machine does not read as a
	// hang, and far below the minute the remote is pretending to work for.
	if took > 5*time.Second {
		t.Errorf("SyncOnce took %v, so the bound is not load-bearing", took)
	}
	token, err := StatusToken(local)
	if err != nil {
		t.Fatalf("StatusToken: %v", err)
	}
	if token != TokenBehind {
		t.Errorf("token = %q, want %q", token, TokenBehind)
	}
}

// TestSyncSurfacesStatsToLocalKV pins that everything the remote reported
// reaches the local file the sync view will read: the backlog, the bytes each
// way, the revision and the last push and pull times.
func TestSyncSurfacesStatsToLocalKV(t *testing.T) {
	t.Parallel()

	reported := Stats{
		CdcOperations:        7,
		LastPullUnixTime:     1_700_000_001,
		LastPushUnixTime:     1_700_000_002,
		NetworkSentBytes:     4096,
		NetworkReceivedBytes: 8192,
		Revision:             "opaque-revision-value",
	}
	r, local := newRunner(t, &Fake{Reported: reported, Applied: true})

	out := r.SyncOnce(t.Context())
	if !out.Applied {
		t.Error("Applied = false, want the pull to report a rebase")
	}
	if got := readStats(t, local); got != reported {
		t.Errorf("stored stats = %+v, want %+v", got, reported)
	}

	body, ok, err := local.KVGet(KeyBacklog)
	if err != nil || !ok {
		t.Fatalf("KVGet(backlog): ok=%v err=%v", ok, err)
	}
	if string(body) != "7" {
		t.Errorf("backlog marker = %s, want 7", body)
	}
}

// TestSyncRefusesWithNothingToDrive pins that a runner with no client refuses
// rather than reporting a good outcome, which is what a caller that forgot to
// check would otherwise record on the statusline.
func TestSyncRefusesWithNothingToDrive(t *testing.T) {
	t.Parallel()

	_, _, local := openSplit(t)
	putMarker(t, local, KeyEnabled, `true`)

	var r *Runner
	if r.Enabled() {
		t.Error("a nil runner reports itself enabled")
	}
	if r.On() {
		t.Error("a nil runner reports itself on")
	}

	r = &Runner{Local: local}
	if out := r.SyncOnce(t.Context()); !errors.Is(out.Err, errNoSync) {
		t.Errorf("SyncOnce err = %v, want %v", out.Err, errNoSync)
	}
}

// readStats reads the stored snapshot back out of the local file.
func readStats(t *testing.T, local Local) Stats {
	t.Helper()
	body, ok, err := local.KVGet(KeyStats)
	if err != nil || !ok {
		t.Fatalf("KVGet(stats): ok=%v err=%v", ok, err)
	}
	var got Stats
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal stats: %v", err)
	}
	return got
}
