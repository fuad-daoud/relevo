package relevo

// A2 round 1's pick tests: off entries are skipped by an omitted token, served
// when named explicitly, refused with ErrAllGated when nothing else is left,
// and shown as "off" in the pick block.

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/availability"
	"github.com/fuad-daoud/relevo/internal/policy"
	"github.com/fuad-daoud/relevo/internal/roles"
)

// offRows builds a claude-only builder row with a off and b on.
func offRows() map[string]roles.Row {
	return map[string]roles.Row{
		"builder": {
			Candidates: []string{"claude/test/a", "claude/test/b"},
			Off:        []string{"claude/test/a"},
		},
	}
}

// TestResolveSkipsOff pins §4.3: with no explicit token the walk passes an off
// entry over exactly as it passes a gated one, and the pick note says "(off)".
func TestResolveSkipsOff(t *testing.T) {
	t.Parallel()

	set := candidateSet(t, rolesRuntimeCandidatesJSON)
	reg := rolesFileRegistry(t, set, policy.Policy{}, offRows())

	res, err := resolveRole(reg, set, nil, "", "builder")
	if err != nil {
		t.Fatalf("resolveRole: %v", err)
	}
	if got := res.Token(); got != "claude/test/b" {
		t.Errorf("picked %q, want claude/test/b", got)
	}
	if res.How != HowOrder || res.Position != 2 {
		t.Errorf("How/Position = %q/%d, want %q/2", res.How, res.Position, HowOrder)
	}
	if len(res.Skipped) != 1 || !res.Skipped[0].Off || res.Skipped[0].Token != "claude/test/a" {
		t.Fatalf("Skipped = %+v, want one off skip for claude/test/a", res.Skipped)
	}

	note := ExplainResolution("builder", res)
	if !strings.Contains(note, "skipped claude/test/a (off)") {
		t.Errorf("ExplainResolution = %q, want an off skip clause", note)
	}
}

// TestResolveExplicitOffNotes pins §4.3's explicit case: naming an off
// candidate serves it, and the resolution carries the advisory note.
func TestResolveExplicitOffNotes(t *testing.T) {
	t.Parallel()

	set := candidateSet(t, rolesRuntimeCandidatesJSON)
	reg := rolesFileRegistry(t, set, policy.Policy{}, offRows())

	res, err := resolveRole(reg, set, nil, "claude/test/a", "builder")
	if err != nil {
		t.Fatalf("resolveRole(explicit off): %v", err)
	}
	if got := res.Token(); got != "claude/test/a" {
		t.Errorf("picked %q, want claude/test/a", got)
	}
	if res.How != HowExplicit {
		t.Errorf("How = %q, want %q", res.How, HowExplicit)
	}
	note := ExplainResolution("builder", res)
	if want := "note: a is off for builder; running it because you named it"; !strings.Contains(note, want) {
		t.Errorf("ExplainResolution = %q, want it to contain %q", note, want)
	}
}

// TestAllOffOrGated pins §4.3's last rule: when every remaining entry is off
// or gated the error is today's ErrAllGated, with the off entries listed as
// "<name> (off)".
func TestAllOffOrGated(t *testing.T) {
	t.Parallel()

	t.Run("all off", func(t *testing.T) {
		set := candidateSet(t, rolesRuntimeCandidatesJSON)
		reg := rolesFileRegistry(t, set, policy.Policy{}, map[string]roles.Row{
			"builder": {
				Candidates: []string{"claude/test/a", "claude/test/b"},
				Off:        []string{"claude/test/a", "claude/test/b"},
			},
		})

		_, err := resolveRole(reg, set, nil, "", "builder")
		if !errors.Is(err, ErrAllGated) {
			t.Fatalf("err = %v, want ErrAllGated", err)
		}
		for _, want := range []string{"claude/test/a (off)", "claude/test/b (off)"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("err = %q, want it to contain %q", err.Error(), want)
			}
		}
	})

	t.Run("off and gated", func(t *testing.T) {
		set := candidateSet(t, rolesRuntimeCandidatesJSON)
		reg := rolesFileRegistry(t, set, policy.Policy{}, offRows())
		gates := []availability.Gate{{Token: "claude/test/b", Kind: availability.RateLimited}}

		_, err := resolveRole(reg, set, gates, "", "builder")
		if !errors.Is(err, ErrAllGated) {
			t.Fatalf("err = %v, want ErrAllGated", err)
		}
		if !strings.Contains(err.Error(), "claude/test/a (off)") {
			t.Errorf("err = %q, want the off entry named", err.Error())
		}
	})

	t.Run("sole off", func(t *testing.T) {
		set := candidateSet(t, rolesRuntimeCandidatesJSON)
		reg := rolesFileRegistry(t, set, policy.Policy{}, map[string]roles.Row{
			"builder": {
				Candidates: []string{"claude/test/a"},
				Off:        []string{"claude/test/a"},
			},
		})

		_, err := resolveRole(reg, set, nil, "", "builder")
		if !errors.Is(err, ErrAllGated) {
			t.Fatalf("err = %v, want ErrAllGated", err)
		}
		if !strings.Contains(err.Error(), "claude/test/a (off)") {
			t.Errorf("err = %q, want the off entry named", err.Error())
		}
	})
}

// TestFormatPolicyShowsOff pins §4.4 and round 2's R1: an off entry prints the
// plain word "off" in the status column, never "<- would pick", and never an
// escape code (relevo config is read through a pipe).
func TestFormatPolicyShowsOff(t *testing.T) {
	t.Parallel()

	set := candidateSet(t, rolesRuntimeCandidatesJSON)
	reg := rolesFileRegistry(t, set, policy.Policy{}, map[string]roles.Row{
		"builder": {
			Candidates: []string{"claude/test/a", "claude/test/b", "claude/test/c"},
			Off:        []string{"claude/test/b"},
		},
	})

	got := FormatPolicyFor(reg, set, policy.Policy{}, nil, availability.History{}, baseTime, time.UTC)

	want := "builder  (config actors)\n" +
		"  1  a  order     <- would pick\n" +
		"  2  b  off\n" +
		"  3  c  order\n" +
		"reviewer  (config actors)\n" +
		"  no candidate listed in config actors reviewer.candidates\n" +
		"researcher  (config actors)\n" +
		"  no candidate listed in config actors researcher.candidates\n"
	if got != want {
		t.Errorf("FormatPolicyFor =\n%q\nwant:\n%q", got, want)
	}
	if strings.Contains(got, "b  order") || strings.Contains(got, "b  unlisted") {
		t.Errorf("an off entry must not carry a how tag:\n%s", got)
	}
	if strings.Contains(got, "\x1b") {
		t.Errorf("an off entry must print no escape codes:\n%q", got)
	}
}

// TestFormatPolicyHeaderSaysActors pins A2 round 3 S2.3, updated by A4-1a: the
// pick block's header names the section the registry came from, which is
// "config actors" for both the actors section and a legacy roles file -- the
// roles file is only ever the input of the A2 migration now.
func TestFormatPolicyHeaderSaysActors(t *testing.T) {
	t.Parallel()

	set := candidateSet(t, rolesRuntimeCandidatesJSON)
	rows := map[string]roles.Row{"builder": {Candidates: []string{testClaudeRef}}}

	actors := actorsFileRegistry(t, set, policy.Policy{}, rows)
	got := FormatPolicyFor(actors, set, policy.Policy{}, nil, availability.History{}, baseTime, time.UTC)
	if !strings.Contains(got, "builder  (config actors)") {
		t.Errorf("actors registry header:\n%s", got)
	}

	fileMode := rolesFileRegistry(t, set, policy.Policy{}, rows)
	got = FormatPolicyFor(fileMode, set, policy.Policy{}, nil, availability.History{}, baseTime, time.UTC)
	if !strings.Contains(got, "builder  (config actors)") {
		t.Errorf("roles registry header:\n%s", got)
	}
}
