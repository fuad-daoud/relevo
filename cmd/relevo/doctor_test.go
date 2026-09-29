package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/doctor"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/release"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/remote"
	"github.com/fuad-daoud/relevo/internal/roles"
	"github.com/fuad-daoud/relevo/internal/store"
)

func testSet(t *testing.T, body string) *candidate.Set {
	t.Helper()
	path := filepath.Join(t.TempDir(), "candidates.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	set, err := candidate.Load(path)
	if err != nil {
		t.Fatalf("candidate.Load: %v", err)
	}
	return set
}

const threeKinds = `[
  {"harness":"agy","provider":"t","model":"m","roles":["builder"]},
  {"harness":"claude","provider":"t","model":"m","roles":["builder"]},
  {"harness":"opencode","provider":"t","model":"m","roles":["builder"]}
]`

func TestAssembleKinds(t *testing.T) {
	tempHome := t.TempDir()
	st := store.New(filepath.Join(tempHome, "store"))
	tbl := testSet(t, threeKinds)

	// Default table has agy, claude, opencode
	kinds, err := assembleKinds(tbl, st)
	if err != nil {
		t.Fatalf("assembleKinds: %v", err)
	}
	expected := []string{"agy", "claude", "opencode"}
	if len(kinds) != len(expected) {
		t.Fatalf("kinds len = %d, want %d: %v", len(kinds), len(expected), kinds)
	}
	for i, k := range expected {
		if kinds[i] != k {
			t.Errorf("kinds[%d] = %q, want %q", i, kinds[i], k)
		}
	}

	// Add a binding with a custom builder kind
	b := store.Binding{
		Name: "custom",
		CWD:  tempHome,
		Builder: store.Endpoint{
			Kind: "custom-kind",
		},
	}
	if err := st.Save(b); err != nil {
		t.Fatalf("st.Save: %v", err)
	}

	kinds2, err := assembleKinds(tbl, st)
	if err != nil {
		t.Fatalf("assembleKinds: %v", err)
	}
	foundCustom := false
	for _, k := range kinds2 {
		if k == "custom-kind" {
			foundCustom = true
			break
		}
	}
	if !foundCustom {
		t.Errorf("custom-kind not found in assembled kinds: %v", kinds2)
	}
}

func TestAssembleKindsSurfacesErrors(t *testing.T) {
	// A store pointing at a file (not a directory) causes st.List() to fail.
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "file-not-dir")
	if err := os.WriteFile(filePath, []byte("data"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	st := store.New(filePath)
	tbl := testSet(t, threeKinds)

	kinds, err := assembleKinds(tbl, st)
	if err == nil {
		t.Error("assembleKinds should surface error from store.List(), got nil")
	}
	// ...and must still hand back what it did learn. A diagnostic that refuses
	// to diagnose because one of its own inputs is unreadable is worse than one
	// that reports the gap and checks the rest.
	if len(kinds) == 0 {
		t.Error("assembleKinds must still return the candidate-derived kinds when the store is unreadable")
	}
}

func TestAssembleKindsWithNoCandidatesUsesBindingsOnly(t *testing.T) {
	tempHome := t.TempDir()
	st := store.New(filepath.Join(tempHome, "store"))
	b := store.Binding{
		Name: "claude-binding",
		CWD:  tempHome,
		Builder: store.Endpoint{
			Kind: "claude",
		},
	}
	if err := st.Save(b); err != nil {
		t.Fatalf("st.Save: %v", err)
	}

	kinds, err := assembleKinds(testSet(t, "[]"), st)
	if err != nil {
		t.Fatalf("assembleKinds: %v", err)
	}
	if len(kinds) != 1 || kinds[0] != "claude" {
		t.Fatalf("kinds = %v, want [claude]", kinds)
	}
}

func TestAssembleDefinitionsFollowsCandidateRoles(t *testing.T) {
	set := testSet(t, `[
	  {"harness":"agy","provider":"t","model":"m","roles":["builder"]},
	  {"harness":"claude","provider":"t","model":"m","roles":["reviewer"]},
	  {"harness":"claude","provider":"t","model":"n","roles":["builder"]}
	]`)

	got := assembleDefinitions(set, []string{"agy", "claude", "opencode"})

	want := map[string][]string{
		"agy":      {"plan-executor", "researcher"},
		"claude":   {"plan-executor", "researcher", "reviewer"},
		"opencode": {"plan-executor", "researcher"}, // binding-only kind: builder's set
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("assembleDefinitions = %v, want %v", got, want)
	}
}

func TestAssembleDefinitionsNilSet(t *testing.T) {
	got := assembleDefinitions(nil, []string{"claude"})
	want := map[string][]string{"claude": {"plan-executor", "researcher"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("assembleDefinitions(nil) = %v, want %v", got, want)
	}
}

func TestLedgerChecks(t *testing.T) {
	now := time.Date(2026, 9, 11, 15, 0, 0, 0, time.UTC)
	gates := []availability.Gate{
		{Token: "claude/anthropic/sonnet", Kind: availability.RateLimited, Since: now, Until: time.Time{}},
		{Token: "agy/google/m", Kind: availability.SpawnFailed, Since: now, Until: now.Add(10 * time.Minute)},
	}

	checks := ledgerChecks(gates)
	if len(checks) != 2 {
		t.Fatalf("got %d checks, want 2: %+v", len(checks), checks)
	}

	if checks[0].Group != "claude" || checks[0].Name != "ledger" || checks[0].Severity != doctor.SevWarn {
		t.Errorf("check 0 = %+v", checks[0])
	}
	if !strings.HasPrefix(checks[0].Fix, "relevo gate --clear ") {
		t.Errorf("check 0 Fix = %q, want prefix %q", checks[0].Fix, "relevo gate --clear ")
	}

	if checks[1].Group != "agy" || checks[1].Name != "ledger" || checks[1].Severity != doctor.SevWarn {
		t.Errorf("check 1 = %+v", checks[1])
	}
	if !strings.HasPrefix(checks[1].Fix, "wait until ") {
		t.Errorf("check 1 Fix = %q, want prefix %q", checks[1].Fix, "wait until ")
	}

	if got := ledgerChecks(nil); len(got) != 0 {
		t.Errorf("ledgerChecks(nil) = %+v, want empty", got)
	}
}

func TestPolicyChecks(t *testing.T) {
	warnings := []relevo.PolicyWarning{
		{Role: "builder", Index: 1, Token: "claude/test/nope", Text: `order.builder[1] "claude/test/nope" is not a configured candidate`},
		{Role: "builder", Index: -1, Token: "opencode/test/m", Text: `builder: opencode/test/m serves the role but is not in order.builder`},
	}

	checks := policyChecks(warnings)
	if len(checks) != 2 {
		t.Fatalf("got %d checks, want 2: %+v", len(checks), checks)
	}

	for i, w := range warnings {
		c := checks[i]
		if c.Group != "" || c.Name != "policy" || c.Severity != doctor.SevWarn {
			t.Errorf("check %d = %+v", i, c)
		}
		if c.Detail != w.Text {
			t.Errorf("check %d Detail = %q, want %q", i, c.Detail, w.Text)
		}
		if c.Fix != "run relevo config edit" {
			t.Errorf("check %d Fix = %q, want %q", i, c.Fix, "run relevo config edit")
		}
	}

	if got := policyChecks(nil); len(got) != 0 {
		t.Errorf("policyChecks(nil) = %+v, want empty", got)
	}
}

// TestServerChecksScopesWarning pins #285's scopes warning and #295's quota:
// a queue-aware, enrolled server without systemd scopes gets a SevWarn row
// naming the daemon-restart risk, in addition to its builders census text on
// the ok row; Scopes:true gets no such warning row; a server that is not
// queue-aware (a pre-queue server) carries no builders text at all. Pure
// over a hand-built []relevo.ServerProbe -- no harness, no network.
//
// Mutation check: drop the `!p.Builders.Scopes` guard in serverChecks and
// the contabo warning row (Scopes:true) reappears, failing this test.
func TestServerChecksScopesWarning(t *testing.T) {
	probes := []relevo.ServerProbe{
		{
			Name: "zen", State: "enrolled", Label: "laptop", TierAware: true, BuilderTier: "edit", MaxTier: "edit",
			QueueAware: true, Builders: &remote.BuildersView{Running: 2, Queued: 1, Cap: 3, Scopes: false},
		},
		{
			Name: "contabo", State: "enrolled", Label: "vps", TierAware: true, BuilderTier: "edit", MaxTier: "edit",
			QueueAware: true, Builders: &remote.BuildersView{Running: 2, Queued: 1, Cap: 3, Scopes: true, Slice: "relevo.slice", Quota: "200%"},
		},
		{
			Name: "quotaonly", State: "enrolled", Label: "vps", TierAware: true, BuilderTier: "edit", MaxTier: "edit",
			QueueAware: true, Builders: &remote.BuildersView{Running: 1, Queued: 0, Cap: 3, Scopes: true, Quota: "150%"},
		},
		{
			Name: "sliceonly", State: "enrolled", Label: "vps", TierAware: true, BuilderTier: "edit", MaxTier: "edit",
			QueueAware: true, Builders: &remote.BuildersView{Running: 1, Queued: 0, Cap: 3, Scopes: true, Slice: "relevo.slice"},
		},
		{
			Name: "plain", State: "enrolled", Label: "vps", TierAware: true, BuilderTier: "edit", MaxTier: "edit",
			QueueAware: true, Builders: &remote.BuildersView{Running: 1, Queued: 0, Cap: 3, Scopes: true},
		},
		{
			Name: "old", State: "enrolled", Label: "laptop", TierAware: true, BuilderTier: "edit", MaxTier: "edit",
		},
	}

	checks := serverChecks(probes)

	var zenOK, zenWarn, contaboOK, contaboWarn, quotaOK, sliceOK, plainOK, oldOK *doctor.Check
	for i := range checks {
		c := &checks[i]
		switch {
		case strings.HasPrefix(c.Detail, "zen: enrolled"):
			zenOK = c
		case strings.HasPrefix(c.Detail, "scopes unavailable on zen"):
			zenWarn = c
		case strings.HasPrefix(c.Detail, "contabo: enrolled"):
			contaboOK = c
		case strings.HasPrefix(c.Detail, "scopes unavailable on contabo"):
			contaboWarn = c
		case strings.HasPrefix(c.Detail, "quotaonly: enrolled"):
			quotaOK = c
		case strings.HasPrefix(c.Detail, "sliceonly: enrolled"):
			sliceOK = c
		case strings.HasPrefix(c.Detail, "plain: enrolled"):
			plainOK = c
		case strings.HasPrefix(c.Detail, "old: enrolled"):
			oldOK = c
		}
	}

	if zenOK == nil || !strings.Contains(zenOK.Detail, "runners 2/3, 1 queued, scopes off") {
		t.Fatalf("zen ok check = %+v, want it naming the builders census", zenOK)
	}
	if zenWarn == nil || zenWarn.Severity != doctor.SevWarn ||
		zenWarn.Detail != "scopes unavailable on zen: a daemon restart kills its builders" {
		t.Fatalf("zen scopes warning = %+v, want the exact message", zenWarn)
	}

	if contaboOK == nil || !strings.Contains(contaboOK.Detail, "runners 2/3, 1 queued, scopes on (relevo.slice, 200%)") {
		t.Fatalf("contabo ok check = %+v, want it naming the builders census and quota", contaboOK)
	}
	if contaboWarn != nil {
		t.Fatalf("contabo scopes warning = %+v, want none (Scopes is true)", contaboWarn)
	}

	if quotaOK == nil || !strings.Contains(quotaOK.Detail, "runners 1/3, 0 queued, scopes on (150%)") {
		t.Fatalf("quotaonly ok check = %+v, want it naming the quota alone", quotaOK)
	}
	if sliceOK == nil || !strings.Contains(sliceOK.Detail, "runners 1/3, 0 queued, scopes on (relevo.slice)") {
		t.Fatalf("sliceonly ok check = %+v, want it naming the slice alone", sliceOK)
	}
	if plainOK == nil || !strings.HasSuffix(plainOK.Detail, "runners 1/3, 0 queued, scopes on") {
		t.Fatalf("plain ok check = %+v, want it naming scopes on", plainOK)
	}

	if oldOK == nil || strings.Contains(oldOK.Detail, "runners ") {
		t.Fatalf("old (pre-queue) ok check = %+v, want no builders text", oldOK)
	}
}

func TestRefusalChecks(t *testing.T) {
	refusals := []relevo.RoleRefusal{
		{Role: "builder", Text: "3 candidates serve builder and no order is set", NoOrder: true,
			Serving: []string{"agy/test/m", "claude/test/m", "opencode/test/m"}},
		{Role: "reviewer", Text: "every candidate serving reviewer is gated",
			Serving: []string{"claude/test/m"}, Gated: []string{"test"}},
	}

	checks := refusalChecks(refusals)
	if len(checks) != 2 {
		t.Fatalf("got %d checks, want 2: %+v", len(checks), checks)
	}
	for i, c := range checks {
		if c.Group != "" || c.Name != "policy" || c.Severity != doctor.SevWarn {
			t.Errorf("check %d = %+v", i, c)
		}
	}
	if want := "3 candidates serve builder and no order is set -- add/bind without --candidate would refuse"; checks[0].Detail != want {
		t.Errorf("builder Detail = %q, want %q", checks[0].Detail, want)
	}
	if want := `relevo config set policy '{"order":{"builder":["agy/test/m","claude/test/m","opencode/test/m"]}}'`; checks[0].Fix != want {
		t.Errorf("builder Fix = %q, want %q", checks[0].Fix, want)
	}
	if want := "every candidate serving reviewer is gated -- bind --actor reviewer without --candidate would refuse"; checks[1].Detail != want {
		t.Errorf("reviewer Detail = %q, want %q", checks[1].Detail, want)
	}
	if want := "relevo gate --clear test"; checks[1].Fix != want {
		t.Errorf("reviewer Fix = %q, want %q", checks[1].Fix, want)
	}
	if got := refusalChecks(nil); len(got) != 0 {
		t.Errorf("refusalChecks(nil) = %+v, want empty", got)
	}
}

// The unreadable-store gap is reported as a global row, positioned with the
// other global rows rather than after the per-kind blocks.
func TestInsertGlobalCheckKeepsRenderOrder(t *testing.T) {
	checks := []doctor.Check{
		{Name: "release", Severity: doctor.SevOK},
		{Name: "daemon", Severity: doctor.SevOK},
		{Group: "claude", Name: "binary", Severity: doctor.SevOK},
	}
	out := insertGlobalCheck(checks, doctor.Check{Name: "bindings", Severity: doctor.SevWarn, Detail: "could not list bindings"})
	if len(out) != 4 {
		t.Fatalf("len = %d, want 4", len(out))
	}
	if out[2].Name != "bindings" {
		t.Errorf("inserted row at %d (%q), want index 2 -- after the last global row", 2, out[2].Name)
	}
	if out[3].Group != "claude" {
		t.Errorf("per-kind rows must stay after the globals, got %+v", out[3])
	}
}

func TestRenderReportVerdict(t *testing.T) {
	// Success case
	repOk := doctor.Report{
		Checks: []doctor.Check{
			{Group: "", Name: "release", Severity: doctor.SevOK, Detail: "v0.7.0 is current"},
			{Group: "", Name: "daemon", Severity: doctor.SevOK, Detail: "running"},
			{Group: "claude", Name: "binary", Severity: doctor.SevOK, Detail: "/usr/bin/claude"},
			{Group: "claude", Name: "plan-executor", Severity: doctor.SevWarn, Detail: "missing: ~/.claude/agents/plan-executor.md", Fix: "relevo config agents --kind claude --role plan-executor"},
		},
		UsableBuilder: true,
	}

	var buf bytes.Buffer
	renderReport(&buf, repOk)
	out := buf.String()

	if !strings.Contains(out, "1 warning, 0 failures -- relevo can run.") {
		t.Errorf("expected success footer, got: %s", out)
	}
	if !strings.Contains(out, "    fix: relevo config agents --kind claude --role plan-executor") {
		t.Errorf("expected indented fix line, got: %s", out)
	}

	// Failure case
	repFail := doctor.Report{
		Checks: []doctor.Check{
			{Group: "agy", Name: "version", Severity: doctor.SevFail, Detail: "1.1.5 (below floor 1.1.6)"},
		},
		UsableBuilder: false,
	}

	buf.Reset()
	renderReport(&buf, repFail)
	outFail := buf.String()

	if !strings.Contains(outFail, "1 failure, 0 warnings -- no usable builder. Fix the failure above.") {
		t.Errorf("expected failure footer, got: %s", outFail)
	}

	// Middle case: no failures, !UsableBuilder (no checked harness on PATH)
	envStub := &stubDoctorEnv{
		daemonRun: true,
		lookPaths: map[string]string{},
	}
	repUnverified := doctor.Run(context.Background(), envStub, []string{"claude"})
	if repUnverified.Failures() != 0 {
		t.Fatalf("expected 0 failures, got %d", repUnverified.Failures())
	}
	if repUnverified.UsableBuilder {
		t.Fatal("expected UsableBuilder = false when no checked harness is on PATH")
	}

	buf.Reset()
	renderReport(&buf, repUnverified)
	outUnverified := buf.String()
	if !strings.Contains(outUnverified, "could not establish a usable builder: no checked harness") {
		t.Errorf("expected 'could not establish a usable builder.', got: %s", outUnverified)
	}
}

func TestRenderReportFooterPrecedence(t *testing.T) {
	healthy := []doctor.Check{
		{Name: "release", Severity: doctor.SevOK, Detail: "v0.7.0 is current"},
		{Name: "daemon", Severity: doctor.SevOK, Detail: "running"},
		{Group: "claude", Name: "binary", Severity: doctor.SevOK, Detail: "/usr/bin/claude"},
		{Group: "claude", Name: "plan-executor", Severity: doctor.SevOK, Detail: "~/.claude/agents/plan-executor.md"},
	}
	cases := []struct {
		name string
		rep  doctor.Report
		want string
	}{
		{"refusal beats can run",
			doctor.Report{Checks: healthy, UsableBuilder: true, BuilderRefusal: "3 candidates serve builder and no order is set"},
			"0 warnings, 0 failures -- relevo cannot pick a builder: 3 candidates serve builder and no order is set."},
		{"no usable builder beats refusal",
			doctor.Report{Checks: healthy, UsableBuilder: false, BuilderRefusal: "3 candidates serve builder and no order is set"},
			"could not establish a usable builder"},
		{"no candidates beats no usable builder",
			doctor.Report{Checks: healthy, UsableBuilder: false, NoCandidates: true},
			"0 warnings, 0 failures -- no candidates configured; set them with relevo config set candidates first."},
		{"a failure beats everything",
			doctor.Report{Checks: append(append([]doctor.Check(nil), healthy...), doctor.Check{Group: "agy", Name: "version", Severity: doctor.SevFail, Detail: "1.1.5 (below floor 1.1.6)"}), NoCandidates: true, BuilderRefusal: "y"},
			"Fix the failure above."},
		{"clean machine can run",
			doctor.Report{Checks: healthy, UsableBuilder: true},
			"0 warnings, 0 failures -- relevo can run."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			renderReport(&buf, tc.rep)
			if !strings.Contains(buf.String(), tc.want) {
				t.Errorf("footer missing %q in:\n%s", tc.want, buf.String())
			}
		})
	}
}

func TestPolicyExample(t *testing.T) {
	got := policyExample("builder", []string{"agy/test/m", "claude/test/m"})
	want := `{"order":{"builder":["agy/test/m","claude/test/m"]}}`
	if got != want {
		t.Errorf("policyExample = %s, want %s", got, want)
	}
}

type stubDoctorEnv struct {
	daemonRun bool
	daemonErr error
	// daemonInfo/daemonInfoOK satisfy doctor.Env (#371). The zero value here
	// means "no record", which is what a test that does not set them is
	// exercising; the silent-machine fixture records a matching version.
	daemonInfo   store.DaemonInfo
	daemonInfoOK bool
	lookPaths    map[string]string
	statErr      error // nil means every role file exists
}

func (s *stubDoctorEnv) DaemonRunning(ctx context.Context) (bool, error) {
	return s.daemonRun, s.daemonErr
}

// DaemonInfo satisfies doctor.Env (#371): the recorded daemon state, or "no
// record" when daemonInfoOK is false.
func (s *stubDoctorEnv) DaemonInfo() (store.DaemonInfo, bool, error) {
	return s.daemonInfo, s.daemonInfoOK, nil
}
func (s *stubDoctorEnv) LookPath(binary string) (string, error) {
	if p, ok := s.lookPaths[binary]; ok {
		return p, nil
	}
	return "", os.ErrNotExist
}
func (s *stubDoctorEnv) HomePath(rel string) (string, error) {
	return filepath.Join("/tmp", rel), nil
}
func (s *stubDoctorEnv) Stat(path string) error {
	return s.statErr
}

// ReleaseState satisfies doctor.Env (#293). These tests assert on severities
// and fix commands for the other rows, so every release state here reads as
// "no usable cache": the release row is SevOK/not checked and cannot mask one.
func (s *stubDoctorEnv) ReleaseState() (string, string, bool, release.Kind) {
	return "", "", false, release.KindUnknown
}

// LoadManifest satisfies doctor.Env (#371 §4.10): these stubs record no
// manifest, so the role-staleness row reads every definition as "would write".
func (s *stubDoctorEnv) LoadManifest() (map[string]string, error) {
	return nil, nil
}

// ReadFile satisfies doctor.Env. These tests assert on severities and fix
// commands, not on role-file contents, so every file reads as empty -- which
// doctor must render as a role row with no model suffix.
func (s *stubDoctorEnv) ReadFile(path string) ([]byte, error) {
	return nil, nil
}

// BinaryVersion satisfies doctor.Env. None of these tests exercise a kind
// with a MinVersion floor, so this is never called; it exists only to keep
// stubDoctorEnv implementing the interface.
func (s *stubDoctorEnv) BinaryVersion(ctx context.Context, path string) (string, error) {
	return "", nil
}

func (s *stubDoctorEnv) Probe(dir string) error {
	return nil
}

// Command satisfies doctor.Env. None of these tests exercise a kind whose
// checks shell out (the opencode session-count note is kind-gated and these
// tests only ever pass "claude"), so this exists only to keep stubDoctorEnv
// implementing the interface.
func (s *stubDoctorEnv) Command(ctx context.Context, bin string, args ...string) ([]byte, error) {
	return nil, os.ErrNotExist
}

// A probe relevo could not complete is not actionable and stays off the hot path.
// An actionable row in the same report must survive it -- the all-or-nothing
// filter this replaces dropped both, and its test could not tell the difference
// because the stub's Stat always succeeded.
func TestBindWarningLinesSkipsOnlyTheRowItCouldNotEstablish(t *testing.T) {
	env := &stubDoctorEnv{
		daemonErr: errors.New("connection reset by peer"),
		lookPaths: map[string]string{"claude": "/usr/bin/claude"},
		statErr:   os.ErrNotExist,
	}
	rep := doctor.Run(context.Background(), env, []string{"claude"})
	joined := strings.Join(bindWarningLines(rep), "\n")

	if strings.Contains(joined, "probe error") {
		t.Errorf("a row relevo could not establish must not reach the hot path: %s", joined)
	}
	if !strings.Contains(joined, "plan-executor") {
		t.Errorf("an actionable row must survive a failed probe elsewhere: %s", joined)
	}
}

// The exception: a failed probe whose verdict is SevFail means relevo cannot run.
// Silence would be the worst possible answer, so it prints anyway.
func TestBindWarningLinesReportsAFailureEvenWhenTheProbeFailed(t *testing.T) {
	rep := doctor.Report{Checks: []doctor.Check{
		{Group: "agy", Name: "version", Severity: doctor.SevFail,
			Detail: "1.1.5 (below floor 1.1.6)", Fix: "upgrade agy to >= 1.1.6", ProbeFailed: true},
	}}
	joined := strings.Join(bindWarningLines(rep), "\n")
	if !strings.Contains(joined, "version") {
		t.Errorf("a SevFail probe failure must still be reported at bind: %s", joined)
	}
}

func TestBindWarningLinesSilentWhenNothingIsWrong(t *testing.T) {
	env := &stubDoctorEnv{
		daemonRun:    true,
		daemonInfoOK: true, // a running daemon with a recorded, matching version
		lookPaths:    map[string]string{"claude": "/usr/bin/claude"},
	}
	rep := doctor.Run(context.Background(), env, []string{"claude"})
	if lines := bindWarningLines(rep); len(lines) != 0 {
		t.Errorf("a healthy machine must yield zero lines, got: %v", lines)
	}
}

// Global rows reach bind too: a stopped daemon makes a binding exactly as inert
// as a missing role file, which is the failure #24 is about.
func TestBindWarningLinesIncludesGlobalRows(t *testing.T) {
	rep := doctor.Report{Checks: []doctor.Check{
		{Name: "daemon", Severity: doctor.SevWarn, Detail: "not running", Fix: "relevo daemon"},
		{Group: "claude", Name: "plan-executor", Severity: doctor.SevOK, Detail: "~/.claude/agents/plan-executor.md"},
	}}
	joined := strings.Join(bindWarningLines(rep), "\n")
	if !strings.Contains(joined, "daemon not running") {
		t.Errorf("global rows must reach bind, got: %s", joined)
	}
}

// deadlineEnv records whether the context it was handed carried a deadline.
type deadlineEnv struct {
	stubDoctorEnv
	sawDeadline bool
}

func (e *deadlineEnv) DaemonRunning(ctx context.Context) (bool, error) {
	_, ok := ctx.Deadline()
	e.sawDeadline = ok
	return e.stubDoctorEnv.DaemonRunning(ctx)
}

// The bind preflight must be bounded: a probe that hangs must not add its
// delay to `relevo bind`.
func TestBindPreflightBoundsTheHotPath(t *testing.T) {
	env := &deadlineEnv{stubDoctorEnv: stubDoctorEnv{daemonRun: true}}
	bindPreflight(context.Background(), env, "claude", false)
	if !env.sawDeadline {
		t.Error("bindPreflight must hand doctor.Run a deadline-bounded context")
	}
}

// An adopted pane's binary is the user's to provide: the preflight must not
// warn about it. Fails if the call site stops passing `adopted` through.
func TestBindPreflightPassesAdoptedThrough(t *testing.T) {
	env := &stubDoctorEnv{daemonRun: true}

	adopted := strings.Join(bindPreflight(context.Background(), env, "claude", true), "\n")
	if strings.Contains(adopted, "binary") {
		t.Errorf("adopted preflight must not warn about the absent binary: %s", adopted)
	}

	normal := strings.Join(bindPreflight(context.Background(), env, "claude", false), "\n")
	if !strings.Contains(normal, "binary") {
		t.Errorf("non-adopted preflight must report the absent binary: %s", normal)
	}
}

func TestBindPreflightChecksOnlyBuilderDefinitions(t *testing.T) {
	env := &stubDoctorEnv{
		daemonRun: true,
		lookPaths: map[string]string{"claude": "/usr/bin/claude"},
		statErr:   os.ErrNotExist, // no role file exists
	}
	lines := bindPreflight(context.Background(), env, "claude", false)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "plan-executor") || !strings.Contains(joined, "researcher") {
		t.Errorf("preflight must warn about the builder's definitions, got:\n%s", joined)
	}
	if strings.Contains(joined, "reviewer") {
		t.Errorf("preflight must not warn about reviewer on a bind, got:\n%s", joined)
	}
}

func TestBindWarningAdoptedRealRunMissingBinary(t *testing.T) {
	envStub := &stubDoctorEnv{
		daemonRun: true,
		lookPaths: map[string]string{}, // binary absent
	}

	// Normal Run: the absent binary is reported
	normalRep := doctor.Run(context.Background(), envStub, []string{"claude"})
	normalLines := bindWarningLines(normalRep)
	joinedNormal := strings.Join(normalLines, "\n")
	if !strings.Contains(joinedNormal, "binary") {
		t.Errorf("expected binary warning for normal bind, got: %s", joinedNormal)
	}

	// Adopted Run: the binary is the user's own agent, so it must not warn
	adoptedRep := doctor.Run(context.Background(), envStub, []string{"claude"}, doctor.WithAdopted(true))
	adoptedLines := bindWarningLines(adoptedRep)
	joinedAdopted := strings.Join(adoptedLines, "\n")
	if strings.Contains(joinedAdopted, "binary") {
		t.Errorf("adopted bind must not have binary warning: %s", joinedAdopted)
	}
}

func TestBindWarningDaemonDownRealRun(t *testing.T) {
	envStub := &stubDoctorEnv{
		daemonRun: false,
		lookPaths: map[string]string{"claude": "/usr/bin/claude"},
	}
	rep := doctor.Run(context.Background(), envStub, []string{"claude"})
	lines := bindWarningLines(rep)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "daemon not running") {
		t.Errorf("expected daemon warning when daemon is down, got: %s", joined)
	}
}

// TestAssembleRoleDefinitionsFileMode pins §3.5: in file mode the per-kind
// definitions come from what the registry resolves for the roles serving each
// candidate, a kind reachable only through a binding gets the builder's
// definitions for that kind, and each list is sorted. Pure: no harness runs.
func TestAssembleRoleDefinitionsFileMode(t *testing.T) {
	set := testSet(t, `[{"harness":"claude","provider":"t","model":"m","roles":["builder"]}]`)
	reg, err := roles.Build(&roles.File{Rows: map[string]roles.Row{
		"builder": {
			Candidates:  []string{"claude/t/m"},
			Definitions: map[string]roles.DefRow{"claude": {Agent: "my-executor", Requires: []string{"my-scout"}}},
		},
	}}, set, policy.Policy{})
	if err != nil {
		t.Fatalf("roles.Build: %v", err)
	}

	got := assembleRoleDefinitions(reg, set, []string{"claude", "opencode"})
	want := map[string][]string{
		// The file's builder executor and what it dispatches to.
		"claude": {"my-executor", "my-scout"},
		// A binding-only kind, which no candidate names: the builder's shipped
		// definitions for that kind.
		"opencode": {"plan-executor", "researcher"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("assembleRoleDefinitions = %v, want %v", got, want)
	}
}

// TestDoctorChecksCustomWriterDefinition pins #382 §5.4: a custom writer role's
// definition is in the doctor scope for its kind, so the on-disk check covers
// it the same way it covers a custom reader's. Pure: no harness runs.
func TestDoctorChecksCustomWriterDefinition(t *testing.T) {
	set := testSet(t, `[{"harness":"claude","provider":"t","model":"m","roles":["builder"]}]`)
	writerShape := "writer"
	reg, err := roles.Build(&roles.File{Rows: map[string]roles.Row{
		"builder": {Candidates: []string{"claude/t/m"}},
		"ui-builder": {
			Shape:       &writerShape,
			Candidates:  []string{"claude/t/m"},
			Definitions: map[string]roles.DefRow{"claude": {Agent: "my-ui"}},
		},
	}}, set, policy.Policy{})
	if err != nil {
		t.Fatalf("roles.Build: %v", err)
	}

	got := assembleRoleDefinitions(reg, set, []string{"claude"})
	found := false
	for _, d := range got["claude"] {
		if d == "my-ui" {
			found = true
		}
	}
	if !found {
		t.Errorf("assembleRoleDefinitions[claude] = %v, want the custom writer definition my-ui", got["claude"])
	}
}

// TestDatabaseCheckRow pins doctor's `database` row (P3d §4.7): the path, the
// file size, the schema version and the binding_record live/archived counts,
// OK on a database that opens. It replaces `relevo db path` and
// `relevo db stats`, and there is no migrate row because every open migrates.
func TestDatabaseCheckRow(t *testing.T) {
	root := filepath.Join(t.TempDir(), "relevo")
	st := store.New(root)

	if err := st.Save(store.Binding{Name: "webshop", CWD: "/repo", Round: 1, State: store.StateActive}); err != nil {
		t.Fatalf("Save live: %v", err)
	}
	if err := st.Save(store.Binding{Name: "oldsite", CWD: "/old", Round: 1, State: store.StateDone}); err != nil {
		t.Fatalf("Save archived: %v", err)
	}
	if _, err := st.Archive("oldsite"); err != nil {
		t.Fatalf("Archive: %v", err)
	}

	c := databaseCheck(st)
	if c.Name != "database" || c.Group != "" {
		t.Fatalf("check = %+v, want the global `database` row", c)
	}
	if c.Severity != doctor.SevOK {
		t.Errorf("severity = %v, want ok", c.Severity)
	}
	for _, want := range []string{st.DBPath(), "schema v", "1 live, 1 archived"} {
		if !strings.Contains(c.Detail, want) {
			t.Errorf("detail = %q, want it to contain %q", c.Detail, want)
		}
	}
	if got := strings.Count(c.Detail, " · "); got != 3 {
		t.Errorf("detail = %q, want four ` · `-separated parts, got %d separators", c.Detail, got)
	}
}
