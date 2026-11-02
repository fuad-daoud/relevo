// Package burst holds the head's side of the burst provider contract: the v2
// report wire types and the rules a document must pass before the head acts
// on it.
package burst

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"time"
)

// ReportVersion is the one report contract version burst speaks; a document
// naming any other version is refused in both directions.
const ReportVersion = 2

// Report reason values the head sends. An unrecognised value read back is
// normalised to "" and returned as a note, never an error.
const (
	ReportReasonInterval  = "interval"
	ReportReasonForwarded = "forwarded"
	ReportReasonClosed    = "closed"
	ReportReasonStartup   = "startup"
)

// Report problem values the head may name for a node; normalisation works the
// same way as for reasons.
const (
	ReportProblemUnreachable = "unreachable"
	ReportProblemFingerprint = "fingerprint"
	ReportProblemKey         = "key"
)

// Report cadence values for ParseReportEvery: 30 s is what the provider
// itself defaults to when the field is missing, and the bounds keep one
// mistyped report_every from either stalling a report loop for minutes or
// running the head's one provider call per second. An out-of-range value is
// clamped to the bounds, never refused: a reply is only thrown away whole
// when its shape, not one word in it, is wrong.
const (
	DefaultReportEvery = 30 * time.Second
	MinReportEvery     = 10 * time.Second
	MaxReportEvery     = 5 * time.Minute
)

// ErrBadInput reports a document that does not fit the report contract; the
// message names the failing field path, as the provider's decoder does, so a
// caller can point at the value that broke.
var ErrBadInput = errors.New("bad burst report input")

// ReportRequest is the input to `burst report`.
type ReportRequest struct {
	Version int             `json:"version"`
	At      time.Time       `json:"at"`
	Reason  string          `json:"reason,omitempty"`
	Head    ReportHead      `json:"head"`
	Local   ReportLocal     `json:"local"`
	Waiting []ReportWaiting `json:"waiting"`
	Nodes   []ReportNode    `json:"nodes"`
}

// ReportHead identifies the relevo head that sent the report.
type ReportHead struct {
	Name  string `json:"name"`
	CPUs  int    `json:"cpus"`
	MemGB int    `json:"mem_gb"`
}

// ReportLocal is the head's own load.
type ReportLocal struct {
	Running int `json:"running"`
	Queued  int `json:"queued"`
	Cap     int `json:"cap"`
}

// ReportWaiting is one waiting round the head holds.
type ReportWaiting struct {
	ID        string    `json:"id"`
	Since     time.Time `json:"since"`
	Candidate string    `json:"candidate"`
	Harness   string    `json:"harness"`
}

// ReportNode is one burst node as the head saw it.
type ReportNode struct {
	ID          string        `json:"id"`
	Running     int           `json:"running"`
	Queued      int           `json:"queued"`
	Unacked     int           `json:"unacked"`
	ForwardedAt *time.Time    `json:"forwarded_at"`
	Rounds      []ReportRound `json:"rounds"`
	Problem     string        `json:"problem,omitempty"`
}

// ReportRound is one round running on a node.
type ReportRound struct {
	ID        string    `json:"id"`
	Candidate string    `json:"candidate"`
	Started   time.Time `json:"started"`
}

// ReportResponse is burst's reply to a report.
type ReportResponse struct {
	Version     int             `json:"version"`
	Nodes       []ReportNodeOut `json:"nodes"`
	Preparing   *Preparing      `json:"preparing"`
	Denied      *Denied         `json:"denied"`
	ReportEvery string          `json:"report_every"`
}

// ReportNodeOut is one ready or draining node in the response. The node is
// this and nothing else: state is the whole stop signal for the head.
type ReportNodeOut struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	URL         string `json:"url"`
	Fingerprint string `json:"fingerprint"`
	Cap         int    `json:"cap"`
	State       string `json:"state"`
}

// Preparing describes what burst is currently bringing up.
type Preparing struct {
	Slots int       `json:"slots"`
	Since time.Time `json:"since"`
}

// Denied reports that the head is inside a retry window; the reason is
// budget, cap, no_capacity or failing (the breaker), and RetryAfter is a Go
// duration string naming when to try again.
type Denied struct {
	Reason     string `json:"reason"`
	RetryAfter string `json:"retry_after"`
}

// wireReportRequest mirrors ReportRequest with pointers so that a missing
// required field can be told apart from a zero value.
type wireReportRequest struct {
	Version *int             `json:"version"`
	At      *time.Time       `json:"at"`
	Reason  *string          `json:"reason"`
	Head    *wireReportHead  `json:"head"`
	Local   *wireReportLocal `json:"local"`
	Waiting *[]wireWaiting   `json:"waiting"`
	Nodes   *[]wireNode      `json:"nodes"`
}

type wireReportHead struct {
	Name  *string `json:"name"`
	CPUs  int     `json:"cpus"`
	MemGB int     `json:"mem_gb"`
}

type wireReportLocal struct {
	Running int `json:"running"`
	Queued  int `json:"queued"`
	Cap     int `json:"cap"`
}

type wireWaiting struct {
	ID        *string    `json:"id"`
	Since     *time.Time `json:"since"`
	Candidate string     `json:"candidate"`
	Harness   string     `json:"harness"`
}

type wireNode struct {
	ID          *string     `json:"id"`
	Running     int         `json:"running"`
	Queued      int         `json:"queued"`
	Unacked     int         `json:"unacked"`
	ForwardedAt *time.Time  `json:"forwarded_at"`
	Rounds      []wireRound `json:"rounds"`
	Problem     *string     `json:"problem"`
}

type wireRound struct {
	ID        string    `json:"id"`
	Candidate string    `json:"candidate"`
	Started   time.Time `json:"started"`
}

// wireReportResponse mirrors ReportResponse with pointers so that a missing
// version or nodes list reads as absent, not as the zero value.
type wireReportResponse struct {
	Version     *int             `json:"version"`
	Nodes       *[]ReportNodeOut `json:"nodes"`
	Preparing   *Preparing       `json:"preparing"`
	Denied      *Denied          `json:"denied"`
	ReportEvery *string          `json:"report_every"`
}

// fingerprintPattern is the pinned worker-certificate fingerprint: "sha256:"
// then 64 lower-case hex characters. It must agree with FingerprintOf in
// internal/remote/client, which writes this shape from the DER hash.
var fingerprintPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// ValidateRequest checks a report request against the provider's own decode
// rules, so a request the head builds is refused here with the same field
// path `burst report` would name rather than over the wire. It returns the
// request, the paths whose value was not recognised and was normalised to ""
// (the caller logs them; they are never an error), and an error only for a
// document the contract itself refuses.
func ValidateRequest(data []byte) (ReportRequest, []string, error) {
	var wire wireReportRequest
	if err := json.Unmarshal(data, &wire); err != nil {
		return ReportRequest{}, nil, badf("%v", err)
	}
	if err := checkRequestShape(&wire); err != nil {
		return ReportRequest{}, nil, err
	}

	var notes []string
	req := ReportRequest{
		Version: *wire.Version,
		At:      wire.At.UTC(),
		Head:    ReportHead{Name: *wire.Head.Name, CPUs: wire.Head.CPUs, MemGB: wire.Head.MemGB},
		Local:   ReportLocal{Running: wire.Local.Running, Queued: wire.Local.Queued, Cap: wire.Local.Cap},
	}
	if wire.Reason != nil && *wire.Reason != "" {
		if isReportReason(*wire.Reason) {
			req.Reason = *wire.Reason
		} else {
			notes = append(notes, "reason")
		}
	}

	waiting, err := decodeWaiting(*wire.Waiting)
	if err != nil {
		return ReportRequest{}, nil, err
	}
	req.Waiting = waiting
	nodes, nodeNotes, err := decodeNodes(*wire.Nodes)
	if err != nil {
		return ReportRequest{}, nil, err
	}
	req.Nodes = nodes
	return req, append(notes, nodeNotes...), nil
}

// decodeWaiting reads the waiting list: each round is addressed by an id and
// timed by a since.
func decodeWaiting(ws []wireWaiting) ([]ReportWaiting, error) {
	out := make([]ReportWaiting, 0, len(ws))
	for i, w := range ws {
		if w.ID == nil || *w.ID == "" {
			return nil, badf("waiting[%d].id: required", i)
		}
		if w.Since == nil {
			return nil, badf("waiting[%d].since: required", i)
		}
		out = append(out, ReportWaiting{
			ID: *w.ID, Since: w.Since.UTC(), Candidate: w.Candidate, Harness: w.Harness,
		})
	}
	return out, nil
}

// decodeNodes reads the node list as the head saw it. An unrecognised
// problem is normalised to "" and returned as a note naming the path, never
// an error: a bad word about one node must not discard the load facts of
// every node in the same report.
func decodeNodes(ns []wireNode) ([]ReportNode, []string, error) {
	var notes []string
	out := make([]ReportNode, 0, len(ns))
	for i, n := range ns {
		if n.ID == nil || *n.ID == "" {
			return nil, nil, badf("nodes[%d].id: required", i)
		}
		for _, c := range []struct {
			name  string
			value int
		}{
			{"running", n.Running},
			{"queued", n.Queued},
			{"unacked", n.Unacked},
		} {
			if c.value < 0 {
				return nil, nil, badf("nodes[%d].%s: must be >= 0, got %d", i, c.name, c.value)
			}
		}
		node := ReportNode{
			ID: *n.ID, Running: n.Running, Queued: n.Queued, Unacked: n.Unacked,
			ForwardedAt: n.ForwardedAt,
			Rounds:      make([]ReportRound, 0, len(n.Rounds)),
		}
		if n.ForwardedAt != nil {
			at := n.ForwardedAt.UTC()
			node.ForwardedAt = &at
		}
		for _, rd := range n.Rounds {
			node.Rounds = append(node.Rounds, ReportRound{ID: rd.ID, Candidate: rd.Candidate, Started: rd.Started.UTC()})
		}
		if n.Problem != nil && *n.Problem != "" {
			if isReportProblem(*n.Problem) {
				node.Problem = *n.Problem
			} else {
				notes = append(notes, fmt.Sprintf("nodes[%d].problem", i))
			}
		}
		out = append(out, node)
	}
	return out, notes, nil
}

// checkRequestShape refuses a request missing or carrying an impossible value
// for any required field, in the order the provider names them.
func checkRequestShape(w *wireReportRequest) error {
	if w.Version == nil {
		return badf("version: required")
	}
	if *w.Version != ReportVersion {
		return badf("version: must be %d, got %d", ReportVersion, *w.Version)
	}
	if w.At == nil {
		return badf("at: required")
	}
	if w.Head == nil {
		return badf("head: required")
	}
	if w.Head.Name == nil || *w.Head.Name == "" {
		return badf("head.name: required")
	}
	if w.Head.CPUs < 0 {
		return badf("head.cpus: must be >= 0, got %d", w.Head.CPUs)
	}
	if w.Head.MemGB < 0 {
		return badf("head.mem_gb: must be >= 0, got %d", w.Head.MemGB)
	}
	if w.Local == nil {
		return badf("local: required")
	}
	for _, c := range []struct {
		name  string
		value int
	}{
		{"local.running", w.Local.Running},
		{"local.queued", w.Local.Queued},
		{"local.cap", w.Local.Cap},
	} {
		if c.value < 0 {
			return badf("%s: must be >= 0, got %d", c.name, c.value)
		}
	}
	if w.Waiting == nil {
		return badf("waiting: required")
	}
	if w.Nodes == nil {
		return badf("nodes: required")
	}
	return nil
}

// ValidateResponse checks burst's reply. A reply that does not fit is refused
// whole, so the head keeps its last good node list rather than acting on a
// half-understood one; an unrecognised word in a closed list (a denied
// reason) or an unreadable report_every is normalised and returned as a note
// the caller logs, never a refusal.
func ValidateResponse(data []byte) (ReportResponse, []string, error) {
	var wire wireReportResponse
	if err := json.Unmarshal(data, &wire); err != nil {
		return ReportResponse{}, nil, badf("%v", err)
	}
	if wire.Version == nil {
		return ReportResponse{}, nil, badf("version: required")
	}
	if *wire.Version != ReportVersion {
		return ReportResponse{}, nil, badf("version: must be %d, got %d", ReportVersion, *wire.Version)
	}
	if wire.Nodes == nil {
		return ReportResponse{}, nil, badf("nodes: required")
	}
	nodes, err := decodeNodesOut(*wire.Nodes)
	if err != nil {
		return ReportResponse{}, nil, err
	}

	var notes []string
	out := ReportResponse{Version: *wire.Version, Nodes: nodes, Preparing: wire.Preparing, Denied: wire.Denied}
	if wire.ReportEvery != nil {
		out.ReportEvery = *wire.ReportEvery
		if out.ReportEvery != "" {
			if _, perr := time.ParseDuration(out.ReportEvery); perr != nil {
				notes = append(notes, "report_every")
			}
		}
	}
	if out.Preparing != nil {
		preparing := *out.Preparing
		preparing.Since = preparing.Since.UTC()
		out.Preparing = &preparing
	}
	if out.Denied != nil && out.Denied.Reason != "" && !isDeniedReason(out.Denied.Reason) {
		denied := *out.Denied
		denied.Reason = ""
		out.Denied = &denied
		notes = append(notes, "denied.reason")
	}
	return out, notes, nil
}

// decodeNodesOut checks each response node and the list's ids together: a
// node the head cannot address, pin, bound seats by, or read the stop sign
// from is worse than no list at all, and a duplicate id would make two seats
// of one node.
func decodeNodesOut(in []ReportNodeOut) ([]ReportNodeOut, error) {
	seen := make(map[string]bool, len(in))
	for i, n := range in {
		for _, c := range []struct {
			name  string
			value string
		}{
			{"id", n.ID},
			{"name", n.Name},
			{"url", n.URL},
			{"fingerprint", n.Fingerprint},
		} {
			if c.value == "" {
				return nil, badf("nodes[%d].%s: required", i, c.name)
			}
		}
		if !isHTTPS(n.URL) {
			return nil, badf("nodes[%d].url: must be an https URL, got %q", i, n.URL)
		}
		if !fingerprintPattern.MatchString(n.Fingerprint) {
			return nil, badf("nodes[%d].fingerprint: must be sha256:<64 hex>, got %q", i, n.Fingerprint)
		}
		if n.Cap < 1 {
			return nil, badf("nodes[%d].cap: must be >= 1, got %d", i, n.Cap)
		}
		// draining is the head's only stop sign: it keeps sending rounds to
		// every node that reads ready, whatever else burst plans for it.
		switch n.State {
		case "ready", "draining":
		default:
			return nil, badf("nodes[%d].state: unknown %q (known: ready, draining)", i, n.State)
		}
		if seen[n.ID] {
			return nil, badf("nodes[%d].id: duplicate %q", i, n.ID)
		}
		seen[n.ID] = true
	}
	out := make([]ReportNodeOut, len(in))
	copy(out, in)
	return out, nil
}

// ParseReportEvery turns the response's report_every string into the
// interval to schedule on: an absent or unreadable value reads as
// DefaultReportEvery and any other value is clamped into
// [MinReportEvery, MaxReportEvery].
func ParseReportEvery(s string) time.Duration {
	d, err := time.ParseDuration(s)
	if err != nil {
		return DefaultReportEvery
	}
	if d < MinReportEvery {
		return MinReportEvery
	}
	if d > MaxReportEvery {
		return MaxReportEvery
	}
	return d
}

func isHTTPS(s string) bool {
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Hostname() != ""
}

func isReportReason(s string) bool {
	switch s {
	case ReportReasonInterval, ReportReasonForwarded, ReportReasonClosed, ReportReasonStartup:
		return true
	}
	return false
}

func isReportProblem(s string) bool {
	switch s {
	case ReportProblemUnreachable, ReportProblemFingerprint, ReportProblemKey:
		return true
	}
	return false
}

func isDeniedReason(s string) bool {
	switch s {
	case "budget", "cap", "no_capacity", "failing":
		return true
	}
	return false
}

func badf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrBadInput, fmt.Sprintf(format, args...))
}
