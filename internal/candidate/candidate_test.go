package candidate

import (
	"errors"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
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

// TestParseRefusesAnAtSignInTheProvider pins the gate-key separator rule: a
// provider carrying "@" could not be read back as a group, so it is refused.
func TestParseRefusesAnAtSignInTheProvider(t *testing.T) {
	_, _, err := Parse("candidates.json", []byte(`[{"harness":"claude","provider":"anthropic@spare","model":"sonnet"}]`))
	if err == nil {
		t.Fatal("Parse: got nil error, want a refusal for \"@\" in the provider")
	}
	if !strings.Contains(err.Error(), `provider must not contain "@"`) {
		t.Errorf("Parse error %q does not name the refusal", err.Error())
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
		{
			name:          "invalid contributor_mode",
			body:          `[{"harness":"muse","provider":"meta","model":"m","roles":["builder"],"contributor_mode":"yes"}]`,
			wantSubstring: `contributor_mode "yes"`,
		},
		{
			name:          "contributor_mode on claude",
			body:          `[{"harness":"claude","provider":"anthropic","model":"m","roles":["builder"],"contributor_mode":"on"}]`,
			wantSubstring: `contributor_mode is only valid for harness "muse"`,
		},
		{
			name:          "contributor_mode on agy",
			body:          `[{"harness":"agy","provider":"google","model":"m","roles":["builder"],"contributor_mode":"off"}]`,
			wantSubstring: `contributor_mode is only valid for harness "muse"`,
		},
		{
			name:          "contributor_mode on codex",
			body:          `[{"harness":"codex","provider":"openai","model":"m","roles":["builder"],"contributor_mode":"on"}]`,
			wantSubstring: `contributor_mode is only valid for harness "muse"`,
		},
		{
			name:          "contributor_mode on opencode",
			body:          `[{"harness":"opencode","provider":"test","model":"m","roles":["builder"],"contributor_mode":"on"}]`,
			wantSubstring: `contributor_mode is only valid for harness "muse"`,
		},
		{
			name:          "invalid extra_args contributor-mode",
			body:          `[{"harness":"muse","provider":"meta","model":"m","roles":["builder"],"extra_args":["--contributor-mode","maybe"]}]`,
			wantSubstring: "contributor_mode",
		},
		{
			name:          "bare extra_args contributor-mode without a value",
			body:          `[{"harness":"muse","provider":"meta","model":"m","roles":["builder"],"extra_args":["--contributor-mode"]}]`,
			wantSubstring: "contributor_mode",
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

// TestLoadContributorModeValues pins the typed field's vocabulary: on and off
// load as written, and a missing key loads as unset without touching anything
// else, so old candidates files behave exactly as today.
func TestLoadContributorModeValues(t *testing.T) {
	tests := []struct {
		name string
		body string
		ref  Ref
		want string
	}{
		{
			name: "on",
			body: `[{"harness":"muse","provider":"meta","model":"m","roles":["builder"],"contributor_mode":"on"}]`,
			ref:  Ref{"muse", "meta", "m"},
			want: "on",
		},
		{
			name: "off",
			body: `[{"harness":"muse","provider":"meta","model":"m","roles":["builder"],"contributor_mode":"off"}]`,
			ref:  Ref{"muse", "meta", "m"},
			want: "off",
		},
		{
			name: "unset",
			body: `[{"harness":"muse","provider":"meta","model":"m","roles":["builder"]}]`,
			ref:  Ref{"muse", "meta", "m"},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := writeCandidates(t, tt.body).Lookup(tt.ref)
			if err != nil {
				t.Fatalf("Lookup(%s): %v", tt.ref, err)
			}
			if c.ContributorMode != tt.want {
				t.Errorf("ContributorMode = %q, want %q", c.ContributorMode, tt.want)
			}
		})
	}
}

// TestParseMigratesExtraArgsContributorMode pins the load-time migration of a
// hand-written --contributor-mode in a muse candidate's extra_args: the value
// becomes the typed field, the flag leaves the effective extra_args, and a
// warning names the migration. The typed field wins when both are set.
func TestParseMigratesExtraArgsContributorMode(t *testing.T) {
	const ref = "muse/meta/m"
	lookup := func(t *testing.T, set *Set) Candidate {
		t.Helper()
		r, err := ParseRef(ref)
		if err != nil {
			t.Fatal(err)
		}
		c, err := set.Lookup(r)
		if err != nil {
			t.Fatalf("Lookup(%s): %v", ref, err)
		}
		return c
	}

	t.Run("bare form migrates and warns", func(t *testing.T) {
		set, warnings := loadWarnings(t, `[{"harness":"muse","provider":"meta","model":"m","roles":["builder"],"extra_args":["--contributor-mode","on"]}]`)
		c := lookup(t, set)
		if c.ContributorMode != "on" {
			t.Errorf("ContributorMode = %q, want %q", c.ContributorMode, "on")
		}
		if len(c.ExtraArgs) != 0 {
			t.Errorf("ExtraArgs = %v, want the flag dropped", c.ExtraArgs)
		}
		if !warnsMigration(t, warnings, "migrated") {
			t.Errorf("warnings = %q, want one naming the migration", warnings)
		}
	})

	t.Run("equals form migrates and keeps neighbours", func(t *testing.T) {
		set, warnings := loadWarnings(t, `[{"harness":"muse","provider":"meta","model":"m","roles":["builder"],"extra_args":["--yolo","--contributor-mode=off"]}]`)
		c := lookup(t, set)
		if c.ContributorMode != "off" {
			t.Errorf("ContributorMode = %q, want %q", c.ContributorMode, "off")
		}
		if want := []string{"--yolo"}; !reflect.DeepEqual(c.ExtraArgs, want) {
			t.Errorf("ExtraArgs = %v, want %v", c.ExtraArgs, want)
		}
		if !warnsMigration(t, warnings, "migrated") {
			t.Errorf("warnings = %q, want one naming the migration", warnings)
		}
	})

	t.Run("typed field wins over extra_args", func(t *testing.T) {
		set, warnings := loadWarnings(t, `[{"harness":"muse","provider":"meta","model":"m","roles":["builder"],"contributor_mode":"off","extra_args":["--contributor-mode=on"]}]`)
		c := lookup(t, set)
		if c.ContributorMode != "off" {
			t.Errorf("ContributorMode = %q, want the typed field %q", c.ContributorMode, "off")
		}
		if len(c.ExtraArgs) != 0 {
			t.Errorf("ExtraArgs = %v, want the flag dropped", c.ExtraArgs)
		}
		if !warnsMigration(t, warnings, "wins") {
			t.Errorf("warnings = %q, want one saying the field wins", warnings)
		}
	})

	t.Run("invalid extra_args value fails naming the field", func(t *testing.T) {
		_, _, err := Parse("candidates.json", []byte(`[{"harness":"muse","provider":"meta","model":"m","roles":["builder"],"extra_args":["--contributor-mode=maybe"]}]`))
		if err == nil {
			t.Fatal("Parse succeeded, want a refusal for the invalid value")
		}
		if !strings.Contains(err.Error(), "contributor_mode") {
			t.Errorf("Parse err = %q, want it naming contributor_mode", err.Error())
		}
	})
}

func warnsMigration(t *testing.T, warnings []string, want string) bool {
	t.Helper()
	for _, w := range warnings {
		if strings.Contains(w, "--contributor-mode") && strings.Contains(w, "contributor_mode") && strings.Contains(w, want) {
			return true
		}
	}
	return false
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

// TestResolveIgnoresAnEffortSuffix pins that the "#effort" suffix a builder's
// own settings carry names how hard to try, not a different candidate: a token
// pasted from one resolves to the same candidate as the bare token. Removing
// the strip in Resolve kills the "#high" row.
func TestResolveIgnoresAnEffortSuffix(t *testing.T) {
	set := parseSet(t, oneSetBody)

	for _, tok := range []string{
		"claude/anthropic/sonnet",
		"claude/anthropic/sonnet#high",
		"claude/anthropic/sonnet#max",
		"claude/anthropic/sonnet#",
	} {
		c, err := set.Resolve(tok)
		if err != nil {
			t.Fatalf("Resolve(%q): %v", tok, err)
		}
		if got := c.Ref().String(); got != "claude/anthropic/sonnet" {
			t.Errorf("Resolve(%q) = %q, want claude/anthropic/sonnet", tok, got)
		}
	}
}

// TestStripEffortLeavesTheModelSuffix pins the one thing the strip must not
// touch: ":effort" belongs to the model name itself, so it survives.
func TestStripEffortLeavesTheModelSuffix(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"claude/anthropic/sonnet#high", "claude/anthropic/sonnet"},
		{"claude/anthropic/sonnet:high", "claude/anthropic/sonnet:high"},
		{"claude/anthropic/son", "claude/anthropic/son"},
		{"", ""},
	} {
		if got := StripEffort(c.in); got != c.want {
			t.Errorf("StripEffort(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestResolveKeepsRefusingAnUnknownEffortToken pins that the tolerance is
// about the suffix alone: a token no candidate serves is still refused, and
// the refusal still names what is configured.
func TestResolveKeepsRefusingAnUnknownEffortToken(t *testing.T) {
	set := parseSet(t, oneSetBody)

	if _, err := set.Resolve("claude/anthropic/nope#high"); !errors.Is(err, ErrUnknownCandidate) {
		t.Errorf("Resolve(unknown#high) err = %v, want ErrUnknownCandidate", err)
	}
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

// TestResolveReachesAModelThatItselfEndsInAnEffortSuffix: a configured model
// named "...#high" is looked up exactly before any suffix is stripped. Stripping
// first makes it unreachable, which the exact-match row kills.
func TestResolveReachesAModelThatItselfEndsInAnEffortSuffix(t *testing.T) {
	set := parseSet(t, `[{"harness":"opencode","provider":"cline-pass","model":"cline-pass/deepseek#high"}]`)

	c, err := set.Resolve("opencode/cline-pass/cline-pass/deepseek#high")
	if err != nil {
		t.Fatalf("Resolve exact: %v", err)
	}
	if got := c.Ref().Model; got != "cline-pass/deepseek#high" {
		t.Errorf("model = %q", got)
	}
}

const windowRowBody = `[
	{"name":"qwen","harness":"opencode","provider":"alibaba-tp","model":"qwen3.8-flash","roles":["builder"],
	 "max_concurrency":2,
	 "windows":{"mode":"demote","only":[{"from":"14:00Z","to":"00:00Z"}],"avoid":[{"from":"00:30Z","to":"05:00Z"}]}}
]`

func TestParseWindowsAndConcurrencyDecode(t *testing.T) {
	set := parseSet(t, windowRowBody)
	c, err := set.Resolve("qwen")
	if err != nil {
		t.Fatal(err)
	}
	if c.MaxConcurrency != 2 {
		t.Errorf("MaxConcurrency = %d, want 2", c.MaxConcurrency)
	}
	want := Windows{
		Mode:  "demote",
		Only:  []Window{{From: "14:00Z", To: "00:00Z"}},
		Avoid: []Window{{From: "00:30Z", To: "05:00Z"}},
	}
	if c.Windows == nil || !reflect.DeepEqual(*c.Windows, want) {
		t.Errorf("Windows = %+v, want %+v", c.Windows, want)
	}
}

func TestParseRefusesBadWindow(t *testing.T) {
	row := func(extra string) string {
		return `[{"harness":"claude","provider":"anthropic","model":"sonnet",` + extra + `}]`
	}
	tests := []struct{ name, extra, want string }{
		{"unparseable clock", `"windows":{"only":[{"from":"25:00","to":"01:00"}]}`, "windows.only[0]: from"},
		{"unknown zone", `"windows":{"avoid":[{"from":"01:00 Mars/Base","to":"02:00"}]}`, "windows.avoid[0]: from"},
		{"from equals to", `"windows":{"only":[{"from":"14:00","to":"14:00Z"}]}`, "windows.only[0]: from"},
		{"from equals to across offsets", `"windows":{"only":[{"from":"02:00+14:00","to":"12:00Z"}]}`, "windows.only[0]: from"},
		{"from equals to across offsets reversed", `"windows":{"only":[{"from":"12:00Z","to":"02:00+14:00"}]}`, "windows.only[0]: from"},
		{"from equals to in a named zone judged at the fixed ref", `"windows":{"only":[{"from":"01:00 Asia/Tokyo","to":"16:00Z"}]}`, "windows.only[0]: from"},
		{"unknown mode", `"windows":{"mode":"soft","only":[{"from":"01:00","to":"02:00"}]}`, "windows.mode"},
		{"both lists empty", `"windows":{}`, "windows: only and avoid"},
		{"from Local zone", `"windows":{"only":[{"from":"14:00 Local","to":"15:00Z"}]}`, "windows.only[0]: from"},
		{"signed clock minute", `"windows":{"only":[{"from":"12:+5","to":"15:00Z"}]}`, "windows.only[0]: from"},
		{"signed clock hour", `"windows":{"only":[{"from":"+1:00","to":"15:00Z"}]}`, "windows.only[0]: from"},
		{"signed offset hour", `"windows":{"only":[{"from":"14:00+-100","to":"15:00Z"}]}`, "windows.only[0]: from"},
		{"signed offset minute", `"windows":{"only":[{"from":"14:00+08:-1","to":"15:00Z"}]}`, "windows.only[0]: from"},
		{"both lists bad reports only", `"windows":{"avoid":[{"from":"26:00","to":"01:00"}],"only":[{"from":"25:00","to":"01:00"}]}`, "windows.only[0]: from"},
		{"max_concurrency negative zero", `"max_concurrency":-0`, "max_concurrency 0"},
		{"max_concurrency zero", `"max_concurrency":0`, "max_concurrency 0"},
		{"max_concurrency negative", `"max_concurrency":-1`, "max_concurrency -1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := Parse("candidates.json", []byte(row(tt.extra)))
			if err == nil {
				t.Fatal("Parse succeeded, want a refusal")
			}
			msg := err.Error()
			if !strings.Contains(msg, "candidate 0") || !strings.Contains(msg, tt.want) {
				t.Errorf("err = %q, want candidate index and %q", msg, tt.want)
			}
		})
	}
}

func TestParseBothListsBadIsDeterministic(t *testing.T) {
	data := []byte(`[{"harness":"claude","provider":"anthropic","model":"sonnet","windows":{"avoid":[{"from":"26:00","to":"01:00"}],"only":[{"from":"25:00","to":"01:00"}]}}]`)
	for i := 0; i < 50; i++ {
		_, _, err := Parse("candidates.json", data)
		if err == nil || !strings.Contains(err.Error(), "windows.only[0]") {
			t.Fatalf("run %d: err = %v, want windows.only[0]", i, err)
		}
	}
}

func TestWindowClockFormsAgree(t *testing.T) {
	ref := time.Date(2026, 10, 23, 3, 0, 0, 0, time.UTC)
	want := time.Date(2026, 10, 23, 14, 0, 0, 0, time.UTC)
	for _, s := range []string{"14:00Z", "14:00", "22:00+08:00", "22:00+0800", "17:00 Asia/Amman"} {
		c, err := ParseWindowClock(s)
		if err != nil {
			t.Fatalf("ParseWindowClock(%q): %v", s, err)
		}
		if got := c.At(ref); !got.Equal(want) {
			t.Errorf("%q = %v, want %v", s, got.UTC(), want)
		}
	}
}

func TestWindowCoversAcrossMidnight(t *testing.T) {
	w := Window{From: "14:00Z", To: "00:00Z"}
	for _, tt := range []struct {
		at   string
		want bool
	}{{"13:59", false}, {"14:00", true}, {"23:59", true}, {"00:00", false}} {
		now, _ := time.Parse("15:04", tt.at)
		now = now.AddDate(2026, 9, 22)
		if got := w.Covers(now); got != tt.want {
			t.Errorf("Covers(%sZ) = %v, want %v", tt.at, got, tt.want)
		}
	}
	ws := Windows{Only: []Window{w}}
	now := time.Date(2026, 10, 23, 15, 0, 0, 0, time.UTC)
	edge, ok := ws.NextEdge(now)
	if want := time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC); !ok || !edge.Equal(want) {
		t.Errorf("NextEdge = %v %v, want %v", edge, ok, want)
	}
}

func TestParseWarnsOnUnknownCandidateKey(t *testing.T) {
	_, warnings, err := Parse("candidates.json", []byte(`[{"harness":"claude","provider":"anthropic","model":"sonnet","roles":["builder"],"maxx":2}]`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	var found bool
	for _, w := range warnings {
		if strings.Contains(w, `unknown key "0.maxx"`) {
			found = true
		}
	}
	if !found {
		t.Errorf("warnings = %q, want one naming 0.maxx", warnings)
	}
}

func TestUnknownKeysNamesThePath(t *testing.T) {
	body := []byte(`[{"name":"a","harness":"claude","provider":"p","model":"m","maxx":1,"windows":{"onlyy":[],"only":[{"from":"01:00","to":"02:00","x":1}]}}]`)
	got := UnknownKeys(body)
	want := []string{"a.maxx", "a.windows.only[].x", "a.windows.onlyy"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("UnknownKeys = %q, want %q", got, want)
	}
	if k := UnknownKeys([]byte(`[{"harness":"claude","windows":{"only":[{"from":"01:00","to":"02:00"}]}}]`)); len(k) != 0 {
		t.Errorf("UnknownKeys on a known shape = %q, want none", k)
	}
}

func TestWindowsCoversOnlyAndAvoidTogether(t *testing.T) {
	at := func(hm string) time.Time {
		t.Helper()
		c, err := ParseWindowClock(hm + "Z")
		if err != nil {
			t.Fatal(err)
		}
		return c.At(time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC))
	}
	night := Windows{
		Only:  []Window{{From: "14:00Z", To: "00:00Z"}},
		Avoid: []Window{{From: "22:00Z", To: "23:00Z"}},
	}
	avoidOnly := Windows{Avoid: []Window{{From: "00:30Z", To: "05:00Z"}}}
	for _, tt := range []struct {
		name string
		w    Windows
		at   string
		want bool
	}{
		{"inside only", night, "15:00", true},
		{"inside only but inside avoid", night, "22:30", false},
		{"outside only", night, "10:00", false},
		{"inside avoid with no only", avoidOnly, "01:00", false},
		{"outside avoid with no only", avoidOnly, "06:00", true},
	} {
		if got := tt.w.Covers(at(tt.at)); got != tt.want {
			t.Errorf("%s: Covers(%sZ) = %v, want %v", tt.name, tt.at, got, tt.want)
		}
	}
}

func TestParseWindowClockRefusesABadOffset(t *testing.T) {
	for _, s := range []string{"14:00+1", "14:00+15:00", "14:00+05:60", "14:00+ab:cd", "14:00+-1:00"} {
		if _, err := ParseWindowClock(s); err == nil || !strings.Contains(err.Error(), "bad offset") {
			t.Errorf("ParseWindowClock(%q) err = %v, want a bad offset", s, err)
		}
	}
	c, err := ParseWindowClock("11:00-03:00")
	if err != nil {
		t.Fatalf("a negative offset: %v", err)
	}
	if got, want := c.At(time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)).UTC().Hour(), 14; got != want {
		t.Errorf("11:00-03:00 = %d:00Z, want %d:00Z", got, want)
	}
}

func TestKnownFieldsListsEachTopLevelKeyOnce(t *testing.T) {
	got := KnownFields()
	if !sort.StringsAreSorted(got) {
		t.Errorf("KnownFields = %v, want sorted", got)
	}
	seen := map[string]bool{}
	for _, k := range got {
		if seen[k] || strings.ContainsAny(k, ".[{") {
			t.Errorf("KnownFields has %q twice or as a nested path: %v", k, got)
		}
		seen[k] = true
	}
	for _, want := range []string{"windows", "max_concurrency", "model", "harness"} {
		if !seen[want] {
			t.Errorf("KnownFields = %v, lacks %q", got, want)
		}
	}
}

func TestParseWindowClockRefusesAMalformedClock(t *testing.T) {
	for _, s := range []string{"14:00 ", "14:00  Asia/Amman", "1:00", "14:00X", "14h00", "14:00 Local"} {
		if _, err := ParseWindowClock(s); err == nil {
			t.Errorf("ParseWindowClock(%q) = nil error, want a refusal", s)
		}
	}
}

func TestAWindowThatDoesNotParseCoversNothingAndHasNoEdge(t *testing.T) {
	bad := Window{From: "14:00Z", To: "25:00Z"}
	now := time.Date(2026, 10, 23, 15, 0, 0, 0, time.UTC)
	if bad.Covers(now) {
		t.Error("a window with a bad to covers now, want false")
	}
	if _, ok := (Windows{Only: []Window{bad}}).NextEdge(now); ok {
		t.Error("NextEdge found an edge for a window that does not parse")
	}
}

func TestNextEdgeOutsideAWindowIsItsNextOpening(t *testing.T) {
	ws := Windows{Avoid: []Window{{From: "00:30Z", To: "05:00Z"}}}
	now := time.Date(2026, 10, 23, 6, 0, 0, 0, time.UTC)
	edge, ok := ws.NextEdge(now)
	if want := time.Date(2026, 10, 24, 0, 30, 0, 0, time.UTC); !ok || !edge.Equal(want) {
		t.Errorf("NextEdge = %v %v, want %v", edge, ok, want)
	}
}

func TestUnknownKeysOfABodyThatIsNotAListIsNone(t *testing.T) {
	if got := UnknownKeys([]byte(`{"name":"x"}`)); got != nil {
		t.Errorf("UnknownKeys(object) = %v, want nil", got)
	}
}
