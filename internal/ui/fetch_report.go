package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/delivery"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/ui/widget"
)

// fetchPrompt reads round's prompt file. It is small enough not to need
// capture.ReadDiff's stored-patch indirection: the file is either there or it
// is not.
func fetchPrompt(ctx context.Context, src widget.Source, key string, round int) tea.Cmd {
	return func() tea.Msg {
		rt, name, guard, done := resolvePrompt(src, key, round)
		if done {
			return guard
		}
		data, miss, failed := readPromptFile(rt, name, key, round)
		if failed {
			return miss
		}
		return promptBody(key, round, data, promptSentAt(rt, name, round))
	}
}

func resolvePrompt(src widget.Source, key string, round int) (relevo.Runtime, string, tabMsg, bool) {
	rt, name, ok := src.Runtime(key)
	if !ok {
		return rt, name, tabMsg{
			name:  key,
			round: round,
			t:     tabPrompt,
			content: tabContent{
				loaded: true,
				round:  round,
				err:    unresolvedKey(key),
			},
		}, true
	}
	if round < 1 {
		return rt, name, tabMsg{
			name:  key,
			round: round,
			t:     tabPrompt,
			content: tabContent{
				loaded: true,
				round:  round,
				empty:  "no completed round yet",
			},
		}, true
	}
	return rt, name, tabMsg{}, false
}

func readPromptFile(rt relevo.Runtime, name, key string, round int) ([]byte, tabMsg, bool) {
	data, err := rt.Store.ReadFile(rt.Store.PromptPath(name, round))
	if err == nil {
		return data, tabMsg{}, false
	}
	if os.IsNotExist(err) {
		return nil, tabMsg{
			name:  key,
			round: round,
			t:     tabPrompt,
			content: tabContent{
				loaded: true,
				round:  round,
				empty:  fmt.Sprintf("no prompt for round %d", round),
			},
		}, true
	}
	return nil, tabMsg{
		name:  key,
		round: round,
		t:     tabPrompt,
		content: tabContent{
			loaded: true,
			round:  round,
			err:    err,
		},
	}, true
}

// promptSentAt is the prompt tab's time: when the prompt was sent, from the
// log entry that recorded it. A log read failure or a missing entry leaves
// the time unknown; it never fails the tab.
func promptSentAt(rt relevo.Runtime, name string, round int) time.Time {
	var at time.Time
	if entries, lerr := rt.Store.ReadLog(name); lerr == nil {
		for i := len(entries) - 1; i >= 0; i-- {
			e := entries[i]
			if e.Round == round && e.Direction == store.DirToBuilder && store.IsPromptKind(e.Kind) {
				at = e.TS
				break
			}
		}
	}
	return at
}

func promptBody(key string, round int, data []byte, at time.Time) tabMsg {
	return tabMsg{
		name:  key,
		round: round,
		t:     tabPrompt,
		content: tabContent{
			loaded: true,
			at:     at,
			round:  round,
			body:   string(data),
		},
	}
}

// fetchReport reads round's own report (or question/findings) entry from
// the binding log -- round-scoped, not "the newest one logged": stepping to
// an earlier round must show that round's report, not a later one's. Found
// nothing for round: an empty log at round 1 keeps the familiar "round 1 in
// flight" prose; anything else reads as round being the open round, not yet
// closed.
func fetchReport(ctx context.Context, src widget.Source, key string, round int) tea.Cmd {
	return func() tea.Msg {
		rt, name, ok := src.Runtime(key)
		if !ok {
			return reportUnresolved(key, round)
		}
		entries, err := rt.Store.ReadLog(name)
		if err != nil {
			return reportReadErr(key, round, err)
		}
		if e, ok := scanReport(entries, round); ok {
			return reportFound(key, e, rt, name, round)
		}
		return reportNone(key, round, len(entries))
	}
}

func reportUnresolved(key string, round int) tabMsg {
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

func reportReadErr(key string, round int, err error) tabMsg {
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

func scanReport(entries []store.LogEntry, round int) (store.LogEntry, bool) {
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		if e.Round != round {
			continue
		}
		if e.Direction == store.DirToMasterMind &&
			(e.Kind == store.KindReport || e.Kind == store.KindQuestion || e.Kind == store.KindFindings) {
			return e, true
		}
	}
	return store.LogEntry{}, false
}

// reportFound answers the scanned entry: an empty payload names itself,
// otherwise the tab shows the same bytes a push would carry -- the pointer
// payload plus the artifact file it points at, expanded by the push path's
// own helper (delivery.PushText). Still non-claiming: PushText only reads,
// so the payload stays pending for the route that pushes it, and the read
// happens here, after ReadLog returned and outside any lock.
func reportFound(key string, e store.LogEntry, rt relevo.Runtime, name string, round int) tabMsg {
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
	body, _ := delivery.PushText(e, delivery.BindingFor(rt.Store, name), rt.Store.ReadFile)
	return tabMsg{
		name:  key,
		round: round,
		t:     tabReport,
		content: tabContent{
			loaded: true,
			at:     e.TS,
			round:  round,
			body:   body,
		},
	}
}

func reportNone(key string, round, entries int) tabMsg {
	if entries == 0 && round == 1 {
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
