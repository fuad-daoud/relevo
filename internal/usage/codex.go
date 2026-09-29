package usage

import (
	"io"
)

type codexUsage struct {
	Input      int64 `json:"input_tokens"`
	Cached     int64 `json:"cached_input_tokens"`
	CacheWrite int64 `json:"cache_write_input_tokens"`
	Output     int64 `json:"output_tokens"`
}

type codexEvent struct {
	Type  string      `json:"type"`
	Usage *codexUsage `json:"usage"`
}

// codexStream reads a headless round's `codex exec --json` stream: one sample per
// turn.completed event that carries usage. OpenAI's input_tokens includes the
// cached part (probe 2026-09-19: 34933 input, 26112 cached), hence the
// subtraction; reasoning_output_tokens is a subset of output_tokens.
func codexStream(r io.Reader, provider, model string) []Sample {
	c, _ := newCarry("codex", provider, model)
	scanLines(r, c.feed)
	return c.samples()
}
