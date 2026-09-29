package candidate

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRefRoundTrip(t *testing.T) {
	tests := []struct {
		input   string
		wantRef Ref
		wantErr error
	}{
		{"claude/anthropic/sonnet", Ref{"claude", "anthropic", "sonnet"}, nil},
		{"opencode/openrouter/z-ai/glm-5.3-flash", Ref{"opencode", "openrouter", "z-ai/glm-5.3-flash"}, nil},
		{"agy/google/gemini-3.8-flash-high", Ref{"agy", "google", "gemini-3.8-flash-high"}, nil},
		{"claude/anthropic", Ref{}, ErrBadRef},
		{"claude//sonnet", Ref{}, ErrBadRef},
		{"/anthropic/sonnet", Ref{}, ErrBadRef},
		{"claude/anthropic/", Ref{}, ErrBadRef},
		{"", Ref{}, ErrBadRef},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got, err := ParseRef(tt.input)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("ParseRef(%q) err = %v, want %v", tt.input, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseRef(%q) unexpected err: %v", tt.input, err)
			}
			if got != tt.wantRef {
				t.Errorf("ParseRef(%q) = %+v, want %+v", tt.input, got, tt.wantRef)
			}
			if got.String() != tt.input {
				t.Errorf("Ref.String() = %q, want %q", got.String(), tt.input)
			}
		})
	}
}

func TestSetQueries(t *testing.T) {
	set := writeCandidates(t, threeSetBody)

	if set.Len() != 3 {
		t.Errorf("Len() = %d, want 3", set.Len())
	}

	wantRefs := []string{
		"agy/google/gemini-3.8-flash-high",
		"claude/anthropic/sonnet",
		"opencode/openrouter/z-ai/glm-5.3-flash",
	}
	if gotRefs := set.Refs(); !reflect.DeepEqual(gotRefs, wantRefs) {
		t.Errorf("Refs() = %v, want %v", gotRefs, wantRefs)
	}

	builders := set.ForRole("builder")
	if len(builders) != 3 {
		t.Fatalf("ForRole(\"builder\") returned %d entries, want 3", len(builders))
	}
	for i, want := range wantRefs {
		if builders[i].Ref().String() != want {
			t.Errorf("ForRole(\"builder\")[%d] = %q, want %q", i, builders[i].Ref().String(), want)
		}
	}

	reviewers := set.ForRole("reviewer")
	if len(reviewers) != 1 {
		t.Fatalf("ForRole(\"reviewer\") returned %d entries, want 1", len(reviewers))
	}
	if reviewers[0].Ref().String() != "claude/anthropic/sonnet" {
		t.Errorf("ForRole(\"reviewer\")[0] = %q, want %q", reviewers[0].Ref().String(), "claude/anthropic/sonnet")
	}

	researchers := set.ForRole("researcher")
	if len(researchers) != 0 {
		t.Errorf("ForRole(\"researcher\") returned %d entries, want 0", len(researchers))
	}
}

func TestLookup(t *testing.T) {
	set := writeCandidates(t, threeSetBody)

	claudeSonnet, err := set.Lookup(Ref{"claude", "anthropic", "sonnet"})
	if err != nil {
		t.Errorf("Lookup(claude/anthropic/sonnet) err = %v, want nil", err)
	}
	if claudeSonnet.Model != "sonnet" {
		t.Errorf("Lookup returned model %q, want sonnet", claudeSonnet.Model)
	}

	_, err = set.Lookup(Ref{"claude", "anthropic", "opus"})
	if !errors.Is(err, ErrUnknownCandidate) {
		t.Errorf("Lookup(claude/anthropic/opus) err = %v, want ErrUnknownCandidate", err)
	}
	if !strings.Contains(err.Error(), "configured:") {
		t.Errorf("Lookup error %q does not contain \"configured:\"", err.Error())
	}
}

func TestLoadMissingFileIsEmpty(t *testing.T) {
	missingPath := filepath.Join(t.TempDir(), "none.json")
	set, err := Load(missingPath)
	if err != nil {
		t.Fatalf("Load(%q) unexpected err: %v", missingPath, err)
	}
	if set == nil {
		t.Fatal("Load() returned nil Set")
	}
	if set.Len() != 0 {
		t.Errorf("Len() = %d, want 0", set.Len())
	}
}

// CanServe false is unreachable with the shipped harness table, so it is not pinned here.
func TestLoadValidation(t *testing.T) {
	tests := []struct {
		name          string
		body          string
		wantSubstring string
	}{
		{
			name:          "missing model",
			body:          `[{"harness":"claude","provider":"anthropic","roles":["builder"]}]`,
			wantSubstring: "candidate 0: harness, provider and model are required",
		},
		{
			name:          "multi-segment provider",
			body:          `[{"harness":"claude","provider":"a/b","model":"m","roles":["builder"]}]`,
			wantSubstring: "provider must be a single segment",
		},
		{
			name:          "invalid tree value",
			body:          `[{"harness":"claude","provider":"p","model":"m","roles":["builder"],"tree":"sideways"}]`,
			wantSubstring: `tree must be "binding" or "none"`,
		},
		{
			name:          "duplicate candidate",
			body:          `[{"harness":"claude","provider":"p","model":"m","roles":["builder"]},{"harness":"claude","provider":"p","model":"m","roles":["reviewer"]}]`,
			wantSubstring: "duplicate candidate claude/p/m at index 0 and 1",
		},
		{
			name:          "not json",
			body:          `not json`,
			wantSubstring: "decode candidates",
		},
		{
			name:          "invalid limit pattern regex",
			body:          `[{"harness":"claude","provider":"anthropic","model":"m","roles":["builder"],"limit_patterns":["(unclosed"]}]`,
			wantSubstring: "limit_patterns[0]",
		},
		{
			name:          "invalid dialog pattern regex",
			body:          `[{"harness":"claude","provider":"anthropic","model":"m","roles":["builder"],"dialog_patterns":["("]}]`,
			wantSubstring: "dialog_patterns[0]",
		},
		{
			name:          "invalid tier",
			body:          `[{"harness":"claude","provider":"anthropic","model":"m","roles":["builder"],"tier":"god"}]`,
			wantSubstring: "candidate 0: tier:",
		},
		{
			name:          "invalid denial pattern regex",
			body:          `[{"harness":"claude","provider":"anthropic","model":"m","roles":["builder"],"denial_patterns":["["]}]`,
			wantSubstring: "denial_patterns[0]",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(writeTemp(t, tt.body))
			if err == nil {
				t.Fatalf("Load() expected error containing %q, got nil", tt.wantSubstring)
			}
			if !strings.Contains(err.Error(), tt.wantSubstring) {
				t.Errorf("Load() error = %q, want substring %q", err.Error(), tt.wantSubstring)
			}
		})
	}
}

func TestLoadAcceptsEmptyRoles(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "empty roles array", body: `[{"harness":"claude","provider":"p","model":"m","roles":[]}]`},
		{name: "omitted roles", body: `[{"harness":"claude","provider":"p","model":"m"}]`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			set := writeCandidates(t, tt.body)
			if set.Len() != 1 {
				t.Fatalf("Len() = %d, want 1", set.Len())
			}
			ref, err := ParseRef("claude/p/m")
			if err != nil {
				t.Fatal(err)
			}
			c, err := set.Lookup(ref)
			if err != nil {
				t.Fatalf("Lookup: %v", err)
			}
			if c.Serves("builder") {
				t.Error(`Serves("builder") = true, want false: the candidate lists no roles`)
			}
		})
	}
}

func TestLoadSkipsUnknownHarnessAndRole(t *testing.T) {
	t.Run("unknown harness", func(t *testing.T) {
		set, warnings := loadWarnings(t, `[
			{"harness":"nope","provider":"p","model":"m","roles":["builder"]},
			{"harness":"claude","provider":"anthropic","model":"m","roles":["builder"]}
		]`)
		if set.Len() != 1 || len(set.Refs()) != 1 || set.Refs()[0] != "claude/anthropic/m" {
			t.Fatalf("set = %v, want only claude/anthropic/m", set.Refs())
		}
		if len(warnings) != 1 || !strings.Contains(warnings[0], `unknown harness "nope" (skipped)`) {
			t.Fatalf("warnings = %v, want one naming the unknown harness", warnings)
		}
	})

	t.Run("unknown role", func(t *testing.T) {
		set, warnings := loadWarnings(t, `[
			{"harness":"claude","provider":"p","model":"m","roles":["reviwer"]},
			{"harness":"claude","provider":"anthropic","model":"m","roles":["builder"]}
		]`)
		if set.Len() != 1 || len(set.Refs()) != 1 || set.Refs()[0] != "claude/anthropic/m" {
			t.Fatalf("set = %v, want only claude/anthropic/m", set.Refs())
		}
		if len(warnings) != 1 || !strings.Contains(warnings[0], `unknown role "reviwer" (skipped)`) {
			t.Fatalf("warnings = %v, want one naming the unknown role", warnings)
		}
	})

	t.Run("a duplicate still fails", func(t *testing.T) {
		body := `[{"harness":"claude","provider":"p","model":"m","roles":["builder"]},{"harness":"claude","provider":"p","model":"m","roles":["reviewer"]}]`
		if _, _, err := LoadWithWarnings(writeTemp(t, body)); err == nil || !strings.Contains(err.Error(), "duplicate candidate") {
			t.Fatalf("LoadWithWarnings err = %v, want a duplicate error", err)
		}
	})
}

func TestLoadFields(t *testing.T) {
	tests := []struct {
		name  string
		body  string
		ref   Ref
		check func(t *testing.T, c Candidate)
	}{
		{
			name: "extra_args and tree",
			body: `[{"harness":"agy","provider":"google","model":"gemini-3.8-flash-high","roles":["builder"],"tree":"binding","extra_args":["--dangerously-skip-permissions"]}]`,
			ref:  Ref{"agy", "google", "gemini-3.8-flash-high"},
			check: func(t *testing.T, c Candidate) {
				if want := []string{"--dangerously-skip-permissions"}; !reflect.DeepEqual(c.ExtraArgs, want) {
					t.Errorf("ExtraArgs = %v, want %v", c.ExtraArgs, want)
				}
				if c.Tree != "binding" {
					t.Errorf("Tree = %q, want \"binding\"", c.Tree)
				}
			},
		},
		{
			name: "limit_patterns",
			body: `[{"harness":"agy","provider":"google","model":"gemini-3.8-flash-high","roles":["builder"],"limit_patterns":["(?i)quota"]}]`,
			ref:  Ref{"agy", "google", "gemini-3.8-flash-high"},
			check: func(t *testing.T, c Candidate) {
				if want := []string{"(?i)quota"}; !reflect.DeepEqual(c.LimitPatterns, want) {
					t.Errorf("LimitPatterns = %v, want %v", c.LimitPatterns, want)
				}
			},
		},
		{
			name: "dialog_patterns",
			body: `[{"harness":"agy","provider":"google","model":"gemini-3.8-flash-high","roles":["builder"],"dialog_patterns":["(?i)confirm"]}]`,
			ref:  Ref{"agy", "google", "gemini-3.8-flash-high"},
			check: func(t *testing.T, c Candidate) {
				if want := []string{"(?i)confirm"}; !reflect.DeepEqual(c.DialogPatterns, want) {
					t.Errorf("DialogPatterns = %v, want %v", c.DialogPatterns, want)
				}
			},
		},
		{
			name: "tier and denial_patterns",
			body: `[{"harness":"claude","provider":"anthropic","model":"sonnet","roles":["builder"],"tier":"yolo","denial_patterns":["(?i)permission denied"]}]`,
			ref:  Ref{"claude", "anthropic", "sonnet"},
			check: func(t *testing.T, c Candidate) {
				if c.Tier != "yolo" {
					t.Errorf("Tier = %q, want \"yolo\"", c.Tier)
				}
				if want := []string{"(?i)permission denied"}; !reflect.DeepEqual(c.DenialPatterns, want) {
					t.Errorf("DenialPatterns = %v, want %v", c.DenialPatterns, want)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := writeCandidates(t, tt.body).Lookup(tt.ref)
			if err != nil {
				t.Fatalf("Lookup(%s): %v", tt.ref, err)
			}
			tt.check(t, c)
		})
	}
}

func TestLoadPlanFlag(t *testing.T) {
	tests := []struct {
		name string
		body string
		ref  Ref
		want bool
	}{
		{
			name: "loaded",
			body: `[{"harness":"claude","provider":"anthropic","model":"sonnet","roles":["builder"],"plan":true}]`,
			ref:  Ref{"claude", "anthropic", "sonnet"},
			want: true,
		},
		{
			name: "defaults to false",
			body: `[{"harness":"agy","provider":"google","model":"g","roles":["builder"]}]`,
			ref:  Ref{"agy", "google", "g"},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := writeCandidates(t, tt.body).Lookup(tt.ref)
			if err != nil {
				t.Fatalf("Lookup(%s): %v", tt.ref, err)
			}
			if c.Plan != tt.want {
				t.Errorf("Plan = %v, want %v", c.Plan, tt.want)
			}
		})
	}
}

func TestSetProviders(t *testing.T) {
	body := `[
	  {"harness":"opencode","provider":"test","model":"m","roles":["builder"]},
	  {"harness":"claude","provider":"anthropic","model":"sonnet","roles":["builder"]},
	  {"harness":"agy","provider":"zeta","model":"g","roles":["builder"]},
	  {"harness":"claude","provider":"test","model":"sonnet","roles":["reviewer"]}
	]`
	set := writeCandidates(t, body)

	got := set.Providers()
	want := []string{"anthropic", "test", "zeta"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Providers() = %v, want %v", got, want)
	}

	var nilSet *Set
	if got := nilSet.Providers(); got != nil {
		t.Errorf("(*Set)(nil).Providers() = %v, want nil", got)
	}
}

func TestParseFillsNames(t *testing.T) {
	tests := []struct {
		name      string
		body      string
		wantNames []string
		ref       Ref
		wantName  string
	}{
		{
			name:      "derived names are filled",
			body:      twoSetBody,
			wantNames: []string{"gemini-3.8-flash-high", "sonnet"},
			ref:       Ref{"claude", "anthropic", "sonnet"},
			wantName:  "sonnet",
		},
		{
			name:      "an explicit name is kept",
			body:      `[{"name":"haiku","harness":"claude","provider":"anthropic","model":"claude-3-5-haiku"}]`,
			wantNames: []string{"haiku"},
			ref:       Ref{"claude", "anthropic", "claude-3-5-haiku"},
			wantName:  "haiku",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			set := parseSet(t, tt.body)
			if got := set.Names(); !reflect.DeepEqual(got, tt.wantNames) {
				t.Errorf("Names() = %v, want %v", got, tt.wantNames)
			}
			c, err := set.Lookup(tt.ref)
			if err != nil {
				t.Fatalf("Lookup(%s): %v", tt.ref, err)
			}
			if c.Name != tt.wantName {
				t.Errorf("candidate Name = %q, want %q", c.Name, tt.wantName)
			}
		})
	}
}

func TestParseNameErrors(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "bad shape",
			body: `[{"name":"Bad","harness":"claude","provider":"anthropic","model":"sonnet"}]`,
			want: `name "Bad": want ^[a-z0-9][a-z0-9.-]{0,23}$`,
		},
		{
			name: "duplicate name",
			body: `[
				{"name":"x","harness":"claude","provider":"anthropic","model":"a"},
				{"name":"x","harness":"claude","provider":"other","model":"b"}
			]`,
			want: `candidate 1: duplicate name "x" at index 0 and 1`,
		},
		{
			name: "provider clash",
			body: `[
				{"harness":"claude","provider":"anthropic","model":"a"},
				{"name":"anthropic","harness":"claude","provider":"other","model":"b"}
			]`,
			want: `candidate 1: name "anthropic" is also a provider name`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := Parse("candidates.json", []byte(tt.body))
			if err == nil {
				t.Fatalf("Parse succeeded, want an error containing %q", tt.want)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Parse err = %q, want it containing %q", err.Error(), tt.want)
			}
		})
	}
}

func TestParseSkippedEntryKeepsDerivedNames(t *testing.T) {
	body := `[
		{"harness":"nope","provider":"p","model":"m"},
		{"harness":"claude","provider":"anthropic","model":"m"}
	]`
	set, warnings := parseWarnings(t, body)
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want one for the unknown harness", warnings)
	}
	// The skipped entry still reserved "m", so the surviving candidate keeps
	// the name DeriveNames gave it in the full list.
	if got, want := set.Names(), []string{"claude-m"}; !reflect.DeepEqual(got, want) {
		t.Errorf("Names() = %v, want %v", got, want)
	}
}

func TestResolve(t *testing.T) {
	set := parseSet(t, twoSetBody)

	t.Run("by name", func(t *testing.T) {
		c, err := set.Resolve("sonnet")
		if err != nil {
			t.Fatalf("Resolve(sonnet): %v", err)
		}
		if got := c.Ref().String(); got != "claude/anthropic/sonnet" {
			t.Errorf("Resolve(sonnet) = %q, want claude/anthropic/sonnet", got)
		}
	})

	t.Run("by token", func(t *testing.T) {
		c, err := set.Resolve("claude/anthropic/sonnet")
		if err != nil {
			t.Fatalf("Resolve(token): %v", err)
		}
		if got := c.Ref().String(); got != "claude/anthropic/sonnet" {
			t.Errorf("Resolve(token) = %q, want claude/anthropic/sonnet", got)
		}
	})

	t.Run("bad token", func(t *testing.T) {
		if _, err := set.Resolve("claude/anthropic"); !errors.Is(err, ErrBadRef) {
			t.Errorf("Resolve(claude/anthropic) err = %v, want ErrBadRef", err)
		}
	})

	t.Run("unknown", func(t *testing.T) {
		_, err := set.Resolve("nope")
		if !errors.Is(err, ErrUnknownCandidate) {
			t.Fatalf("Resolve(nope) err = %v, want ErrUnknownCandidate", err)
		}
		want := `unknown candidate "nope" (known: gemini-3.8-flash-high, sonnet)`
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Resolve(nope) err = %q, want it containing %q", err.Error(), want)
		}
	})

	t.Run("nil set", func(t *testing.T) {
		var nilSet *Set
		_, err := nilSet.Resolve("sonnet")
		if !errors.Is(err, ErrUnknownCandidate) {
			t.Fatalf("(*Set)(nil).Resolve err = %v, want ErrUnknownCandidate", err)
		}
		if !strings.Contains(err.Error(), "(no candidates configured)") {
			t.Errorf("(*Set)(nil).Resolve err = %q, want it to say no candidates are configured", err.Error())
		}
	})
}

func TestNameOf(t *testing.T) {
	set := parseSet(t, oneSetBody)

	if got := set.NameOf("claude/anthropic/sonnet"); got != "sonnet" {
		t.Errorf("NameOf(known) = %q, want sonnet", got)
	}
	if got := set.NameOf("claude/anthropic/opus"); got != "claude/anthropic/opus" {
		t.Errorf("NameOf(unknown) = %q, want the token back", got)
	}

	var nilSet *Set
	if got := nilSet.NameOf("claude/anthropic/sonnet"); got != "claude/anthropic/sonnet" {
		t.Errorf("(*Set)(nil).NameOf = %q, want the token back", got)
	}
}

func TestNameFor(t *testing.T) {
	set := parseSet(t, oneSetBody)

	name, ok := set.NameFor("claude/anthropic/sonnet")
	if !ok || name != "sonnet" {
		t.Errorf("NameFor(known) = %q, %v, want sonnet, true", name, ok)
	}

	if name, ok := set.NameFor("claude/anthropic/opus"); ok || name != "" {
		t.Errorf("NameFor(unknown) = %q, %v, want \"\", false", name, ok)
	}

	var nilSet *Set
	if name, ok := nilSet.NameFor("claude/anthropic/sonnet"); ok || name != "" {
		t.Errorf("(*Set)(nil).NameFor = %q, %v, want \"\", false", name, ok)
	}
}
