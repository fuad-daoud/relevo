package transcript

import (
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite testdata/*.log from the renderer")

func TestFixtures(t *testing.T) {
	streams, err := filepath.Glob(filepath.Join("testdata", "*.jsonl"))
	if err != nil || len(streams) == 0 {
		t.Fatalf("no fixtures: %v", err)
	}
	for _, stream := range streams {
		kind := strings.TrimSuffix(filepath.Base(stream), ".jsonl")
		t.Run(kind, func(t *testing.T) {
			raw, err := os.ReadFile(stream)
			if err != nil {
				t.Fatal(err)
			}
			logPath := filepath.Join("testdata", kind+".log")
			var got []string
			for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
				got = append(got, Render(kind, []byte(line))...)
			}
			g := strings.Join(got, "\n") + "\n"
			if *update {
				if err := os.WriteFile(logPath, []byte(g), 0o644); err != nil {
					t.Fatalf("write %s: %v", logPath, err)
				}
				return
			}
			want, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			if w := string(want); g != w {
				t.Errorf("rendered:\n%s\nwant:\n%s", g, w)
			}
		})
	}
}

func TestRenderRules(t *testing.T) {
	cases := map[string]struct {
		kind string
		line string
		want []string
	}{
		"empty":               {"claude", "", nil},
		"blank":               {"claude", "   ", nil},
		"non-json":            {"claude", "plain text", []string{"plain text"}},
		"json but not object": {"claude", `["a"]`, []string{`["a"]`}},
		"broken json":         {"agy", `{"event":`, []string{`{"event":`}},
		"unknown type":        {"claude", `{"type":"brand_new"}`, []string{"[brand_new]"}},
		"unknown event":       {"agy", `{"event":"brand_new"}`, []string{"[brand_new]"}},
		"no type at all":      {"claude", `{"x":1}`, []string{"[?]"}},
		"unknown kind":        {"droid", `{"type":"item"}`, []string{"[item]"}},

		// Only a line that starts with a trailer prefix after trimming is
		// dropped; the prefix inside a line is the builder's own text.
		"indented exit trailer":  {"claude", "   relevo-exit:0", nil},
		"rusage trailer":         {"claude", "relevo-rusage:cpu_usec=1", nil},
		"trailing whitespace":    {"claude", "relevo-exit:3  \t", nil},
		"prefix in the middle":   {"claude", "the builder echoed relevo-exit:0", []string{"the builder echoed relevo-exit:0"}},
		"exit word is not a hit": {"claude", "exiting:0", []string{"exiting:0"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := Render(c.kind, []byte(c.line)); !reflect.DeepEqual(got, c.want) {
				t.Errorf("Render(%q, %q) = %q, want %q", c.kind, c.line, got, c.want)
			}
		})
	}
}

func TestRenderDropsSupervisorTrailers(t *testing.T) {
	body := map[string]string{
		"claude":   `{"type":"assistant","message":{"content":[{"type":"text","text":"the answer"}]}}`,
		"agy":      `{"event":"result","result":{"status":"SUCCESS","response":"the answer","denied_actions":[]}}`,
		"opencode": `{"type":"text","part":{"type":"text","text":"the answer"}}`,
		"codex":    `{"type":"item.completed","item":{"type":"agent_message","text":"the answer"}}`,
	}
	trailers := map[string][]string{
		"relevo": {"relevo-rusage:cpu_usec=4982568 mem_peak=348131328", "relevo-exit:0"},
	}
	for _, kind := range []string{"claude", "agy", "opencode", "codex"} {
		for name, lines := range trailers {
			t.Run(kind+"/"+name, func(t *testing.T) {
				var got []string
				for _, line := range append([]string{body[kind]}, lines...) {
					got = append(got, Render(kind, []byte(line))...)
				}
				if want := Render(kind, []byte(body[kind])); !reflect.DeepEqual(got, want) {
					t.Errorf("rendered = %q, want %q without the trailers", got, want)
				}
			})
		}
	}
}

func TestClaudeTable(t *testing.T) {
	cases := map[string]struct {
		line string
		want []string
	}{
		"text and tool in one message": {
			`{"type":"assistant","message":{"content":[{"type":"text","text":"Reading it.\nTwo lines."},{"type":"tool_use","name":"Read","input":{"file_path":"internal/doctor/doctor.go"}}]}}`,
			[]string{"Reading it.\nTwo lines.", "● Read internal/doctor/doctor.go"},
		},
		"thinking then text in one message": {
			`{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"weigh it"},{"type":"text","text":"done"}]}}`,
			[]string{"∴ weigh it", "done"},
		},
		"empty thinking block is nothing": {
			`{"type":"assistant","message":{"content":[{"type":"thinking","thinking":"","signature":"sig"}]}}`,
			nil,
		},
		"tool with no argument": {
			`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"ListAgents","input":{}}]}}`,
			[]string{"● ListAgents"},
		},
		"tool with a long multi-line command": {
			`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"` + strings.Repeat("x", 250) + `\nsecond"}}]}}`,
			[]string{"● Bash " + strings.Repeat("x", 200) + "..."},
		},
		"result error with array content": {
			`{"type":"user","message":{"content":[{"type":"tool_result","is_error":true,"content":[{"type":"text","text":"exit status 1\nmore"}]}]}}`,
			[]string{"  ⎿ error: exit status 1"},
		},
		"result error with empty message": {
			`{"type":"user","message":{"content":[{"type":"tool_result","is_error":true,"content":""}]}}`,
			[]string{"  ⎿ error"},
		},
		"result ok with string content": {
			`{"type":"user","message":{"content":[{"type":"tool_result","is_error":false,"content":"     1\t# relevo\n     2\t"}]}}`,
			[]string{"  ⎿ ok:      1\t# relevo"},
		},
		"result ok with array content": {
			`{"type":"user","message":{"content":[{"type":"tool_result","is_error":false,"content":[{"type":"text","text":"7c3ca64 docs: x\ne19924c feat: y"}]}]}}`,
			[]string{"  ⎿ ok: 7c3ca64 docs: x"},
		},
		"result ok with empty content": {
			`{"type":"user","message":{"content":[{"type":"tool_result","is_error":false,"content":""}]}}`,
			[]string{"  ⎿ ok"},
		},
		"error event with a message": {
			`{"type":"error","message":"Unexpected server error","ref":"err_a1d49da9"}`,
			[]string{"  ⎿ error: Unexpected server error"},
		},
		"final result with denials and error": {
			`{"type":"result","subtype":"error_during_execution","is_error":true,"result":"gave up","permission_denials":[{"tool_name":"Bash"},{"tool_name":"Edit"}]}`,
			[]string{"denied: Bash", "denied: Edit", "result: error_during_execution", "gave up"},
		},
		"final result empty":  {`{"type":"result","subtype":"success","is_error":false,"result":""}`, nil},
		"system is noise":     {`{"type":"system","subtype":"hook_started"}`, nil},
		"rate limit is noise": {`{"type":"rate_limit_event"}`, nil},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := Render("claude", []byte(c.line)); !reflect.DeepEqual(got, c.want) {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestMainArgumentPick(t *testing.T) {
	cases := map[string]struct {
		params map[string]any
		want   string
	}{
		"priority key wins over others": {map[string]any{"description": "d", "command": "c"}, "● Bash c"},
		"single string param":           {map[string]any{"AbsolutePathX": "/x"}, "● Bash /x"},
		"first in sorted order":         {map[string]any{"zeta": "z", "alpha": "a", "n": 3}, "● Bash a"},
		"non-string only":               {map[string]any{"n": 3, "ok": true}, "● Bash"},
		"empty string is not an arg":    {map[string]any{"command": "", "b": "x"}, "● Bash x"},
		"nil params":                    {nil, "● Bash"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := toolLine("Bash", c.params); got != c.want {
				t.Errorf("toolLine = %q, want %q", got, c.want)
			}
		})
	}
}

func TestOneLineCutsOnARuneBoundary(t *testing.T) {
	s := strings.Repeat("é", 101) // 202 bytes; byte 200 is inside the 101st rune
	got := oneLine(s)
	if !strings.HasSuffix(got, "...") || strings.Count(got, "é") != 100 {
		t.Errorf("oneLine = %q; want 100 whole runes then ...", got)
	}
	if got := oneLine("short\nrest"); got != "short" {
		t.Errorf("oneLine = %q, want the first line", got)
	}
	if got := oneLine("with cr\r\nnext"); got != "with cr" {
		t.Errorf("oneLine = %q, want the carriage return stripped", got)
	}
}

func TestAgyTable(t *testing.T) {
	cases := map[string]struct {
		line string
		want []string
	}{
		"tool active with several params": {
			`{"event":"step_update","step_update":{"step_type":"tool","state":"ACTIVE","tool_name":"run_command","tool_info":{"parameters":{"Cwd":"/repo","CommandLine":"go test ./..."}}}}`,
			[]string{"● run_command go test ./..."},
		},
		"tool name falls back to tool_info.name": {
			`{"event":"step_update","step_update":{"step_type":"tool","state":"ACTIVE","tool_info":{"name":"view_file","parameters":{"AbsolutePath":"/x"}}}}`,
			[]string{"● view_file /x"},
		},
		"tool done with output": {
			`{"event":"step_update","step_update":{"step_type":"tool","state":"DONE","tool_name":"list_dir","tool_info":{"output":"cmd/\ndist/\ndocs/"}}}`,
			[]string{"  ⎿ ok: cmd/"},
		},
		"tool done without output": {
			`{"event":"step_update","step_update":{"step_type":"tool","state":"DONE","tool_name":"write_to_file","tool_info":{"output":""}}}`,
			[]string{"  ⎿ ok"},
		},
		"tool done with crlf output": {
			`{"event":"step_update","step_update":{"step_type":"tool","state":"DONE","tool_name":"run_command","tool_info":{"output":"FAIL\tx [setup failed]\r\nmore\r\n"}}}`,
			[]string{"  ⎿ ok: FAIL\tx [setup failed]"},
		},
		"tool error without message": {
			`{"event":"step_update","step_update":{"step_type":"tool","state":"ERROR","tool_name":"view_file","tool_info":{"error":{"type":"TOOL_ERROR"}}}}`,
			[]string{"  ⎿ error"},
		},
		"tool in an unknown state": {
			`{"event":"step_update","step_update":{"step_type":"tool","state":"PAUSED","tool_name":"view_file"}}`,
			[]string{"[step_update]"},
		},
		"agent_response step is noise": {
			`{"event":"step_update","step_update":{"step_type":"agent_response","state":"DONE"}}`,
			nil,
		},
		"init is noise": {`{"event":"init","init":{}}`, nil},
		"result failure with response": {
			`{"event":"result","result":{"status":"ERROR","response":"could not continue","denied_actions":[]}}`,
			[]string{"result: ERROR", "could not continue"},
		},
		"denied action without display name": {
			`{"event":"result","result":{"status":"SUCCESS","response":"","denied_actions":[{"action":"run_command"}]}}`,
			[]string{"denied: run_command"},
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := Render("agy", []byte(c.line)); !reflect.DeepEqual(got, c.want) {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestOpencodeTable(t *testing.T) {
	cases := map[string]struct {
		line string
		want []string
	}{
		"error": {
			`{"type":"error","timestamp":1789589781193,"sessionID":"ses_1","error":{"type":"provider.no-route","message":"Model unavailable: openrouter/z-ai/glm-5.3-flash"}}`,
			[]string{"  ⎿ error: Model unavailable: openrouter/z-ai/glm-5.3-flash"},
		},
		"tool_use in an unknown status is rule 5": {
			`{"type":"tool_use","part":{"type":"tool","tool":"read","state":{"status":"running","input":{"path":"a"}}}}`,
			[]string{"[tool_use]"},
		},
		"tool_use with no argument": {
			`{"type":"tool_use","part":{"type":"tool","tool":"todoread","state":{"status":"completed","input":{},"output":""}}}`,
			[]string{"● todoread", "  ⎿ ok"},
		},
		"unknown type is its type": {
			`{"type":"session_compacted","part":{}}`,
			[]string{"[session_compacted]"},
		},
		"missing type is ?": {
			`{"part":{"type":"text","text":"hi"}}`,
			[]string{"[?]"},
		},
		"empty text is nothing, as in the claude table": {
			`{"type":"text","part":{"type":"text","text":""}}`,
			nil,
		},
		"reasoning with several lines": {
			`{"type":"reasoning","part":{"type":"reasoning","text":"plan\nthen act"}}`,
			[]string{"∴ plan", "∴ then act"},
		},
		"reasoning with empty text is nothing": {
			`{"type":"reasoning","part":{"type":"reasoning","text":""}}`,
			nil,
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := Render("opencode", []byte(c.line)); !reflect.DeepEqual(got, c.want) {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

type lineCase struct {
	line string
	want []string
}

var codexCases = map[string]lineCase{
	"thread.started is noise": {
		`{"type":"thread.started","thread_id":"t1"}`,
		nil,
	},
	"turn.started is noise": {
		`{"type":"turn.started"}`,
		nil,
	},
	"item.started is noise": {
		`{"type":"item.started","item":{"id":"i1","type":"command_execution"}}`,
		nil,
	},
	"turn.failed": {
		`{"type":"turn.failed","error":{"message":"boom"}}`,
		[]string{"  ⎿ error: boom"},
	},
	"top-level error": {
		`{"type":"error","message":"top level boom"}`,
		[]string{"  ⎿ error: top level boom"},
	},
	"item.completed agent_message": {
		`{"type":"item.completed","item":{"type":"agent_message","text":"hi"}}`,
		[]string{"hi"},
	},
	"item.completed agent_message with empty text": {
		`{"type":"item.completed","item":{"type":"agent_message","text":""}}`,
		nil,
	},
	"item.completed reasoning renders as thinking": {
		`{"type":"item.completed","item":{"type":"reasoning","text":"thinking"}}`,
		[]string{"∴ thinking"},
	},
	"item.completed reasoning with empty text": {
		`{"type":"item.completed","item":{"type":"reasoning","text":""}}`,
		nil,
	},
	"item.completed error": {
		`{"type":"item.completed","item":{"type":"error","message":"item boom"}}`,
		[]string{"  ⎿ error: item boom"},
	},
	"command_execution success": {
		`{"type":"item.completed","item":{"type":"command_execution","command":"ls","aggregated_output":"a\nb\n","exit_code":0}}`,
		[]string{"● bash ls", "  ⎿ ok: a"},
	},
	"command_execution failure": {
		`{"type":"item.completed","item":{"type":"command_execution","command":"ls","aggregated_output":"nope","exit_code":2}}`,
		[]string{"● bash ls", "  ⎿ error: exit 2: nope"},
	},
	"file_change with two changes": {
		`{"type":"item.completed","item":{"type":"file_change","changes":[{"path":"a.go"},{"path":"b.go"}]}}`,
		[]string{"● edit a.go", "● edit b.go"},
	},
	"file_change with no changes": {
		`{"type":"item.completed","item":{"type":"file_change","changes":[]}}`,
		[]string{"[file_change]"},
	},
	"collab_tool_call spawn_agent": {
		`{"type":"item.completed","item":{"type":"collab_tool_call","tool":"spawn_agent","prompt":"go do it"}}`,
		[]string{"● spawn_agent go do it"},
	},
	"collab_tool_call wait with a completed state": {
		`{"type":"item.completed","item":{"type":"collab_tool_call","tool":"wait","agents_states":{"a1":{"status":"completed","message":"done"}}}}`,
		[]string{"  ⎿ ok: done"},
	},
	"collab_tool_call wait with no completed states": {
		`{"type":"item.completed","item":{"type":"collab_tool_call","tool":"wait","agents_states":{"a1":{"status":"pending_init","message":null}}}}`,
		[]string{"[collab_tool_call wait]"},
	},
	"collab_tool_call other tool": {
		`{"type":"item.completed","item":{"type":"collab_tool_call","tool":"frobnicate"}}`,
		[]string{"[collab_tool_call frobnicate]"},
	},
	"item.completed unknown item type": {
		`{"type":"item.completed","item":{"type":"foo"}}`,
		[]string{"[foo]"},
	},
}

func TestCodexTable(t *testing.T) {
	for name, c := range codexCases {
		t.Run(name, func(t *testing.T) {
			if got := Render("codex", []byte(c.line)); !reflect.DeepEqual(got, c.want) {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}
