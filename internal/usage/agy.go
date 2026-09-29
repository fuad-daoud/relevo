package usage

import (
	"io"
)

type agyUsage struct {
	Input     int64 `json:"input_tokens"`
	Output    int64 `json:"output_tokens"`
	Thinking  int64 `json:"thinking_tokens"`
	CacheRead int64 `json:"cache_read_tokens"`
}

func (u agyUsage) tokens() Tokens {
	return Tokens{In: u.Input, CacheRead: u.CacheRead, Out: u.Output + u.Thinking}
}

type agyEvent struct {
	Event string `json:"event"`
	Init  *struct {
		Model string `json:"model"`
	} `json:"init"`
	StepUpdate *struct {
		StepType string    `json:"step_type"`
		Usage    *agyUsage `json:"usage"`
	} `json:"step_update"`
	Result *struct {
		Usage *agyUsage `json:"usage"`
	} `json:"result"`
}

// agyStream reads a headless round's stream: the result event's usage, or each
// agent_response step_update with usage when there is none. agy reports no dollars
// and keeps no pane record, so there is no pane reader.
func agyStream(r io.Reader, fallbackProvider, fallbackModel string) []Sample {
	c, _ := newCarry("agy", fallbackProvider, fallbackModel)
	scanLines(r, c.feed)
	return c.samples()
}
