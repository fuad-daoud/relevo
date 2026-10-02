package ui

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/mastermind"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
)

// gateCall is one Gate invocation, recorded by fakeActions.
type gateCall struct {
	subject string
	forDur  time.Duration
	reason  string
}

// sendCall is one Send invocation, recorded by fakeActions.
type sendCall struct{ key, file string }

// retryCall is one Retry invocation, recorded by fakeActions.
type retryCall struct{ key, candidate string }

// openCall is one OpenArtifact invocation, recorded by fakeActions (round 5b).
type openCall struct{ path, kind string }

// fakeActions is the double every key -> confirm -> action path is tested
// against (§7): it records every call with its arguments and returns scripted
// Results.
type fakeActions struct {
	stops   []string
	dones   []string
	unbinds []string
	gates   []gateCall
	ungates []string
	shells  []string

	// Round 2: the calls that take a file, a binding input or a candidate,
	// plus the candidate lists the prompts cycle.
	sends    []sendCall
	binds    []BindInput
	retries  []retryCall
	pulls    []string
	candRole []string // the roles Candidates was asked for

	candidates []string // what Candidates returns
	chainsDoc  relevo.ChainsDoc
	chainsErr  error
	// The scripted per-chain traces the trace view reads, by chain name.
	chainTraces map[string]relevo.ChainTraceDoc
	traceErr    error
	pullText    string
	pullOK      bool
	pullErr     error

	// The config views (round 2): the scripted stored doc and its error,
	// plus the edits and probes the view made.
	doc         relevo.ConfigDoc
	docErr      error
	configEdits []relevo.ConfigEdit
	probes      []string

	// The servers view: the scripted health probes ServerProbes answers with,
	// and how many times it was asked.
	serverProbes     []relevo.ServerProbe
	serverProbeCalls int

	// The agents view (round 5): the scripted per-agent file states, the
	// files the user's editor was opened on, and the (kind, agent) pairs a
	// reset was asked for.
	files    map[string][]harness.AgentFile
	edited   []string
	resets   [][2]string
	filesErr error

	// The artifacts tab (round 5b): the (path, kind) pairs OpenArtifact was
	// asked for.
	opened []openCall

	// The audit view (round 6) (§3.3): the revision rows, each revision's
	// changes, the scripted roll back preview and its error, and the
	// revisions a roll back was asked for.
	revs       []db.RevisionRow
	changes    map[int64][]relevo.ChangeLine
	preview    []relevo.ChangeLine
	previewErr error
	rollbacks  []int64

	result   Result
	shellCmd *exec.Cmd
	shellErr error
}

func (f *fakeActions) Stop(_ context.Context, key string) Result {
	f.stops = append(f.stops, key)
	return f.result
}

func (f *fakeActions) Done(_ context.Context, key string) Result {
	f.dones = append(f.dones, key)
	return f.result
}

func (f *fakeActions) Unbind(_ context.Context, key string) Result {
	f.unbinds = append(f.unbinds, key)
	return f.result
}

func (f *fakeActions) Gate(_ context.Context, subject string, forDur time.Duration, reason string) Result {
	f.gates = append(f.gates, gateCall{subject: subject, forDur: forDur, reason: reason})
	return f.result
}

func (f *fakeActions) Ungate(_ context.Context, subject string) Result {
	f.ungates = append(f.ungates, subject)
	return f.result
}

func (f *fakeActions) Shell(key string) (*exec.Cmd, error) {
	f.shells = append(f.shells, key)
	return f.shellCmd, f.shellErr
}

func (f *fakeActions) Send(_ context.Context, key, planFile string) Result {
	f.sends = append(f.sends, sendCall{key: key, file: planFile})
	return f.result
}

func (f *fakeActions) Bind(_ context.Context, in BindInput) Result {
	f.binds = append(f.binds, in)
	return f.result
}

func (f *fakeActions) Retry(_ context.Context, key, candidate string) Result {
	f.retries = append(f.retries, retryCall{key: key, candidate: candidate})
	return f.result
}

func (f *fakeActions) Pull(_ context.Context, key string) (string, bool, error) {
	f.pulls = append(f.pulls, key)
	return f.pullText, f.pullOK, f.pullErr
}

func (f *fakeActions) Candidates(role string) []string {
	f.candRole = append(f.candRole, role)
	return f.candidates
}

func (f *fakeActions) ConfigDoc() (relevo.ConfigDoc, error) { return f.doc, f.docErr }

func (f *fakeActions) Chains(_ context.Context) (relevo.ChainsDoc, error) {
	return f.chainsDoc, f.chainsErr
}

func (f *fakeActions) ChainTrace(_ context.Context, name string) (relevo.ChainTraceDoc, error) {
	if f.traceErr != nil {
		return relevo.ChainTraceDoc{}, f.traceErr
	}
	return f.chainTraces[name], nil
}

func (f *fakeActions) ApplyConfig(_ context.Context, e relevo.ConfigEdit) Result {
	f.configEdits = append(f.configEdits, e)
	return f.result
}

// ServerProbes answers the scripted server probes and counts the calls, so a
// test can tell a load-time probe from a re-probe.
func (f *fakeActions) ServerProbes(_ context.Context) []relevo.ServerProbe {
	f.serverProbeCalls++
	return f.serverProbes
}

// A nil adapter, and a runtime with no config store, answer no server probes
// rather than panicking.
func TestServerProbesNilRuntimeIsEmpty(t *testing.T) {
	var a *mastermindActions
	if got := a.ServerProbes(context.Background()); got != nil {
		t.Errorf("ServerProbes on a nil adapter = %v, want none", got)
	}
	a = &mastermindActions{live: newLiveRuntime(relevo.Runtime{})}
	if got := a.ServerProbes(context.Background()); len(got) != 0 {
		t.Errorf("ServerProbes with no config store = %v, want none", got)
	}
}

func (f *fakeActions) Probe(_ context.Context, name string) Result {
	f.probes = append(f.probes, name)
	return f.result
}

// AgentFiles answers the scripted per-agent file states (§3): a name the
// fixture does not carry has none, as a custom agent does.
func (f *fakeActions) AgentFiles(agent string) ([]harness.AgentFile, error) {
	if f.filesErr != nil {
		return nil, f.filesErr
	}
	return f.files[agent], nil
}

// ResetAgentFile records the (kind, agent) pair it was asked for (§3). The
// fake never touches $HOME: the files it scripts are strings.
func (f *fakeActions) ResetAgentFile(_ context.Context, kind, agent string) Result {
	f.resets = append(f.resets, [2]string{kind, agent})
	return f.result
}

// AgentEditor records the path the view opened and returns a command that
// would run if the process ever ran it. It is exec.Command("true") so no test
// starts a real editor (§7).
func (f *fakeActions) AgentEditor(path string) (*exec.Cmd, error) {
	f.edited = append(f.edited, path)
	return exec.Command("true"), nil
}

// OpenArtifact records the (path, kind) pair the view opened (round 5b) and
// returns a command no test ever runs.
func (f *fakeActions) OpenArtifact(path, kind string) (*exec.Cmd, error) {
	f.opened = append(f.opened, openCall{path: path, kind: kind})
	return exec.Command("true"), nil
}

// ConfigLog answers the scripted revision rows (§3.3).
func (f *fakeActions) ConfigLog() ([]db.RevisionRow, error) { return f.revs, nil }

// ConfigChanges answers the scripted lines for rev: a revision the fixture does
// not carry has no changes.
func (f *fakeActions) ConfigChanges(rev int64) ([]relevo.ChangeLine, error) {
	return f.changes[rev], nil
}

// RollbackPreview answers the scripted preview and its error (§3.3).
func (f *fakeActions) RollbackPreview(rev int64) ([]relevo.ChangeLine, error) {
	return f.preview, f.previewErr
}

// Rollback records the revision it was asked for (§3.3).
func (f *fakeActions) Rollback(_ context.Context, rev int64) Result {
	f.rollbacks = append(f.rollbacks, rev)
	return f.result
}

// key is one rune keypress, as the tests send them.
func key(r rune) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}} }

// actionModel is a loaded shell at 140x40 with a as its Actions seam.
func actionModel(t *testing.T, a Actions, rows ...view.BindingStatus) Model {
	t.Helper()
	st := store.New(t.TempDir())
	m := newModel(context.Background(), mastermindSource{relevo.Runtime{Store: st}}, Options{Interval: time.Second, Actions: a})
	m.now = func() time.Time { return railNow }
	res, _ := m.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	m = res.(Model)
	m.statusInFlight = false
	res, _ = m.Update(statusMsg{report: view.Report{Bindings: rows}})
	return res.(Model)
}

// goldenActionModel is actionModel for the goldens: the full report with a as
// the Actions seam.
func goldenActionModel(t *testing.T, width, height int, a Actions, rep view.Report) Model {
	t.Helper()
	return goldenActionModelWithStore(t, width, height, a, rep, store.New(t.TempDir()))
}

// goldenActionModelWithStore is goldenActionModel over st, so a fixture whose
// plan tab reads its body and sent time from a seeded store can supply it.
func goldenActionModelWithStore(t *testing.T, width, height int, a Actions, rep view.Report, st *store.Store) Model {
	t.Helper()
	m := newModel(context.Background(), mastermindSource{relevo.Runtime{Store: st}}, Options{Interval: time.Second, Actions: a, Version: "v0.13.0-28-gb66c6fc"})
	m.now = func() time.Time { return railNow }
	res, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: height})
	m = res.(Model)
	m.statusInFlight = false
	res, _ = m.Update(statusMsg{report: rep})
	return res.(Model)
}

// pointer points the fleet's cursor at key.
func pointer(t *testing.T, m Model, key string) Model {
	t.Helper()
	fv := fleet(m)
	rows := fv.rows(m.env())
	for i := range rows {
		if rows[i].Key() == key {
			fv.cursor, fv.sticky = i, key
			m.stack[0] = fv
			return m
		}
	}
	t.Fatalf("no row keyed %q", key)
	return m
}

func TestStopKeyConfirmsThenCalls(t *testing.T) {
	fa := &fakeActions{result: Result{Text: "webshop round 4 stopped", Refresh: true}}
	m := actionModel(t, fa, view.BindingStatus{Name: "webshop", Round: 4, Display: "ACTIVE"})

	res, cmd := m.Update(key('x'))
	m = drain(t, res.(Model), cmd)
	if m.overlay == nil {
		t.Fatal("x must open the stop confirm")
	}

	// n cancels: nothing happens.
	res, cmd = m.Update(key('n'))
	m = res.(Model)
	if m.overlay != nil {
		t.Error("n must close the confirm")
	}
	if cmd != nil {
		m = drain(t, m, cmd)
	}
	if len(fa.stops) != 0 {
		t.Errorf("n must call nothing, stops = %v", fa.stops)
	}

	// y confirms: Stop is called with the row's key.
	res, cmd = m.Update(key('x'))
	m = drain(t, res.(Model), cmd)
	res, cmd = m.Update(key('y'))
	m = res.(Model)
	if m.overlay != nil {
		t.Error("y must close the confirm")
	}
	m = drain(t, m, cmd)

	if len(fa.stops) != 1 || fa.stops[0] != "webshop" {
		t.Fatalf("stops = %v, want one webshop", fa.stops)
	}
}

func TestConfirmNamesTheOwningMasterMind(t *testing.T) {
	owned := view.BindingStatus{Name: "webshop", Round: 4, Display: "ACTIVE", MasterMindName: "architect-1"}
	yours := owned
	yours.MasterMindName = "you"

	if got := strings.Join(stopConfirmLines(owned, railNow), "\n"); !strings.Contains(got, "MasterMind architect-1 is waiting on this round") {
		t.Errorf("stop confirm must name the waiting mastermind:\n%s", got)
	}
	if got := strings.Join(doneConfirmLines(owned), "\n"); !strings.Contains(got, "MasterMind architect-1 owns this binding") {
		t.Errorf("done confirm must name the owning mastermind:\n%s", got)
	}
	if got := strings.Join(unbindConfirmLines(owned), "\n"); !strings.Contains(got, "MasterMind architect-1 owns this binding") {
		t.Errorf("unbind confirm must name the owning mastermind:\n%s", got)
	}

	for _, got := range []string{
		strings.Join(stopConfirmLines(yours, railNow), "\n"),
		strings.Join(doneConfirmLines(yours), "\n"),
		strings.Join(unbindConfirmLines(yours), "\n"),
		strings.Join(stopConfirmLines(view.BindingStatus{}, railNow), "\n"),
	} {
		if strings.Contains(got, "MasterMind ") {
			t.Errorf("no mastermind line for you or an empty mastermind:\n%s", got)
		}
	}
}

func TestDoneKeyConfirmsThenCalls(t *testing.T) {
	fa := &fakeActions{result: Result{Text: "atlas marked done", Refresh: true}}
	m := actionModel(t, fa, view.BindingStatus{Name: "atlas", Round: 4, Display: "ACTIVE"})

	res, cmd0 := m.Update(key('D'))
	m = drain(t, res.(Model), cmd0)
	if m.overlay == nil {
		t.Fatal("D must open the done confirm")
	}
	box, ok := m.overlay.(confirmBox)
	if !ok {
		t.Fatalf("D must open a confirmBox, got %T", m.overlay)
	}
	if box.title != "Mark atlas done?" {
		t.Errorf("done confirm title = %q", box.title)
	}

	res, cmd := m.Update(key('y'))
	m = drain(t, res.(Model), cmd)
	if len(fa.dones) != 1 || fa.dones[0] != "atlas" {
		t.Fatalf("dones = %v, want one atlas", fa.dones)
	}
}

func TestUnbindKeyConfirmsThenCalls(t *testing.T) {
	fa := &fakeActions{result: Result{Text: "archived atlas", Refresh: true}}
	m := actionModel(t, fa, view.BindingStatus{Name: "atlas", Round: 4, Display: "ACTIVE", Branch: "relevo/atlas"})

	res, cmd0 := m.Update(key('u'))
	m = drain(t, res.(Model), cmd0)
	if m.overlay == nil {
		t.Fatal("u must open the unbind confirm")
	}
	box, ok := m.overlay.(confirmBox)
	if !ok {
		t.Fatalf("u must open a confirmBox, got %T", m.overlay)
	}
	if !strings.Contains(box.title, "its branch relevo/atlas is kept") {
		t.Errorf("unbind confirm title = %q", box.title)
	}

	res, cmd := m.Update(key('y'))
	m = drain(t, res.(Model), cmd)
	if len(fa.unbinds) != 1 || fa.unbinds[0] != "atlas" {
		t.Fatalf("unbinds = %v, want one atlas", fa.unbinds)
	}
}

// TestGatePromptValidatesDuration: the gate form's duration rule. Ported for
// O3 (gate's two chained prompts became one form): an invalid duration keeps
// the form open on `for`; a valid one submits both fields at once.
func TestGatePromptValidatesDuration(t *testing.T) {
	fa := &fakeActions{result: Result{Text: "gated", Refresh: true}}
	m := actionModel(t, fa, view.BindingStatus{Name: "atlas", Round: 4, Display: "ACTIVE", BuilderCandidate: "opencode/cline-pass/glm"})

	res, cmd0 := m.Update(key('g'))
	m = drain(t, res.(Model), cmd0)
	if m.overlay == nil {
		t.Fatal("g must open the gate form")
	}
	fb, ok := m.overlay.(formBox)
	if !ok {
		t.Fatalf("g must open a formBox, got %T", m.overlay)
	}

	// An invalid duration keeps the form open with the error.
	fb.fields[0].input.SetValue("nope")
	m.overlay = fb
	res, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = res.(Model)
	kept, ok := m.overlay.(formBox)
	if !ok {
		t.Fatalf("an invalid duration must keep the form open, got %T", m.overlay)
	}
	if kept.err == "" {
		t.Error("an invalid duration must show the validation error")
	}
	if kept.focus != 0 {
		t.Errorf("an invalid duration must focus `for`, got focus %d", kept.focus)
	}

	// A valid duration and reason submit together.
	kept.fields[0].input.SetValue("2h")
	kept.fields[1].input.SetValue("quota")
	m.overlay = kept
	res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = drain(t, res.(Model), cmd)

	if len(fa.gates) != 1 {
		t.Fatalf("gates = %v, want one call", fa.gates)
	}
	got := fa.gates[0]
	if got.subject != "opencode/cline-pass/glm" || got.forDur != 2*time.Hour || got.reason != "quota" {
		t.Errorf("gate call = %+v", got)
	}
}

func TestUngateCommand(t *testing.T) {
	fa := &fakeActions{result: Result{Text: "cleared cline-pass (1 entries)", Refresh: true}}
	m := actionModel(t, fa, view.BindingStatus{Name: "atlas", Round: 4, Display: "ACTIVE"})

	cmd := execLine("ungate cline-pass", m.env(), m.prefs)
	m = drain(t, m, cmd)

	if m.overlay != nil {
		t.Error("ungate must have no confirm")
	}
	if len(fa.ungates) != 1 || fa.ungates[0] != "cline-pass" {
		t.Fatalf("ungates = %v, want one cline-pass", fa.ungates)
	}

	// The command row exists, and its completions are the gated providers
	// and names.
	found := false
	for _, c := range commands {
		if c.name == "ungate" {
			found = true
		}
	}
	if !found {
		t.Error("the command table must have an ungate row")
	}
	env := m.env()
	env.Report.Gated = gatedGates()
	c := newCmdLine()
	c.input.SetValue("ungate")
	var names []string
	for _, match := range c.matches(env) {
		if strings.HasPrefix(match.name, "ungate ") {
			names = append(names, match.name)
		}
	}
	if len(names) == 0 || names[0] != "ungate codex" {
		t.Errorf("ungate completions = %v, want the gated provider first", names)
	}
}

func TestActionResultBecomesNoticeAndLog(t *testing.T) {
	m := actionModel(t, &fakeActions{}, view.BindingStatus{Name: "atlas", Round: 4, Display: "ACTIVE"})

	res, _ := m.Update(workingMsg{verb: "stop", key: "atlas"})
	m = res.(Model)
	if got := stripANSI(m.keysView(m.env())); !strings.Contains(got, "working: stop atlas…") {
		t.Errorf("the footer must show the action in flight, got %q", got)
	}

	res, _ = m.Update(actionMsg{verb: "stop", key: "atlas", res: Result{
		Text:    "atlas round 4 stopped\nmore detail",
		Refresh: true,
	}})
	m = res.(Model)

	if m.notice != "atlas round 4 stopped" {
		t.Errorf("notice = %q, want the first line", m.notice)
	}
	if m.noticeErr {
		t.Error("a successful action's notice must not be an error")
	}
	if len(m.actionLog) != 1 || m.actionLog[0].Text != "atlas round 4 stopped\nmore detail" || m.actionLog[0].Err || m.actionLog[0].Verb != "stop" {
		t.Errorf("log = %+v, want the full text", m.actionLog)
	}
	if _, ok := m.running["atlas"]; ok {
		t.Error("the action must no longer be in flight")
	}

	// An error is a red notice and its own log entry.
	res, _ = m.Update(actionMsg{verb: "stop", key: "atlas", res: Result{Err: errSentinel}})
	m = res.(Model)
	if !strings.Contains(m.notice, "sentinel") {
		t.Errorf("notice = %q, want the error", m.notice)
	}
	if !m.noticeErr {
		t.Error("an action error must render in errorStyle")
	}
	if len(m.actionLog) != 2 || !strings.Contains(m.actionLog[1].Text, "sentinel") || !m.actionLog[1].Err || m.actionLog[1].Verb != "stop" {
		t.Errorf("log = %+v, want the error appended", m.actionLog)
	}
}

func TestSecondActionOnSameBindingRefused(t *testing.T) {
	fa := &fakeActions{}
	m := actionModel(t, fa, view.BindingStatus{Name: "atlas", Round: 4, Display: "ACTIVE"})

	res, _ := m.Update(workingMsg{verb: "stop", key: "atlas"})
	m = res.(Model)

	res, cmd := m.Update(key('x'))
	m = res.(Model)
	if m.overlay != nil {
		t.Error("a second action must be refused, not confirmed")
	}
	m = drain(t, m, cmd)

	if !strings.Contains(m.notice, "atlas: stop still running") {
		t.Errorf("notice = %q, want the refused line", m.notice)
	}
	if len(fa.stops) != 0 {
		t.Errorf("the refused action must call nothing, stops = %v", fa.stops)
	}
}

func TestActionKeysHiddenWithoutActions(t *testing.T) {
	m := actionModel(t, nil, view.BindingStatus{Name: "atlas", Round: 4, Display: "ACTIVE"})

	hidden := map[string]bool{"x": true, "D": true, "u": true, "g": true, "o": true, "E": true, "b": true, "r": true}
	for _, kh := range fleet(m).Keys() {
		if hidden[kh.Key] {
			t.Errorf("the fleet must not advertise %q without Actions", kh.Key)
		}
	}

	for _, r := range []rune{'x', 'E', 'b', 'r'} {
		res, cmd := m.Update(key(r))
		m = res.(Model)
		if cmd != nil || m.overlay != nil {
			t.Errorf("%q must do nothing without Actions", r)
		}
	}
}

func TestShellKeyExecs(t *testing.T) {
	fa := &fakeActions{shellCmd: exec.Command("true")}
	m := actionModel(t, fa, view.BindingStatus{Name: "atlas", Round: 4, Display: "ACTIVE"})

	res, cmd := m.Update(key('o'))
	m = res.(Model)
	if cmd == nil {
		t.Fatal("o must return an exec command")
	}
	if len(fa.shells) != 1 || fa.shells[0] != "atlas" {
		t.Fatalf("shells = %v, want one atlas", fa.shells)
	}
	got, err := fa.Shell("atlas")
	if err != nil {
		t.Fatalf("Shell: %v", err)
	}
	if got == nil || got.Args[0] != "true" {
		t.Fatalf("the fake must return an exec command, got %v", got)
	}
	_ = m
}

// errSentinel is a distinguishable error string for the notice tests.
var errSentinel = sentinelErr("sentinel failure")

type sentinelErr string

func (e sentinelErr) Error() string { return string(e) }

func TestEnsureYouIdempotent(t *testing.T) {
	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })

	reg := &mastermind.DBRegistry{
		KV:  db.TxKV{DB: d},
		Now: func() time.Time { return railNow },
	}
	rt := relevo.Runtime{MasterMinds: reg, Now: func() time.Time { return railNow }}

	first, err := ensureYou(rt)
	if err != nil {
		t.Fatalf("ensureYou: %v", err)
	}
	second, err := ensureYou(rt)
	if err != nil {
		t.Fatalf("ensureYou again: %v", err)
	}
	if first != second {
		t.Errorf("ensureYou is not idempotent: %q then %q", first, second)
	}

	recs, err := reg.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(recs) != 1 {
		t.Fatalf("%d mastermind records, want 1", len(recs))
	}
	if recs[0].HarnessKind != "human" || recs[0].Name != "you" || recs[0].SessionID != "tui" {
		t.Errorf("record = %+v, want the human tui mastermind named you", recs[0])
	}
}

// --- round 2: send, bind, retry, report ready ---------------------------

// TestSendPromptThenConfirm: the picker's validation and confirm. Ported for
// O4 (the bare plan-file prompt became the picker): a missing file keeps the
// picker open with the error, a real file opens the existing send confirm.
func TestSendPromptThenConfirm(t *testing.T) {
	dir := t.TempDir()
	plan := filepath.Join(dir, "plan.md")
	if err := os.WriteFile(plan, []byte("# plan\n"), 0o644); err != nil {
		t.Fatalf("write plan: %v", err)
	}

	fa := &fakeActions{result: Result{Text: "sent round 5 to atlas", Refresh: true}}
	m := actionModel(t, fa, view.BindingStatus{Name: "atlas", Round: 4, Display: "ACTIVE", CWD: dir})

	res, cmd := m.Update(key('s'))
	m = drain(t, res.(Model), cmd)
	pb, ok := m.overlay.(sendPicker)
	if !ok {
		t.Fatalf("s must open the send picker, got %T", m.overlay)
	}

	// A file that is not there keeps the picker open with the error.
	pb.input.SetValue(filepath.Join(dir, "nope.md"))
	m.overlay = pb
	res, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = res.(Model)
	kept, ok := m.overlay.(sendPicker)
	if !ok {
		t.Fatalf("a missing file must keep the picker open, got %T", m.overlay)
	}
	if kept.err == "" {
		t.Error("a missing file must show the error")
	}
	if len(fa.sends) != 0 {
		t.Fatalf("a missing file must send nothing, sends = %v", fa.sends)
	}

	// A real file advances to the send confirm, which names the round.
	kept.input.SetValue(plan)
	m.overlay = kept
	res, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = drain(t, res.(Model), cmd)
	box, ok := m.overlay.(confirmBox)
	if !ok {
		t.Fatalf("a real file must open the send confirm, got %T", m.overlay)
	}
	if box.title != "Send "+plan+" to atlas as round 4?" {
		t.Errorf("send confirm title = %q", box.title)
	}

	res, cmd = m.Update(key('y'))
	m = drain(t, res.(Model), cmd)
	if len(fa.sends) != 1 || fa.sends[0].key != "atlas" || fa.sends[0].file != plan {
		t.Fatalf("sends = %+v, want one send of %q", fa.sends, plan)
	}
}

func TestEditorSendSkipsEmpty(t *testing.T) {
	header := []byte(editorPlanHeader("atlas"))
	if planSent(header, header) {
		t.Error("an unchanged draft must not be sent")
	}
	if planSent(header, []byte("  \n")) {
		t.Error("an emptied draft must not be sent")
	}
	if !planSent(header, append(header, []byte("do the thing\n")...)) {
		t.Error("a changed, non-empty draft must be sent")
	}
}

func TestBindPromptChain(t *testing.T) {
	fa := &fakeActions{candidates: []string{"deepseek", "glm", "haiku"}}
	m := actionModel(t, fa, view.BindingStatus{Name: "atlas", Round: 4, Display: "ACTIVE"})

	res, cmd := m.Update(key('b'))
	m = drain(t, res.(Model), cmd)
	name, ok := m.overlay.(promptBox)
	if !ok {
		t.Fatalf("b must open the name prompt, got %T", m.overlay)
	}

	// An invalid name keeps the prompt open with the validator's message.
	name.input.SetValue("Atlas")
	m.overlay = name
	res, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = res.(Model)
	kept, ok := m.overlay.(promptBox)
	if !ok {
		t.Fatalf("an invalid name must keep the prompt open, got %T", m.overlay)
	}
	if kept.err == "" {
		t.Error("an invalid name must show the validator's message")
	}

	// A valid name advances to the candidate prompt, whose choices are the
	// builder role's candidates.
	kept.input.SetValue("inbox")
	m.overlay = kept
	res, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = drain(t, res.(Model), cmd)
	cand, ok := m.overlay.(promptBox)
	if !ok {
		t.Fatalf("a valid name must open the candidate prompt, got %T", m.overlay)
	}
	if len(fa.candRole) != 1 || fa.candRole[0] != "builder" {
		t.Fatalf("Candidates roles = %v, want one builder", fa.candRole)
	}

	// tab cycles the candidate list. The cycle starts at the second entry --
	// promptBox's sel counts the entry already shown, and none is shown yet.
	res, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = res.(Model)
	cand = m.overlay.(promptBox)
	if cand.input.Value() != "glm" {
		t.Errorf("first tab = %q, want the next candidate", cand.input.Value())
	}
	res, _ = m.Update(tea.KeyMsg{Type: tea.KeyTab})
	m = res.(Model)
	cand = m.overlay.(promptBox)
	if cand.input.Value() != "haiku" {
		t.Errorf("second tab = %q, want the next candidate", cand.input.Value())
	}

	res, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = drain(t, res.(Model), cmd)
	feat, ok := m.overlay.(promptBox)
	if !ok {
		t.Fatalf("the candidate must advance to the feature prompt, got %T", m.overlay)
	}
	feat.input.SetValue("auth")
	m.overlay = feat
	res, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = drain(t, res.(Model), cmd)
	box, ok := m.overlay.(confirmBox)
	if !ok {
		t.Fatalf("the feature must advance to the bind confirm, got %T", m.overlay)
	}
	if !strings.Contains(box.title, "as builder on haiku?") {
		t.Errorf("bind confirm title = %q", box.title)
	}

	res, cmd = m.Update(key('y'))
	m = drain(t, res.(Model), cmd)
	if len(fa.binds) != 1 {
		t.Fatalf("binds = %+v, want one", fa.binds)
	}
	if got := fa.binds[0]; got.Name != "inbox" || got.Candidate != "haiku" || got.Feature != "auth" {
		t.Errorf("BindInput = %+v, want inbox/haiku/auth", got)
	}
}

func TestRetryConfirmText(t *testing.T) {
	open := view.BindingStatus{Name: "atlas", Round: 4, Display: "ACTIVE"}
	idle := view.BindingStatus{Name: "atlas", Round: 4, Display: "PAUSED"}

	if got := retryConfirmTitle(open, "haiku"); got != "Stop atlas round 4 and resend its plan on haiku?" {
		t.Errorf("open-round retry title = %q", got)
	}
	if got := retryConfirmTitle(idle, "haiku"); got != "Resend atlas's last plan as a new round on haiku?" {
		t.Errorf("no-open-round retry title = %q", got)
	}
	if got := strings.Join(retryConfirmLines(open, "haiku"), "\n"); !strings.Contains(got, "the binding keeps haiku for later rounds") {
		t.Errorf("retry confirm must say the builder change persists:\n%s", got)
	}
	if got := strings.Join(retryConfirmLines(open, "haiku"), "\n"); strings.Contains(got, "MasterMind ") {
		t.Errorf("no mastermind line for a mastermind-less row:\n%s", got)
	}
}

// TestRetryKeyCarriesTheChosenCandidate: the list includes the current
// candidate, disabled. Ported for O5 (the tab-cycled retry prompt became the
// candidate list): enter on an enabled row opens the confirm and Retry.
func TestRetryKeyCarriesTheChosenCandidate(t *testing.T) {
	fa := &fakeActions{candidates: []string{"glm", "haiku"}}
	m := actionModel(t, fa, view.BindingStatus{
		Name: "atlas", Round: 4, Display: "ACTIVE",
		BuilderCandidate: "opencode/cline-pass/glm", BuilderName: "glm",
	})

	res, cmd := m.Update(key('r'))
	m = drain(t, res.(Model), cmd)
	lb, ok := m.overlay.(listBox)
	if !ok {
		t.Fatalf("r must open the retry list, got %T", m.overlay)
	}
	if len(lb.items) != 2 {
		t.Fatalf("items = %+v, want the role's candidates including the current one", lb.items)
	}
	if !lb.items[0].disabled || lb.sel != 1 {
		t.Fatalf("the current candidate must be disabled and sel must start on the ready one: %+v sel=%d", lb.items, lb.sel)
	}

	res, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = drain(t, res.(Model), cmd)
	if _, ok := m.overlay.(confirmBox); !ok {
		t.Fatalf("a candidate must open the retry confirm, got %T", m.overlay)
	}

	res, cmd = m.Update(key('y'))
	m = drain(t, res.(Model), cmd)
	if len(fa.retries) != 1 {
		t.Fatalf("retries = %+v, want one", fa.retries)
	}
	if got := fa.retries[0]; got.key != "atlas" || got.candidate != "haiku" {
		t.Errorf("retry call = %+v, want atlas on haiku", got)
	}
}

// TestPlannedRound pins the two suffixes a round's plan may carry: the current
// NNN-prompt.md and the legacy NNN-plan.md. Everything else is not a plan.
func TestPlannedRound(t *testing.T) {
	cases := []struct {
		base string
		want int
		ok   bool
	}{
		{"004-prompt.md", 4, true},
		{"004-plan.md", 4, true},
		{"1-prompt.md", 1, true},
		{"004-report.md", 0, false},
		{"004-done", 0, false},
		{"bind.json", 0, false},
		{"004-plan.txt", 0, false},
		{"000-prompt.md", 0, false},
		{"no-round-prompt.md", 0, false},
		{"004-planner/plan.md", 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.base, func(t *testing.T) {
			got, ok := plannedRound(tc.base)
			if got != tc.want || ok != tc.ok {
				t.Errorf("plannedRound(%q) = %d, %v; want %d, %v", tc.base, got, ok, tc.want, tc.ok)
			}
		})
	}
}

// TestLastPlannedRound: the retry scan picks the highest round with a plan on
// disk, counting the prompt spelling and the legacy one alike.
func TestLastPlannedRound(t *testing.T) {
	cases := []struct {
		name  string
		files []string
		want  int
	}{
		{
			name:  "prompt beats an older plan",
			files: []string{"001-plan.md", "003-prompt.md", "003-done", "002-report.md", "002-planner/plan.md"},
			want:  3,
		},
		{
			name:  "legacy spelling still answers",
			files: []string{"001-prompt.md", "002-plan.md"},
			want:  2,
		},
		{
			name:  "nothing plan-shaped",
			files: []string{"001-report.md", "bind.json"},
			want:  0,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := store.New(t.TempDir())
			for _, f := range tc.files {
				path := filepath.Join(st.Dir("atlas"), filepath.FromSlash(f))
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
				}
				if err := os.WriteFile(path, []byte("x\n"), 0o644); err != nil {
					t.Fatalf("write %s: %v", path, err)
				}
			}
			if got := lastPlannedRound(relevo.Runtime{Store: st}, "atlas"); got != tc.want {
				t.Errorf("lastPlannedRound = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestReportReadyRowAndPull(t *testing.T) {
	fa := &fakeActions{pullText: "round 3 report\n\nall good\n", pullOK: true}
	b := view.BindingStatus{
		Name: "atlas", Round: 3, Display: "ACTIVE", MasterMindName: "you",
		Last:    &view.LastEvent{TS: railNow.Add(-3 * time.Minute), Round: 3, Kind: store.KindReport},
		Pending: &view.PendingInfo{Round: 3, Kind: store.KindReport},
	}
	// The post-pull refetch must still answer with the row: a store with no
	// bindings would read as "atlas is gone" and pop the round view.
	rep := view.Report{Bindings: []view.BindingStatus{b}}
	st := store.New(t.TempDir())
	m := newModel(context.Background(), fixedSource{rt: relevo.Runtime{Store: st}, rep: rep},
		Options{Interval: time.Second, Actions: fa})
	m.now = func() time.Time { return railNow }
	res, _ := m.Update(tea.WindowSizeMsg{Width: 140, Height: 40})
	m = res.(Model)
	m.statusInFlight = false
	res, _ = m.Update(statusMsg{report: rep})
	m = res.(Model)
	m = pointer(t, m, "atlas")

	if !reportReady(m.report.Bindings[0]) {
		t.Fatal("a you-mastermind row with a pending payload is report ready")
	}
	if got := nowCell(m.report.Bindings[0], railNow); got != "report ready · 3m" {
		t.Errorf("NOW cell = %q, want report ready · 3m", got)
	}
	if got := stripANSI(m.headerView(m.env())); !strings.Contains(got, "1 needs you") {
		t.Errorf("the header must count a ready report: %q", got)
	}

	res, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m = drain(t, res.(Model), cmd)
	if len(fa.pulls) != 1 || fa.pulls[0] != "atlas" {
		t.Fatalf("pulls = %v, want exactly one atlas", fa.pulls)
	}
	rv, ok := m.top().(roundView)
	if !ok {
		t.Fatalf("enter must open the round view, got %T", m.top())
	}
	if rv.pane.detail.active != tabReport {
		t.Errorf("the pulled report must be shown in the report tab")
	}
	if got := rv.pane.detail.cache[tabReport].body; !strings.Contains(got, "all good") {
		t.Errorf("report tab body = %q, want the pulled text", got)
	}
}

// TestReportReadyReaderRowSaysArtifact pins the fleet NOW cell for a reader
// round waiting on this cockpit: the row's word follows its shape, so a reader
// reads "artifact ready" while a writer keeps "report ready".
func TestReportReadyReaderRowSaysArtifact(t *testing.T) {
	reader := view.BindingStatus{
		Name: "atlas", Round: 3, Display: "ACTIVE", MasterMindName: "you",
		Shape:   store.ShapeReader,
		Last:    &view.LastEvent{TS: railNow.Add(-3 * time.Minute), Round: 3, Kind: store.KindReport},
		Pending: &view.PendingInfo{Round: 3, Kind: store.KindReport},
	}
	if !reportReady(reader) {
		t.Fatal("a reader row with a pending payload is report ready")
	}
	if got := nowCell(reader, railNow); got != "artifact ready · 3m" {
		t.Errorf("reader NOW cell = %q, want artifact ready · 3m", got)
	}

	writer := reader
	writer.Shape = ""
	if got := nowCell(writer, railNow); got != "report ready · 3m" {
		t.Errorf("writer NOW cell = %q, want report ready · 3m", got)
	}
}

// fixedSource answers every status with the same report and resolves every key
// to one runtime: a test's refetch after an action must not empty the fleet the
// row came from.
type fixedSource struct {
	rt  relevo.Runtime
	rep view.Report
}

func (s fixedSource) Status(context.Context) (view.Report, error) { return s.rep, nil }

func (s fixedSource) Runtime(k string) (relevo.Runtime, string, bool) { return s.rt, k, true }

func (s fixedSource) Base() relevo.Runtime { return s.rt }

func (s fixedSource) MarkViewed(string) {}

func TestFooterDropsWholeKeys(t *testing.T) {
	allowed := map[string]bool{": command": true, "? all keys": true, "q quit": true, "esc back": true}
	for _, width := range []int{80, 100, 140} {
		m := actionModel(t, &fakeActions{}, view.BindingStatus{Name: "atlas", Round: 4, Display: "ACTIVE"})
		res, _ := m.Update(tea.WindowSizeMsg{Width: width, Height: 40})
		m = res.(Model)
		for _, kh := range m.top().Keys() {
			allowed[kh.Key+" "+kh.Help] = true
		}

		row := strings.TrimRight(stripANSI(m.keysView(m.env())), " ")
		if !strings.Contains(row, "?  all keys") && !strings.Contains(row, "? all keys") {
			t.Errorf("width %d: ? all keys must always be in the keys row: %q", width, row)
		}
		if !strings.Contains(row, "q  quit") && !strings.Contains(row, "q quit") {
			t.Errorf("width %d: q quit must always be in the keys row: %q", width, row)
		}
		for _, part := range strings.Split(row, "     ") {
			cleaned := strings.Join(strings.Fields(part), " ")
			if cleaned == "" {
				continue
			}
			if !allowed[cleaned] {
				t.Errorf("width %d: %q is not a whole key (cleaned %q)", width, part, cleaned)
			}
		}
	}
}
