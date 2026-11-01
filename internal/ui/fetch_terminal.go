package ui

import (
	"context"
	"fmt"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/capture"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/ui/widget"
)

// fetchTerminal resolves the binding's builder log for the terminal tab: for
// a headless builder, or a remote builder relevo has a local log for, the tail
// of its round log; otherwise a prose line saying where the builder runs.
// Only the binding's current round (b.Round) has a live process, so a
// headless builder's non-current round with no round log of its own reads as
// the empty prose "terminal is live; round N left no log".
func fetchTerminal(ctx context.Context, src widget.Source, key string, round, lines int) tea.Cmd {
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

		// A headless builder has no pane; its output is a round's
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
			// last round that ran, so fall back to
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
			// as before. When LogPath is the stream path itself, this branch
			// stops applying and rule 2 renders the stream.
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
			// Rule 2: otherwise the round's transcript.
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

		// A remote builder runs on someone else's server: show the
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

		// A local builder is always headless; anything else has no
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

func fetchDiff(ctx context.Context, src widget.Source, key string, round int) tea.Cmd {
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
				// diff is missing, none has been captured yet.
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
