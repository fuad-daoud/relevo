package store

import (
	"os"
	"testing"
)

const promptPathRound = 1

// TestPromptPathResolvesTheRoundPrompt pins PromptPath's resolution rule: the
// new name wins when present, the pre-rename name is the fallback, and a fresh
// round gets the new name.
func TestPromptPathResolvesTheRoundPrompt(t *testing.T) {
	body := []byte("prompt data\n")
	write := func(t *testing.T, path string) {
		t.Helper()
		if err := os.WriteFile(path, body, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	seal := func(t *testing.T, s *Store, binding, path string) {
		t.Helper()
		if err := s.WithLock(func(tx *Tx) error {
			return tx.PutRoundFile(binding, promptPathRound, path, body)
		}); err != nil {
			t.Fatal(err)
		}
	}
	newName := func(s *Store, binding string) string {
		return s.roundFile(binding, promptPathRound, "prompt", ".md")
	}
	oldName := func(s *Store, binding string) string {
		return s.roundFile(binding, promptPathRound, "plan", ".md")
	}

	cases := []struct {
		name    string
		setup   func(t *testing.T, s *Store, binding string)
		wantNew bool
	}{
		{"neither name exists returns the new name", func(*testing.T, *Store, string) {}, true},
		{"new name on disk returns the new path", func(t *testing.T, s *Store, b string) {
			write(t, newName(s, b))
		}, true},
		{"only old name on disk returns the old path", func(t *testing.T, s *Store, b string) {
			write(t, oldName(s, b))
		}, false},
		{"only new name as a sealed row returns the new path", func(t *testing.T, s *Store, b string) {
			seal(t, s, b, newName(s, b))
		}, true},
		{"only old name as a sealed row returns the old path", func(t *testing.T, s *Store, b string) {
			seal(t, s, b, oldName(s, b))
		}, false},
		{"both on disk returns the new path", func(t *testing.T, s *Store, b string) {
			write(t, newName(s, b))
			write(t, oldName(s, b))
		}, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, binding := seedBinding(t)
			tc.setup(t, s, binding)
			want := oldName(s, binding)
			if tc.wantNew {
				want = newName(s, binding)
			}
			if got := s.PromptPath(binding, promptPathRound); got != want {
				t.Errorf("PromptPath = %q, want %q", got, want)
			}
		})
	}
}

// TestOutputPathIsTheLabelUnderTheArtifactDir pins OutputPath's shape: the
// label's .md inside the actor's round artifact directory.
func TestOutputPathIsTheLabelUnderTheArtifactDir(t *testing.T) {
	s := New("/state")
	cases := []struct {
		name               string
		round              int
		actor, label, want string
	}{
		{"reviewer findings", 3, "reviewer", "findings", "/state/webshop/out/003-reviewer/findings.md"},
		{"planner plan", 12, "planner", "plan", "/state/webshop/out/012-planner/plan.md"},
	}
	for _, tc := range cases {
		if got := s.OutputPath("webshop", tc.round, tc.actor, tc.label); got != tc.want {
			t.Errorf("%s: OutputPath = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestIsPromptKindMatchesBothSpellings pins the predicate every reader asks:
// the value new entries write and the value already written.
func TestIsPromptKindMatchesBothSpellings(t *testing.T) {
	for _, k := range []Kind{KindPrompt, Kind("plan")} {
		if !IsPromptKind(k) {
			t.Errorf("IsPromptKind(%q) = false, want true", k)
		}
	}
	for _, k := range []Kind{KindReport, KindQuestion, KindAnswer, KindDiff, ""} {
		if IsPromptKind(k) {
			t.Errorf("IsPromptKind(%q) = true, want false", k)
		}
	}
}
