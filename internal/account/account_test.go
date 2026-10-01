package account

import (
	"reflect"
	"strings"
	"testing"
)

func parse(t *testing.T, body string, known map[Kind][]string) (Set, []string) {
	t.Helper()
	set, warnings, err := Parse("accounts", []byte(body), known)
	if err != nil {
		t.Fatalf("Parse(%s): %v", body, err)
	}
	return set, warnings
}

type parseCase struct {
	name string
	body string
	want string // an error substring; "" means the body parses
}

var parseCases = []parseCase{
	{
		name: "claude",
		body: `[{"name":"work","harness":"claude","groups":["anthropic"],"config_dir":"/home/fuad/.claude-work"}]`,
	},
	{
		name: "codex",
		body: `[{"name":"personal","harness":"codex","groups":["openai"],"home":"/home/fuad/.codex-personal"}]`,
	},
	{
		name: "opencode",
		body: `[{"name":"clinepass-2","harness":"opencode","groups":["cline-pass"],"integration":"cline-pass","label":"ClinePass 2"}]`,
	},
	{
		name: "two opencode accounts with different labels",
		body: `[{"name":"cp1","harness":"opencode","groups":["cline-pass"],"integration":"cline-pass","label":"ClinePass"},{"name":"cp2","harness":"opencode","groups":["cline-pass"],"integration":"cline-pass","label":"ClinePass 2"}]`,
	},
	{
		name: "duplicate names",
		body: `[{"name":"work","harness":"claude","groups":["anthropic"],"config_dir":"/a"},{"name":"work","harness":"codex","groups":["openai"],"home":"/b"}]`,
		want: "duplicate name",
	},
	{
		name: "unknown kind",
		body: `[{"name":"gem","harness":"gemini","groups":["google"]}]`,
		want: `unknown harness "gemini"`,
	},
	{
		name: "agy is refused",
		body: `[{"name":"main","harness":"agy","groups":["google"]}]`,
		want: "agy offers no login selector",
	},
	{
		name: "a name with @ is refused",
		body: `[{"name":"claude@work","harness":"claude","groups":["anthropic"],"config_dir":"/a"}]`,
		want: "want ^[a-z0-9][a-z0-9.-]{0,23}$",
	},
	{
		name: "a name starting with a dot is refused",
		body: `[{"name":".work","harness":"claude","groups":["anthropic"],"config_dir":"/a"}]`,
		want: "want ^[a-z0-9][a-z0-9.-]{0,23}$",
	},
	{
		name: "claude without config_dir",
		body: `[{"name":"work","harness":"claude","groups":["anthropic"]}]`,
		want: "config_dir is required",
	},
	{
		name: "claude with a foreign selector",
		body: `[{"name":"work","harness":"claude","groups":["anthropic"],"config_dir":"/a","home":"/b"}]`,
		want: "home, integration and label belong to another kind",
	},
	{
		name: "codex without home",
		body: `[{"name":"personal","harness":"codex","groups":["openai"]}]`,
		want: "home is required",
	},
	{
		name: "codex with a foreign selector",
		body: `[{"name":"personal","harness":"codex","groups":["openai"],"home":"/b","integration":"i"}]`,
		want: "config_dir, integration and label belong to another kind",
	},
	{
		name: "opencode without a label",
		body: `[{"name":"cp","harness":"opencode","groups":["cline-pass"],"integration":"cline-pass"}]`,
		want: "integration and label are required",
	},
	{
		name: "opencode with a foreign selector",
		body: `[{"name":"cp","harness":"opencode","groups":["cline-pass"],"integration":"cline-pass","label":"x","config_dir":"/a"}]`,
		want: "config_dir and home belong to another kind",
	},
	{
		name: "opencode duplicate integration and label",
		body: `[{"name":"cp1","harness":"opencode","groups":["cline-pass"],"integration":"cline-pass","label":"ClinePass"},{"name":"cp2","harness":"opencode","groups":["cline-pass"],"integration":"cline-pass","label":"ClinePass"}]`,
		want: "is already account 0",
	},
}

func TestParseValidation(t *testing.T) {
	for _, tt := range parseCases {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := Parse("accounts", []byte(tt.body), nil)
			if tt.want == "" {
				if err != nil {
					t.Fatalf("Parse: unexpected error %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("Parse: got nil error, want one containing %q", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Parse error %q does not contain %q", err.Error(), tt.want)
			}
		})
	}
}

func TestParseWarnsAboutAGroupNoCandidateUses(t *testing.T) {
	known := map[Kind][]string{Claude: {"anthropic"}}
	body := `[{"name":"work","harness":"claude","groups":["anthropic","anthorpic"],"config_dir":"/a"}]`

	_, warnings := parse(t, body, known)
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want exactly one", warnings)
	}
	if want := `accounts: work: group "anthorpic" matches no claude candidate`; warnings[0] != want {
		t.Errorf("warning = %q, want %q", warnings[0], want)
	}

	if _, warnings := parse(t, body, nil); len(warnings) != 0 {
		t.Errorf("warnings with no candidate data = %v, want none", warnings)
	}
}

func TestPool(t *testing.T) {
	set := Set{
		{Name: "cp1", Harness: OpenCode, Groups: []string{"cline-pass"}},
		{Name: "work", Harness: Claude, Groups: []string{"anthropic"}},
		{Name: "cp2", Harness: OpenCode, Groups: []string{"cline-pass"}},
		{Name: "cp3", Harness: OpenCode, Groups: []string{"other"}},
	}

	var names []string
	for _, a := range set.Pool(OpenCode, "cline-pass") {
		names = append(names, a.Name)
	}
	if !reflect.DeepEqual(names, []string{"cp1", "cp2"}) {
		t.Errorf("Pool(opencode, cline-pass) = %v, want [cp1 cp2] in config order", names)
	}
	if got := set.Pool(Claude, "cline-pass"); len(got) != 0 {
		t.Errorf("Pool(claude, cline-pass) = %v, want none", got)
	}
}

func TestGateKeyRoundTrip(t *testing.T) {
	tests := []struct{ group, accountName string }{
		{"cline-pass", "clinepass-2"},
		{"anthropic", "work"},
		{"cline-pass", ""}, // a bare group entry
	}

	for _, tt := range tests {
		key := GateKey(tt.group, tt.accountName)
		group, accountName, ok := ParseGateKey(key)
		if !ok {
			t.Fatalf("ParseGateKey(%q) = false", key)
		}
		if group != tt.group || accountName != tt.accountName {
			t.Fatalf("ParseGateKey(%q) = %q/%q, want %q/%q", key, group, accountName, tt.group, tt.accountName)
		}
		if again := GateKey(group, accountName); again != key {
			t.Errorf("GateKey round trip of %q = %q", key, again)
		}
	}
}

func TestParseGateKeyRejectsMalformed(t *testing.T) {
	for _, key := range []string{"", "@account", "group@", "g@a@b"} {
		if group, accountName, ok := ParseGateKey(key); ok {
			t.Errorf("ParseGateKey(%q) = %q/%q, true; want false", key, group, accountName)
		}
	}
}

func TestGated(t *testing.T) {
	a := Account{Name: "cp2", Harness: OpenCode, Groups: []string{"cline-pass"}}
	tests := []struct {
		name  string
		gates []string
		want  bool
	}{
		{"no gates", nil, false},
		{"the account", []string{"cline-pass@cp2"}, true},
		{"another account", []string{"cline-pass@cp1"}, false},
		{"the bare group", []string{"cline-pass"}, true},
		{"another group", []string{"google"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Gated(a, tt.gates); got != tt.want {
				t.Errorf("Gated = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSelectFailoverOrder(t *testing.T) {
	pool := []Account{
		{Name: "cp1", Harness: OpenCode, Groups: []string{"cline-pass"}},
		{Name: "cp2", Harness: OpenCode, Groups: []string{"cline-pass"}},
		{Name: "cp3", Harness: OpenCode, Groups: []string{"cline-pass"}},
	}

	t.Run("the first ungated account", func(t *testing.T) {
		got, ok, err := Select(pool, []string{"cline-pass@cp1"}, Failover)
		if err != nil || !ok {
			t.Fatalf("Select = %+v/%v/%v, want cp2/true/nil", got, ok, err)
		}
		if got.Name != "cp2" {
			t.Errorf("Select picked %q, want cp2", got.Name)
		}
	})

	t.Run("a bare group gates the whole pool", func(t *testing.T) {
		if _, ok, err := Select(pool, []string{"cline-pass"}, Failover); err != nil || ok {
			t.Errorf("Select = ok %v, err %v; want false/nil", ok, err)
		}
	})

	t.Run("an empty pool selects nothing", func(t *testing.T) {
		if _, ok, err := Select(nil, nil, Failover); ok || err != nil {
			t.Errorf("Select(nil) = ok %v, err %v; want false/nil", ok, err)
		}
	})
}

func TestSelectRefusesRoundRobinForOpenCode(t *testing.T) {
	opencode := []Account{{Name: "cp1", Harness: OpenCode, Groups: []string{"cline-pass"}}}
	if _, _, err := Select(opencode, nil, RoundRobin); err == nil {
		t.Fatal("Select(round-robin, opencode) = nil error, want a refusal")
	}

	claude := []Account{{Name: "work", Harness: Claude, Groups: []string{"anthropic"}}}
	got, ok, err := Select(claude, nil, RoundRobin)
	if err != nil || !ok || got.Name != "work" {
		t.Fatalf("Select(round-robin, claude) = %+v/%v/%v, want work/true/nil", got, ok, err)
	}
}

func TestSelectRejectsAnUnknownMode(t *testing.T) {
	if _, _, err := Select(nil, nil, Mode("least-recently-limited")); err == nil {
		t.Fatal("Select(unknown mode) = nil error, want one")
	}
}
