package usage

import "io"

type museModelUsage struct {
	Model     string `json:"model"`
	Input     int64  `json:"input_tokens"`
	Cached    int64  `json:"cached_tokens"`
	Output    int64  `json:"output_tokens"`
	Reasoning int64  `json:"reasoning_tokens"`
	Calls     int64  `json:"calls"`
}

type museEvent struct {
	PayloadType string `json:"payload_type"`
	Payload     struct {
		Models []museModelUsage `json:"models"`
	} `json:"payload"`
}

// museStream reads a headless round's `muse exec --json` stream: one sample per
// model in each run.usage event. Input includes the cached part, hence the
// subtraction. Reasoning tokens are left out as codex's are, on the
// assumption that they are part of output. The subscription meter bills no
// per-round price, so no sample carries a cost.
func museStream(r io.Reader, provider, model string) []Sample {
	c, _ := newCarry("muse", provider, model)
	scanLines(r, c.feed)
	return c.samples()
}
