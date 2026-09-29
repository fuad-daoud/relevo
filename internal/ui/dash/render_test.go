package dash

import (
	"context"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/histq"
)

var updateGolden = flag.Bool("update", false, "update golden files")

var ansiRE = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSI(s string) string { return ansiRE.ReplaceAllString(s, "") }

// renderModel is the golden fixture: a sized screen whose fake fetch applies
// the query (so a filtered golden shows the filter working), over the same
// fixed rows and the same fixed clock as every other test.
func renderModel(t *testing.T, w, h int, queryText string) Model {
	t.Helper()
	m := newTestModel(t, queryText)
	m.SetSize(w, h)
	m.fetchFn = func(ctx context.Context, q histq.Query) ([]db.RoundRow, error) {
		return q.Apply(testRows()), nil
	}
	return feed(t, m)
}

func TestGoldenViews(t *testing.T) {
	cases := []struct {
		name          string
		width, height int
		build         func(t *testing.T) Model
	}{
		{
			// Every column at 160: the default newest-first grid.
			name: "flat", width: 160, height: 40,
			build: func(t *testing.T) Model { return renderModel(t, 160, 40, "") },
		},
		{
			// by:candidate, groups collapsed and ordered by summed cost.
			name: "grouped-builder", width: 160, height: 40,
			build: func(t *testing.T) Model { return renderModel(t, 160, 40, "by:candidate") },
		},
		{
			// The first group opened, its rounds indented beneath it.
			name: "grouped-expanded", width: 160, height: 40,
			build: func(t *testing.T) Model {
				m := renderModel(t, 160, 40, "by:candidate")
				res, _ := m.Update(special(tea.KeyEnter))
				return res
			},
		},
		{
			// The editor with a bad query: the error inline under /, the
			// previous query's rows still on the grid.
			name: "editing-parse-error", width: 160, height: 40,
			build: func(t *testing.T) Model {
				m := renderModel(t, 160, 40, "since:30d")
				res, _ := m.Update(key("/"))
				m = res
				res, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlU}) // clear the prefill
				m = res
				res, _ = m.Update(key("bogus:1"))
				m = res
				res, _ = m.Update(special(tea.KeyEnter))
				return res
			},
		},
		{
			// No rows selected: the empty state is prose.
			name: "empty", width: 160, height: 40,
			build: func(t *testing.T) Model {
				m := newTestModel(t, "harness:codex")
				m.SetSize(160, 40)
				m.fetchFn = func(ctx context.Context, q histq.Query) ([]db.RoundRow, error) {
					return nil, nil
				}
				return feed(t, m)
			},
		},
		{
			// A failed query: line 2 is the error, the last good rows stay.
			name: "fetch-error", width: 160, height: 40,
			build: func(t *testing.T) Model {
				m := renderModel(t, 160, 40, "")
				res, _ := m.Update(ErrMsg{Err: errors.New("no such column: repo.origin_url")})
				return res
			},
		},
		{
			// 100 columns: gate, tree and commits are gone, tokens stays.
			name: "narrow-100", width: 100, height: 30,
			build: func(t *testing.T) Model { return renderModel(t, 100, 30, "") },
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := tc.build(t)
			got := stripANSI(m.View())

			path := filepath.Join("testdata", tc.name+".golden")
			if *updateGolden {
				if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
					t.Fatalf("write golden: %v", err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read golden: %v", err)
			}
			if got != string(want) {
				t.Errorf("%s: golden mismatch\ngot:\n%s\nwant:\n%s", tc.name, got, string(want))
			}

			lines := strings.Split(got, "\n")
			if len(lines) != tc.height {
				t.Errorf("%s: %d lines, want %d (height)", tc.name, len(lines), tc.height)
			}
			for i, l := range lines {
				if w := lipgloss.Width(l); w > tc.width {
					t.Errorf("%s: line %d is %d wide, want <= %d: %q", tc.name, i, w, tc.width, l)
				}
			}
		})
	}
}
