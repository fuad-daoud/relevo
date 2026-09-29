package availability

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/candidate"
	"github.com/fuad-daoud/relevo/internal/harness"
	"github.com/fuad-daoud/relevo/internal/spawn"
	"github.com/fuad-daoud/relevo/internal/store"
	"github.com/fuad-daoud/relevo/internal/transcript"
)

const (
	// probeBudget is the harness's own print budget for one latency probe: long
	// enough for a trivial round, short enough that a wedged harness does not
	// hold the listing for a whole round budget.
	probeBudget = 60 * time.Second

	// probeTimeout bounds the whole probe, harness start included, so a harness
	// that never exits is data ("timeout") rather than a hang.
	probeTimeout = 90 * time.Second

	// probePrompt is the one-line prompt every probe sends: the smallest
	// possible answer, with no tools, so the measurement is time to first model
	// output and not time to do work.
	probePrompt = "relevo latency probe: reply with the single word ok. Do not use any tools."
)

// ProbeResult is one probe's outcome: the recorded latency.Sample plus the
// candidate's short name, which FormatProbe prints. Only the sample is recorded
// -- a name is a display alias, and the token stays the identity.
type ProbeResult struct {
	Sample
	Name string
}

// LineExec is the process seam Probe runs a candidate's harness through.
// cmd/relevo implements it with os/exec; tests implement it with a script, so
// no test in this package ever spawns a harness or reaches the network.
type LineExec interface {
	// Run starts argv in dir with the parent environment minus proc.DeniedEnv,
	// calls onLine for every stdout line as it arrives (without the newline),
	// and returns when the process exits or ctx is done. A non-zero exit is an
	// error whose text ends with the last 300 bytes of stderr.
	Run(ctx context.Context, dir string, argv []string, onLine func(line []byte)) error
}

// probeTier is the most restrictive tier kind's harness can honour, for a probe
// that must not change anything: read, else edit, else harness (the harness's
// own config decides). Never yolo.
func probeTier(kind string) (harness.Tier, error) {
	h, ok := harness.Lookup(kind)
	if !ok {
		return "", fmt.Errorf("unknown harness %q", kind)
	}

	for _, t := range []harness.Tier{harness.TierRead, harness.TierEdit, harness.TierHarness} {
		if _, err := h.PermissionArgs(t); err == nil {
			return t, nil
		}
	}

	// TierHarness always succeeds, so this is unreachable.
	return harness.TierHarness, nil
}

// probeRole picks the role a probe runs under: in file mode the first role
// whose candidate list names c, in registry order; in legacy mode the first of
// the candidate's own roles the registry knows. ok is false when neither names
// one.
func probeRole(d Deps, c candidate.Candidate) (harness.RoleSpec, bool) {
	reg := d.RoleRegistry()
	if reg.FileMode() {
		for _, name := range reg.Names() {
			if !reg.Serves(name, c.Ref()) {
				continue
			}
			if spec, err := reg.Spec(name, c.Harness); err == nil {
				return spec, true
			}
		}
		return harness.RoleSpec{}, false
	}
	for _, name := range c.Roles {
		if _, ok := reg.Role(name); !ok {
			continue
		}
		if spec, err := reg.Spec(name, c.Harness); err == nil {
			return spec, true
		}
	}
	return harness.RoleSpec{}, false
}

// probeFailure is a failed probe's result, its message bounded to the 300 bytes
// a sample carries.
func probeFailure(d Deps, ref string, c candidate.Candidate, host, text string) ProbeResult {
	return ProbeResult{
		Sample: Sample{At: d.Now().UTC(), Token: ref, Host: host, Err: probeErrText(text)},
		Name:   c.Name,
	}
}

// ProbeCandidate runs one candidate's harness once, headless, at the most
// restrictive tier the harness can honour (probeTier), in a fresh temp
// directory that is `git init`ed like a real round's worktree, because codex
// refuses to run outside a git repository, and measures how long it takes to
// produce its first model output. Failures are data: they come back in Err,
// never as a returned error, so one broken candidate cannot hide the rest of
// the listing.
func ProbeCandidate(ctx context.Context, d Deps, x LineExec, c candidate.Candidate, host string) ProbeResult {
	ref := c.Ref().String()

	role, known := probeRole(d, c)
	if !known {
		return probeFailure(d, ref, c, host, "no known role")
	}

	dir, err := os.MkdirTemp("", "relevo-probe-")
	if err != nil {
		return probeFailure(d, ref, c, host, err.Error())
	}
	defer func() { _ = os.RemoveAll(dir) }()

	tier, err := probeTier(c.Harness)
	if err != nil {
		return probeFailure(d, ref, c, host, err.Error())
	}

	argv, err := spawn.HeadlessLaunch(c, role, tier, probeBudget, probePrompt, dir, dir)
	if err != nil {
		return probeFailure(d, ref, c, host, err.Error())
	}

	pctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()

	// codex refuses to run outside a git repository, so the probe runs in a
	// fresh git repo, like a real round's worktree. The git call stays outside
	// the timed window: neither TTFTMS nor TotalMS includes it.
	if err := x.Run(pctx, dir, []string{"git", "init", "-q"}, nil); err != nil {
		return probeFailure(d, ref, c, host, "git init: "+err.Error())
	}

	return probeMeasure(pctx, d, x, c, host, dir, argv)
}

// probeMeasure runs the prepared argv and turns its output into a result.
func probeMeasure(ctx context.Context, d Deps, x LineExec, c candidate.Candidate, host, dir string, argv []string) ProbeResult {
	ref := c.Ref().String()
	start := d.Now()
	var (
		ttft       int64
		seen       bool
		harnessErr string
	)

	runErr := x.Run(ctx, dir, argv, func(line []byte) {
		// The harness's own fatal error message is collected even after the
		// first output, so the last one wins: a harness can emit output and
		// then fail.
		if msg, ok := transcript.ErrorText(c.Harness, line); ok {
			harnessErr = msg
		}
		if seen {
			return
		}
		if transcript.FirstOutput(c.Harness, line) {
			seen = true
			ttft = d.Now().Sub(start).Milliseconds()
		}
	})

	r := ProbeResult{
		Sample: Sample{
			At:      start.UTC(),
			Token:   ref,
			Host:    host,
			TTFTMS:  ttft,
			TotalMS: d.Now().Sub(start).Milliseconds(),
		},
		Name: c.Name,
	}
	switch {
	case runErr != nil && harnessErr != "":
		// The harness's own reason, with the exit status kept for context: the
		// real message is on stdout, not in stderr.
		r.Err = probeErrText(harnessErr + " [" + exitWord(runErr) + "]")
	case runErr != nil:
		r.Err = probeErrText(runErr.Error())
	case !seen && harnessErr != "":
		r.Err = probeErrText(harnessErr)
	case !seen:
		r.Err = "no model output"
	}

	return r
}

// exitWord is the exit-status head of a run error: everything up to its first
// colon ("exit status 1"), or the whole text when it has none.
func exitWord(err error) string {
	s := err.Error()
	if i := strings.Index(s, ":"); i >= 0 {
		return s[:i]
	}
	return s
}

// probeErrText bounds an error's text to the 300 bytes a sample carries, so one
// pathological harness cannot bloat the history file.
func probeErrText(s string) string {
	if len(s) <= 300 {
		return s
	}
	return s[:300]
}

// Probe measures every named candidate, sequentially, one at a time so the
// probes do not contend with each other, and records each result in the latency
// history. An unknown or unparsable token is a user error returned before
// anything runs. each, when non-nil, is called once per result in order, so the
// CLI can print as it goes.
func Probe(ctx context.Context, d Deps, x LineExec, tokens []string, host string, each func(ProbeResult)) ([]ProbeResult, error) {
	var cands []candidate.Candidate
	if len(tokens) == 0 {
		if d.Candidates != nil {
			for _, ref := range d.Candidates.Refs() {
				parsed, err := candidate.ParseRef(ref)
				if err != nil {
					continue
				}
				c, err := d.Candidates.Lookup(parsed)
				if err != nil {
					continue
				}
				cands = append(cands, c)
			}
		}
	} else {
		for _, token := range tokens {
			// The argument may be a candidate name or a canonical token.
			c, err := d.Candidates.Resolve(token)
			if err != nil {
				return nil, err
			}
			cands = append(cands, c)
		}
	}

	var results []ProbeResult
	for _, c := range cands {
		r := ProbeCandidate(ctx, d, x, c, host)
		results = append(results, r)
		if each != nil {
			each(r)
		}
		recordLatency(d, r)
	}

	return results, nil
}

// recordLatency appends one probe result to the latency history under the store
// lock, so a probe and a listing never read a torn document. A history failure
// is one stderr line; it never fails a probe. A nil Latency means no store is
// configured, so there is nothing to record into.
func recordLatency(d Deps, r ProbeResult) {
	if d.Latency == nil {
		return
	}

	err := d.Store.WithLock(func(*store.Tx) error {
		h, err := LoadLatency(d.Latency)
		if err != nil {
			return err
		}
		return SaveLatency(d.Latency, h.Prune(d.Now()).Append(r.Sample))
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "relevo: could not record latency: %v\n", err)
	}
}

// ProbeMS renders a millisecond count the way a listing reads: whole
// milliseconds below a second, one decimal above.
func ProbeMS(ms int64) string {
	if ms < 1000 {
		return fmt.Sprintf("%dms", ms)
	}
	return fmt.Sprintf("%.1fs", float64(ms)/1000)
}

// ProbeNameWidth is the column width FormatProbe is given: the longest name
// among the results about to print. An empty slice is width 0.
func ProbeNameWidth(names []string) int {
	width := 0
	for _, name := range names {
		if len(name) > width {
			width = len(name)
		}
	}
	return width
}

// FormatProbe renders one probe result as a line, without a trailing newline:
// the time to first output and the total on success, or the error, with the
// TTFT appended when output was seen before the failure. The candidate is named
// by its short name, or by its token when the result carries no name.
func FormatProbe(r ProbeResult, width int) string {
	label := r.Name
	if label == "" {
		label = r.Token
	}
	if r.Err == "" {
		return fmt.Sprintf("%-*s  ttft %s  total %s", width, label, ProbeMS(r.TTFTMS), ProbeMS(r.TotalMS))
	}

	out := fmt.Sprintf("%-*s  error: %s", width, label, r.Err)
	if r.TTFTMS > 0 {
		out += "  (ttft " + ProbeMS(r.TTFTMS) + ")"
	}
	return out
}
