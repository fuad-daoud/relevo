package ui

import (
	"context"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/ui/widget"
	"github.com/fuad-daoud/relevo/internal/view"
)

// fetchLog formats round's log entries with the cmdLog layout.
func fetchLog(ctx context.Context, src widget.Source, key string, round int) tea.Cmd {
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
			b.WriteString(view.LogLine(e))
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
func fetchArtifacts(ctx context.Context, src widget.Source, key string, round, sel int) tea.Cmd {
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
		actor := b.WriterRole()
		output := relevo.OutputFile(rt, b)
		files, err := relevo.RoundArtifacts(rt, name, round, actor, output)
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
			loaded:         true,
			round:          round,
			at:             time.Now(),
			artifacts:      files,
			artifactRel:    files[sel].Rel,
			artifactActor:  actor,
			artifactOutput: output,
			artifactDir:    rt.Store.ArtifactDir(name, round, actor),
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
