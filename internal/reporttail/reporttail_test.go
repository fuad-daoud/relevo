package reporttail

import (
	"reflect"
	"strings"
	"testing"
)

func TestParseReportTail(t *testing.T) {
	t.Parallel()

	t.Run("complete block with every key", func(t *testing.T) {
		input := `Some report prose here.

` + "```relevo" + `
status: halted
halted_at: "Task 2 step 3"
changed_paths: ["internal/relevo/send.go", "internal/relevo/deliver.go"]
commands_run: ["go test ./...", "make check"]
not_done: ["cleanup tmp", "docs"]
` + "```" + `
`
		tail, ok := Parse([]byte(input))
		if !ok {
			t.Fatalf("expected ok=true, got false")
		}
		expected := Tail{
			Status:          OutcomeHalted,
			HaltedAt:        "Task 2 step 3",
			ChangedPathsSet: true,
			ChangedPaths:    []string{"internal/relevo/send.go", "internal/relevo/deliver.go"},
			CommandsRun:     []string{"go test ./...", "make check"},
			NotDone:         []string{"cleanup tmp", "docs"},
		}
		if !reflect.DeepEqual(tail, expected) {
			t.Fatalf("expected %+v, got %+v", expected, tail)
		}
	})

	t.Run("block with only status done", func(t *testing.T) {
		input := "```relevo\nstatus: done\n```\n"
		tail, ok := Parse([]byte(input))
		if !ok {
			t.Fatalf("expected ok=true, got false")
		}
		expected := Tail{
			Status: OutcomeDone,
		}
		if !reflect.DeepEqual(tail, expected) {
			t.Fatalf("expected %+v, got %+v", expected, tail)
		}
	})

	t.Run("each of the four statuses", func(t *testing.T) {
		statuses := []string{OutcomeDone, OutcomeHalted, OutcomeBlocked, OutcomeDeferred}
		for _, s := range statuses {
			input := "```relevo\nstatus: " + s + "\n```"
			tail, ok := Parse([]byte(input))
			if !ok || tail.Status != s {
				t.Fatalf("expected status %s and ok=true, got %s and %v", s, tail.Status, ok)
			}
		}
	})

	t.Run("unknown status -> false", func(t *testing.T) {
		badStatuses := []string{"unstructured", "DONE", "running", "failed", "pending"}
		for _, s := range badStatuses {
			input := "```relevo\nstatus: " + s + "\n```"
			if _, ok := Parse([]byte(input)); ok {
				t.Fatalf("expected ok=false for status %q, got true", s)
			}
		}
	})
}

func TestParseReportTailMissingBlock(t *testing.T) {
	t.Parallel()

	t.Run("missing block -> false", func(t *testing.T) {
		inputs := [][]byte{
			nil,
			[]byte(""),
			[]byte("Just some report with no relevo block\n"),
		}
		for _, inp := range inputs {
			if _, ok := Parse(inp); ok {
				t.Fatalf("expected ok=false for missing block, got true")
			}
		}
	})

	t.Run("block not at the end (prose after the closing fence) -> false", func(t *testing.T) {
		input := "```relevo\nstatus: done\n```\nSome prose after the closing fence.\n"
		if _, ok := Parse([]byte(input)); ok {
			t.Fatalf("expected ok=false for prose after closing fence, got true")
		}
	})

	t.Run("unclosed fence -> false", func(t *testing.T) {
		input := "```relevo\nstatus: done\n"
		if _, ok := Parse([]byte(input)); ok {
			t.Fatalf("expected ok=false for unclosed fence, got true")
		}
	})

	t.Run("a report whose prose contains a ```go fence before the relevo block still parses", func(t *testing.T) {
		input := `Here is some Go code:

` + "```go" + `
func main() {
    println("hello")
}
` + "```" + `

And here is the tail:

` + "```relevo" + `
status: done
` + "```" + `
`
		tail, ok := Parse([]byte(input))
		if !ok {
			t.Fatalf("expected ok=true, got false")
		}
		if tail.Status != OutcomeDone {
			t.Fatalf("expected status: done, got %s", tail.Status)
		}
	})
}

func TestParseReportTailLastBlockWins(t *testing.T) {
	t.Parallel()

	t.Run("an earlier ```relevo block followed by a later one -> the later one wins", func(t *testing.T) {
		input := `
` + "```relevo" + `
status: halted
halted_at: "step 1"
` + "```" + `

Some prose in between.

` + "```relevo" + `
status: done
` + "```" + `
`
		tail, ok := Parse([]byte(input))
		if !ok {
			t.Fatalf("expected ok=true, got false")
		}
		if tail.Status != OutcomeDone || tail.HaltedAt != "" {
			t.Fatalf("expected later block to win (status: done, halted_at: empty), got %+v", tail)
		}
	})

	t.Run("repeated key last wins", func(t *testing.T) {
		input := `
` + "```relevo" + `
status: halted
halted_at: "first step"
status: done
halted_at: "second step"
changed_paths: ["foo.go"]
changed_paths: []
` + "```" + `
`
		tail, ok := Parse([]byte(input))
		if !ok {
			t.Fatalf("expected ok=true, got false")
		}
		if tail.Status != OutcomeDone {
			t.Fatalf("expected status: done, got %s", tail.Status)
		}
		if tail.HaltedAt != "second step" {
			t.Fatalf("expected halted_at: 'second step', got %q", tail.HaltedAt)
		}
		if tail.ChangedPaths != nil {
			t.Fatalf("expected changed_paths: nil (from []), got %+v", tail.ChangedPaths)
		}
	})

	t.Run("unknown key ignored", func(t *testing.T) {
		input := "```relevo\nstatus: done\ncustom_field: some_val\nanother_key: 123\n```\n"
		tail, ok := Parse([]byte(input))
		if !ok {
			t.Fatalf("expected ok=true, got false")
		}
		if tail.Status != OutcomeDone {
			t.Fatalf("expected status: done, got %s", tail.Status)
		}
	})
}

func TestParseReportTailComments(t *testing.T) {
	t.Parallel()

	t.Run("# comments outside and inside quotes", func(t *testing.T) {
		input := `
` + "```relevo" + `
status: done # this is a comment
halted_at: "step #1" # comment after quote
changed_paths: ["path/with#hash", "path2"] # comment after list
commands_run: [cmd #not_comment]
` + "```" + `
`
		tail, ok := Parse([]byte(input))
		if !ok {
			t.Fatalf("expected ok=true, got false")
		}
		if tail.Status != OutcomeDone {
			t.Fatalf("expected status: done, got %s", tail.Status)
		}
		if tail.HaltedAt != "step #1" {
			t.Fatalf("expected halted_at 'step #1', got %q", tail.HaltedAt)
		}
		expectedPaths := []string{"path/with#hash", "path2"}
		if !reflect.DeepEqual(tail.ChangedPaths, expectedPaths) {
			t.Fatalf("expected paths %+v, got %+v", expectedPaths, tail.ChangedPaths)
		}
		expectedCmds := []string{"cmd #not_comment"}
		if !reflect.DeepEqual(tail.CommandsRun, expectedCmds) {
			t.Fatalf("expected cmds %+v, got %+v", expectedCmds, tail.CommandsRun)
		}
	})
}

func TestParseReportTailScalars(t *testing.T) {
	t.Parallel()

	t.Run("[] -> nil", func(t *testing.T) {
		input := `
` + "```relevo" + `
status: done
changed_paths: []
commands_run: [  ]
not_done: []
` + "```" + `
`
		tail, ok := Parse([]byte(input))
		if !ok {
			t.Fatalf("expected ok=true, got false")
		}
		if tail.ChangedPaths != nil {
			t.Fatalf("expected nil changed_paths, got %+v", tail.ChangedPaths)
		}
		if tail.CommandsRun != nil {
			t.Fatalf("expected nil commands_run, got %+v", tail.CommandsRun)
		}
		if tail.NotDone != nil {
			t.Fatalf("expected nil not_done, got %+v", tail.NotDone)
		}
	})

	t.Run("bare scalar for a list key -> one element", func(t *testing.T) {
		input := `
` + "```relevo" + `
status: done
changed_paths: "single/file.go"
commands_run: make test
not_done: 'leave for later'
` + "```" + `
`
		tail, ok := Parse([]byte(input))
		if !ok {
			t.Fatalf("expected ok=true, got false")
		}
		if !reflect.DeepEqual(tail.ChangedPaths, []string{"single/file.go"}) {
			t.Fatalf("expected ['single/file.go'], got %+v", tail.ChangedPaths)
		}
		if !reflect.DeepEqual(tail.CommandsRun, []string{"make test"}) {
			t.Fatalf("expected ['make test'], got %+v", tail.CommandsRun)
		}
		if !reflect.DeepEqual(tail.NotDone, []string{"leave for later"}) {
			t.Fatalf("expected ['leave for later'], got %+v", tail.NotDone)
		}
	})

	t.Run("a line without : -> false", func(t *testing.T) {
		input := "```relevo\nstatus: done\njust a random line without colon\n```\n"
		if _, ok := Parse([]byte(input)); ok {
			t.Fatalf("expected ok=false for line without colon, got true")
		}
	})
}

func TestParseReportTailLists(t *testing.T) {
	t.Parallel()

	t.Run("block list of three", func(t *testing.T) {
		input := `
` + "```relevo" + `
status: done
halted_at: ""
changed_paths:
  - Makefile
  - scripts/check-plugin-version_test.sh
  - internal/relevo/reporttail.go
commands_run:
  - make check
not_done:
  - docs
` + "```" + `
`
		tail, ok := Parse([]byte(input))
		if !ok {
			t.Fatalf("expected ok=true, got false")
		}
		if tail.Status != OutcomeDone {
			t.Fatalf("expected status done, got %s", tail.Status)
		}
		wantPaths := []string{"Makefile", "scripts/check-plugin-version_test.sh", "internal/relevo/reporttail.go"}
		if !reflect.DeepEqual(tail.ChangedPaths, wantPaths) {
			t.Fatalf("changed_paths: got %+v, want %+v", tail.ChangedPaths, wantPaths)
		}
		if !reflect.DeepEqual(tail.CommandsRun, []string{"make check"}) {
			t.Fatalf("commands_run: got %+v", tail.CommandsRun)
		}
		if !reflect.DeepEqual(tail.NotDone, []string{"docs"}) {
			t.Fatalf("not_done: got %+v", tail.NotDone)
		}
	})
}

func TestParseReportTailListOverrides(t *testing.T) {
	t.Parallel()

	t.Run("block list after a flow list on another key", func(t *testing.T) {
		input := `
` + "```relevo" + `
status: done
commands_run: ["make check"]
changed_paths:
  - Makefile
  - scripts/check-plugin-version_test.sh
` + "```" + `
`
		tail, ok := Parse([]byte(input))
		if !ok {
			t.Fatalf("expected ok=true, got false")
		}
		if !reflect.DeepEqual(tail.CommandsRun, []string{"make check"}) {
			t.Fatalf("commands_run: got %+v", tail.CommandsRun)
		}
		wantPaths := []string{"Makefile", "scripts/check-plugin-version_test.sh"}
		if !reflect.DeepEqual(tail.ChangedPaths, wantPaths) {
			t.Fatalf("changed_paths: got %+v, want %+v", tail.ChangedPaths, wantPaths)
		}
	})

	t.Run("mixed flow then block on one key last wins", func(t *testing.T) {
		input := `
` + "```relevo" + `
status: done
changed_paths: ["old.go"]
changed_paths:
  - Makefile
  - scripts/check-plugin-version_test.sh
` + "```" + `
`
		tail, ok := Parse([]byte(input))
		if !ok {
			t.Fatalf("expected ok=true, got false")
		}
		want := []string{"Makefile", "scripts/check-plugin-version_test.sh"}
		if !reflect.DeepEqual(tail.ChangedPaths, want) {
			t.Fatalf("changed_paths: got %+v, want %+v", tail.ChangedPaths, want)
		}
	})
}

func TestParseReportTailFlowLists(t *testing.T) {
	t.Parallel()

	t.Run("flow list over several lines", func(t *testing.T) {
		input := "```relevo\nstatus: done\nhalted_at: \"\"\nchanged_paths: [\n  \"cmd/relevo/client.go\",\n  \"internal/relevo/send.go\"\n]\ncommands_run: [\"make check\"]\n```\n"
		tail, ok := Parse([]byte(input))
		if !ok {
			t.Fatalf("expected ok=true, got false")
		}
		if tail.Status != OutcomeDone {
			t.Errorf("status = %q, want %q", tail.Status, OutcomeDone)
		}
		if !tail.ChangedPathsSet {
			t.Errorf("ChangedPathsSet = false, want true")
		}
		wantPaths := []string{"cmd/relevo/client.go", "internal/relevo/send.go"}
		if !reflect.DeepEqual(tail.ChangedPaths, wantPaths) {
			t.Errorf("changed_paths = %+v, want %+v", tail.ChangedPaths, wantPaths)
		}
		wantCommands := []string{"make check"}
		if !reflect.DeepEqual(tail.CommandsRun, wantCommands) {
			t.Errorf("commands_run = %+v, want %+v", tail.CommandsRun, wantCommands)
		}
	})

	t.Run("flow list over several lines with a trailing comma and the first item on the key line", func(t *testing.T) {
		input := "```relevo\nstatus: done\nnot_done: [\"a\",\n\"b\",\n]\n```\n"
		tail, ok := Parse([]byte(input))
		if !ok {
			t.Fatalf("expected ok=true, got false")
		}
		wantNotDone := []string{"a", "b"}
		if !reflect.DeepEqual(tail.NotDone, wantNotDone) {
			t.Errorf("not_done = %+v, want %+v", tail.NotDone, wantNotDone)
		}
	})

	t.Run("flow list whose items contain brackets inside quotes", func(t *testing.T) {
		input := "```relevo\nstatus: done\nchanged_paths: [\n  \"odd]name.go\",\n  \"x[.go\",\n  \"y.go\"\n]\n```\n"
		tail, ok := Parse([]byte(input))
		if !ok {
			t.Fatalf("expected ok=true, got false")
		}
		wantPaths := []string{"odd]name.go", "x[.go", "y.go"}
		if !reflect.DeepEqual(tail.ChangedPaths, wantPaths) {
			t.Errorf("changed_paths = %+v, want %+v", tail.ChangedPaths, wantPaths)
		}
	})
}

func TestParseReportTailReasons(t *testing.T) {
	t.Parallel()

	t.Run("dash with no open list key -> false", func(t *testing.T) {
		input := "```relevo\nstatus: done\n- Makefile\n```\n"
		if _, ok := Parse([]byte(input)); ok {
			t.Fatalf("expected ok=false for list item with no open list key, got true")
		}
		_, _, reason := ParseWithReason([]byte(input))
		if !strings.Contains(reason, "has no ':'") {
			t.Fatalf("reason = %q, want tail: line N has no ':'", reason)
		}
	})

	t.Run("rejected block reports why", func(t *testing.T) {
		input := "```relevo\nstatus: done\njust a random line without colon\n```\n"
		_, ok, reason := ParseWithReason([]byte(input))
		if ok {
			t.Fatalf("expected ok=false")
		}
		if reason != "tail: line 3 has no ':'" {
			t.Fatalf("reason = %q, want tail: line 3 has no ':'", reason)
		}
	})

	t.Run("missing block has empty reason", func(t *testing.T) {
		_, ok, reason := ParseWithReason([]byte("plain prose\n"))
		if ok {
			t.Fatalf("expected ok=false")
		}
		if reason != "" {
			t.Fatalf("reason = %q, want empty", reason)
		}
	})

	t.Run("unclosed flow list", func(t *testing.T) {
		input := "```relevo\nstatus: done\nchanged_paths: [\n\"a.go\",\n```\n"
		_, ok, reason := ParseWithReason([]byte(input))
		if ok {
			t.Fatalf("expected ok=false")
		}
		wantReason := "tail: line 3: changed_paths list is not closed"
		if reason != wantReason {
			t.Fatalf("reason = %q, want %q", reason, wantReason)
		}
	})
}

// TestParseReportTailChangedPathsSet pins ChangedPathsSet: the changed_paths
// key counts as present whether its list is inline or a block, whether empty or
// not. Only a tail with no such key at all is never compared against the diff.
func TestParseReportTailChangedPathsSet(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		body    string
		wantSet bool
		want    []string
	}{
		{
			name:    "inline empty list",
			body:    "```relevo\nstatus: done\nchanged_paths: []\n```\n",
			wantSet: true,
			want:    nil,
		},
		{
			name:    "inline two elements",
			body:    "```relevo\nstatus: done\nchanged_paths: [a, b]\n```\n",
			wantSet: true,
			want:    []string{"a", "b"},
		},
		{
			name:    "block form with no items",
			body:    "```relevo\nstatus: done\nchanged_paths:\n```\n",
			wantSet: true,
			want:    nil,
		},
		{
			name:    "block form with two items",
			body:    "```relevo\nstatus: done\nchanged_paths:\n  - a.go\n  - b.go\n```\n",
			wantSet: true,
			want:    []string{"a.go", "b.go"},
		},
		{
			name:    "key absent",
			body:    "```relevo\nstatus: done\n```\n",
			wantSet: false,
			want:    nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tail, ok := Parse([]byte(tc.body))
			if !ok {
				t.Fatalf("Parse(%q) not ok", tc.body)
			}
			if tail.ChangedPathsSet != tc.wantSet {
				t.Errorf("ChangedPathsSet = %v, want %v", tail.ChangedPathsSet, tc.wantSet)
			}
			if !reflect.DeepEqual(tail.ChangedPaths, tc.want) {
				t.Errorf("ChangedPaths = %+v, want %+v", tail.ChangedPaths, tc.want)
			}
		})
	}
}

// stripTailCase is one body StripTail is pinned against; want "" means the
// result must be nil.
type stripTailCase struct {
	name string
	body string
	want string
}

// stripTailCases returns the bodies TestStripTailRemovesTheTrailingBlock pins.
func stripTailCases() []stripTailCase {
	block := "```relevo\nstatus: done\nhalted_at: \"\"\nchanged_paths: []\ncommands_run: []\nnot_done: []\n```\n"
	return []stripTailCase{
		{
			name: "no fence – identical",
			body: "Some prose with no block.\n",
			want: "Some prose with no block.\n",
		},
		{
			name: "well-formed trailing block",
			body: "The review is done.\n\n" + block,
			want: "The review is done.\n",
		},
		{
			name: "block-only body",
			body: block,
			want: "",
		},
		{
			name: "prose after closing fence – identical",
			body: "Prose before.\n\n" + block + "\nMore prose after.\n",
			want: "Prose before.\n\n" + block + "\nMore prose after.\n",
		},
		{
			name: "unclosed fence – cut to end",
			body: "Before.\n\n```relevo\nstatus: done\n",
			want: "Before.\n",
		},
		{
			name: "two blocks – only the last goes",
			body: "First.\n\n```relevo\nstatus: halted\nhalted_at: \"s1\"\nchanged_paths: []\ncommands_run: []\nnot_done: []\n```\n\nSecond.\n\n" + block,
			want: "First.\n\n```relevo\nstatus: halted\nhalted_at: \"s1\"\nchanged_paths: []\ncommands_run: []\nnot_done: []\n```\n\nSecond.\n",
		},
		{
			name: "extra blank lines before fence – dropped",
			body: "Prose.\n\n\n\n" + block,
			want: "Prose.\n",
		},
		{
			name: "CRLF body – kept part is LF",
			body: "Line one.\r\nLine two.\r\n\r\n" + block,
			want: "Line one.\nLine two.\n",
		},
	}
}

func TestStripTailRemovesTheTrailingBlock(t *testing.T) {
	t.Parallel()

	for _, tc := range stripTailCases() {
		t.Run(tc.name, func(t *testing.T) {
			got := StripTail([]byte(tc.body))
			if tc.want == "" {
				if got != nil {
					t.Errorf("StripTail(%q) = %q, want nil", tc.body, got)
				}
				return
			}
			if string(got) != tc.want {
				t.Errorf("StripTail(%q)\ngot:  %q\nwant: %q", tc.body, got, tc.want)
			}
		})
	}

	t.Run("empty body", func(t *testing.T) {
		if got := StripTail(nil); got != nil {
			t.Errorf("StripTail(nil) = %q, want nil", got)
		}
		if got := StripTail([]byte{}); got != nil {
			t.Errorf("StripTail([]byte{}) = %q, want nil", got)
		}
	})
}
