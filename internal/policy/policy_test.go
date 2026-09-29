package policy

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/fuad-daoud/relevo/internal/harness"
)

func load(t *testing.T, body string) (Policy, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return Load(path)
}

// loadWarnings loads body with LoadWithWarnings, failing on any error.
func loadWarnings(t *testing.T, body string) (Policy, []string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	p, warnings, err := LoadWithWarnings(path)
	if err != nil {
		t.Fatalf("LoadWithWarnings(%s): %v", body, err)
	}
	return p, warnings
}

// TestLoadUnknownKeysWarn pins that an unknown key is a warning, not an
// error, unless it is also an invalid value.
func TestLoadUnknownKeysWarn(t *testing.T) {
	t.Run("unknown top-level key", func(t *testing.T) {
		p, warnings := loadWarnings(t, `{"orders":{"builder":["a/b/c"]}}`)
		if len(warnings) != 1 || !strings.Contains(warnings[0], `unknown key "orders"`) {
			t.Fatalf("warnings = %v, want one naming orders", warnings)
		}
		if len(p.Order) != 0 {
			t.Errorf("Order = %v, want empty", p.Order)
		}
	})

	t.Run("unknown nested key", func(t *testing.T) {
		_, warnings := loadWarnings(t, `{"classify":{"provider":"jev","unknown_key":true}}`)
		if len(warnings) != 1 || !strings.Contains(warnings[0], `unknown key "classify.unknown_key"`) {
			t.Fatalf("warnings = %v, want one naming classify.unknown_key", warnings)
		}
	})

	t.Run("a key under a map-typed field gives no warning", func(t *testing.T) {
		_, warnings := loadWarnings(t, `{"order":{"builder":["a/b/c"]},"tier":{"builder":"read"}}`)
		if len(warnings) != 0 {
			t.Fatalf("warnings = %v, want none", warnings)
		}
	})

	t.Run("a bad value is still an error", func(t *testing.T) {
		_, err := load(t, `{"max_switches":-1,"unknown_key":true}`)
		if err == nil {
			t.Fatal("Load: got nil error, want one wrapping ErrBadPolicy")
		}
		if !errors.Is(err, ErrBadPolicy) {
			t.Fatalf("Load error %v does not wrap ErrBadPolicy", err)
		}
	})
}

func TestLoadMissingFileIsZeroPolicy(t *testing.T) {
	p, err := Load(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(p.Order) != 0 {
		t.Fatalf("Order = %v, want empty", p.Order)
	}
	if got := p.OrderFor("builder"); got != nil {
		t.Fatalf("OrderFor(%q) = %v, want nil", "builder", got)
	}
}

func TestLoadValidPolicy(t *testing.T) {
	body := `{"order":{"builder":["agy/google/m","claude/anthropic/sonnet"],"reviewer":["claude/anthropic/opus"]}}`
	p, err := load(t, body)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	wantBuilder := []string{"agy/google/m", "claude/anthropic/sonnet"}
	if got := p.OrderFor("builder"); !reflect.DeepEqual(got, wantBuilder) {
		t.Fatalf("OrderFor(%q) = %v, want %v", "builder", got, wantBuilder)
	}

	wantReviewer := []string{"claude/anthropic/opus"}
	if got := p.OrderFor("reviewer"); !reflect.DeepEqual(got, wantReviewer) {
		t.Fatalf("OrderFor(%q) = %v, want %v", "reviewer", got, wantReviewer)
	}

	if got := p.OrderFor("researcher"); got != nil {
		t.Fatalf("OrderFor(%q) = %v, want nil", "researcher", got)
	}
}

func TestOrderReturnsACopy(t *testing.T) {
	body := `{"order":{"builder":["agy/google/m","claude/anthropic/sonnet"],"reviewer":["claude/anthropic/opus"]}}`
	p, err := load(t, body)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	got := p.OrderFor("builder")
	got[0] = "x"

	if again := p.OrderFor("builder"); again[0] != "agy/google/m" {
		t.Fatalf("OrderFor(%q)[0] = %q after mutating a prior copy, want %q", "builder", again[0], "agy/google/m")
	}
}

func TestLoadErrors(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		contains []string
	}{
		{
			name:     "bad json",
			body:     `{`,
			contains: []string{"policy.json"},
		},
		{
			name:     "unknown role",
			body:     `{"order":{"reviwer":["a/b/c"]}}`,
			contains: []string{"order.reviwer", "unknown role", "builder reviewer researcher"},
		},
		{
			name:     "null list",
			body:     `{"order":{"builder":null}}`,
			contains: []string{"order.builder", "must be an array"},
		},
		{
			name:     "bad token",
			body:     `{"order":{"builder":["claude/sonnet"]}}`,
			contains: []string{"order.builder[0]", "harness/provider/model"},
		},
		{
			name:     "empty token",
			body:     `{"order":{"builder":[""]}}`,
			contains: []string{"order.builder[0]"},
		},
		{
			name:     "duplicate",
			body:     `{"order":{"builder":["a/b/c","x/y/z","a/b/c"]}}`,
			contains: []string{"order.builder[2]", "duplicate", "a/b/c"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := load(t, tt.body)
			if err == nil {
				t.Fatalf("Load: got nil error, want one wrapping ErrBadPolicy")
			}
			if !errors.Is(err, ErrBadPolicy) {
				t.Fatalf("Load error %v does not wrap ErrBadPolicy", err)
			}
			for _, want := range tt.contains {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("Load error %q does not contain %q", err.Error(), want)
				}
			}
		})
	}
}

func TestSwitchLimit(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		want     int
		wantErr  bool
		contains []string
	}{
		{name: "absent defaults", body: `{}`, want: DefaultMaxSwitches},
		{name: "zero disables switching", body: `{"max_switches":0}`, want: 0},
		{name: "positive", body: `{"max_switches":5}`, want: 5},
		{
			name:     "negative",
			body:     `{"max_switches":-1}`,
			wantErr:  true,
			contains: []string{"max_switches", "must be >= 0"},
		},
		{
			name:    "wrong type",
			body:    `{"max_switches":"two"}`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := load(t, tt.body)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Load: got nil error, want one wrapping ErrBadPolicy")
				}
				if !errors.Is(err, ErrBadPolicy) {
					t.Fatalf("Load error %v does not wrap ErrBadPolicy", err)
				}
				for _, want := range tt.contains {
					if !strings.Contains(err.Error(), want) {
						t.Fatalf("Load error %q does not contain %q", err.Error(), want)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if got := p.SwitchLimit(); got != tt.want {
				t.Fatalf("SwitchLimit() = %d, want %d", got, tt.want)
			}
		})
	}

	if got := (Policy{}).SwitchLimit(); got != DefaultMaxSwitches {
		t.Fatalf("Policy{}.SwitchLimit() = %d, want %d", got, DefaultMaxSwitches)
	}
}

func TestLoadEmptyOrderIsValid(t *testing.T) {
	for _, body := range []string{`{"order":{}}`, `{}`} {
		p, err := load(t, body)
		if err != nil {
			t.Fatalf("Load(%q): %v", body, err)
		}
		if got := p.OrderFor("builder"); got != nil {
			t.Fatalf("Load(%q): OrderFor(%q) = %v, want nil", body, "builder", got)
		}
	}
}

func TestLimitGateDefault(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		want     time.Duration
		wantErr  bool
		contains []string
	}{
		{name: "absent defaults", body: `{}`, want: DefaultLimitGate},
		{name: "custom duration", body: `{"limit_gate_default_ms":1800000}`, want: 30 * time.Minute},
		{
			name:     "zero",
			body:     `{"limit_gate_default_ms":0}`,
			wantErr:  true,
			contains: []string{"limit_gate_default_ms", "must be > 0"},
		},
		{
			name:     "negative",
			body:     `{"limit_gate_default_ms":-5}`,
			wantErr:  true,
			contains: []string{"limit_gate_default_ms", "must be > 0"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := load(t, tt.body)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Load: got nil error, want one wrapping ErrBadPolicy")
				}
				if !errors.Is(err, ErrBadPolicy) {
					t.Fatalf("Load error %v does not wrap ErrBadPolicy", err)
				}
				for _, want := range tt.contains {
					if !strings.Contains(err.Error(), want) {
						t.Fatalf("Load error %q does not contain %q", err.Error(), want)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if got := p.LimitGateDefault(); got != tt.want {
				t.Fatalf("LimitGateDefault() = %v, want %v", got, tt.want)
			}
		})
	}

	if got := (Policy{}).LimitGateDefault(); got != DefaultLimitGate {
		t.Fatalf("Policy{}.LimitGateDefault() = %v, want %v", got, DefaultLimitGate)
	}
}

// msDurationCases is the shared shape of every *_ms policy knob: nil is a
// pinned default, a present value converts from milliseconds, and 0 is a
// load error naming the key.
var msDurationCases = []struct {
	key        string
	literal    time.Duration // the default's documented value, pinned independently of the const
	overrideMS int
	get        func(Policy) time.Duration
}{
	{"stall_after_ms", 15 * time.Minute, 60000, Policy.StallAfter},
	{"progress_interval_ms", 30 * time.Second, 5000, Policy.ProgressInterval},
	{"explore_after_ms", 20 * time.Minute, 600000, Policy.ExploreAfter},
	{"stale_after_ms", 4 * time.Hour, 1800000, Policy.StaleAfter},
}

func TestMSDurationDefaultsAndOverrides(t *testing.T) {
	for _, tc := range msDurationCases {
		t.Run(tc.key, func(t *testing.T) {
			if got := tc.get(Policy{}); got != tc.literal {
				t.Fatalf("%s default = %v, want %v", tc.key, got, tc.literal)
			}

			p, err := load(t, `{}`)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if got := tc.get(p); got != tc.literal {
				t.Fatalf("%s() with no key = %v, want %v", tc.key, got, tc.literal)
			}

			p, err = load(t, fmt.Sprintf(`{%q:%d}`, tc.key, tc.overrideMS))
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			want := time.Duration(tc.overrideMS) * time.Millisecond
			if got := tc.get(p); got != want {
				t.Fatalf("%s() with %d = %v, want %v", tc.key, tc.overrideMS, got, want)
			}

			_, err = load(t, fmt.Sprintf(`{%q:0}`, tc.key))
			if err == nil {
				t.Fatalf("Load with %s 0: got nil error, want one wrapping ErrBadPolicy", tc.key)
			}
			if !errors.Is(err, ErrBadPolicy) {
				t.Fatalf("Load error %v does not wrap ErrBadPolicy", err)
			}
			for _, want := range []string{tc.key, "must be > 0"} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("Load error %q does not contain %q", err.Error(), want)
				}
			}
		})
	}
}

func TestGatePolicy(t *testing.T) {
	t.Run("nil Gate defaults", func(t *testing.T) {
		p := Policy{}
		if got := p.GateDefault(); got != "" {
			t.Errorf("GateDefault() = %q, want \"\"", got)
		}
		if got := p.GateTimeout(); got != DefaultGateTimeout {
			t.Errorf("GateTimeout() = %v, want %v", got, DefaultGateTimeout)
		}
	})

	t.Run("default and timeout_ms honoured", func(t *testing.T) {
		body := `{"gate":{"default":"make check","timeout_ms":1800000}}`
		p, err := load(t, body)
		if err != nil {
			t.Fatalf("Load: unexpected error %v", err)
		}
		if got := p.GateDefault(); got != "make check" {
			t.Errorf("GateDefault() = %q, want %q", got, "make check")
		}
		if got := p.GateTimeout(); got != 30*time.Minute {
			t.Errorf("GateTimeout() = %v, want %v", got, 30*time.Minute)
		}
	})

	t.Run("absent timeout_ms defaults", func(t *testing.T) {
		body := `{"gate":{"default":"make check"}}`
		p, err := load(t, body)
		if err != nil {
			t.Fatalf("Load: unexpected error %v", err)
		}
		if got := p.GateTimeout(); got != DefaultGateTimeout {
			t.Errorf("GateTimeout() = %v, want %v", got, DefaultGateTimeout)
		}
	})

	badCases := []struct {
		name     string
		body     string
		contains string
	}{
		{"timeout_ms zero", `{"gate":{"timeout_ms":0}}`, "gate.timeout_ms: must be > 0, got 0"},
		{"timeout_ms negative", `{"gate":{"timeout_ms":-5}}`, "gate.timeout_ms: must be > 0, got -5"},
	}

	for _, bc := range badCases {
		t.Run(bc.name, func(t *testing.T) {
			_, err := load(t, bc.body)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !errors.Is(err, ErrBadPolicy) {
				t.Fatalf("error %v does not wrap ErrBadPolicy", err)
			}
			if !strings.Contains(err.Error(), bc.contains) {
				t.Errorf("error %q does not contain %q", err.Error(), bc.contains)
			}
		})
	}
}

func TestScanPatterns(t *testing.T) {
	t.Run("bad regex returns ErrBadPolicy naming index 0", func(t *testing.T) {
		body := `{"scan_patterns": ["("]}`
		_, err := load(t, body)
		if err == nil {
			t.Fatalf("Load: got nil error, want ErrBadPolicy")
		}
		if !errors.Is(err, ErrBadPolicy) {
			t.Fatalf("Load error %v does not wrap ErrBadPolicy", err)
		}
		if !strings.Contains(err.Error(), "scan_patterns[0]") {
			t.Fatalf("Load error %q does not name index 0 (scan_patterns[0])", err.Error())
		}
	})

	t.Run("valid patterns load", func(t *testing.T) {
		body := `{"scan_patterns": ["foo.*bar", "(?i)baz"]}`
		p, err := load(t, body)
		if err != nil {
			t.Fatalf("Load: unexpected error %v", err)
		}
		want := []string{"foo.*bar", "(?i)baz"}
		if !reflect.DeepEqual(p.ScanPatterns, want) {
			t.Fatalf("ScanPatterns = %v, want %v", p.ScanPatterns, want)
		}
	})
}

func TestClassifyPolicy(t *testing.T) {
	t.Run("nil receiver accessors return defaults", checkClassifyNilDefaults)
	t.Run("valid block with defaults", checkClassifyBlockDefaults)
	t.Run("explicit values honoured", checkClassifyExplicitValues)
	t.Run("1.0 accepted", checkClassifyThresholdOne)

	badCases := []struct {
		name     string
		body     string
		contains string
	}{
		{"provider missing", `{"classify":{}}`, "classify.provider: required"},
		{"provider other", `{"classify":{"provider":"other"}}`, "classify.provider: unknown \"other\" (known: jev)"},
		{"threshold 0", `{"classify":{"provider":"jev","injection_threshold":0}}`, "classify.injection_threshold: must be in (0, 1], got 0"},
		{"threshold negative", `{"classify":{"provider":"jev","injection_threshold":-0.1}}`, "classify.injection_threshold: must be in (0, 1], got -0.1"},
		{"threshold 1.5", `{"classify":{"provider":"jev","injection_threshold":1.5}}`, "classify.injection_threshold: must be in (0, 1], got 1.5"},
		{"timeout 0", `{"classify":{"provider":"jev","timeout_ms":0}}`, "classify.timeout_ms: must be > 0, got 0"},
		{"timeout negative", `{"classify":{"provider":"jev","timeout_ms":-10}}`, "classify.timeout_ms: must be > 0, got -10"},
	}

	for _, bc := range badCases {
		t.Run(bc.name, func(t *testing.T) {
			_, err := load(t, bc.body)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !errors.Is(err, ErrBadPolicy) {
				t.Fatalf("error %v does not wrap ErrBadPolicy", err)
			}
			if !strings.Contains(err.Error(), bc.contains) {
				t.Errorf("error %q does not contain %q", err.Error(), bc.contains)
			}
		})
	}
}

func checkClassifyNilDefaults(t *testing.T) {
	var c *Classify
	if got := c.ModelName(); got != DefaultClassifyModel {
		t.Errorf("ModelName() = %q, want %q", got, DefaultClassifyModel)
	}
	if got := c.Threshold(); got != DefaultInjectionThreshold {
		t.Errorf("Threshold() = %v, want %v", got, DefaultInjectionThreshold)
	}
	if got := c.Timeout(); got != DefaultClassifyTimeout {
		t.Errorf("Timeout() = %v, want %v", got, DefaultClassifyTimeout)
	}
}

func checkClassifyBlockDefaults(t *testing.T) {
	p, err := load(t, `{"classify":{"provider":"jev"}}`)
	if err != nil {
		t.Fatalf("Load: unexpected error %v", err)
	}
	if p.Classify == nil {
		t.Fatal("Classify is nil")
	}
	if got := p.Classify.ModelName(); got != "jev-latest" {
		t.Errorf("ModelName() = %q, want jev-latest", got)
	}
	if got := p.Classify.Threshold(); got != 0.7 {
		t.Errorf("Threshold() = %v, want 0.7", got)
	}
	if got := p.Classify.Timeout(); got != 4*time.Second {
		t.Errorf("Timeout() = %v, want 4s", got)
	}
}

func checkClassifyExplicitValues(t *testing.T) {
	body := `{"classify":{"provider":"jev","model":"jev-v2","injection_threshold":0.85,"timeout_ms":2500}}`
	p, err := load(t, body)
	if err != nil {
		t.Fatalf("Load: unexpected error %v", err)
	}
	if p.Classify == nil {
		t.Fatal("Classify is nil")
	}
	if got := p.Classify.ModelName(); got != "jev-v2" {
		t.Errorf("ModelName() = %q, want jev-v2", got)
	}
	if got := p.Classify.Threshold(); got != 0.85 {
		t.Errorf("Threshold() = %v, want 0.85", got)
	}
	if got := p.Classify.Timeout(); got != 2500*time.Millisecond {
		t.Errorf("Timeout() = %v, want 2.5s", got)
	}
}

func checkClassifyThresholdOne(t *testing.T) {
	p, err := load(t, `{"classify":{"provider":"jev","injection_threshold":1.0}}`)
	if err != nil {
		t.Fatalf("Load: unexpected error %v", err)
	}
	if got := p.Classify.Threshold(); got != 1.0 {
		t.Errorf("Threshold() = %v, want 1.0", got)
	}
}

func TestTierPolicy(t *testing.T) {
	t.Run("max_tier default is edit", checkTierDefaultIsEdit)
	t.Run("max_tier yolo and tier builder yolo loads", checkTierYoloLoads)

	badCases := []struct {
		name     string
		body     string
		contains string
	}{
		{"tier builder yolo with default cap exceeds max_tier", `{"tier":{"builder":"yolo"}}`, "exceeds max_tier"},
		{"max_tier harness is rejected", `{"max_tier":"harness"}`, `"harness" is not a cap`},
		{"unknown role key in tier is rejected", `{"tier":{"wizard":"read"}}`, "unknown role"},
		{"invalid max_tier string is rejected", `{"max_tier":"god"}`, "max_tier:"},
		{"invalid tier value string is rejected", `{"tier":{"builder":"invalid"}}`, "tier.builder:"},
	}
	for _, bc := range badCases {
		t.Run(bc.name, func(t *testing.T) {
			_, err := load(t, bc.body)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !errors.Is(err, ErrBadPolicy) {
				t.Fatalf("error %v does not wrap ErrBadPolicy", err)
			}
			if !strings.Contains(err.Error(), bc.contains) {
				t.Errorf("error %q does not contain %q", err.Error(), bc.contains)
			}
		})
	}
}

func checkTierDefaultIsEdit(t *testing.T) {
	p := Policy{}
	if p.MaxTierOrDefault() != harness.TierEdit {
		t.Errorf("Policy{}.MaxTierOrDefault() = %v, want %v", p.MaxTierOrDefault(), harness.TierEdit)
	}
	pLoaded, err := load(t, `{}`)
	if err != nil {
		t.Fatalf("Load({}) err: %v", err)
	}
	if pLoaded.MaxTierOrDefault() != harness.TierEdit {
		t.Errorf("pLoaded.MaxTierOrDefault() = %v, want %v", pLoaded.MaxTierOrDefault(), harness.TierEdit)
	}
}

func checkTierYoloLoads(t *testing.T) {
	p, err := load(t, `{"max_tier":"yolo","tier":{"builder":"yolo"}}`)
	if err != nil {
		t.Fatalf("Load unexpected err: %v", err)
	}
	if p.MaxTierOrDefault() != harness.TierYolo {
		t.Errorf("MaxTierOrDefault() = %v, want %v", p.MaxTierOrDefault(), harness.TierYolo)
	}
	got, ok := p.TierFor("builder")
	if !ok || got != harness.TierYolo {
		t.Errorf("TierFor(\"builder\") = (%v, %v), want (yolo, true)", got, ok)
	}
	if _, ok = p.TierFor("reviewer"); ok {
		t.Errorf("TierFor(\"reviewer\") ok = true, want false")
	}
}

// TestGateRegateDefaultAndValidation pins gate.regate: nil is 0.
func TestGateRegateDefaultAndValidation(t *testing.T) {
	if got := (Policy{}).GateRegate(); got != 0 {
		t.Fatalf("Policy{}.GateRegate() = %d, want 0", got)
	}

	p, err := load(t, `{"gate":{"regate":2}}`)
	if err != nil {
		t.Fatalf("Load: unexpected error %v", err)
	}
	if got := p.GateRegate(); got != 2 {
		t.Fatalf("GateRegate() = %d, want 2", got)
	}
	if got := p.GateDefault(); got != "" {
		t.Fatalf("GateRegate must not disturb GateDefault: got %q", got)
	}

	p, err = load(t, `{"gate":{"default":"make check"}}`)
	if err != nil {
		t.Fatalf("Load: unexpected error %v", err)
	}
	if got := p.GateRegate(); got != 0 {
		t.Fatalf("GateRegate() with no regate key = %d, want 0", got)
	}

	_, err = load(t, `{"gate":{"regate":-1}}`)
	if err == nil {
		t.Fatalf("Load with gate.regate -1: got nil error, want one wrapping ErrBadPolicy")
	}
	if !errors.Is(err, ErrBadPolicy) {
		t.Fatalf("Load error %v does not wrap ErrBadPolicy", err)
	}
	for _, want := range []string{"gate.regate", "must be >= 0"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Load error %q does not contain %q", err.Error(), want)
		}
	}

	if got := (Policy{Gate: &GatePolicy{}}).GateRegate(); got != 0 {
		t.Fatalf("GatePolicy{}.GateRegate() = %d, want 0", got)
	}
}

// TestVerifyDefault pins that a policy with no verify key is false.
func TestVerifyDefault(t *testing.T) {
	if got := (Policy{}).VerifyDefault(); got {
		t.Fatalf("Policy{}.VerifyDefault() = true, want false")
	}
	if got := (Policy{Verify: &VerifyPolicy{}}).VerifyDefault(); got {
		t.Fatalf("VerifyPolicy{}.VerifyDefault() = true, want false")
	}

	p, err := load(t, `{"verify":{"default":true}}`)
	if err != nil {
		t.Fatalf("Load: unexpected error %v", err)
	}
	if !p.VerifyDefault() {
		t.Fatalf("verify.default true did not reach VerifyDefault()")
	}
	if got := p.GateDefault(); got != "" {
		t.Fatalf("VerifyDefault must not disturb GateDefault: got %q", got)
	}
}

var notifyBadCases = []struct {
	name     string
	body     string
	contains string
}{
	{"bad scheme", `{"notify":{"webhooks":[{"url":"ftp://example.com/hook"}]}}`, "notify.webhooks[0].url"},
	{"missing url", `{"notify":{"webhooks":[{"url":""}]}}`, "notify.webhooks[0].url"},
	{
		"bad format",
		`{"notify":{"webhooks":[{"url":"https://example.com/hook","format":"teams"}]}}`,
		"notify.webhooks[0].format: unknown \"teams\"",
	},
	{
		"unknown event",
		`{"notify":{"webhooks":[{"url":"https://example.com/hook","events":["frobnicated"]}]}}`,
		"notify.webhooks[0].events[0]: unknown event \"frobnicated\"",
	},
	{
		"state suffix on the wrong event",
		`{"notify":{"webhooks":[{"url":"https://example.com/hook","events":["round_started:x"]}]}}`,
		"notify.webhooks[0].events[0]",
	},
}

func TestNotifyPolicy(t *testing.T) {
	t.Run("nil Notify is fine", checkNotifyNil)
	t.Run("two webhooks load", checkNotifyTwoWebhooks)

	for _, bc := range notifyBadCases {
		t.Run(bc.name, func(t *testing.T) {
			_, err := load(t, bc.body)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !errors.Is(err, ErrBadPolicy) {
				t.Fatalf("error %v does not wrap ErrBadPolicy", err)
			}
			if !strings.Contains(err.Error(), bc.contains) {
				t.Errorf("error %q does not contain %q", err.Error(), bc.contains)
			}
		})
	}
}

func checkNotifyNil(t *testing.T) {
	p, err := load(t, `{}`)
	if err != nil {
		t.Fatalf("Load: unexpected error %v", err)
	}
	if p.Notify != nil {
		t.Fatalf("Notify = %v, want nil", p.Notify)
	}
}

func checkNotifyTwoWebhooks(t *testing.T) {
	body := `{"notify":{"webhooks":[
		{"url":"https://hooks.slack.com/services/x","format":"slack","events":["state_changed:needs_you","binding_stale"]},
		{"url":"https://example.com/hook","format":"json"}
	]}}`
	p, err := load(t, body)
	if err != nil {
		t.Fatalf("Load: unexpected error %v", err)
	}
	if p.Notify == nil {
		t.Fatalf("Notify = nil, want populated")
	}
	if len(p.Notify.Webhooks) != 2 {
		t.Fatalf("len(Webhooks) = %d, want 2", len(p.Notify.Webhooks))
	}

	first := p.Notify.Webhooks[0]
	if first.URL != "https://hooks.slack.com/services/x" {
		t.Errorf("Webhooks[0].URL = %q", first.URL)
	}
	if first.Format != "slack" {
		t.Errorf("Webhooks[0].Format = %q, want slack", first.Format)
	}
	wantEvents := []string{"state_changed:needs_you", "binding_stale"}
	if !reflect.DeepEqual(first.Events, wantEvents) {
		t.Errorf("Webhooks[0].Events = %v, want %v", first.Events, wantEvents)
	}

	second := p.Notify.Webhooks[1]
	if second.Format != "json" {
		t.Errorf("Webhooks[1].Format = %q, want json", second.Format)
	}
	if len(second.Events) != 0 {
		t.Errorf("Webhooks[1].Events = %v, want empty (no filter)", second.Events)
	}
}

func TestMaxBuildersOrDefault(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		want    int
		wantErr bool
	}{
		{name: "absent defaults to max(1, NumCPU-1)", body: `{}`, want: maxInt(1, runtime.NumCPU()-1)},
		{name: "one is ok", body: `{"serve":{"max_builders":1}}`, want: 1},
		{name: "zero is an error", body: `{"serve":{"max_builders":0}}`, wantErr: true},
		{name: "negative is an error", body: `{"serve":{"max_builders":-1}}`, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := load(t, tt.body)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Load: got nil error, want one wrapping ErrBadPolicy")
				}
				if !errors.Is(err, ErrBadPolicy) {
					t.Fatalf("Load error %v does not wrap ErrBadPolicy", err)
				}
				if !strings.Contains(err.Error(), "serve.max_builders") {
					t.Fatalf("Load error %q does not mention serve.max_builders", err.Error())
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if got := p.MaxBuildersOrDefault(); got != tt.want {
				t.Fatalf("MaxBuildersOrDefault() = %d, want %d", got, tt.want)
			}
		})
	}

	if got := (Policy{}).MaxBuildersOrDefault(); got != maxInt(1, runtime.NumCPU()-1) {
		t.Fatalf("Policy{}.MaxBuildersOrDefault() = %d, want %d", got, maxInt(1, runtime.NumCPU()-1))
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func TestServeScopeValidation(t *testing.T) {
	goodCases := []struct {
		name string
		body string
	}{
		{"slice ends in .slice", `{"serve":{"scope":{"slice":"relevo.slice"}}}`},
		{"cpu_weight in range", `{"serve":{"scope":{"cpu_weight":500}}}`},
		{"memory_max matches", `{"serve":{"scope":{"memory_max":"512M"}}}`},
		{"tasks_max positive", `{"serve":{"scope":{"tasks_max":10}}}`},
	}
	for _, gc := range goodCases {
		t.Run(gc.name, func(t *testing.T) {
			if _, err := load(t, gc.body); err != nil {
				t.Fatalf("Load: %v", err)
			}
		})
	}

	badCases := []struct {
		name     string
		body     string
		contains string
	}{
		{"slice missing suffix", `{"serve":{"scope":{"slice":"relevo"}}}`, "serve.scope.slice: must end in \".slice\""},
		{"cpu_weight above range", `{"serve":{"scope":{"cpu_weight":10001}}}`, "serve.scope.cpu_weight: must be 1..10000"},
		{"cpu_weight negative", `{"serve":{"scope":{"cpu_weight":-1}}}`, "serve.scope.cpu_weight: must be 1..10000"},
		{"memory_max does not match", `{"serve":{"scope":{"memory_max":"512x"}}}`, "serve.scope.memory_max: must match"},
		{"tasks_max negative", `{"serve":{"scope":{"tasks_max":-1}}}`, "serve.scope.tasks_max: must be at least 1"},
	}
	for _, bc := range badCases {
		t.Run(bc.name, func(t *testing.T) {
			_, err := load(t, bc.body)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !errors.Is(err, ErrBadPolicy) {
				t.Fatalf("error %v does not wrap ErrBadPolicy", err)
			}
			if !strings.Contains(err.Error(), bc.contains) {
				t.Errorf("error %q does not contain %q", err.Error(), bc.contains)
			}
		})
	}
}

// TestScopeQuotaValidation pins cpu_quota in both scope blocks.
func TestScopeQuotaValidation(t *testing.T) {
	blocks := []struct {
		name   string
		body   string // %q is the quota
		prefix string // the exact path in the error, block-qualified
	}{
		{"scope", `{"scope":{"cpu_quota":%q}}`, ": scope.cpu_quota:"},
		{"serve.scope", `{"serve":{"scope":{"cpu_quota":%q}}}`, ": serve.scope.cpu_quota:"},
	}

	for _, bc := range blocks {
		for _, quota := range []string{"200%", "1%", "1000%"} {
			t.Run(bc.name+"/good/"+quota, func(t *testing.T) {
				if _, err := load(t, fmt.Sprintf(bc.body, quota)); err != nil {
					t.Fatalf("Load: %v", err)
				}
			})
		}
		for _, quota := range []string{"200", "200 %", "%", "0%"} {
			t.Run(bc.name+"/bad/"+quota, func(t *testing.T) {
				_, err := load(t, fmt.Sprintf(bc.body, quota))
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if !errors.Is(err, ErrBadPolicy) {
					t.Fatalf("error %v does not wrap ErrBadPolicy", err)
				}
				if !strings.Contains(err.Error(), "scope.cpu_quota") {
					t.Errorf("error %q does not mention scope.cpu_quota", err.Error())
				}
				if !strings.Contains(err.Error(), bc.prefix) {
					t.Errorf("error %q does not name the %s block (%q)", err.Error(), bc.name, bc.prefix)
				}
			})
		}
	}
}

// TestScopeGateQuotaValidation pins gate_cpu_quota in both scope blocks.
func TestScopeGateQuotaValidation(t *testing.T) {
	blocks := []struct {
		name   string
		body   string // %q is the quota
		prefix string // the exact path in the error, block-qualified
	}{
		{"scope", `{"scope":{"gate_cpu_quota":%q}}`, ": scope.gate_cpu_quota:"},
		{"serve.scope", `{"serve":{"scope":{"gate_cpu_quota":%q}}}`, ": serve.scope.gate_cpu_quota:"},
	}

	for _, bc := range blocks {
		for _, quota := range []string{"300%", "1%", "1000%"} {
			t.Run(bc.name+"/good/"+quota, func(t *testing.T) {
				if _, err := load(t, fmt.Sprintf(bc.body, quota)); err != nil {
					t.Fatalf("Load: %v", err)
				}
			})
		}
		for _, quota := range []string{"300", "0%", "abc%"} {
			t.Run(bc.name+"/bad/"+quota, func(t *testing.T) {
				_, err := load(t, fmt.Sprintf(bc.body, quota))
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if !errors.Is(err, ErrBadPolicy) {
					t.Fatalf("error %v does not wrap ErrBadPolicy", err)
				}
				if !strings.Contains(err.Error(), "gate_cpu_quota") {
					t.Errorf("error %q does not mention gate_cpu_quota", err.Error())
				}
				if !strings.Contains(err.Error(), bc.prefix) {
					t.Errorf("error %q does not name the %s block (%q)", err.Error(), bc.name, bc.prefix)
				}
			})
		}
	}
}

// TestScopeForWholeBlockOverride pins that serve.scope replaces the
// top-level block entirely for a served round, with nothing leaking in.
func TestScopeForWholeBlockOverride(t *testing.T) {
	top := &ScopePolicy{Slice: "top.slice", CPUWeight: 111, MemoryMax: "1G", CPUQuota: "10%", TasksMax: 11}
	served := &ScopePolicy{Slice: "serve.slice", CPUWeight: 222, MemoryMax: "2G", CPUQuota: "20%", TasksMax: 22}

	t.Run("serve.scope replaces the whole block", func(t *testing.T) {
		p := Policy{Scope: top, Serve: &ServePolicy{Scope: served}}
		got := p.ScopeFor(true)
		if got != served {
			t.Fatalf("ScopeFor(true) = %+v, want the serve.scope block", got)
		}
		if got.Slice != served.Slice || got.CPUWeight != served.CPUWeight ||
			got.MemoryMax != served.MemoryMax || got.CPUQuota != served.CPUQuota || got.TasksMax != served.TasksMax {
			t.Errorf("ScopeFor(true) = %+v, want every field from serve.scope", got)
		}
		if got.Slice == top.Slice || got.CPUWeight == top.CPUWeight ||
			got.MemoryMax == top.MemoryMax || got.CPUQuota == top.CPUQuota || got.TasksMax == top.TasksMax {
			t.Errorf("ScopeFor(true) = %+v, a top-level field leaked in", got)
		}
	})

	t.Run("absent serve.scope falls back", func(t *testing.T) {
		p := Policy{Scope: top, Serve: &ServePolicy{}}
		if got := p.ScopeFor(true); got != top {
			t.Fatalf("ScopeFor(true) = %+v, want the top-level block", got)
		}
	})

	t.Run("both absent is nil", func(t *testing.T) {
		if got := (Policy{}).ScopeFor(true); got != nil {
			t.Fatalf("ScopeFor(true) = %+v, want nil", got)
		}
		if got := (Policy{Serve: &ServePolicy{}}).ScopeFor(true); got != nil {
			t.Fatalf("ScopeFor(true) with an empty serve = %+v, want nil", got)
		}
	})

	t.Run("unserved always uses the top-level block", func(t *testing.T) {
		p := Policy{Scope: top, Serve: &ServePolicy{Scope: served}}
		if got := p.ScopeFor(false); got != top {
			t.Fatalf("ScopeFor(false) = %+v, want the top-level block", got)
		}
		if got := (Policy{Serve: &ServePolicy{Scope: served}}).ScopeFor(false); got != nil {
			t.Fatalf("ScopeFor(false) with no top-level block = %+v, want nil", got)
		}
	})
}

// TestParseCPUList pins the cpu-list grammar: sorted, de-duplicated cores;
// a backwards range or a core above 1023 is an error.
func TestParseCPUList(t *testing.T) {
	good := []struct {
		in   string
		want []int
	}{
		{"0", []int{0}},
		{"0-2", []int{0, 1, 2}},
		{"1,3,5-7", []int{1, 3, 5, 6, 7}},
		{"3,1-3", []int{1, 2, 3}},
	}
	for _, c := range good {
		t.Run("good/"+c.in, func(t *testing.T) {
			got, err := ParseCPUList(c.in)
			if err != nil {
				t.Fatalf("ParseCPUList(%q): %v", c.in, err)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("ParseCPUList(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}

	for _, in := range []string{"", "0-", "a", "1 ,2", "3-1", "0-1024"} {
		t.Run("bad/"+in, func(t *testing.T) {
			if _, err := ParseCPUList(in); err == nil {
				t.Fatalf("ParseCPUList(%q): got nil error, want one", in)
			}
		})
	}
}

// TestScopeAllowedCPUsValidation pins allowed_cpus in both blocks: a good
// cpu-list loads, a backwards range is refused.
func TestScopeAllowedCPUsValidation(t *testing.T) {
	blocks := []struct {
		name   string
		served bool
		body   string // %q is the cpu-list
		prefix string // the exact path in the error, block-qualified
	}{
		{"scope", false, `{"scope":{"allowed_cpus":%q}}`, ": scope.allowed_cpus:"},
		{"serve.scope", true, `{"serve":{"scope":{"allowed_cpus":%q}}}`, ": serve.scope.allowed_cpus:"},
	}

	for _, bc := range blocks {
		t.Run(bc.name+"/good", func(t *testing.T) {
			p, err := load(t, fmt.Sprintf(bc.body, "0-2"))
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			sc := p.ScopeFor(bc.served)
			if sc == nil || sc.AllowedCPUs != "0-2" {
				t.Fatalf("AllowedCPUs = %+v, want the loaded pool", sc)
			}
		})
		t.Run(bc.name+"/bad", func(t *testing.T) {
			_, err := load(t, fmt.Sprintf(bc.body, "3-1"))
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !errors.Is(err, ErrBadPolicy) {
				t.Fatalf("error %v does not wrap ErrBadPolicy", err)
			}
			for _, want := range []string{bc.prefix, "runs backwards"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not contain %q", err.Error(), want)
				}
			}
		})
	}
}

// TestServeUnknownKeyWarns pins that an unknown key inside serve or scope
// warns rather than rejecting the file.
func TestServeUnknownKeyWarns(t *testing.T) {
	for _, body := range []string{`{"serve":{"frobnicate":true}}`, `{"scope":{"frobnicate":true}}`} {
		_, warnings := loadWarnings(t, body)
		if len(warnings) != 1 {
			t.Fatalf("Load(%s): warnings = %v, want exactly one", body, warnings)
		}
		if !strings.Contains(warnings[0], "unknown key") || !strings.Contains(warnings[0], "frobnicate") {
			t.Errorf("Load(%s): warning %q does not name the unknown key", body, warnings[0])
		}
	}
}

// TestLoadAcceptsNameOrder pins that an order entry is a candidate name or a
// harness/provider/model token.
func TestLoadAcceptsNameOrder(t *testing.T) {
	for _, body := range []string{
		`{"order":{"builder":["sonnet"]}}`,
		`{"order":{"builder":["a"]}}`,
		`{"order":{"builder":["claude/anthropic/sonnet"]}}`,
	} {
		if _, err := load(t, body); err != nil {
			t.Errorf("Load(%s) = %v, want no error", body, err)
		}
	}

	_, err := load(t, `{"order":{"builder":["A/b"]}}`)
	if err == nil {
		t.Fatal("Load accepted A/b, want the bad-policy error")
	}
	want := `order.builder[0]: "A/b": want a candidate name or harness/provider/model`
	if !strings.Contains(err.Error(), want) {
		t.Errorf("Load(A/b) err = %q, want it containing %q", err.Error(), want)
	}
	if !errors.Is(err, ErrBadPolicy) {
		t.Errorf("Load(A/b) err = %v, want ErrBadPolicy", err)
	}
}

// TestArtifactMaxMBValidates pins the artifact size cap's policy rule: a
// negative artifact_max_mb is refused, and unset or 0 both mean the 25 MB
// default (ArtifactMaxBytes).
func TestArtifactMaxMBValidates(t *testing.T) {
	t.Parallel()

	if _, err := load(t, `{"artifact_max_mb":-1}`); !errors.Is(err, ErrBadPolicy) {
		t.Errorf("negative artifact_max_mb err = %v, want ErrBadPolicy", err)
	}
	if _, err := load(t, `{"artifact_max_mb":-1}`); err == nil || !strings.Contains(err.Error(), "artifact_max_mb") {
		t.Errorf("negative artifact_max_mb err = %v, want it to name artifact_max_mb", err)
	}

	cases := []struct {
		name string
		body string
		want int64
	}{
		{"unset falls back to the default", `{}`, DefaultArtifactMaxMB << 20},
		{"0 falls back to the default", `{"artifact_max_mb":0}`, DefaultArtifactMaxMB << 20},
		{"1 is 1 MB", `{"artifact_max_mb":1}`, 1 << 20},
		{"explicit 25", `{"artifact_max_mb":25}`, 25 << 20},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p, err := load(t, tc.body)
			if err != nil {
				t.Fatalf("load(%s): %v", tc.body, err)
			}
			if got := p.ArtifactMaxBytes(); got != tc.want {
				t.Errorf("ArtifactMaxBytes(%s) = %d, want %d", tc.body, got, tc.want)
			}
		})
	}
}
