package harness

import (
	"errors"
	"reflect"
	"regexp"
	"testing"
	"time"
)

func museBuilderLaunch(t *testing.T, model string, tier Tier, extra ...string) (Launch, error) {
	t.Helper()
	builder, _ := RoleByName("builder")
	h, ok := Lookup("muse")
	if !ok {
		t.Fatal("muse is not a known harness")
	}
	return h.Launch("meta", model, extra, builder, tier)
}

func TestLaunchMuseArgv(t *testing.T) {
	tests := []struct {
		name  string
		model string
		tier  Tier
		want  []string
	}{
		{"yolo with effort", "muse-spark-1.3:xhigh", TierYolo, []string{
			"exec", "--json", "--model", "muse-spark-1.3", "--reasoning-effort", "xhigh",
			"--disable-reminders", "--disable-request-user-input", "--workspace", "/w",
			"-c", `run.developer_prompt="DEF"`, "the prompt", "--yolo"}},
		{"harness without effort", "muse-spark-1.3", TierHarness, []string{
			"exec", "--json", "--model", "muse-spark-1.3",
			"--disable-reminders", "--disable-request-user-input", "--workspace", "/w",
			"-c", `run.developer_prompt="DEF"`, "the prompt"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l, err := museBuilderLaunch(t, tt.model, tt.tier)
			if err != nil {
				t.Fatalf("Launch() error = %v", err)
			}
			if l.Print[l.PromptAt] != PromptPlaceholder {
				t.Errorf("PromptAt %d names %q, want the prompt placeholder", l.PromptAt, l.Print[l.PromptAt])
			}
			got := l.PrintArgs("the prompt", time.Hour, "/w", "/state", "", "", "DEF")
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("PrintArgs = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestLaunchMuseRefusals(t *testing.T) {
	for _, bad := range []string{":high", "muse-spark-1.3:"} {
		if _, err := museBuilderLaunch(t, bad, TierYolo); !errors.Is(err, ErrBadModel) {
			t.Errorf("Launch(%q) error = %v, want ErrBadModel", bad, err)
		}
	}
	for _, tier := range []Tier{TierRead, TierEdit} {
		if _, err := museBuilderLaunch(t, "muse-spark-1.3", tier); !errors.Is(err, ErrTierUnsupported) {
			t.Errorf("Launch at %s error = %v, want ErrTierUnsupported", tier, err)
		}
	}
	if _, err := museBuilderLaunch(t, "muse-spark-1.3", TierHarness, "--yolo"); err != nil {
		t.Errorf("tier harness must accept a permission flag in extra_args: %v", err)
	}
	if _, err := museBuilderLaunch(t, "muse-spark-1.3", TierYolo, "--sandbox-network"); !errors.Is(err, ErrExtraArgsPermission) {
		t.Errorf("extra_args --sandbox-network at yolo error = %v, want ErrExtraArgsPermission", err)
	}
}

func TestPrintArgsEncodesDefinitionAsJSONString(t *testing.T) {
	l, err := museBuilderLaunch(t, "muse-spark-1.3", TierHarness)
	if err != nil {
		t.Fatal(err)
	}
	got := l.PrintArgs("p", 0, "/w", "/s", "", "", "say \"hi\"\nline two")
	want := `run.developer_prompt="say \"hi\"\nline two"`
	if got[len(got)-2] != want {
		t.Errorf("definition arg = %q, want %q", got[len(got)-2], want)
	}
}

func TestDefinitionPlaceholderOnlyInMuse(t *testing.T) {
	builder, _ := RoleByName("builder")
	for _, h := range All() {
		if h.Kind == "muse" {
			continue
		}
		l, err := h.Launch("p", "m", nil, builder, TierHarness)
		if err != nil {
			continue
		}
		for _, a := range l.Print {
			if a == DefinitionPlaceholder {
				t.Errorf("%s print form carries DefinitionPlaceholder", h.Kind)
			}
		}
	}
}

func TestMuseDeleteSessionUnsupported(t *testing.T) {
	h, _ := Lookup("muse")
	if _, err := h.DeleteSession("sess-1"); !errors.Is(err, ErrSessionDeleteUnsupported) {
		t.Errorf("DeleteSession error = %v, want ErrSessionDeleteUnsupported", err)
	}
}

func TestMuseLimitPatternsMatchQuotaText(t *testing.T) {
	h, _ := Lookup("muse")
	for _, text := range []string{
		"Usage limit reached for your plan",
		`{"limit_kind":"tierLimit"}`,
		"tier limit hit",
		"error 429",
	} {
		matched := false
		for _, p := range h.LimitPatterns {
			if regexp.MustCompile(p).MatchString(text) {
				matched = true
			}
		}
		if !matched {
			t.Errorf("no muse limit pattern matches %q", text)
		}
	}
}
