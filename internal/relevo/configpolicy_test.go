package relevo

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/config"
	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/policy"
)

func TestSettings(t *testing.T) {
	t.Parallel()

	t.Run("real policy", func(t *testing.T) {
		raw := `{"max_switches":2,"max_tier":"yolo","serve":{"scope":{"slice":"relevo.slice"}}}`
		p, _, err := policy.Parse(config.FileName(config.Policy), []byte(raw))
		if err != nil {
			t.Fatalf("policy.Parse: %v", err)
		}
		doc := ConfigDoc{
			Policy:    p,
			PolicyRaw: json.RawMessage(raw),
		}
		settings := Settings(doc, 22)
		if len(settings) != 18 {
			t.Fatalf("len(settings) = %d, want 18", len(settings))
		}

		// Rows 1, 2 and 15 are Set (1-based: indices 0, 1, 14)
		for i, s := range settings {
			wantSet := (i == 0 || i == 1 || i == 14)
			if s.Set != wantSet {
				t.Errorf("row %d (%s).Set = %v, want %v", i+1, s.Key, s.Set, wantSet)
			}
		}

		// serve.scope reads slice relevo.slice
		if settings[14].Key != "serve.scope" || settings[14].Value != "slice relevo.slice" {
			t.Errorf("serve.scope value = %q, want %q", settings[14].Value, "slice relevo.slice")
		}

		// serve.max_builders reads 21 / 21
		if settings[13].Key != "serve.max_builders" || settings[13].Value != "21" || settings[13].Default != "21" {
			t.Errorf("serve.max_builders = %s / %s, want 21 / 21", settings[13].Value, settings[13].Default)
		}

		// max_tier reads yolo / edit
		if settings[1].Key != "max_tier" || settings[1].Value != "yolo" || settings[1].Default != "edit" {
			t.Errorf("max_tier = %s / %s, want yolo / edit", settings[1].Value, settings[1].Default)
		}

		// artifact_max_mb is unset here, so it reads its 25 / 25 default
		if settings[3].Key != "artifact_max_mb" || settings[3].Value != "25" || settings[3].Default != "25" {
			t.Errorf("artifact_max_mb = %s / %s, want 25 / 25", settings[3].Value, settings[3].Default)
		}
	})

	t.Run("empty policy", func(t *testing.T) {
		doc := ConfigDoc{}
		settings := Settings(doc, 4)
		if len(settings) != 18 {
			t.Fatalf("len(settings) = %d, want 18", len(settings))
		}
		for i, s := range settings {
			if s.Set {
				t.Errorf("row %d (%s) is Set, want unset", i+1, s.Key)
			}
		}
	})
}

func policyTestDoc(t *testing.T) ConfigDoc {
	t.Helper()
	d := configeditDoc(t)
	d.Actors = copyActors(d.Actors)
	for k, a := range d.Actors {
		a.Tier = ""
		d.Actors[k] = a
	}
	return d
}

func TestEditPolicy(t *testing.T) {
	t.Parallel()

	doc := policyTestDoc(t)

	t.Run("sets gate.timeout_ms and creates gate", func(t *testing.T) {
		d := doc
		d.PolicyRaw = json.RawMessage(`{}`)
		edit, err := EditPolicy(d, []PolicySet{{Path: "gate.timeout_ms", Value: 600000}}, "set timeout")
		if err != nil {
			t.Fatalf("EditPolicy: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(edit.Sections[config.Policy], &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		gate, ok := m["gate"].(map[string]any)
		if !ok {
			t.Fatalf("gate is not a map: %v", m["gate"])
		}
		if timeout, ok := gate["timeout_ms"].(float64); !ok || int64(timeout) != 600000 {
			t.Errorf("gate.timeout_ms = %v, want 600000", gate["timeout_ms"])
		}
	})

	t.Run("deleting serve.scope.slice from real policy removes serve entirely", func(t *testing.T) {
		d := doc
		raw := `{"max_switches":2,"max_tier":"yolo","serve":{"scope":{"slice":"relevo.slice"}}}`
		d.PolicyRaw = json.RawMessage(raw)
		edit, err := EditPolicy(d, []PolicySet{{Path: "serve.scope.slice", Value: nil}}, "del slice")
		if err != nil {
			t.Fatalf("EditPolicy: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(edit.Sections[config.Policy], &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if _, exists := m["serve"]; exists {
			t.Errorf("serve still present: %v", m["serve"])
		}
	})

	t.Run("unknown key future_knob survives unrelated set", func(t *testing.T) {
		d := doc
		raw := `{"future_knob":1}`
		d.PolicyRaw = json.RawMessage(raw)
		edit, err := EditPolicy(d, []PolicySet{{Path: "gate.timeout_ms", Value: 60000}}, "set timeout")
		if err != nil {
			t.Fatalf("EditPolicy: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(edit.Sections[config.Policy], &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if knob, ok := m["future_knob"].(float64); !ok || knob != 1 {
			t.Errorf("future_knob = %v, want 1", m["future_knob"])
		}
	})

	t.Run("returns ErrNoChange when value is unchanged", func(t *testing.T) {
		d := doc
		raw := `{"max_switches":2}`
		d.PolicyRaw = json.RawMessage(raw)
		_, err := EditPolicy(d, []PolicySet{{Path: "max_switches", Value: 2}}, "set switches")
		if !errors.Is(err, ErrNoChange) {
			t.Errorf("err = %v, want ErrNoChange", err)
		}
	})

	t.Run("max_switches: -1 returns a FieldError", func(t *testing.T) {
		d := doc
		d.PolicyRaw = json.RawMessage(`{}`)
		_, err := EditPolicy(d, []PolicySet{{Path: "max_switches", Value: -1}}, "set switches")
		var fe *FieldError
		if !errors.As(err, &fe) {
			t.Fatalf("err = %v, want *FieldError", err)
		}
		if !strings.Contains(fe.Msg, "max_switches") {
			t.Errorf("fe.Msg = %q, want max_switches", fe.Msg)
		}
	})
}

func TestResetSetting(t *testing.T) {
	t.Parallel()

	doc := policyTestDoc(t)
	raw := `{"max_switches":2,"max_tier":"yolo"}`
	p, _, err := policy.Parse(config.FileName(config.Policy), []byte(raw))
	if err != nil {
		t.Fatalf("policy.Parse: %v", err)
	}
	doc.Policy = p
	doc.PolicyRaw = json.RawMessage(raw)

	t.Run("max_tier removes it", func(t *testing.T) {
		edit, err := ResetSetting(doc, "max_tier")
		if err != nil {
			t.Fatalf("ResetSetting: %v", err)
		}
		if edit.Message != "reset max_tier" {
			t.Errorf("message = %q, want reset max_tier", edit.Message)
		}
		var m map[string]any
		if err := json.Unmarshal(edit.Sections[config.Policy], &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if _, exists := m["max_tier"]; exists {
			t.Errorf("max_tier still present in %v", m)
		}
	})

	t.Run("gate.default when unset returns ErrNoChange", func(t *testing.T) {
		_, err := ResetSetting(doc, "gate.default")
		if !errors.Is(err, ErrNoChange) {
			t.Errorf("err = %v, want ErrNoChange", err)
		}
	})
}

// TestHumanPolicyError pins the three rewrite rules: a tier-above-max_tier
// rejection becomes a sentence naming the actor; anything else has its
// leading "<name>.json: " and trailing ": bad <word>" stripped; anything that
// matches neither passes through unchanged.
func TestHumanPolicyError(t *testing.T) {
	t.Parallel()

	cases := []struct{ in, want string }{
		{
			in:   `roles.json: builder.tier: yolo exceeds max_tier edit: bad roles`,
			want: "builder runs at yolo, above edit; lower its tier in :actors first",
		},
		{
			in:   `policy.json: serve.scope.slice: must end in ".slice", got "x": bad policy`,
			want: `serve.scope.slice: must end in ".slice", got "x"`,
		},
		{
			in:   "something else",
			want: "something else",
		},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := HumanPolicyError(errors.New(tc.in)); got != tc.want {
				t.Errorf("HumanPolicyError(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestFormatDuration(t *testing.T) {
	t.Parallel()

	cases := []struct {
		d    time.Duration
		want string
	}{
		{time.Hour, "1h"},
		{15 * time.Minute, "15m"},
		{30 * time.Second, "30s"},
		{90 * time.Minute, "1h30m"},
		{4 * time.Hour, "4h"},
	}
	for _, tc := range cases {
		got := FormatDuration(tc.d)
		if got != tc.want {
			t.Errorf("FormatDuration(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

func TestLoadConfigDocPolicyRaw(t *testing.T) {
	t.Parallel()

	d, err := db.Open(filepath.Join(t.TempDir(), "relevo.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	st := config.Open(d)

	raw := []byte("{\n  \"max_switches\": 2\n}\n")
	_, err = st.As("ui", "init policy").PutDoc(map[config.Section]json.RawMessage{
		config.Policy: json.RawMessage(raw),
	})
	if err != nil {
		t.Fatalf("PutDoc: %v", err)
	}

	got, err := LoadConfigDoc(st)
	if err != nil {
		t.Fatalf("LoadConfigDoc: %v", err)
	}
	if string(got.PolicyRaw) != string(raw) {
		t.Errorf("PolicyRaw = %q, want %q", string(got.PolicyRaw), string(raw))
	}
	if got.Policy.MaxSwitches == nil || *got.Policy.MaxSwitches != 2 {
		t.Errorf("Policy.MaxSwitches = %v, want 2", got.Policy.MaxSwitches)
	}
}
