package sync

import (
	"context"
	"errors"
	"path/filepath"
	gosync "sync"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

const adoptedID = "inst-x"

// adoptLater is a clock past the quiet window, so the identity a case adopts
// reads as abandoned.
func adoptLater() time.Time { return time.Now().Add(48 * time.Hour) }

// deadMachine enrols the installation a case adopts: it joins the log, exports
// two bindings and goes quiet. It returns the log, shared by every handle.
func deadMachine(t *testing.T) *synclog.MemTransport {
	t.Helper()
	shared, local := joinPair(t, adoptedID)
	passGate(t, shared)
	seedJoin(t, shared, joinBinding("b1", adoptedID), joinBinding("b2", adoptedID))
	log := synclog.NewMemTransport(adoptedID)
	if _, err := runEnable(t, shared, local, []byte(joinToken), log); err != nil {
		t.Fatalf("enrol the installation: %v", err)
	}
	return log
}

// freshMachine is a machine with a temporary identity and nothing of its own.
func freshMachine(t *testing.T, origin string) (*db.DB, Local, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "relevo.db")
	shared, err := db.OpenSplit(path, db.Options{Origin: origin})
	if err != nil {
		t.Fatalf("db.OpenSplit: %v", err)
	}
	t.Cleanup(func() { _ = shared.Close() })
	local, err := LocalHandle(shared)
	if err != nil {
		t.Fatalf("LocalHandle: %v", err)
	}
	passGate(t, shared)
	storeR2(t, local)
	return shared, local, path
}

func adopter(shared *db.DB, local Local, transport synclog.LogTransport, now func() time.Time, identity func(string) error) *Enabler {
	return &Enabler{
		Request:   EnableRequest{Local: local, Shared: shared, URL: joinRemote, Token: []byte(joinToken)},
		Transport: transport,
		Now:       now,
		Adopt:     &Adoption{ID: adoptedID, Identity: identity},
	}
}

// TestAdoptionPutsNothingOnTheRemoteUnderAnotherIdentity pins the property the
// adoption exists for: whatever the machine's temporary identity is, the remote
// holds no entry, mark or meta row under any id but the adopted one, and the
// adopted log was imported in full before the machine became it.
func TestAdoptionPutsNothingOnTheRemoteUnderAnotherIdentity(t *testing.T) {
	log := deadMachine(t)
	shared, local, _ := freshMachine(t, "tmp-m")
	var adoptedAs string
	e := adopter(shared, local, log.OnLog("tmp-m"), adoptLater, func(id string) error { adoptedAs = id; return nil })

	res, err := e.Enable()
	if err != nil {
		t.Fatalf("Enable: %v", err)
	}
	if res.Adopted != adoptedID || adoptedAs != adoptedID {
		t.Fatalf("adopted = %q, identity written = %q, want %q", res.Adopted, adoptedAs, adoptedID)
	}
	state, err := log.ReadMarks()
	if err != nil {
		t.Fatalf("ReadMarks: %v", err)
	}
	for origin := range state.Heads {
		if origin != adoptedID {
			t.Errorf("the remote holds entries under %q", origin)
		}
	}
	if len(state.Marks) != 0 {
		t.Errorf("the remote holds marks %+v, want none", state.Marks)
	}
	if claims := log.Claims(); len(claims) != 1 || claims[0] != adoptedID {
		t.Errorf("the remote holds claims %v, want only %q", claims, adoptedID)
	}
	if stats, _ := log.Stats(); stats.Origins != 1 {
		t.Errorf("the remote holds %d origins, want 1", stats.Origins)
	}
	raw, err := db.OpenRaw(shared.Path())
	if err != nil {
		t.Fatalf("db.OpenRaw: %v", err)
	}
	defer func() { _ = raw.Close() }()
	var rows int
	if err := raw.QueryRow(`SELECT COUNT(*) FROM binding WHERE origin = ?`, adoptedID).Scan(&rows); err != nil || rows != 2 {
		t.Errorf("the machine holds %d of the adopted bindings (%v), want 2", rows, err)
	}
	var temp int
	if err := raw.QueryRow(`SELECT COUNT(*) FROM installation WHERE id = ?`, "tmp-m").Scan(&temp); err != nil || temp != 0 {
		t.Errorf("the temporary identity's installation row remains (%d, %v)", temp, err)
	}
	if on, _ := Enabled(local); on {
		t.Error("an adoption turned sync on before the daemon restarted")
	}
	if _, joining, _ := ReadJoin(local); !joining {
		t.Error("an adoption cleared the join marker, which is what the resume needs")
	}
	if head, ok, _ := synclog.OwnHead(local); !ok || head != state.Heads[adoptedID] {
		t.Errorf("own head = %d (%v), want the remote head %d", head, ok, state.Heads[adoptedID])
	}
	if _, found, _ := shared.ImportMark(adoptedID); found {
		t.Error("the adopted identity kept an import mark of its own log")
	}
}

// TestAdoptedMachineResumesAsTheIdentityAndEchoesNothing pins the second half:
// reopened as the adopted identity, the enable resumes, turns the machine on and
// appends nothing for the rows it imported from its own log.
func TestAdoptedMachineResumesAsTheIdentityAndEchoesNothing(t *testing.T) {
	log := deadMachine(t)
	before, _ := log.Stats()
	shared, local, path := freshMachine(t, "tmp-m")
	if _, err := adopter(shared, local, log.OnLog("tmp-m"), adoptLater, nil).Enable(); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	_ = shared.Close()

	reopened, err := db.OpenSplit(path, db.Options{Origin: adoptedID})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	relocal, err := LocalHandle(reopened)
	if err != nil {
		t.Fatalf("LocalHandle: %v", err)
	}
	resume := &Enabler{
		Request:   EnableRequest{Local: relocal, Shared: reopened, URL: joinRemote},
		Transport: log.OnLog(adoptedID),
		Now:       adoptLater,
	}
	res, err := resume.Enable()
	if err != nil {
		t.Fatalf("the resumed enable: %v", err)
	}
	if !res.Resumed || res.Appended != 0 {
		t.Errorf("resumed = %v, appended = %d, want a resume that appended nothing", res.Resumed, res.Appended)
	}
	if on, _ := Enabled(relocal); !on {
		t.Error("the resumed enable did not turn sync on")
	}
	if after, _ := log.Stats(); after.Entries != before.Entries {
		t.Errorf("the log holds %d entries, was %d: the adopted rows were exported twice", after.Entries, before.Entries)
	}
}

// TestAdoptionRacersExactlyOneWins pins the claim: two fresh machines adopting
// one identity at once end with one adopted and one refused.
func TestAdoptionRacersExactlyOneWins(t *testing.T) {
	log := deadMachine(t)
	errs := make([]error, 2)
	var wg gosync.WaitGroup
	for i := range errs {
		shared, local, _ := freshMachine(t, "tmp-"+string(rune('a'+i)))
		e := adopter(shared, local, log.OnLog("tmp-"+string(rune('a'+i))), adoptLater, nil)
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = e.Enable()
		}()
	}
	wg.Wait()
	wins, claimed := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			wins++
		case errors.Is(err, ErrAdoptionClaimed):
			claimed++
		default:
			t.Errorf("unexpected refusal: %v", err)
		}
	}
	if wins != 1 || claimed != 1 {
		t.Fatalf("wins = %d, claimed = %d, want exactly one of each", wins, claimed)
	}
}

// TestAdoptionRefusesAnIdentityStillInUse pins the 24-hour rule through the
// whole enable, for both signs of life.
func TestAdoptionRefusesAnIdentityStillInUse(t *testing.T) {
	t.Run("the log wrote within the day", func(t *testing.T) {
		log := deadMachine(t)
		shared, local, _ := freshMachine(t, "tmp-m")
		_, err := adopter(shared, local, log.OnLog("tmp-m"), time.Now, nil).Enable()
		if !errors.Is(err, ErrAdoptionLive) {
			t.Fatalf("Enable = %v, want ErrAdoptionLive", err)
		}
		if claims := log.Claims(); len(claims) != 0 {
			t.Errorf("a refused adoption left claims %v", claims)
		}
	})
	t.Run("the reader published a mark within the day", func(t *testing.T) {
		log := deadMachine(t)
		if err := log.OnLog(adoptedID).PublishMarks(map[string]int{"other": 1}, adoptLater().Add(-time.Hour)); err != nil {
			t.Fatalf("PublishMarks: %v", err)
		}
		shared, local, _ := freshMachine(t, "tmp-m")
		_, err := adopter(shared, local, log.OnLog("tmp-m"), adoptLater, nil).Enable()
		if !errors.Is(err, ErrAdoptionLive) {
			t.Fatalf("Enable = %v, want ErrAdoptionLive", err)
		}
	})
}

// TestAdoptionRefusalWindow pins the rule's edges on its own.
func TestAdoptionRefusalWindow(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	cases := map[string]struct {
		entry, mark time.Time
		live        bool
	}{
		"nothing at all":            {live: false},
		"an hour old":               {entry: now.Add(-time.Hour), live: true},
		"just under a day":          {mark: now.Add(-24*time.Hour + time.Minute), live: true},
		"a day old":                 {entry: now.Add(-24 * time.Hour), live: false},
		"stamped slightly ahead":    {mark: now.Add(30 * time.Minute), live: true},
		"stamped far in the future": {entry: now.Add(3 * time.Hour), live: false},
	}
	for name, tc := range cases {
		err := AdoptionRefusal(now, tc.entry, tc.mark)
		if (err != nil) != tc.live {
			t.Errorf("%s: AdoptionRefusal = %v, want live = %v", name, err, tc.live)
		}
	}
}

// TestAdoptionRefusesAMachineWithChangesOfItsOwn pins the preconditions: a
// shared row of its own, and a change waiting in the outbox with no row left.
func TestAdoptionRefusesAMachineWithChangesOfItsOwn(t *testing.T) {
	t.Run("a shared row", func(t *testing.T) {
		log := deadMachine(t)
		shared, local, _ := freshMachine(t, "tmp-m")
		seedJoin(t, shared, joinBinding("mine", "tmp-m"))
		_, err := adopter(shared, local, log.OnLog("tmp-m"), adoptLater, nil).Enable()
		if !errors.Is(err, db.ErrAdoptionDirty) {
			t.Fatalf("Enable = %v, want ErrAdoptionDirty", err)
		}
		if claims := log.Claims(); len(claims) != 0 {
			t.Errorf("a refused adoption left claims %v", claims)
		}
	})
	t.Run("an outbox row whose row is gone", func(t *testing.T) {
		log := deadMachine(t)
		shared, local, _ := freshMachine(t, "tmp-m")
		seedJoin(t, shared, joinBinding("mine", "tmp-m"), `DELETE FROM binding WHERE id = 'mine'`)
		blockers, err := shared.AdoptionBlockers()
		if err != nil || len(blockers.Rows) != 0 || blockers.Outbox == 0 {
			t.Fatalf("blockers = %+v, %v, want an outbox count and no rows", blockers, err)
		}
		_, err = adopter(shared, local, log.OnLog("tmp-m"), adoptLater, nil).Enable()
		if !errors.Is(err, db.ErrAdoptionDirty) {
			t.Fatalf("Enable = %v, want ErrAdoptionDirty", err)
		}
		if claims := log.Claims(); len(claims) != 0 {
			t.Errorf("the refusal came after the claim: the remote holds %v", claims)
		}
	})
}

// TestAdoptionOfOwnIdentityIsRefused pins that a machine cannot adopt the id it
// already has.
func TestAdoptionOfOwnIdentityIsRefused(t *testing.T) {
	log := deadMachine(t)
	shared, local, _ := freshMachine(t, adoptedID)
	_, err := adopter(shared, local, log.OnLog(adoptedID), adoptLater, nil).Enable()
	if !errors.Is(err, ErrAdoptionSelf) {
		t.Fatalf("Enable = %v, want ErrAdoptionSelf", err)
	}
}

// TestAdoptedClaimDoubleWinIsFoundOnTheNextAttempt pins the detection: A reads
// its own token back, B's later push lands over it, and A's next steady attempt
// compares the claim with its token, exports and imports nothing, and leaves the
// fact the status surface reads.
func TestAdoptedClaimDoubleWinIsFoundOnTheNextAttempt(t *testing.T) {
	log := deadMachine(t)
	shared, local, path := freshMachine(t, "tmp-m")
	if _, err := adopter(shared, local, log.OnLog("tmp-m"), adoptLater, nil).Enable(); err != nil {
		t.Fatalf("Enable: %v", err)
	}
	adopted, ok, err := ReadAdopted(local)
	if err != nil || !ok {
		t.Fatalf("ReadAdopted = %v, %v", ok, err)
	}
	_ = shared.Close()
	reopened, err := db.OpenSplit(path, db.Options{Origin: adoptedID})
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	relocal, _ := LocalHandle(reopened)

	runner := &Runner{Client: log.OnLog(adoptedID), Local: relocal}
	if res := runner.SyncOnce(context.Background(), reopened); res.Err != nil {
		t.Fatalf("an attempt on the machine's own claim: %v", res.Err)
	}

	log.OverwriteClaim(adoptedID, "the other machine's token", time.Now())
	seedJoin(t, reopened, joinBinding("after", adoptedID))
	before, _ := log.Stats()
	res := runner.SyncOnce(context.Background(), reopened)
	if !errors.Is(res.Err, ErrIdentityTaken) {
		t.Fatalf("the attempt after the double win = %v, want ErrIdentityTaken", res.Err)
	}
	if after, _ := log.Stats(); after.Entries != before.Entries {
		t.Error("the attempt exported after finding the claim was another machine's")
	}
	if got := IdentityTaken(relocal); got != adoptedID {
		t.Errorf("identity-taken fact = %q, want %q", got, adoptedID)
	}
	if adopted.Token == "" {
		t.Error("the adoption stored no token")
	}
}

// inflatedHead reports a head the log does not hold yet, which is a log that
// grew while the import ran.
type inflatedHead struct{ *synclog.MemTransport }

func (l inflatedHead) ReadMarks() (synclog.MarkState, error) {
	state, err := l.MemTransport.ReadMarks()
	state.Heads[adoptedID] += 5
	return state, err
}

// TestAdoptionStopsBeforeTheSwapWhenTheImportFellShort pins that the machine
// does not become the identity until the identity's log is applied through the
// head: nothing is written beside the database, and the temporary identity's
// own row stays.
func TestAdoptionStopsBeforeTheSwapWhenTheImportFellShort(t *testing.T) {
	log := deadMachine(t)
	shared, local, _ := freshMachine(t, "tmp-m")
	var written bool
	e := adopter(shared, local, inflatedHead{log.OnLog("tmp-m")}, adoptLater, func(string) error { written = true; return nil })
	_, err := e.Enable()
	if !errors.Is(err, ErrAdoptionIncomplete) {
		t.Fatalf("Enable = %v, want ErrAdoptionIncomplete", err)
	}
	if written {
		t.Error("the identity was written before the import reached the head")
	}
}
