package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/stats"
	"github.com/muesli/termenv"
)

func strPtr(s string) *string { return &s }

func TestLogFoldsSend(t *testing.T) {
	now := time.Date(2026, 9, 25, 18, 33, 0, 0, time.UTC)
	roundID := "r17"
	roundNum := 17

	events := []db.EventLogRow{
		// Newest first: drift, pick, plan
		{
			TS:          now.Add(2 * time.Second),
			Seq:         3,
			Kind:        "drift",
			Note:        strPtr("36 files, +3187, -45"),
			BindingName: "oc-live",
			RoundID:     &roundID,
			Round:       &roundNum,
		},
		{
			TS:          now.Add(1 * time.Second),
			Seq:         2,
			Kind:        "pick",
			Note:        strPtr("picked claude/anthropic/sonnet for builder: order #5"),
			BindingName: "oc-live",
			RoundID:     &roundID,
			Round:       &roundNum,
		},
		{
			TS:          now,
			Seq:         1,
			Kind:        "plan",
			EntryJSON:   `{"tier":"yolo"}`,
			BindingName: "oc-live",
			RoundID:     &roundID,
			Round:       &roundNum,
		},
	}

	name := func(token string) string {
		if token == "claude/anthropic/sonnet" {
			return "sonnet"
		}
		return token
	}

	entries := buildLogEntries(events, availability.History{}, nil, nil, time.Time{}, name)
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}

	e := entries[0]
	if e.Word != "sent" {
		t.Errorf("Word = %q, want sent", e.Word)
	}
	wantDetail := "on sonnet (#5) · yolo · edited since r16: 36 files +3187 -45"
	if e.Detail != wantDetail {
		t.Errorf("Detail = %q, want %q", e.Detail, wantDetail)
	}
}

// TestLogRowOfBothPromptKindsReadsSent pins the cockpit's event word for a
// prompt row in either stored spelling: the legacy one already written and the
// one new entries carry.
func TestLogRowOfBothPromptKindsReadsSent(t *testing.T) {
	now := time.Date(2026, 9, 25, 18, 33, 0, 0, time.UTC)
	for _, kind := range []string{"prompt", "plan"} {
		entries := buildLogEntries([]db.EventLogRow{
			{TS: now, Seq: 1, Kind: kind, BindingName: "atlas"},
		}, availability.History{}, nil, nil, time.Time{}, nil)
		if len(entries) != 1 || entries[0].Word != "sent" {
			t.Errorf("row of kind %q = %+v, want one entry with Word \"sent\"", kind, entries)
		}
	}
}

func TestLogFoldsReport(t *testing.T) {
	now := time.Date(2026, 9, 25, 19, 4, 0, 0, time.UTC)
	roundID := "r1"
	roundNum := 1

	tokens := int64(138240)
	durMS := int64(66000)

	events := []db.EventLogRow{
		{
			TS:          now.Add(time.Second),
			Seq:         2,
			Kind:        "diff",
			Note:        strPtr("1 file, +1 -0; no commits, dirty"),
			BindingName: "question",
			RoundID:     &roundID,
			Round:       &roundNum,
		},
		{
			TS:          now,
			Seq:         1,
			Kind:        "report",
			EntryJSON:   `{"outcome":"halted","halted_at":"step 2"}`,
			BindingName: "question",
			RoundID:     &roundID,
			Round:       &roundNum,
			Tokens:      &tokens,
			DurationMS:  &durMS,
		},
	}

	entries := buildLogEntries(events, availability.History{}, nil, nil, time.Time{}, nil)
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}

	e := entries[0]
	if e.Word != "halted" {
		t.Errorf("Word = %q, want halted", e.Word)
	}
	wantTokens := stats.ShortTokens(tokens) + " tokens"
	wantDetail := fmt.Sprintf("at step 2 · 1 file +1 -0 · 0 commits · dirty · %s · 1m", wantTokens)
	if e.Detail != wantDetail {
		t.Errorf("Detail = %q, want %q", e.Detail, wantDetail)
	}

	// Done report with " paths:" tail in diff note drops that tail
	doneEvents := []db.EventLogRow{
		{
			TS:          now.Add(time.Second),
			Seq:         2,
			Kind:        "diff",
			Note:        strPtr("1 file, +1 -0; 1 commit, clean paths: main.go foo.go"),
			BindingName: "haiku",
			RoundID:     &roundID,
			Round:       &roundNum,
		},
		{
			TS:          now,
			Seq:         1,
			Kind:        "report",
			EntryJSON:   `{"outcome":"done"}`,
			BindingName: "haiku",
			RoundID:     &roundID,
			Round:       &roundNum,
		},
	}
	doneEntries := buildLogEntries(doneEvents, availability.History{}, nil, nil, time.Time{}, nil)
	if len(doneEntries) != 1 {
		t.Fatalf("got %d entries, want 1", len(doneEntries))
	}
	if doneEntries[0].Word != "done" {
		t.Errorf("Word = %q, want done", doneEntries[0].Word)
	}
	if strings.Contains(doneEntries[0].Detail, "paths:") {
		t.Errorf("Detail = %q, must not contain 'paths:'", doneEntries[0].Detail)
	}
	if strings.Contains(doneEntries[0].Detail, "main.go") {
		t.Errorf("Detail = %q, must not contain paths tail", doneEntries[0].Detail)
	}
	if !strings.HasPrefix(doneEntries[0].Detail, "1 file +1 -0 · 1 commit · clean") {
		t.Errorf("Detail = %q, want prefix '1 file +1 -0 · 1 commit · clean'", doneEntries[0].Detail)
	}
}

func TestLogSwitchAndExit(t *testing.T) {
	now := time.Date(2026, 9, 25, 18, 26, 0, 0, time.UTC)
	roundID := "r2"
	roundNum := 2

	events := []db.EventLogRow{
		{
			TS:          now.Add(time.Second),
			Seq:         2,
			Kind:        "switch",
			Note:        strPtr("switched builder (exited (code 0) without a report): picked glm-5.3-flash for builder: order #6"),
			BindingName: "oc-496",
			RoundID:     &roundID,
			Round:       &roundNum,
		},
		{
			TS:          now,
			Seq:         1,
			Kind:        "exit",
			Note:        strPtr("without a report (code 0)"),
			BindingName: "oc-496",
			RoundID:     &roundID,
			Round:       &roundNum,
		},
	}

	entries := buildLogEntries(events, availability.History{}, nil, nil, time.Time{}, nil)
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}

	if entries[0].Word != "switched" || entries[0].Detail != "to glm-5.3-flash (#6) · exited without a report" {
		t.Errorf("entries[0] = %+v, want switched / to glm-5.3-flash (#6) · exited without a report", entries[0])
	}
	if entries[1].Word != "exited" || entries[1].Detail != "without a report (code 0)" {
		t.Errorf("entries[1] = %+v, want exited / without a report (code 0)", entries[1])
	}

	// Rate-limited switch reason is shortened by statsGateReason
	rateLimitSwitch := []db.EventLogRow{
		{
			TS:          now,
			Seq:         1,
			Kind:        "switch",
			Note:        strPtr("switched builder (rate-limited: AGY_ERROR: Error 429: weekly limit reached): picked glm-5.3-flash for builder: order #6"),
			BindingName: "oc-496",
			RoundID:     &roundID,
			Round:       &roundNum,
		},
	}
	rlEntries := buildLogEntries(rateLimitSwitch, availability.History{}, nil, nil, time.Time{}, nil)
	if len(rlEntries) != 1 {
		t.Fatalf("got %d entries, want 1", len(rlEntries))
	}
	wantReason := statsGateReason("AGY_ERROR: Error 429: weekly limit reached")
	wantDetail := fmt.Sprintf("to glm-5.3-flash (#6) · %s", wantReason)
	if rlEntries[0].Detail != wantDetail {
		t.Errorf("Detail = %q, want %q", rlEntries[0].Detail, wantDetail)
	}
}

func TestLogGatesRevisionsActions(t *testing.T) {
	now := time.Date(2026, 9, 25, 18, 11, 0, 0, time.UTC)
	since := now.Add(-7 * 24 * time.Hour)

	hist := availability.History{
		Events: []availability.Event{
			{
				At:       now,
				Kind:     availability.RateLimited,
				Provider: "cline-pass",
				Note:     "Error 429: weekly Clinepass limit reached, resets in 1d 4h",
			},
			{
				At:       now.Add(-8 * 24 * time.Hour), // older than since
				Kind:     availability.RateLimited,
				Provider: "cline-pass",
				Note:     "old limit",
			},
		},
	}

	revs := []db.RevisionRow{
		{
			Rev:     4,
			At:      now.Add(-time.Hour),
			Message: "config set candidates",
		},
	}

	actions := []actionEntry{
		{
			At:   now.Add(-30 * time.Minute),
			Verb: "gate",
			Text: "failed to gate provider",
			Err:  true,
		},
	}

	entries := buildLogEntries(nil, hist, revs, actions, since, nil)
	// 3 entries: hist (now), action (now-30m), rev (now-1h). Old hist event dropped.
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3", len(entries))
	}

	// Hist rate_limited gives gated and detail starting "cline-pass · "
	if entries[0].Word != "gated" || !strings.HasPrefix(entries[0].Detail, "cline-pass · ") {
		t.Errorf("entries[0] = %+v, want gated starting with 'cline-pass · '", entries[0])
	}

	// Action gives Binding "you", Word = verb, Err carried through
	if entries[1].Binding != "you" || entries[1].Word != "gate" || !entries[1].Err {
		t.Errorf("entries[1] = %+v, want you / gate / Err=true", entries[1])
	}

	// Revision gives config / rev 4 · config set candidates
	if entries[2].Word != "config" || entries[2].Detail != "rev 4 · config set candidates" {
		t.Errorf("entries[2] = %+v, want config / rev 4 · config set candidates", entries[2])
	}
}

func TestLogNewestFirst(t *testing.T) {
	now := time.Date(2026, 9, 25, 20, 0, 0, 0, time.UTC)
	since := now.Add(-7 * 24 * time.Hour)

	events := []db.EventLogRow{
		{TS: now.Add(-4 * time.Minute), Seq: 1, Kind: "plan", BindingName: "atlas"},
	}
	hist := availability.History{
		Events: []availability.Event{
			{At: now.Add(-3 * time.Minute), Kind: availability.Cleared, Provider: "google"},
		},
	}
	revs := []db.RevisionRow{
		{Rev: 1, At: now.Add(-2 * time.Minute), Message: "init"},
	}
	actions := []actionEntry{
		{At: now.Add(-1 * time.Minute), Verb: "stop", Text: "stopped"},
	}

	entries := buildLogEntries(events, hist, revs, actions, since, nil)
	if len(entries) != 4 {
		t.Fatalf("got %d entries, want 4", len(entries))
	}

	// Mixed sources come out in time order, newest first: action (-1m), rev (-2m), gate (-3m), event (-4m)
	if entries[0].Word != "stop" {
		t.Errorf("entries[0].Word = %q, want stop", entries[0].Word)
	}
	if entries[1].Word != "config" {
		t.Errorf("entries[1].Word = %q, want config", entries[1].Word)
	}
	if entries[2].Word != "cleared" {
		t.Errorf("entries[2].Word = %q, want cleared", entries[2].Word)
	}
	if entries[3].Word != "sent" {
		t.Errorf("entries[3].Word = %q, want sent", entries[3].Word)
	}
}

func TestLogViewDayRulesAndMargins(t *testing.T) {
	orig := lipgloss.ColorProfile()
	defer lipgloss.SetColorProfile(orig)
	lipgloss.SetColorProfile(termenv.TrueColor)

	now := time.Date(2026, 9, 25, 19, 0, 0, 0, time.Local)
	v := logView{
		loaded: true,
		events: []db.EventLogRow{
			{TS: now.Add(-10 * time.Minute), Seq: 1, Kind: "exit", BindingName: "atlas"},
			{TS: now.Add(-20 * time.Minute), Seq: 2, Kind: "exit", BindingName: "webshop"},
		},
	}

	env := Env{Now: now, Width: 132, Height: 30}
	body := v.Body(env, 132, 30)
	lines := strings.Split(body, "\n")

	if len(lines) != 30 {
		t.Fatalf("len(lines) = %d, want 30", len(lines))
	}

	band := strings.Split(selBandStyle.Render("·"), "·")[0]
	bg := strings.TrimSuffix(strings.TrimPrefix(band, "\x1b["), "m")

	var ruleFound bool
	for i, l := range lines {
		w := lipgloss.Width(l)
		if w > 132 {
			t.Errorf("line %d width %d > 132", i, w)
		}
		if !strings.HasPrefix(l, "   ") {
			t.Errorf("line %d does not start with 3 spaces: %q", i, l)
		}
		if strings.Contains(l, "today") && strings.Contains(l, "2 events") {
			ruleFound = true
			// Body has a today rule ending with N events, 3 cells before the edge
			// The line ends with "   ", so the rule content ends 3 cells before the edge.
			if !strings.HasSuffix(l, "   ") {
				t.Errorf("rule line does not end with 3 spaces margin: %q", l)
			}
			plain := stripAnsi(l)
			trimmed := strings.TrimRight(plain, " ")
			if !strings.HasSuffix(trimmed, "2 events") {
				t.Errorf("rule line trimmed content = %q, want suffix '2 events'", trimmed)
			}
			// Cursor is never on a rule line:
			if strings.Contains(l, bg) {
				t.Errorf("cursor must never be on a rule line: %q", l)
			}
		}
	}
	if !ruleFound {
		t.Error("expected today rule line in body")
	}
}

func stripAnsi(s string) string {
	var b strings.Builder
	inEsc := false
	for i := 0; i < len(s); i++ {
		if s[i] == '\x1b' {
			inEsc = true
			continue
		}
		if inEsc {
			if s[i] == 'm' {
				inEsc = false
			}
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func TestLogViewFollow(t *testing.T) {
	now := time.Date(2026, 9, 25, 19, 0, 0, 0, time.Local)
	v := logView{
		follow: true,
		cursor: 0,
		loaded: true,
		events: []db.EventLogRow{
			{TS: now.Add(-10 * time.Minute), Seq: 1, Kind: "exit", BindingName: "atlas"},
			{TS: now.Add(-20 * time.Minute), Seq: 2, Kind: "exit", BindingName: "webshop"},
		},
	}
	env := Env{Now: now, Width: 132, Height: 30}

	// Down sets follow false
	res, _ := v.Update(tea.KeyMsg{Type: tea.KeyDown}, env)
	v = res.(logView)
	if v.follow {
		t.Error("down must set follow = false")
	}
	if v.cursor != 1 {
		t.Errorf("cursor = %d, want 1", v.cursor)
	}

	// A new eventLogMsg with a newer event keeps the cursor on the same entry index
	newEvents := []db.EventLogRow{
		{TS: now.Add(-5 * time.Minute), Seq: 3, Kind: "exit", BindingName: "newbind"},
		{TS: now.Add(-10 * time.Minute), Seq: 1, Kind: "exit", BindingName: "atlas"},
		{TS: now.Add(-20 * time.Minute), Seq: 2, Kind: "exit", BindingName: "webshop"},
	}
	res, _ = v.Update(eventLogMsg{events: newEvents, at: now}, env)
	v = res.(logView)
	if v.cursor != 1 {
		t.Errorf("cursor after new eventLogMsg = %d, want 1 (kept index)", v.cursor)
	}

	// f sets follow true and cursor 0
	res, _ = v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'f'}}, env)
	v = res.(logView)
	if !v.follow {
		t.Error("f must set follow = true")
	}
	if v.cursor != 0 {
		t.Errorf("cursor = %d, want 0", v.cursor)
	}
}

func TestLogViewEnterOpensRound(t *testing.T) {
	now := time.Date(2026, 9, 25, 19, 0, 0, 0, time.Local)
	roundNum := 1
	v := logView{
		loaded: true,
		events: []db.EventLogRow{
			{TS: now.Add(-10 * time.Minute), Seq: 1, Kind: "plan", BindingName: "atlas", Round: &roundNum},
		},
		hist: availability.History{
			Events: []availability.Event{
				{At: now.Add(-20 * time.Minute), Kind: availability.RateLimited, Provider: "cline-pass"},
			},
		},
	}
	env := Env{Now: now, Width: 132, Height: 30}

	// Cursor 0 is the sent entry: enter returns non-nil command
	v.cursor = 0
	_, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter}, env)
	if cmd == nil {
		t.Error("enter on a sent entry must return a non-nil command")
	}

	// Cursor 1 is the gate entry: enter returns nil
	v.cursor = 1
	_, cmd = v.Update(tea.KeyMsg{Type: tea.KeyEnter}, env)
	if cmd != nil {
		t.Error("enter on a gate entry must return nil")
	}
}

func TestLogViewFilter(t *testing.T) {
	now := time.Date(2026, 9, 25, 19, 0, 0, 0, time.Local)
	v := logView{
		loaded: true,
		input:  newTextInput(),
		events: []db.EventLogRow{
			{TS: now.Add(-10 * time.Minute), Seq: 1, Kind: "exit", BindingName: "oc-496"},
			{TS: now.Add(-20 * time.Minute), Seq: 2, Kind: "exit", BindingName: "atlas"},
		},
	}
	env := Env{Now: now, Width: 132, Height: 30}

	// Type /
	res, _ := v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}}, env)
	v = res.(logView)
	if !v.editing {
		t.Error("/ must enter editing")
	}

	// Type oc-496
	for _, r := range "oc-496" {
		res, _ = v.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}, env)
		v = res.(logView)
	}

	// Press enter
	res, _ = v.Update(tea.KeyMsg{Type: tea.KeyEnter}, env)
	v = res.(logView)
	if v.editing {
		t.Error("enter must exit editing")
	}
	if v.filter != "oc-496" {
		t.Errorf("filter = %q, want oc-496", v.filter)
	}

	filtered := v.filteredEntries(env)
	if len(filtered) != 1 {
		t.Fatalf("got %d filtered entries, want 1", len(filtered))
	}
	if filtered[0].Binding != "oc-496" {
		t.Errorf("filtered[0].Binding = %q, want oc-496", filtered[0].Binding)
	}
}

func TestLogSwitchRateLimitedReason(t *testing.T) {
	now := time.Date(2026, 9, 25, 18, 30, 0, 0, time.UTC)
	roundID := "r5"
	roundNum := 5
	events := []db.EventLogRow{
		{
			TS:          now,
			Seq:         1,
			Kind:        "switch",
			Note:        strPtr("switched builder (rate-limited: Error 429: weekly Clinepass limit reached, resets in 1d 4h): picked claude/anthropic/sonnet for builder: order #5; skipped …"),
			BindingName: "oc-live",
			RoundID:     &roundID,
			Round:       &roundNum,
		},
	}
	name := func(token string) string {
		if token == "claude/anthropic/sonnet" {
			return "sonnet"
		}
		return token
	}
	entries := buildLogEntries(events, availability.History{}, nil, nil, time.Time{}, name)
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	wantDetail := "to sonnet (#5) · weekly Clinepass limit reached, resets in 1d 4h"
	if entries[0].Detail != wantDetail {
		t.Errorf("Detail = %q, want %q", entries[0].Detail, wantDetail)
	}
}

// A gate history row with an empty note shows the provider alone: no trailing
// " · " from a reason that is not there (§2, round 7).
func TestLogGateRowWithoutReason(t *testing.T) {
	now := time.Date(2026, 9, 25, 18, 11, 0, 0, time.UTC)
	hist := availability.History{
		Events: []availability.Event{
			{At: now, Kind: availability.RateLimited, Provider: "cline-pass", Note: ""},
		},
	}

	entries := buildLogEntries(nil, hist, nil, nil, time.Time{}, nil)
	if len(entries) != 1 {
		t.Fatalf("got %d entries, want 1", len(entries))
	}
	if entries[0].Word != "gated" {
		t.Errorf("Word = %q, want gated", entries[0].Word)
	}
	if entries[0].Detail != "cline-pass" {
		t.Errorf("Detail = %q, want %q", entries[0].Detail, "cline-pass")
	}
}

func TestLogViewBlankRowAboveHeader(t *testing.T) {
	now := time.Date(2026, 9, 25, 19, 0, 0, 0, time.Local)
	v := logView{
		loaded: true,
		events: []db.EventLogRow{
			{TS: now.Add(-10 * time.Minute), Seq: 1, Kind: "exit", BindingName: "oc-496"},
		},
	}
	env := Env{Now: now, Width: 132, Height: 30}
	body := v.Body(env, 132, 30)
	lines := strings.Split(body, "\n")
	if len(lines) < 2 {
		t.Fatalf("got %d lines, want at least 2", len(lines))
	}
	if strings.TrimSpace(lines[0]) != "" {
		t.Errorf("line 0 must be blank (spaces only), got %q", lines[0])
	}
	if !strings.Contains(lines[1], "TIME") {
		t.Errorf("line 1 must contain TIME, got %q", lines[1])
	}
}

func TestLogViewPageDown(t *testing.T) {
	now := time.Date(2026, 9, 25, 19, 0, 0, 0, time.Local)
	makeView := func(n int) (logView, Env) {
		var events []db.EventLogRow
		for i := 0; i < n; i++ {
			events = append(events, db.EventLogRow{
				TS:          now.Add(-time.Duration(i+1) * time.Minute),
				Seq:         i + 1,
				Kind:        "exit",
				BindingName: "oc-496",
			})
		}
		v := logView{loaded: true, events: events}
		env := Env{Now: now, Width: 132, Height: 34}
		return v, env
	}

	// With 40 entries, second pgdown clamps to the last entry (39)
	v, env := makeView(40)
	res, _ := v.Update(tea.KeyMsg{Type: tea.KeyPgDown}, env)
	v = res.(logView)
	want1 := bodyHeight(env) - 3
	if v.cursor != want1 {
		t.Errorf("cursor after 1st pgdown = %d, want %d", v.cursor, want1)
	}

	res, _ = v.Update(tea.KeyMsg{Type: tea.KeyPgDown}, env)
	v = res.(logView)
	want2 := 39 // 2 * 26 = 52 clamped to 39
	if v.cursor != want2 {
		t.Errorf("cursor after 2nd pgdown = %d, want %d", v.cursor, want2)
	}

	// With 60 entries, second pgdown is exactly twice that (52)
	v60, env60 := makeView(60)
	res, _ = v60.Update(tea.KeyMsg{Type: tea.KeyPgDown}, env60)
	v60 = res.(logView)
	if v60.cursor != want1 {
		t.Errorf("cursor after 1st pgdown = %d, want %d", v60.cursor, want1)
	}
	res, _ = v60.Update(tea.KeyMsg{Type: tea.KeyPgDown}, env60)
	v60 = res.(logView)
	if v60.cursor != 2*want1 {
		t.Errorf("cursor after 2nd pgdown = %d, want %d", v60.cursor, 2*want1)
	}
}
