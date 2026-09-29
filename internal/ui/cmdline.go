package ui

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
)

// command is one entry of the ':' command table (§4.7). done marks a
// `round <key>` entry whose report row reads DONE, so live bindings sort
// before done ones (§2.3).
type command struct {
	name, args, help string
	done             bool
}

// commands is the table §4.7 fixes, in display order. The trailing false is
// command.done: no fixed command is a done binding (§2.3).
var commands = []command{
	{"fleet", "", "bindings on this machine", false},
	{"rounds", "[query…]", "every round, filtered", false},
	{"round", "<binding> [N]", "open one binding's round", false},
	{"stats", "[7d|30d|90d|all]", "rounds, cost and health", false},
	{"candidates", "", "candidates, gates and who picks them", false},
	{"actors", "", "who runs each job, and in what order", false},
	{"agents", "", "agent definitions per harness", false},
	{"settings", "", "limits, checks and timings", false},
	{"audit", "", "config revisions, and roll back", false},
	{"log", "", "this session's action results", false},
	{"ungate", "<provider|candidate>", "clear a recorded rate limit", false},
	{"help", "", "keys", false},
	{"quit", "", "leave", false},
}

// cmdLine is the ':' command line: an open flag, its text input and the
// index of the selected completion (§4.7).
type cmdLine struct {
	open  bool
	input textinput.Model
	sel   int
}

// newCmdLine builds the command line with its ':' prompt.
func newCmdLine() cmdLine {
	in := newTextInput()
	in.Prompt = ":"
	return cmdLine{input: in}
}

// opened shows the command line, empty, with the cursor in the input.
func (c cmdLine) opened() cmdLine {
	c.open = true
	c.sel = 0
	c.input.SetValue("")
	c.input.Focus()
	c.input.CursorEnd()
	return c
}

// typed is the command line's current text.
func (c cmdLine) typed() string { return strings.TrimSpace(c.input.Value()) }

// matches is every completion candidate whose name has the typed text as a
// case-insensitive subsequence, ranked by prefix, then live before done, then
// length, then alphabetical, capped at 8. Pure: the tests drive it directly.
func (c cmdLine) matches(env Env) []command {
	out := c.allMatches(env)
	if len(out) > 8 {
		out = out[:8]
	}
	return out
}

// matchCount is how many completions match, before matches' cap of 8. It is
// what the command modal's "+ N more match" row counts (§3.1).
func (c cmdLine) matchCount(env Env) int {
	return len(c.allMatches(env))
}

// allMatches is every completion candidate whose name has the typed text as a
// case-insensitive subsequence, ranked by prefix, then live before done, then
// length, then alphabetical, before matches' cap of 8. Pure: the tests drive
// it directly.
func (c cmdLine) allMatches(env Env) []command {
	typed := strings.ToLower(c.typed())
	cands := make([]command, 0, len(commands)+len(env.Report.Bindings)+len(env.Report.Gated))
	cands = append(cands, commands...)
	for i := range env.Report.Bindings {
		key := env.Report.Bindings[i].Key()
		cands = append(cands, command{
			name: "round " + key,
			help: "open " + key,
			done: env.Report.Bindings[i].Display == "DONE",
		})
	}
	// `ungate` completes to the providers and names of the live gates
	// (§4.3): the same subjects `relevo gate --clear` accepts.
	for _, g := range env.Report.Gated {
		subject := g.Token
		if g.Name != "" {
			subject = g.Name
		}
		help := "clear the rate limit on " + g.Token
		cands = append(cands, command{name: "ungate " + subject, help: help})
		cands = append(cands, command{name: "ungate " + g.Token, help: help})
	}
	var out []command
	for _, cand := range cands {
		if subsequence(typed, strings.ToLower(cand.name)) {
			out = append(out, cand)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		pi := strings.HasPrefix(strings.ToLower(out[i].name), typed)
		pj := strings.HasPrefix(strings.ToLower(out[j].name), typed)
		if pi != pj {
			return pi
		}
		if out[i].done != out[j].done {
			return !out[i].done
		}
		if len(out[i].name) != len(out[j].name) {
			return len(out[i].name) < len(out[j].name)
		}
		return out[i].name < out[j].name
	})
	return out
}

// cmdSection names the modal section a completion belongs to (§3.1): the
// commands table's own entries are VIEWS, `round <key>` entries are BINDINGS
// and `ungate …` entries are GATES.
func cmdSection(c command) string {
	switch {
	case strings.HasPrefix(c.name, "round "):
		return "BINDINGS"
	case strings.HasPrefix(c.name, "ungate "):
		return "GATES"
	default:
		return "VIEWS"
	}
}

// subsequence reports whether sub's runes appear, in order, in s.
func subsequence(sub, s string) bool {
	if sub == "" {
		return true
	}
	rs := []rune(sub)
	i := 0
	for _, r := range s {
		if r == rs[i] {
			i++
			if i == len(rs) {
				return true
			}
		}
	}
	return false
}

// update handles one key while the command line is open: up/down move the
// selection, tab completes, enter executes, esc closes, everything else goes
// to the input (§4.7, §6).
func (c cmdLine) update(k tea.KeyMsg, env Env, p prefs) (cmdLine, tea.Cmd) {
	ms := c.matches(env)
	switch k.String() {
	case "up", "ctrl+p":
		if len(ms) > 0 {
			c.sel = (c.sel - 1 + len(ms)) % len(ms)
		}
		return c, nil
	case "down", "ctrl+n":
		if len(ms) > 0 {
			c.sel = (c.sel + 1) % len(ms)
		}
		return c, nil
	case "tab":
		if len(ms) > 0 {
			if c.sel >= len(ms) {
				c.sel = 0
			}
			c.input.SetValue(ms[c.sel].name + " ")
			c.input.CursorEnd()
		}
		return c, nil
	case "enter":
		return c.execute(env, p)
	case "esc":
		c.open = false
		c.input.Blur()
		return c, nil
	}
	var cmd tea.Cmd
	c.input, cmd = c.input.Update(k)
	if len(ms) > 0 && c.sel >= len(ms) {
		c.sel = 0
	}
	return c, cmd
}

// execute runs the typed command: an exact name wins, otherwise the
// selected completion (§6).
func (c cmdLine) execute(env Env, p prefs) (cmdLine, tea.Cmd) {
	value := c.input.Value()
	c.open = false
	c.input.Blur()
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return c, nil
	}
	for _, cmd := range commands {
		if cmd.name == fields[0] {
			return c, execLine(value, env, p)
		}
	}
	ms := c.matches(env)
	if len(ms) == 0 {
		return c, notice(fmt.Sprintf("unknown command %q (try :help)", ":"+fields[0]))
	}
	sel := c.sel
	if sel >= len(ms) {
		sel = 0
	}
	return c, execLine(ms[sel].name, env, p)
}
