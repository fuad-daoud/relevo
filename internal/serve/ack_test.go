package serve

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/store"
)

// TestSettleServedConfirmsUpToRound pins the bound settleServed uses: every
// unconfirmed to_planner entry with Round <= upTo is confirmed with route
// "ack"; everything else is left alone.
func TestSettleServedConfirmsUpToRound(t *testing.T) {
	st := store.New(t.TempDir())
	name := "api"
	// AppendLog refuses a name with no record.
	if err := st.Save(store.Binding{Name: name, CWD: t.TempDir()}); err != nil {
		t.Fatalf("save: %v", err)
	}

	for _, e := range []store.LogEntry{
		{Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport, Payload: "r1"},
		{Round: 2, Direction: store.DirToMasterMind, Kind: store.KindReport, Payload: "r2"},
		{Round: 3, Direction: store.DirToMasterMind, Kind: store.KindReport, Payload: "r3"},
		{Round: 2, Direction: store.DirToBuilder, Kind: store.KindPlan, Payload: "plan"},
	} {
		if err := st.AppendLog(name, e); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	n := 0
	if err := st.WithLock(func(tx *store.Tx) error {
		var err error
		n, err = settleServed(tx, name, 2)
		return err
	}); err != nil {
		t.Fatalf("settleServed: %v", err)
	}
	if n != 2 {
		t.Fatalf("settleServed returned %d, want 2", n)
	}

	got, err := st.ReadLog(name)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("log has %d entries, want 4", len(got))
	}
	for i, want := range []struct {
		confirmed bool
		route     string
	}{
		{true, "ack"},
		{true, "ack"},
		{false, ""},
		{false, ""},
	} {
		if got[i].Confirmed != want.confirmed {
			t.Errorf("entry %d confirmed = %v, want %v", i, got[i].Confirmed, want.confirmed)
		}
		if got[i].Route != want.route {
			t.Errorf("entry %d route = %q, want %q", i, got[i].Route, want.route)
		}
	}
}

// TestAckRoundSettlesPending acks a closed round over the wire: the report entry
// that showed as pending before must be confirmed afterwards.
func TestAckRoundSettlesPending(t *testing.T) {
	env := setupTestEnv(t)
	ctx := context.Background()

	createBody, _ := json.Marshal(remote.CreateBindingRequest{
		Name:       "api",
		RepoID:     env.repoID,
		BaseCommit: env.headSHA,
		Role:       "builder",
	})
	doSigned(t, env.ts, env.kp, "POST", "/v1/bindings", createBody, "application/json")

	outRef := "refs/relevo/api/out"
	_ = env.gitClient.UpdateRef(ctx, env.clientDir, outRef, env.headSHA, "")
	snap, _ := env.transport.Snapshot(ctx, env.clientDir, []string{outRef}, "")
	bundleBytes, _ := io.ReadAll(snap.Body)
	_ = snap.Body.Close()

	formBytes, ct := makeRoundForm(t, 1, "# Plan 1", bundleBytes)
	resp, _ := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds", formBytes, ct)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("start status = %d, want 201", resp.StatusCode)
	}

	rt := env.runtime(t)
	reportText := "# Report 1\nCompleted work.\n\n```relevo\nstatus: done\n```\n"
	if err := os.WriteFile(rt.Store.ReportPath("api", 1), []byte(reportText), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rt.Store.DonePath("api", 1), []byte(""), 0o644); err != nil {
		t.Fatal(err)
	}
	env.runner.setAlive(false)

	if err := env.srv.Tick(ctx); err != nil {
		t.Fatalf("tick: %v", err)
	}

	pending, found, err := rt.Store.PendingForMasterMind("api")
	if err != nil {
		t.Fatal(err)
	}
	if !found || pending.Round != 1 {
		t.Fatalf("pending before ack = (round %d, found=%v), want round 1", pending.Round, found)
	}

	resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/rounds/1/ack", nil, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ack status = %d, want 200; body: %s", resp.StatusCode, string(body))
	}

	pending, found, err = rt.Store.PendingForMasterMind("api")
	if err != nil {
		t.Fatal(err)
	}
	if found {
		t.Fatalf("pending report round %d remains after the ack", pending.Round)
	}
}

// TestDoneSettlesClosedRounds: done must settle up to the closed round for a
// DONE binding whose last acked round trails it, so nothing is left pending.
func TestDoneSettlesClosedRounds(t *testing.T) {
	env := setupTestEnv(t)

	rt := env.runtime(t)
	b := store.Binding{
		Name:  "api",
		CWD:   t.TempDir(),
		Owner: string(env.id),
		State: store.StateDone,
		Round: 2,
		Serve: &store.ServeFacts{ClosedRound: 2, AckedRound: 1},
	}
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	for _, e := range []store.LogEntry{
		{Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport, Payload: "r1"},
		{Round: 2, Direction: store.DirToBuilder, Kind: store.KindPlan, Confirmed: true},
		{Round: 2, Direction: store.DirToMasterMind, Kind: store.KindReport, Payload: "r2"},
	} {
		if err := rt.Store.AppendLog("api", e); err != nil {
			t.Fatal(err)
		}
	}

	if _, found, err := rt.Store.PendingForMasterMind("api"); err != nil {
		t.Fatal(err)
	} else if !found {
		t.Fatal("expected a pending report before done")
	}

	resp, body := doSigned(t, env.ts, env.kp, "POST", "/v1/bindings/api/done", nil, "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("done status = %d, want 200; body: %s", resp.StatusCode, string(body))
	}

	if e, found, err := rt.Store.PendingForMasterMind("api"); err != nil {
		t.Fatal(err)
	} else if found {
		t.Fatalf("pending report round %d remains after done", e.Round)
	}

	got, err := rt.Store.ReadLog("api")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("log has %d entries, want 3", len(got))
	}
	for i := range got {
		if !got[i].Confirmed {
			t.Errorf("entry %d (round %d, %s) not confirmed after done", i, got[i].Round, got[i].Direction)
		}
	}
}

// TestSettleAllServedBackfills: two owners, each holding an acked but never
// confirmed report, both read as settled after one settleAllServed walk.
func TestSettleAllServedBackfills(t *testing.T) {
	srv, _ := newTestServer(t, 0)

	var stores []*store.Store
	for i := 0; i < 2; i++ {
		kp, err := remote.Generate()
		if err != nil {
			t.Fatal(err)
		}
		dir, ok := remote.IDOf(kp.Public).Dir()
		if !ok {
			t.Fatal("client id has no owner dir")
		}
		ownerRoot := filepath.Join(srv.cfg.Root, "bindings", dir)
		rt := srv.runtimeAt(ownerRoot)

		b := store.Binding{
			Name:  "api",
			CWD:   t.TempDir(),
			Owner: string(remote.IDOf(kp.Public)),
			State: store.StateActive,
			Serve: &store.ServeFacts{ClosedRound: 1, AckedRound: 1},
		}
		if err := rt.Store.Save(b); err != nil {
			t.Fatalf("save binding for owner %d: %v", i, err)
		}
		if err := rt.Store.AppendLog("api", store.LogEntry{
			Round: 1, Direction: store.DirToMasterMind, Kind: store.KindReport, Payload: "r1",
		}); err != nil {
			t.Fatalf("append log for owner %d: %v", i, err)
		}

		if _, found, err := rt.Store.PendingForMasterMind("api"); err != nil {
			t.Fatal(err)
		} else if !found {
			t.Fatalf("owner %d: expected a pending report before settleAllServed", i)
		}
		stores = append(stores, rt.Store)
	}

	if err := srv.settleAllServed(); err != nil {
		t.Fatalf("settleAllServed: %v", err)
	}

	for i, st := range stores {
		if e, found, err := st.PendingForMasterMind("api"); err != nil {
			t.Fatal(err)
		} else if found {
			t.Fatalf("owner %d: pending report round %d remains after settleAllServed", i, e.Round)
		}
	}
}
