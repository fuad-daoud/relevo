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
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/ui/widget"
)

// fetchPrompt reads round's prompt file. It is small enough not to need
// capture.ReadDiff's stored-patch indirection: the file is either there or it
// is not.
func fetchPrompt(ctx context.Context, src widget.Source, key string, round int) tea.Cmd {
	return func() tea.Msg {
		rt, name, ok := src.Runtime(key)
		if !ok {
			return tabMsg{
				name:  key,
				round: round,
				t:     tabPrompt,
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
				t:     tabPrompt,
				content: tabContent{
					loaded: true,
					round:  round,
					empty:  "no completed round yet",
				},
			}
		}
		data, err := rt.Store.ReadFile(rt.Store.PromptPath(name, round))
		if err != nil {
			if os.IsNotExist(err) {
				return tabMsg{
					name:  key,
					round: round,
					t:     tabPrompt,
					content: tabContent{
						loaded: true,
						round:  round,
						empty:  fmt.Sprintf("no prompt for round %d", round),
					},
				}
			}
			return tabMsg{
				name:  key,
				round: round,
				t:     tabPrompt,
				content: tabContent{
					loaded: true,
					round:  round,
					err:    err,
				},
			}
		}

		// The prompt tab's time is when the prompt was sent, from the log
		// entry that recorded it. A log read failure or a missing entry
		// leaves the time unknown; it never fails the tab.
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
			if e.Direction == store.DirToMasterMind &&
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
				// The tab shows the same bytes a push would carry: the pointer
				// payload plus the artifact file it points at, expanded by the
				// push path's own helper (delivery.PushText), so a report whose
				// payload is only a `relevo show` line still reads as the
				// report here. Expansion is PushText's rule, not a second one:
				// KindQuestion and every other kind stay payload-only, the file
				// text is joined on a blank line, capped at MaxPushBytes, and
				// an unreadable file falls back to the payload alone.
				//
				// Still non-claiming: PushText only reads, so the payload stays
				// pending for the route that pushes it. The read happens out
				// here, after ReadLog returned and outside any lock -- no file
				// I/O under the state lock, as pullMatching also insists.
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
