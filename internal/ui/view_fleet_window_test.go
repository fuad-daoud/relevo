package ui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// windowExpect is the slice of the whole list a window of listH lines must
// show: the window the layout picks, clipped to it, padded out to listH. It is
// the pre-window-only-paint rule, written out longhand.
func windowExpect(f fleetView, full []string, slots []fleetSlot, width, listH int) []string {
	start := f.windowTopSlots(slots, listH)
	end := len(slots)
	if listH > 0 && start+listH < end {
		end = start + listH
	}
	want := make([]string, 0, listH)
	if start < len(slots) {
		want = append(want, full[start:end]...)
	}
	for len(want) < listH {
		want = append(want, fit("", width))
	}
	return want
}

// TestWindowPaintMatchesTheFullList: the fleet paints one window of its list,
// so at every window height -- one line up to the whole list, with the cursor
// in five places -- the painted lines must be exactly the slice of the
// whole-list render they stand for. Painting only the window is the cost this
// pass removed; clipping one line too few is the pixel change it must not make.
func TestWindowPaintMatchesTheFullList(t *testing.T) {
	const width = 120
	env := Env{Loaded: true, Now: railNow, Report: realFleetReport(), StatusAt: railNow, Width: width, Height: 50}

	for _, cursor := range []int{0, 1, 4, 9, 20} {
		f := newFleetView(true)
		f.cursor = cursor
		rows := f.rows(env)
		slots := f.fleetListSlots(env, rows)
		if len(slots) < 2 {
			t.Fatalf("the fixture must lay out a list longer than one line, got %d", len(slots))
		}
		full := make([]string, len(slots))
		for i, s := range slots {
			full[i] = f.paintSlot(s, rows, env, width)
		}
		for listH := 1; listH <= len(slots); listH++ {
			got := f.fleetWindowLines(env, width, listH)
			want := windowExpect(f, full, slots, width, listH)
			if len(got) != len(want) {
				t.Fatalf("cursor %d, window %d: %d lines, want %d", cursor, listH, len(got), len(want))
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("cursor %d, window %d, line %d:\n got %q\nwant %q", cursor, listH, i, got[i], want[i])
				}
			}
		}
	}
}

// TestRowsAreComputedOncePerCycle: a frame sorts the report once, and the memo
// is keyed on the report, the filter text, the done fold and the sort order --
// so an unchanged report is sorted once across frames, a new report is sorted
// again, and typing a filter neither serves stale rows nor sorts twice.
func TestRowsAreComputedOncePerCycle(t *testing.T) {
	const (
		width  = 160
		height = 50
	)
	env := Env{Loaded: true, Now: railNow, Report: realFleetReport(), StatusAt: railNow, Width: width, Height: height}

	f := newFleetView(true)
	for range 3 {
		f.Body(env, width, height)
	}
	if got := f.memo.sorts; got != 1 {
		t.Fatalf("three frames over one report must sort once, got %d sorts", got)
	}

	fresh := realFleetReport()
	fresh.Bindings = fresh.Bindings[:len(fresh.Bindings)-1]
	env.Report = fresh
	f.Body(env, width, height)
	if got := f.memo.sorts; got != 2 {
		t.Fatalf("a new report must sort again, got %d sorts", got)
	}

	typed := newFleetView(true)
	key := func(r rune) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}} }
	res, _ := typed.Update(key('/'), env)
	typed = res.(fleetView)
	for _, r := range "spool" {
		res, _ = typed.Update(key(r), env)
		typed = res.(fleetView)
	}
	before := typed.memo.sorts
	typed.Body(env, width, height)
	if got := typed.memo.sorts; got != before {
		t.Fatalf("the frame after typing must reuse the rows typing computed, got %d sorts", got)
	}
	rows := typed.rows(env)
	if len(rows) != 1 || rows[0].Name != "spool-db" {
		t.Fatalf("typing must narrow the rows, got %d rows", len(rows))
	}
}
