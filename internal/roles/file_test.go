package roles

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// writeRoles writes body to a temporary roles.json and returns its path.
func writeRoles(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "roles.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// fileFromJSON writes body to a temporary roles.json and loads it, failing on
// any error.
func fileFromJSON(t *testing.T, body string) *File {
	t.Helper()
	f, err := Load(writeRoles(t, body))
	if err != nil {
		t.Fatalf("Load(%s): %v", body, err)
	}
	return f
}

func TestLoadMissingFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "roles.json")

	f, warnings, err := LoadWithWarnings(missing)
	if err != nil || f != nil || warnings != nil {
		t.Fatalf("LoadWithWarnings(%q) = (%v, %v, %v), want (nil, nil, nil)", missing, f, warnings, err)
	}

	f2, err := Load(missing)
	if err != nil || f2 != nil {
		t.Fatalf("Load(%q) = (%v, %v), want (nil, nil)", missing, f2, err)
	}
}

// specExample is an example roles.json.
const specExample = `{
  "builder": {
    "definitions": {
      "claude": { "agent": "my-executor", "requires": ["my-scout"] }
    },
    "candidates": [
      "opencode/cline-pass/cline-pass/deepseek-v4.1-flash#high",
      "codex/openai/gpt-5.6-terra:high",
      "claude/anthropic/sonnet"
    ],
    "tier": "yolo"
  },
  "reviewer": {
    "candidates": ["claude/anthropic/sonnet", "codex/openai/gpt-5.6-terra:high"],
    "tier": "yolo"
  },
  "security-reviewer": {
    "shape": "reader",
    "definitions": { "claude": { "agent": "sec-review" } },
    "candidates": ["claude/anthropic/sonnet"]
  }
}`

// TestLoadSpecExample pins that the example parses with no warnings:
// every key in it is a key this relevo knows.
func TestLoadSpecExample(t *testing.T) {
	f, warnings, err := LoadWithWarnings(writeRoles(t, specExample))
	if err != nil {
		t.Fatalf("LoadWithWarnings: %v", err)
	}
	if len(warnings) != 0 {
		t.Errorf("warnings = %v, want none", warnings)
	}
	if len(f.Rows) != 3 {
		t.Fatalf("Rows has %d entries, want 3", len(f.Rows))
	}
	row, ok := f.Rows["security-reviewer"]
	if !ok {
		t.Fatal("Rows is missing security-reviewer")
	}
	if row.Shape == nil || *row.Shape != "reader" {
		t.Errorf("security-reviewer shape = %v, want \"reader\"", row.Shape)
	}
	if row.Tier != nil {
		t.Errorf("security-reviewer tier = %v, want nil", row.Tier)
	}
}

// TestLoadValidation gives one case per validation rule; every case must wrap
// ErrBadRoles and name the row and field it rejected.
func TestLoadValidation(t *testing.T) {
	tests := []struct {
		name          string
		body          string
		wantSubstring string
	}{
		{
			name:          "bad role name",
			body:          `{"Builder": {"shape": "reader"}}`,
			wantSubstring: "Builder: bad role name",
		},
		{
			name:          "built-in shape mismatch",
			body:          `{"builder": {"shape": "reader"}}`,
			wantSubstring: "builder.shape: built-in role is writer",
		},
		{
			name:          "new role shape required",
			body:          `{"my-role": {}}`,
			wantSubstring: "my-role.shape: required for a new role",
		},
		{
			name:          "new role shape unknown word",
			body:          `{"my-role": {"shape": "sideways"}}`,
			wantSubstring: "my-role.shape: must be writer or reader",
		},
		{
			name:          "built-in shape unknown word",
			body:          `{"reviewer": {"shape": "sideways"}}`,
			wantSubstring: "reviewer.shape: must be writer or reader",
		},
		{
			name:          "gate on a reader",
			body:          `{"reviewer": {"gate": true}}`,
			wantSubstring: "reviewer.gate: a reader role has no gate",
		},
		{
			name:          "unknown harness",
			body:          `{"reviewer": {"definitions": {"nope": {"agent": "x"}}}}`,
			wantSubstring: `reviewer.definitions.nope: unknown harness "nope"`,
		},
		{
			name:          "agent required",
			body:          `{"reviewer": {"definitions": {"claude": {}}}}`,
			wantSubstring: "reviewer.definitions.claude.agent: required",
		},
		{
			name:          "bad agent name",
			body:          `{"reviewer": {"definitions": {"claude": {"agent": "MyExecutor"}}}}`,
			wantSubstring: `reviewer.definitions.claude.agent: bad name "MyExecutor"`,
		},
		{
			name:          "bad requires name",
			body:          `{"reviewer": {"definitions": {"claude": {"agent": "x", "requires": ["Bad"]}}}}`,
			wantSubstring: `reviewer.definitions.claude.requires[0]: bad name "Bad"`,
		},
		{
			name:          "bad candidate entry",
			body:          `{"builder": {"candidates": ["A/b"]}}`,
			wantSubstring: `builder.candidates[0]: "A/b": want a candidate name or harness/provider/model`,
		},
		{
			name:          "duplicate candidate",
			body:          `{"builder": {"candidates": ["claude/anthropic/sonnet", "claude/anthropic/sonnet"]}}`,
			wantSubstring: `builder.candidates[1]: duplicate token "claude/anthropic/sonnet"`,
		},
		{
			name:          "off entry not a candidate",
			body:          `{"builder": {"candidates": ["claude/anthropic/sonnet"], "off": ["codex/openai/gpt-5.6-terra:high"]}}`,
			wantSubstring: `builder.off[0]: "codex/openai/gpt-5.6-terra:high" is not in candidates`,
		},
		{
			name:          "bad tier",
			body:          `{"builder": {"tier": "god"}}`,
			wantSubstring: "builder.tier: unknown tier",
		},
		{
			name:          "not json",
			body:          `not json`,
			wantSubstring: "bad roles",
		},
		{
			name:          "top level array",
			body:          `[]`,
			wantSubstring: "bad roles",
		},
		{
			name:          "top level null",
			body:          `null`,
			wantSubstring: "top-level value must be an object",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(writeRoles(t, tt.body))
			if err == nil {
				t.Fatalf("Load(%s) = nil error, want one containing %q", tt.body, tt.wantSubstring)
			}
			if !errors.Is(err, ErrBadRoles) {
				t.Errorf("Load(%s) err = %v, want ErrBadRoles", tt.body, err)
			}
			if !strings.Contains(err.Error(), tt.wantSubstring) {
				t.Errorf("Load(%s) err = %q, want substring %q", tt.body, err.Error(), tt.wantSubstring)
			}
		})
	}
}

// TestLoadUnknownKeyWarnings pins for roles.json that a key this relevo
// does not know is kept and warned about, never refused, and the warning names
// the row path exactly as policy's does.
func TestLoadUnknownKeyWarnings(t *testing.T) {
	body := `{
	  "builder": {
	    "colour": "red",
	    "definitions": {"claude": {"agent": "plan-executor", "model": "x"}}
	  }
	}`
	f, warnings, err := LoadWithWarnings(writeRoles(t, body))
	if err != nil {
		t.Fatalf("LoadWithWarnings: %v", err)
	}
	if f == nil {
		t.Fatal("LoadWithWarnings returned a nil File")
	}

	want := []string{
		`roles.json: unknown key "builder.colour" (a typo, or a key a newer relevo reads)`,
		`roles.json: unknown key "builder.definitions.claude.model" (a typo, or a key a newer relevo reads)`,
	}
	if !reflect.DeepEqual(warnings, want) {
		t.Fatalf("warnings = %q, want %q", warnings, want)
	}
}

// TestLoadWarningsSurviveValidationFailure pins that warnings are returned
// even when validation then fails, as policy.LoadWithWarnings does, and
// that a File which failed a rule is never handed out.
func TestLoadWarningsSurviveValidationFailure(t *testing.T) {
	body := `{"my-role": {"colour": "red"}}`

	f, warnings, err := LoadWithWarnings(writeRoles(t, body))
	if err == nil {
		t.Fatal("LoadWithWarnings = nil error, want the missing-shape error")
	}
	if !errors.Is(err, ErrBadRoles) {
		t.Errorf("err = %v, want ErrBadRoles", err)
	}
	if f != nil {
		t.Errorf("File = %+v, want nil on a validation failure", f)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], `unknown key "my-role.colour"`) {
		t.Errorf("warnings = %q, want the unknown-key warning even though validation failed", warnings)
	}
}

func TestLoadReturnsSameFileAsLoadWithWarnings(t *testing.T) {
	path := writeRoles(t, specExample)

	viaWarnings, _, err := LoadWithWarnings(path)
	if err != nil {
		t.Fatalf("LoadWithWarnings: %v", err)
	}
	viaLoad, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(viaLoad, viaWarnings) {
		t.Errorf("Load() = %+v, LoadWithWarnings() = %+v, want the same File", viaLoad, viaWarnings)
	}
}

// TestLoadBuiltinShapeOverride pins the built-in shape rule: a row may repeat
// the built-in shape, and may not contradict it.
func TestLoadBuiltinShapeOverride(t *testing.T) {
	if _, err := Load(writeRoles(t, `{"reviewer": {"shape": "reader"}}`)); err != nil {
		t.Errorf("Load(shape reader on reviewer) = %v, want nil", err)
	}

	_, err := Load(writeRoles(t, `{"reviewer": {"shape": "writer"}}`))
	if err == nil {
		t.Fatal("Load(shape writer on reviewer) = nil, want an error")
	}
	if !errors.Is(err, ErrBadRoles) {
		t.Errorf("err = %v, want ErrBadRoles", err)
	}
	if !strings.Contains(err.Error(), "reviewer.shape: built-in role is reader") {
		t.Errorf("err = %q, want the built-in shape named", err.Error())
	}
}

// TestLoadNewWriterAccepted pins that a new writer row loads with its shape kept.
func TestLoadNewWriterAccepted(t *testing.T) {
	f, err := Load(writeRoles(t, `{"my-writer": {"shape": "writer"}}`))
	if err != nil {
		t.Fatalf("Load(new writer row) = %v, want nil", err)
	}
	row, ok := f.Rows["my-writer"]
	if !ok {
		t.Fatal("Rows is missing my-writer")
	}
	if row.Shape == nil || *row.Shape != "writer" {
		t.Errorf("my-writer shape = %v, want \"writer\"", row.Shape)
	}
}

// TestLoadAcceptsNameCandidates pins the validation rule for roles.json:
// a candidate name, even one that is not a token, validates.
func TestLoadAcceptsNameCandidates(t *testing.T) {
	for _, body := range []string{
		`{"builder": {"candidates": ["sonnet"]}}`,
		`{"builder": {"candidates": ["a"]}}`,
	} {
		if _, err := Load(writeRoles(t, body)); err != nil {
			t.Errorf("Load(%s) = %v, want no error", body, err)
		}
	}
}
