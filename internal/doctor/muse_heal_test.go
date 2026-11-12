package doctor

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/chatlabel"
	"github.com/fuad-daoud/relevo/internal/mastermind"
	"github.com/fuad-daoud/relevo/internal/relevo"
)

var _ mastermind.Registry = (*healFakeRegistry)(nil)

// healFakeRegistry is the doctor heal tests' registry: one record, counted
// SetHost writes. Lookups match only that record, so an unrelated holder
// never appears.
type healFakeRegistry struct {
	rec      mastermind.Record
	setHosts int
}

func (f *healFakeRegistry) Get(id string) (mastermind.Record, error) {
	if id != f.rec.ID {
		return mastermind.Record{}, fmt.Errorf("%s: %w", id, mastermind.ErrNotFound)
	}
	return f.rec, nil
}

func (f *healFakeRegistry) ByName(name string) (mastermind.Record, error) {
	if name != f.rec.Name {
		return mastermind.Record{}, fmt.Errorf("%s: %w", name, mastermind.ErrNotFound)
	}
	return f.rec, nil
}

func (f *healFakeRegistry) BySession(kind, sessionID string) (mastermind.Record, error) {
	if kind != f.rec.HarnessKind || sessionID != f.rec.SessionID {
		return mastermind.Record{}, fmt.Errorf("%s %s: %w", kind, sessionID, mastermind.ErrNotFound)
	}
	return f.rec, nil
}

func (f *healFakeRegistry) ByHost(pid int, startedAt int64) (mastermind.Record, error) {
	if pid != f.rec.HostPID || startedAt != f.rec.HostStartedAt {
		return mastermind.Record{}, fmt.Errorf("host %d: %w", pid, mastermind.ErrNotFound)
	}
	return f.rec, nil
}

func (f *healFakeRegistry) List() ([]mastermind.Record, error) {
	return []mastermind.Record{f.rec}, nil
}

func (f *healFakeRegistry) Create(r mastermind.Record) (mastermind.Record, error) {
	f.rec = r
	return r, nil
}

func (f *healFakeRegistry) MoveSession(string, string, time.Time) (mastermind.Record, error) {
	return mastermind.Record{}, errors.New("heal fake: no MoveSession")
}

func (f *healFakeRegistry) SetHost(id string, pid int, startedAt int64) (mastermind.Record, error) {
	f.setHosts++
	if id != f.rec.ID {
		return mastermind.Record{}, fmt.Errorf("%s: %w", id, mastermind.ErrNotFound)
	}
	f.rec.HostPID = pid
	f.rec.HostStartedAt = startedAt
	return f.rec, nil
}

func (f *healFakeRegistry) Rename(string, string) (mastermind.Record, error) {
	return mastermind.Record{}, errors.New("heal fake: no Rename")
}

func (*healFakeRegistry) Touch(string, time.Time) error { return nil }

func (*healFakeRegistry) Forget(string, func(string) bool) error { return nil }

// clearHostSessionEnv blanks the variables a host harness session exports, so
// the resolve under test sees only the muse session these tests set: a run
// inside Claude Code (CLAUDECODE) or a MasterMind shell (RELEVO_MASTERMIND)
// would otherwise resolve to the host's own record first.
func clearHostSessionEnv(t *testing.T) {
	t.Helper()
	for _, name := range []string{"CLAUDECODE", "RELEVO_MASTERMIND", "RELEVO_PLANNER"} {
		t.Setenv(name, "")
	}
}

// healMuseSession is the muse session the doctor heal tests run as.
const healMuseSession = "1a2b3c4d-5e6f-4a8b-9c0d-1e2f3a4b5c6d"

// healDoctorRuntime is a runtime on a gone muse record whose session now runs
// as liveHost: the old host reads dead, the walk host live and verified.
func healDoctorRuntime(reg *healFakeRegistry, seam func(int) (int, bool)) relevo.Runtime {
	const liveHost = 80
	const liveStarted = 800
	return relevo.Runtime{
		MasterMinds: reg,
		ProcStart: func(pid int) (int64, error) {
			if pid == liveHost {
				return liveStarted, nil
			}
			return 0, errors.New("no such process")
		},
		MuseHost: seam,
		Now:      func() time.Time { return time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC) },
	}
}

func healGoneRecord() *healFakeRegistry {
	return &healFakeRegistry{rec: mastermind.Record{
		ID: "mm_aaaaaaaaaaaa", Name: "muse-1", HarnessKind: "muse",
		SessionID: healMuseSession, HostPID: 70, HostStartedAt: 700,
	}}
}

// TestDoctorMuseFactsHealsGoneRecord pins that the doctor's resolve re-points
// a gone muse record at the calling session's live host, with exactly one
// SetHost: the follow-up MCPChild read then checks the live host.
func TestDoctorMuseFactsHealsGoneRecord(t *testing.T) {
	t.Setenv("MUSE_SESSION_ID", healMuseSession)
	clearHostSessionEnv(t)
	reg := healGoneRecord()
	rt := healDoctorRuntime(reg, func(int) (int, bool) { return 80, true })

	var in MasterMindCheckInput
	museMasterMindFacts(rt, &in, chatlabel.Resolver{})
	if in.Resolved == nil {
		t.Fatalf("Resolved = nil, want the healed record")
	}
	if in.Resolved.HostPID != 80 || in.Resolved.HostStartedAt != 800 {
		t.Errorf("Resolved host = %d@%d, want 80@800", in.Resolved.HostPID, in.Resolved.HostStartedAt)
	}
	if reg.setHosts != 1 {
		t.Errorf("SetHost calls = %d, want exactly 1", reg.setHosts)
	}
}

// TestDoctorMuseFactsRefusesHealForAnotherSession pins the heal guard: a
// caller in a different session resolves nothing and writes nothing.
func TestDoctorMuseFactsRefusesHealForAnotherSession(t *testing.T) {
	t.Setenv("MUSE_SESSION_ID", "2b3c4d5e-6f70-4b8c-9d0e-1f2a3b4c5d6e")
	clearHostSessionEnv(t)
	reg := healGoneRecord()
	rt := healDoctorRuntime(reg, func(int) (int, bool) { return 80, true })

	var in MasterMindCheckInput
	museMasterMindFacts(rt, &in, chatlabel.Resolver{})
	if in.Resolved != nil || reg.setHosts != 0 {
		t.Errorf("another session resolved %+v with %d SetHost calls, want nil and 0", in.Resolved, reg.setHosts)
	}
}

// TestDoctorMuseFactsRefusesHealAtUnverifiedWalk pins the muse heal guard: a
// caller under no verified muse ancestor keeps the gone record, with zero
// SetHost calls.
func TestDoctorMuseFactsRefusesHealAtUnverifiedWalk(t *testing.T) {
	t.Setenv("MUSE_SESSION_ID", healMuseSession)
	clearHostSessionEnv(t)
	reg := healGoneRecord()
	rt := healDoctorRuntime(reg, func(int) (int, bool) { return 0, false })

	var in MasterMindCheckInput
	museMasterMindFacts(rt, &in, chatlabel.Resolver{})
	if in.Resolved == nil {
		t.Fatalf("Resolved = nil, want the unchanged gone record")
	}
	if in.Resolved.HostPID != 70 || reg.setHosts != 0 {
		t.Errorf("unverified walk moved the record: host %d, SetHost calls %d; want 70 and 0", in.Resolved.HostPID, reg.setHosts)
	}
}
