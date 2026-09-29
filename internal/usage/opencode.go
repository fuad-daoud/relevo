package usage

import "io"

type opencodeTokens struct {
	Input     int64 `json:"input"`
	Output    int64 `json:"output"`
	Reasoning int64 `json:"reasoning"`
	Cache     struct {
		Read  int64 `json:"read"`
		Write int64 `json:"write"`
	} `json:"cache"`
}

func (t opencodeTokens) tokens() Tokens {
	return Tokens{In: t.Input, CacheRead: t.Cache.Read, CacheWrite: t.Cache.Write, Out: t.Output + t.Reasoning}
}

type opencodeEvent struct {
	Type string `json:"type"`
	Part *struct {
		Type   string          `json:"type"`
		Cost   *float64        `json:"cost"`
		Tokens *opencodeTokens `json:"tokens"`
	} `json:"part"`
}

// opencodeStream reads a headless round's `run --format json` stream: one sample
// per step_finish part, dollars as opencode computed them.
func opencodeStream(r io.Reader, provider, model string) []Sample {
	c, _ := newCarry("opencode", provider, model)
	scanLines(r, c.feed)
	return c.samples()
}
