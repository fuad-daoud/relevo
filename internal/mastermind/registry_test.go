package mastermind

import (
	"errors"
	"strconv"
	"testing"
	"time"
)

func TestRegistryCreateRejectsSessionTaken(t *testing.T) {
	reg := testRegistry(t)
	mustCreate(t, reg, record("pl_aaaaaaaaaaaa", "alpha", "claude", "sess-1", 0))

	clash := record("pl_bbbbbbbbbbbb", "beta", "claude", "sess-1", 0)

	_, err := reg.Create(clash)
	if !errors.Is(err, ErrSessionTaken) {
		t.Fatalf("Create with a taken session = %v, want ErrSessionTaken", err)
	}

	other := record("pl_bbbbbbbbbbbb", "beta", "opencode", "ses_other", 0)
	if _, err := reg.Create(other); err != nil {
		t.Fatalf("Create other kind: %v", err)
	}

	third := record("pl_cccccccccccc", "gamma", "claude", "sess-1", 0)
	if _, err := reg.Create(third); !errors.Is(err, ErrSessionTaken) {
		t.Errorf("Create claude/sess-1 again = %v, want ErrSessionTaken", err)
	}

	if records, err := reg.List(); err != nil {
		t.Fatalf("List: %v", err)
	} else if len(records) != 2 {
		t.Errorf("registry holds %d records, want 2", len(records))
	}
}

func TestRegistryCreateRejectsHostTaken(t *testing.T) {
	reg := testRegistry(t)
	mustCreate(t, reg, record("pl_aaaaaaaaaaaa", "alpha", "claude", "sess-1", 4242))

	clash := record("pl_bbbbbbbbbbbb", "beta", "claude", "sess-2", 4242)
	if _, err := reg.Create(clash); !errors.Is(err, ErrHostTaken) {
		t.Fatalf("Create with a taken host = %v, want ErrHostTaken", err)
	}

	noHost := record("pl_bbbbbbbbbbbb", "beta", "claude", "sess-2", 0)
	if _, err := reg.Create(noHost); err != nil {
		t.Fatalf("Create without a host: %v", err)
	}
}

func TestRegistryCreateRejectsNameTaken(t *testing.T) {
	reg := testRegistry(t)
	mustCreate(t, reg, record("pl_aaaaaaaaaaaa", "alpha", "claude", "sess-1", 0))

	clash := record("pl_bbbbbbbbbbbb", "alpha", "claude", "sess-2", 0)
	if _, err := reg.Create(clash); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("Create with a taken name = %v, want ErrNameTaken", err)
	}
}

func TestMoveSessionAppendsHistoryAndCapsAt20(t *testing.T) {
	reg := testRegistry(t)
	mustCreate(t, reg, record("pl_aaaaaaaaaaaa", "alpha", "claude", "s0", 0))

	for i := 1; i <= 21; i++ {
		at := testNow.Add(time.Duration(i) * time.Minute)
		if _, err := reg.MoveSession("pl_aaaaaaaaaaaa", sessionName(i), "", at); err != nil {
			t.Fatalf("MoveSession(%d): %v", i, err)
		}
	}

	rec, err := reg.Get("pl_aaaaaaaaaaaa")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if rec.SessionID != "s21" {
		t.Errorf("SessionID = %q, want s21", rec.SessionID)
	}
	if len(rec.Sessions) != MaxSessions {
		t.Fatalf("history holds %d sessions, want %d", len(rec.Sessions), MaxSessions)
	}
	if rec.Sessions[0].SessionID != "s1" {
		t.Errorf("oldest kept session = %q, want s1", rec.Sessions[0].SessionID)
	}
	if rec.Sessions[len(rec.Sessions)-1].SessionID != "s20" {
		t.Errorf("newest history entry = %q, want s20", rec.Sessions[len(rec.Sessions)-1].SessionID)
	}
	if got, want := rec.Sessions[len(rec.Sessions)-1].To, testNow.Add(21*time.Minute); !got.Equal(want) {
		t.Errorf("newest history To = %v, want %v", got, want)
	}
	if got, want := rec.Sessions[1].From, rec.Sessions[0].To; !got.Equal(want) {
		t.Errorf("history From = %v, want the previous To %v", got, want)
	}

	mustCreate(t, reg, record("pl_bbbbbbbbbbbb", "beta", "claude", "held", 0))
	if _, err := reg.MoveSession("pl_aaaaaaaaaaaa", "held", "", testNow); !errors.Is(err, ErrSessionTaken) {
		t.Errorf("MoveSession onto a held session = %v, want ErrSessionTaken", err)
	}

	before, err := reg.Get("pl_aaaaaaaaaaaa")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if before.TranscriptLocator != "" {
		t.Fatalf("TranscriptLocator = %q, want empty", before.TranscriptLocator)
	}
	if _, err := reg.MoveSession("pl_aaaaaaaaaaaa", "s22", "/tmp/t.jsonl", testNow.Add(22*time.Minute)); err != nil {
		t.Fatalf("MoveSession with transcript: %v", err)
	}
	after, err := reg.Get("pl_aaaaaaaaaaaa")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if after.TranscriptLocator != "/tmp/t.jsonl" {
		t.Errorf("TranscriptLocator = %q, want /tmp/t.jsonl", after.TranscriptLocator)
	}
}

func TestByHostRequiresStartTimeMatch(t *testing.T) {
	reg := testRegistry(t)
	mustCreate(t, reg, record("pl_aaaaaaaaaaaa", "alpha", "claude", "sess-1", 4242))

	rec, err := reg.ByHost(4242, 42420)
	if err != nil {
		t.Fatalf("ByHost: %v", err)
	}
	if rec.ID != "pl_aaaaaaaaaaaa" {
		t.Errorf("ByHost returned %s, want pl_aaaaaaaaaaaa", rec.ID)
	}

	if _, err := reg.ByHost(4242, 1); !errors.Is(err, ErrNotFound) {
		t.Errorf("ByHost with a different start time = %v, want ErrNotFound", err)
	}
	if _, err := reg.ByHost(4242, 0); !errors.Is(err, ErrNotFound) {
		t.Errorf("ByHost with start 0 = %v, want ErrNotFound", err)
	}
	if _, err := reg.ByHost(0, 0); !errors.Is(err, ErrNotFound) {
		t.Errorf("ByHost(0, 0) = %v, want ErrNotFound", err)
	}
	if _, err := reg.ByHost(-1, 42420); !errors.Is(err, ErrNotFound) {
		t.Errorf("ByHost(-1, start) = %v, want ErrNotFound", err)
	}
}

func TestRegistrySetHostAndRename(t *testing.T) {
	reg := testRegistry(t)
	mustCreate(t, reg, record("pl_aaaaaaaaaaaa", "alpha", "claude", "sess-1", 0))

	rec, err := reg.SetHost("pl_aaaaaaaaaaaa", 777, 7770)
	if err != nil {
		t.Fatalf("SetHost: %v", err)
	}
	if rec.HostPID != 777 || rec.HostStartedAt != 7770 {
		t.Errorf("SetHost left %+v", rec)
	}

	mustCreate(t, reg, record("pl_bbbbbbbbbbbb", "beta", "claude", "sess-2", 888))
	if _, err := reg.SetHost("pl_aaaaaaaaaaaa", 888, 8880); !errors.Is(err, ErrHostTaken) {
		t.Errorf("SetHost onto a taken host = %v, want ErrHostTaken", err)
	}

	if _, err := reg.Rename("pl_aaaaaaaaaaaa", "beta"); !errors.Is(err, ErrNameTaken) {
		t.Errorf("Rename to a taken name = %v, want ErrNameTaken", err)
	}
	if _, err := reg.Rename("pl_aaaaaaaaaaaa", "Gamma"); !errors.Is(err, ErrInvalid) {
		t.Errorf("Rename to an invalid name = %v, want ErrInvalid", err)
	}
	renamed, err := reg.Rename("pl_aaaaaaaaaaaa", "gamma")
	if err != nil {
		t.Fatalf("Rename: %v", err)
	}
	if renamed.Name != "gamma" {
		t.Errorf("Name = %q, want gamma", renamed.Name)
	}
	if byName, err := reg.ByName("gamma"); err != nil || byName.ID != "pl_aaaaaaaaaaaa" {
		t.Errorf("ByName after rename = %+v, %v", byName, err)
	}

	if err := reg.Touch("pl_aaaaaaaaaaaa", testNow.Add(10*time.Minute)); err != nil {
		t.Fatalf("Touch: %v", err)
	}
	touched, err := reg.Get("pl_aaaaaaaaaaaa")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !touched.SeenAt.Equal(testNow.Add(10 * time.Minute)) {
		t.Errorf("SeenAt = %v, want the touched time", touched.SeenAt)
	}
	if err := reg.Touch("pl_aaaaaaaaaaaa", testNow.Add(10*time.Minute+time.Second)); err != nil {
		t.Fatalf("Touch: %v", err)
	}
	again, err := reg.Get("pl_aaaaaaaaaaaa")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !again.SeenAt.Equal(testNow.Add(10 * time.Minute)) {
		t.Errorf("SeenAt = %v, want the throttled time unchanged", again.SeenAt)
	}
}

func TestRegistryForgetUsesInUseCallback(t *testing.T) {
	reg := testRegistry(t)
	mustCreate(t, reg, record("pl_aaaaaaaaaaaa", "alpha", "claude", "sess-1", 0))

	if err := reg.Forget("pl_aaaaaaaaaaaa", func(string) bool { return true }); !errors.Is(err, ErrInUse) {
		t.Fatalf("Forget with inUse true = %v, want ErrInUse", err)
	}
	if err := reg.Forget("pl_bbbbbbbbbbbb", nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("Forget of an unknown id = %v, want ErrNotFound", err)
	}
	if err := reg.Forget("pl_aaaaaaaaaaaa", func(string) bool { return false }); err != nil {
		t.Fatalf("Forget: %v", err)
	}
	if _, err := reg.Get("pl_aaaaaaaaaaaa"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get after Forget = %v, want ErrNotFound", err)
	}
}

func TestGetRejectsAMalformedID(t *testing.T) {
	reg := testRegistry(t)
	mustCreate(t, reg, record("pl_aaaaaaaaaaaa", "alpha", "claude", "sess-1", 0))

	if _, err := reg.Get("../../etc/passwd"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get(traversal) = %v, want ErrNotFound", err)
	}
	if _, err := reg.Get("alpha"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get(name) = %v, want ErrNotFound", err)
	}
}

func sessionName(i int) string { return "s" + strconv.Itoa(i) }
