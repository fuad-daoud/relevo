// Package histq parses relevo's round-history query language, shared by
// `relevo history -q` and the dashboard filter line, into a db.Filter plus the
// conditions only Go can apply and the regroup axis. It depends on db alone.
package histq

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// ErrBadSince is the same value relevo.ErrBadSince names, so a caller matching
// that sentinel still does.
var ErrBadSince = errors.New("--since wants 24h, 7d or YYYY-MM-DD")

// ParseSince turns "24h", "7d" or a YYYY-MM-DD date into the instant before
// which rounds are ignored; "" means no cut. Only h and d suffixes are
// accepted, w deliberately not.
func ParseSince(s string, now time.Time) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	if t, err := time.Parse("2006-01-02", s); err == nil {
		return t, nil
	}
	if len(s) >= 2 {
		n, err := strconv.Atoi(s[:len(s)-1])
		if err == nil && n > 0 {
			switch s[len(s)-1] {
			case 'h':
				return now.Add(-time.Duration(n) * time.Hour), nil
			case 'd':
				return now.Add(-time.Duration(n) * 24 * time.Hour), nil
			}
		}
	}
	return time.Time{}, fmt.Errorf("%q: %w", s, ErrBadSince)
}

// Axis names the column Group collapses rows by; AxisNone means "do not group".
type Axis string

const (
	AxisNone      Axis = "none"
	AxisBinding   Axis = "binding"
	AxisRepo      Axis = "repo"
	AxisFeature   Axis = "feature"
	AxisTicket    Axis = "ticket"
	AxisCandidate Axis = "candidate"
	AxisActor     Axis = "actor"
	AxisHarness   Axis = "harness"
	AxisProvider  Axis = "provider"
	AxisModel     Axis = "model"
	AxisDay       Axis = "day"
	AxisOutcome   Axis = "outcome"
)

var axes = []Axis{
	AxisNone, AxisBinding, AxisRepo, AxisFeature, AxisTicket, AxisCandidate, AxisActor,
	AxisHarness, AxisProvider, AxisModel, AxisDay, AxisOutcome,
}

// Axes returns the twelve axis names, none first. The caller owns the slice.
func Axes() []Axis { return append([]Axis(nil), axes...) }

func ParseAxis(s string) (Axis, bool) {
	for _, a := range axes {
		if string(a) == s {
			return a, true
		}
	}
	return AxisNone, false
}

// NumCond is one numeric comparison. Key is cost|tokens|commits|duration|round,
// Op is > < >= <= =, and duration is in minutes, tokens in+cache+write+out, cost USD.
type NumCond struct {
	Key   string
	Op    string
	Value float64
}

// Query is one parsed query line.
type Query struct {
	Filter db.Filter
	// Words are bare tokens, matched case-insensitively as substrings of a
	// row's binding name, repo or feature.
	Words []string
	Nums  []NumCond
	// Report, Gate, Basis, Server and Mode have no db.Filter column; Apply
	// enforces them.
	Report, Gate, Basis, Server, Mode string
	// By is the regroup axis; AxisNone when `by:` was absent.
	By  Axis
	Raw string
	// Since and Until hold the raw text as typed, so String prints "7d".
	Since, Until string
	Now          time.Time
}

// ErrQuery is a parse error: its message is `query: <token> at <pos>: <reason>`.
type ErrQuery struct {
	Token  string
	Pos    int
	Reason string
}

func (e ErrQuery) Error() string {
	return fmt.Sprintf("query: %s at %d: %s", e.Token, e.Pos, e.Reason)
}

// The vocabulary is its own: the accepted values are listed here, not imported from internal/usage.
var (
	outcomeValues = []string{
		db.OutcomeReported, db.OutcomeHalted, db.OutcomeExited,
		db.OutcomeSwitched, db.OutcomeDoneNoReport, db.OutcomeOpen,
	}
	reportValues = []string{"done", "halted", "blocked", "deferred", "unstructured"}
	gateValues   = []string{"pass", "fail", "timeout", "error"}
	basisValues  = []string{"measured", "estimated", "unknown"}
	modeValues   = []string{"pane", "headless", "remote"}
)

// ParseAt parses s with now as the clock since/until are resolved against.
// Filter.Newest is always true: a query reads newest first.
func ParseAt(s string, now time.Time) (Query, error) {
	q := Query{Raw: strings.TrimSpace(s), Now: now, By: AxisNone}
	q.Filter.Newest = true

	toks, err := splitTokens(s)
	if err != nil {
		return Query{}, err
	}
	for _, t := range toks {
		if err := q.applyToken(t, now); err != nil {
			return Query{}, err
		}
	}
	return q, nil
}

type token struct {
	text string
	pos  int
}

// splitTokens splits s on whitespace outside double quotes; a backslash escapes a quote in a quoted value.
func splitTokens(s string) ([]token, error) {
	var out []token
	for i := 0; i < len(s); {
		for i < len(s) && isSpace(s[i]) {
			i++
		}
		if i >= len(s) {
			break
		}
		start := i
		var sb strings.Builder
		inQuote := false
		for i < len(s) {
			c := s[i]
			if inQuote && c == '\\' && i+1 < len(s) && s[i+1] == '"' {
				sb.WriteByte('"')
				i += 2
				continue
			}
			if c == '"' {
				inQuote = !inQuote
				i++
				continue
			}
			if !inQuote && isSpace(c) {
				break
			}
			sb.WriteByte(c)
			i++
		}
		if inQuote {
			return nil, ErrQuery{Token: sb.String(), Pos: start, Reason: "unbalanced quote"}
		}
		out = append(out, token{text: sb.String(), pos: start})
	}
	return out, nil
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

func (q *Query) applyToken(t token, now time.Time) error {
	if t.text == "" {
		return ErrQuery{Token: t.text, Pos: t.pos, Reason: "empty token"}
	}
	key, op, value, ok := splitToken(t.text)
	if !ok {
		q.Words = append(q.Words, t.text)
		return nil
	}
	if op == ":" {
		return q.applyKeyValue(key, value, t, now)
	}
	return q.applyNumCond(key, op, value, t)
}

func splitToken(s string) (key, op, value string, ok bool) {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case ':':
			return s[:i], ":", s[i+1:], true
		case '>', '<':
			if i+1 < len(s) && s[i+1] == '=' {
				return s[:i], s[i : i+2], s[i+2:], true
			}
			return s[:i], s[i : i+1], s[i+1:], true
		case '=':
			return s[:i], "=", s[i+1:], true
		}
	}
	return "", "", "", false
}

func (q *Query) applyKeyValue(key, value string, t token, now time.Time) error {
	if value == "" {
		return ErrQuery{Token: t.text, Pos: t.pos, Reason: "empty value"}
	}
	if q.applyPlainKey(key, value) {
		return nil
	}
	switch key {
	case "outcome":
		if !db.ValidOutcome(value) {
			return badEnum(key, t, outcomeValues)
		}
		q.Filter.Outcome = value
	case "report", "gate", "basis", "mode":
		return q.applyEnum(key, value, t)
	case "since", "until":
		return q.applyWindow(key, value, t, now)
	case "round":
		return q.applyRound(value, t)
	case "archived":
		return q.applyArchived(value, t)
	case "by":
		return q.applyBy(value, t)
	default:
		return ErrQuery{Token: t.text, Pos: t.pos, Reason: "unknown key"}
	}
	return nil
}

func (q *Query) applyPlainKey(key, value string) bool {
	switch key {
	case "binding":
		q.Filter.Binding = value
	case "repo":
		q.Filter.Repo = value
	case "feature":
		q.Filter.Feature = value
	case "ticket":
		q.Filter.Ticket = value
	case "mastermind":
		q.Filter.MasterMind = value
	case "harness":
		q.Filter.Harness = value
	case "provider":
		q.Filter.Provider = value
	case "model":
		q.Filter.Model = value
	case "candidate":
		q.Filter.Candidate = value
	case "state":
		q.Filter.State = value
	case "server":
		q.Server = value
	default:
		return false
	}
	return true
}

func (q *Query) applyEnum(key, value string, t token) error {
	want := enumValues(key)
	if !slices.Contains(want, value) {
		return badEnum(key, t, want)
	}
	switch key {
	case "report":
		q.Report = value
	case "gate":
		q.Gate = value
	case "basis":
		q.Basis = value
	case "mode":
		q.Mode = value
	}
	return nil
}

func enumValues(key string) []string {
	switch key {
	case "report":
		return reportValues
	case "gate":
		return gateValues
	case "basis":
		return basisValues
	case "mode":
		return modeValues
	}
	return nil
}

func badEnum(key string, t token, want []string) error {
	return ErrQuery{Token: t.text, Pos: t.pos, Reason: fmt.Sprintf("%s wants one of %s", key, strings.Join(want, ", "))}
}

func (q *Query) applyWindow(key, value string, t token, now time.Time) error {
	ts, err := ParseSince(value, now)
	if err != nil {
		return ErrQuery{Token: t.text, Pos: t.pos, Reason: err.Error()}
	}
	if key == "since" {
		q.Filter.Since = ts
		q.Since = value
		return nil
	}
	q.Filter.Until = ts
	q.Until = value
	return nil
}

func (q *Query) applyRound(value string, t token) error {
	n, err := strconv.Atoi(value)
	if err != nil {
		return ErrQuery{Token: t.text, Pos: t.pos, Reason: "round wants a number"}
	}
	q.Filter.Round = n
	return nil
}

func (q *Query) applyArchived(value string, t token) error {
	switch value {
	case "true", "false":
		b := value == "true"
		q.Filter.Archived = &b
		return nil
	}
	return ErrQuery{Token: t.text, Pos: t.pos, Reason: "archived wants true or false"}
}

func (q *Query) applyBy(value string, t token) error {
	a, ok := ParseAxis(value)
	if !ok {
		return ErrQuery{Token: t.text, Pos: t.pos, Reason: "by wants one of " + axisNames()}
	}
	q.By = a
	return nil
}

func (q *Query) applyNumCond(key, op, value string, t token) error {
	switch key {
	case "cost", "tokens", "commits", "duration", "round":
	default:
		return ErrQuery{Token: t.text, Pos: t.pos, Reason: "unknown numeric key"}
	}
	if value == "" {
		return ErrQuery{Token: t.text, Pos: t.pos, Reason: "empty value"}
	}
	n, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return ErrQuery{Token: t.text, Pos: t.pos, Reason: "bad number"}
	}
	q.Nums = append(q.Nums, NumCond{Key: key, Op: op, Value: n})
	return nil
}

// String renders q canonically: filter keys in a fixed order, then the numeric
// conditions in input order, then the words, then by. A value with a space is
// double-quoted; since/until print as typed.
func (q Query) String() string {
	var parts []string
	add := func(key, value string) {
		if value != "" {
			parts = append(parts, key+":"+quoteValue(value))
		}
	}
	add("binding", q.Filter.Binding)
	add("repo", q.Filter.Repo)
	add("feature", q.Filter.Feature)
	add("ticket", q.Filter.Ticket)
	add("mastermind", q.Filter.MasterMind)
	add("harness", q.Filter.Harness)
	add("provider", q.Filter.Provider)
	add("model", q.Filter.Model)
	add("candidate", q.Filter.Candidate)
	add("outcome", q.Filter.Outcome)
	add("report", q.Report)
	add("state", q.Filter.State)
	add("gate", q.Gate)
	add("basis", q.Basis)
	add("server", q.Server)
	add("mode", q.Mode)
	if q.Filter.Round != 0 {
		parts = append(parts, "round:"+strconv.Itoa(q.Filter.Round))
	}
	add("since", q.Since)
	add("until", q.Until)
	if q.Filter.Archived != nil {
		parts = append(parts, "archived:"+strconv.FormatBool(*q.Filter.Archived))
	}
	for _, n := range q.Nums {
		parts = append(parts, n.Key+n.Op+formatNum(n.Value))
	}
	for _, w := range q.Words {
		parts = append(parts, quoteValue(w))
	}
	if q.By != AxisNone && q.By != "" {
		parts = append(parts, "by:"+string(q.By))
	}
	return strings.Join(parts, " ")
}

// quoteValue double-quotes s when it holds a space, escaping any quote.
func quoteValue(s string) string {
	if !strings.Contains(s, " ") {
		return s
	}
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}

// formatNum prints v so Parse reads it back: a whole number with no decimal point.
func formatNum(v float64) string {
	if v == math.Trunc(v) && math.Abs(v) < 1e15 {
		return strconv.FormatInt(int64(v), 10)
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

func axisNames() string {
	names := make([]string, len(axes))
	for i, a := range axes {
		names[i] = string(a)
	}
	return strings.Join(names, ", ")
}
