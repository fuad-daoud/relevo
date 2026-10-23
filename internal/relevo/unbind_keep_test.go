package relevo

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/remote/client"
	"github.com/fuad-daoud/relevo/internal/store"
)

func keptWorktreeRuntime(t *testing.T, kind, name string) (Runtime, *fakeDeleter, string) {
	t.Helper()
	rt := newRuntime(t)
	wt := t.TempDir()
	switch kind {
	case "dirty":
		rt.Git = &fakeGit{dirtyResult: true}
	case "dirty-err":
		rt.Git = &fakeGit{dirtyErr: errors.New("git lock busy")}
	case "no-git":
		rt.Git = nil
	case "remove-fail":
		rt.Git = &fakeGit{removeWorktreeErr: errors.New("git lock locked")}
	}
	d := &fakeDeleter{}
	rt.SessionReaper = d
	b := store.Binding{
		Name: name, CWD: wt, Worktree: wt, State: store.StateActive,
		MasterMind:        store.Endpoint{Kind: "claude", SessionID: "s"},
		Builder:           store.Endpoint{Mode: store.ModeHeadless},
		AbandonedSessions: []store.AbandonedSession{{Kind: "opencode", ID: "ses-earlier"}},
	}
	if err := rt.Store.Save(b); err != nil {
		t.Fatal(err)
	}
	return rt, d, wt
}

func TestUnbindKeepsBindingWhenWorktreeKept(t *testing.T) {
	t.Parallel()

	for _, kind := range []string{"dirty", "dirty-err", "no-git", "remove-fail"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			rt, d, wt := keptWorktreeRuntime(t, kind, "kept")

			for _, archive := range []bool{false, true} {
				res, err := Unbind(context.Background(), rt, "kept", archive)
				if !errors.Is(err, ErrWorktreeKept) || !errors.Is(err, ErrRefused) {
					t.Fatalf("Unbind(archive=%v) err = %v, want ErrWorktreeKept and ErrRefused", archive, err)
				}
				if !strings.Contains(err.Error(), wt) || !strings.Contains(err.Error(), "kept (") || strings.Contains(err.Error(), "--force") {
					t.Errorf("message %q must name the worktree and the reason, with no force hint", err)
				}
				if res.WorktreeKept != wt || res.Archived {
					t.Errorf("result = %+v, want the kept worktree and no archive", res)
				}
				if _, lerr := rt.Store.Load("kept"); lerr != nil {
					t.Fatalf("binding must still load: %v", lerr)
				}
			}
			if len(d.calls) != 0 {
				t.Errorf("sessions deleted for a kept binding: %+v", d.calls)
			}
		})
	}
}

func TestUnbindForceArchivesKeptWorktree(t *testing.T) {
	t.Parallel()

	rt, d, wt := keptWorktreeRuntime(t, "dirty", "kept")
	res, err := UnbindWith(context.Background(), rt, "kept", true, true)
	if err != nil {
		t.Fatalf("UnbindWith force: %v", err)
	}
	if res.WorktreeKept != wt || !res.Archived {
		t.Errorf("result = %+v, want archived with the worktree reported kept", res)
	}
	if _, lerr := rt.Store.Load("kept"); !errors.Is(lerr, store.ErrNotFound) {
		t.Errorf("binding still loads after forced unbind: %v", lerr)
	}
	if len(d.calls) != 1 {
		t.Errorf("deleted sessions = %+v, want the abandoned one", d.calls)
	}
}

func TestUnbindRemoteWorktreeKeptLeavesLocalBinding(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	if err := st.Save(remoteBinding("zen")); err != nil {
		t.Fatal(err)
	}
	fr := &fakeRemote{unbindErr: &client.HTTPError{Status: 409, Body: remote.ErrorBody{Code: remote.CodeWorktreeKept, Message: "binding api kept"}}}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	_, err := Unbind(context.Background(), rt, "api", false)
	if !errors.Is(err, ErrWorktreeKept) || !strings.Contains(err.Error(), "worktree on zen kept: binding api kept") {
		t.Fatalf("Unbind err = %v, want ErrWorktreeKept naming the server", err)
	}
	if _, lerr := st.Load("api"); lerr != nil {
		t.Fatalf("local binding must stay: %v", lerr)
	}
	if len(fr.unbindForce) != 1 || fr.unbindForce[0] {
		t.Errorf("force sent = %v, want [false]", fr.unbindForce)
	}
}

func TestUnbindRemoteForcePassesForce(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	if err := st.Save(remoteBinding("zen")); err != nil {
		t.Fatal(err)
	}
	fr := &fakeRemote{}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	if _, err := UnbindWith(context.Background(), rt, "api", false, true); err != nil {
		t.Fatalf("UnbindWith: %v", err)
	}
	if len(fr.unbindForce) != 1 || !fr.unbindForce[0] {
		t.Errorf("force sent = %v, want [true]", fr.unbindForce)
	}
}

const oldServerKeptBody = "binding api kept: worktree /p/api kept (uncommitted changes); commit or discard the work, then unbind again, or `relevo unbind api --force` archives it anyway; on the server: relevo serve unbind --owner SHA256:abc api --force"

func TestUnbindOfAServedBindingNamesTheServerAndTheReason(t *testing.T) {
	t.Parallel()

	st := store.New(t.TempDir())
	if err := st.Save(remoteBinding("zen")); err != nil {
		t.Fatal(err)
	}
	fr := &fakeRemote{unbindErr: &client.HTTPError{Status: 409, Body: remote.ErrorBody{Code: remote.CodeWorktreeKept, Message: oldServerKeptBody}}}
	rt := Runtime{Store: st, Remote: fr, Now: func() time.Time { return baseTime }}

	_, err := Unbind(context.Background(), rt, "api", false)
	if !errors.Is(err, ErrWorktreeKept) {
		t.Fatalf("Unbind err = %v, want ErrWorktreeKept", err)
	}
	if got, want := err.Error(), "worktree on zen kept: uncommitted changes"; got != want {
		t.Errorf("error = %q, want %q", got, want)
	}
	if strings.Contains(err.Error(), "--force") || strings.Contains(err.Error(), "serve unbind") {
		t.Errorf("error %q carries the server's hints", err)
	}
}
