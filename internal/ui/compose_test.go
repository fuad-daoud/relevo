package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

func TestComposeBoxKeepsBothSides(t *testing.T) {
	width := 60
	raw := "“quoted” 日本 text"
	styled := accentStyle.Render(raw)
	base := []string{
		fit(styled, width),
		fit("second line", width),
	}
	box := []string{"[MODAL]"}
	boxW := lipgloss.Width(box[0])
	x := 20
	y := 0

	prefix := ansi.Strip(ansi.Truncate(base[0], x, ""))
	strippedBox := ansi.Strip(box[0])
	suffix := ansi.Strip(ansi.TruncateLeft(base[0], x+boxW, ""))
	expected := prefix + strippedBox + suffix

	result := composeBox(base, box, x, y, width)

	if got := ansi.Strip(result[0]); got != expected {
		t.Errorf("ansi.Strip(result[0]) = %q, want %q", got, expected)
	}
	for i, r := range result {
		if w := lipgloss.Width(r); w != width {
			t.Errorf("row %d width = %d, want %d", i, w, width)
		}
	}
}

func TestComposeBoxClips(t *testing.T) {
	base := []string{"row0", "row1"}
	baseCopy := []string{"row0", "row1"}
	width := 10

	// Box wider than width: clamped at x=0 and fitted
	wideBox := []string{"0123456789ABCDEF"}
	resWide := composeBox(base, wideBox, 5, 0, width)
	if w := lipgloss.Width(resWide[0]); w != width {
		t.Errorf("wide box width = %d, want %d", w, width)
	}
	if ansi.Strip(resWide[0]) != "0123456789" {
		t.Errorf("wide box clipped = %q, want %q", ansi.Strip(resWide[0]), "0123456789")
	}

	// Box taller than base: keeps only in-range rows
	tallBox := []string{"b0", "b1", "b2", "b3"}
	resTall := composeBox(base, tallBox, 0, 0, width)
	if len(resTall) != len(base) {
		t.Errorf("tall box len = %d, want %d", len(resTall), len(base))
	}

	// Base slice is unchanged
	if base[0] != baseCopy[0] || base[1] != baseCopy[1] {
		t.Errorf("base was modified: got %v, want %v", base, baseCopy)
	}
}

func TestDimLinesStripsStyle(t *testing.T) {
	orig := lipgloss.ColorProfile()
	defer lipgloss.SetColorProfile(orig)
	lipgloss.SetColorProfile(termenv.TrueColor)

	accented := accentStyle.Render("sample line")
	dimmed := dimLines([]string{accented})[0]

	accentSeq := strings.Split(accentStyle.Render("·"), "·")[0]
	shadeSeq := strings.Split(shadeStyle.Render("·"), "·")[0]

	if strings.Contains(dimmed, accentSeq) {
		t.Errorf("dimmed output contains accent escape %q:\n%q", accentSeq, dimmed)
	}
	if !strings.Contains(dimmed, shadeSeq) {
		t.Errorf("dimmed output does not contain shade escape %q:\n%q", shadeSeq, dimmed)
	}
}

// TestModalInteriorHasNoBackground: in true colour, a modal whose rows are
// styled paints no row on the old #161a21 panel background; the interior is
// the terminal background (§2.1, §5).
func TestModalInteriorHasNoBackground(t *testing.T) {
	orig := lipgloss.ColorProfile()
	defer lipgloss.SetColorProfile(orig)
	lipgloss.SetColorProfile(termenv.TrueColor)

	// The escape the deleted panelStyle used to paint behind every row.
	oldBG := strings.Split(lipgloss.NewStyle().Background(lipgloss.Color("#161a21")).Render("·"), "·")[0]

	rows := []string{textStyle.Render("one"), accentStyle.Render("two"), ""}
	box := renderModal("modal", rows, 40, 120, false)
	for i, r := range box {
		if strings.Contains(r, oldBG) {
			t.Errorf("box row %d carries the old panel background %q:\n%q", i, oldBG, r)
		}
	}
}

// TestModalRowBandCoversRow: in true colour, a selected modalRow over a styled
// name and desc is one plain run under exactly one background-setting SGR and
// one trailing reset, so the band covers every cell (§2.2, §5).
func TestModalRowBandCoversRow(t *testing.T) {
	orig := lipgloss.ColorProfile()
	defer lipgloss.SetColorProfile(orig)
	lipgloss.SetColorProfile(termenv.TrueColor)

	const innerW = 30
	out := modalRow(true, "atlas", "r4 · demo", "", innerW, accentStyle, dimStyle)

	plain := "▸ atlas  r4 · demo"
	if got := ansi.Strip(out); got != fit(plain, innerW) {
		t.Errorf("plain text = %q, want %q:\n%q", got, fit(plain, innerW), out)
	}

	band := strings.Split(selBandStyle.Render("·"), "·")[0]
	bg := strings.TrimSuffix(strings.TrimPrefix(band, "\x1b["), "m")
	if !strings.HasPrefix(out, "\x1b[") {
		t.Errorf("the band must start at the first cell, got %q", out)
	}
	if n := strings.Count(out, bg); n != 1 {
		t.Errorf("the band's background SGR %q appears %d times, want exactly one:\n%q", bg, n, out)
	}
	if !strings.HasSuffix(out, "\x1b[0m") {
		t.Errorf("the band must end in one reset, got %q", out)
	}
	if n := strings.Count(out, "\x1b[0m"); n != 1 {
		t.Errorf("a reset appears %d times, want one at the end only:\n%q", n, out)
	}
}

// TestListBoxBandCoversRow: in true colour, a selected listBox row is one plain
// run under exactly one background-setting SGR, and its plain width equals
// innerW, so the band covers every cell (§2.2, §5).
func TestListBoxBandCoversRow(t *testing.T) {
	orig := lipgloss.ColorProfile()
	defer lipgloss.SetColorProfile(orig)
	lipgloss.SetColorProfile(termenv.TrueColor)

	const width = 132
	const want = 72
	innerW := modalInnerW(want, width)
	lb := listBox{
		kind: "retry on…",
		items: []listItem{
			{name: "deepseek-v4.1-flash", status: "ready", statusStyle: greenStyle},
		},
		sel: 0,
	}
	rows := lb.view(width)

	band := strings.Split(selBandStyle.Render("·"), "·")[0]
	bg := strings.TrimSuffix(strings.TrimPrefix(band, "\x1b["), "m")

	var selected []string
	for _, row := range rows {
		if strings.Contains(row, bg) {
			selected = append(selected, row)
		}
	}
	if len(selected) != 1 {
		t.Fatalf("exactly one row must carry the band, got %d:\n%v", len(selected), rows)
	}
	row := selected[0]
	if !strings.HasPrefix(row, "\x1b[") {
		t.Errorf("the band must start at the first cell, got %q", row)
	}
	if n := strings.Count(row, bg); n != 1 {
		t.Errorf("the band's background SGR %q appears %d times, want exactly one:\n%q", bg, n, row)
	}
	if got := lipgloss.Width(ansi.Strip(row)); got != innerW {
		t.Errorf("the selected row's plain width = %d, want innerW %d:\n%q", got, innerW, row)
	}
}
