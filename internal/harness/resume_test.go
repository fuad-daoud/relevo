package harness

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestResumePerKind pins the resume argv for every kind that has one: the
// session flag and the tier's permission args appended exactly as Launch
// appends them. opencode has no read-only form, so a resume at read is
// refused; codex has no verified resume form at all.
func TestResumePerKind(t *testing.T) {
	const prompt = "you built round 1"
	tests := []struct {
		name    string
		kind    string
		id      string
		tier    Tier
		want    []string
		wantErr error
		refuse  bool
	}{
		{
			name: "claude", kind: "claude", id: "sess-1", tier: TierRead,
			want: []string{"-p", prompt, "--resume", "sess-1", "--output-format", "stream-json", "--verbose",
				"--permission-mode", "plan"},
		},
		{
			name: "agy", kind: "agy", id: "conv-1", tier: TierRead,
			want: []string{"-p", prompt, "--conversation", "conv-1", "--output-format", "stream-json",
				"--mode", "plan"},
		},
		{
			name: "opencode", kind: "opencode", id: "ses-1", tier: TierHarness,
			want: []string{"run", prompt, "--session", "ses-1", "--fork", "--format", "json", "--thinking", "--standalone"},
		},
		{
			name: "opencode read", kind: "opencode", id: "ses-1", tier: TierRead,
			wantErr: ErrTierUnsupported,
		},
		{
			name: "codex", kind: "codex", id: "thread-1", tier: TierHarness,
			wantErr: ErrResumeUnsupported,
		},
		{
			name: "empty id", kind: "claude", id: "", tier: TierRead,
			refuse: true,
		},
		{
			name: "flag-shaped id", kind: "claude", id: "-x", tier: TierRead,
			refuse: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, ok := Lookup(tt.kind)
			if !ok {
				t.Fatalf("Lookup(%q) not found", tt.kind)
			}
			got, err := h.Resume(tt.id, prompt, tt.tier)
			switch {
			case tt.refuse:
				if err == nil {
					t.Fatalf("Resume(%q) = %v, want a refusal", tt.id, got)
				}
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("Resume() error = %v, want %v", err, tt.wantErr)
				}
			default:
				if err != nil {
					t.Fatalf("Resume() error = %v", err)
				}
				if !reflect.DeepEqual(got, tt.want) {
					t.Errorf("Resume() = %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestResumeRefusalNamesTheKind(t *testing.T) {
	h, ok := Lookup("codex")
	if !ok {
		t.Fatal("Lookup(\"codex\") not found")
	}
	_, err := h.Resume("thread-1", "p", TierHarness)
	if !errors.Is(err, ErrResumeUnsupported) {
		t.Fatalf("error = %v, want ErrResumeUnsupported", err)
	}
	if !strings.Contains(err.Error(), "codex") {
		t.Errorf("error = %q, want it to name codex", err)
	}
}

func TestResumeRefusesWhitespace(t *testing.T) {
	h, _ := Lookup("claude")
	for _, id := range []string{"a b", "a\tb", "a\nb"} {
		if _, err := h.Resume(id, "p", TierRead); err == nil {
			t.Errorf("Resume(%q) = nil error, want a refusal", id)
		}
	}
}

// TestDeleteSessionPerKind pins the delete argv for the one kind relevo can
// delete sessions for: opencode's own delete, with --standalone so the delete
// never starts the background service that would resume the session.
func TestDeleteSessionPerKind(t *testing.T) {
	t.Parallel()

	h, ok := Lookup("opencode")
	if !ok {
		t.Fatal("Lookup(\"opencode\") not found")
	}
	got, err := h.DeleteSession("ses-1")
	if err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	want := []string{"session", "delete", "--standalone", "ses-1"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DeleteSession() = %v, want %v", got, want)
	}
}

// TestDeleteSessionUnsupportedNamesTheKind pins the refusal for every kind
// whose sessions end with their process: claude and agy continue a session in
// place, codex has no verified delete.
func TestDeleteSessionUnsupportedNamesTheKind(t *testing.T) {
	t.Parallel()

	for _, kind := range []string{"claude", "agy", "codex"} {
		h, ok := Lookup(kind)
		if !ok {
			t.Fatalf("Lookup(%q) not found", kind)
		}
		argv, err := h.DeleteSession("sess-1")
		if !errors.Is(err, ErrSessionDeleteUnsupported) {
			t.Errorf("DeleteSession(%q) error = %v, want ErrSessionDeleteUnsupported", kind, err)
		}
		if err == nil || !strings.Contains(err.Error(), kind) {
			t.Errorf("DeleteSession(%q) error = %q, want it to name the kind", kind, err)
		}
		if argv != nil {
			t.Errorf("DeleteSession(%q) = %v, want nil alongside the refusal", kind, argv)
		}
	}
}

func TestDeleteSessionRefusesAnEmptyOrFlagShapedID(t *testing.T) {
	t.Parallel()

	h, _ := Lookup("opencode")
	for _, id := range []string{"", "-x"} {
		if argv, err := h.DeleteSession(id); err == nil {
			t.Errorf("DeleteSession(%q) = %v, want a refusal", id, argv)
		}
	}
}

type resumeBuildCase struct {
	name    string
	kind    string
	id      string
	want    []string
	wantErr error
	refuse  bool
}

// TestResumeBuildPerKind pins the lost builder's resume argv: the builder-grade
// print form the round was started with, model flag included, followed by the
// harness's own selector.
func TestResumeBuildPerKind(t *testing.T) {
	const (
		provider = "test"
		model    = "m"
		prompt   = "finish round 1"
		dir      = "/repo"
		state    = "/state"
	)
	budget := 2 * time.Hour
	builder, ok := RoleByName("builder")
	if !ok {
		t.Fatal("RoleByName(\"builder\") not found")
	}

	tests := []resumeBuildCase{
		{
			name: "claude", kind: "claude", id: "sess-1",
			want: []string{"-p", prompt, "--model", model, "--agent", "plan-executor",
				"--output-format", "stream-json", "--verbose", "--resume", "sess-1"},
		},
		{
			name: "agy", kind: "agy", id: "conv-1",
			want: []string{"-p", prompt, "--model", model, "--agent", "plan-executor",
				"--output-format", "stream-json", "--print-timeout", "2h0m0s",
				"--add-dir", dir, "--conversation", "conv-1"},
		},
		{
			name: "opencode", kind: "opencode", id: "ses-1",
			want: []string{"run", prompt, "-m", provider + "/" + model, "--agent", "plan-executor",
				"--format", "json", "--thinking", "--standalone", "--session", "ses-1", "--fork"},
		},
		{name: "codex", kind: "codex", id: "thread-1", wantErr: ErrResumeUnsupported},
		{name: "unknown kind", kind: "future", id: "id-1", wantErr: ErrResumeUnsupported},
		{name: "empty id", kind: "claude", id: "", refuse: true},
		{name: "whitespace id", kind: "claude", id: "a b", refuse: true},
		{name: "flag-shaped id", kind: "claude", id: "-x", refuse: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			checkResumeBuild(t, tt, builder, provider, model, prompt, budget, dir, state)
		})
	}
}

func checkResumeBuild(t *testing.T, tt resumeBuildCase, builder RoleSpec, provider, model, prompt string, budget time.Duration, dir, state string) {
	t.Helper()
	h := Harness{Kind: tt.kind}
	if known, ok := Lookup(tt.kind); ok {
		h = known
	}
	l, err := h.Launch(provider, model, nil, builder, TierHarness)
	if err != nil {
		t.Fatalf("Launch(%s): %v", tt.kind, err)
	}
	got, err := h.ResumeBuild(tt.id, l, prompt, budget, dir, state)
	switch {
	case tt.wantErr != nil:
		if !errors.Is(err, tt.wantErr) {
			t.Fatalf("ResumeBuild() error = %v, want %v", err, tt.wantErr)
		}
		if got != nil {
			t.Errorf("ResumeBuild() = %v, want nil alongside the refusal", got)
		}
	case tt.refuse:
		if err == nil {
			t.Fatalf("ResumeBuild(%q) = %v, want a refusal", tt.id, got)
		}
	default:
		if err != nil {
			t.Fatalf("ResumeBuild() error = %v", err)
		}
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("ResumeBuild() = %v, want %v", got, tt.want)
		}
		// The result is the round's print form and then the selector.
		base := l.PrintArgs(prompt, budget, dir, state)
		if len(got) < len(base) || !reflect.DeepEqual(got[:len(base)], base) {
			t.Errorf("ResumeBuild() = %v, want it to start with PrintArgs %v", got, base)
		}
	}
}
