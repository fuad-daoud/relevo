package transcript

import (
	"bytes"
	"reflect"
	"strings"
	"testing"
)

const (
	museFailedQuota = `{"payload_type":"run.terminal.failed","payload":{"terminal":"failed","error":{"kind":"quota","message":"Usage limit reached for your plan","retryable":false,"quota":{"limit_kind":"tierLimit","resets_at_ms":1792848178000}}}}`
	museDelta       = `{"payload_type":"run.output.delta","payload":{"text":"hi"}}`
	museToolOK      = `{"payload_type":"tool.result","payload":{"text":"line one\nline two","correlation_facts":{"outcome":"success","tool_name":"bash"}}}`
	museCompleted   = `{"payload_type":"run.terminal.completed","payload":{"terminal":"completed","text":"all done"}}`
	museCancelled   = `{"payload_type":"run.terminal.cancelled","payload":{"terminal":"cancelled"}}`
)

func TestMuseSessionID(t *testing.T) {
	const id = "01a16d03-a71a-75f2-a2cd-394c8814cff2"
	cases := []struct {
		name, line, want string
	}{
		{"first line of the session stream", streamFixture(t, "muse-stream.jsonl", 1), id},
		{"usage line is not on the session stream", streamFixture(t, "muse-stream.jsonl", 23), ""},
		{"line without a stream", museDelta, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := SessionID("muse", []byte(c.line)); got != c.want {
				t.Errorf("SessionID = %q, want %q", got, c.want)
			}
		})
	}
}

func TestMuseFirstOutput(t *testing.T) {
	cases := []struct {
		name, line string
		want       bool
	}{
		{"delta", museDelta, true},
		{"tool result", museToolOK, true},
		{"lifecycle started", `{"payload_type":"run.lifecycle.started","payload":{}}`, false},
		{"model configured", `{"payload_type":"run.model.configured","payload":{}}`, false},
		{"terminal", museCompleted, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := FirstOutput("muse", []byte(c.line)); got != c.want {
				t.Errorf("FirstOutput = %v, want %v", got, c.want)
			}
		})
	}
}

func TestMuseFinalText(t *testing.T) {
	got := FinalText("muse", fixture("muse.jsonl")(t))
	if !strings.HasPrefix(got, "Done. All work is committed on `muse/bench-1293`.") {
		t.Errorf("FinalText = %.80q, want the terminal's answer", got)
	}
	if got := FinalText("muse", []byte(museDelta+"\n")); got != "" {
		t.Errorf("FinalText of a bare delta = %q, want empty", got)
	}
}

func TestMuseTurnEnd(t *testing.T) {
	cases := []struct {
		name   string
		stream string
		want   TurnEnd
	}{
		{"completed", museDelta + "\n" + museCompleted + "\n", TurnEndClean},
		{"failed", museToolOK + "\n" + museFailedQuota + "\n", TurnEndUnclean},
		{"cancelled", museCancelled + "\n", TurnEndUnclean},
		{"cut after a tool result", museToolOK + "\n", TurnEndUnclean},
		{"resumed after a clean turn then cut", museCompleted + "\n" + museDelta + "\n", TurnEndUnclean},
		{"bookkeeping only", `{"payload_type":"run.lifecycle.started","payload":{}}` + "\n", TurnEndUnknown},
	}
	for _, c := range cases {
		if got := TurnEndState("muse", []byte(c.stream)); got != c.want {
			t.Errorf("%s: TurnEndState = %d, want %d", c.name, got, c.want)
		}
	}
}

func TestMuseErrorTextAndLimit(t *testing.T) {
	const msg = "Usage limit reached for your plan"
	if got, ok := ErrorText("muse", []byte(museFailedQuota)); !ok || got != msg {
		t.Errorf("ErrorText = %q, %v, want %q", got, ok, msg)
	}
	if got := LimitLines("muse", []byte(museFailedQuota)); !reflect.DeepEqual(got, []string{msg}) {
		t.Errorf("LimitLines = %q, want the error message", got)
	}
	reason := `{"payload_type":"run.terminal.failed","payload":{"reason":{"message":"tier limit"}}}`
	if got, _ := ErrorText("muse", []byte(reason)); got != "tier limit" {
		t.Errorf("reason object = %q, want tier limit", got)
	}
	bare := `{"payload_type":"run.terminal.failed","payload":{"error":{"quota":{"limit_kind":"tierLimit"}}}}`
	if got := LimitLines("muse", []byte(bare)); len(got) != 1 || !strings.Contains(got[0], "limit_kind") {
		t.Errorf("message-less failure = %q, want the payload as evidence", got)
	}
}

func TestMuseModelTextIsNotFailure(t *testing.T) {
	for _, line := range []string{
		`{"payload_type":"run.output.delta","payload":{"text":"Usage limit reached"}}`,
		`{"payload_type":"tool.result","payload":{"text":"rate limit exceeded","correlation_facts":{"outcome":"error"}}}`,
		museCompleted,
		museCancelled,
	} {
		if got := LimitLines("muse", []byte(line)); got != nil {
			t.Errorf("LimitLines(%s) = %q, want nothing", line, got)
		}
		if got, ok := ErrorText("muse", []byte(line)); ok {
			t.Errorf("ErrorText(%s) = %q, want none", line, got)
		}
	}
}

func TestMuseRendersTheFixture(t *testing.T) {
	r := NewRenderer()
	var got []string
	for _, line := range bytes.Split(fixture("muse.jsonl")(t), []byte{'\n'}) {
		got = append(got, r.Render("muse", line)...)
	}
	if len(got) < 8 || got[0] != "● bash" || !strings.HasPrefix(got[1], "  ⎿ ok: ") {
		t.Fatalf("rendered head = %q", got)
	}
	text := strings.Join(got, "\n")
	for _, want := range []string{
		"● read_file", "Done. All work is committed on `muse/bench-1293`.",
		"[usage] in 199605  cache 9073518  out 56699  reasoning 31120  window 4%  weekly 1%",
		"[terminal.completed]",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("rendering lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "todo.snapshot") || strings.Contains(text, "[run.output.delta]") {
		t.Errorf("rendering shows bookkeeping or raw deltas:\n%s", text)
	}
}

func TestMuseRenderTable(t *testing.T) {
	cases := map[string]struct {
		lines []string
		want  []string
	}{
		"deltas join into one block before the next event": {
			[]string{museDelta, `{"payload_type":"run.output.delta","payload":{"text":" there\nbye"}}`, museToolOK},
			[]string{"hi there", "bye", "● bash", "  ⎿ ok: line one"},
		},
		"failed tool result": {
			[]string{`{"payload_type":"tool.result","payload":{"text":"boom","correlation_facts":{"outcome":"error","tool_name":"bash"}}}`},
			[]string{"● bash", "  ⎿ error: boom"},
		},
		"failed terminal shows the message": {
			[]string{museFailedQuota},
			[]string{"  ⎿ error: Usage limit reached for your plan"},
		},
		"null allowance": {
			[]string{`{"payload_type":"run.usage","payload":{"allowance":null,"models":[{"input_tokens":10,"cached_tokens":4,"output_tokens":2,"reasoning_tokens":1}]}}`},
			[]string{"[usage] in 6  cache 4  out 2  reasoning 1"},
		},
		"unknown event": {[]string{`{"payload_type":"mystery.event","payload":{}}`}, []string{"[mystery.event]"}},
		"task lifecycle is quiet": {
			[]string{`{"payload_type":"task.lifecycle.started","payload":{}}`}, nil,
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			r := NewRenderer()
			var got []string
			for _, l := range c.lines {
				got = append(got, r.Render("muse", []byte(l))...)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}
