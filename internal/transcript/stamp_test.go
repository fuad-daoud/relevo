package transcript

import (
	"fmt"
	"reflect"
	"testing"
	"time"
)

// utc pins the rendering zone for a case that asserts a clock, and restores
// the machine's own zone afterwards. Every case that pins clock bytes uses it.
func utc(t *testing.T) {
	t.Helper()
	prev := time.Local
	time.Local = time.UTC
	t.Cleanup(func() { time.Local = prev })
}

// stampLineAt is the opencode fixture's own event time, epoch milliseconds:
// 2026-09-26T20:16:21Z.
const stampLineAt = 1789589781193

func TestStampPerHarness(t *testing.T) {
	utc(t)

	cases := []struct {
		name string
		kind string
		line string
		want []string
	}{
		{
			"claude clock only on the call line",
			"claude",
			`{"type":"assistant","timestamp":"2026-09-26T20:16:21Z","message":{"content":[{"type":"tool_use","id":"tu1","name":"Bash","input":{"command":"go test ./..."}}]}}`,
			[]string{"20:16:21 ● Bash go test ./..."},
		},
		{
			"claude with no time in the event keeps today's bytes",
			"claude",
			`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"go test ./..."}}]}}`,
			[]string{"● Bash go test ./..."},
		},
		{
			"opencode clock and duration on the call line",
			"opencode",
			fmt.Sprintf(`{"type":"tool_use","timestamp":%d,"part":{"type":"tool","tool":"shell","state":{"status":"completed","input":{"command":"echo probe"},"output":"probe","time":{"start":1000,"end":5200}}}}`, stampLineAt),
			[]string{"20:16:21 +4.2s ● shell echo probe", "20:16:21   ⎿ ok: probe"},
		},
		{
			"opencode clock only when the part measured nothing",
			"opencode",
			fmt.Sprintf(`{"type":"tool_use","timestamp":%d,"part":{"type":"tool","tool":"shell","state":{"status":"completed","input":{"command":"echo probe"},"output":"probe"}}}`, stampLineAt),
			[]string{"20:16:21 ● shell echo probe", "20:16:21   ⎿ ok: probe"},
		},
		{
			"agy duration only on the done line",
			"agy",
			`{"event":"step_update","step_update":{"step_type":"tool","state":"DONE","duration_seconds":0.9,"tool_info":{"name":"run_command","output":"ok  github.com/… 0.4s"}}}`,
			[]string{"+0.9s   ⎿ ok: ok  github.com/… 0.4s"},
		},
		{
			"agy active line carries nothing",
			"agy",
			`{"event":"step_update","step_update":{"step_type":"tool","state":"ACTIVE","tool_name":"run_command","tool_info":{"parameters":{"CommandLine":"go test ./..."}}}}`,
			[]string{"● run_command go test ./..."},
		},
		{
			"codex has no time to show",
			"codex",
			`{"type":"item.completed","item":{"type":"agent_message","text":"hi"}}`,
			[]string{"hi"},
		},
		{
			"a multi-line text element carries the stamp on its first line",
			"claude",
			`{"type":"assistant","timestamp":"2026-09-26T20:16:21Z","message":{"content":[{"type":"text","text":"Reading it.\nTwo lines."}]}}`,
			[]string{"20:16:21 Reading it.\nTwo lines."},
		},
		{
			"a thinking block carries the stamp on its first line",
			"claude",
			`{"type":"assistant","timestamp":"2026-09-26T20:16:21Z","message":{"content":[{"type":"thinking","thinking":"weigh it\nagain"}]}}`,
			[]string{"20:16:21 ∴ weigh it", "∴ again"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := NewRenderer().Render(c.kind, []byte(c.line)); !reflect.DeepEqual(got, c.want) {
				t.Errorf("Render(%q, %q) = %q, want %q", c.kind, c.line, got, c.want)
			}
		})
	}
}

// TestClaudeCallResultDuration is the sequence case: one pass sees the call and
// its result, so the result carries the span between the two event times.
func TestClaudeCallResultDuration(t *testing.T) {
	utc(t)

	call := `{"type":"assistant","timestamp":"2026-09-26T20:16:21.000Z","message":{"content":[{"type":"tool_use","id":"tu1","name":"Bash","input":{"command":"go test ./..."}}]}}`
	result := `{"type":"user","timestamp":"2026-09-26T20:16:25.200Z","message":{"content":[{"type":"tool_result","tool_use_id":"tu1","is_error":false,"content":"ok  github.com/… 0.4s"}]}}`

	r := NewRenderer()
	if got, want := r.Render("claude", []byte(call)), []string{"20:16:21 ● Bash go test ./..."}; !reflect.DeepEqual(got, want) {
		t.Errorf("call = %q, want %q", got, want)
	}
	if got, want := r.Render("claude", []byte(result)), []string{"20:16:25 +4.2s   ⎿ ok: ok  github.com/… 0.4s"}; !reflect.DeepEqual(got, want) {
		t.Errorf("result = %q, want %q", got, want)
	}
	// The pair is consumed once: the same result answered again is absent.
	if got, want := r.Render("claude", []byte(result)), []string{"20:16:25   ⎿ ok: ok  github.com/… 0.4s"}; !reflect.DeepEqual(got, want) {
		t.Errorf("repeated result = %q, want the clock only", got)
	}
}

func TestClaudeDurationAbsentCases(t *testing.T) {
	utc(t)

	call := func(at, id string) string {
		return fmt.Sprintf(`{"type":"assistant","timestamp":%q,"message":{"content":[{"type":"tool_use","id":%q,"name":"Bash","input":{"command":"ls"}}]}}`, at, id)
	}
	result := func(at, id string) string {
		return fmt.Sprintf(`{"type":"user","timestamp":%q,"message":{"content":[{"type":"tool_result","tool_use_id":%q,"is_error":false,"content":"ok"}]}}`, at, id)
	}

	t.Run("an equal pair is a measurement", func(t *testing.T) {
		r := NewRenderer()
		r.Render("claude", []byte(call("2026-09-26T20:16:21.000Z", "tu1")))
		want := []string{"20:16:21 +0.0s   ⎿ ok: ok"}
		if got := r.Render("claude", []byte(result("2026-09-26T20:16:21.000Z", "tu1"))); !reflect.DeepEqual(got, want) {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("an end before the start is absent", func(t *testing.T) {
		r := NewRenderer()
		r.Render("claude", []byte(call("2026-09-26T20:16:25.000Z", "tu1")))
		want := []string{"20:16:21   ⎿ ok: ok"}
		if got := r.Render("claude", []byte(result("2026-09-26T20:16:21.000Z", "tu1"))); !reflect.DeepEqual(got, want) {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("an unknown id is absent", func(t *testing.T) {
		want := []string{"20:16:25   ⎿ ok: ok"}
		if got := NewRenderer().Render("claude", []byte(result("2026-09-26T20:16:25.000Z", "nope"))); !reflect.DeepEqual(got, want) {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("a pair split across passes is absent", func(t *testing.T) {
		NewRenderer().Render("claude", []byte(call("2026-09-26T20:16:21.000Z", "tu1")))
		want := []string{"20:16:25   ⎿ ok: ok"}
		if got := NewRenderer().Render("claude", []byte(result("2026-09-26T20:16:25.000Z", "tu1"))); !reflect.DeepEqual(got, want) {
			t.Errorf("got %q, want %q", got, want)
		}
	})
	t.Run("a result with no time of its own is absent", func(t *testing.T) {
		r := NewRenderer()
		r.Render("claude", []byte(call("2026-09-26T20:16:21.000Z", "tu1")))
		line := `{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"tu1","is_error":false,"content":"ok"}]}}`
		want := []string{"  ⎿ ok: ok"}
		if got := r.Render("claude", []byte(line)); !reflect.DeepEqual(got, want) {
			t.Errorf("got %q, want %q", got, want)
		}
	})
}

func TestSplitStamp(t *testing.T) {
	cases := []struct {
		line string
		want string
		body string
	}{
		{"12:41:03 +4.2s ● shell go test ./...", "12:41:03 +4.2s ", "● shell go test ./..."},
		{"12:41:03 ● shell go test ./...", "12:41:03 ", "● shell go test ./..."},
		{"+0.9s   ⎿ ok: ok  github.com/… 0.4s", "+0.9s ", "  ⎿ ok: ok  github.com/… 0.4s"},
		{"● shell go test ./...", "", "● shell go test ./..."},
		{"12:41:03", "", "12:41:03"},
		{"12:41103 ● x", "", "12:41103 ● x"},
		{"+0.9 ● x", "", "+0.9 ● x"},
		{"+0.9s", "", "+0.9s"},
		{"", "", ""},
	}
	for _, c := range cases {
		stamp, body := SplitStamp(c.line)
		if stamp != c.want || body != c.body {
			t.Errorf("SplitStamp(%q) = (%q, %q), want (%q, %q)", c.line, stamp, body, c.want, c.body)
		}
	}
}
