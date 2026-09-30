package relevo

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/account"
	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/store"
)

// testAccounts is four opencode logins on one provider, the cline-pass pool:
// enough logins that a rotation stays on the candidate instead of walking on.
func testAccounts() account.Set {
	return account.Set{
		{Name: "cp1", Harness: account.OpenCode, Groups: []string{"test"}, Integration: "cline-pass", Label: "ClinePass 1"},
		{Name: "cp2", Harness: account.OpenCode, Groups: []string{"test"}, Integration: "cline-pass", Label: "ClinePass 2"},
		{Name: "cp3", Harness: account.OpenCode, Groups: []string{"test"}, Integration: "cline-pass", Label: "ClinePass 3"},
		{Name: "cp4", Harness: account.OpenCode, Groups: []string{"test"}, Integration: "cline-pass", Label: "ClinePass 4"},
	}
}

// fakeOpencodeAuth is the injected seam: it records every flip and answers
// Active from a scripted label, so no test spawns opencode.
type fakeOpencodeAuth struct {
	active    string
	switches  []opencodeFlip
	activeErr error
	switchErr error
}

type opencodeFlip struct{ Integration, Label string }

func (f *fakeOpencodeAuth) Active(context.Context, string) (string, error) {
	if f.activeErr != nil {
		return "", f.activeErr
	}
	return f.active, nil
}

func (f *fakeOpencodeAuth) Switch(_ context.Context, integration, label string) error {
	if f.switchErr != nil {
		return f.switchErr
	}
	f.switches = append(f.switches, opencodeFlip{integration, label})
	f.active = label
	return nil
}

// accountRotateSetup binds webshop to opencode/test/m with the four-account
// pool and puts it on account cp2, round 1 open and a builder running.
func accountRotateSetup(t *testing.T, fr *fakeRunner) (Runtime, store.Binding) {
	t.Helper()
	rt := newRuntime(t)
	rt.Runner = fr
	rt.Candidates = candidateSet(t, testTwoProviderJSON)
	rt.Policy = orderOf("builder", testOpencodeRef, testClaudeRef)
	rt.Accounts = testAccounts()
	if _, err := Bind(context.Background(), rt, BindOptions{
		Name: "webshop", Candidate: testOpencodeRef, MasterMindID: testMasterMindName, CWD: "/repo", Headless: true,
	}); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	if _, err := Send(context.Background(), rt, "webshop", writePlan(t, "do it"), SendOptions{}); err != nil {
		t.Fatalf("Send: %v", err)
	}
	b, err := rt.Store.Load("webshop")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if b.BuilderAccount != "cp1" {
		t.Fatalf("setup account = %q, want cp1 (the pick's first ungated login)", b.BuilderAccount)
	}
	b.BuilderAccount = "cp2"
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	return rt, b
}

// TestRotationOnLimitStaysOnCandidate is the slice's headline case: a limit on
// account 2 of 4 rotates to account 3 on the SAME candidate, uncounted, and
// records the gate against the account that hit it.
func TestRotationOnLimitStaysOnCandidate(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := accountRotateSetup(t, fr)
	oldPID := b.Builder.PID
	fr.script(oldPID, false)
	fr.exit(oldPID, 1)
	logPath := b.Builder.LogPath
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, []byte("working\nquota reached: try again later\nrelevo-exit:1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := reconcile(t, at(rt, time.Minute), b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.BuilderCandidate != testOpencodeRef {
		t.Errorf("BuilderCandidate = %q, want %q: a rotation stays on the candidate", got.BuilderCandidate, testOpencodeRef)
	}
	if got.BuilderAccount != "cp3" {
		t.Errorf("BuilderAccount = %q, want cp3", got.BuilderAccount)
	}
	if got.RoundSwitches != 0 {
		t.Errorf("RoundSwitches = %d, want 0: a rotation is uncounted", got.RoundSwitches)
	}

	rl := rateLimitedEntries(loadLedger(t, rt))
	if len(rl) != 1 || rl[0].Subject != "test@cp2" {
		t.Errorf("rate_limited entries = %+v, want one on test@cp2", rl)
	}
	sw := switches(t, rt)
	if len(sw) != 1 || !strings.Contains(sw[0].Note, "; account cp3") {
		t.Errorf("switch entries = %+v, want one naming account cp3", sw)
	}
}

// TestPickEntryCarriesTheAccount pins that the pick a bind logs names the login
// it drew from, the same clause the switch entry carries.
func TestPickEntryCarriesTheAccount(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, _ := accountRotateSetup(t, fr)
	entries, err := rt.Store.ReadLog("webshop")
	if err != nil {
		t.Fatalf("ReadLog: %v", err)
	}
	var note string
	for _, e := range entries {
		if e.Kind == store.KindPick {
			note = e.Note
		}
	}
	if !strings.Contains(note, "; account cp1") {
		t.Errorf("pick note = %q, want it to name account cp1", note)
	}
}

// TestAllAccountsGatedWalksToNextCandidate pins the fallback: once every login
// in the pool is gated the candidate itself is gated, so the switch walks the
// actor order to the next candidate, which has no accounts of its own.
func TestAllAccountsGatedWalksToNextCandidate(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := accountRotateSetup(t, fr)
	for _, key := range []string{"test@cp1", "test@cp2", "test@cp3", "test@cp4"} {
		if _, err := availability.Unavailable(AvailabilityDeps(rt), testOpencodeRef, time.Time{}, "quota", key); err != nil {
			t.Fatalf("Unavailable(%s): %v", key, err)
		}
	}

	var got store.Binding
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		var err error
		got, err = switchBuilder(context.Background(), rt, tx, b, "gated", false, false)
		return err
	})
	if err != nil {
		t.Fatalf("switchBuilder: %v", err)
	}
	if got.BuilderCandidate != testClaudeRef {
		t.Errorf("BuilderCandidate = %q, want %q", got.BuilderCandidate, testClaudeRef)
	}
	if got.BuilderAccount != "" {
		t.Errorf("BuilderAccount = %q, want empty: no claude account is configured", got.BuilderAccount)
	}
}

// TestNextBuilderRotatesWithinPool is the pure rule: the next ungated login
// after the current one, wrapping, and nothing when the current login is not
// gated or the pool has none free.
func TestNextBuilderRotatesWithinPool(t *testing.T) {
	t.Parallel()

	set := testAccounts()
	cases := []struct {
		name    string
		account string
		gates   []string
		set     account.Set
		want    string
		ok      bool
	}{
		{name: "no accounts", account: "cp2", gates: []string{"test@cp2"}, set: nil, ok: false},
		{name: "no account recorded", account: "", gates: []string{"test@cp2"}, set: set, ok: false},
		{name: "current not gated", account: "cp2", gates: nil, set: set, ok: false},
		{name: "next after current", account: "cp2", gates: []string{"test@cp2"}, set: set, want: "cp3", ok: true},
		{name: "wraps past the last", account: "cp4", gates: []string{"test@cp4"}, set: set, want: "cp1", ok: true},
		{name: "skips a gated next", account: "cp2", gates: []string{"test@cp2", "test@cp3"}, set: set, want: "cp4", ok: true},
		{name: "pool exhausted", account: "cp2", gates: []string{"test@cp1", "test@cp2", "test@cp3", "test@cp4"}, set: set, ok: false},
		{name: "bare group gates all", account: "cp2", gates: []string{"test"}, set: set, ok: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			b := store.Binding{BuilderCandidate: testOpencodeRef, BuilderAccount: c.account}
			got, ok := nextBuilder(b, AccountPick{Set: c.set, Gates: c.gates})
			if ok != c.ok {
				t.Fatalf("nextBuilder ok = %v, want %v", ok, c.ok)
			}
			if ok && got.Name != c.want {
				t.Errorf("nextBuilder = %q, want %q", got.Name, c.want)
			}
		})
	}
}

// TestRotationFlipsOpencodeActive pins the seam: a rotation moves the
// install-global opencode row to the new account and records it, because
// opencode has no per-process login.
func TestRotationFlipsOpencodeActive(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := accountRotateSetup(t, fr)
	fa := &fakeOpencodeAuth{active: "ClinePass 2"} // the row relevo thinks is active
	rt.OpencodeAuth = fa
	if _, err := availability.Unavailable(AvailabilityDeps(rt), testOpencodeRef, time.Time{}, "quota", "test@cp2"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}

	var got store.Binding
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		var err error
		got, err = switchBuilder(context.Background(), rt, tx, b, "rate-limited", false, false)
		return err
	})
	if err != nil {
		t.Fatalf("switchBuilder: %v", err)
	}
	if got.BuilderAccount != "cp3" {
		t.Fatalf("BuilderAccount = %q, want cp3", got.BuilderAccount)
	}
	if len(fa.switches) != 1 || fa.switches[0] != (opencodeFlip{"cline-pass", "ClinePass 3"}) {
		t.Fatalf("switches = %+v, want one flip to ClinePass 3", fa.switches)
	}
	if got := readActiveAccount(rt.Gates, "cline-pass"); got != "ClinePass 3" {
		t.Errorf("recorded active account = %q, want ClinePass 3", got)
	}
}

// TestRotationLeavesAnUngatedActiveRow pins the drift guard: when the row
// opencode is actually using is another, ungated login, a healthy round is not
// moved, even though the binding rotates off its own gated account.
func TestRotationLeavesAnUngatedActiveRow(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := accountRotateSetup(t, fr)
	fa := &fakeOpencodeAuth{active: "ClinePass 4"} // drifted, and cp4 is not gated
	rt.OpencodeAuth = fa
	if _, err := availability.Unavailable(AvailabilityDeps(rt), testOpencodeRef, time.Time{}, "quota", "test@cp2"); err != nil {
		t.Fatalf("Unavailable: %v", err)
	}

	var got store.Binding
	err := rt.Store.WithLock(func(tx *store.Tx) error {
		var err error
		got, err = switchBuilder(context.Background(), rt, tx, b, "rate-limited", false, false)
		return err
	})
	if err != nil {
		t.Fatalf("switchBuilder: %v", err)
	}
	if got.BuilderAccount != "cp3" {
		t.Fatalf("BuilderAccount = %q, want cp3", got.BuilderAccount)
	}
	if len(fa.switches) != 0 {
		t.Errorf("switches = %+v, want none: the active row is not gated", fa.switches)
	}
}

// TestResumeKeepsTheAccount pins the resume rule: a lost builder resumed after
// a daemon restart keeps the account it recorded, and the seam is asked to
// restore the row to that login.
func TestResumeKeepsTheAccount(t *testing.T) {
	t.Parallel()

	fr := newFakeRunner()
	rt, b := accountRotateSetup(t, fr)
	fa := &fakeOpencodeAuth{active: "ClinePass 1"} // a human moved the row
	rt.OpencodeAuth = fa
	// A session id is what makes the lost-builder path resume rather than
	// relaunch fresh, so the account restore runs.
	b.Builder.StreamSessionID = "sess-x"
	if err := rt.Store.Save(b); err != nil {
		t.Fatalf("Save: %v", err)
	}
	rt.StartedAt = time.Unix(b.Builder.StartedAt+60, 0)
	rt.Watched = NewWatched()
	fr.script(b.Builder.PID, false)
	rt = withClock(rt, &fakeClock{now: baseTime.Add(10 * time.Minute)})

	got, err := reconcile(t, rt, b)
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if got.BuilderAccount != "cp2" {
		t.Errorf("BuilderAccount = %q, want cp2: a resume keeps the recorded account", got.BuilderAccount)
	}
	if len(fa.switches) != 1 || fa.switches[0] != (opencodeFlip{"cline-pass", "ClinePass 2"}) {
		t.Errorf("switches = %+v, want the row restored to ClinePass 2", fa.switches)
	}
}

// TestNoAccountsKeepsTheGateBare is the empty-pool rule: with no accounts a
// pick records none and a limit stays on the bare group, byte-identical to
// before accounts existed.
func TestNoAccountsKeepsTheGateBare(t *testing.T) {
	t.Parallel()

	rt, b := sentSwitchable(t) // no accounts configured
	if got := pickFor(rt); len(got.Set) != 0 || got.Gates != nil || got.Mode != "" {
		t.Fatalf("pickFor with no accounts = %+v, want the zero value", got)
	}
	if got, ok := nextBuilder(b, pickFor(rt)); ok {
		t.Fatalf("nextBuilder with no accounts = %v, %v; want no rotation", got.Name, ok)
	}
	res, err := resolveRole(rt.RoleRegistry(), rt.Candidates, availability.Gates(AvailabilityDeps(rt)), "", "builder", pickFor(rt))
	if err != nil {
		t.Fatalf("resolveRole: %v", err)
	}
	if res.Account != "" {
		t.Errorf("Resolution.Account = %q, want empty", res.Account)
	}
}
