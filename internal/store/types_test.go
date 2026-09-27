package store

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestBindingZeroValueOmitsOptionalKeys pins omitempty: a binding without a
// group of optional fields must not serialise that group's keys, so a
// bind.json written before the fields existed stays byte-identical.
func TestBindingZeroValueOmitsOptionalKeys(t *testing.T) {
	cases := []struct {
		name   string
		build  func() Binding
		absent []string
	}{
		{"empty binding", func() Binding { return Binding{} }, []string{"round_closed_tree", "round_switches"}},
		{"no consults", func() Binding { return newBinding("webshop", "/repo") }, []string{"consults", "consult_cap"}},
		{"no repo ref or feature", func() Binding { return newBinding("webshop", "/repo") }, []string{"repo_ref", "feature", "transcript_locator"}},
		{"no commit facts", func() Binding { return Binding{} }, []string{"branch", "base", "round_baseline_head"}},
		{"no headless endpoint fields", func() Binding {
			return Binding{Builder: Endpoint{AgentName: "b", PaneID: "w1:p2", Kind: "opencode"}}
		}, []string{"mode", "pid", "started_at", "log_path", "stream_round", "stream_offset"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := json.Marshal(tc.build())
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			for _, key := range tc.absent {
				if bytes.Contains(raw, []byte(`"`+key+`"`)) {
					t.Errorf("serialised %q, which needs omitempty: %s", key, raw)
				}
			}
		})
	}
}

func bindingWithSwitchFields() Binding {
	b := newBinding("webshop", "/repo")
	b.RoundSwitches = 2
	b.BuilderMissingSince = time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	return b
}

func bindingWithRepoRef() Binding {
	b := newBinding("webshop", "/repo")
	b.RepoRef = &RepoRef{OriginURL: "https://github.com/o/r", CommonDir: "/repo/.git"}
	b.Feature = "auth"
	b.MasterMind.TranscriptLocator = "/home/x/.claude/projects/slug/S.jsonl"
	return b
}

func bindingWithConsult() Binding {
	b := newBinding("webshop", "/repo")
	b.ConsultCap = 4
	b.Consults = []Consult{{
		ID:           "7f2a3c1d",
		Role:         "reviewer",
		Endpoint:     Endpoint{AgentName: "webshop-reviewer-7f2a3c1d", PaneID: "w2:p9", Kind: "claude"},
		Round:        3,
		AskPath:      "/state/webshop/003-7f2a3c1d-ask.md",
		FindingsPath: "/state/webshop/003-7f2a3c1d-findings.md",
		State:        ConsultRunning,
		SpawnedAt:    time.Unix(1757000000, 0).UTC(),
	}}
	return b
}

// TestBindingFieldGroupsRoundTrip pins that each optional field group survives
// a marshal/unmarshal and that its non-zero values are written.
func TestBindingFieldGroupsRoundTrip(t *testing.T) {
	treeID := "4b825dc642cb6eb9a060e54bf8d69288fbee4904"
	cases := []struct {
		name  string
		build func() Binding
		want  []string
	}{
		{
			name:  "round closed tree",
			build: func() Binding { return Binding{RoundClosedTree: treeID} },
			want:  []string{`"round_closed_tree"`},
		},
		{"switch fields", bindingWithSwitchFields, []string{`"round_switches"`}},
		{
			name:  "commit facts",
			build: func() Binding { return Binding{Branch: "relevo/api-auth", Base: "c0ffee", RoundBaselineHead: "beef"} },
			want:  []string{`"branch"`, `"base"`, `"round_baseline_head"`},
		},
		{"repo ref, feature and transcript locator", bindingWithRepoRef, []string{`"repo_ref"`, `"feature"`, `"transcript_locator"`}},
		{"a consult and its cap", bindingWithConsult, []string{`"consults"`, `"consult_cap"`}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := tc.build()
			if in.Role == "" {
				// A builder binding's actor is stored as the literal
				// "builder"; the decoder normalises it there, so the round
				// trip starts from the canonical value.
				in.Role = "builder"
			}
			if in.Shape == "" {
				// A binding with no shape decodes as a writer; the round trip
				// starts from the canonical value.
				in.Shape = ShapeWriter
			}
			raw, err := json.Marshal(in)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			for _, key := range tc.want {
				if !bytes.Contains(raw, []byte(key)) {
					t.Errorf("serialised value lacks %s: %s", key, raw)
				}
			}
			var got Binding
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if !reflect.DeepEqual(got, in) {
				t.Errorf("round trip mismatch:\n got %+v\nwant %+v", got, in)
			}
		})
	}
}

// TestSameBindingDetectsAConsultChange pins SameBinding catching a slice
// change the daemon must persist: an appended consult and a consult moving
// running -> done.
func TestSameBindingDetectsAConsultChange(t *testing.T) {
	base := newBinding("webshop", "/repo")
	withConsult := func(state ConsultState) Binding {
		b := base
		b.Consults = []Consult{{ID: "7f2a3c1d", Role: "reviewer", State: state}}
		return b
	}

	if !SameBinding(base, newBinding("webshop", "/repo")) {
		t.Fatal("two identically-built bindings compare different")
	}
	if SameBinding(base, withConsult(ConsultRunning)) {
		t.Error("an appended consult compares same; the daemon would skip the save")
	}
	if SameBinding(withConsult(ConsultRunning), withConsult(ConsultDone)) {
		t.Error("running -> done compares same; the daemon would never persist the transition")
	}
}

func TestEndpointHeadless(t *testing.T) {
	cases := []struct {
		name string
		mode Mode
		want bool
	}{
		{"empty mode reads as pane", "", false},
		{"explicit pane", Mode("pane"), false},
		{"headless", ModeHeadless, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := Endpoint{Kind: "agy", Mode: c.mode}
			if got := e.Headless(); got != c.want {
				t.Errorf("Headless() = %v, want %v", got, c.want)
			}
		})
	}
}

func TestEndpointHeadlessFieldsRoundTrip(t *testing.T) {
	want := Endpoint{
		AgentName:    "webshop-builder",
		Kind:         "agy",
		Mode:         ModeHeadless,
		PID:          4242,
		StartedAt:    1789000000,
		LogPath:      "/state/webshop/003-builder.log",
		StreamRound:  3,
		StreamOffset: 4096,
	}
	data, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got Endpoint
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("round trip: got %+v, want %+v", got, want)
	}
	if !got.Headless() {
		t.Error("decoded headless endpoint reports Headless() false")
	}

	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal map: %v", err)
	}
	if decoded["mode"] != "headless" || decoded["log_path"] != want.LogPath {
		t.Errorf("JSON keys: %s", data)
	}
	if decoded["pid"] != float64(4242) || decoded["started_at"] != float64(1789000000) {
		t.Errorf("pid/started_at must be JSON numbers: %s", data)
	}
	if decoded["stream_round"] != float64(3) || decoded["stream_offset"] != float64(4096) {
		t.Errorf("stream cursor keys: %s", data)
	}
}

func TestValidFeature(t *testing.T) {
	cases := []struct {
		name  string
		in    string
		valid bool
	}{
		{"simple word", "auth", true},
		{"space inside", "api v2", true},
		{"dot underscore dash digit", "x.y_z-1", true},
		{"empty", "", false},
		{"too long", strings.Repeat("a", 65), false},
		{"leading space", " lead", false},
		{"trailing space", "trail ", false},
		{"slash", "a/b", false},
		{"non-ascii", "é", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := ValidFeature(c.in)
			if c.valid && err != nil {
				t.Errorf("ValidFeature(%q) = %v, want nil", c.in, err)
			}
			if !c.valid && err == nil {
				t.Errorf("ValidFeature(%q) = nil, want an error", c.in)
			}
		})
	}
}
