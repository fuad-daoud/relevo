package verb

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/db/wire"
	"github.com/fuad-daoud/relevo/internal/installation"
	relevosync "github.com/fuad-daoud/relevo/internal/sync"
	"github.com/fuad-daoud/relevo/internal/synclog"
)

// TestAdoptionIsOnlyBuiltForAnAsEnable pins that a verb without --as carries no
// adoption, and one with it rewrites the installation file beside the database.
func TestAdoptionIsOnlyBuiltForAnAsEnable(t *testing.T) {
	root := t.TempDir()
	if _, err := installation.Load(root); err != nil {
		t.Fatalf("Load: %v", err)
	}
	v := &Runner{Path: filepath.Join(root, "relevo.db")}
	if got, err := v.adoption(&wire.SyncVerb{Verb: wire.SyncVerbEnable}); err != nil || got != nil {
		t.Fatalf("an enable without --as built an adoption: %+v (%v)", got, err)
	}
	a, err := v.adoption(&wire.SyncVerb{Verb: wire.SyncVerbEnable, AdoptAs: "inst-x"})
	if err != nil || a == nil || a.ID != "inst-x" {
		t.Fatalf("adoption = %+v (%v), want one for inst-x", a, err)
	}
	if err := a.Identity("inst-x"); err != nil {
		t.Fatalf("Identity: %v", err)
	}
	if inst, _, err := installation.Read(root); err != nil || inst.ID != "inst-x" {
		t.Errorf("installation file = %+v, %v, want inst-x", inst, err)
	}
	if adoptedWarning("inst-x") == "" {
		t.Error("the adoption warning is empty")
	}
}

// TestEnableAdoptionBesideHistoryNoneRefused pins that an enable combining --as
// and --history none is refused with the fix named.
func TestEnableAdoptionBesideHistoryNoneRefused(t *testing.T) {
	runner, _ := joinRunner(t)
	res := runner.Run(context.Background(), &wire.SyncVerb{
		Verb:      wire.SyncVerbEnable,
		RemoteURL: "libsql://join.invalid",
		AdoptAs:   "inst-x",
		History:   relevosync.HistoryNone,
	}, []byte(verbFixtureToken))
	if res.OK {
		t.Fatal("enable with --as and --history none accepted, want refusal")
	}
	if res.Code != wire.SyncCodeInvalid {
		t.Errorf("code = %q, want %q", res.Code, wire.SyncCodeInvalid)
	}
	if !strings.Contains(res.Message, "--history none") {
		t.Errorf("message = %q, want the fix named", res.Message)
	}
}

// countingOpenLog is a worker that counts what releases it spends, so a test
// can say a refused enable opened nothing and closed nothing.
type countingOpenLog struct {
	*synclog.MemTransport
	closes *int
}

func (l *countingOpenLog) Close() error {
	*l.closes++
	return nil
}

// TestEnableWithAsAndHistoryNoneStagesAndOpensNothing pins that an enable
// combining --as and --history none is refused before any row is staged and
// before any worker is opened, on both the plain and the join arm.
func TestEnableWithAsAndHistoryNoneStagesAndOpensNothing(t *testing.T) {
	t.Parallel()
	for _, joining := range []bool{false, true} {
		runner, local, _ := joinMachine(t, joinPassphrase)
		var openCalls, noBucketCalls, closes int
		runner.Open = func(context.Context) (synclog.LogTransport, error) {
			openCalls++
			return &countingOpenLog{MemTransport: synclog.NewMemTransport("m1"), closes: &closes}, nil
		}
		runner.OpenNoBucket = func(context.Context) (synclog.LogTransport, bool, error) {
			noBucketCalls++
			return &countingOpenLog{MemTransport: synclog.NewMemTransport("m1"), closes: &closes}, false, nil
		}
		before := localState(t, local)
		res := runner.Run(context.Background(), &wire.SyncVerb{
			Verb:          wire.SyncVerbEnable,
			RemoteURL:     joinRemote,
			AdoptAs:       "inst-x",
			History:       relevosync.HistoryNone,
			Join:          joining,
			PassphraseLen: len(joinPassphrase),
		}, []byte(joinToken+joinPassphrase))
		if res.OK {
			t.Fatalf("joining=%v: enable with --as and --history none accepted, want refusal", joining)
		}
		if res.Code != wire.SyncCodeInvalid {
			t.Errorf("joining=%v: code = %q, want %q", joining, res.Code, wire.SyncCodeInvalid)
		}
		if !strings.Contains(res.Message, "--history none") {
			t.Errorf("joining=%v: message = %q, want the fix named", joining, res.Message)
		}
		if openCalls != 0 || noBucketCalls != 0 {
			t.Errorf("joining=%v: openers called (open %d, no-bucket %d), want none before the refusal", joining, openCalls, noBucketCalls)
		}
		if closes != 0 {
			t.Errorf("joining=%v: closed %d workers, want none: none was opened", joining, closes)
		}
		if after := localState(t, local); after["section"] != before["section"] {
			t.Errorf("joining=%v: section changed across a refused enable:\n before %q\n after  %q", joining, before["section"], after["section"])
		}
	}
}

// TestAdoptionWithBadHistoryRefusesInvalid pins that an adoption with an
// unknown history mode builds nothing and answers invalid.
func TestAdoptionWithBadHistoryRefusesInvalid(t *testing.T) {
	t.Parallel()
	runner, _ := joinRunner(t)
	adopt, err := runner.adoption(&wire.SyncVerb{Verb: wire.SyncVerbEnable, AdoptAs: "inst-x", History: "sometimes"})
	if adopt != nil {
		t.Errorf("adoption with a bad history built %+v, want nil", adopt)
	}
	if !errors.Is(err, db.ErrInvalid) {
		t.Errorf("adoption error = %v, want it to wrap the invalid marker", err)
	}
}

// TestEnableWithAsAndBadHistoryRefusedBeforeOpening pins the user-visible half:
// an enable combining --as with an unknown history mode is refused as invalid
// before any worker is opened. The machine holds no token, so the adoption
// check firing first is what answers: falling through would name the missing
// token instead.
func TestEnableWithAsAndBadHistoryRefusedBeforeOpening(t *testing.T) {
	t.Parallel()
	runner, _ := joinRunner(t)
	var openCalls, noBucketCalls int
	runner.Open = func(context.Context) (synclog.LogTransport, error) {
		openCalls++
		return synclog.NewMemTransport("m1"), nil
	}
	runner.OpenNoBucket = func(context.Context) (synclog.LogTransport, bool, error) {
		noBucketCalls++
		return synclog.NewMemTransport("m1"), false, nil
	}
	res := runner.Run(context.Background(), &wire.SyncVerb{
		Verb:      wire.SyncVerbEnable,
		RemoteURL: "libsql://join.invalid",
		AdoptAs:   "inst-x",
		History:   "sometimes",
	}, nil)
	if res.OK {
		t.Fatal("enable with --as and a bad history accepted, want refusal")
	}
	if res.Code != wire.SyncCodeInvalid {
		t.Errorf("code = %q, want %q", res.Code, wire.SyncCodeInvalid)
	}
	if !strings.Contains(res.Message, "reading mode") {
		t.Errorf("message = %q, want the reading mode named", res.Message)
	}
	if openCalls != 0 || noBucketCalls != 0 {
		t.Errorf("openers called (open %d, no-bucket %d), want none before the refusal", openCalls, noBucketCalls)
	}
}
