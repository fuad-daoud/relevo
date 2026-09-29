package usage

import (
	"encoding/json"
	"time"
)

// streamCarry is the parser state a resumable stream read needs between calls, so
// the per-stream cache can feed it appended lines only and ask for the fold so
// far. feed ignores what it cannot parse.
type streamCarry interface {
	feed(line []byte)
	samples() []Sample
}

// newCarry builds the harness's carry; false when the harness has no stream
// reader.
func newCarry(harness, provider, model string) (streamCarry, bool) {
	switch harness {
	case "claude":
		return &claudeCarry{provider: provider, seen: map[string]bool{}}, true
	case "agy":
		return &agyCarry{provider: provider, model: model}, true
	case "opencode":
		return &opencodeCarry{provider: provider, model: model}, true
	case "codex":
		return &codexCarry{provider: provider, model: model}, true
	}
	return nil, false
}

// claudeCarry: assistant samples deduped by message.id, and a pending result
// event, which wins.
type claudeCarry struct {
	provider  string
	model     string
	result    *Sample
	assistant []Sample
	seen      map[string]bool
}

func (c *claudeCarry) feed(line []byte) {
	var ev claudeEvent
	if json.Unmarshal(line, &ev) != nil {
		return
	}
	switch ev.Type {
	case "system":
		if ev.Subtype == "init" && ev.Model != "" && c.model == "" {
			c.model = ev.Model
		}
	case "assistant":
		if ev.Message == nil || ev.Message.Usage == nil || c.seen[ev.Message.ID] {
			return
		}
		c.seen[ev.Message.ID] = true
		if ev.Message.Model != "" {
			c.model = ev.Message.Model
		}
		c.assistant = append(c.assistant, Sample{Provider: c.provider, Model: ev.Message.Model, Tokens: ev.Message.Usage.tokens()})
	case "result":
		if ev.Usage == nil {
			return
		}
		s := Sample{Provider: c.provider, Tokens: ev.Usage.tokens()}
		if ev.TotalCostUSD != nil {
			s.USD, s.HasCost = *ev.TotalCostUSD, true
		}
		c.result = &s
	}
}

func (c *claudeCarry) samples() []Sample {
	if c.result != nil {
		c.result.Model = c.model
		return []Sample{*c.result}
	}
	for i := range c.assistant {
		if c.assistant[i].Model == "" {
			c.assistant[i].Model = c.model
		}
	}
	return c.assistant
}

// agyCarry: the step samples, and a pending result event, which wins.
type agyCarry struct {
	provider string
	model    string
	result   *Sample
	steps    []Sample
}

func (c *agyCarry) feed(line []byte) {
	var ev agyEvent
	if json.Unmarshal(line, &ev) != nil {
		return
	}
	switch ev.Event {
	case "init":
		if ev.Init != nil && ev.Init.Model != "" {
			c.model = ev.Init.Model
		}
	case "step_update":
		if ev.StepUpdate != nil && ev.StepUpdate.StepType == "agent_response" && ev.StepUpdate.Usage != nil {
			c.steps = append(c.steps, Sample{Provider: c.provider, Tokens: ev.StepUpdate.Usage.tokens()})
		}
	case "result":
		if ev.Result != nil && ev.Result.Usage != nil {
			c.result = &Sample{Provider: c.provider, Tokens: ev.Result.Usage.tokens()}
		}
	}
}

func (c *agyCarry) samples() []Sample {
	if c.result != nil {
		c.result.Model = c.model
		return []Sample{*c.result}
	}
	for i := range c.steps {
		c.steps[i].Model = c.model
	}
	return c.steps
}

// opencodeCarry: one sample per step_finish part.
type opencodeCarry struct {
	provider string
	model    string
	out      []Sample
}

func (c *opencodeCarry) feed(line []byte) {
	var ev opencodeEvent
	if json.Unmarshal(line, &ev) != nil || ev.Type != "step_finish" || ev.Part == nil || ev.Part.Tokens == nil {
		return
	}
	s := Sample{Provider: c.provider, Model: c.model, Tokens: ev.Part.Tokens.tokens()}
	if ev.Part.Cost != nil {
		s.USD, s.HasCost = *ev.Part.Cost, true
	}
	c.out = append(c.out, s)
}

func (c *opencodeCarry) samples() []Sample { return c.out }

// codexCarry: one sample per turn.completed event that carries usage.
type codexCarry struct {
	provider string
	model    string
	out      []Sample
}

func (c *codexCarry) feed(line []byte) {
	var ev codexEvent
	if json.Unmarshal(line, &ev) != nil || ev.Type != "turn.completed" || ev.Usage == nil {
		return
	}
	c.out = append(c.out, Sample{Provider: c.provider, Model: c.model, Tokens: Tokens{
		In:         ev.Usage.Input - ev.Usage.Cached,
		CacheRead:  ev.Usage.Cached,
		CacheWrite: ev.Usage.CacheWrite,
		Out:        ev.Usage.Output,
	}})
}

func (c *codexCarry) samples() []Sample { return c.out }

// streamCache is one headless stream's parse state on the reader.
type streamCache struct {
	size   int64
	mtime  time.Time
	offset int64 // bytes of the file already read from disk; the held tail's bytes included
	carry  streamCarry
	tail   []byte
}
