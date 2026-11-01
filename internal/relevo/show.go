package relevo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/delivery"
	"github.com/fuad-daoud/relevo/internal/refusal"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
)

// ErrNoCompletedRound is Show's error when no --round is given and every
// round of the binding is still open.
var ErrNoCompletedRound = errors.New("no completed round yet; --round N to read an open round's prompt")

type ShowSection string

const (
	ShowPrompt     ShowSection = "prompt"
	ShowReport     ShowSection = "report"
	ShowDiff       ShowSection = "diff"
	ShowDrift      ShowSection = "drift"
	ShowLog        ShowSection = "log"
	ShowTranscript ShowSection = "transcript"
	ShowGate       ShowSection = "gate"
	ShowFindings   ShowSection = "findings"
	ShowOutput     ShowSection = "output"
	ShowArtifacts  ShowSection = "artifacts"
	// ShowTrace is a chain's ordered trace, not a binding's round: it is the
	// one section a chain name answers and a binding name refuses.
	ShowTrace ShowSection = "trace"
	// ShowWorkflow is the stored definition a workflow chain runs.
	ShowWorkflow ShowSection = "workflow"
)

func ValidShowSection(s ShowSection) bool {
	switch s {
	case ShowPrompt, ShowReport, ShowDiff, ShowDrift, ShowLog, ShowTranscript, ShowGate, ShowFindings, ShowOutput, ShowArtifacts, ShowTrace, ShowWorkflow:
		return true
	}
	return false
}

// ShowOptions is `relevo show`'s flags.
type ShowOptions struct {
	Name string
	// Round is the round to read; 0 means the newest completed round.
	Round   int
	Section ShowSection
	JSON    bool
	// Peek suppresses the pending-payload claim a non-peek live read makes:
	// `show --peek` is the `wait --peek` contract, a read that leaves the
	// payload for the route that pushes it. `show --owner` sets it too, so
	// the admin's read of another owner's binding stays read-only.
	Peek bool
	// ArchivedOnly skips a live binding of the same name: history rows show closed rounds.
	ArchivedOnly bool
	// FindingsID is the consult whose findings --findings names. It is
	// meaningful only with Section == ShowFindings.
	FindingsID string
	// ArtifactRel is `--artifact`'s file. With Section == ShowArtifacts it
	// names one Rel to return raw instead of the list; meaningful only there.
	ArtifactRel string
}

// ShowResult is one round's requested section, resolved from a live
// binding's files or a past binding's database rows.
type ShowResult struct {
	Name       string
	Round      int
	Rounds     int
	Live       bool
	Archived   bool
	ArchivedAt time.Time
	Section    ShowSection
	// Text is the section's content; "" when Missing.
	Text string
	// Missing is true when the round has no such section (the file or the
	// artifact row is absent) -- not an error.
	Missing bool
	// Events is filled for Section log: one entry per event, in order.
	Events []store.LogEntry
	// Artifacts is filled for Section artifacts: the round's artifact files,
	// output first, then summary.md, then by rel.
	Artifacts []view.ArtifactFile `json:"artifacts,omitempty"`
	// Trace is filled for Section trace: the chain's state and its trace, the
	// document the CLI encodes for --json. Text carries the same trace
	// rendered for a human. Nil for every other section, and for no round.
	Trace *ChainTraceDoc `json:"trace,omitempty"`
}

// Show resolves opts against a live binding's files; then, for a name that
// is not live, against the newest archived record of that name and its
// sealed round files and log; and finally against rt.DB, for a binding the
// live store never held. It returns store.ErrNotFound, wrapped, when
// opts.Name is none of the three -- including a live name whose database
// row was never archived.
func Show(ctx context.Context, rt Runtime, opts ShowOptions) (ShowResult, error) {
	if opts.Name == "" {
		return ShowResult{}, fmt.Errorf("show: a binding name is required")
	}
	if !ValidShowSection(opts.Section) {
		return ShowResult{}, fmt.Errorf("show: %q: invalid section", opts.Section)
	}

	// A trace is a chain's, not a round's: it answers before any binding is
	// looked up, so a chain whose name is also its builder's reads its trace
	// rather than the builder's newest round.
	if opts.Section == ShowTrace {
		return showTrace(ctx, rt, opts)
	}
	if opts.Section == ShowWorkflow {
		return showWorkflow(ctx, rt, opts)
	}

	b, err := rt.Store.Load(opts.Name)
	// live is whether the live store holds the name now: it is what tells the
	// database fallback apart from a name the live store never held.
	live := err == nil
	if err == nil && !opts.ArchivedOnly { // history rows skip live bindings: a rebind may reuse the name
		res, err := showLive(rt, b, opts)
		if err != nil {
			return ShowResult{}, err
		}
		// A non-peek live read claims the pending mastermind-bound payload it
		// printed, and nothing else: the claim runs only once the section
		// resolved, and only when claimPrinted accepts what was printed, so a
		// failed read consumes no payload and a read of some other section
		// confirms nothing. The pulled text is discarded -- the caller asked
		// for the section. A failed claim is the show error, because a silent
		// failure would leave the payload to be pushed after it was read.
		// --peek skips the claim, and the archived and database branches below
		// never claim: nothing is pending there.
		if !opts.Peek {
			if _, _, perr := delivery.PullMatching(ctx, rt.Store, opts.Name, "show",
				claimPrinted(rt.Store, opts.Section, res, opts.FindingsID)); perr != nil {
				return ShowResult{}, perr
			}
		}
		return res, nil
	}
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return ShowResult{}, err
	}

	// An archived binding is answered from its record. ListArchived is
	// ordered oldest-archived first, so the last match is the newest archive
	// of the name; the database stays the fallback for a binding that has
	// mirror rows but no record.
	archived, err := rt.Store.ListArchived()
	if err != nil {
		return ShowResult{}, err
	}
	var newest *store.ArchivedBinding
	for i := range archived {
		if archived[i].Binding.Name == opts.Name {
			newest = &archived[i]
		}
	}
	if newest != nil {
		return showArchived(rt, *newest, opts)
	}

	if rt.DB != nil {
		binding, found, derr := rt.DB.Binding(opts.Name)
		if derr != nil {
			return ShowResult{}, derr
		}
		if found {
			// A name the live store holds whose database row was never archived
			// has no history to read: those mirror rows are that live binding's
			// current rounds, and showDB reports them with Live false, so a
			// history read would paint the live state as sealed history. A row
			// carrying an archive timestamp is a real history row and answers.
			if live && binding.ArchivedAt == nil {
				return ShowResult{}, fmt.Errorf("binding %q is live and has no archived record (live or in the database): %w", opts.Name, store.ErrNotFound)
			}
			return showDB(rt, binding, opts)
		}
	}

	return ShowResult{}, fmt.Errorf("binding %q not found (live or in the database): %w", opts.Name, store.ErrNotFound)
}

func liveFindingsPresent(rt Runtime, name, id string) func(round int) (bool, error) {
	return func(round int) (bool, error) {
		_, missing, err := readFileOrMissing(rt.Store.ReadFile, rt.Store.FindingsPath(name, round, id))
		if err != nil {
			return false, err
		}
		return !missing, nil
	}
}

func archivedFindingsRound(rt Runtime, ab store.ArchivedBinding, rounds int, opts ShowOptions) (int, error) {
	present := func(round int) (bool, error) {
		base := filepath.Base(rt.Store.FindingsPath(ab.Binding.Name, round, opts.FindingsID))
		_, ok, err := rt.Store.ArchivedFile(ab.RecordID, base)
		return ok, err
	}
	return view.FindingsRoundFor(opts.Round, ab.Binding.Round, rounds, ab.Binding.Name, opts.FindingsID, present)
}

func showLive(rt Runtime, b store.Binding, opts ShowOptions) (ShowResult, error) {
	entries, err := rt.Store.ReadLog(b.Name)
	if err != nil {
		return ShowResult{}, err
	}

	// Rounds is the highest round number with a prompt log entry, not
	// b.Round: b.Round is the *next* round once a round has closed
	// (finishRound does Round++), so it overcounts by one for an idle
	// binding and undercounts nothing for one mid-round -- the round in
	// flight has already logged its prompt entry.
	rounds := 0
	completed := 0
	for _, e := range entries {
		if store.IsPromptKind(e.Kind) && e.Round > rounds {
			rounds = e.Round
		}
		if e.Kind == store.KindReport && e.Round > completed {
			completed = e.Round
		}
	}

	round := opts.Round
	if opts.Section == ShowFindings {
		// A consult is recorded on the binding's current round, which may have
		// no plan entry yet, so a findings read is bounded by both counters
		// and, with no --round, scans for the round holding the findings.
		present := liveFindingsPresent(rt, b.Name, opts.FindingsID)
		round, err = view.FindingsRoundFor(opts.Round, b.Round, rounds, b.Name, opts.FindingsID, present)
		if err != nil {
			return ShowResult{}, err
		}
	} else if round == 0 {
		round, err = defaultRound(rt, b, completed, opts.Section)
		if err != nil {
			return ShowResult{}, err
		}
	} else if round < 1 || round > rounds {
		// A server-chain member installs its rounds only at close, so the
		// round in flight sits outside the prompt-entry range that bounds an
		// explicit --round. A transcript read may still name it, as the
		// default does; every other section keeps the refusal.
		open, oerr := openRoundTranscriptRound(rt, b, opts.Section, round)
		if oerr != nil {
			return ShowResult{}, oerr
		}
		if !open {
			return ShowResult{}, refusal.Rangef("round %d: binding has %d rounds", round, rounds)
		}
	}

	res := ShowResult{
		Name:    b.Name,
		Round:   round,
		Rounds:  rounds,
		Live:    true,
		Section: opts.Section,
	}

	read := func(path string) (string, bool, error) {
		return readFileOrMissing(rt.Store.ReadFile, path)
	}
	readBytes := func(path string) ([]byte, bool, error) {
		return readBytesMissing(rt.Store.ReadFile, path)
	}
	list := func() ([]string, error) { return rt.Store.RoundFiles(b.Name) }
	if err := showSections(rt, b.Name, round, b.WriterRole(), b.Shape, OutputFile(rt, b), entries, b.Builder, read, readBytes, list, opts, &res); err != nil {
		return ShowResult{}, err
	}
	return res, nil
}

// showSections resolves opts.Section into res for one round of name: the
// section switch showLive and showArchived share. actor and shape locate a
// reader's artifact directory, output is its resolved output rel, entries
// is the binding's log for ShowLog, read reports a missing file rather
// than an error, live renders the transcript with the endpoint's own
// segments and kind, and list backs the gate section's fallback.
func showSections(rt Runtime, name string, round int, actor, shape, output string, entries []store.LogEntry, live store.Endpoint, read func(path string) (text string, missing bool, err error), readBytes func(path string) ([]byte, bool, error), list func() ([]string, error), opts ShowOptions, res *ShowResult) error {
	var err error
	switch opts.Section {
	case ShowPrompt:
		res.Text, res.Missing, err = read(rt.Store.PromptPath(name, round))
	case ShowReport, ShowOutput:
		// reportPathFor: a reader's report is its artifact directory's output
		// file, read through the artifact helper so a live, sealed or archived
		// round all answer; --output is that same file whatever the shape, and
		// a writer's report is the flat NNN-report.md.
		if shape == store.ShapeReader || opts.Section == ShowOutput {
			var text []byte
			text, err = readOutput(rt, name, round, actor, output)
			if err == nil {
				res.Text, res.Missing = string(text), text == nil
				if !res.Missing {
					res.Text = view.WithFallbackHeader(entries, round, res.Text)
				}
			}
		} else {
			res.Text, res.Missing, err = read(rt.Store.ReportPath(name, round))
		}
	case ShowDiff:
		res.Text, res.Missing, err = read(rt.Store.DiffPath(name, round))
	case ShowDrift:
		res.Text, res.Missing, err = read(rt.Store.DriftPath(name, round))
	case ShowArtifacts:
		if opts.ArtifactRel != "" {
			// --artifact: one file's bytes, raw. An unlisted rel is
			// ErrNoArtifact, which the caller reports as an error.
			var data []byte
			data, err = ReadArtifact(rt, name, round, actor, opts.ArtifactRel)
			if err == nil {
				res.Text = string(data)
			}
		} else {
			res.Artifacts, err = RoundArtifacts(rt, name, round, actor, output)
			if err == nil {
				res.Missing = len(res.Artifacts) == 0
			}
		}
	case ShowTranscript:
		// The round's transcript: its NNN-builder.log when one exists, and
		// otherwise its stream rendered per segment.
		var text []byte
		var found bool
		text, _, found, err = RoundTranscript(rt.Store, name, round, live, readBytes)
		if err == nil {
			res.Text, res.Missing = string(text), !found
		}
	case ShowGate:
		// The round's gate log. Live, it is read through rt.Store.ReadFile,
		// so a sealed round's log is found in the database exactly as a
		// present one is; archived, it is the record's own sealed bytes.
		res.Text, res.Missing, err = read(rt.Store.GateLogPath(name, round))
		if err == nil && res.Missing {
			err = showChainCheckGate(rt, name, round, read, list, res)
		}
	case ShowFindings:
		res.Text, res.Missing, err = read(rt.Store.FindingsPath(name, round, opts.FindingsID))
	case ShowLog:
		for _, e := range entries {
			if e.Round == round {
				res.Events = append(res.Events, e)
			}
		}
	}
	if err != nil {
		return err
	}
	return nil
}

// showArchived resolves opts against one archived record: the record's
// events are the log and every round file comes from its sealed bytes.
// Gate and findings come from the record too, where the mirror had no
// row for them.
func showArchived(rt Runtime, ab store.ArchivedBinding, opts ShowOptions) (ShowResult, error) {
	entries, err := rt.Store.ArchivedLog(ab.RecordID)
	if err != nil {
		return ShowResult{}, err
	}

	// Rounds and completed follow showLive's rules: the highest round with
	// a prompt entry, and the highest with a report entry, falling back to
	// ab.Binding.Round - 1 when no report entry exists.
	rounds := 0
	completed := 0
	for _, e := range entries {
		if store.IsPromptKind(e.Kind) && e.Round > rounds {
			rounds = e.Round
		}
		if e.Kind == store.KindReport && e.Round > completed {
			completed = e.Round
		}
	}

	round := opts.Round
	if opts.Section == ShowFindings {
		if round, err = archivedFindingsRound(rt, ab, rounds, opts); err != nil {
			return ShowResult{}, err
		}
	} else if round == 0 {
		if completed == 0 {
			completed = ab.Binding.Round - 1
		}
		if completed < 1 {
			return ShowResult{}, ErrNoCompletedRound
		}
		round = completed
	} else if round < 1 || round > rounds {
		return ShowResult{}, refusal.Rangef("round %d: binding has %d rounds", round, rounds)
	}

	res := ShowResult{
		Name:       ab.Binding.Name,
		Round:      round,
		Rounds:     rounds,
		Live:       false,
		Archived:   true,
		ArchivedAt: ab.ArchivedAt,
		Section:    opts.Section,
	}

	// Every section path showSections asks for is the same helper the live
	// path uses; the record keys its sealed files by basename, so the file
	// name is filepath.Base of that path.
	read := func(path string) (string, bool, error) {
		data, ok, err := rt.Store.ArchivedFile(ab.RecordID, filepath.Base(path))
		if err != nil {
			return "", false, err
		}
		if !ok {
			return "", true, nil
		}
		return string(data), false, nil
	}
	readBytes := func(path string) ([]byte, bool, error) {
		return rt.Store.ArchivedFile(ab.RecordID, filepath.Base(path))
	}
	list := func() ([]string, error) { return rt.Store.ArchivedFiles(ab.RecordID) }
	if err := showSections(rt, ab.Binding.Name, round, ab.Binding.WriterRole(), ab.Binding.Shape, OutputFile(rt, ab.Binding), entries, ab.Binding.Builder, read, readBytes, list, opts, &res); err != nil {
		return ShowResult{}, err
	}
	return res, nil
}

// showDB resolves opts against binding's rows in rt.DB, the fallback for a
// binding that is neither live nor archived in the store: one with mirror
// rows but no record, adopted by name only.
func showDB(rt Runtime, binding db.BindingRow, opts ShowOptions) (ShowResult, error) {
	allRounds, err := rt.DB.Rounds(binding.ID)
	if err != nil {
		return ShowResult{}, err
	}
	total := len(allRounds)

	round := opts.Round
	if round == 0 {
		completed := 0
		for _, r := range allRounds {
			if r.Outcome != db.OutcomeOpen && r.Number > completed {
				completed = r.Number
			}
		}
		if completed < 1 {
			return ShowResult{}, ErrNoCompletedRound
		}
		round = completed
	} else if round < 1 || round > total {
		return ShowResult{}, refusal.Rangef("round %d: binding has %d rounds", round, total)
	}

	var target *db.Round
	for i := range allRounds {
		if allRounds[i].Number == round {
			target = &allRounds[i]
			break
		}
	}
	if target == nil {
		return ShowResult{}, refusal.Rangef("round %d: binding has %d rounds", round, total)
	}

	res := ShowResult{
		Name:    binding.Name,
		Round:   round,
		Rounds:  total,
		Live:    false,
		Section: opts.Section,
	}
	if binding.ArchivedAt != nil {
		res.Archived = true
		res.ArchivedAt = *binding.ArchivedAt
	}

	switch opts.Section {
	case ShowPrompt, ShowReport, ShowDiff, ShowDrift:
		var a db.Artifact
		var found bool
		a, found, err = rt.DB.Artifact(target.ID, string(opts.Section))
		if err == nil {
			if found {
				res.Text = a.Text
			} else {
				res.Missing = true
			}
		}
	case ShowLog:
		// internal/ingest links event.round_id to its round once that
		// round's row exists, so db.Events(bindingID, round) itself scopes
		// to round -- no client-side re-filtering needed.
		var events []db.Event
		events, err = rt.DB.Events(binding.ID, round)
		for _, e := range events {
			var entry store.LogEntry
			if jerr := json.Unmarshal([]byte(e.EntryJSON), &entry); jerr == nil {
				res.Events = append(res.Events, entry)
			}
		}
	case ShowTranscript:
		var recs []db.TranscriptRecord
		recs, err = rt.DB.Transcript(db.OwnerRound, target.ID, 0, 0)
		if err == nil {
			lines := make([]string, 0, len(recs))
			for _, r := range recs {
				lines = append(lines, r.Rendered)
			}
			res.Text = strings.Join(lines, "\n")
			res.Missing = len(recs) == 0
		}
	case ShowGate, ShowFindings, ShowOutput, ShowArtifacts:
		// A gate log and a consult's findings are round files, not ingest
		// artifacts, so an archived (database-only) binding has no row to
		// read them from: report Missing rather than an empty section. The
		// same holds for the artifact sections: showDB holds no artifact
		// rows.
		res.Missing = true
	}
	if err != nil {
		return ShowResult{}, err
	}
	return res, nil
}
