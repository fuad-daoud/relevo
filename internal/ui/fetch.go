package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/capture"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/view"
)

type tab int

const (
	tabPlan tab = iota
	tabReport
	tabTerminal
	tabDiff
	tabLog
	tabArtifacts
	tabCount
)

// tabTitles indexes by tab and is used by both the tab bar and the tests.
var tabTitles = [tabCount]string{"plan", "report", "transcript", "diff", "log", "artifacts"}

// writerTabs is the tab bar a writer round draws: today's tabs, unchanged.
var writerTabs = []tab{tabPlan, tabReport, tabTerminal, tabDiff, tabLog}

// readerTabs is the tab bar a reader round draws (round 5b): no report and
// no diff, and the artifacts tab instead.
var readerTabs = []tab{tabPlan, tabArtifacts, tabLog, tabTerminal}

// tabContent is one tab's rendered body plus why it might be empty.
//
// err and empty are distinct and must never be collapsed into one field. err
// means the read failed. empty means the read succeeded and there is
// legitimately nothing to show -- a state that must render as prose, never as
// an error. See §6.
type tabContent struct {
	body   string    // rendered content, ready for the viewport
	loaded bool      // false until the first fetch returns
	err    error     // fetch failure, scoped to this tab alone
	empty  string    // prose explaining expected emptiness
	round  int       // the round the body belongs to (report, diff); 0 when not round-keyed
	at     time.Time // the event time for plan and report (zero when unknown), the read time for the others

	// transcript is true when the body is a rendered round log (headless
	// stream or pane session record, #184): colour markers, show the log
	// source line instead of the capture one.
	transcript bool
	// logName is the base name of that log ("003-builder.log"); "" for a
	// capture.
	logName string

	// The artifacts tab (round 5b): the files RoundArtifacts listed for
	// the round, in its order; artifactRel the selected file, artifactBody
	// its raw bytes, artifactActor the binding's actor (whose final message
	// summary.md is) and artifactErr a failed read of the selected file.
	artifacts     []relevo.ArtifactFile
	artifactRel   string
	artifactBody  string
	artifactActor string
	artifactErr   error
}

// headlessLogLines caps how much of a round log the terminal tab holds:
// the whole log for any round a human would read, a bounded body for a
// runaway one. A builder's log is a file, not a screen.
const headlessLogLines = 5000

type tickMsg time.Time

type statusMsg struct {
	report view.Report
	err    error
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
// never returns a partial report alongside a report-level error. The scope
// argument and the relevo.Bindings call it carried are gone (X3).
func fetchStatus(ctx context.Context, src Source) tea.Cmd {
	return func() tea.Msg {
		rep, err := src.Status(ctx)
		if err != nil {
			return statusMsg{err: err}
		}
		return statusMsg{report: rep}
	}
}

// fetchPlan reads round's plan file. It is small enough not to need
// capture.ReadDiff's stored-patch indirection: the file is either there or it
// is not.
func fetchPlan(ctx context.Context, src Source, key string, round int) tea.Cmd {
	return func() tea.Msg {
		rt, name, ok := src.Runtime(key)
		if !ok {
			return tabMsg{
				name:  key,
				round: round,
				t:     tabPlan,
				content: tabContent{
					loaded: true,
					round:  round,
					err:    unresolvedKey(key),
				},
			}
		}
		if round < 1 {
			return tabMsg{
				name:  key,
				round: round,
				t:     tabPlan,
				content: tabContent{
					loaded: true,
					round:  round,
					empty:  "no completed round yet",
				},
			}
		}
		data, err := rt.Store.ReadFile(rt.Store.PlanPath(name, round))
		if err != nil {
			if os.IsNotExist(err) {
				return tabMsg{
					name:  key,
					round: round,
					t:     tabPlan,
					content: tabContent{
						loaded: true,
						round:  round,
						empty:  fmt.Sprintf("no plan for round %d", round),
					},
				}
			}
			return tabMsg{
				name:  key,
				round: round,
				t:     tabPlan,
				content: tabContent{
					loaded: true,
					round:  round,
					err:    err,
				},
			}
		}

		// The plan tab's time is when the plan was sent, from the log entry
		// that recorded it. A log read failure or a missing entry leaves the
		// time unknown; it never fails the tab.
		var at time.Time
		if entries, lerr := rt.Store.ReadLog(name); lerr == nil {
			for i := len(entries) - 1; i >= 0; i-- {
				e := entries[i]
				if e.Round == round && e.Direction == store.DirToBuilder && e.Kind == store.KindPlan {
					at = e.TS
					break
				}
			}
		}

		return tabMsg{
			name:  key,
			round: round,
			t:     tabPlan,
			content: tabContent{
				loaded: true,
				at:     at,
				round:  round,
				body:   string(data),
			},
		}
	}
}

// fetchReport reads round's own report (or question/findings) entry from
// the binding log -- round-scoped, not "the newest one logged": stepping to
// an earlier round must show that round's report, not a later one's. Found
// nothing for round: an empty log at round 1 keeps the familiar "round 1 in
// flight" prose; anything else reads as round being the open round, not yet
// closed (#183).
func fetchReport(ctx context.Context, src Source, key string, round int) tea.Cmd {
	return func() tea.Msg {
		rt, name, ok := src.Runtime(key)
		if !ok {
			return tabMsg{
				name:  key,
				round: round,
				t:     tabReport,
				content: tabContent{
					loaded: true,
					err:    unresolvedKey(key),
				},
			}
		}
		entries, err := rt.Store.ReadLog(name)
		if err != nil {
			return tabMsg{
				name:  key,
				round: round,
				t:     tabReport,
				content: tabContent{
					loaded: true,
					err:    err,
				},
			}
		}

		for i := len(entries) - 1; i >= 0; i-- {
			e := entries[i]
			if e.Round != round {
				continue
			}
			if e.Direction == store.DirToPlanner &&
				(e.Kind == store.KindReport || e.Kind == store.KindQuestion || e.Kind == store.KindFindings) {
				if e.Payload == "" {
					return tabMsg{
						name:  key,
						round: round,
						t:     tabReport,
						content: tabContent{
							loaded: true,
							at:     e.TS,
							round:  round,
							empty:  fmt.Sprintf("round %d report has no payload", round),
						},
					}
				}
				return tabMsg{
					name:  key,
					round: round,
					t:     tabReport,
					content: tabContent{
						loaded: true,
						at:     e.TS,
						round:  round,
						body:   e.Payload,
					},
				}
			}
		}

		if len(entries) == 0 && round == 1 {
			return tabMsg{
				name:  key,
				round: round,
				t:     tabReport,
				content: tabContent{
					loaded: true,
					empty:  "round 1 in flight; no report yet",
				},
			}
		}

		return tabMsg{
			name:  key,
			round: round,
			t:     tabReport,
			content: tabContent{
				loaded: true,
				round:  round,
				empty:  fmt.Sprintf("round %d is open; report arrives when it closes", round),
			},
		}
	}
}

// transcriptTab is the terminal tab for already-read transcript bytes: the
// last headlessLogLines lines, transcript true, logName the source the bytes
// came from. Every terminal read shares it.
func transcriptTab(key string, body []byte, logName string) tabMsg {
	text := strings.TrimRight(string(body), "\n")
	if all := strings.Split(text, "\n"); len(all) > headlessLogLines {
		text = strings.Join(all[len(all)-headlessLogLines:], "\n")
	}
	return tabMsg{
		name: key,
		t:    tabTerminal,
		content: tabContent{
			loaded:     true,
			at:         time.Now(),
			body:       text,
			transcript: true,
			logName:    logName,
		},
	}
}

// logTab reads a round log file for the terminal tab -- the remote-builder
// branch's read. transcriptTab does the trimming and capping. ok is false
// when the file cannot be read (missing or otherwise), so the caller decides
// what the tab says instead: the remote branch falls back to its own prose.
// key names the reply's row; name is the store path the log was read from.
func logTab(key, name string, read func(string) ([]byte, error), logPath string) (tabMsg, bool) {
	data, err := read(logPath)
	if err != nil {
		return tabMsg{}, false
	}
	return transcriptTab(key, data, filepath.Base(logPath)), true
}

// fetchTerminal resolves the binding's builder log for the terminal tab: for
// a headless builder, or a remote builder relevo has a local log for, the tail
// of its round log; otherwise a prose line saying where the builder runs.
// round is the round being viewed; only the binding's current round (b.Round)
// has a live process, so a headless builder's non-current round with no round
// log of its own reads as the empty prose "terminal is live; round N left no
// log" (#183).
func fetchTerminal(ctx context.Context, src Source, key string, round, lines int) tea.Cmd {
	if lines < 1 {
		lines = 1
	}
	return func() tea.Msg {
		rt, name, ok := src.Runtime(key)
		if !ok {
			return tabMsg{
				name:  key,
				round: round,
				t:     tabTerminal,
				content: tabContent{
					loaded: true,
					at:     time.Now(),
					err:    unresolvedKey(key),
				},
			}
		}
		b, err := rt.Store.Load(name)
		if err != nil {
			return tabMsg{
				name:  key,
				round: round,
				t:     tabTerminal,
				content: tabContent{
					loaded: true,
					at:     time.Now(),
					err:    err,
				},
			}
		}

		// A headless builder (#99) has no pane; its output is a round's
		// transcript -- its NNN-builder.log when one exists, otherwise its
		// stream rendered per segment. Shown, never parsed.
		if b.Builder.Headless() {
			readBytes := func(p string) ([]byte, bool, error) {
				data, rerr := rt.Store.ReadFile(p)
				if rerr != nil {
					if os.IsNotExist(rerr) {
						return nil, false, nil
					}
					return nil, false, rerr
				}
				return data, true, nil
			}
			if round != b.Round {
				// A past round: its own round files are the only place it
				// could be.
				text, source, found, rerr := relevo.RoundTranscript(rt.Store, name, round, b.Builder, readBytes)
				if rerr == nil && found {
					msg := transcriptTab(key, text, source)
					msg.round = round
					return msg
				}
				return tabMsg{
					name:  key,
					round: round,
					t:     tabTerminal,
					content: tabContent{
						loaded: true,
						at:     time.Now(),
						empty:  fmt.Sprintf("terminal is live; round %d left no log", round),
					},
				}
			}
			// The current round: the cursor names the round the process is
			// writing (or, between rounds, last wrote) -- between rounds
			// clearProcess blanks LogPath, but the cursor still names the
			// last round that ran (transcript spec §4.7), so fall back to
			// the viewed round rather than a blank tab.
			if b.Builder.LogPath == "" && b.Builder.StreamRound == 0 {
				return tabMsg{
					name:  key,
					round: round,
					t:     tabTerminal,
					content: tabContent{
						loaded: true,
						at:     time.Now(),
						empty:  "headless builder; no round has run yet, so there is no log",
					},
				}
			}
			// Rule 1: the endpoint's own log. A live process that names a
			// log which is not its round's stream reads that file, exactly
			// as before. In 2b LogPath becomes the stream path itself, so
			// this branch stops applying and rule 2 renders the stream.
			if b.Builder.LogPath != "" && b.Builder.LogPath != rt.Store.StreamPath(name, b.Builder.StreamRound) {
				if msg, ok := logTab(key, name, rt.Store.ReadFile, b.Builder.LogPath); ok {
					msg.round = round
					return msg
				}
				return tabMsg{
					name:  key,
					round: round,
					t:     tabTerminal,
					content: tabContent{
						loaded: true,
						at:     time.Now(),
						empty:  "log not written yet: " + b.Builder.LogPath,
					},
				}
			}
			// Rule 2: otherwise the round's transcript (§4.5).
			r := b.Builder.StreamRound
			if r == 0 {
				r = round
			}
			text, source, found, rerr := relevo.RoundTranscript(rt.Store, name, r, b.Builder, readBytes)
			if rerr == nil && found {
				msg := transcriptTab(key, text, source)
				msg.round = round
				return msg
			}
			return tabMsg{
				name:  key,
				round: round,
				t:     tabTerminal,
				content: tabContent{
					loaded: true,
					at:     time.Now(),
					empty:  "log not written yet: " + rt.Store.StreamPath(name, r),
				},
			}
		}

		// A remote builder (#100) runs on someone else's server: show the
		// round's local builder log when relevo has one, otherwise the single
		// line naming where the builder runs.
		if b.Builder.Remote() {
			if msg, ok := logTab(key, name, rt.Store.ReadFile, rt.Store.BuilderLogPath(name, round)); ok {
				msg.round = round
				return msg
			}
			return tabMsg{
				name:  key,
				round: round,
				t:     tabTerminal,
				content: tabContent{
					loaded: true,
					at:     time.Now(),
					empty:  fmt.Sprintf("remote builder on %s: relevo show --log %s", b.Builder.Server, name),
				},
			}
		}

		// A local builder is always headless since #303; anything else has no
		// relevo-readable terminal.
		return tabMsg{
			name:  key,
			round: round,
			t:     tabTerminal,
			content: tabContent{
				loaded: true,
				at:     time.Now(),
				empty:  "no relevo-readable terminal for this builder",
			},
		}
	}
}

// fetchDiff retrieves the stored patch for the specified round.
func fetchDiff(ctx context.Context, src Source, key string, round int) tea.Cmd {
	if round < 1 {
		return func() tea.Msg {
			return tabMsg{
				name:  key,
				round: round,
				t:     tabDiff,
				content: tabContent{
					loaded: true,
					at:     time.Now(),
					round:  round,
					empty:  "no completed round yet",
				},
			}
		}
	}

	return func() tea.Msg {
		rt, name, ok := src.Runtime(key)
		if !ok {
			return tabMsg{
				name:  key,
				round: round,
				t:     tabDiff,
				content: tabContent{
					loaded: true,
					at:     time.Now(),
					round:  round,
					err:    unresolvedKey(key),
				},
			}
		}
		patch, ok, err := capture.ReadDiff(rt.Store, name, round)
		if err != nil {
			return tabMsg{
				name:  key,
				round: round,
				t:     tabDiff,
				content: tabContent{
					loaded: true,
					at:     time.Now(),
					round:  round,
					err:    err,
				},
			}
		}
		if !ok {
			empty := fmt.Sprintf("no diff recorded for round %d — no baseline captured", round)
			if b, berr := rt.Store.Load(name); berr == nil && round == b.Round {
				// round is the binding's current, not-yet-closed round: no
				// diff is missing, none has been captured yet (#183).
				empty = fmt.Sprintf("diff is captured when round %d closes", round)
			}
			return tabMsg{
				name:  key,
				round: round,
				t:     tabDiff,
				content: tabContent{
					loaded: true,
					at:     time.Now(),
					round:  round,
					empty:  empty,
				},
			}
		}

		return tabMsg{
			name:  key,
			round: round,
			t:     tabDiff,
			content: tabContent{
				loaded: true,
				at:     time.Now(),
				round:  round,
				body:   string(patch),
			},
		}
	}
}

// fetchLog formats round's log entries with the cmdLog layout.
func fetchLog(ctx context.Context, src Source, key string, round int) tea.Cmd {
	return func() tea.Msg {
		rt, name, ok := src.Runtime(key)
		if !ok {
			return tabMsg{
				name:  key,
				round: round,
				t:     tabLog,
				content: tabContent{
					loaded: true,
					at:     time.Now(),
					err:    unresolvedKey(key),
				},
			}
		}
		entries, err := rt.Store.ReadLog(name)
		if err != nil {
			return tabMsg{
				name:  key,
				round: round,
				t:     tabLog,
				content: tabContent{
					loaded: true,
					at:     time.Now(),
					err:    err,
				},
			}
		}

		var b strings.Builder
		n := 0
		for _, e := range entries {
			if e.Round != round {
				continue
			}
			b.WriteString(relevo.LogLine(e))
			b.WriteByte('\n')
			n++
		}

		if n == 0 {
			empty := fmt.Sprintf("no entries for round %d", round)
			if len(entries) == 0 {
				empty = "no entries yet"
			}
			return tabMsg{
				name:  key,
				round: round,
				t:     tabLog,
				content: tabContent{
					loaded: true,
					at:     time.Now(),
					empty:  empty,
				},
			}
		}

		return tabMsg{
			name:  key,
			round: round,
			t:     tabLog,
			content: tabContent{
				loaded: true,
				at:     time.Now(),
				body:   b.String(),
			},
		}
	}
}

// fetchArtifacts reads a reader round's artifact directory for the artifacts
// tab (round 5b): the file list RoundArtifacts orders, and the sel-th file's
// bytes through ReadArtifact. Both answer a live round's files on disk and a
// sealed round's round_file rows, so the tab renders the same either way.
func fetchArtifacts(ctx context.Context, src Source, key string, round, sel int) tea.Cmd {
	return func() tea.Msg {
		rt, name, ok := src.Runtime(key)
		if !ok {
			return tabMsg{
				name:  key,
				round: round,
				t:     tabArtifacts,
				content: tabContent{
					loaded: true,
					round:  round,
					err:    unresolvedKey(key),
				},
			}
		}
		if round < 1 {
			return tabMsg{
				name:  key,
				round: round,
				t:     tabArtifacts,
				content: tabContent{
					loaded: true,
					round:  round,
					empty:  "no completed round yet",
				},
			}
		}
		b, err := rt.Store.Load(name)
		if err != nil {
			return tabMsg{
				name:  key,
				round: round,
				t:     tabArtifacts,
				content: tabContent{
					loaded: true,
					round:  round,
					err:    err,
				},
			}
		}
		actor := relevo.BindingRole(b)
		files, err := relevo.RoundArtifacts(rt, name, round, actor)
		if err != nil {
			return tabMsg{
				name:  key,
				round: round,
				t:     tabArtifacts,
				content: tabContent{
					loaded: true,
					round:  round,
					err:    err,
				},
			}
		}
		if len(files) == 0 {
			return tabMsg{
				name:  key,
				round: round,
				t:     tabArtifacts,
				content: tabContent{
					loaded: true,
					round:  round,
					at:     time.Now(),
					empty:  fmt.Sprintf("no artifacts for round %d", round),
				},
			}
		}
		if sel < 0 || sel >= len(files) {
			sel = 0
		}
		content := tabContent{
			loaded:        true,
			round:         round,
			at:            time.Now(),
			artifacts:     files,
			artifactRel:   files[sel].Rel,
			artifactActor: actor,
		}
		data, rerr := relevo.ReadArtifact(rt, name, round, actor, files[sel].Rel)
		if rerr != nil {
			content.artifactErr = rerr
		} else {
			content.artifactBody = string(data)
		}
		return tabMsg{name: key, round: round, t: tabArtifacts, content: content}
	}
}

// sectionForTab maps a ui tab to the relevo.ShowSection fetchShow reads for
// it -- terminal -> transcript, everything else its own name (§5.8).
func sectionForTab(t tab) relevo.ShowSection {
	switch t {
	case tabPlan:
		return relevo.ShowPlan
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
		return relevo.ShowPlan
	}
}

// tabForSection is sectionForTab's inverse, so fetchShow's tabMsg carries
// the ui tab a reply routes to rather than the relevo.ShowSection it read.
func tabForSection(s relevo.ShowSection) tab {
	switch s {
	case relevo.ShowPlan:
		return tabPlan
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
		return tabPlan
	}
}

// fetchShow wraps relevo.Show for a non-live (hist) binding's detail tabs:
// every tab of an archived or otherwise not-live binding reads the
// database through it. Missing renders as tabContent.empty prose ("no
// <section> for round N"), never as an error; a Show error renders as
// tabContent.err exactly like a failed file read (§6).
func fetchShow(ctx context.Context, rt relevo.Runtime, name string, round int, section relevo.ShowSection) tea.Cmd {
	t := tabForSection(section)
	return func() tea.Msg {
		res, err := relevo.Show(ctx, rt, relevo.ShowOptions{Name: name, Round: round, Section: section})
		if err != nil {
			content := tabContent{loaded: true, round: round, err: err}
			if t != tabPlan && t != tabReport {
				content.at = time.Now()
			}
			return tabMsg{name: name, round: round, t: t, content: content}
		}

		// Show carries no event time for a plan or report section, so those
		// source lines stay timeless rather than claiming the read time.
		content := tabContent{loaded: true, round: round}
		if t != tabPlan && t != tabReport {
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
				b.WriteString(relevo.LogLine(e))
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
// live is false for a hist row's detail (#172, §5.8): every tab goes
// through fetchShow instead of live's own fetchers. A hist row's key is
// its bare name (hist rows are planner-only; the server never has them),
// read through src.Base(). sel is the artifacts tab's cursor, ignored by
// every other tab.
//
// It takes explicit parameters rather than a Model: fetch.go is built in
// Step 2 and Model does not exist until Step 3, so a Model parameter here
// would not compile in the step that introduces it.
func fetchFor(ctx context.Context, src Source, t tab, key string, round, lines, sel int, live bool) tea.Cmd {
	if !live {
		return fetchShow(ctx, src.Base(), key, round, sectionForTab(t))
	}
	switch t {
	case tabPlan:
		return fetchPlan(ctx, src, key, round)
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
