package setup

import (
	"errors"
	"io"

	"github.com/fuad-daoud/relevo/internal/mastermind"
	"github.com/fuad-daoud/relevo/internal/relevo"
)

// QuestionsCode is the exit a run with questions still open takes: the
// document the answer flow needs is already on stdout, so the report prints
// nothing on top of it.
const QuestionsCode = 3

// ErrQuestionsOpen is what the run returns when questions are still open.
// The questions document is already on stdout through Deps.PrintDoc; the
// caller exits it as QuestionsCode without printing again.
var ErrQuestionsOpen = errors.New("setup questions are still open")

// UsageError marks the caller's own arguments as the failure: an --answer
// value no question takes, or questions read against the wrong set. The
// caller exits it as usage.
type UsageError struct {
	Err error
}

func (e *UsageError) Error() string { return e.Err.Error() }
func (e *UsageError) Unwrap() error { return e.Err }

// InternalError is a failure before any row or document printed: a dead
// prompt, a probe the machine itself refuses. The caller reports it like any
// other internal failure.
type InternalError struct {
	Err error
}

func (e *InternalError) Error() string { return e.Err.Error() }
func (e *InternalError) Unwrap() error { return e.Err }

// StepError is a failed step as its result row carries it: the detail the
// row prints and the fix it names. Every step failure that reaches the
// caller is shaped this way, so the row the apply already printed is the
// whole report and the caller exits silently.
type StepError struct {
	Message string
	Next    string
}

func (e *StepError) Error() string { return e.Message }

// LimitError is the smoke round's usage-limit failover: no builder could run
// it, so the run fails naming the reset and the setup fix. It renders as a
// step row like any StepError; the caller exits it as gate_active.
type LimitError struct {
	Message string
	Next    string
}

func (e *LimitError) Error() string { return e.Message }

// CodedStepError is a step failure that already carries its exit: a smoke
// transport error the caller classified (the builder-busy family, refused
// instead of internal). The row prints Message with Next; the caller exits
// Code silently.
type CodedStepError struct {
	Message string
	Next    string
	Code    int
}

func (e *CodedStepError) Error() string { return e.Message }

// Deps carries the cmd-owned values the setup run reads: the install
// environment and helpers that stay in cmd/relevo because other commands use
// them, and the stdio the run prints through. Every field is a value or a
// plain function; the run never names cmd/relevo.
type Deps struct {
	Env SetupEnv
	// Refresh reloads the runtime after the config write, for the agents
	// step and the smoke round.
	Refresh func() (relevo.Runtime, error)
	// ActorSummary renders the written actors as the config step's detail.
	ActorSummary func(actors []byte, order []string) (string, error)
	// InstallService installs the service unit as `relevo service install`
	// does, shaped as a StepError on failure.
	InstallService func() error
	// MastermindRegistry is the registry the smoke round binds through.
	MastermindRegistry func(rt relevo.Runtime) (*mastermind.DBRegistry, error)
	// ClassifyTransport shapes a smoke-transport failure before it becomes a
	// row. Nil keeps the raw error, whose row truncates and exits internal.
	ClassifyTransport func(error) error
	// InSandbox is the env sandbox the run skips the host-owned installs in.
	InSandbox bool
	// UnitPresent is the installed service unit a rerun keeps.
	UnitPresent bool
	Stdin       io.Reader
	Notices     io.Writer
	// PrintDoc writes a result document to stdout as JSON.
	PrintDoc func(v any) error
}
