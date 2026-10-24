package usage

import (
	"os"
	"strings"
	"testing"
)

func TestMuseStream(t *testing.T) {
	got := museStream(mustOpen(t, "testdata/muse-stream.jsonl"), "meta", "muse-spark-1.3")
	if len(got) != 1 {
		t.Fatalf("samples = %d, want 1", len(got))
	}
	want := Tokens{In: 9273123 - 9073518, CacheRead: 9073518, Out: 56699}
	if got[0].Tokens != want {
		t.Errorf("tokens = %+v, want %+v", got[0].Tokens, want)
	}
	if s := got[0]; s.Provider != "meta" || s.Model != "muse-spark-1.3" || s.HasCost {
		t.Errorf("sample = %+v, want Provider meta, Model muse-spark-1.3, HasCost false", s)
	}
}

func TestMuseStreamSumsEveryModel(t *testing.T) {
	line := `{"payload_type":"run.usage","payload":{"models":[` +
		`{"model":"a","input_tokens":100,"cached_tokens":60,"output_tokens":5,"reasoning_tokens":2,"calls":1},` +
		`{"model":"","input_tokens":10,"cached_tokens":0,"output_tokens":1,"reasoning_tokens":0,"calls":1}]}}`
	got := museStream(strings.NewReader(line+"\n"), "meta", "fallback")
	if len(got) != 2 {
		t.Fatalf("samples = %d, want 2", len(got))
	}
	if got[0].Tokens != (Tokens{In: 40, CacheRead: 60, Out: 5}) || got[0].Model != "a" {
		t.Errorf("first = %+v", got[0])
	}
	if got[1].Tokens != (Tokens{In: 10, Out: 1}) || got[1].Model != "fallback" {
		t.Errorf("second = %+v, want the passed model when the entry names none", got[1])
	}
}

func TestMuseStreamSmokeAndEmpty(t *testing.T) {
	if got := museStream(mustOpen(t, "testdata/muse-smoke.jsonl"), "meta", "m"); len(got) != 1 || got[0].Tokens.Out == 0 {
		t.Errorf("smoke samples = %+v, want one with output tokens", got)
	}
	if got := museStream(strings.NewReader(""), "meta", "m"); len(got) != 0 {
		t.Errorf("empty = %+v, want none", got)
	}
	notUsage := `{"payload_type":"tool.result","payload":{"models":[{"input_tokens":9}]}}`
	if got := museStream(strings.NewReader(notUsage), "meta", "m"); len(got) != 0 {
		t.Errorf("non-usage event = %+v, want none", got)
	}
}

func TestMuseSourceCarryStripsEffort(t *testing.T) {
	c, ok := newSourceCarry(Source{Harness: "muse", Provider: "meta", Model: "muse-spark-1.3:xhigh"})
	if !ok {
		t.Fatal("no carry for muse")
	}
	c.feed([]byte(`{"payload_type":"run.usage","payload":{"models":[{"input_tokens":1}]}}`))
	if got := c.samples(); len(got) != 1 || got[0].Model != "muse-spark-1.3" {
		t.Errorf("samples = %+v, want the model without its effort", got)
	}
}

func TestStreamStepsMuse(t *testing.T) {
	data, err := os.ReadFile("testdata/muse-stream.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	got := StreamSteps("muse", data)
	if got.Steps != 80 || got.ToolCalls != 3 {
		t.Errorf("Steps/ToolCalls = %d/%d, want 80/3", got.Steps, got.ToolCalls)
	}
}
