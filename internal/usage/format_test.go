package usage

import (
	"strings"
	"testing"
)

func TestMoney(t *testing.T) {
	cases := []struct {
		c    Cost
		want string
	}{
		{Cost{USD: 0.41, Basis: Measured}, "$0.41"},
		{Cost{USD: 0.41, Basis: Estimated}, "~$0.41"},
		{Cost{USD: 0.41, Basis: Measured, Plan: true}, "plan"},
		{Cost{USD: 0, Basis: Unknown}, "unknown"},
		{Cost{USD: 0, Basis: Measured}, "$0.00"},
		{Cost{USD: 0.004, Basis: Measured}, "<$0.01"},
		{Cost{USD: 0.005, Basis: Estimated}, "~$0.01"},
		{Cost{USD: 12.346, Basis: Measured}, "$12.35"},
	}
	for _, c := range cases {
		if got := Money(c.c); got != c.want {
			t.Errorf("Money(%+v) = %q, want %q", c.c, got, c.want)
		}
	}
}

func TestShortTokens(t *testing.T) {
	cases := map[int64]string{0: "0", 999: "999", 1000: "1k", 1500: "2k", 182_400: "182k", 999_499: "999k", 999_500: "1.0M", 2_340_000: "2.3M"}
	for in, want := range cases {
		if got := ShortTokens(in); got != want {
			t.Errorf("ShortTokens(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestShortDuration(t *testing.T) {
	cases := map[int64]string{0: "", 1: "<1m", 59_999: "<1m", 60_000: "1m", 14 * 60_000: "14m", 65 * 60_000: "1h05m", 3 * 3_600_000: "3h00m"}
	for in, want := range cases {
		if got := ShortDuration(in); got != want {
			t.Errorf("ShortDuration(%d) = %q, want %q", in, got, want)
		}
	}
}

type lineCase struct {
	name string
	u    Usage
	want string
}

func TestLine(t *testing.T) {
	cases := lineCases()
	cases = append(cases, lineMoreCases()...)
	cases = append(cases, lineStepCases()...)
	for _, c := range cases {
		if got := Line(c.u); got != c.want {
			t.Errorf("%s:\n got  %q\n want %q", c.name, got, c.want)
		}
	}
}

func lineCases() []lineCase {
	return []lineCase{
		{
			name: "measured, the spec's example",
			u: Usage{Harness: "claude", Provider: "anthropic", Model: "claude-sonnet-5", DurationMS: 14 * 60_000,
				Tokens: Tokens{In: 2_100, CacheRead: 166_000, CacheWrite: 14_000, Out: 12_000},
				Cost:   Cost{USD: 0.41, Basis: Measured}, Samples: 1},
			want: "claude/anthropic/claude-sonnet-5  14m  in 2k  cache 166k (91%)  write 14k  out 12k  $0.41",
		},
		{
			name: "measured, no cache writes",
			u: Usage{Harness: "opencode", Provider: "cline-pass", Model: "glm-5.3-flash", DurationMS: 5 * 60_000,
				Tokens: Tokens{In: 410_000, CacheRead: 3_100_000, Out: 7_000}, Cost: Cost{USD: 0.16, Basis: Measured}, Samples: 1},
			want: "opencode/cline-pass/glm-5.3-flash  5m  in 410k  cache 3.1M (88%)  write 0  out 7k  $0.16",
		},
		{
			name: "unknown, no samples",
			u:    Usage{Harness: "agy", Provider: "google", Model: "gemini-3-pro", DurationMS: 6 * 60_000, Cost: Cost{Basis: Unknown}, Note: "agy keeps no usage record"},
			want: "agy/google/gemini-3-pro  6m  unknown (agy keeps no usage record)",
		},
		{
			name: "plan lane",
			u: Usage{Harness: "claude", Provider: "anthropic", Model: "opus", DurationMS: 9 * 60_000,
				Tokens: Tokens{In: 1_200, CacheRead: 88_000, CacheWrite: 4_000, Out: 6_000}, Cost: Cost{USD: 1, Basis: Estimated, Plan: true}, Samples: 1},
			want: "claude/anthropic/opus  9m  in 1k  cache 88k (94%)  write 4k  out 6k  plan",
		},
	}
}

func lineMoreCases() []lineCase {
	return []lineCase{
		{
			name: "estimated, the issue's example",
			u: Usage{Harness: "claude", Provider: "anthropic", Model: "claude-sonnet-5", DurationMS: 14 * 60_000,
				Tokens: Tokens{In: 16_000, CacheRead: 166_000, CacheWrite: 400, Out: 12_000},
				Cost:   Cost{USD: 0.41, Basis: Estimated}, Samples: 3},
			want: "claude/anthropic/claude-sonnet-5  14m  in 16k  cache 166k (91%)  write 400  out 12k  ~$0.41",
		},
		{
			name: "measured",
			u: Usage{Harness: "opencode", Provider: "openrouter", Model: "z-ai/glm-5.3-flash", DurationMS: 90_000,
				Tokens: Tokens{In: 465_339, CacheRead: 6_656_256, Out: 57_612}, Cost: Cost{USD: 0.2983, Basis: Measured}, Samples: 95},
			want: "opencode/openrouter/z-ai/glm-5.3-flash  1m  in 465k  cache 6.7M (93%)  write 0  out 58k  $0.30",
		},
		{
			name: "unknown with tokens",
			u: Usage{Harness: "agy", Provider: "google", Model: "gemini-3.8-flash-high", DurationMS: 9 * 60_000,
				Tokens: Tokens{In: 680_097, CacheRead: 2_333_950, Out: 41_527}, Cost: Cost{Basis: Unknown}, Samples: 1,
				Note: "no price for google/gemini-3.8-flash-high"},
			want: "agy/google/gemini-3.8-flash-high  9m  in 680k  cache 2.3M (77%)  write 0  out 42k  unknown (no price for google/gemini-3.8-flash-high)",
		},
		{
			name: "unknown, no samples, other note",
			u:    Usage{Harness: "agy", Provider: "google", Model: "gemini-3.8-flash-high", DurationMS: 9 * 60_000, Cost: Cost{Basis: Unknown}, Note: "agy keeps no usage record"},
			want: "agy/google/gemini-3.8-flash-high  9m  unknown (agy keeps no usage record)",
		},
		{
			name: "adopted builder, nothing known",
			u:    Usage{Harness: "claude", Cost: Cost{Basis: Unknown}, Note: "no stream"},
			want: "claude  unknown (no stream)",
		},
		{
			name: "plan lane, no cache writes",
			u: Usage{Harness: "claude", Provider: "anthropic", Model: "claude-opus-5", DurationMS: 60_000,
				Tokens: Tokens{In: 100, Out: 50}, Cost: Cost{USD: 1, Basis: Estimated, Plan: true}, Samples: 1},
			want: "claude/anthropic/claude-opus-5  1m  in 100  cache 0 (0%)  write 0  out 50  plan",
		},
		{
			name: "no prompt tokens, no cache parenthetical",
			u:    Usage{Harness: "claude", Provider: "anthropic", Model: "m", Tokens: Tokens{Out: 5}, Cost: Cost{USD: 0.01, Basis: Measured}, Samples: 1},
			want: "claude/anthropic/m  in 0  cache 0  write 0  out 5  $0.01",
		},
	}
}

func lineStepCases() []lineCase {
	return []lineCase{
		{
			name: "steps, calls/step and step latency",
			u: Usage{Harness: "opencode", Provider: "cline-pass", Model: "glm-5.3-flash", DurationMS: 14 * 60_000,
				Tokens: Tokens{In: 2_100, CacheRead: 166_000, CacheWrite: 14_000, Out: 12_000},
				Cost:   Cost{USD: 0.41, Basis: Measured}, Samples: 1,
				Steps: 157, ToolCalls: 183, StepP50MS: 3100, FirstOutputP50MS: 640},
			want: "opencode/cline-pass/glm-5.3-flash  14m  in 2k  cache 166k (91%)  write 14k  out 12k  157 steps  1.17 calls/step  step p50 3.1s  first out p50 640ms  $0.41",
		},
		{
			name: "tool calls without steps",
			u: Usage{Harness: "codex", Provider: "openai", Model: "gpt-5", DurationMS: 60_000,
				Tokens: Tokens{In: 100, Out: 50}, Cost: Cost{USD: 0.01, Basis: Measured}, Samples: 1,
				ToolCalls: 4},
			want: "codex/openai/gpt-5  1m  in 100  cache 0 (0%)  write 0  out 50  4 tool calls  $0.01",
		},
	}
}

func TestParts(t *testing.T) {
	cases := []struct {
		name string
		u    Usage
		want []string
	}{
		{
			name: "measured",
			u: Usage{Harness: "claude", Provider: "anthropic", Model: "claude-sonnet-5", DurationMS: 9 * 60_000,
				Tokens: Tokens{In: 100, CacheRead: 15_000_000, Out: 55_000}, Cost: Cost{USD: 4.71, Basis: Measured}, Samples: 1},
			want: []string{"claude-sonnet-5", "9m", "in 100", "cache 15.0M (100%)", "write 0", "out 55k", "$4.71"},
		},
		{
			name: "unknown: the model is not repeated in the note",
			u: Usage{Harness: "claude", Provider: "anthropic", Model: "claude-sonnet-5", DurationMS: 6 * 60_000,
				Tokens: Tokens{In: 100_000, CacheRead: 3_000_000, Out: 251}, Cost: Cost{Basis: Unknown}, Samples: 2,
				Note: "no price for anthropic/claude-sonnet-5"},
			want: []string{"claude-sonnet-5", "6m", "in 100k", "cache 3.0M (97%)", "write 0", "out 251", "unknown: no price"},
		},
		{
			name: "unknown, no samples, other note",
			u:    Usage{Harness: "agy", Provider: "google", Model: "gemini-3.8-flash-high", DurationMS: 60_000, Cost: Cost{Basis: Unknown}, Note: "agy keeps no usage record"},
			want: []string{"gemini-3.8-flash-high", "1m", "unknown: agy keeps no usage record"},
		},
		{
			name: "adopted builder: harness stands in for the model",
			u:    Usage{Harness: "claude", Cost: Cost{Basis: Unknown}, Note: "shared cwd"},
			want: []string{"claude", "unknown: shared cwd"},
		},
		{
			name: "no prompt tokens: no cache part",
			u:    Usage{Model: "m", Tokens: Tokens{Out: 5}, Cost: Cost{USD: 0.01, Basis: Measured}, Samples: 1},
			want: []string{"m", "in 0", "cache 0", "write 0", "out 5", "$0.01"},
		},
	}
	for _, c := range cases {
		got := Parts(c.u)
		if strings.Join(got, "|") != strings.Join(c.want, "|") {
			t.Errorf("%s:\n got  %q\n want %q", c.name, got, c.want)
		}
	}
}

func TestLiveParts(t *testing.T) {
	cases := []struct {
		name string
		u    Usage
		want []string
	}{
		{
			name: "opencode mid-round: measured dollars",
			u: Usage{Harness: "opencode", Model: "glm-5.3-flash", DurationMS: 4 * 60_000,
				Tokens: Tokens{In: 1_800, CacheRead: 91_000, CacheWrite: 3_100, Out: 8_200}, Cost: Cost{USD: 0.04, Basis: Measured}, Samples: 3},
			want: []string{"live", "glm-5.3-flash", "4m", "in 2k", "cache 91k (95%)", "write 3k", "out 8k", "$0.04"},
		},
		{
			name: "claude mid-round: assistant events only, estimated",
			u: Usage{Harness: "claude", Model: "claude-sonnet-5", DurationMS: 2 * 60_000,
				Tokens: Tokens{In: 900, CacheRead: 40_000, CacheWrite: 1_200, Out: 2_300}, Cost: Cost{USD: 0.02, Basis: Estimated}, Samples: 1},
			want: []string{"live", "claude-sonnet-5", "2m", "in 900", "cache 40k (95%)", "write 1k", "out 2k", "~$0.02"},
		},
	}
	for _, c := range cases {
		got := LiveParts(c.u)
		if strings.Join(got, " · ") != strings.Join(c.want, " · ") {
			t.Errorf("%s:\n got  %q\n want %q", c.name, got, c.want)
		}
	}
}

func TestLiveShort(t *testing.T) {
	live := Usage{Samples: 1, Cost: Cost{USD: 0.04, Basis: Estimated}, Tokens: Tokens{In: 1_800, CacheRead: 91_000, CacheWrite: 3_100, Out: 7_100}}
	if got := LiveShort(live); got != "live ~$0.04 · 103k tok" {
		t.Errorf("LiveShort = %q", got)
	}
	if got := LiveShort(Usage{}); got != "" {
		t.Errorf("LiveShort without samples = %q, want empty", got)
	}
}
