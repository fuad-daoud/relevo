package usage

import (
	"bufio"
	"encoding/json"
	"io"
	"io/fs"
	"path"
	"strings"
	"time"
)

// maxLine bounds one line; claude's tool results can run to megabytes.
const maxLine = 16 << 20

// ProjectSlug is the directory name claude keeps a cwd's transcripts under
// (~/.claude/projects/<slug>): every byte of the absolute path outside
// [A-Za-z0-9] becomes '-'. Verified against five entries on 2026-09-18.
func ProjectSlug(cwd string) string {
	var b strings.Builder
	for i := 0; i < len(cwd); i++ {
		c := cwd[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
			b.WriteByte(c)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

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

// claudeProject reads every *.jsonl under fsys (subagents included) and returns one
// sample per assistant message.id inside [start, end] whose cwd equals worktree.
// Files older than start are skipped unread: one untouched since before the round
// cannot hold a record inside it.
func claudeProject(fsys fs.FS, worktree string, start, end time.Time, provider string) []Sample {
	var out []Sample
	seen := map[string]bool{}
	_ = fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || path.Ext(p) != ".jsonl" {
			return nil
		}
		if info, err := d.Info(); err == nil && info.ModTime().Before(start) {
			return nil
		}
		f, err := fsys.Open(p)
		if err != nil {
			return nil
		}
		defer func() { _ = f.Close() }()
		scanLines(f, func(line []byte) {
			var ev claudeEvent
			if json.Unmarshal(line, &ev) != nil || ev.Type != "assistant" || ev.Message == nil || ev.Message.Usage == nil {
				return
			}
			if ev.CWD != worktree || seen[ev.Message.ID] {
				return
			}
			ts, err := time.Parse(time.RFC3339Nano, ev.Timestamp)
			if err != nil || ts.Before(start) || ts.After(end) {
				return
			}
			seen[ev.Message.ID] = true
			out = append(out, Sample{Provider: provider, Model: ev.Message.Model, Tokens: ev.Message.Usage.tokens()})
		})
		return nil
	})
	return out
}
