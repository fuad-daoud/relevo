package store

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
	"github.com/fuad-daoud/relevo/internal/usage"
)

// maxLogEntries is a corruption guard, not a rotation policy: a write that
// would take the log past it is refused rather than silently truncating and
// hiding the newest entries.
const maxLogEntries = 10000

// Direction is which way a message travelled.
type Direction string

const (
	DirToBuilder Direction = "to_runner"
	// DirToMasterMind keeps the historical "to_planner" value: log directions
	// are state already written.
	DirToMasterMind Direction = "to_planner"

	// DirToConsult is additive: folding it into DirToBuilder would redefine a
	// persisted value.
	DirToConsult Direction = "to_consult"
)

// Kind is what sort of message it was. The values are persisted.
type Kind string

const (
	KindPrompt   Kind = "prompt"
	KindReport   Kind = "report"
	KindQuestion Kind = "question"
	KindAnswer   Kind = "answer"
	KindDiff     Kind = "diff"
	KindDrift    Kind = "drift"
	KindFork     Kind = "fork"
	KindPick     Kind = "pick"
	KindSwitch   Kind = "switch"
	KindExit     Kind = "exit"
	KindGate     Kind = "gate"
	KindPause    Kind = "pause"
	KindResume   Kind = "resume"
	KindStop     Kind = "stop"
	KindAsk      Kind = "ask"
	KindFindings Kind = "findings"
	KindLand     Kind = "land"
	KindEdge     Kind = "edge"
	KindQueue    Kind = "queue"
	KindRetired  Kind = "retired"

	// KindChain is a chain's one end delivery: the payload the mastermind
	// reads when a chain finishes, halts or stops. It is additive, like
	// DirToConsult: nothing enumerates the set exhaustively.
	KindChain Kind = "chain"
)

// kindPlanLegacy is the kind a round's prompt carried before the rename: it is
// state already written, matched through IsPromptKind and never written again.
const kindPlanLegacy Kind = "plan"

// IsPromptKind reports whether k is a round's prompt in either spelling: the
// value new entries write and the value already written.
func IsPromptKind(k Kind) bool {
	return k == KindPrompt || k == kindPlanLegacy
}

// LogEntry is one relayed message. An unconfirmed DirToMasterMind entry is also
// relevo's pending-delivery record, which makes a crash mid-delivery
// recoverable without a second file.
type LogEntry struct {
	// Seq is the 1-based position in the log; appendLog overwrites whatever a
	// caller passed.
	Seq         int        `json:"seq,omitempty"`
	TS          time.Time  `json:"ts"`
	Round       int        `json:"round"`
	Direction   Direction  `json:"direction"`
	Kind        Kind       `json:"kind"`
	Path        string     `json:"path,omitempty"`
	Payload     string     `json:"payload,omitempty"`
	DeliveredAt *time.Time `json:"delivered_at,omitempty"`
	Confirmed   bool       `json:"confirmed"`
	// Route is empty while the entry is still pending.
	Route string `json:"route,omitempty"`
	Note  string `json:"note,omitempty"`
	Late  bool   `json:"late,omitempty"`
	Tier  string `json:"tier,omitempty"`

	// Gate is nil when the binding has no gate.
	Gate *GateRecord `json:"gate,omitempty"`

	// Tree == "" means the commit facts are unknown and Commits is meaningless.
	Commits int    `json:"commits,omitempty"`
	Tree    string `json:"tree,omitempty"`

	// Usage nil is unknown, not free.
	Usage *usage.Usage `json:"usage,omitempty"`
	// PriorTokens is on a report entry that closed a remote round.
	PriorTokens *usage.Tokens `json:"prior_tokens,omitempty"`

	// Rusage is nil when the round was not a systemd scope.
	Rusage *Rusage `json:"rusage,omitempty"`

	// Outcome is parsed from the builder's trailing relevo block, on report
	// entries only; an empty one means the entry predates the field.
	Outcome      string   `json:"outcome,omitempty"`
	HaltedAt     string   `json:"halted_at,omitempty"`
	ChangedPaths []string `json:"changed_paths,omitempty"`
	CommandsRun  []string `json:"commands_run,omitempty"`
	NotDone      []string `json:"not_done,omitempty"`

	// Verdict is empty on every entry but a verify consult's findings with a
	// readable block.
	Verdict string   `json:"verdict,omitempty"`
	Reasons []string `json:"reasons,omitempty"`

	// Flagged counts instruction-shaped lines in the report or question body;
	// FlaggedBy says which judge produced it.
	Flagged   int    `json:"flagged,omitempty"`
	FlaggedBy string `json:"flagged_by,omitempty"`

	// Classify is nil when no classifier was configured. Note set means the
	// classifier did not answer and the counts are zero.
	Classify *ClassifyRecord `json:"classify,omitempty"`

	// BuilderSession is nil when the round's stream named none -- never
	// guessed.
	BuilderSession *BuilderSession `json:"runner_session,omitempty"`
}

// logEntryAlias is LogEntry without its methods, for the ordinary decode.
type logEntryAlias LogEntry

// UnmarshalJSON reads a log entry written by this relevo or by one before the
// A4 rename: the old direction value "to_builder" maps to DirToBuilder, and a
// legacy "builder_session" key maps to BuilderSession. The new "runner_session"
// key wins when both are present.
func (e *LogEntry) UnmarshalJSON(raw []byte) error {
	var alias logEntryAlias
	if err := json.Unmarshal(raw, &alias); err != nil {
		return err
	}
	out := LogEntry(alias)
	if out.Direction == "to_builder" {
		out.Direction = DirToBuilder
	}
	if out.BuilderSession == nil {
		var old struct {
			BuilderSession *BuilderSession `json:"builder_session"`
		}
		if err := json.Unmarshal(raw, &old); err != nil {
			return err
		}
		out.BuilderSession = old.BuilderSession
	}
	*e = out
	return nil
}

// BuilderSession is the harness session a closed round's report names the
// builder by.
type BuilderSession struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// AbandonedSession is a harness session relevo stopped using while its round
// was still open. The harness would otherwise resume it on its own, so the
// daemon deletes it. Attempts counts failed delete attempts, so a delete that
// keeps failing is dropped rather than retried forever.
type AbandonedSession struct {
	Kind     string `json:"kind"`
	ID       string `json:"id"`
	Attempts int    `json:"attempts,omitempty"`
}

type ClassifyRecord struct {
	Provider    string  `json:"provider"`
	Model       string  `json:"model,omitempty"`
	Threshold   float64 `json:"threshold"`
	Paragraphs  int     `json:"paragraphs"`
	Partial     bool    `json:"partial,omitempty"`
	Above       int     `json:"above"`
	Max         float64 `json:"injection_max"`
	InputTokens int     `json:"input_tokens,omitempty"`
	Note        string  `json:"note,omitempty"`
}

// GateRecord is the acceptance check's result, on report entries of a gated
// round.
type GateRecord struct {
	Command    string `json:"command"`
	Result     string `json:"result"`
	ExitCode   int    `json:"exit_code"`
	DurationMS int64  `json:"duration_ms"`
	LogPath    string `json:"log_path"`
	Note       string `json:"note,omitempty"`
}

func (s *Store) AppendLog(name string, e LogEntry) error {
	return s.WithLock(func(tx *Tx) error { return tx.AppendLog(name, e) })
}

func (s *Store) ReadLog(name string) ([]LogEntry, error) {
	var entries []LogEntry
	err := s.read(name, func(tx *Tx) error {
		var err error
		entries, err = tx.ReadLog(name)
		return err
	})
	return entries, err
}

func (s *Store) ReadLogAfter(name string, after int) ([]LogEntry, error) {
	var entries []LogEntry
	err := s.read(name, func(tx *Tx) error {
		var err error
		entries, err = tx.ReadLogAfter(name, after)
		return err
	})
	return entries, err
}

// PendingEntry is one undelivered mastermind payload with its index in the log.
// confirmIndex marks an entry in place, so the index stays valid while several
// are confirmed under one lock.
type PendingEntry struct {
	Entry LogEntry
	Idx   int
}

func (s *Store) PendingForMasterMind(name string) (LogEntry, bool, error) {
	var e LogEntry
	var found bool
	err := s.read(name, func(tx *Tx) error {
		var err error
		e, _, found, err = tx.PendingForMasterMind(name)
		return err
	})
	return e, found, err
}

func (s *Store) ConfirmIndex(name string, idx int, route string) error {
	return s.WithLock(func(tx *Tx) error { return tx.ConfirmIndex(name, idx, route) })
}

func (t *Tx) AppendLog(name string, e LogEntry) error {
	return t.s.appendLog(name, e)
}

func (t *Tx) ReadLog(name string) ([]LogEntry, error) {
	return t.s.readLog(name)
}

func (t *Tx) ReadLogAfter(name string, after int) ([]LogEntry, error) {
	return t.s.readLogAfter(name, after)
}

// PendingForMasterMind reads the oldest undelivered mastermind payload and its index
// under the held lock.
func (t *Tx) PendingForMasterMind(name string) (LogEntry, int, bool, error) {
	return t.s.pendingForMasterMind(name)
}

// PendingForMasterMindThrough reads every undelivered mastermind payload whose round
// is at most round -- every round when round <= 0 -- in log order.
func (t *Tx) PendingForMasterMindThrough(name string, round int) ([]PendingEntry, error) {
	return t.s.pendingForMasterMindThrough(name, round)
}

func (t *Tx) ConfirmIndex(name string, idx int, route string) error {
	return t.s.confirmIndex(name, idx, route)
}

// The unexported methods below assume the lock is already held via Tx.

// appendLog stamps TS when the caller left it zero and assigns Seq from the
// binding's stored events.
func (s *Store) appendLog(name string, e LogEntry) error {
	if err := ValidName(name); err != nil {
		return err
	}
	d, err := s.dbForWrite()
	if err != nil {
		return err
	}
	rec, ok, err := d.RecordGet(s.owner, name)
	if err != nil {
		return fmt.Errorf("append log for %q: %w", name, err)
	}
	// The log belongs to a binding the store already knows, exactly as
	// log.jsonl was a file inside the binding's directory.
	if !ok {
		return fmt.Errorf("append log for %q: %w", name, ErrNotFound)
	}

	n, err := d.EventMaxSeq(rec.ID)
	if err != nil {
		return err
	}
	if n >= s.maxLog() {
		return fmt.Errorf("log exceeds %d entries", s.maxLog())
	}

	ev, err := encodeEvent(e, n+1)
	if err != nil {
		return err
	}

	if err := s.ensureBindingDir(name); err != nil {
		return fmt.Errorf("create binding dir: %w", err)
	}

	return d.EventAppend(rec.ID, ev)
}

// encodeEvent is shared by appendLog and SaveWithLog so both write identical
// rows.
func encodeEvent(e LogEntry, seq int) (db.RecordEvent, error) {
	if e.TS.IsZero() {
		e.TS = time.Now().UTC()
	}
	e.Seq = seq
	encoded, err := json.Marshal(e)
	if err != nil {
		return db.RecordEvent{}, fmt.Errorf("encode log entry: %w", err)
	}
	return recordEventOf(e, string(encoded)), nil
}

// readLog returns every entry in order; a binding with no record yields nil,
// nil, the result a missing log.jsonl gave before the database.
func (s *Store) readLog(name string) ([]LogEntry, error) {
	d, err := s.dbForRead()
	if err != nil {
		return nil, err
	}
	if d == nil {
		return nil, nil
	}
	rec, ok, err := d.RecordGet(s.owner, name)
	if err != nil {
		return nil, fmt.Errorf("read log for %q: %w", name, err)
	}
	if !ok {
		return nil, nil
	}
	events, err := d.EventsOf(rec.ID, 0)
	if err != nil {
		return nil, fmt.Errorf("read log for %q: %w", name, err)
	}
	return logEntriesOf(events)
}

// readLogAfter returns the entries whose Seq is greater than after; a log with
// no such entry yields nil, nil.
func (s *Store) readLogAfter(name string, after int) ([]LogEntry, error) {
	d, err := s.dbForRead()
	if err != nil {
		return nil, err
	}
	if d == nil {
		return nil, nil
	}
	rec, ok, err := d.RecordGet(s.owner, name)
	if err != nil {
		return nil, fmt.Errorf("read log for %q: %w", name, err)
	}
	if !ok {
		return nil, nil
	}
	events, err := d.EventsOf(rec.ID, after)
	if err != nil {
		return nil, fmt.Errorf("read log for %q: %w", name, err)
	}
	return logEntriesOf(events)
}

// pendingForMasterMind returns the OLDEST undelivered payload bound for the
// mastermind. Oldest-first because arrival order is the only order relevo can
// defend without judging content: a report queued before two consult findings
// must not be delivered after both of them.
func (s *Store) pendingForMasterMind(name string) (LogEntry, int, bool, error) {
	entries, err := s.readLog(name)
	if err != nil {
		return LogEntry{}, 0, false, err
	}

	for i, e := range entries {
		if e.Direction == DirToMasterMind && !e.Confirmed {
			return e, i, true, nil
		}
	}

	return LogEntry{}, 0, false, nil
}

// pendingForMasterMindThrough is pendingForMasterMind's through-round sibling.
func (s *Store) pendingForMasterMindThrough(name string, round int) ([]PendingEntry, error) {
	entries, err := s.readLog(name)
	if err != nil {
		return nil, err
	}

	var pending []PendingEntry
	for i, e := range entries {
		if e.Direction == DirToMasterMind && !e.Confirmed && (round <= 0 || e.Round <= round) {
			pending = append(pending, PendingEntry{Entry: e, Idx: i})
		}
	}

	return pending, nil
}

// confirmIndex marks the idx'th event in Seq order as delivered by route.
//
// It patches the entry's JSON through a map[string]json.RawMessage, so a key a
// newer relevo wrote survives this binary. It takes an index rather than
// re-deriving "the entry we must have meant", so callers holding the lock
// across pendingForMasterMind and this call confirm exactly the entry they read.
func (s *Store) confirmIndex(name string, idx int, route string) error {
	// This write does not pass through read, and the name still becomes a path
	// in the sibling helpers, so it takes the same first-line refusal.
	if err := ValidName(name); err != nil {
		return err
	}
	d, err := s.dbForWrite()
	if err != nil {
		return err
	}
	rec, ok, err := d.RecordGet(s.owner, name)
	if err != nil {
		return fmt.Errorf("read log for %q: %w", name, err)
	}
	var events []db.RecordEvent
	if ok {
		if events, err = d.EventsOf(rec.ID, 0); err != nil {
			return fmt.Errorf("read log for %q: %w", name, err)
		}
	}
	if idx < 0 || idx >= len(events) {
		return fmt.Errorf("confirm entry %d for %q: log has %d entries", idx, name, len(events))
	}
	ev := events[idx]
	if ev.Confirmed {
		return nil
	}

	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(ev.JSON), &m); err != nil {
		return fmt.Errorf("decode log entry %d for %q: %w", idx, name, err)
	}

	now := time.Now().UTC()
	delivered, err := json.Marshal(now)
	if err != nil {
		return fmt.Errorf("encode delivered_at: %w", err)
	}
	m["confirmed"] = json.RawMessage("true")
	m["delivered_at"] = delivered
	if route != "" {
		encodedRoute, err := json.Marshal(route)
		if err != nil {
			return fmt.Errorf("encode route: %w", err)
		}
		m["route"] = encodedRoute
	}

	// Seq is written through so a reader of the JSON alone still sees it.
	encodedSeq, err := json.Marshal(ev.Seq)
	if err != nil {
		return fmt.Errorf("encode seq: %w", err)
	}
	m["seq"] = encodedSeq

	patched, err := json.Marshal(m)
	if err != nil {
		return fmt.Errorf("encode log entry: %w", err)
	}

	return d.EventConfirm(rec.ID, ev.Seq, now, route, string(patched))
}
