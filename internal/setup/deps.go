package setup

import (
	"errors"
	"io"

	"github.com/fuad-daoud/relevo/internal/mastermind"
	"github.com/fuad-daoud/relevo/internal/relevo"
)

// QuestionsCode is the exit a run with questions still open takes: the
// document the answer flow needs is already on stdout.
const QuestionsCode = 3

// ErrQuestionsOpen is what the run returns when questions are still open;
// the caller exits it as QuestionsCode without printing again.
var ErrQuestionsOpen = errors.New("setup questions are still open")

// UsageError marks the caller's own arguments as the failure.
type UsageError struct {
	Err error
}

func (e *UsageError) Error() string { return e.Err.Error() }
func (e *UsageError) Unwrap() error { return e.Err }

// InternalError is a failure before any row or document printed.
type InternalError struct {
	Err error
}

func (e *InternalError) Error() string { return e.Err.Error() }
func (e *InternalError) Unwrap() error { return e.Err }

// StepError is a failed step as its result row carries it, so the row the
// apply already printed is the whole report.
type StepError struct {
	Message string
	// Next is the fix it names.
	Next string
}

func (e *StepError) Error() string { return e.Message }

// LimitError is the smoke round's usage-limit failover: no builder could run
// it. It renders as a step row; the caller exits it as gate_active.
type LimitError struct {
	Message string
	// Next is the fix it names.
	Next string
}

func (e *LimitError) Error() string { return e.Message }

// CodedStepError is a step failure that already carries its exit. The row
// prints Message with Next; the caller exits Code silently.
type CodedStepError struct {
	Message string
	Next    string
	Code    int
}

func (e *CodedStepError) Error() string { return e.Message }

// Deps carries the cmd-owned values the setup run reads. Every field is a
// value or a plain function; the run never names cmd/relevo.
type Deps struct {
	Env SetupEnv
	// Refresh reloads the runtime after the config write.
	Refresh func() (relevo.Runtime, error)
	// ActorSummary renders the written actors as the config step's detail.
	ActorSummary func(actors []byte, order []string) (string, error)
	// InstallService installs the service unit, shaped as a StepError on failure.
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
