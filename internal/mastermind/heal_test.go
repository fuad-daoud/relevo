package mastermind

import (
	"errors"
	"testing"
	"time"
)

// spyRegistry counts the writes a heal makes and can fail SetHost.
type spyRegistry struct {
	*DBRegistry
	setHosts    int
	moves       int
	setHostFail error
}

func (s *spyRegistry) SetHost(id string, pid int, startedAt int64) (Record, error) {
	s.setHosts++
	if s.setHostFail != nil {
		return Record{}, s.setHostFail
	}
	return s.DBRegistry.SetHost(id, pid, startedAt)
}

func (s *spyRegistry) MoveSession(id, sessionID string, now time.Time) (Record, error) {
	s.moves++
	return s.DBRegistry.MoveSession(id, sessionID, now)
}

// healFixture is one gone record (host 101 started at 1010) whose session is
// "sess-a", behind a spy.
func healFixture(t *testing.T) (*spyRegistry, Record) {
	t.Helper()
	reg := &spyRegistry{DBRegistry: testRegistry(t)}
	rec := mustCreate(t, reg.DBRegistry, record("pl_aaaaaaaaaaaa", "alpha", "claude", "sess-a", 101))
	return reg, rec
}

// healInput is a Resolve call from the session sess-a, now running as host
// pid 202, whose start time is 2020; the old host 101 reads gone.
func healInput(procStart func(int) (int64, error)) ResolveInput {
	return ResolveInput{
		Flag:      "alpha",
		Env:       envFunc(claudeEnv(202, "sess-a")),
		PPID:      202,
		ProcStart: procStart,
		Now:       testNow,
		Heal:      true,
	}
}

func newHostAlive(pid int) (int64, error) {
	if pid == 202 {
		return 2020, nil
	}
	return 0, errors.New("no such process")
}

func TestHealRepointsGoneRecordAtCallingHost(t *testing.T) {
	reg, rec := healFixture(t)
	got, _, err := Resolve(reg, healInput(newHostAlive))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.HostPID != 202 || got.HostStartedAt != 2020 {
		t.Errorf("returned host = %d@%d, want 202@2020", got.HostPID, got.HostStartedAt)
	}
	stored, err := reg.Get(rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.HostPID != 202 || stored.HostStartedAt != 2020 {
		t.Errorf("stored host = %d@%d, want 202@2020", stored.HostPID, stored.HostStartedAt)
	}
	if reg.setHosts != 1 {
		t.Errorf("SetHost calls = %d, want exactly 1", reg.setHosts)
	}
	if reg.moves != 0 {
		t.Errorf("MoveSession calls = %d, want 0 for an unmoved session", reg.moves)
	}
}

func TestHealLeavesLiveRecordUntouched(t *testing.T) {
	reg, rec := healFixture(t)
	in := healInput(func(pid int) (int64, error) {
		if pid == rec.HostPID {
			return rec.HostStartedAt, nil
		}
		return 2020, nil
	})
	got, _, err := Resolve(reg, in)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.HostPID != rec.HostPID {
		t.Errorf("host = %d, want the live host %d kept", got.HostPID, rec.HostPID)
	}
	if reg.setHosts != 0 {
		t.Errorf("SetHost calls = %d on a live record, want 0", reg.setHosts)
	}
}

func TestHealRefusesAnotherSession(t *testing.T) {
	reg, rec := healFixture(t)
	in := healInput(newHostAlive)
	in.Env = envFunc(claudeEnv(202, "sess-other"))
	got, _, err := Resolve(reg, in)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.HostPID != rec.HostPID || reg.setHosts != 0 {
		t.Errorf("another session healed the record: host %d, SetHost calls %d", got.HostPID, reg.setHosts)
	}
}

func TestHealRefusesWithoutDetectedSession(t *testing.T) {
	reg, rec := healFixture(t)
	in := healInput(newHostAlive)
	in.Env = envFunc(nil)
	got, _, err := Resolve(reg, in)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.HostPID != rec.HostPID || reg.setHosts != 0 {
		t.Errorf("an undetected caller healed the record: host %d, SetHost calls %d", got.HostPID, reg.setHosts)
	}
}

func TestHealRefusesUnreadableCallingHost(t *testing.T) {
	reg, rec := healFixture(t)
	got, _, err := Resolve(reg, healInput(func(int) (int64, error) { return 0, errors.New("ps failed") }))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.HostPID != rec.HostPID || reg.setHosts != 0 {
		t.Errorf("an unreadable host healed the record: host %d, SetHost calls %d", got.HostPID, reg.setHosts)
	}
}

func TestHealDoesNotStealHostAnotherRecordHolds(t *testing.T) {
	reg, rec := healFixture(t)
	mustCreate(t, reg.DBRegistry, Record{
		ID: "pl_bbbbbbbbbbbb", Name: "beta", HarnessKind: "claude", SessionID: "sess-b",
		HostPID: 202, HostStartedAt: 2020, CWD: "/tmp/relevo-mastermind-test",
		CreatedAt: testNow, SeenAt: testNow,
	})
	got, _, err := Resolve(reg, healInput(newHostAlive))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.HostPID != rec.HostPID || reg.setHosts != 0 {
		t.Errorf("stole another record's host: host %d, SetHost calls %d", got.HostPID, reg.setHosts)
	}
}

func TestHealWriteFailureDoesNotFailResolve(t *testing.T) {
	reg, rec := healFixture(t)
	reg.setHostFail = errors.New("store busy")
	got, res, err := Resolve(reg, healInput(newHostAlive))
	if err != nil {
		t.Fatalf("Resolve failed because the heal did: %v", err)
	}
	if res != ResolutionFlag || got.ID != rec.ID || got.HostPID != rec.HostPID {
		t.Errorf("Resolve = %s %d (%s), want the stale record by flag", got.ID, got.HostPID, res)
	}
}

func TestHealSkipsExplicitRecord(t *testing.T) {
	reg := &spyRegistry{DBRegistry: testRegistry(t)}
	rec := mustCreate(t, reg.DBRegistry, record("pl_aaaaaaaaaaaa", "alpha", "claude", "sess-a", 0))
	got, _, err := Resolve(reg, healInput(newHostAlive))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.HostPID != 0 || got.ID != rec.ID || reg.setHosts != 0 {
		t.Errorf("explicit record healed: host %d, SetHost calls %d", got.HostPID, reg.setHosts)
	}
}

func TestHealIsOffUnlessAsked(t *testing.T) {
	reg, rec := healFixture(t)
	in := healInput(newHostAlive)
	in.Heal = false
	got, _, err := Resolve(reg, in)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.HostPID != rec.HostPID || reg.setHosts != 0 {
		t.Errorf("healed with Heal unset: host %d, SetHost calls %d", got.HostPID, reg.setHosts)
	}
}

func TestHealOnEnvHit(t *testing.T) {
	reg, rec := healFixture(t)
	in := healInput(newHostAlive)
	in.Flag = ""
	in.Env = envFunc(withEnv(claudeEnv(202, "sess-a"), "RELEVO_MASTERMIND", rec.ID))
	got, res, err := Resolve(reg, in)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if res != ResolutionEnv || got.HostPID != 202 || reg.setHosts != 1 {
		t.Errorf("env hit: %s host %d, SetHost calls %d; want env, 202, 1", res, got.HostPID, reg.setHosts)
	}
}
