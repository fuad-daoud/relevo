package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/bugreport"
	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/hooks"
	"github.com/fuad-daoud/relevo/internal/installation"
	"github.com/fuad-daoud/relevo/internal/release"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/sanitize"
	"github.com/fuad-daoud/relevo/internal/store"
)

// logBindings is how many bindings --logs reads a round from when no --name
// selects one: the most recently active three.
const logBindings = 3

// journalArgv is the one journalctl invocation a bundle makes.
var journalArgv = []string{
	"journalctl", "--user", "-u", "relevo.service", "-n", "100", "--no-pager", "-o", "short-iso",
}

// newRuntimeReadOnly builds the runtime the bundle reads through: the config
// loaded without importing, migrating or writing anything, and the machine
// database opened read-only, carrying the installation's id as its origin so
// its scoped reads match the rows this installation wrote. Any write through
// it fails at the SQLite level, which a bundle reports as an omitted line
// rather than a failed run. The loaded config comes back with it, so a caller
// needs no second read.
func newRuntimeReadOnly() (relevo.Runtime, config.Loaded, error) {
	root, err := store.DefaultRoot()
	if err != nil {
		return relevo.Runtime{}, config.Loaded{}, err
	}
	configDir, err := userConfigRoot()
	if err != nil {
		return relevo.Runtime{}, config.Loaded{}, err
	}
	L, err := loadConfigReadOnly(root, filepath.Join(configDir, "relevo"))
	if err != nil {
		return relevo.Runtime{}, config.Loaded{}, err
	}

	// Read the installation without minting one: a read verb must not write.
	// An absent file leaves the origin empty, which is the safe no-rows
	// default, rather than creating an identity to answer a read.
	var d *db.DB
	if fileExists(filepath.Join(root, "relevo.db")) {
		inst, ok, err := installation.Read(root)
		if err != nil {
			return relevo.Runtime{}, config.Loaded{}, err
		}
		origin := ""
		if ok {
			origin = inst.ID
		}
		d, err = db.OpenReadOnlyWith(filepath.Join(root, "relevo.db"), db.Options{Origin: origin})
		if err != nil {
			return relevo.Runtime{}, config.Loaded{}, err
		}
	}
	// The shared store hands the read-only handle to every record read, so an
	// origin-scoped row matches. A machine with no database gets a store that
	// borrows nothing, and the runtime is returned before a config store is
	// built over a nil handle.
	st := store.NewShared(root, "", d)
	rt, err := buildRuntime(root, L, st, false)
	if err != nil {
		return relevo.Runtime{}, config.Loaded{}, err
	}
	if d == nil {
		return rt, L, nil
	}
	rt.Store = st
	rt.DB = d
	rt.Gates = d
	rt.Latency = d
	// The config store reads the sections the doctor rows need; every read
	// goes through the read-only handle, so nothing it loads can be written
	// back.
	rt.Config = config.Open(d)
	return rt, L, nil
}

// bugreportSources wires one source per section to the read-only runtime. Each
// source builds on its own, so one that errors or panics is one omitted line
// and never fails the run.
func bugreportSources(rt relevo.Runtime, root string, L config.Loaded, le bugreport.LastError, haveLE bool, opts bugreportOptions) []bugreport.Source {
	srcs := []bugreport.Source{}
	if opts.haveBody {
		srcs = append(srcs, descriptionSource(opts.body))
	}
	srcs = append(srcs,
		environmentSource(root),
		lastErrorSource(le, haveLE),
		doctorSource(rt, L),
		statusSource(rt),
		roundsSource(rt, opts),
		hooksSource(rt),
		gatesSource(rt),
		daemonSource(rt),
		journalSource(),
	)
	if opts.logs {
		srcs = append(srcs, logsSource(rt, opts))
	}
	return srcs
}

// descriptionSource carries the --body file as the bundle's first section: the
// caller's own description, sanitized like every other body and redacted with
// the rest of the bundle. It is wired only when the flag was given, so an
// absent --body leaves today's sections and their order untouched.
func descriptionSource(text string) bugreport.Source {
	return bugreport.Source{Name: bugreport.SectionDescription, Build: func() (bugreport.Section, error) {
		clean := sanitize.Text(text)
		if strings.TrimRight(clean, "\n") == "" {
			return bugreport.DescriptionSection(nil), nil
		}
		return bugreport.DescriptionSection(bodyLines(clean)), nil
	}}
}

// environmentSource names the binary that produced the bundle and the machine
// it ran on: its version and detected distribution, the Go toolchain and
// target, and the state root the report is written under.
func environmentSource(root string) bugreport.Source {
	return bugreport.Source{Name: bugreport.SectionEnvironment, Build: func() (bugreport.Section, error) {
		return bugreport.EnvironmentSection(bugreport.EnvFacts{
			Version:      buildVersion(),
			Distribution: string(release.Detect(releaseInputs())),
			GoVersion:    runtime.Version(),
			GOOS:         runtime.GOOS,
			GOARCH:       runtime.GOARCH,
			StateRoot:    root,
		}), nil
	}}
}

// lastErrorSource carries the failure the state root recorded, when one is
// there: the same slot an `internal` failure writes.
func lastErrorSource(e bugreport.LastError, ok bool) bugreport.Source {
	return bugreport.Source{Name: bugreport.SectionLastError, Build: func() (bugreport.Section, error) {
		return bugreport.LastErrorSection(e, ok), nil
	}}
}

// doctorSource runs the doctor's own checks and projects them, so the bundle
// carries the document `relevo doctor --json` prints.
func doctorSource(rt relevo.Runtime, L config.Loaded) bugreport.Source {
	return bugreport.Source{Name: bugreport.SectionDoctor, Build: func() (bugreport.Section, error) {
		// The doctor reads the config store for its usage row. A machine with
		// no database has none this read-only pass may open, so the section
		// says so rather than failing on a nil store.
		if rt.Config == nil {
			return bugreport.Section{}, errors.New("no machine database")
		}
		rep, err := doctorReport(rt, L)
		if err != nil {
			return bugreport.Section{}, err
		}
		return bugreport.DoctorSection(doctorMirror(doctorDocOf(rep))), nil
	}}
}

// doctorMirror converts the doctor document into the bundle's own mirror type,
// so the projection needs no import of the command that runs a doctor.
func doctorMirror(d DoctorDoc) bugreport.DoctorDoc {
	out := bugreport.DoctorDoc{
		UsableBuilder:  d.UsableBuilder,
		NoCandidates:   d.NoCandidates,
		BuilderRefusal: d.BuilderRefusal,
		Failures:       d.Failures,
		Warnings:       d.Warnings,
		Checks:         make([]bugreport.DoctorCheck, 0, len(d.Checks)),
	}
	for _, c := range d.Checks {
		out.Checks = append(out.Checks, bugreport.DoctorCheck{
			Group: c.Group, Name: c.Name, Severity: c.Severity,
			Detail: c.Detail, Fix: c.Fix, ProbeFailed: c.ProbeFailed,
		})
	}
	return out
}

// statusSource projects one row per binding, allow-listed: identity, routing
// and the pending payload's kind, never a free-text field.
func statusSource(rt relevo.Runtime) bugreport.Source {
	return bugreport.Source{Name: bugreport.SectionStatus, Build: func() (bugreport.Section, error) {
		rep, err := relevo.Status(context.Background(), rt)
		if err != nil {
			return bugreport.Section{}, err
		}
		return bugreport.StatusSection(rep), nil
	}}
}

// roundsSource projects the newest entries of the bindings' logs: five per
// binding, one binding under --name, one round under --round.
func roundsSource(rt relevo.Runtime, opts bugreportOptions) bugreport.Source {
	return bugreport.Source{Name: bugreport.SectionRounds, Build: func() (bugreport.Section, error) {
		bindings, err := rt.Store.List()
		if err != nil {
			return bugreport.Section{}, err
		}
		logs := make([]bugreport.BindingLog, 0, len(bindings))
		for _, b := range bindings {
			if opts.name != "" && b.Name != opts.name {
				continue
			}
			entries, err := rt.Store.ReadLog(b.Name)
			if err != nil {
				return bugreport.Section{}, err
			}
			logs = append(logs, bugreport.BindingLog{Name: b.Name, Entries: entries})
		}
		return bugreport.RoundsSection(logs, opts.name, opts.round), nil
	}}
}

// hooksSource projects the newest hook runs, with their output left out.
func hooksSource(rt relevo.Runtime) bugreport.Source {
	return bugreport.Source{Name: bugreport.SectionHooks, Build: func() (bugreport.Section, error) {
		runs, err := hookRuns(rt)
		if err != nil {
			return bugreport.Section{}, err
		}
		return bugreport.HooksSection(runs), nil
	}}
}

// gatesSource projects the live gates and the newest ledger entries.
func gatesSource(rt relevo.Runtime) bugreport.Source {
	return bugreport.Source{Name: bugreport.SectionGates, Build: func() (bugreport.Section, error) {
		if rt.Gates == nil {
			return bugreport.Section{Name: bugreport.SectionGates}, nil
		}
		ledger, err := availability.LoadLedger(rt.Gates)
		if err != nil {
			return bugreport.Section{}, err
		}
		return bugreport.GatesSection(availability.Gates(relevo.AvailabilityDeps(rt)), ledger), nil
	}}
}

// daemonSource projects the daemon lock's answer and the daemon's own record.
func daemonSource(rt relevo.Runtime) bugreport.Source {
	return bugreport.Source{Name: bugreport.SectionDaemon, Build: func() (bugreport.Section, error) {
		running, err := rt.Store.DaemonRunning()
		if err != nil {
			return bugreport.Section{}, err
		}
		info, found, err := rt.Store.ReadDaemonInfo()
		if err != nil {
			return bugreport.Section{}, err
		}
		return bugreport.DaemonSection(running, info, found), nil
	}}
}

// journalSource reads the daemon's journal where the platform has one.
func journalSource() bugreport.Source {
	return bugreport.Source{Name: bugreport.SectionJournal, Build: func() (bugreport.Section, error) {
		run := func(argv []string) ([]byte, error) { return bugreportExec(context.Background(), argv) }
		return journalSection(runtime.GOOS, run), nil
	}}
}

// journalSection is the journal rule, pure in goos so it is tested without
// running journalctl: the last hundred lines of the user unit on Linux, and
// the one line that says the platform has no such journal anywhere else.
func journalSection(goos string, run func(argv []string) ([]byte, error)) bugreport.Section {
	sec := bugreport.Section{Name: bugreport.SectionJournal}
	if goos != "linux" {
		sec.Omitted = "not available on " + goos
		return sec
	}
	out, err := run(journalArgv)
	if err != nil {
		sec.Omitted = err.Error()
		return sec
	}
	text := strings.TrimRight(string(out), "\n")
	if text == "" {
		return sec
	}
	sec.Lines = strings.Split(text, "\n")
	return sec
}

// hookRuns reads the machine database's hook run log. No database is an error
// rather than an empty list: an unreadable machine is a line in the bundle.
func hookRuns(rt relevo.Runtime) ([]hooks.HookRun, error) {
	if rt.DB == nil {
		return nil, errors.New("no database")
	}
	return hooks.NewKVLog(db.TxKV{DB: rt.DB}).Runs()
}

// logRound is one binding and the round --logs reads for it.
type logRound struct {
	binding store.Binding
	round   int
	at      time.Time
}

// logsSource adds the bodies the default bundle leaves out: one round's report,
// diff and transcript tail per selected binding, and the newest hook runs'
// output. Everything it adds is control-char safe and truncation is marked.
func logsSource(rt relevo.Runtime, opts bugreportOptions) bugreport.Source {
	return bugreport.Source{Name: bugreport.SectionLogs, Build: func() (bugreport.Section, error) {
		rounds, err := selectedRounds(rt, opts)
		if err != nil {
			return bugreport.Section{}, err
		}
		sec := bugreport.Section{Name: bugreport.SectionLogs}
		for _, lr := range rounds {
			sec.Lines = append(sec.Lines, roundBody(rt, lr)...)
		}
		runs, err := hookRuns(rt)
		if err != nil {
			return bugreport.Section{}, err
		}
		for _, line := range hookOutputLines(runs) {
			sec.Lines = append(sec.Lines, line)
		}
		return sec, nil
	}}
}

// selectedRounds is the --logs selection: the newest round of the three most
// recently active bindings, or the one binding --name names, with --round
// naming exactly one round of it.
func selectedRounds(rt relevo.Runtime, opts bugreportOptions) ([]logRound, error) {
	bindings, err := rt.Store.List()
	if err != nil {
		return nil, err
	}
	var all []logRound
	for _, b := range bindings {
		if opts.name != "" && b.Name != opts.name {
			continue
		}
		entries, err := rt.Store.ReadLog(b.Name)
		if err != nil {
			return nil, err
		}
		lr := logRound{binding: b, round: b.Round}
		if len(entries) > 0 {
			last := entries[len(entries)-1]
			lr.at = last.TS
			if lr.round == 0 {
				lr.round = last.Round
			}
		}
		all = append(all, lr)
	}
	if opts.name == "" {
		sort.SliceStable(all, func(i, j int) bool { return all[i].at.After(all[j].at) })
		if len(all) > logBindings {
			all = all[:logBindings]
		}
	}
	for i := range all {
		if opts.round > 0 {
			all[i].round = opts.round
		}
	}
	return all, nil
}

// roundBody is one round's report, diff and transcript as the --logs section
// carries them, each capped and marked where it was cut.
func roundBody(rt relevo.Runtime, lr logRound) []string {
	out := []string{fmt.Sprintf("== %s round %d ==", lr.binding.Name, lr.round)}
	out = append(out, "-- report --")
	out = append(out, bodyLines(readSection(rt, lr.binding.Name, lr.round, relevo.ShowReport, bugreport.LogReportBytes))...)
	out = append(out, "-- diff --")
	out = append(out, bodyLines(readSection(rt, lr.binding.Name, lr.round, relevo.ShowDiff, bugreport.LogDiffBytes))...)
	out = append(out, "-- transcript (last "+strconv.Itoa(bugreport.LogTailLines)+" lines) --")
	out = append(out, bodyLines(transcriptTail(rt, lr))...)
	return out
}

// readSection reads one round section through `relevo.Show` with Peek set, so
// the read never claims a pending payload and never stamps viewed -- a bundle
// is not the binding's reader. A section the round does not have is "(no
// file)", so a round that left none is visible rather than silently absent,
// and the body is sanitized and capped.
func readSection(rt relevo.Runtime, name string, round int, section relevo.ShowSection, limit int) string {
	res, err := relevo.Show(context.Background(), rt, relevo.ShowOptions{
		Name:    name,
		Round:   round,
		Section: section,
		Peek:    true,
	})
	if err != nil || res.Missing {
		return "(no file)"
	}
	body := bugreport.Truncate(sanitize.Text(res.Text), limit)
	if strings.TrimRight(body, "\n") == "" {
		return "(empty)"
	}
	return body
}

// transcriptTail reads the round's transcript through the round's own reader
// and keeps its tail. A round whose process wrote none is one line, not an
// empty block.
func transcriptTail(rt relevo.Runtime, lr logRound) string {
	read := func(path string) ([]byte, bool, error) {
		data, err := rt.Store.ReadFile(path)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return nil, false, nil
			}
			return nil, false, err
		}
		return data, true, nil
	}
	text, _, found, err := relevo.RoundTranscript(rt.Store, lr.binding.Name, lr.round, lr.binding.Builder, read)
	if err != nil || !found {
		return "(no transcript)"
	}
	return bugreport.Truncate(sanitize.Text(bugreport.TailLines(string(text), bugreport.LogTailLines)), bugreport.LogDiffBytes)
}

// hookOutputLines is the hook output --logs adds: the newest runs' combined
// output, one block per run that has any.
func hookOutputLines(runs []hooks.HookRun) []string {
	if len(runs) > 20 {
		runs = runs[len(runs)-20:]
	}
	var out []string
	for _, r := range runs {
		if r.Output == "" {
			continue
		}
		out = append(out, fmt.Sprintf("== hook output: %s %s ==", r.At.UTC().Format(time.RFC3339), r.Event))
		out = append(out, bodyLines(bugreport.Truncate(sanitize.Text(r.Output), bugreport.LogDiffBytes))...)
	}
	return out
}

// bodyLines splits a body into the section's own lines, so a multi-line block
// renders as it reads rather than as one row.
func bodyLines(body string) []string {
	if body == "" {
		return []string{"(none)"}
	}
	return strings.Split(strings.TrimRight(body, "\n"), "\n")
}
