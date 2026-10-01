package usage

import (
	"bufio"
	"io"
)

// maxLine bounds one line; claude's tool results can run to megabytes.
const maxLine = 16 << 20

type claudeUsage struct {
	Input      int64 `json:"input_tokens"`
	CacheWrite int64 `json:"cache_creation_input_tokens"`
	CacheRead  int64 `json:"cache_read_input_tokens"`
	Output     int64 `json:"output_tokens"`
}

func (u claudeUsage) tokens() Tokens {
	return Tokens{In: u.Input, CacheRead: u.CacheRead, CacheWrite: u.CacheWrite, Out: u.Output}
}

type claudeEvent struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	Model     string `json:"model"` // system/init only
	Timestamp string `json:"timestamp"`
	CWD       string `json:"cwd"`
	Message   *struct {
		ID    string       `json:"id"`
		Model string       `json:"model"`
		Usage *claudeUsage `json:"usage"`
	} `json:"message"`
	// result event only
	TotalCostUSD *float64     `json:"total_cost_usd"`
	Usage        *claudeUsage `json:"usage"`
}

func scanLines(r io.Reader, fn func(line []byte)) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), maxLine)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		fn(line)
	}
}

// claudeStream reads a headless round's stream-json. The result event's usage is
// the sample (HasCost when total_cost_usd is present); with no result event, the
// assistant events deduped by message.id are the samples, tokens only.
func claudeStream(r io.Reader, fallbackProvider string) []Sample {
	c, _ := newCarry("claude", fallbackProvider, "")
	scanLines(r, c.feed)
	return c.samples()
}
