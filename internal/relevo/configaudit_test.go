package relevo

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/db"
)

// auditStore is a store over a fresh database, with a pinned clock, for the
// tests that write real revisions.
func auditStore(t *testing.T) *config.Store {
	t.Helper()
	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return config.Open(d).WithClock(func() time.Time { return time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC) })
}

// TestDescribeChange pins the subject, field and value rules against their own
// examples and one case per remaining rule.
func TestDescribeChange(t *testing.T) {
	t.Parallel()

	candsBefore := config.Doc{config.Candidates: json.RawMessage(`[
		{"name": "gemini-3.8-flash-high", "harness": "agy", "provider": "google", "model": "gemini-3.8-flash-high"},
		{"name": "claude-sonnet-4-6", "harness": "agy", "provider": "antigravity", "model": "claude-sonnet-4-6"}
	]`)}
	candsAfter := config.Doc{config.Candidates: json.RawMessage(`[
		{"name": "gemini-3.8-flash-high", "harness": "agy", "provider": "google", "model": "gemini-3.8-flash-high"},
		{"name": "claude-sonnet-4-6", "harness": "agy", "provider": "agy-extra", "model": "claude-sonnet-4-6"}
	]`)}
	sonnet := config.Doc{config.Candidates: json.RawMessage(`[{"name": "sonnet", "harness": "claude", "provider": "anthropic", "model": "sonnet"}]`)}

	cases := []struct {
		name          string
		c             config.Change
		before, after config.Doc
		want          ChangeLine
	}{
		{
			name: "rule 1: a candidate's field",
			c: config.Change{
				Path: "candidates[1].provider", Op: "change",
				Before: json.RawMessage(`"antigravity"`), After: json.RawMessage(`"agy-extra"`),
			},
			before: candsBefore, after: candsAfter,
			want: ChangeLine{Op: "~", Subject: "claude-sonnet-4-6", Field: "provider", Before: "antigravity", After: "agy-extra"},
		},
		{
			name: "rule 2: a whole candidate entry",
			c: config.Change{
				Path: "candidates[0]", Op: "remove",
				Before: json.RawMessage(`{"name":"sonnet","harness":"claude"}`),
			},
			before: sonnet, after: sonnet,
			want: ChangeLine{Op: "-", Subject: "candidate sonnet", Before: "harness claude, name sonnet"},
		},
		{
			name: "rule 3: an actor's field",
			c: config.Change{
				Path: "actors.planner.agent", Op: "change",
				Before: json.RawMessage(`"architect"`), After: json.RawMessage(`"plan-executor"`),
			},
			want: ChangeLine{Op: "~", Subject: "actor planner", Field: "agent", Before: "architect", After: "plan-executor"},
		},
		{
			name: "rule 4: a whole actor entry",
			c: config.Change{
				Path: "actors.planner", Op: "add",
				After: json.RawMessage(`{"agent":"architect"}`),
			},
			want: ChangeLine{Op: "+", Subject: "actor planner", After: "agent architect"},
		},
		{
			name: "rule 5: an agent's field",
			c: config.Change{
				Path: "agents.scout.shape", Op: "change",
				Before: json.RawMessage(`"reader"`), After: json.RawMessage(`"writer"`),
			},
			want: ChangeLine{Op: "~", Subject: "agent scout", Field: "shape", Before: "reader", After: "writer"},
		},
		{
			name: "rule 5: a whole agent entry",
			c: config.Change{
				Path: "agents.scout", Op: "remove",
				Before: json.RawMessage(`{"shape":"reader"}`),
			},
			want: ChangeLine{Op: "-", Subject: "agent scout", Before: "shape reader"},
		},
		{
			name: "rule 6: a policy number in ms",
			c: config.Change{
				Path: "policy.stall_after_ms", Op: "change",
				Before: json.RawMessage(`900000`), After: json.RawMessage(`1200000`),
			},
			want: ChangeLine{Op: "~", Subject: "stall_after", Before: "900000ms", After: "1200000ms"},
		},
		{
			name: "rule 6: a policy number without ms",
			c: config.Change{
				Path: "policy.max_switches", Op: "change",
				Before: json.RawMessage(`2`), After: json.RawMessage(`3`),
			},
			want: ChangeLine{Op: "~", Subject: "max_switches", Before: "2", After: "3"},
		},
		{
			name: "rule 6: a policy bool",
			c: config.Change{
				Path: "policy.check", Op: "add",
				After: json.RawMessage(`true`),
			},
			want: ChangeLine{Op: "+", Subject: "check", After: "on"},
		},
		{
			name: "rule 7: a section removed",
			c: config.Change{
				Path: "roles", Op: "remove",
				Before: json.RawMessage(`{"builder":["gemini-3.8-flash-high"]}`),
			},
			want: ChangeLine{Op: "-", Subject: "roles", Before: "builder …"},
		},
		{
			name: "rule 7: a section added",
			c: config.Change{
				Path: "servers", Op: "add",
				After: json.RawMessage(`{"zen":{"url":"https://zen:7777"}}`),
			},
			want: ChangeLine{Op: "+", Subject: "servers", After: "zen …"},
		},
		{
			name: "a secret set carries no value",
			c: config.Change{
				Path: "secret.client_key", Op: "set",
			},
			want: ChangeLine{Op: "*", Subject: "secret.client_key"},
		},
		{
			name: "a quoted actor key with a field",
			c: config.Change{
				Path: `actors["plan-executor"].tier`, Op: "change",
				Before: json.RawMessage(`"edit"`), After: json.RawMessage(`"yolo"`),
			},
			want: ChangeLine{Op: "~", Subject: "actor plan-executor", Field: "tier", Before: "edit", After: "yolo"},
		},
		{
			name: "a quoted agent key",
			c: config.Change{
				Path: `agents["plan-executor"]`, Op: "add",
				After: json.RawMessage(`{"shape":"writer"}`),
			},
			want: ChangeLine{Op: "+", Subject: "agent plan-executor", After: "shape writer"},
		},
		{
			name: "a bare actor key with a dotted field",
			c: config.Change{
				Path: "actors.builder.candidates", Op: "change",
				Before: json.RawMessage(`["a"]`), After: json.RawMessage(`["b"]`),
			},
			want: ChangeLine{Op: "~", Subject: "actor builder", Field: "candidates", Before: "a", After: "b"},
		},
		{
			name: "an unclosed quoted key falls through",
			c: config.Change{
				Path: `actors["bad`, Op: "change",
				Before: json.RawMessage(`"a"`), After: json.RawMessage(`"b"`),
			},
			want: ChangeLine{Op: "~", Subject: `actors["bad`, Before: "a", After: "b"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := DescribeChange(tc.c, tc.before, tc.after); got != tc.want {
				t.Errorf("DescribeChange(%s) = %+v, want %+v", tc.c.Path, got, tc.want)
			}
		})
	}
}

// A value longer than 60 runes is cut with the ellipsis, as cutValue cuts it.
func TestDescribeChangeCutsValue(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("x", 70)
	c := config.Change{Path: "policy.reason", Op: "add", After: json.RawMessage(`"` + long + `"`)}

	got := DescribeChange(c, config.Doc{}, config.Doc{})
	if n := len([]rune(got.After)); n != auditMaxValue {
		t.Errorf("value = %d runes, want %d: %q", n, auditMaxValue, got.After)
	}
	if !strings.HasSuffix(got.After, "…") {
		t.Errorf("value = %q, want it cut with an ellipsis", got.After)
	}
}

// RevisionChanges describes a real revision against the one before it: the
// provider change is the change, and the candidate is named.
func TestRevisionChanges(t *testing.T) {
	t.Parallel()

	s := auditStore(t)

	bodyA := []byte(`[{"harness":"agy","provider":"google","model":"gemini-3.8-flash-high"},` +
		`{"harness":"agy","provider":"agy-extra","model":"claude-sonnet-4-6"}]`)
	if _, err := s.As("cli", "config set candidates").Put(config.Candidates, bodyA); err != nil {
		t.Fatalf("seed Put: %v", err)
	}
	bodyB := []byte(`[{"harness":"agy","provider":"google","model":"gemini-3.8-flash-high"},` +
		`{"harness":"agy","provider":"google","model":"claude-sonnet-4-6"}]`)
	if _, err := s.As("cli", "config set candidates").Put(config.Candidates, bodyB); err != nil {
		t.Fatalf("Put: %v", err)
	}

	got, err := RevisionChanges(s, 2)
	if err != nil {
		t.Fatalf("RevisionChanges(2): %v", err)
	}
	want := []ChangeLine{{
		Op: "~", Subject: "claude-sonnet-4-6", Field: "provider", Before: "agy-extra", After: "google",
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("RevisionChanges(2) = %+v, want %+v", got, want)
	}
}

// A revision whose document the provider lock refuses cannot be rolled back to:
// the refusal names the reason, and the message names the revision.
func TestRollbackPreviewRefused(t *testing.T) {
	t.Parallel()

	s := auditStore(t)

	// The raw write bypasses the form, exactly as an older config would.
	raw := []byte(`[{"harness":"agy","provider":"antigravity","model":"claude-sonnet-4-6"}]`)
	if _, err := s.As("cli", "config set candidates").Put(config.Candidates, raw); err != nil {
		t.Fatalf("raw Put: %v", err)
	}
	fixed := []byte(`[{"harness":"agy","provider":"agy-extra","model":"claude-sonnet-4-6"}]`)
	if _, err := s.As("cli", "config set candidates").Put(config.Candidates, fixed); err != nil {
		t.Fatalf("Put: %v", err)
	}

	_, err := RollbackPreview(s, 1)
	var refused *RollbackRefused
	if !errors.As(err, &refused) {
		t.Fatalf("RollbackPreview(1) = %v, want *RollbackRefused", err)
	}
	if refused.Rev != 1 {
		t.Errorf("refused rev = %d, want 1", refused.Rev)
	}
	if !strings.Contains(refused.Reason, "agy") {
		t.Errorf("reason = %q, want it to name agy's providers", refused.Reason)
	}
	if want := "can't roll back to #1: "; !strings.HasPrefix(err.Error(), want) {
		t.Errorf("error = %q, want it to start %q", err.Error(), want)
	}
}

// Rolling back to the newest revision changes nothing, and says so through
// config.ErrNoChange.
func TestRollbackPreviewNoChange(t *testing.T) {
	t.Parallel()

	s := auditStore(t)

	body := []byte(`[{"harness":"claude","provider":"anthropic","model":"sonnet"}]`)
	if _, err := s.As("cli", "config set candidates").Put(config.Candidates, body); err != nil {
		t.Fatalf("Put: %v", err)
	}

	if _, err := RollbackPreview(s, 1); !errors.Is(err, config.ErrNoChange) {
		t.Errorf("RollbackPreview(newest) = %v, want config.ErrNoChange", err)
	}
}

// CheckDoc accepts a document the form would accept and refuses one the form
// would refuse.
func TestCheckDoc(t *testing.T) {
	t.Parallel()

	valid := config.Doc{
		config.Candidates: json.RawMessage(`[{"name":"sonnet","harness":"claude","provider":"anthropic","model":"sonnet"}]`),
		config.Actors:     json.RawMessage(`{"builder":{"agent":"plan-executor","candidates":[{"candidate":"sonnet"}]}}`),
	}
	if err := CheckDoc(valid); err != nil {
		t.Errorf("CheckDoc(valid) = %v, want nil", err)
	}

	bad := config.Doc{
		config.Candidates: valid[config.Candidates],
		config.Actors:     json.RawMessage(`{"builder":{"agent":"plan-executor","candidates":[{"candidate":"ghost candidate"}]}}`),
	}
	if err := CheckDoc(bad); err == nil {
		t.Error("CheckDoc must refuse an actor naming a candidate the config cannot resolve")
	}
}
