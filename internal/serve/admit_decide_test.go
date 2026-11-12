package serve

import (
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/remote"
)

// TestDecide pins the admission decision: ceilings hold first, then the
// pressure brake, then the memory rule with K times the largest burst, and an
// oversize candidate alone in its class starts because nothing it waits on
// would free a seat.
func TestDecide(t *testing.T) {
	const gb = int64(1 << 30)
	builder := seatCost{Base: 1 * gb, Burst: 5 * gb}
	planner := seatCost{Base: gb / 2, Burst: 1 * gb}
	tests := []struct {
		name    string
		running []seatCost
		cand    seatCost
		budget  int64
		k       int
		class   int
		ceiling int
		brake   bool
		start   bool
		wait    string
		bytes   int64
	}{
		{
			name: "exact fit starts", running: []seatCost{builder}, cand: builder,
			budget: 12 * gb, k: 2, class: 0, ceiling: 4, brake: true, start: true,
		},
		{
			name: "one byte over is held", running: []seatCost{builder}, cand: builder,
			budget: 12*gb - 1, k: 2, class: 0, ceiling: 4, brake: true,
			start: false, wait: waitMemory, bytes: 1,
		},
		{
			name: "k below one reads as one", running: []seatCost{{Base: gb, Burst: gb}}, cand: seatCost{Base: gb, Burst: gb},
			budget: 3 * gb, k: 0, class: 0, ceiling: 4, brake: true, start: true,
		},
		{
			name: "k term is decisive", running: []seatCost{{Base: gb, Burst: gb}}, cand: seatCost{Base: gb, Burst: gb},
			budget: 3 * gb, k: 2, class: 0, ceiling: 4, brake: true,
			start: false, wait: waitMemory, bytes: gb,
		},
		{
			name: "peak is the max of running and candidate", running: []seatCost{{Burst: 5 * gb}}, cand: seatCost{Burst: gb},
			budget: 5 * gb, k: 1, class: 0, ceiling: 4, brake: true, start: true,
		},
		{
			name: "ceiling holds at the limit before memory", running: []seatCost{builder}, cand: builder,
			budget: 0, k: 2, class: 1, ceiling: 1, brake: true,
			start: false, wait: waitCeiling,
		},
		{
			name: "ceiling holds over the limit", running: []seatCost{builder, builder}, cand: builder,
			budget: 100 * gb, k: 2, class: 2, ceiling: 1, brake: true,
			start: false, wait: waitCeiling,
		},
		{
			name: "pressure holds without consulting memory", running: nil, cand: builder,
			budget: 100 * gb, k: 2, class: 0, ceiling: 4, brake: false,
			start: false, wait: waitPressure,
		},
		{
			name: "pressure clear admits a fit", running: nil, cand: builder,
			budget: 100 * gb, k: 2, class: 0, ceiling: 4, brake: true, start: true,
		},
		{
			name: "oversize alone starts", running: nil, cand: builder,
			budget: 10 * gb, k: 2, class: 0, ceiling: 4, brake: true, start: true,
		},
		{
			name:    "oversize with only the other class running still starts",
			running: []seatCost{builder}, cand: builder,
			budget: 10 * gb, k: 2, class: 0, ceiling: 4, brake: true, start: true,
		},
		{
			name:    "oversize with same-class running is memory-held",
			running: []seatCost{builder}, cand: builder,
			budget: 10 * gb, k: 2, class: 1, ceiling: 4, brake: true,
			start: false, wait: waitMemory, bytes: 2 * gb,
		},
		{
			name:    "planner costs are memory-checked too",
			running: []seatCost{{Base: 2 * gb, Burst: gb}}, cand: planner,
			budget: 4 * gb, k: 2, class: 0, ceiling: 4, brake: true,
			start: false, wait: waitMemory, bytes: gb / 2,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			start, wait, bytes := decide(tc.running, tc.cand, tc.budget, tc.k, tc.class, tc.ceiling, tc.brake)
			if start != tc.start || wait != tc.wait || bytes != tc.bytes {
				t.Fatalf("decide() = (%v, %q, %d), want (%v, %q, %d)",
					start, wait, bytes, tc.start, tc.wait, tc.bytes)
			}
		})
	}
}

// TestWaitedTooLong pins the fairness horizon: a zero stamp never blocks, and
// the block starts at ten minutes.
func TestWaitedTooLong(t *testing.T) {
	now := time.Now()
	tests := []struct {
		name     string
		queuedAt time.Time
		want     bool
	}{
		{name: "zero never blocks", queuedAt: time.Time{}, want: false},
		{name: "nine minutes lets the class through", queuedAt: now.Add(-9 * time.Minute), want: false},
		{name: "ten minutes blocks", queuedAt: now.Add(-10 * time.Minute), want: true},
		{name: "eleven minutes blocks", queuedAt: now.Add(-11 * time.Minute), want: true},
		{name: "future never blocks", queuedAt: now.Add(time.Minute), want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := waitedTooLong(tc.queuedAt, now); got != tc.want {
				t.Fatalf("waitedTooLong(%v) = %v, want %v", tc.queuedAt, got, tc.want)
			}
		})
	}
}

// TestQueueViewForWaits pins the wait stamping: a held round carries its
// reason and shortfall, a round the pass would start carries none, and a
// waits slice short of the queue reads as unstated rather than panicking.
func TestQueueViewForWaits(t *testing.T) {
	now := time.Now()
	c := census{
		Running: 1, BuilderCap: 2, PlannersRunning: 1, PlannerCap: 1,
		Queued: []queuedRound{
			{Owner: "a", Name: "held", QueuedAt: now},
			{Owner: "b", Name: "free", QueuedAt: now},
			{Owner: "c", Name: "plan", QueuedAt: now, Planner: true},
		},
	}
	v := remote.BuildersView{Cap: 2, PlannersCap: 1}
	waits := []queueWait{{Wait: waitMemory, WaitBytes: 7}, {}, {}}

	held := queueViewFor(c.Queued[0], 0, c, v, waits)
	if held.Wait != waitMemory || held.WaitBytes != 7 {
		t.Errorf("held wait = (%q, %d), want (%q, 7)", held.Wait, held.WaitBytes, waitMemory)
	}
	if held.Running != 1 || held.Cap != 2 {
		t.Errorf("held counts = running %d cap %d, want 1 and 2", held.Running, held.Cap)
	}
	if free := queueViewFor(c.Queued[1], 1, c, v, waits); free.Wait != "" || free.WaitBytes != 0 {
		t.Errorf("startable wait = (%q, %d), want unstated", free.Wait, free.WaitBytes)
	}
	if short := queueViewFor(c.Queued[0], 0, c, v, nil); short.Wait != "" {
		t.Errorf("short-slice wait = %q, want unstated", short.Wait)
	}
	if plan := queueViewFor(c.Queued[2], 2, c, v, waits); plan.Running != 1 || plan.Cap != 1 {
		t.Errorf("planner counts = running %d cap %d, want 1 and 1", plan.Running, plan.Cap)
	}
}

// TestQueueWaitsChargesRoundsThePassWouldStart pins that the simulated pass
// charges a round it would start before weighing the next, as the real pass
// does: the second of two rounds that fit only one at a time reads a memory
// hold for the first one's base, not a clean start.
func TestQueueWaitsChargesRoundsThePassWouldStart(t *testing.T) {
	const gb = int64(1 << 30)
	c := census{
		BuilderCap: 5, PlannerCap: 4,
		Queued: []queuedRound{
			{Name: "a", Base: gb, Burst: 5 * gb},
			{Name: "b", Base: gb, Burst: 5 * gb},
		},
	}
	got := queueWaits(c, 11*gb, 2, true, time.Now())
	if got[0] != (queueWait{}) {
		t.Errorf("a wait = %+v, want none: it fits alone", got[0])
	}
	if want := (queueWait{Wait: waitMemory, WaitBytes: gb}); got[1] != want {
		t.Errorf("b wait = %+v, want %+v behind a's seat", got[1], want)
	}
}

// TestReservedBytesSumsBasesAndLargestBurst pins the builders view's seat
// reservation: no rounds reserve nothing, one round holds its base plus K
// times its burst, several rounds hold every base plus K times the largest
// burst wherever it sits, and a K below one reads as one the way decide
// clamps it. Planners count: the census carries them in RunningRounds.
//
// Mutation: drop the int64(k)*peak term and the multi-round rows read only
// their bases.
func TestReservedBytesSumsBasesAndLargestBurst(t *testing.T) {
	t.Parallel()
	const gb = int64(1 << 30)
	for _, tc := range []struct {
		name   string
		rounds []runningRound
		k      int
		want   int64
	}{
		{"no rounds reserve nothing", nil, 2, 0},
		{"one round", []runningRound{{Base: gb, Burst: 5 * gb}}, 2, 11 * gb},
		{
			"largest burst in the middle",
			[]runningRound{{Base: gb, Burst: gb}, {Base: 2 * gb, Burst: 5 * gb}, {Base: gb, Burst: 2 * gb}},
			2, 14 * gb,
		},
		{
			"planners hold seats too",
			[]runningRound{{Base: gb, Burst: gb}, {Planner: true, Base: gb / 2, Burst: 3 * gb}},
			1, 9 * gb / 2,
		},
		{"k below one reads as one", []runningRound{{Base: gb, Burst: 3 * gb}}, 0, 4 * gb},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := reservedBytes(tc.rounds, tc.k); got != tc.want {
				t.Errorf("reservedBytes(%+v, %d) = %d, want %d", tc.rounds, tc.k, got, tc.want)
			}
		})
	}
}

// TestBuildersViewForStampsItsSeatArguments pins that the builders view
// carries the caller's own budget and K alongside the census counts, so the
// view and the decision read one pair of numbers.
func TestBuildersViewForStampsItsSeatArguments(t *testing.T) {
	t.Parallel()
	const gb = int64(1 << 30)
	env := setupTestEnv(t)
	c := census{
		Running: 2, PlannersRunning: 1, BuilderCap: 4, PlannerCap: 2,
		RunningRounds: []runningRound{{Base: gb, Burst: 2 * gb}, {Planner: true, Base: gb / 2, Burst: gb}},
	}
	const budget = 12 * gb
	got := env.srv.buildersViewFor(c, budget, 2)
	if got.ReservedBytes != 11*gb/2 {
		t.Errorf("ReservedBytes = %d, want %d", got.ReservedBytes, 11*gb/2)
	}
	if got.BudgetBytes != budget {
		t.Errorf("BudgetBytes = %d, want %d", got.BudgetBytes, budget)
	}
	if got.K != 2 {
		t.Errorf("K = %d, want 2", got.K)
	}
	if got.Running != 2 || got.PlannersRunning != 1 {
		t.Errorf("counts = running %d planners %d, want 2 and 1", got.Running, got.PlannersRunning)
	}
}
