package main

// cmd/relevo/setup_smoke_feed.go shows the smoke round while it runs: which
// builder holds it, a switch when one gives way to the next, and each tool
// call the builder makes, read from the round's stream as it grows.

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/relevo"
	"github.com/fuad-daoud/relevo/internal/transcript"
)

// smokeFeedTick is how often the feed reads the binding and its stream.
var smokeFeedTick = time.Second

// smokeQuietAfter is how long the feed stays silent before it says the
// builder is still at work: a model thinking prints no tool call.
const smokeQuietAfter = 20 * time.Second

// smokeFeedStream is where the feed goes; stderr keeps stdout clean under
// --json and in scripts whose stdout is parsed.
var smokeFeedStream io.Writer = os.Stderr

// smokeFeedWidth caps one printed tool call, so a long command stays one line.
const smokeFeedWidth = 100

type smokeFeed struct {
	rt      relevo.Runtime
	name    string
	w       io.Writer
	started time.Time
	trim    string // the smoke directory's prefix, cut from printed paths

	builder string
	kind    string
	offset  int64
	partial []byte
	render  *transcript.Renderer
	spoke   time.Time
}

// startSmokeFeed runs the feed until the returned stop is called.
func startSmokeFeed(rt relevo.Runtime, name, smokeDir string) func() {
	f := &smokeFeed{rt: rt, name: name, w: smokeFeedStream, started: time.Now(), trim: smokeDir + string(os.PathSeparator)}
	f.spoke = f.started
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		tick := time.NewTicker(smokeFeedTick)
		defer tick.Stop()
		for {
			f.step(time.Now())
			select {
			case <-tick.C:
			case <-done:
				f.step(time.Now())
				return
			}
		}
	}()
	return func() {
		close(done)
		<-stopped
	}
}

// step prints what changed since the last one: a new builder, new tool
// calls, or, after a quiet stretch, that the builder is still at work.
func (f *smokeFeed) step(now time.Time) {
	b, err := f.rt.Store.Load(f.name)
	if err != nil {
		return
	}
	if b.BuilderCandidate != "" && b.BuilderCandidate != f.builder {
		f.switchTo(b.BuilderCandidate)
	}
	if f.builder == "" {
		return
	}
	for _, line := range f.newLines(f.rt.Store.StreamPath(b.Name, b.Round)) {
		f.say(now, "  "+line)
	}
	if now.Sub(f.spoke) >= smokeQuietAfter {
		f.say(now, fmt.Sprintf("  still working (%s)", now.Sub(f.started).Truncate(time.Second)))
	}
}

func (f *smokeFeed) say(now time.Time, line string) {
	fmt.Fprintln(f.w, line)
	f.spoke = now
}

// switchTo names the builder now holding the round and renders the stream
// as its harness from here on; a stream the switch restarted shrinks, and
// newLines starts it over from the top.
func (f *smokeFeed) switchTo(token string) {
	name := f.rt.Candidates.NameOf(token)
	if f.builder == "" {
		fmt.Fprintf(f.w, "  builder %s is working on it\n", name)
	} else {
		fmt.Fprintf(f.w, "  %s stopped (a usage limit or a failure); %s takes over\n", f.rt.Candidates.NameOf(f.builder), name)
	}
	f.builder, f.kind, f.render = token, smokeKindOf(f.rt, token), transcript.NewRenderer()
	f.spoke = time.Now()
}

func smokeKindOf(rt relevo.Runtime, token string) string {
	if rt.Candidates != nil {
		if c, err := rt.Candidates.ResolveToken(token); err == nil {
			return c.Harness
		}
	}
	if r, err := candidate.ParseRef(candidate.StripEffort(token)); err == nil {
		return r.Harness
	}
	return ""
}

// newLines reads what the stream gained since the last read and returns the
// tool calls it rendered; a half-written last line waits for the next read.
func (f *smokeFeed) newLines(path string) []string {
	fh, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer fh.Close()
	if st, err := fh.Stat(); err == nil && st.Size() < f.offset {
		f.offset, f.partial = 0, nil
	}
	if _, err := fh.Seek(f.offset, io.SeekStart); err != nil {
		return nil
	}
	chunk, err := io.ReadAll(fh)
	if err != nil || len(chunk) == 0 {
		return nil
	}
	f.offset += int64(len(chunk))
	buf := append(f.partial, chunk...)
	cut := bytes.LastIndexByte(buf, '\n')
	if cut < 0 {
		f.partial = buf
		return nil
	}
	f.partial = append([]byte{}, buf[cut+1:]...)
	var out []string
	for _, raw := range bytes.Split(buf[:cut], []byte{'\n'}) {
		for _, line := range f.render.Render(f.kind, raw) {
			if _, body := transcript.SplitStamp(line); strings.HasPrefix(body, "● ") {
				out = append(out, clip(strings.ReplaceAll(body, f.trim, ""), smokeFeedWidth))
			}
		}
	}
	return out
}

func clip(s string, width int) string {
	r := []rune(s)
	if len(r) <= width {
		return s
	}
	return string(r[:width-1]) + "…"
}
