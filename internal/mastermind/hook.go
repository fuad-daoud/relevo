package mastermind

import (
	"crypto/rand"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/fuad-daoud/relevo/internal/db"
)

// HookInput is the Claude Code SessionStart payload read from stdin. Unknown
// fields are ignored.
type HookInput struct {
	HookEventName  string `json:"hook_event_name"`
	Source         string `json:"source"`
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	CWD            string `json:"cwd"`
}

// The SessionStart sources Claude Code reports; any other value reads as
// startup.
const (
	SourceStartup = "startup"
	SourceResume  = "resume"
	SourceClear   = "clear"
	SourceCompact = "compact"
)

// ParseHookInput decodes one SessionStart payload. session_id and cwd are
// required, and an unknown source reads as "startup".
func ParseHookInput(r io.Reader) (HookInput, error) {
	var in HookInput
	if err := json.NewDecoder(r).Decode(&in); err != nil {
		return HookInput{}, fmt.Errorf("mastermind: read hook payload: %w", err)
	}
	if in.SessionID == "" {
		return HookInput{}, fmt.Errorf("mastermind: hook payload: session_id is required: %w", ErrInvalid)
	}
	if in.CWD == "" {
		return HookInput{}, fmt.Errorf("mastermind: hook payload: cwd is required: %w", ErrInvalid)
	}

	switch in.Source {
	case SourceStartup, SourceResume, SourceClear, SourceCompact:
	default:
		in.Source = SourceStartup
	}
	return in, nil
}

// hookSpecificOutput is the envelope both hook answers use.
type hookSpecificOutput struct {
	HookEventName     string `json:"hookEventName"`
	AdditionalContext string `json:"additionalContext"`
}

type hookEnvelope struct {
	HookSpecificOutput hookSpecificOutput `json:"hookSpecificOutput"`
}

// The hook events relevo answers: Claude Code's session open, and the
// user-prompt event that carries a mid-session status notice.
const (
	HookEventSessionStart     = "SessionStart"
	HookEventUserPromptSubmit = "UserPromptSubmit"
)

// hookContext is the sentence naming the MasterMind, carried by both answers.
func hookContext(r Record) string {
	return fmt.Sprintf(
		"You are relevo MasterMind %s (%s). RELEVO_MASTERMIND is set in your shell; pass --mastermind %s only to act as another MasterMind.",
		r.Name, r.ID, r.Name)
}

const noEnvNote = "RELEVO_MASTERMIND could not be exported ($CLAUDE_ENV_FILE is unset); relevo resolves this session through its host process."

//go:embed guide.md
var guide string

// Guide is the shared guide text: the hook injects it, and internal/mcp
// appends it to both mode texts.
func Guide() string { return guide }

// HookOutput is what `relevo mastermind init --hook claude` prints on success.
func HookOutput(r Record) []byte {
	return encodeHookContext(HookEventSessionStart, ConsentText(ConsentYes, &r))
}

// HookOutputNoEnv is HookOutput plus the export-failure note.
func HookOutputNoEnv(r Record) []byte {
	return encodeHookContext(HookEventSessionStart, NoEnvText(r))
}

// NoEnvText is HookOutputNoEnv's text without its envelope: the identity
// sentence, the export-failure note, and the guide. A UserPromptSubmit hook
// cannot rely on $CLAUDE_ENV_FILE, so its grant notice uses this wording.
func NoEnvText(r Record) string {
	return hookContext(r) + " " + noEnvNote + "\n\n" + Guide()
}

// HookNote is the same envelope carrying a failure note, so a failed init
// never blocks the session.
func HookNote(msg string) []byte { return encodeHookContext(HookEventSessionStart, msg) }

func encodeHookContext(event, context string) []byte {
	raw, err := json.Marshal(hookEnvelope{HookSpecificOutput: hookSpecificOutput{
		HookEventName:     event,
		AdditionalContext: context,
	}})
	if err != nil {
		return []byte("{}\n")
	}
	return append(raw, '\n')
}

// EnvLine is the line `init --hook` appends to $CLAUDE_ENV_FILE.
func EnvLine(id string) string { return "export RELEVO_MASTERMIND=" + id + "\n" }

// InitResult is which outcome Init reached.
type InitResult string

const (
	InitCreated    InitResult = "created"
	InitReattached InitResult = "reattached"
	InitMoved      InitResult = "moved"
)

// InitInput is everything `relevo mastermind init` knows when it registers.
type InitInput struct {
	Kind           string
	SessionID      string
	TranscriptPath string
	CWD            string
	// Name is --name. Non-empty on an existing record renames it; empty
	// takes DefaultName.
	Name          string
	Agent         string
	HostPID       int
	HostStartedAt int64
	// Now stamps created_at and seen_at.
	Now time.Time
	// PriorID reuses a database row's id for (kind, session) when relevo.db
	// already has one; a false return mints a fresh id.
	PriorID func(kind, session string) (string, bool)
}

// Init registers, re-attaches or moves a record, inside one registry
// transaction so two concurrent hook firings serialise.
func Init(reg Registry, in InitInput) (Record, InitResult, error) {
	if dr, ok := reg.(*DBRegistry); ok {
		if err := dr.ensureImported(); err != nil {
			return Record{}, "", err
		}
		var (
			rec Record
			res InitResult
		)
		err := dr.KV.Tx(func(tx db.KVTx) error {
			var e error
			rec, res, e = initLocked(dr.ops(tx), in)
			return e
		})
		return rec, res, err
	}
	return initLocked(regAdapter{reg}, in)
}

// regOps is the transaction-free surface initLocked drives.
type regOps interface {
	byName(name string) (Record, error)
	byHost(pid int, startedAt int64) (Record, error)
	bySession(kind, sessionID string) (Record, error)
	create(r Record) (Record, error)
	moveSession(id, sessionID, transcript string, now time.Time) (Record, error)
	setHost(id string, pid int, startedAt int64) (Record, error)
	rename(id, name string) (Record, error)
	touchForced(id string, now time.Time) (Record, error)
}

// regAdapter runs Init against an arbitrary Registry.
type regAdapter struct{ Registry }

func (a regAdapter) byName(name string) (Record, error)     { return a.ByName(name) }
func (a regAdapter) bySession(k, s string) (Record, error)  { return a.BySession(k, s) }
func (a regAdapter) create(r Record) (Record, error)        { return a.Create(r) }
func (a regAdapter) rename(id, name string) (Record, error) { return a.Rename(id, name) }

func (a regAdapter) byHost(pid int, startedAt int64) (Record, error) {
	return a.ByHost(pid, startedAt)
}

func (a regAdapter) moveSession(id, sessionID, transcript string, now time.Time) (Record, error) {
	return a.MoveSession(id, sessionID, transcript, now)
}

func (a regAdapter) setHost(id string, pid int, startedAt int64) (Record, error) {
	return a.SetHost(id, pid, startedAt)
}

func (a regAdapter) touchForced(id string, now time.Time) (Record, error) {
	rec, err := a.Get(id)
	if err != nil {
		return Record{}, err
	}
	if err := a.Touch(id, now); err != nil {
		return Record{}, err
	}
	rec.SeenAt = now
	return rec, nil
}

func initLocked(reg regOps, in InitInput) (Record, InitResult, error) {
	rec, found, err := findCaller(reg, in)
	if err != nil {
		return Record{}, "", err
	}
	if found {
		return reattach(reg, rec, in)
	}
	return register(reg, in)
}

// findCaller looks by host first (stable across /clear and a rename), then
// by session (covers `--resume` in a new process).
func findCaller(reg regOps, in InitInput) (Record, bool, error) {
	if in.HostPID > 0 {
		rec, err := reg.byHost(in.HostPID, in.HostStartedAt)
		switch {
		case err == nil:
			return rec, true, nil
		case errors.Is(err, ErrNotFound):
		default:
			return Record{}, false, fmt.Errorf("mastermind: init: by host: %w", err)
		}
	}

	rec, err := reg.bySession(in.Kind, in.SessionID)
	switch {
	case err == nil:
		return rec, true, nil
	case errors.Is(err, ErrNotFound):
		return Record{}, false, nil
	default:
		return Record{}, false, fmt.Errorf("mastermind: init: by session: %w", err)
	}
}

// reattach renames when asked, moves the session when it changed, then
// points the record at the calling process.
func reattach(reg regOps, rec Record, in InitInput) (Record, InitResult, error) {
	res := InitReattached

	if in.Name != "" && in.Name != rec.Name {
		updated, err := reg.rename(rec.ID, in.Name)
		if err != nil {
			return Record{}, "", err
		}
		rec = updated
	}

	if rec.SessionID != in.SessionID {
		updated, err := reg.moveSession(rec.ID, in.SessionID, in.TranscriptPath, in.Now)
		if err != nil {
			return Record{}, "", err
		}
		rec = updated
		res = InitMoved
	}

	// HostPID 0 (explicit registration) never clears an existing host.
	if in.HostPID > 0 && rec.HostPID != in.HostPID {
		updated, err := reg.setHost(rec.ID, in.HostPID, in.HostStartedAt)
		if err != nil {
			return Record{}, "", err
		}
		rec = updated
	}

	return stampSeen(reg, rec, res, in)
}

func register(reg regOps, in InitInput) (Record, InitResult, error) {
	id := ""
	if in.PriorID != nil {
		if prior, ok := in.PriorID(in.Kind, in.SessionID); ok {
			id = prior
		}
	}
	if id == "" {
		minted, err := NewID(rand.Reader)
		if err != nil {
			return Record{}, "", err
		}
		id = minted
	}

	name := in.Name
	if name == "" {
		name = DefaultName(in.Agent, in.Kind, func(candidate string) bool {
			_, err := reg.byName(candidate)
			return err == nil
		})
	}

	created, err := reg.create(Record{
		ID:                id,
		Name:              name,
		HarnessKind:       in.Kind,
		SessionID:         in.SessionID,
		HostPID:           in.HostPID,
		HostStartedAt:     in.HostStartedAt,
		CWD:               in.CWD,
		TranscriptLocator: in.TranscriptPath,
		CreatedAt:         in.Now,
		SeenAt:            in.Now,
	})
	if err != nil {
		return Record{}, "", err
	}
	return stampSeen(reg, created, InitCreated, in)
}

// stampSeen sets seen_at to now, without the once-a-minute throttle Touch
// applies.
func stampSeen(reg regOps, rec Record, res InitResult, in InitInput) (Record, InitResult, error) {
	if in.Now.IsZero() {
		return rec, res, nil
	}
	updated, err := reg.touchForced(rec.ID, in.Now)
	if err != nil {
		return Record{}, "", err
	}
	return updated, res, nil
}
