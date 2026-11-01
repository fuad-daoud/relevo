package bugreport

import (
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/hooks"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/usage"
	"github.com/fuad-daoud/relevo/internal/view"
)

// The caps every projection applies: a bundle carries tails, never records.
const (
	roundEntries = 5
	hookRuns     = 20
	ledgerRows   = 20
	noteRunes    = 200
)

// The --logs caps: the bundle's budget for one round's body and the
// transcript tail.
const (
	LogReportBytes = 16 << 10
	LogDiffBytes   = 64 << 10
	LogTailLines   = 200
)

// DescriptionSection is the --body file as the bundle's first section: the
// section is here even when the file held no text.
func DescriptionSection(lines []string) Section {
	return Section{Name: SectionDescription, Lines: lines}
}

// EnvFacts is what the environment section projects: the build's own facts.
type EnvFacts struct {
	Version      string
	Distribution string
	GoVersion    string
	GOOS         string
	GOARCH       string
	StateRoot    string
}

func EnvironmentSection(f EnvFacts) Section {
	return Section{
		Name:    SectionEnvironment,
		Columns: []string{"fact", "value"},
		Rows: [][]string{
			{"relevo version", f.Version},
			{"relevo distribution", f.Distribution},
			{"go version", f.GoVersion},
			{"goos", f.GOOS},
			{"goarch", f.GOARCH},
			{"state root", f.StateRoot},
		},
	}
}

func LastErrorSection(e LastError, ok bool) Section {
	if !ok {
		return Section{Name: SectionLastError, Lines: []string{"no failure recorded"}}
	}
	return Section{
		Name:    SectionLastError,
		Columns: []string{"fact", "value"},
		Rows: [][]string{
			{"time", stamp(e.Time)},
			{"verb", e.Verb},
			{"argv", strings.Join(e.Argv, " ")},
			{"code", e.Code},
			{"message", e.Message},
			{"next", e.Next},
		},
	}
}

// DoctorDoc mirrors the document `relevo doctor --json` prints.
type DoctorDoc struct {
	UsableBuilder  bool          `json:"usable_builder"`
	NoCandidates   bool          `json:"no_candidates"`
	BuilderRefusal string        `json:"builder_refusal,omitempty"`
	Failures       int           `json:"failures"`
	Warnings       int           `json:"warnings"`
	Checks         []DoctorCheck `json:"checks"`
}

// DoctorCheck is one doctor row.
type DoctorCheck struct {
	Group       string
	Name        string
	Severity    string
	Detail      string
	Fix         string
	ProbeFailed bool
}

func DoctorSection(d DoctorDoc) Section {
	sec := Section{
		Name: SectionDoctor,
		Lines: []string{
			"usable builder: " + strconv.FormatBool(d.UsableBuilder),
			"no candidates: " + strconv.FormatBool(d.NoCandidates),
			"builder refusal: " + d.BuilderRefusal,
			"failures: " + strconv.Itoa(d.Failures),
			"warnings: " + strconv.Itoa(d.Warnings),
		},
		Columns: []string{"group", "name", "severity", "detail", "fix", "probe_failed"},
	}
	for _, c := range d.Checks {
		sec.Rows = append(sec.Rows, []string{
			c.Group, c.Name, c.Severity, c.Detail, c.Fix, strconv.FormatBool(c.ProbeFailed),
		})
	}
	return sec
}

// StatusSection projects the status rows the bundle carries; the free text a
// row also holds is never taken.
func StatusSection(rep view.Report) Section {
	sec := Section{
		Name:    SectionStatus,
		Columns: []string{"binding", "actor", "candidate", "state", "round", "rounds", "shape", "where", "pending"},
	}
	for _, b := range rep.Bindings {
		sec.Rows = append(sec.Rows, []string{
			b.Name, b.Role, b.BuilderCandidate, b.State,
			strconv.Itoa(b.PlanRound), strconv.Itoa(b.Round),
			b.Shape, where(b.Server), pendingKind(b.Pending),
		})
	}
	return sec
}

// where is the one word the bundle carries about a binding's builder: a
// server that is named means remote.
func where(server string) string {
	if server != "" {
		return "remote"
	}
	return "local"
}

func pendingKind(p *view.PendingInfo) string {
	if p == nil {
		return ""
	}
	return string(p.Kind)
}

type BindingLog struct {
	Name    string
	Entries []store.LogEntry
}

// RoundsSection projects the newest entries of the selected bindings' logs.
func RoundsSection(logs []BindingLog, name string, round int) Section {
	sec := Section{
		Name: SectionRounds,
		Columns: []string{
			"binding", "seq", "ts", "round", "direction", "kind", "route", "confirmed",
			"late", "tier", "outcome", "halted_at", "note",
			"tokens_in", "tokens_cache_read", "tokens_cache_write", "tokens_out",
		},
	}
	for _, bl := range logs {
		if name != "" && bl.Name != name {
			continue
		}
		for _, e := range tailRound(bl.Entries, round, roundEntries) {
			sec.Rows = append(sec.Rows, roundRow(bl.Name, e))
		}
	}
	return sec
}

// tailRound keeps the newest n entries of one round.
func tailRound(entries []store.LogEntry, round, n int) []store.LogEntry {
	out := make([]store.LogEntry, 0, len(entries))
	for _, e := range entries {
		if round > 0 && e.Round != round {
			continue
		}
		out = append(out, e)
	}
	if len(out) > n {
		out = out[len(out)-n:]
	}
	return out
}

// roundRow is one log entry as the bundle keeps it; the note is kept, cut to
// noteRunes, because it carries the reason a report was rejected.
func roundRow(name string, e store.LogEntry) []string {
	var tokens usage.Tokens
	if e.Usage != nil {
		tokens = e.Usage.Tokens
	}
	return []string{
		name, strconv.Itoa(e.Seq), stamp(e.TS), strconv.Itoa(e.Round),
		string(e.Direction), string(e.Kind), e.Route, strconv.FormatBool(e.Confirmed),
		strconv.FormatBool(e.Late), e.Tier, e.Outcome, e.HaltedAt,
		truncateRunes(e.Note, noteRunes),
		strconv.FormatInt(tokens.In, 10), strconv.FormatInt(tokens.CacheRead, 10),
		strconv.FormatInt(tokens.CacheWrite, 10), strconv.FormatInt(tokens.Out, 10),
	}
}

func HooksSection(runs []hooks.HookRun) Section {
	sec := Section{
		Name:    SectionHooks,
		Columns: []string{"at", "event", "hook", "exit_code", "error"},
	}
	for _, r := range tailRuns(runs, hookRuns) {
		sec.Rows = append(sec.Rows, []string{
			stamp(r.At), r.Event, hookName(r.Argv), strconv.Itoa(r.ExitCode),
			truncateRunes(r.Error, noteRunes),
		})
	}
	return sec
}

func tailRuns(runs []hooks.HookRun, n int) []hooks.HookRun {
	if len(runs) > n {
		return runs[len(runs)-n:]
	}
	return runs
}

// hookName is the basename of the command a hook ran, never its arguments.
func hookName(argv []string) string {
	if len(argv) == 0 {
		return ""
	}
	return filepath.Base(argv[0])
}

func GatesSection(gates []availability.Gate, ledger availability.Ledger) Section {
	sec := Section{
		Name:    SectionGates,
		Columns: []string{"kind", "subject", "at", "until", "source", "binding", "note"},
	}
	for _, g := range gates {
		sec.Rows = append(sec.Rows, []string{
			string(g.Kind), g.Token, stamp(g.Since), stamp(g.Until), g.Source, g.Binding,
			truncateRunes(g.Note, noteRunes),
		})
	}
	entries := ledger.Entries
	if len(entries) > ledgerRows {
		entries = entries[len(entries)-ledgerRows:]
	}
	for _, e := range entries {
		sec.Rows = append(sec.Rows, []string{
			string(e.Kind), e.Subject, stamp(e.At), stamp(e.Until), e.Source, e.Binding,
			truncateRunes(e.Note, noteRunes),
		})
	}
	return sec
}

func DaemonSection(running bool, info store.DaemonInfo, found bool) Section {
	sec := Section{Name: SectionDaemon, Columns: []string{"fact", "value"}}
	add := func(k, v string) { sec.Rows = append(sec.Rows, []string{k, v}) }
	add("running", strconv.FormatBool(running))
	if found {
		add("version", info.Version)
		add("pid", strconv.Itoa(info.PID))
		add("started_at", stamp(info.StartedAt))
		add("exe", info.Exe)
	}
	return sec
}

// Truncate caps text at limit bytes and marks the cut, so a reader never takes
// a tail for the whole text. The cut lands on a rune boundary.
func Truncate(text string, limit int) string {
	if limit <= 0 || len(text) <= limit {
		return text
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(text[cut]) {
		cut--
	}
	return text[:cut] + "\n… truncated at " + strconv.Itoa(limit) + " bytes"
}

// TailLines keeps the newest n lines of text.
func TailLines(text string, n int) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if n > 0 && len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

// stamp is a time as the bundle writes it: UTC, or "" when there is no time.
func stamp(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
