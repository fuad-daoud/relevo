package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/ui/widget"
	"github.com/fuad-daoud/relevo/internal/view"
)

type tab int

const (
	tabPrompt tab = iota
	tabReport
	tabTerminal
	tabDiff
	tabLog
	tabArtifacts
	tabCount
)

// tabTitles is indexed by tab; the tab bar and the tests both read it.
var tabTitles = [tabCount]string{"prompt", "report", "transcript", "diff", "log", "artifacts"}

// writerTabs is the tab bar a writer round draws.
var writerTabs = []tab{tabPrompt, tabReport, tabTerminal, tabDiff, tabLog}

// readerTabs is the tab bar a reader round draws: no report and
// no diff, and the artifacts tab instead.
var readerTabs = []tab{tabPrompt, tabArtifacts, tabLog, tabTerminal}

// tabContent is one tab's rendered body plus why it might be empty.
//
// err and empty are distinct and must never be collapsed into one field. err
// means the read failed. empty means the read succeeded and there is
// legitimately nothing to show -- a state that must render as prose, never as
// an error.
type tabContent struct {
	body   string
	loaded bool  // false until the first fetch returns
	err    error // fetch failure, scoped to this tab alone
	empty  string
	round  int       // the round the body belongs to (report, diff); 0 when not round-keyed
	at     time.Time // the event time for prompt and report (zero when unknown), the read time for the others

	// transcript is true when the body is a rendered round log (headless
	// stream or pane session record): colour markers, show the log
	// source line instead of the capture one.
	transcript bool
	// logName is the base name of that log ("003-builder.log"); "" for a
	// capture.
	logName string

	// The artifacts tab: the files RoundArtifacts listed for
	// the round, in its order; artifactRel the selected file, artifactBody
	// its raw bytes, artifactActor the binding's actor and artifactOutput
	// the actor's output rel (whose file, or summary.md, is the final
	// message), and artifactErr a failed read of the selected file.
	artifacts      []view.ArtifactFile
	artifactRel    string
	artifactBody   string
	artifactActor  string
	artifactOutput string
	artifactErr    error
	// artifactDir is the directory artifactRel lives in, or would live in
	// for a sealed round. The open action and the hint line join it.
	artifactDir string
}

// headlessLogLines caps how much of a round log the terminal tab holds:
// the whole log for any round a human would read, a bounded body for a
// runaway one. A builder's log is a file, not a screen.
const headlessLogLines = 5000

// watchdogMsg is the first fetch's time-box: it fires one poll interval after
// Init and says only that the first status has not landed yet. It carries no
// report, so a slow widget.Source.Status can never merge a partial one -- the fleet
// keeps painting the bounded loading state until the whole status arrives.
type watchdogMsg time.Time

// statusMsg carries one fleet refresh...
type statusMsg struct {
	report view.Report
	err    error
}

// detailRowMsg carries one binding's own full-detail row: the three figures a
// fleet row deliberately leaves nil (Live, LiveUsage, Headless.Tail), fetched
// for the row the round view has open. row is nil whenever err is, so a failed
// fetch leaves the fleet's own nil figures rather than a half-populated row.
type detailRowMsg struct {
	key string
	row *view.BindingStatus
	err error
}

// tabMsg carries a row key (BindingStatus.Key()) and round so a late reply
// that no longer matches the current selection is discarded rather than
// shown.
type tabMsg struct {
	name    string
	round   int
	t       tab
	content tabContent
}

// unresolvedKey is the error a fetcher reports when the source cannot
// resolve a row key: the same prose a gone binding's own store read
// produces, so an unknown owner's tab reads exactly like a vanished one.
func unresolvedKey(key string) error {
	return fmt.Errorf("%s: %w", key, store.ErrNotFound)
}

// fetchStatus calls src.Status(ctx) and returns statusMsg{report, err}. It
// never returns a partial report alongside a report-level error.
func fetchStatus(ctx context.Context, src widget.Source) tea.Cmd {
	return func() tea.Msg {
		rep, err := src.Status(ctx)
		if err != nil {
			return statusMsg{err: err}
		}
		return statusMsg{report: rep}
	}
}

// fetchStatusRow is fetchStatus for one row: it resolves key through
// src.Runtime and builds that single binding's row with its detail figures on,
// which is exactly what the fleet refresh leaves out. A key the source cannot
// resolve -- or resolves to a runtime carrying no store, as a server source
// does for an owner it does not hold -- is the same prose an unknown owner's
// tab reads, never a blank row and never a panic.
func fetchStatusRow(ctx context.Context, src widget.Source, key string) tea.Cmd {
	return func() tea.Msg {
		rt, name, ok := src.Runtime(key)
		if !ok || rt.Store == nil {
			return detailRowMsg{key: key, err: unresolvedKey(key)}
		}
		row, err := relevo.StatusRow(ctx, rt, name)
		if err != nil {
			return detailRowMsg{key: key, err: err}
		}
		return detailRowMsg{key: key, row: &row}
	}
}

// sectionForTab maps a ui tab to the relevo.ShowSection fetchShow reads for
// it -- terminal -> transcript, everything else its own name.
func sectionForTab(t tab) relevo.ShowSection {
	switch t {
	case tabPrompt:
		return relevo.ShowPrompt
	case tabReport:
		return relevo.ShowReport
	case tabTerminal:
		return relevo.ShowTranscript
	case tabDiff:
		return relevo.ShowDiff
	case tabLog:
		return relevo.ShowLog
	case tabArtifacts:
		return relevo.ShowArtifacts
	default:
		return relevo.ShowPrompt
	}
}

// tabForSection is sectionForTab's inverse, so fetchShow's tabMsg carries
// the ui tab a reply routes to rather than the relevo.ShowSection it read.
func tabForSection(s relevo.ShowSection) tab {
	switch s {
	case relevo.ShowPrompt:
		return tabPrompt
	case relevo.ShowReport:
		return tabReport
	case relevo.ShowTranscript:
		return tabTerminal
	case relevo.ShowDiff:
		return tabDiff
	case relevo.ShowLog:
		return tabLog
	case relevo.ShowArtifacts:
		return tabArtifacts
	default:
		return tabPrompt
	}
}

// fetchShow wraps relevo.Show for a non-live (hist) binding's detail tabs:
// every tab of an archived or otherwise not-live binding reads the
// database through it. Missing renders as tabContent.empty prose ("no
// <section> for round N"), never as an error; a Show error renders as
// tabContent.err exactly like a failed file read.
//
// Peek and ArchivedOnly keep this read from claiming anything: the cockpit
// is the human's view, so a read here never claims a pending payload (the
// no-claim rule for live tabs does not cover this path), and a history row
// reads the archive rather than a live binding of the same name -- which a
// rebind can leave behind, shadowing the archived rounds the row is showing.
func fetchShow(ctx context.Context, rt relevo.Runtime, name string, round int, section relevo.ShowSection) tea.Cmd {
	t := tabForSection(section)
	return func() tea.Msg {
		res, err := relevo.Show(ctx, rt, relevo.ShowOptions{
			Name:         name,
			Round:        round,
			Section:      section,
			Peek:         true,
			ArchivedOnly: true,
		})
		if err != nil {
			content := tabContent{loaded: true, round: round, err: err}
			if t != tabPrompt && t != tabReport {
				content.at = time.Now()
			}
			return tabMsg{name: name, round: round, t: t, content: content}
		}

		// Show carries no event time for a prompt or report section, so those
		// source lines stay timeless rather than claiming the read time.
		content := tabContent{loaded: true, round: round}
		if t != tabPrompt && t != tabReport {
			content.at = time.Now()
		}
		switch {
		case section == relevo.ShowLog:
			if len(res.Events) == 0 {
				content.empty = fmt.Sprintf("no %s for round %d", section, round)
				break
			}
			var b strings.Builder
			for _, e := range res.Events {
				b.WriteString(view.LogLine(e))
				b.WriteByte('\n')
			}
			content.body = b.String()
		case res.Missing:
			content.empty = fmt.Sprintf("no %s for round %d", section, round)
		case section == relevo.ShowTranscript:
			content.body = res.Text
			content.transcript = true
		default:
			content.body = res.Text
		}

		return tabMsg{name: name, round: round, t: t, content: content}
	}
}

// fetchFor dispatches to the right command for a tab, so switchTab and
// visibleTabFetch share one mapping instead of two switches that can drift.
// live is false for a hist row's detail: every tab goes
// through fetchShow instead of live's own fetchers. A hist row's key is
// its bare name (hist rows are mastermind-only; the server never has them),
// read through src.Base(). sel is the artifacts tab's cursor, ignored by
// every other tab.
func fetchFor(ctx context.Context, src widget.Source, t tab, key string, round, lines, sel int, live bool) tea.Cmd {
	if !live {
		return fetchShow(ctx, src.Base(), key, round, sectionForTab(t))
	}
	switch t {
	case tabPrompt:
		return fetchPrompt(ctx, src, key, round)
	case tabReport:
		return fetchReport(ctx, src, key, round)
	case tabTerminal:
		return fetchTerminal(ctx, src, key, round, lines)
	case tabDiff:
		return fetchDiff(ctx, src, key, round)
	case tabLog:
		return fetchLog(ctx, src, key, round)
	case tabArtifacts:
		return fetchArtifacts(ctx, src, key, round, sel)
	default:
		return nil
	}
}
